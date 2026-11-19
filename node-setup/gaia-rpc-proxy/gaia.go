package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	gaiaMaxRequest        = 64 << 10
	gaiaMaxResponse       = 16 << 20
	gaiaProofTTL          = 5 * time.Minute
	gaiaMaxHeadAge        = 2 * time.Minute
	gaiaFutureSkew        = 10 * time.Second
	gaiaArchiveRetryLimit = 4
)

var (
	errGaiaInvalid = errors.New("invalid GAIA RPC response")
	gaiaPruned     = regexp.MustCompile(`^height ([1-9][0-9]*) is not available, lowest height is ([1-9][0-9]*)$`)
)

// GaiaProxy has no TRON journal, health, or routing-mode dependencies.
type GaiaProxy struct {
	primary, archive                                             *url.URL
	client                                                       *http.Client
	freshClient                                                  *http.Client
	timeout                                                      time.Duration
	admission                                                    *requestAdmission
	now                                                          func() time.Time
	proofMu                                                      sync.Mutex
	proof                                                        gaiaProof
	checking                                                     chan struct{}
	logger                                                       *log.Logger
	requests, primaryReads, archiveReads, fallbacks, directReads atomic.Uint64
	invalid, upstreamErrors, proofChecks, proofFailures          atomic.Uint64
	archiveRetries, archiveRetryRecovered, archiveRetryExhausted atomic.Uint64
	head                                                         gaiaHeadCache
	headHits, headMisses                                         atomic.Uint64
	blockMu                                                      sync.Mutex
	blocks                                                       [128]gaiaBlockMetadata
}

type gaiaBlockMetadata struct {
	height int64
	txs    int
}

func (g *GaiaProxy) rememberBlock(height int64, txs int) {
	g.blockMu.Lock()
	g.blocks[height%int64(len(g.blocks))] = gaiaBlockMetadata{height: height, txs: txs}
	g.blockMu.Unlock()
}

func (g *GaiaProxy) blockCountMatches(height int64, txs int) bool {
	g.blockMu.Lock()
	block := g.blocks[height%int64(len(g.blocks))]
	g.blockMu.Unlock()
	return block.height != height || block.txs == txs
}

type gaiaProof struct {
	until         time.Time
	height, floor int64
	valid         bool
	generation    uint64
	failure       gaiaFailure
}

// These fields contain only local constants and a bounded HTTP status. Never
// retain or wrap an upstream error: net/http errors may contain credential URLs.
type gaiaFailure struct {
	stage, reason string
	httpStatus    int
}

func (f gaiaFailure) Error() string { return f.reason }

func gaiaFailed(reason string, status int) gaiaFailure {
	if status < 100 || status > 599 {
		status = 0
	}
	return gaiaFailure{reason: reason, httpStatus: status}
}

func gaiaAt(stage string, err error) gaiaFailure {
	f := gaiaFailed("invalid_response", 0)
	if err == nil {
		f.reason = "rpc_error"
	} else {
		var classified gaiaFailure
		if errors.As(err, &classified) {
			f = classified
		}
	}
	f.stage = stage
	return f
}

func gaiaContextFailure(ctx context.Context) gaiaFailure {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return gaiaFailed("deadline", 0)
	}
	return gaiaFailed("canceled", 0)
}

func gaiaProofFailure(p gaiaProof, stage string, err error) (gaiaProof, bool) {
	p.failure = gaiaAt(stage, err)
	return p, false
}

func (g *GaiaProxy) logFailure(q gaiaRequest, err error) {
	f := gaiaAt("request", err)
	var classified gaiaFailure
	if errors.As(err, &classified) && classified.stage != "" {
		f = classified
	}
	method := q.Method
	if method != "status" && method != "block" && method != "block_results" {
		method = "unknown"
	}
	logger := g.logger
	if logger == nil {
		logger = log.Default()
	}
	logger.Printf("gaia_read_failed method=%s height=%d stage=%s reason=%s http_status=%d", method, max(q.height, 0), f.stage, f.reason, f.httpStatus)
}

