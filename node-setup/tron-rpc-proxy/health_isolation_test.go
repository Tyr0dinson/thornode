package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type probeFaultTransport struct {
	base        http.RoundTripper
	stage, body string
	code        int
	err         error
}

func (f probeFaultTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	body, _ := io.ReadAll(r.Body)
	r.Body = io.NopCloser(bytes.NewReader(body))
	match := f.stage == "common" && r.URL.Path == "/wallet/getblockbynum" && !bytes.Equal(body, []byte(`{"num":0}`))
	match = match || f.stage == "rpc-genesis" && bytes.Contains(body, []byte(`"eth_getBlockByNumber"`))
	match = match || f.stage == "finalized" && bytes.Contains(body, []byte(`"eth_blockNumber"`))
	if !match {
		return f.base.RoundTrip(r)
	}
	if f.err != nil {
		return nil, f.err
	}
	return &http.Response{StatusCode: f.code, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(f.body)), Request: r}, nil
}

func externalOutage(p *Proxy) {
	p.external.Client.Transport = externalProbeFailureTransport{base: p.external.Client.Transport, kind: "http-503"}
	p.probe(context.Background())
}

func TestCommonBlockUnavailableVersusInvalid(t *testing.T) {
	for _, tc := range []struct {
		name, upstream, body string
		code                 int
		err                  error
		wantLocal            bool
	}{
		{name: "external-transport", upstream: "external", err: errors.New("private URL and credential"), wantLocal: true},
		{name: "external-http", upstream: "external", code: 503, body: `{}`, wantLocal: true},
		{name: "external-missing", upstream: "external", code: 200, body: `{}`, wantLocal: true},
		{name: "external-malformed", upstream: "external", code: 200, body: `not-json`, wantLocal: true},
		{name: "external-wrong-height", upstream: "external", code: 200, body: `{"blockID":"0000000000000001aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","block_header":{"raw_data":{"number":1}}}`, wantLocal: true},
		{name: "external-invalid-id", upstream: "external", code: 200, body: `{"blockID":"invalid","block_header":{"raw_data":{"number":99980}}}`, wantLocal: true},
		{name: "local-http", upstream: "local", code: 503, body: `{}`},
		{name: "local-missing", upstream: "local", code: 200, body: `{}`},
		{name: "local-malformed", upstream: "local", code: 200, body: `not-json`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, _, _, _ := fixture(t)
			p.setMode("local")
			u := p.external
			if tc.upstream == "local" {
				u = p.local
			}
			u.Client.Transport = probeFaultTransport{base: u.Client.Transport, stage: "common", code: tc.code, body: tc.body, err: tc.err}
			h := p.probe(context.Background())
			if h.Local != tc.wantLocal || (p.currentMode() == "local") != tc.wantLocal {
				t.Fatalf("incorrect common-block decision: %+v, mode=%s", h, p.currentMode())
			}
			if tc.upstream == "external" && (h.External || !p.healthy()) {
				t.Fatal("invalid external evidence did not isolate availability to local")
			}
			p.setMode("external")
			w := httptest.NewRecorder()
			p.admin(w, httptest.NewRequest("POST", "/arm-local", nil))
			if w.Code != 503 {
				t.Fatal("incomplete common evidence allowed admission")
			}
			if p.currentMode() != "external" || p.healthy() != h.External {
				t.Fatal("cold admission or external readiness ignored evidence")
			}
		})
	}
}

