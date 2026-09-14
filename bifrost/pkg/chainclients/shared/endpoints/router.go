package endpoints

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/config"
)

// SyntheticBase is the secret-free URL client libraries are constructed against.
// net/http embeds the request URL in every *url.Error above any RoundTripper, so
// the real endpoint URLs (which carry API keys) live only inside the router.
func SyntheticBase(chain common.Chain) *url.URL {
	return &url.URL{Scheme: "http", Host: strings.ToLower(chain.String()) + ".endpoints.invalid", Path: "/"}
}

// Target is one endpoint's base URL and credentials for a given host field.
type Target struct {
	Name    string
	URL     *url.URL // nil when the endpoint has no host for this selector
	auth    authHeader
	secrets []string
}

// HostSelector picks which host field of an endpoint a router should use.
type HostSelector func(config.ChainEndpointConfig) string

// RPCHost selects rpc_host.
func RPCHost(e config.ChainEndpointConfig) string { return e.RPCHost }

// APIHost selects api_host (TRON).
func APIHost(e config.ChainEndpointConfig) string { return e.APIHost }

// TargetsFrom builds one Target per endpoint, in the given order. Endpoints with an
// empty host for the selector get a nil URL and are skipped by the router. Userinfo
// embedded in a URL becomes the endpoint's basic auth and is stripped from the URL.
func TargetsFrom(eps []config.ChainEndpointConfig, sel HostSelector) ([]Target, error) {
	out := make([]Target, len(eps))
	for i, e := range eps {
		out[i] = Target{Name: e.Name}
		host := sel(e)
		if host == "" {
			out[i].auth, _ = authFor(e, nil)
			continue
		}
		u, err := url.Parse(NormalizeHost(host))
		if err != nil || u.Scheme == "" || u.Host == "" {
			return nil, fmt.Errorf("endpoint %q: invalid url", e.Name)
		}
		out[i].auth, out[i].URL = authFor(e, u)
		out[i].secrets = SecretsOf(u, out[i].auth.secrets()...)
	}
	return out, nil
}

// HTTPRouterConfig configures an HTTPRouter.
type HTTPRouterConfig struct {
	// Base is the URL the client library was constructed with (use SyntheticBase).
	// The router keeps the part of each request path that follows Base.Path and
	// appends it to the chosen endpoint's path, so REST-style clients (TRON
	// /wallet/...) work unchanged. Base must carry no userinfo and no query.
	Base *url.URL
	// Targets, one per endpoint, in the same order as the manager's endpoint list.
	Targets []Target
	// WriteMethods are JSON-RPC methods that must not be retried on another endpoint
	// once they may have been sent.
	WriteMethods []string
	// WritePaths are request path suffixes (REST) with the same treatment.
	WritePaths []string
	// MaxBodyBytes caps the request body the router buffers for retries (default 8 MiB).
	MaxBodyBytes int64
	// DialTimeout bounds TCP connect and TLS handshake per attempt (default 5s).
	DialTimeout time.Duration
	// AttemptTimeout bounds the wait for response headers per attempt (default 10s),
	// so one black-holed endpoint cannot consume the caller's whole request budget.
	AttemptTimeout time.Duration
	// Transport performs the actual requests. Default: a clone of
	// http.DefaultTransport with the timeouts above.
	Transport http.RoundTripper
}

// HTTPRouter is an http.RoundTripper that sends every request to the manager's
// active endpoint and, for reads, retries across the other healthy endpoints.
type HTTPRouter struct {
	mgr          *Manager
	cfg          HTTPRouterConfig
	targets      map[string]Target
	writeMethods map[string]bool
	log          zerolog.Logger
}

const (
	defaultMaxBodyBytes   = 8 << 20
	defaultDialTimeout    = 5 * time.Second
	defaultAttemptTimeout = 10 * time.Second
	sniffLimit            = 64 << 10
	// minSniffBuffer is bufio's own minimum reader size; a smaller request is
	// silently rounded up, so Peek must ask for what it will actually get.
	minSniffBuffer = 16
)

