package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

type journal interface {
	mode() (string, error)
	setMode(string) error
	get(string) (*Entry, error)
	save(*Entry) error
}

type Proxy struct {
	cfg                                        Config
	store                                      journal
	local, external                            *Upstream
	unsafe                                     atomic.Bool
	requests, fallbacks, broadcasts, ambiguous atomic.Uint64
	broadcastRetries                           atomic.Uint64
	healthMu                                   sync.RWMutex
	health                                     Health
	probeMu                                    sync.Mutex
	modeMu                                     sync.Mutex
	writeGate                                  chan struct{}
	admission                                  *requestAdmission
	logger                                     *slog.Logger
	diagnosticMu                               sync.Mutex
	diagnostics                                Diagnostics
	now                                        func() time.Time
	modeEpoch                                  uint64
	failbackEligible                           bool
	recovery                                   recoveryWindow
	autoFailbacks                              atomic.Uint64
}

func newProxy(c Config, s journal, local, external *Upstream) *Proxy {
	return &Proxy{cfg: c, store: s, local: local, external: external, writeGate: make(chan struct{}, 1), admission: newRequestAdmission(), logger: slog.New(slog.NewJSONHandler(os.Stderr, nil)), now: time.Now}
}

func (p *Proxy) setMode(mode string) bool {
	return p.setModeFor(mode, "operator", reasonOperator, "operator")
}

func (p *Proxy) setModeFor(mode, stage string, reason failureReason, upstream string) bool {
	return p.setModeCheck(mode, checkResult{Stage: stage, Reason: reason, Upstream: upstream})
}

func (p *Proxy) setModeCheck(mode string, check checkResult) bool {
	p.modeMu.Lock()
	defer p.modeMu.Unlock()
	return p.setModeLocked(mode, check)
}

func (p *Proxy) setModeLocked(mode string, check checkResult) bool {
	if p.unsafe.Load() {
		return false
	}
	previous, err := p.store.mode()
	if err != nil {
		p.unsafe.Store(true)
		return false
	}
	if previous == mode {
		// The policy update below must also run for an already-external hold.
		p.updateRecoveryPolicyLocked(mode, previous, check)
		return true
	}
	if p.store.setMode(mode) != nil {
		p.unsafe.Store(true)
		return false
	}
	p.updateRecoveryPolicyLocked(mode, previous, check)
	p.recordDiagnostic("routing_changed", check, p.snapshot(), previous, mode)
	return true
}

func (p *Proxy) currentMode() string {
	mode, err := p.store.mode()
	if err != nil {
		p.unsafe.Store(true)
		return "unavailable"
	}
	return mode
}

func (p *Proxy) snapshot() Health { p.healthMu.RLock(); defer p.healthMu.RUnlock(); return p.health }
func (p *Proxy) healthy() bool {
	return p.routeHealthy(p.currentMode())
}

func (p *Proxy) routeHealthy(mode string) bool {
	h := p.snapshot()
	if p.unsafe.Load() || !p.fresh(h) {
		return false
	}
	return mode == "local" && h.Local || mode == "external" && h.External
}

func (p *Proxy) monitor(ctx context.Context) {
	ticker := time.NewTicker(time.Duration(p.cfg.PollSeconds) * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.probe(ctx)
		}
	}
}

func failure(w http.ResponseWriter, code int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	if code == 503 {
		w.Header().Set("Retry-After", "2")
	}
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
}

func reply(w http.ResponseWriter, data []byte) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(200)
	_, _ = w.Write(data)
}

func bulkReceiptHeight(body []byte) (int64, bool) {
	if len(body) > 65536 || uniqueJSON(body) != nil {
		return 0, false
	}
	// Exact keys avoid encoding/json's case-insensitive aliases differing from Java.
	var q map[string]json.RawMessage
	var num *int64
	if json.Unmarshal(body, &q) != nil || len(q) != 1 || json.Unmarshal(q["num"], &num) != nil || num == nil || *num < 0 {
		return 0, false
	}
	return *num, true
}