func newGaiaProxy(primary, archive *url.URL, timeout time.Duration) *GaiaProxy {
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	if timeout > 30*time.Second {
		timeout = 30 * time.Second
	}
	cloneURL := func(u *url.URL) *url.URL {
		if u == nil {
			return nil
		}
		v := *u
		return &v
	}
	g := &GaiaProxy{
		primary: cloneURL(primary), archive: cloneURL(archive), timeout: timeout, now: time.Now, logger: log.Default(),
		admission: &requestAdmission{active: make(chan struct{}, maxActiveRequests), waiting: make(chan struct{}, maxQueuedRequests), waitTimeout: min(timeout, requestAdmissionTimeout)},
		checking:  make(chan struct{}, 1),
		head:      gaiaHeadCache{gate: make(chan struct{}, 1)},
		client: &http.Client{Timeout: timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }, Transport: &http.Transport{
			Proxy: nil, DialContext: (&net.Dialer{Timeout: timeout, KeepAlive: 30 * time.Second}).DialContext,
			TLSHandshakeTimeout: timeout, ResponseHeaderTimeout: timeout, MaxConnsPerHost: maxActiveRequests,
			MaxIdleConns: 2 * maxActiveRequests, MaxIdleConnsPerHost: maxActiveRequests, IdleConnTimeout: 60 * time.Second,
		}},
	}
	freshTransport := g.client.Transport.(*http.Transport).Clone()
	freshTransport.DisableKeepAlives = true
	freshClient := *g.client
	freshClient.Transport = freshTransport
	g.freshClient = &freshClient
	return g
}

type gaiaRequest struct {
	Version string                     `json:"jsonrpc"`
	ID      json.RawMessage            `json:"id"`
	Method  string                     `json:"method"`
	Params  map[string]json.RawMessage `json:"params"`
	height  int64
}

func gaiaHeight(s string) (int64, bool) {
	if len(s) == 0 || s[0] < '1' || s[0] > '9' {
		return 0, false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, false
		}
	}
	h, err := strconv.ParseInt(s, 10, 64)
	return h, err == nil && h > 0
}

func gaiaID(raw json.RawMessage) (string, bool) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return "", false
	}
	if raw[0] == '"' {
		var s string
		err := json.Unmarshal(raw, &s)
		return "s:" + s, err == nil
	}
	n, err := strconv.ParseInt(string(raw), 10, 64)
	return "n:" + strconv.FormatInt(n, 10), err == nil
}

func gaiaParseRequest(r *http.Request, body []byte) (gaiaRequest, error) {
	q := gaiaRequest{Version: "2.0", ID: json.RawMessage(`-1`)}
	if r.Header.Get("Upgrade") != "" || strings.Contains(strings.ToLower(r.Header.Get("Connection")), "upgrade") || r.URL.EscapedPath() != r.URL.Path {
		return q, errGaiaInvalid
	}
	switch r.Method {
	case http.MethodPost:
		if (r.URL.Path != "/gaia" && r.URL.Path != "/gaia/") || r.URL.RawQuery != "" || uniqueJSON(body) != nil {
			return q, errGaiaInvalid
		}
		q = gaiaRequest{}
		d := json.NewDecoder(bytes.NewReader(body))
		d.DisallowUnknownFields()
		if d.Decode(&q) != nil {
			return q, errGaiaInvalid
		}
	case http.MethodGet:
		if len(body) != 0 {
			return q, errGaiaInvalid
		}
		switch r.URL.Path {
		case "/gaia/status":
			q.Method = "status"
		case "/gaia/block":
			q.Method = "block"
		case "/gaia/block_results":
			q.Method = "block_results"
		default:
			return q, errGaiaInvalid
		}
		values, err := url.ParseQuery(r.URL.RawQuery)
		if err != nil || len(values) > 1 {
			return q, errGaiaInvalid
		}
		for key, v := range values {
			if key != "height" || len(v) != 1 {
				return q, errGaiaInvalid
			}
			b, _ := json.Marshal(v[0])
			q.Params = map[string]json.RawMessage{"height": b}
		}
	default:
		return q, errGaiaInvalid
	}
	if q.Version != "2.0" {
		return q, errGaiaInvalid
	}
	if _, ok := gaiaID(q.ID); !ok {
		return q, errGaiaInvalid
	}
	if q.Method != "status" && q.Method != "block" && q.Method != "block_results" {
		return q, errGaiaInvalid
	}
	if len(q.Params) > 1 || q.Method == "status" && len(q.Params) != 0 {
		return q, errGaiaInvalid
	}
	for key, raw := range q.Params {
		if key != "height" {
			return q, errGaiaInvalid
		}
		if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			continue
		}
		var s string
		if json.Unmarshal(raw, &s) != nil {
			return q, errGaiaInvalid
		}
		var ok bool
		q.height, ok = gaiaHeight(s)
		if !ok {
			return q, errGaiaInvalid
		}
	}
	return q, nil
}

