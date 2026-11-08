package endpoints

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"gitlab.com/thorchain/thornode/v3/config"
)

// Probe reads liveness data straight from one endpoint, bypassing the routers, so
// the health loop always measures the endpoint itself.
type Probe interface {
	Name() string
	// Height returns the endpoint's current chain height (or slot / ledger index).
	Height(ctx context.Context) (int64, error)
	// Identity returns a value that is equal for endpoints on the same network and
	// different across networks (genesis hash, chain id, network id). "" means the
	// endpoint cannot report one; it is then treated as unverified, never as a match.
	Identity(ctx context.Context) (string, error)
}

// HashProbe is implemented by probes whose chain has a stable block hash per height.
type HashProbe interface {
	Probe
	HashAt(ctx context.Context, height int64) (string, error)
}

// CompoundProbe adds an extra liveness check (another host or protocol of the same
// endpoint) to a base probe. Height fails when either part fails.
type CompoundProbe struct {
	Probe
	// Extra is an additional liveness check run after a successful Height.
	Extra func(ctx context.Context) error
	// ExtraHeight, when set, is the tip of a second host of the same endpoint that
	// serves the chain too. The endpoint is then only as far as the lower of the two,
	// so a second host that stopped advancing makes the endpoint lag.
	ExtraHeight func(ctx context.Context) (int64, error)
	// ExtraIdentity, when set, is given the base probe's verified identity and
	// returns the identity to report for the endpoint. It lets a second host of the
	// same endpoint (a gRPC server, a REST API) take part in identity verification:
	// return a different identity for a proven mismatch (the endpoint is then
	// disabled, or fatal at startup for priority 1 with expected_identity), or an
	// error to leave the endpoint unverified until the hosts agree.
	ExtraIdentity func(ctx context.Context, base string) (string, error)
}

// Height runs the base probe and then the extra checks.
func (p CompoundProbe) Height(ctx context.Context) (int64, error) {
	h, err := p.Probe.Height(ctx)
	if err != nil {
		return 0, err
	}
	if p.Extra != nil {
		if err := p.Extra(ctx); err != nil {
			return 0, fmt.Errorf("endpoint %s: %w", p.Name(), err)
		}
	}
	if p.ExtraHeight != nil {
		extra, err := p.ExtraHeight(ctx)
		if err != nil {
			return 0, fmt.Errorf("endpoint %s: %w", p.Name(), err)
		}
		h = min(h, extra)
	}
	return h, nil
}

// Identity runs the base probe's identity check and then ExtraIdentity.
func (p CompoundProbe) Identity(ctx context.Context) (string, error) {
	id, err := p.Probe.Identity(ctx)
	if err != nil || p.ExtraIdentity == nil {
		return id, err
	}
	return p.ExtraIdentity(ctx, id)
}

// HashAt delegates to the base probe when it supports hashes.
func (p CompoundProbe) HashAt(ctx context.Context, height int64) (string, error) {
	hp, ok := p.Probe.(HashProbe)
	if !ok {
		return "", fmt.Errorf("endpoint %s: hash probe not supported", p.Name())
	}
	return hp.HashAt(ctx, height)
}

// authHeader injects per-endpoint credentials. Bearer wins when both are set,
// matching the Phase 1 EVM / UTXO behavior.
type authHeader struct {
	bearer string
	user   string
	pass   string
}

// authFor builds the credentials for an endpoint host. Explicit config fields win;
// otherwise userinfo embedded in the URL is lifted into the header and stripped from
// the URL so it never reaches logs or a foreign host.
func authFor(e config.ChainEndpointConfig, u *url.URL) (authHeader, *url.URL) {
	a := authHeader{bearer: e.AuthorizationBearer, user: e.UserName, pass: e.Password}
	if u == nil {
		return a, u
	}
	clean := *u
	if u.User != nil {
		if a.bearer == "" && a.user == "" && a.pass == "" {
			a.user = u.User.Username()
			a.pass, _ = u.User.Password()
		}
		clean.User = nil
	}
	return a, &clean
}