// NewHTTPRouter validates the config against the manager and returns the router.
func NewHTTPRouter(mgr *Manager, cfg HTTPRouterConfig, log zerolog.Logger) (*HTTPRouter, error) {
	if cfg.Base == nil {
		return nil, errors.New("router: Base is required")
	}
	if cfg.Base.User != nil || cfg.Base.RawQuery != "" {
		return nil, errors.New("router: Base must not carry userinfo or a query (use SyntheticBase)")
	}
	if len(cfg.Targets) != mgr.Len() {
		return nil, fmt.Errorf("router: %d targets for %d endpoints", len(cfg.Targets), mgr.Len())
	}
	r := &HTTPRouter{
		mgr:          mgr,
		cfg:          cfg,
		targets:      make(map[string]Target, len(cfg.Targets)),
		writeMethods: make(map[string]bool, len(cfg.WriteMethods)),
		log:          log.With().Str("module", "endpoint-router").Stringer("chain", mgr.Chain()).Logger(),
	}
	for i, t := range cfg.Targets {
		if t.Name != mgr.entries[i].Name {
			return nil, fmt.Errorf("router: target %d (%s) does not match endpoint %s", i, t.Name, mgr.entries[i].Name)
		}
		r.targets[t.Name] = t
	}
	for _, m := range cfg.WriteMethods {
		r.writeMethods[m] = true
	}
	if r.cfg.MaxBodyBytes <= 0 {
		r.cfg.MaxBodyBytes = defaultMaxBodyBytes
	}
	if r.cfg.DialTimeout <= 0 {
		r.cfg.DialTimeout = defaultDialTimeout
	}
	if r.cfg.AttemptTimeout <= 0 {
		r.cfg.AttemptTimeout = defaultAttemptTimeout
	}
	if r.cfg.Transport == nil {
		r.cfg.Transport = newTransport(r.cfg.DialTimeout, r.cfg.AttemptTimeout)
	}
	return r, nil
}

func newTransport(dial, header time.Duration) http.RoundTripper {
	base, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return http.DefaultTransport
	}
	t := base.Clone()
	t.DialContext = (&net.Dialer{Timeout: dial, KeepAlive: 30 * time.Second}).DialContext
	t.TLSHandshakeTimeout = dial
	t.ResponseHeaderTimeout = header
	return t
}

type pinKey struct{}

// PinContext makes the routers send the request to the named endpoint only, with
// no retry and regardless of its health state. It exists for signer-cache
// verification reads that must observe a specific endpoint.
func PinContext(ctx context.Context, name string) context.Context {
	return context.WithValue(ctx, pinKey{}, name)
}

func pinnedFrom(ctx context.Context) (string, bool) {
	name, ok := ctx.Value(pinKey{}).(string)
	return name, ok && name != ""
}

// PinnedEndpoint returns the endpoint name a context was pinned to with PinContext.
func PinnedEndpoint(ctx context.Context) (string, bool) { return pinnedFrom(ctx) }