func TestLocalFaultStillClosesDuringExternalOutage(t *testing.T) {
	for _, kind := range []string{"stale", "missing", "genesis", "rpc-genesis", "finalized"} {
		t.Run(kind, func(t *testing.T) {
			p, _, local, _ := fixture(t)
			p.setMode("local")
			externalOutage(p)
			local.mu.Lock()
			switch kind {
			case "stale":
				local.age = time.Minute
			case "missing":
				local.missing = true
			case "genesis":
				local.genesis = strings.Repeat("0", 64)
			}
			local.mu.Unlock()
			if kind == "rpc-genesis" || kind == "finalized" {
				p.local.Client.Transport = probeFaultTransport{base: p.local.Client.Transport, stage: kind, code: 200, body: `{"jsonrpc":"2.0","id":1,"result":null}`}
			}
			h := p.probe(context.Background())
			if h.Local || p.currentMode() != "external" || p.healthy() {
				t.Fatalf("local fault tolerated: %+v", h)
			}
			if w := request(p, "/wallet/getnowblock", []byte(`null`)); w.Code != 503 {
				t.Fatal("served faulty local")
			}
		})
	}
}

func TestExternalIdentityFailureDoesNotProveLocalFault(t *testing.T) {
	p, _, _, external := fixture(t)
	p.setMode("local")
	external.mu.Lock()
	external.genesis = strings.Repeat("0", 64)
	external.mu.Unlock()
	h := p.probe(context.Background())
	if h.External || !h.Local || p.currentMode() != "local" || !p.healthy() {
		t.Fatalf("external identity blamed on local: %+v", h)
	}
	p.setMode("external")
	w := httptest.NewRecorder()
	p.admin(w, httptest.NewRequest("POST", "/arm-local", nil))
	if w.Code != 503 || p.currentMode() != "external" {
		t.Fatal("bad external identity authorized cold admission")
	}
}

func TestUnhealthyExternalNeverReceivesReadFallback(t *testing.T) {
	for _, kind := range []string{"historical-floor", "missing-receipt", "ambiguous-lookup", "local-read-failure"} {
		t.Run(kind, func(t *testing.T) {
			p, _, local, external := fixture(t)
			p.setMode("local")
			externalOutage(p)
			external.mu.Lock()
			external.reads = map[string]int{}
			external.mu.Unlock()
			route, body := "/wallet/gettransactioninfobyid", []byte(`{"value":"`+strings.Repeat("1", 64)+`"}`)
			switch kind {
			case "historical-floor":
				p.cfg.BlockFloor = 90000
				route, body = "/wallet/getblockbynum", []byte(`{"num":89999}`)
			case "ambiguous-lookup":
				route = "/wallet/gettransactionbyid"
			case "local-read-failure":
				route, body = "/wallet/getnowblock", []byte(`null`)
			}
			local.mu.Lock()
			local.missing = true
			local.mu.Unlock()
			if w := request(p, route, body); w.Code != 503 {
				t.Fatalf("unknown result not refused: %d", w.Code)
			}
			external.mu.Lock()
			defer external.mu.Unlock()
			if len(external.reads) != 0 {
				t.Fatalf("dispatched to unhealthy external: %+v", external.reads)
			}
		})
	}
}

func TestBroadcastWhileExternalUnhealthy(t *testing.T) {
	for _, result := range []string{"success", "disconnect", "reconciled", "duplicate"} {
		t.Run(result, func(t *testing.T) {
			p, s, local, external := fixture(t)
			p.setMode("local")
			externalOutage(p)
			local.mu.Lock()
			local.response = result
			if result == "reconciled" {
				local.response, local.lookup = "disconnect", true
			}
			local.mu.Unlock()
			external.mu.Lock()
			external.reads = map[string]int{}
			external.mu.Unlock()
			body := signed(t, false, false, time.Now().Add(time.Hour))
			w := request(p, "/wallet/broadcasttransaction", body)
			want := 503
			if result == "success" || result == "reconciled" {
				want = 200
			}
			if w.Code != want {
				t.Fatalf("broadcast got %d want %d", w.Code, want)
			}
			entry, _ := parseSigned(body)
			stored, _ := s.get(entry.ID)
			if stored == nil || stored.Attempts != 1 || stored.Accepted != (want == 200) || !bytes.Equal(stored.Body, body) {
				t.Fatalf("journal safety changed: %+v", stored)
			}
			local.mu.Lock()
			defer local.mu.Unlock()
			external.mu.Lock()
			defer external.mu.Unlock()
			if len(local.writes) != 1 || len(external.writes) != 0 || len(external.reads) != 0 {
				t.Fatal("unhealthy external contacted or extra broadcast")
			}
		})
	}
}