func (a authHeader) apply(h http.Header) {
	h.Del("Authorization")
	switch {
	case a.bearer != "":
		h.Set("Authorization", "Bearer "+a.bearer)
	case a.user != "" || a.pass != "":
		h.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(a.user+":"+a.pass)))
	}
}

func (a authHeader) secrets() []string { return []string{a.bearer, a.pass} }

// NormalizeHost gives scheme-less hosts an http:// prefix, matching the UTXO rpc
// client and cmd/bifrost startup normalization (the repo's own defaults such as
// bitcoin-daemon:8332 are scheme-less).
func NormalizeHost(host string) string {
	if host != "" && !strings.Contains(host, "://") {
		return "http://" + host
	}
	return host
}

// maxProbeBody bounds a probe response; height and hash replies are tiny.
const maxProbeBody = 1 << 20

// ErrRateLimited marks a probe answer that was a rate limit (HTTP 429, JSON-RPC
// -32005 / -32029, gRPC ResourceExhausted). The health loop cools the endpoint down
// instead of counting a strike.
var ErrRateLimited = errors.New("rate limited")

// errNotJSONObject is HTTPPostJSON's answer to a 200 whose body is not a JSON object.
var errNotJSONObject = errors.New("response is not a JSON object")

// isRateLimitCode reports whether a JSON-RPC error code is a provider rate limit.
func isRateLimitCode(code int) bool { return code == -32005 || code == -32029 }

// JSONRPCProbe is a generic JSON-RPC 2.0 prober. Chain-specific constructors below
// fill in method names and result parsers.
type JSONRPCProbe struct {
	name    string
	url     *url.URL
	auth    authHeader
	client  *http.Client
	secrets []string
	bad     error

	heightMethod   string
	heightParams   any
	parseHeight    func(json.RawMessage) (int64, error)
	identityMethod string
	identityParams any
	parseIdentity  func(json.RawMessage) (string, error)
	hashMethod     string
	hashParams     func(int64) any
	parseHash      func(json.RawMessage) (string, error)
	// endpointErrorCodes are JSON-RPC codes that are this chain's "endpoint cannot
	// serve" answer even though other providers use them for a rate limit (Solana's
	// -32005 "node is unhealthy"): a strike, not a cooldown, as in the router
	endpointErrorCodes []int
}

// jsonRPCHashProbe is the HashProbe view of a JSONRPCProbe; only probes with a
// hashMethod are returned as this type so `p.(HashProbe)` is meaningful.
type jsonRPCHashProbe struct{ *JSONRPCProbe }

func newJSONRPCProbe(e config.ChainEndpointConfig, host string, timeout time.Duration) *JSONRPCProbe {
	p := &JSONRPCProbe{name: e.Name, client: &http.Client{Timeout: timeout}}
	u, err := url.Parse(NormalizeHost(host))
	if err != nil || u.Host == "" {
		p.url = &url.URL{Scheme: "http", Host: "invalid.invalid"}
		p.bad = fmt.Errorf("endpoint %s: invalid url", e.Name)
		p.auth, _ = authFor(e, nil)
		return p
	}
	p.auth, p.url = authFor(e, u)
	p.secrets = SecretsOf(u, p.auth.secrets()...)
	return p
}

// Name returns the endpoint name.
func (p *JSONRPCProbe) Name() string { return p.name }

