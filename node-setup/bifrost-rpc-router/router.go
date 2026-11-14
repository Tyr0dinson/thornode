package main

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type metrics struct {
	requests, errors, canceled, status4xx, status5xx, durationNS atomic.Uint64
}

// status records an upstream reply's class and reports whether the status is
// itself a provider failure. Other 4xx/5xx replies are ordinary RPC answers
// (some daemons send JSON-RPC errors as HTTP 500) and must not drive the alert.
func (m *metrics) status(code int) bool {
	switch {
	case code >= 400 && code < 500:
		m.status4xx.Add(1)
	case code >= 500 && code < 600:
		m.status5xx.Add(1)
	}
	return code == http.StatusTooManyRequests || code == http.StatusBadGateway || code == http.StatusServiceUnavailable || code == http.StatusGatewayTimeout
}

// clientCanceled reports that the caller went away. net/http also cancels the
// request when the connection read deadline (set to the chain deadline) fires,
// so a cancellation at or after the deadline is a timeout, not a hang-up.
func clientCanceled(ctx context.Context) bool {
	if !errors.Is(ctx.Err(), context.Canceled) {
		return false
	}
	deadline, ok := ctx.Deadline()
	return !ok || time.Now().Before(deadline)
}

type upstream struct {
	proxy     *httputil.ReverseProxy
	transport *http.Transport
	stats     *metrics
	// For the opt-in head cache, which makes its own provider call.
	target   *url.URL
	t        Target
	endpoint Endpoint
	timeout  time.Duration
	heads    headCache
	ahead    readAhead
	calls    callAhead
}

type snapshot struct {
	catalog   *catalog
	state     State
	upstreams map[string]map[string]map[string]*upstream
}

type router struct {
	current   atomic.Pointer[snapshot]
	mu        sync.Mutex
	faults    atomic.Uint64 // Rejected or unpersisted route-state writes.
	admission map[string]*admission
	filename  string
	lock      *os.File
	save      func(string, State) error
}

func newRouter(filename string, initialize bool) (*router, error) {
	c, err := loadCatalog(filename)
	if err != nil {
		return nil, err
	}
	s, lock, fault, err := openState(c, initialize)
	if err != nil {
		return nil, err
	}
	r := &router{filename: filename, lock: lock, save: persist, admission: map[string]*admission{}}
	for name, ch := range c.config.Chains {
		r.admission[name] = &admission{active: make(chan struct{}, ch.Active), waiting: make(chan struct{}, ch.Waiting), queue: time.Duration(ch.QueueSeconds) * time.Second}
	}
	r.current.Store(buildSnapshot(c, s))
	if fault {
		r.faults.Add(1)
	}
	return r, nil
}

func buildSnapshot(c *catalog, s State) *snapshot {
	out := &snapshot{catalog: c, state: s, upstreams: map[string]map[string]map[string]*upstream{}}
	for chain, backends := range c.targets {
		out.upstreams[chain] = map[string]map[string]*upstream{}
		for backend, targets := range backends {
			out.upstreams[chain][backend] = map[string]*upstream{}
			for endpoint, target := range targets {
				out.upstreams[chain][backend][endpoint] = newUpstream(target, c.config.Chains[chain].Endpoints[endpoint], c.config.Chains[chain], chain, backend, endpoint)
			}
		}
	}
	return out
}

func (s *snapshot) closeIdle() {
	for _, backends := range s.upstreams {
		for _, endpoints := range backends {
			for _, u := range endpoints {
				u.transport.CloseIdleConnections()
			}
		}
	}
}

func (r *router) close() {
	r.current.Load().closeIdle()
	r.lock.Close()
}