// RoundTrip implements http.RoundTripper.
func (r *HTTPRouter) RoundTrip(req *http.Request) (*http.Response, error) {
	body, err := r.bufferBody(req)
	if err != nil {
		return nil, err
	}
	isWrite := r.isWrite(req, body)

	var candidates []*Entry
	if name, ok := pinnedFrom(req.Context()); ok {
		e := r.mgr.Entry(name)
		if e == nil {
			return nil, fmt.Errorf("chain %s: pinned endpoint %s not configured", r.mgr.Chain(), name)
		}
		candidates = []*Entry{e}
	} else if isWrite {
		wt := r.mgr.WriteTarget()
		if wt == nil {
			return nil, fmt.Errorf("chain %s: no permitted write endpoint (active endpoint disabled)", r.mgr.Chain())
		}
		candidates = []*Entry{wt}
		for _, e := range r.mgr.ReadCandidates() { // dial-error fallbacks only
			if e != wt {
				candidates = append(candidates, e)
			}
		}
	} else {
		candidates = r.mgr.ReadCandidates()
	}

	var lastErr error
	for i, e := range candidates {
		if err := req.Context().Err(); err != nil {
			return nil, err
		}
		t, ok := r.targets[e.Name]
		if !ok || t.URL == nil {
			continue
		}
		if isWrite && i > 0 && !isDialError(lastErr) {
			break // may have been sent; never duplicate a write
		}
		// per-attempt budget: a stalled endpoint must leave time for the next one.
		// The budget only bounds the wait for response headers; once they arrive the
		// timer is stopped and the body read keeps the caller's own deadline.
		actx, acancel := context.WithCancel(req.Context())
		timer := time.AfterFunc(attemptBudget(req.Context(), r.cfg.AttemptTimeout, len(candidates)-i-1), acancel)
		out := r.rewrite(req.WithContext(actx), t, body)
		resp, err := r.cfg.Transport.RoundTrip(out)
		timer.Stop()
		if err != nil {
			acancel()
			if req.Context().Err() != nil {
				return nil, err // caller cancelled; not the endpoint's fault
			}
			r.mgr.RecordError(e.Name, ErrorKindTransport)
			lastErr = Redact(fmt.Errorf("endpoint %s: %w", e.Name, err), t.secrets)
			continue
		}
		// The body wrapper is the single health reporter for this response: it
		// records one success when the body completes cleanly, or one transport
		// failure when it breaks mid-stream. Recording success only at body
		// completion is what lets a broken body actually evict an endpoint: a
		// header-time success would reset the error run that the same request
		// is about to add to.
		name := e.Name
		ab := &attemptBody{ReadCloser: resp.Body, cancel: acancel, report: func(rerr error) {
			if req.Context().Err() != nil {
				return // caller cancelled; not the endpoint's fault
			}
			if rerr != nil {
				r.mgr.RecordError(name, ErrorKindTransport)
				return
			}
			r.mgr.RecordSuccess(name)
		}}
		resp.Body = ab
		verdict, kind, fromBody := r.classify(resp, isWrite)
		switch verdict {
		case verdictOK, verdictAppError:
			// the endpoint answered; success is recorded when the caller finishes
			// the body, and the caller's own error handling applies to appError
			return resp, nil
		case verdictRateLimited:
			ab.disarm() // this verdict is recorded here, not by the body
			r.mgr.RecordRateLimited(e.Name)
			if isWrite && fromBody {
				return resp, nil // the write may have been accepted; hand the answer back, never retry
			}
			drain(resp)
			lastErr = fmt.Errorf("endpoint %s: rate limited", e.Name)
			if isWrite {
				return nil, lastErr
			}
		case verdictEndpointError:
			// a broken body already reported itself from inside classify
			if !ab.reported() {
				r.mgr.RecordError(e.Name, kind)
			}
			ab.disarm()
			if isWrite && fromBody {
				return resp, nil
			}
			drain(resp)
			lastErr = fmt.Errorf("endpoint %s: http %d", e.Name, resp.StatusCode)
			if isWrite {
				return nil, lastErr
			}
		}
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("chain %s: no endpoint available", r.mgr.Chain())
	}
	return nil, lastErr
}

// attemptBody releases the attempt context once the caller has consumed the body
// and reports the outcome of the response exactly once: a non-EOF read error is a
// transport failure, a clean end (EOF or Close) is a success. disarm cancels the
// reporting when the caller records its own verdict instead.
type attemptBody struct {
	io.ReadCloser
	cancel context.CancelFunc

	mu     sync.Mutex
	report func(error)
	fired  bool
	failed bool
}

func (b *attemptBody) fire(err error) {
	b.mu.Lock()
	report := b.report
	if b.fired || report == nil {
		b.mu.Unlock()
		return
	}
	b.fired, b.failed = true, err != nil
	b.mu.Unlock()
	report(err)
}

// reported reports whether the outcome has already been sent to the manager as a
// failure (so the caller does not record the same physical failure twice).
func (b *attemptBody) reported() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.fired && b.failed
}

// disarm stops any further reporting from this body.
func (b *attemptBody) disarm() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.report = nil
}

func (b *attemptBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	switch {
	case err == nil:
	case errors.Is(err, io.EOF):
		b.fire(nil)
	default:
		b.fire(err)
	}
	return n, err
}