func gaiaReadRequest(method string, height int64) gaiaRequest {
	q := gaiaRequest{Version: "2.0", ID: json.RawMessage(`"gaia-check"`), Method: method}
	if height > 0 {
		b, _ := json.Marshal(strconv.FormatInt(height, 10))
		q.Params = map[string]json.RawMessage{"height": b}
		q.height = height
	}
	return q
}

type gaiaBlockID struct {
	Hash  string `json:"hash"`
	Parts struct {
		Total uint32 `json:"total"`
		Hash  string `json:"hash"`
	} `json:"parts"`
}

// Count arrays one element at a time; do not retain transaction payloads/events
// or materialize arbitrary response maps alongside the capped response bytes.
type gaiaTransactions struct {
	present bool
	count   int
}

func (v *gaiaTransactions) UnmarshalJSON(b []byte) error {
	v.present = true
	if bytes.Equal(b, []byte("null")) {
		return nil
	}
	d := json.NewDecoder(bytes.NewReader(b))
	token, err := d.Token()
	if err != nil || token != json.Delim('[') {
		return errGaiaInvalid
	}
	for d.More() {
		var tx *string
		if d.Decode(&tx) != nil || tx == nil {
			return errGaiaInvalid
		}
		v.count++
	}
	_, err = d.Token()
	return err
}

type gaiaTxResults struct {
	present bool
	count   int
}

func (v *gaiaTxResults) UnmarshalJSON(b []byte) error {
	v.present = true
	if bytes.Equal(b, []byte("null")) {
		return nil
	}
	d := json.NewDecoder(bytes.NewReader(b))
	token, err := d.Token()
	if err != nil || token != json.Delim('[') {
		return errGaiaInvalid
	}
	for d.More() {
		var tx struct {
			Code *uint32 `json:"code"`
		}
		if d.Decode(&tx) != nil || tx.Code == nil {
			return errGaiaInvalid
		}
		v.count++
	}
	_, err = d.Token()
	return err
}

type gaiaResult struct {
	NodeInfo *struct {
		Network string `json:"network"`
	} `json:"node_info"`
	SyncInfo *struct {
		Height     string    `json:"latest_block_height"`
		Earliest   string    `json:"earliest_block_height"`
		Time       time.Time `json:"latest_block_time"`
		CatchingUp *bool     `json:"catching_up"`
	} `json:"sync_info"`
	BlockID gaiaBlockID `json:"block_id"`
	Block   *struct {
		Header struct {
			Chain  string    `json:"chain_id"`
			Height string    `json:"height"`
			Time   time.Time `json:"time"`
		} `json:"header"`
		Data *struct {
			Txs gaiaTransactions `json:"txs"`
		} `json:"data"`
	} `json:"block"`
	Height    string        `json:"height"`
	TxResults gaiaTxResults `json:"txs_results"`
}

type gaiaRPCError struct {
	Code    *int   `json:"code"`
	Message string `json:"message"`
	Data    string `json:"data"`
}

type gaiaEnvelope struct {
	Version string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  *gaiaResult     `json:"result"`
	Error   *gaiaRPCError   `json:"error"`
}

func gaiaHash(s string) bool { b, err := hex.DecodeString(s); return err == nil && len(b) == 32 }

func gaiaValidate(q gaiaRequest, e *gaiaEnvelope) bool {
	id, ok := gaiaID(e.ID)
	want, _ := gaiaID(q.ID)
	if !ok || id != want || e.Version != "2.0" || (e.Result == nil) == (e.Error == nil) {
		return false
	}
	if e.Error != nil {
		return e.Error.Code != nil && e.Error.Message != ""
	}
	r := e.Result
	switch q.Method {
	case "status":
		if r.NodeInfo == nil || r.NodeInfo.Network != "cosmoshub-4" || r.SyncInfo == nil || r.SyncInfo.CatchingUp == nil || r.SyncInfo.Time.IsZero() {
			return false
		}
		_, ok := gaiaHeight(r.SyncInfo.Height)
		// Some providers use "0" to mean an unspecified retention boundary.
		if r.SyncInfo.Earliest != "" && r.SyncInfo.Earliest != "0" {
			_, earliestOK := gaiaHeight(r.SyncInfo.Earliest)
			ok = ok && earliestOK
		}
		return ok
	case "block":
		if r.Block == nil || r.Block.Header.Chain != "cosmoshub-4" || r.Block.Header.Time.IsZero() || r.Block.Data == nil || !r.Block.Data.Txs.present || !gaiaHash(r.BlockID.Hash) || r.BlockID.Parts.Total == 0 || !gaiaHash(r.BlockID.Parts.Hash) {
			return false
		}
		h, ok := gaiaHeight(r.Block.Header.Height)
		return ok && (q.height == 0 || h == q.height)
	case "block_results":
		h, ok := gaiaHeight(r.Height)
		return ok && (q.height == 0 || h == q.height) && r.TxResults.present
	}
	return false
}