func (p *JSONRPCProbe) call(ctx context.Context, method string, params any, out *json.RawMessage) error {
	if p.bad != nil {
		return p.bad
	}
	if params == nil {
		params = []any{}
	}
	body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.url.String(), bytes.NewReader(body))
	if err != nil {
		return p.wrap(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	p.auth.apply(req.Header)
	resp, err := p.client.Do(req)
	if err != nil {
		return p.wrap(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusTooManyRequests {
		return p.wrap(fmt.Errorf("%s: http 429: %w", method, ErrRateLimited))
	}
	if resp.StatusCode != http.StatusOK {
		return p.wrap(fmt.Errorf("%s: http status %d", method, resp.StatusCode))
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxProbeBody))
	if err != nil {
		return p.wrap(err)
	}
	var env struct {
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return p.wrap(fmt.Errorf("%s: decode: %w", method, err))
	}
	if env.Error != nil {
		if isRateLimitCode(env.Error.Code) && !slices.Contains(p.endpointErrorCodes, env.Error.Code) {
			return p.wrap(fmt.Errorf("%s: rpc error %d: %w", method, env.Error.Code, ErrRateLimited))
		}
		return p.wrap(fmt.Errorf("%s: rpc error %d: %s", method, env.Error.Code, env.Error.Message))
	}
	*out = env.Result
	return nil
}

func (p *JSONRPCProbe) wrap(err error) error {
	return Redact(fmt.Errorf("endpoint %s: %w", p.name, err), p.secrets)
}

// Height implements Probe.
func (p *JSONRPCProbe) Height(ctx context.Context) (int64, error) {
	var raw json.RawMessage
	if err := p.call(ctx, p.heightMethod, p.heightParams, &raw); err != nil {
		return 0, err
	}
	h, err := p.parseHeight(raw)
	if err != nil {
		return 0, p.wrap(fmt.Errorf("%s: %w", p.heightMethod, err))
	}
	return h, nil
}

// Identity implements Probe.
func (p *JSONRPCProbe) Identity(ctx context.Context) (string, error) {
	var raw json.RawMessage
	if err := p.call(ctx, p.identityMethod, p.identityParams, &raw); err != nil {
		return "", err
	}
	id, err := p.parseIdentity(raw)
	if err != nil {
		return "", p.wrap(fmt.Errorf("%s: %w", p.identityMethod, err))
	}
	return id, nil
}

// HashAt implements HashProbe.
func (p jsonRPCHashProbe) HashAt(ctx context.Context, height int64) (string, error) {
	var raw json.RawMessage
	if err := p.call(ctx, p.hashMethod, p.hashParams(height), &raw); err != nil {
		return "", err
	}
	h, err := p.parseHash(raw)
	if err != nil {
		return "", p.wrap(fmt.Errorf("%s: %w", p.hashMethod, err))
	}
	return h, nil
}

// asProbe returns the HashProbe view when a hash method is configured.
func (p *JSONRPCProbe) asProbe() Probe {
	if p.hashMethod != "" {
		return jsonRPCHashProbe{p}
	}
	return p
}

// ---- parsers ----

func parseHexInt64(raw json.RawMessage) (int64, error) {
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return 0, err
	}
	n, ok := new(big.Int).SetString(strings.TrimPrefix(strings.ToLower(s), "0x"), 16)
	if !ok || !n.IsInt64() {
		return 0, fmt.Errorf("not a hex quantity: %q", s)
	}
	return n.Int64(), nil
}

func parseHexString(raw json.RawMessage) (string, error) {
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", err
	}
	return strings.ToLower(s), nil
}

func parseString(raw json.RawMessage) (string, error) {
	var s string
	err := json.Unmarshal(raw, &s)
	return s, err
}

func parseNumber(raw json.RawMessage) (int64, error) {
	var n json.Number
	if err := json.Unmarshal(raw, &n); err != nil {
		return 0, err
	}
	return n.Int64()
}

// field walks nested objects and returns the leaf as raw JSON.
func field(raw json.RawMessage, path ...string) (json.RawMessage, error) {
	cur := raw
	for _, key := range path {
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(cur, &obj); err != nil {
			return nil, fmt.Errorf("field %q: %w", key, err)
		}
		next, ok := obj[key]
		if !ok {
			return nil, fmt.Errorf("field %q missing", key)
		}
		cur = next
	}
	return cur, nil
}