func allowed(method, route string, body []byte) bool {
	if uniqueJSON(body) != nil {
		return false
	}
	if method != "POST" && !(route == "/wallet/getchainparameters" && method == "GET") {
		return false
	}
	switch route {
	case "/wallet/getnowblock", "/wallet/getchainparameters":
		return bytes.Equal(bytes.TrimSpace(body), []byte("null")) || bytes.Equal(bytes.TrimSpace(body), []byte("{}"))
	case "/wallet/gettransactioninfobyblocknum":
		_, ok := bulkReceiptHeight(body)
		return ok
	case "/wallet/getblockbynum":
		var q struct {
			Num *int64 `json:"num"`
		}
		return json.Unmarshal(body, &q) == nil && q.Num != nil && *q.Num >= 0
	case "/wallet/gettransactionbyid", "/wallet/gettransactioninfobyid":
		var q struct {
			Value string `json:"value"`
		}
		return json.Unmarshal(body, &q) == nil && validID(q.Value)
	case "/wallet/getaccount", "/wallet/estimateenergy", "/wallet/createtransaction", "/wallet/triggersmartcontract", "/wallet/broadcasttransaction":
		var q map[string]json.RawMessage
		return json.Unmarshal(body, &q) == nil && len(q) > 0
	case "/jsonrpc":
		var q struct {
			Version string            `json:"jsonrpc"`
			ID      json.RawMessage   `json:"id"`
			Method  string            `json:"method"`
			Params  []json.RawMessage `json:"params"`
		}
		if json.Unmarshal(body, &q) != nil || q.Version != "2.0" || len(q.ID) == 0 || string(q.ID) == "null" {
			return false
		}
		var idNumber json.Number
		var idString string
		if json.Unmarshal(q.ID, &idNumber) != nil && json.Unmarshal(q.ID, &idString) != nil {
			return false
		}
		return (q.Method == "eth_blockNumber" && len(q.Params) == 0) || (q.Method == "eth_call" && len(q.Params) == 2)
	}
	return false
}