func newUpstream(t Target, endpoint Endpoint, ch Chain, chain, backend, id string) *upstream {
	target, _ := url.Parse(t.URL)
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DialContext = (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext
	// HTTP/1 avoids hidden HTTP/2 replay paths for opaque RPC POST bodies.
	transport.ForceAttemptHTTP2 = false
	transport.Protocols = new(http.Protocols)
	transport.Protocols.SetHTTP1(true)
	// Clone initializes DefaultTransport's HTTP/2 support and can retain its
	// ALPN list even when Protocols is changed. Match TLS negotiation to HTTP/1.
	if transport.TLSClientConfig == nil {
		transport.TLSClientConfig = &tls.Config{}
	}
	transport.TLSClientConfig.NextProtos = []string{"http/1.1"}
	transport.TLSNextProto = make(map[string]func(string, *tls.Conn) http.RoundTripper)
	transport.MaxConnsPerHost = ch.Active
	transport.MaxIdleConnsPerHost = ch.Active
	transport.MaxIdleConns = ch.Active
	transport.MaxResponseHeaderBytes = 16 << 10
	transport.ResponseHeaderTimeout = time.Duration(ch.TimeoutSeconds) * time.Second
	transport.TLSHandshakeTimeout = 5 * time.Second
	transport.IdleConnTimeout = 60 * time.Second
	u := &upstream{transport: transport, stats: &metrics{}, target: target, t: t, endpoint: endpoint, timeout: time.Duration(ch.TimeoutSeconds) * time.Second}
	failure := func(reason string) {
		u.stats.errors.Add(1)
		log.Printf("event=upstream_failure chain=%s backend=%s endpoint=%s reason=%s", chain, backend, id, reason)
	}
	u.proxy = &httputil.ReverseProxy{
		Transport: transport,
		ErrorLog:  log.New(io.Discard, "", 0),
		// Forward what the provider has sent without waiting for its EOF: a
		// caller may decode a complete reply and hang up before the body ends.
		FlushInterval: -1,
		Rewrite: func(p *httputil.ProxyRequest) {
			rewriteUpstream(p.Out, p.In, target, t, endpoint)
		},
		ModifyResponse: func(resp *http.Response) error {
			if resp.StatusCode >= 300 && resp.StatusCode < 400 || resp.StatusCode == http.StatusSwitchingProtocols {
				resp.Body.Close()
				return errors.New("upstream redirect/upgrade rejected")
			}
			if u.stats.status(resp.StatusCode) {
				failure("http_status")
			}
			resp.Header.Del("Set-Cookie")
			resp.Header.Del("Location")
			resp.Header.Del("Server")
			resp.Trailer = nil
			resp.Header.Del("Trailer")
			resp.Body = &boundedResponse{ReadCloser: resp.Body, remaining: 64 << 20, ctx: resp.Request.Context(), failure: failure, canceled: func() { u.stats.canceled.Add(1) }}
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, req *http.Request, err error) {
			// A caller that hangs up is not a provider failure.
			if clientCanceled(req.Context()) {
				u.stats.canceled.Add(1)
			} else {
				reason := "transport"
				if req.Context().Err() != nil {
					reason = "deadline"
				}
				failure(reason)
			}
			http.Error(w, "upstream unavailable; request outcome may be ambiguous", http.StatusBadGateway)
		},
	}
	return u
}

func rewriteUpstream(out, in *http.Request, target *url.URL, t Target, endpoint Endpoint) {
	out.URL.Scheme, out.URL.Host, out.Host = target.Scheme, target.Host, target.Host
	if endpoint.Suffix {
		out.URL.Path = strings.TrimSuffix(target.Path, "/") + strings.TrimPrefix(in.URL.Path, endpoint.Path)
	} else {
		out.URL.Path = target.Path
	}
	if out.URL.Path == "" {
		out.URL.Path = "/"
	}
	out.URL.RawPath, out.URL.RawQuery = "", in.URL.RawQuery
	out.GetBody, out.Trailer = nil, nil
	if out.Method == http.MethodGet && (out.Body == nil || out.Body == http.NoBody) {
		// A GET may be a broadcast on some chains. A non-rewindable empty body disables
		// Transport's implicit replay on a reused connection. Its GET body
		// probe still emits no payload, Content-Length or chunked framing.
		out.Body = io.NopCloser(strings.NewReader(""))
	}
	// Caller credentials, forwarding headers and idempotency hints never cross.
	out.Header = make(http.Header)
	for _, key := range []string{"Content-Type", "Content-Encoding", "Accept", "Accept-Encoding"} {
		if v := in.Header.Get(key); v != "" {
			out.Header.Set(key, v)
		}
	}
	out.Header.Set("User-Agent", "bifrost-rpc-router")
	for k, v := range t.Headers {
		out.Header.Set(k, v)
	}
}

var errResponseLimit = errors.New("response size limit")

// boundedResponse classifies how the body ended exactly once per request.
type boundedResponse struct {
	io.ReadCloser
	remaining int64
	ctx       context.Context
	failure   func(string)
	canceled  func()
	done      bool
}

func (b *boundedResponse) Read(p []byte) (int, error) {
	if int64(len(p)) > b.remaining+1 {
		p = p[:b.remaining+1]
	}
	n, err := b.ReadCloser.Read(p)
	b.remaining -= int64(n)
	if b.remaining < 0 {
		n = 0
		err = errResponseLimit
	}
	if err != nil && !b.done {
		b.done = true
		switch {
		case err == io.EOF:
		case err != errResponseLimit && clientCanceled(b.ctx):
			b.canceled()
		default:
			b.failure("response_body")
		}
	}
	return n, err
}

// Close before EOF means the copy to the caller stopped first.
func (b *boundedResponse) Close() error {
	if !b.done {
		b.done = true
		if clientCanceled(b.ctx) {
			b.canceled()
		}
	}
	return b.ReadCloser.Close()
}

func (r *router) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	if req.Method == http.MethodGet {
		switch req.URL.Path {
		case "/healthz":
			w.WriteHeader(http.StatusOK)
			return
		case "/readyz":
			jsonReply(w, map[string]string{"status": "configured", "scope": "router only; not provider or scanner health"})
			return
		case "/metrics":
			r.metrics(w)
			return
		}
	}
	if req.Method != http.MethodGet && req.Method != http.MethodPost || req.URL.IsAbs() || req.URL.RawPath != "" || req.Header.Get("Upgrade") != "" || len(req.Trailer) != 0 {
		http.Error(w, "unsupported request", 400)
		return
	}
	if _, err := url.ParseQuery(req.URL.RawQuery); err != nil {
		http.Error(w, "invalid query", 400)
		return
	}
	cleanPath := strings.TrimSuffix(req.URL.Path, "/")
	if !routePath.MatchString(cleanPath) {
		http.Error(w, "invalid path", 400)
		return
	}
	c := r.current.Load().catalog.config
	for name, ch := range c.Chains {
		for id, endpoint := range ch.Endpoints {
			if cleanPath != endpoint.Path && !(endpoint.Suffix && strings.HasPrefix(cleanPath, endpoint.Path+"/")) {
				continue
			}
			ctx, cancel := context.WithTimeout(req.Context(), time.Duration(ch.TimeoutSeconds)*time.Second)
			defer cancel()
			deadline, _ := ctx.Deadline()
			_ = http.NewResponseController(w).SetWriteDeadline(deadline.Add(time.Second))
			_ = http.NewResponseController(w).SetReadDeadline(deadline)
			if r.admission[name].acquire(ctx) != nil {
				w.Header().Set("Retry-After", "1")
				http.Error(w, "chain request capacity unavailable", 503)
				return
			}
			defer r.admission[name].release()
			// A queued request selects the latest route only when it is admitted.
			s := r.current.Load()
			u := s.upstreams[name][s.state.Routes[name]][id]
			if req.ContentLength > ch.MaxRequestBytes {
				http.Error(w, "request too large", 413)
				return
			}
			req = req.WithContext(ctx)
			req.Body = http.MaxBytesReader(w, req.Body, ch.MaxRequestBytes)
			start := time.Now()
			u.stats.requests.Add(1)
			defer func() { u.stats.durationNS.Add(uint64(time.Since(start))) }()
			live := s.catalog.config.Chains[name]
			if live.HeadCacheMS > 0 && u.serveCached(w, req, live) {
				return
			}
			u.proxy.ServeHTTP(w, req)
			return
		}
	}
	http.NotFound(w, req)
}