func (g *GaiaProxy) call(ctx context.Context, endpoint *url.URL, q gaiaRequest) ([]byte, *gaiaEnvelope, error) {
	return g.callWithClient(ctx, g.client, endpoint, q)
}

func (g *GaiaProxy) callWithClient(ctx context.Context, client *http.Client, endpoint *url.URL, q gaiaRequest) ([]byte, *gaiaEnvelope, error) {
	if ctx.Err() != nil {
		return nil, nil, gaiaContextFailure(ctx)
	}
	if endpoint == nil {
		return nil, nil, gaiaFailed("endpoint", 0)
	}
	body, err := json.Marshal(q)
	if err != nil {
		return nil, nil, gaiaFailed("request_encode", 0)
	}
	r, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), bytes.NewReader(body))
	if err != nil {
		return nil, nil, gaiaFailed("request_build", 0)
	}
	r.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(r)
	if err != nil {
		if ctx.Err() != nil {
			return nil, nil, gaiaContextFailure(ctx)
		}
		return nil, nil, gaiaFailed("transport", 0)
	}
	defer resp.Body.Close()
	// Comet returns JSON-RPC failures with either 200 or 500, depending on server.
	if resp.StatusCode != 200 && resp.StatusCode != 500 {
		return nil, nil, gaiaFailed("http_status", resp.StatusCode)
	}
	if resp.ContentLength > gaiaMaxResponse {
		return nil, nil, gaiaFailed("body_size", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, gaiaMaxResponse+1))
	if ctx.Err() != nil {
		return nil, nil, gaiaContextFailure(ctx)
	}
	if err != nil {
		return nil, nil, gaiaFailed("body_read", resp.StatusCode)
	}
	if len(data) > gaiaMaxResponse {
		return nil, nil, gaiaFailed("body_size", resp.StatusCode)
	}
	if !json.Valid(data) {
		return nil, nil, gaiaFailed("decode", resp.StatusCode)
	}
	if uniqueJSON(data) != nil {
		return nil, nil, gaiaFailed("duplicate_json_or_depth", resp.StatusCode)
	}
	var envelope gaiaEnvelope
	if json.Unmarshal(data, &envelope) != nil {
		return nil, nil, gaiaFailed("decode", resp.StatusCode)
	}
	id, ok := gaiaID(envelope.ID)
	want, _ := gaiaID(q.ID)
	if !ok || id != want || envelope.Version != "2.0" {
		return nil, nil, gaiaFailed("response_identity", resp.StatusCode)
	}
	if !gaiaValidate(q, &envelope) || resp.StatusCode == 500 && envelope.Error == nil {
		return nil, nil, gaiaFailed("response_shape", resp.StatusCode)
	}
	if ctx.Err() != nil {
		return nil, nil, gaiaContextFailure(ctx)
	}
	return data, &envelope, nil
}

// Only Comet's exact, height-matched retention error supplies a floor. A generic
// internal error, future-height error, or an error for another height cannot.
func gaiaMissing(q gaiaRequest, e *gaiaEnvelope) int64 {
	if q.height <= 0 || (q.Method != "block" && q.Method != "block_results") || e == nil || e.Error == nil || e.Error.Code == nil || *e.Error.Code != -32603 {
		return 0
	}
	m := gaiaPruned.FindStringSubmatch(e.Error.Data)
	if len(m) != 3 {
		return 0
	}
	h, ok := gaiaHeight(m[1])
	floor, floorOK := gaiaHeight(m[2])
	if !ok || !floorOK || h != q.height || floor <= h {
		return 0
	}
	return floor
}