func (b *attemptBody) Close() error {
	b.fire(nil)
	err := b.ReadCloser.Close()
	b.cancel()
	return err
}

// attemptBudget bounds one attempt so that, when more candidates remain, at least
// half of the caller's remaining deadline is left for them.
func attemptBudget(ctx context.Context, base time.Duration, remainingCandidates int) time.Duration {
	budget := base
	if deadline, ok := ctx.Deadline(); ok {
		remaining := time.Until(deadline)
		if remainingCandidates > 0 {
			remaining /= 2
		}
		if remaining < budget {
			budget = remaining
		}
	}
	if budget <= 0 {
		budget = time.Millisecond
	}
	return budget
}

type verdict int

const (
	verdictOK verdict = iota
	verdictAppError
	verdictRateLimited
	verdictEndpointError
)

// classify decides what a response says about the endpoint. 200 JSON bodies are
// sniffed (bounded) for JSON-RPC server-side error codes; fromBody reports that the
// verdict came from an intact JSON body (so a write's answer can still be returned).
func (r *HTTPRouter) classify(resp *http.Response, isWrite bool) (verdict, ErrorKind, bool) {
	switch {
	case resp.StatusCode == http.StatusTooManyRequests:
		return verdictRateLimited, ErrorKindRateLimit, false
	case resp.StatusCode >= 500, resp.StatusCode >= 300 && resp.StatusCode < 400,
		resp.StatusCode == http.StatusUnauthorized, resp.StatusCode == http.StatusForbidden:
		return verdictEndpointError, ErrorKindHTTP, false
	case resp.StatusCode >= 400:
		return verdictAppError, "", false
	}
	ct := strings.ToLower(resp.Header.Get("Content-Type"))
	if strings.Contains(ct, "text/html") {
		return verdictEndpointError, ErrorKindHTTP, false
	}
	if !strings.Contains(ct, "json") && ct != "" {
		return verdictOK, "", false
	}
	if resp.ContentLength > sniffLimit {
		return verdictOK, "", false
	}
	// size the sniff buffer to the response when its length is known: this is the
	// hot path for every routed EVM, Solana, XRP and TRON call
	size := sniffLimit
	if resp.ContentLength > 0 && int(resp.ContentLength)+1 < size {
		size = int(resp.ContentLength) + 1
	}
	if size < minSniffBuffer {
		size = minSniffBuffer
	}
	br := bufio.NewReaderSize(resp.Body, size)
	peek, perr := br.Peek(size)
	resp.Body = &peekedBody{Reader: br, closer: resp.Body}
	if perr != nil && !errors.Is(perr, io.EOF) {
		// headers arrived but the body broke: the endpoint failed this request
		return verdictEndpointError, ErrorKindTransport, false
	}
	if len(peek) == size && resp.ContentLength <= 0 {
		return verdictOK, "", false // large streamed result; JSON-RPC error objects are tiny
	}
	trimmed := bytes.TrimLeft(peek, " \t\r\n")
	if len(trimmed) == 0 {
		return verdictEndpointError, ErrorKindTransport, false
	}
	v, k := classifyJSONRPC(trimmed)
	return v, k, true
}

// classifyJSONRPC applies the JSON-RPC error code rules to a single response or a
// batch; the worst element decides.
func classifyJSONRPC(trimmed []byte) (verdict, ErrorKind) {
	type rpcErr struct {
		Code int `json:"code"`
	}
	var errs []*rpcErr
	switch trimmed[0] {
	case '{':
		var env struct {
			Error *rpcErr `json:"error"`
		}
		if json.Unmarshal(trimmed, &env) != nil {
			return verdictOK, ""
		}
		errs = append(errs, env.Error)
	case '[':
		var batch []struct {
			Error *rpcErr `json:"error"`
		}
		if json.Unmarshal(trimmed, &batch) != nil {
			return verdictOK, ""
		}
		for _, b := range batch {
			errs = append(errs, b.Error)
		}
	default:
		return verdictOK, ""
	}
	// the worst element decides: an endpoint-level error anywhere in a batch means the
	// batch was not served and must be retried elsewhere
	worst, worstKind := verdictOK, ErrorKind("")
	for _, e := range errs {
		if e == nil {
			continue
		}
		switch {
		case isRateLimitCode(e.Code): // provider request limit exceeded (Infura / Ankr style)
			if worst < verdictRateLimited {
				worst, worstKind = verdictRateLimited, ErrorKindRateLimit
			}
		case e.Code == -32601 || e.Code == -32603: // method not found / internal error: the endpoint cannot serve bifrost
			worst, worstKind = verdictEndpointError, ErrorKindHTTP
		default:
			if worst < verdictAppError {
				worst = verdictAppError
			}
		}
	}
	return worst, worstKind
}