func fieldInt64(path ...string) func(json.RawMessage) (int64, error) {
	return func(raw json.RawMessage) (int64, error) {
		leaf, err := field(raw, path...)
		if err != nil {
			return 0, err
		}
		// accept a JSON number or a numeric string (CometBFT heights are strings)
		var s string
		if err := json.Unmarshal(leaf, &s); err == nil {
			return strconv.ParseInt(s, 10, 64)
		}
		return parseNumber(leaf)
	}
}

func fieldString(path ...string) func(json.RawMessage) (string, error) {
	return func(raw json.RawMessage) (string, error) {
		leaf, err := field(raw, path...)
		if err != nil {
			return "", err
		}
		var s string
		if err := json.Unmarshal(leaf, &s); err == nil {
			return s, nil
		}
		var n json.Number
		if err := json.Unmarshal(leaf, &n); err == nil {
			return n.String(), nil
		}
		return "", fmt.Errorf("field %v is neither string nor number", path)
	}
}

// ---- chain constructors ----

// NewEVMProbe covers ETH, AVAX, BSC, BASE, POL and TRON's JSON-RPC (rpc_host).
func NewEVMProbe(e config.ChainEndpointConfig, timeout time.Duration) Probe {
	p := newJSONRPCProbe(e, e.RPCHost, timeout)
	p.heightMethod, p.parseHeight = "eth_blockNumber", parseHexInt64
	p.identityMethod, p.parseIdentity = "eth_chainId", parseHexString
	p.hashMethod = "eth_getBlockByNumber"
	p.hashParams = func(h int64) any { return []any{"0x" + strconv.FormatInt(h, 16), false} }
	p.parseHash = fieldString("hash")
	return p.asProbe()
}

// NewUTXOProbe covers BTC, LTC, BCH, DOGE, ZEC. Identity is the genesis block hash.
func NewUTXOProbe(e config.ChainEndpointConfig, timeout time.Duration) Probe {
	p := newJSONRPCProbe(e, e.RPCHost, timeout)
	p.heightMethod, p.parseHeight = "getblockcount", parseNumber
	p.identityMethod, p.identityParams, p.parseIdentity = "getblockhash", []any{0}, parseString
	p.hashMethod = "getblockhash"
	p.hashParams = func(h int64) any { return []any{h} }
	p.parseHash = parseString
	return p.asProbe()
}

// NewCometProbe covers GAIA and NOBLE via the CometBFT RPC on rpc_host. Identity is
// node_info.network, which the Cosmos client compares to cosmos_chain_id.
func NewCometProbe(e config.ChainEndpointConfig, timeout time.Duration) Probe {
	p := newJSONRPCProbe(e, e.RPCHost, timeout)
	p.heightMethod, p.parseHeight = "status", fieldInt64("sync_info", "latest_block_height")
	p.identityMethod, p.parseIdentity = "status", fieldString("node_info", "network")
	// commit carries the block hash without the block's transactions; the block
	// RPC would download a whole block from every endpoint on every health cycle
	p.hashMethod = "commit"
	p.hashParams = func(h int64) any { return map[string]any{"height": strconv.FormatInt(h, 10)} }
	p.parseHash = fieldString("signed_header", "commit", "block_id", "hash")
	return p.asProbe()
}

// NewSolanaProbe reports the finalized slot. No hash probe: slots can be skipped.
func NewSolanaProbe(e config.ChainEndpointConfig, timeout time.Duration) Probe {
	p := newJSONRPCProbe(e, e.RPCHost, timeout)
	p.heightMethod, p.parseHeight = "getSlot", parseNumber
	p.heightParams = []any{map[string]any{"commitment": "finalized"}}
	p.identityMethod, p.parseIdentity = "getGenesisHash", parseString
	p.endpointErrorCodes = []int{-32005, -32016} // "node is unhealthy", "minimum context slot not reached": behind, not a rate limit
	return p.asProbe()
}