func (g *GaiaProxy) cachedProof() gaiaProof {
	g.proofMu.Lock()
	defer g.proofMu.Unlock()
	return g.proof
}
func (g *GaiaProxy) fresh(p gaiaProof) bool { return p.valid && g.now().Before(p.until) }

func (g *GaiaProxy) agreement(ctx context.Context, hint int64) (gaiaProof, bool) {
	before := g.cachedProof()
	if g.fresh(before) && (hint == 0 || before.floor >= hint) {
		return before, true
	}
	select {
	case g.checking <- struct{}{}:
		defer func() { <-g.checking }()
	case <-ctx.Done():
		return gaiaProofFailure(gaiaProof{}, "agreement_wait", gaiaContextFailure(ctx))
	}
	if ctx.Err() != nil {
		return gaiaProofFailure(gaiaProof{}, "agreement_wait", gaiaContextFailure(ctx))
	}
	current := g.cachedProof()
	if g.now().Before(current.until) && (current.generation != before.generation || !current.valid || hint == 0 || current.floor >= hint) {
		return current, current.valid
	}
	g.proofChecks.Add(1)
	proof, ok := g.checkAgreement(ctx, hint)
	proof.generation = current.generation + 1
	if !ok {
		g.proofFailures.Add(1)
		// Remember only the routing candidate, not its trust: old history must
		// revalidate rather than silently use asymmetric primary results.
		proof = gaiaProof{until: g.now().Add(time.Second), floor: current.floor, generation: proof.generation, failure: proof.failure}
	}
	g.proofMu.Lock()
	g.proof = proof
	g.proofMu.Unlock()
	return proof, ok
}

func (g *GaiaProxy) checkAgreement(ctx context.Context, hint int64) (gaiaProof, bool) {
	proof := gaiaProof{until: g.now().Add(gaiaProofTTL)}
	_, p, err := g.call(ctx, g.primary, gaiaReadRequest("status", 0))
	if err != nil || p.Error != nil {
		return gaiaProofFailure(proof, "agreement_primary_status", err)
	}
	_, a, err := g.call(ctx, g.archive, gaiaReadRequest("status", 0))
	if err != nil || a.Error != nil {
		return gaiaProofFailure(proof, "agreement_archive_status", err)
	}
	now := g.now()
	for _, e := range []*gaiaEnvelope{p, a} {
		s := e.Result.SyncInfo
		if *s.CatchingUp || s.Time.After(now.Add(gaiaFutureSkew)) || !now.Before(s.Time.Add(gaiaMaxHeadAge)) {
			return gaiaProofFailure(proof, "agreement", gaiaFailed("status_not_fresh_synced", 0))
		}
	}
	ph, _ := gaiaHeight(p.Result.SyncInfo.Height)
	ah, _ := gaiaHeight(a.Result.SyncInfo.Height)
	proof.height = min(ph, ah)
	if hint > proof.height {
		return gaiaProofFailure(proof, "agreement", gaiaFailed("floor_above_head", 0))
	}
	_, pb, err := g.call(ctx, g.primary, gaiaReadRequest("block", proof.height))
	if err != nil || pb.Error != nil {
		return gaiaProofFailure(proof, "agreement_primary_block", err)
	}
	_, ab, err := g.call(ctx, g.archive, gaiaReadRequest("block", proof.height))
	if err != nil || ab.Error != nil {
		return gaiaProofFailure(proof, "agreement_archive_block", err)
	}
	if !strings.EqualFold(pb.Result.BlockID.Hash, ab.Result.BlockID.Hash) || pb.Result.BlockID.Parts.Total != ab.Result.BlockID.Parts.Total || !strings.EqualFold(pb.Result.BlockID.Parts.Hash, ab.Result.BlockID.Parts.Hash) {
		return gaiaProofFailure(proof, "agreement", gaiaFailed("block_disagreement", 0))
	}
	candidate, _ := gaiaHeight(p.Result.SyncInfo.Earliest)
	candidate = max(candidate, hint)
	// Status may lag method-specific retention. Permit one upward correction,
	// never an unbounded search, and verify BOTH reads at the shared boundary.
	for attempt := 0; candidate > 0 && candidate <= proof.height && attempt < 2; attempt++ {
		bq, rq := gaiaReadRequest("block", candidate), gaiaReadRequest("block_results", candidate)
		_, block, blockErr := g.call(ctx, g.primary, bq)
		_, results, resultsErr := g.call(ctx, g.primary, rq)
		if blockErr != nil || resultsErr != nil {
			break
		}
		if block.Error == nil && results.Error == nil {
			if block.Result.Block.Data.Txs.count == results.Result.TxResults.count {
				proof.floor = candidate
			}
			break
		}
		next := max(gaiaMissing(bq, block), gaiaMissing(rq, results))
		if next <= candidate {
			break
		}
		candidate = next
	}
	proof.valid = ctx.Err() == nil && g.now().Before(proof.until)
	if !proof.valid {
		if ctx.Err() != nil {
			return gaiaProofFailure(proof, "agreement", gaiaContextFailure(ctx))
		}
		return gaiaProofFailure(proof, "agreement", gaiaFailed("proof_expired", 0))
	}
	return proof, proof.valid
}