func (r *router) metrics(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	s := r.current.Load()
	fmt.Fprintf(w, "rpc_router_revision %d\nrpc_router_state_faults_total %d\n", s.state.Version, r.faults.Load())
	for chain, backends := range s.upstreams {
		a := r.admission[chain]
		fmt.Fprintf(w, "rpc_router_active{chain=%q} %d\nrpc_router_waiting{chain=%q} %d\nrpc_router_rejected_total{chain=%q} %d\n", chain, len(a.active), chain, len(a.waiting), chain, a.rejected.Load())
		for backend, endpoints := range backends {
			selected := 0
			if s.state.Routes[chain] == backend {
				selected = 1
			}
			fmt.Fprintf(w, "rpc_router_selected{chain=%q,backend=%q} %d\n", chain, backend, selected)
			for id, u := range endpoints {
				labels := fmt.Sprintf("chain=%q,backend=%q,endpoint=%q", chain, backend, id)
				fmt.Fprintf(w, "rpc_router_requests_total{%s} %d\nrpc_router_errors_total{%s} %d\nrpc_router_duration_seconds_sum{%s} %f\n", labels, u.stats.requests.Load(), labels, u.stats.errors.Load(), labels, float64(u.stats.durationNS.Load())/1e9)
				fmt.Fprintf(w, "rpc_router_client_canceled_total{%s} %d\nrpc_router_upstream_status_total{%s,class=\"4xx\"} %d\nrpc_router_upstream_status_total{%s,class=\"5xx\"} %d\n", labels, u.stats.canceled.Load(), labels, u.stats.status4xx.Load(), labels, u.stats.status5xx.Load())
				// Requests answered without a provider call; requests_total includes them.
				fmt.Fprintf(w, "rpc_router_head_cache_hits_total{%s} %d\n", labels, u.heads.hits.Load())
				// Read-ahead: early provider calls made, and how many were then asked for.
				fmt.Fprintf(w, "rpc_router_read_ahead_fetches_total{%s} %d\nrpc_router_read_ahead_hits_total{%s} %d\n", labels, u.ahead.fetches.Load(), labels, u.ahead.hits.Load())
			}
		}
	}
}