func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.RawQuery != "" {
		failure(w, 400, "query parameters disabled")
		return
	}
	if r.Method == "GET" {
		switch r.URL.Path {
		case "/healthz":
			reply(w, []byte(`{"alive":true}`))
			return
		case "/readyz":
			if !p.healthy() {
				failure(w, 503, "not ready")
				return
			}
			reply(w, []byte(`{"ready":true}`))
			return
		case "/metrics":
			w.Header().Set("Content-Type", "text/plain; version=0.0.4")
			_, _ = fmt.Fprintf(w, "tron_proxy_requests_total %d\ntron_proxy_fallbacks_total %d\ntron_proxy_broadcast_attempts_total %d\ntron_proxy_ambiguous_total %d\ntron_proxy_external_mode %d\ntron_proxy_ready %d\n", p.requests.Load(), p.fallbacks.Load(), p.broadcasts.Load(), p.ambiguous.Load(), boolInt(p.currentMode() == "external"), boolInt(p.healthy()))
			_, _ = fmt.Fprintf(w, "tron_proxy_broadcast_retries_total %d\n", p.broadcastRetries.Load())
			_, _ = fmt.Fprintf(w, "tron_proxy_auto_failbacks_total %d\n", p.autoFailbacks.Load())
			_, _ = fmt.Fprintf(w, "tron_proxy_active_requests %d\ntron_proxy_waiting_requests %d\ntron_proxy_admission_rejected_total %d\ntron_proxy_admission_timeouts_total %d\ntron_proxy_admission_canceled_total %d\n", len(p.admission.active), len(p.admission.waiting), p.admission.rejected.Load(), p.admission.timedOut.Load(), p.admission.canceled.Load())
			return
		}
	}
	if err := p.admission.acquire(r.Context()); err != nil {
		message := "request canceled before admission"
		if err == errAdmissionFull || err == errAdmissionTimeout {
			message = err.Error()
		}
		failure(w, 503, message)
		return
	}
	defer p.admission.release()
	p.requests.Add(1)
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 65536))
	if r.Context().Err() != nil {
		failure(w, 503, "request canceled before dispatch")
		return
	}
	if err != nil {
		failure(w, 413, "request too large")
		return
	}
	if r.Method == "GET" && len(body) == 0 {
		body = []byte("null")
	}
	if !allowed(r.Method, r.URL.Path, body) {
		failure(w, 400, "unsupported request")
		return
	}
	isWrite := r.URL.Path == "/wallet/broadcasttransaction"
	if !p.cfg.BroadcastEnabled && (isWrite || r.URL.Path == "/wallet/createtransaction" || r.URL.Path == "/wallet/triggersmartcontract") {
		failure(w, 403, "broadcasts are disabled")
		return
	}
	if !p.healthy() {
		failure(w, 503, "upstream health unverified")
		return
	}
	if isWrite {
		p.broadcast(w, r, body)
		return
	}
	mode := p.currentMode()
	if p.unsafe.Load() {
		failure(w, 503, "journal unavailable")
		return
	}
	if mode == "local" && !p.routeHealthy("local") {
		if !p.setModeFor("external", "request", reasonUnverified, "local") {
			failure(w, 503, "journal unavailable")
			return
		}
		mode = "external"
	}
	bulkReceipts := r.URL.Path == "/wallet/gettransactioninfobyblocknum"
	if mode == "local" && (r.URL.Path == "/wallet/getblockbynum" || bulkReceipts) {
		var q struct {
			Num int64 `json:"num"`
		}
		_ = json.Unmarshal(body, &q)
		floor := p.cfg.BlockFloor
		if bulkReceipts {
			floor = max(floor, p.cfg.ReceiptFloor)
		}
		if q.Num < floor {
			mode = "external"
		}
	}
	if mode == "local" {
		data, err := p.local.call(r.Context(), r.Method, r.URL.Path, body)
		if err == nil && validResponse(r.URL.Path, body, data) {
			reply(w, data)
			return
		}
		// Bulk support/history is optional. Only independent health checks may
		// evict local for this failure; the external retry is request-scoped.
		if !bulkReceipts && !p.setModeCheck("external", (checkResult{Stage: "read", Upstream: "local"}).failed(err)) {
			failure(w, 503, "journal unavailable")
			return
		}
		p.fallbacks.Add(1)
	}
	// Historical routing and a failed local request can reach here after the
	// initial selected-route gate. Recheck the actual target before dispatch.
	if !p.routeHealthy("external") {
		failure(w, 503, "external upstream health unverified")
		return
	}
	data, err := p.external.call(r.Context(), r.Method, r.URL.Path, body)
	if err != nil || !validResponse(r.URL.Path, body, data) {
		p.ambiguous.Add(1)
		failure(w, 503, "upstream result indeterminate")
		return
	}
	reply(w, data)
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func (p *Proxy) admin(w http.ResponseWriter, r *http.Request) {
	if r.Method == "GET" && r.URL.Path == "/status" {
		data, _ := json.Marshal(struct {
			Mode        string         `json:"mode"`
			Health      Health         `json:"health"`
			Unsafe      bool           `json:"unsafe"`
			Diagnostics Diagnostics    `json:"diagnostics"`
			Failback    FailbackStatus `json:"failback"`
		}{p.currentMode(), p.snapshot(), p.unsafe.Load(), p.recentDiagnostics(), p.failbackStatus()})
		reply(w, data)
		return
	}
	if r.Method != "POST" {
		failure(w, 400, "unsupported operator request")
		return
	}
	switch r.URL.Path {
	case "/external":
		if !p.setMode("external") {
			failure(w, 503, "journal unavailable")
			return
		}
	case "/arm-local":
		p.probeMu.Lock()
		defer p.probeMu.Unlock()
		h := p.probeLocked(r.Context())
		if !h.Local || !h.External || !h.ChainCheck.OK || !p.fresh(h) || p.unsafe.Load() {
			failure(w, 503, "local admission failed")
			return
		}
		if !p.setMode("local") {
			failure(w, 503, "journal unavailable")
			return
		}
	default:
		failure(w, 400, "unsupported operator request")
		return
	}
	reply(w, []byte(`{"ok":true}`))
}