func (g *GaiaProxy) logArchiveRetry(q gaiaRequest, event string, attempt int) {
	logger := g.logger
	if logger == nil {
		logger = log.Default()
	}
	logger.Printf("gaia_archive_retry method=%s height=%d event=%s attempt=%d", q.Method, q.height, event, attempt)
}

// Retries retain the admitted proof snapshot and the original request deadline.
// A fresh connection never relaxes response identity, height or chain checks.
func (g *GaiaProxy) archiveCall(ctx context.Context, q gaiaRequest, remaining *int) ([]byte, *gaiaEnvelope, error) {
	data, e, err := g.call(ctx, g.archive, q)
	for err == nil && gaiaMissing(q, e) > 0 {
		if ctx.Err() != nil {
			return nil, nil, gaiaContextFailure(ctx)
		}
		if *remaining == 0 {
			g.archiveRetryExhausted.Add(1)
			return nil, nil, gaiaFailed("pruned_retry_exhausted", 0)
		}
		*remaining -= 1
		g.archiveRetries.Add(1)
		data, e, err = g.callWithClient(ctx, g.freshClient, g.archive, q)
	}
	return data, e, err
}

func (g *GaiaProxy) archiveRead(ctx context.Context, q gaiaRequest, proof gaiaProof) ([]byte, error) {
	if !g.fresh(proof) {
		return nil, gaiaAt("archive", gaiaFailed("proof_expired", 0))
	}
	if q.height <= 0 || q.height > proof.height || (q.Method != "block" && q.Method != "block_results") {
		return nil, gaiaAt("archive", gaiaFailed("request_bounds", 0))
	}
	g.archiveReads.Add(1)
	remaining := gaiaArchiveRetryLimit
	data, e, err := g.archiveCall(ctx, q, &remaining)
	if err != nil || e.Error != nil {
		return nil, gaiaAt("archive", err)
	}
	if q.Method == "block_results" {
		// Compare only with metadata already observed; never add a validation RPC.
		if !g.blockCountMatches(q.height, e.Result.TxResults.count) {
			return nil, gaiaAt("archive_pair", gaiaFailed("tx_count_mismatch", 0))
		}
	}
	if ctx.Err() != nil {
		return nil, gaiaAt("archive", gaiaContextFailure(ctx))
	}
	if q.Method == "block" {
		g.rememberBlock(q.height, e.Result.Block.Data.Txs.count)
	}
	if remaining < gaiaArchiveRetryLimit {
		g.archiveRetryRecovered.Add(1)
		g.logArchiveRetry(q, "recovered", gaiaArchiveRetryLimit-remaining)
	}
	return data, nil
}