func (r *router) admin(w http.ResponseWriter, req *http.Request) {
	if req.Method == http.MethodGet && req.URL.Path == "/routes" {
		s := r.current.Load()
		w.Header().Set("ETag", strconv.Quote(strconv.FormatUint(s.state.Version, 10)))
		jsonReply(w, struct {
			State
			StateFaults uint64 `json:"state_faults"`
		}{s.state, r.faults.Load()})
		return
	}
	if req.Method != http.MethodPost || (req.URL.Path != "/switch" && req.URL.Path != "/reload") {
		http.NotFound(w, req)
		return
	}
	var command struct {
		Chain   string `json:"chain"`
		Backend string `json:"backend"`
	}
	b, err := io.ReadAll(io.LimitReader(req.Body, 4097))
	if err != nil || len(b) > 4096 || strictJSON(b, &command) != nil {
		http.Error(w, "invalid command", 400)
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	old := r.current.Load()
	if req.Header.Get("If-Match") != strconv.Quote(strconv.FormatUint(old.state.Version, 10)) {
		http.Error(w, "route revision changed; reread status", 412)
		return
	}
	s := State{Version: old.state.Version + 1, Catalog: old.state.Catalog, Routes: map[string]string{}}
	for k, v := range old.state.Routes {
		s.Routes[k] = v
	}
	next := &snapshot{catalog: old.catalog, state: s, upstreams: old.upstreams}
	if req.URL.Path == "/switch" {
		if old.catalog.config.Chains[command.Chain].Backends[command.Backend] == nil {
			http.Error(w, "unknown chain/backend", 400)
			return
		}
		s.Routes[command.Chain] = command.Backend
	} else {
		if command.Chain != "" || command.Backend != "" {
			http.Error(w, "reload takes an empty object", 400)
			return
		}
		c, err := loadCatalog(r.filename)
		if err != nil || !compatible(old.catalog.config, c.config) {
			http.Error(w, "invalid or incompatible catalog", 400)
			return
		}
		s.Catalog = c.digest
		if !validState(s, c) {
			http.Error(w, "reload removes an active backend", 400)
			return
		}
		next = buildSnapshot(c, s)
	}
	next.state = s
	if r.save(old.catalog.config.StateFile, s) != nil {
		if req.URL.Path == "/reload" {
			next.closeIdle()
		}
		// Reject this change only: this router is the only RPC path for every
		// chain, so a disk fault must cost one change, not all dispatch. The
		// revision is unchanged, so the operator can retry it. A rename may have
		// committed before fsync failed, so a restart before a successful retry
		// can come up on either route.
		r.faults.Add(1)
		log.Print("event=routing_state_fault")
		http.Error(w, "state commit failed; route unchanged, retry with the same revision", 503)
		return
	}
	r.current.Store(next)
	if req.URL.Path == "/reload" {
		old.closeIdle()
	}
	log.Printf("event=route_update revision=%d action=%s chain=%s backend=%s", s.Version, strings.TrimPrefix(req.URL.Path, "/"), command.Chain, command.Backend)
	w.Header().Set("ETag", strconv.Quote(strconv.FormatUint(s.Version, 10)))
	jsonReply(w, s)
}