// peekedBody serves the buffered bytes first and closes the original body.
type peekedBody struct {
	*bufio.Reader
	closer io.Closer
}

func (p *peekedBody) Close() error { return p.closer.Close() }

// bufferBody reads the request body once so it can be replayed per endpoint.
func (r *HTTPRouter) bufferBody(req *http.Request) ([]byte, error) {
	if req.Body == nil || req.Body == http.NoBody {
		return nil, nil
	}
	defer func() { _ = req.Body.Close() }()
	b, err := io.ReadAll(io.LimitReader(req.Body, r.cfg.MaxBodyBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > r.cfg.MaxBodyBytes {
		return nil, fmt.Errorf("router: request body exceeds %d bytes", r.cfg.MaxBodyBytes)
	}
	return b, nil
}

// isWrite classifies the request by JSON-RPC method (single request or batch) or
// by REST path suffix.
func (r *HTTPRouter) isWrite(req *http.Request, body []byte) bool {
	for _, p := range r.cfg.WritePaths {
		if strings.HasSuffix(req.URL.Path, p) {
			return true
		}
	}
	if len(r.writeMethods) == 0 || len(body) == 0 {
		return false
	}
	trimmed := bytes.TrimLeft(body, " \t\r\n")
	if len(trimmed) > 0 && trimmed[0] == '[' {
		var batch []struct {
			Method string `json:"method"`
		}
		if json.Unmarshal(trimmed, &batch) == nil {
			for _, b := range batch {
				if r.writeMethods[b.Method] {
					return true
				}
			}
		}
		return false
	}
	var single struct {
		Method string `json:"method"`
	}
	if json.Unmarshal(trimmed, &single) == nil {
		return r.writeMethods[single.Method]
	}
	return false
}

// rewrite clones req for target t: scheme/host from t, path = t.path + (req.path
// minus Base.path), t's query plus the request's own query, per-endpoint
// Authorization, fresh body.
func (r *HTTPRouter) rewrite(req *http.Request, t Target, body []byte) *http.Request {
	out := req.Clone(req.Context())
	u := *t.URL
	suffix := strings.TrimPrefix(req.URL.Path, r.cfg.Base.Path)
	u.Path = joinPath(t.URL.Path, suffix)
	u.RawPath = ""
	q := t.URL.Query()
	for k, vs := range req.URL.Query() {
		for _, v := range vs {
			q.Add(k, v)
		}
	}
	u.RawQuery = q.Encode()
	out.URL = &u
	out.Host = u.Host
	t.auth.apply(out.Header)
	if body != nil {
		out.Body = io.NopCloser(bytes.NewReader(body))
		out.ContentLength = int64(len(body))
		out.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(body)), nil }
	} else {
		out.Body = http.NoBody
		out.ContentLength = 0
	}
	return out
}

func joinPath(base, suffix string) string {
	if suffix == "" || suffix == "/" {
		return base
	}
	return strings.TrimSuffix(base, "/") + "/" + strings.TrimPrefix(suffix, "/")
}

// isDialError reports whether err happened before any bytes were sent (connection
// refused, DNS failure, TLS handshake), which makes a retry safe even for writes.
// Timeouts after the request was sent are never dial errors.
func isDialError(err error) bool {
	if err == nil {
		return false
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return opErr.Op == "dial"
	}
	var dnsErr *net.DNSError
	return errors.As(err, &dnsErr)
}

func drain(resp *http.Response) {
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	_ = resp.Body.Close()
}