func (g *GaiaProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), g.timeout)
	defer cancel()
	// Bound slow request bodies and response writes as well as upstream work.
	controller := http.NewResponseController(w)
	deadline, _ := ctx.Deadline()
	_ = controller.SetReadDeadline(deadline)
	_ = controller.SetWriteDeadline(deadline)
	defer func() { _ = controller.SetReadDeadline(time.Time{}); _ = controller.SetWriteDeadline(time.Time{}) }()
	if g.admission.acquire(ctx) != nil {
		failure(w, 503, "GAIA admission unavailable")
		return
	}
	defer g.admission.release()
	g.requests.Add(1)
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, gaiaMaxRequest))
	if err != nil {
		g.invalid.Add(1)
		failure(w, 413, "GAIA request too large or unreadable")
		return
	}
	if ctx.Err() != nil {
		failure(w, 504, "GAIA request deadline exceeded")
		return
	}
	q, err := gaiaParseRequest(r, body)
	if err != nil {
		g.invalid.Add(1)
		failure(w, 400, "unsupported GAIA read request")
		return
	}
	if q.Method == "block" && q.height == 0 {
		data, err := g.primaryHead(ctx, q)
		if err != nil || ctx.Err() != nil {
			g.upstreamErrors.Add(1)
			code := http.StatusBadGateway
			if ctx.Err() != nil {
				code = http.StatusGatewayTimeout
				if err == nil {
					err = gaiaContextFailure(ctx)
				}
			}
			g.logFailure(q, gaiaAt("primary_head", err))
			failure(w, code, "GAIA read unavailable")
			return
		}
		reply(w, data)
		return
	}
	proof := g.cachedProof()
	var data []byte
	if q.height > 0 && proof.floor > q.height && !g.fresh(proof) {
		var ok bool
		proof, ok = g.agreement(ctx, 0)
		if !ok {
			err = proof.failure
		}
	}
	if err != nil {
		// A known historical route failed revalidation; do not mix providers.
	} else if q.height > 0 && proof.floor > q.height && g.fresh(proof) {
		g.directReads.Add(1)
		data, err = g.archiveRead(ctx, q, proof)
	} else {
		g.primaryReads.Add(1)
		var e *gaiaEnvelope
		data, e, err = g.call(ctx, g.primary, q)
		if err != nil {
			err = gaiaAt("primary", err)
		}
		if err == nil {
			if hint := gaiaMissing(q, e); hint != 0 {
				data = nil
				var ok bool
				proof, ok = g.agreement(ctx, hint)
				if !ok {
					err = proof.failure
				} else {
					g.fallbacks.Add(1)
					data, err = g.archiveRead(ctx, q, proof)
				}
			} else if e.Error != nil {
				g.logFailure(q, gaiaAt("primary", nil))
			}
		}
	}
	if err != nil || ctx.Err() != nil {
		g.upstreamErrors.Add(1)
		code := http.StatusBadGateway
		if ctx.Err() != nil {
			code = http.StatusGatewayTimeout
			if err == nil {
				err = gaiaAt("request", gaiaContextFailure(ctx))
			}
		}
		g.logFailure(q, err)
		failure(w, code, "GAIA read unavailable")
		return
	}
	reply(w, data)
}

func (g *GaiaProxy) Metrics(w io.Writer) {
	_, _ = fmt.Fprintf(w, "gaia_proxy_head_cache_hits_total %d\ngaia_proxy_head_cache_misses_total %d\n", g.headHits.Load(), g.headMisses.Load())
	_, _ = fmt.Fprintf(w, "gaia_proxy_archive_retries_total %d\ngaia_proxy_archive_retry_recovered_total %d\ngaia_proxy_archive_retry_exhausted_total %d\n", g.archiveRetries.Load(), g.archiveRetryRecovered.Load(), g.archiveRetryExhausted.Load())
	_, _ = fmt.Fprintf(w, "gaia_proxy_requests_total %d\ngaia_proxy_primary_reads_total %d\ngaia_proxy_archive_reads_total %d\ngaia_proxy_fallbacks_total %d\ngaia_proxy_direct_archive_reads_total %d\ngaia_proxy_invalid_requests_total %d\ngaia_proxy_upstream_errors_total %d\ngaia_proxy_agreement_checks_total %d\ngaia_proxy_agreement_failures_total %d\n", g.requests.Load(), g.primaryReads.Load(), g.archiveReads.Load(), g.fallbacks.Load(), g.directReads.Load(), g.invalid.Load(), g.upstreamErrors.Load(), g.proofChecks.Load(), g.proofFailures.Load())
	_, _ = fmt.Fprintf(w, "gaia_proxy_active_requests %d\ngaia_proxy_waiting_requests %d\ngaia_proxy_admission_rejected_total %d\ngaia_proxy_admission_timeouts_total %d\ngaia_proxy_admission_canceled_total %d\n", len(g.admission.active), len(g.admission.waiting), g.admission.rejected.Load(), g.admission.timedOut.Load(), g.admission.canceled.Load())
	proof := g.cachedProof()
	floor := int64(0)
	valid := 0
	if g.fresh(proof) {
		floor = proof.floor
		valid = 1
	}
	_, _ = fmt.Fprintf(w, "gaia_proxy_agreement_valid %d\ngaia_proxy_verified_primary_floor %d\n", valid, floor)
}