// NewXRPProbe reports the validated ledger index; identity is server_info network_id
// (absent on some servers, in which case the endpoint stays unverified).
func NewXRPProbe(e config.ChainEndpointConfig, timeout time.Duration) Probe {
	p := newJSONRPCProbe(e, e.RPCHost, timeout)
	// the validated ledger, the height XrpBlockScanner.GetHeight reads: Clio forwards
	// ledger_current to its rippled, so the open ledger keeps advancing while the
	// validated data Clio serves itself has stalled
	p.heightMethod, p.parseHeight = "ledger", fieldInt64("ledger_index")
	p.heightParams = []any{map[string]any{"ledger_index": "validated"}}
	p.identityMethod, p.parseIdentity = "server_info", fieldString("info", "network_id")
	p.hashMethod = "ledger"
	p.hashParams = func(h int64) any { return []any{map[string]any{"ledger_index": h}} }
	p.parseHash = fieldString("ledger", "ledger_hash")
	return p.asProbe()
}

// HTTPPostJSON returns a poster bound to an endpoint's REST host and credentials: it
// POSTs body (JSON) to base+path and returns the response as a JSON object. A 429 is
// reported as ErrRateLimited; any other non-200 status, or a body that is not a JSON
// object, is an error. Errors never carry the URL or the credentials.
func HTTPPostJSON(e config.ChainEndpointConfig, base string, timeout time.Duration) func(ctx context.Context, path, body string) (map[string]json.RawMessage, error) {
	u, err := url.Parse(NormalizeHost(base))
	if err != nil || u.Host == "" {
		return func(context.Context, string, string) (map[string]json.RawMessage, error) {
			return nil, fmt.Errorf("endpoint %s: invalid url", e.Name)
		}
	}
	auth, clean := authFor(e, u)
	secrets := SecretsOf(clean, auth.secrets()...)
	hc := &http.Client{Timeout: timeout}
	return func(ctx context.Context, path, body string) (map[string]json.RawMessage, error) {
		target := *clean
		setEscapedPath(&target, strings.TrimSuffix(clean.EscapedPath(), "/")+"/"+strings.TrimPrefix(path, "/"))
		if body == "" {
			body = "{}"
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, target.String(), strings.NewReader(body))
		if err != nil {
			return nil, Redact(err, secrets)
		}
		req.Header.Set("Content-Type", "application/json")
		auth.apply(req.Header)
		resp, err := hc.Do(req)
		if err != nil {
			return nil, Redact(err, secrets)
		}
		raw, rerr := io.ReadAll(io.LimitReader(resp.Body, maxProbeBody))
		_ = resp.Body.Close()
		if resp.StatusCode == http.StatusTooManyRequests {
			return nil, fmt.Errorf("%s: http 429: %w", path, ErrRateLimited)
		}
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("%s: http status %d", path, resp.StatusCode)
		}
		if rerr != nil {
			return nil, Redact(fmt.Errorf("%s: %w", path, rerr), secrets)
		}
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(raw, &obj); err != nil {
			return nil, fmt.Errorf("%s: %w", path, errNotJSONObject)
		}
		return obj, nil
	}
}

// HTTPPostCheck returns an extra liveness check for CompoundProbe that POSTs an
// empty JSON object to base+path with the endpoint's credentials and requires a 200
// whose JSON body contains every field in wantFields. The field requirement is what
// separates a real chain API from a proxy or a captive portal that answers 200 to
// anything.
func HTTPPostCheck(e config.ChainEndpointConfig, base, path string, timeout time.Duration, wantFields ...string) func(ctx context.Context) error {
	post := HTTPPostJSON(e, base, timeout)
	return func(ctx context.Context) error {
		obj, err := post(ctx, path, "{}")
		if err != nil {
			if len(wantFields) == 0 && errors.Is(err, errNotJSONObject) {
				return nil // liveness only: any 200 answers
			}
			return err
		}
		for _, f := range wantFields {
			if _, ok := obj[f]; !ok {
				return fmt.Errorf("%s: response has no %q field; is this endpoint the right chain API?", path, f)
			}
		}
		return nil
	}
}