func TestLocalOutageRecoveryDoesNotAutomaticallyReadmit(t *testing.T) {
	p, _, local, _ := fixture(t)
	p.setMode("local")
	externalTransport := p.external.Client.Transport
	externalOutage(p)
	local.mu.Lock()
	local.age = time.Minute
	local.mu.Unlock()
	p.probe(context.Background())
	local.mu.Lock()
	local.age = 0
	local.mu.Unlock()
	p.external.Client.Transport = externalTransport
	p.probe(context.Background())
	if p.currentMode() != "external" {
		t.Fatal("recovery automatically admitted local")
	}
}

func TestLocalSelectedRouteRejectsStaleSnapshot(t *testing.T) {
	p, _, _, _ := fixture(t)
	p.setMode("local")
	externalOutage(p)
	p.healthMu.Lock()
	p.health.Checked = time.Now().Add(-time.Minute)
	p.healthMu.Unlock()
	for _, route := range []string{"/readyz", "/wallet/getnowblock", "/wallet/broadcasttransaction"} {
		w := httptest.NewRecorder()
		method, body := "POST", []byte(`null`)
		if route == "/readyz" {
			method = "GET"
		}
		if route == "/wallet/broadcasttransaction" {
			body = signed(t, false, false, time.Now().Add(time.Hour))
		}
		p.ServeHTTP(w, httptest.NewRequest(method, route, bytes.NewReader(body)))
		if w.Code != 503 {
			t.Fatalf("stale snapshot allowed %s", route)
		}
	}
}

// A final transport assertion also protects against converting JSON-RPC
// errors/missing values into a successful independent local health check.
func TestIndependentFinalizedAndGenesisChecks(t *testing.T) {
	for _, stage := range []string{"rpc-genesis", "finalized"} {
		for _, body := range []string{`{}`, `{"jsonrpc":"2.0","id":1,"error":{"code":-1}}`, `{"jsonrpc":"2.0","id":2,"result":"0x1868c"}`} {
			p, _, _, _ := fixture(t)
			p.local.Client.Transport = probeFaultTransport{base: p.local.Client.Transport, stage: stage, code: 200, body: body}
			_, _, ok := p.probeNode(context.Background(), p.local)
			if ok {
				t.Fatalf("accepted invalid local %s evidence", stage)
			}
		}
	}
}

func TestSelectedRouteReadinessDuringExternalOutage(t *testing.T) {
	p, _, _, _ := fixture(t)
	p.setMode("local")
	externalOutage(p)
	w := httptest.NewRecorder()
	p.admin(w, httptest.NewRequest("GET", "/status", nil))
	var state struct {
		Mode   string `json:"mode"`
		Health Health `json:"health"`
	}
	if json.Unmarshal(w.Body.Bytes(), &state) != nil || state.Mode != "local" || !state.Health.Local || state.Health.External {
		t.Fatal("status did not expose isolated local health")
	}
	for _, mode := range []string{"local", "external"} {
		p.setMode(mode)
		w = httptest.NewRecorder()
		p.ServeHTTP(w, httptest.NewRequest("GET", "/readyz", nil))
		want, metric := 200, "tron_proxy_ready 1\n"
		if mode == "external" {
			want, metric = 503, "tron_proxy_ready 0\n"
		}
		if w.Code != want {
			t.Fatalf("%s readiness got %d want %d", mode, w.Code, want)
		}
		w = httptest.NewRecorder()
		p.ServeHTTP(w, httptest.NewRequest("GET", "/metrics", nil))
		if !strings.Contains(w.Body.String(), metric) {
			t.Fatal("readiness metric ignored selected route")
		}
	}
}
