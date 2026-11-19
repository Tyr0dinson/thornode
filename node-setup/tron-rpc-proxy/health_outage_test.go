package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

type externalProbeFailureTransport struct {
	base http.RoundTripper
	kind string
}

func (t externalProbeFailureTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Path != "/wallet/getnowblock" {
		return t.base.RoundTrip(r)
	}
	if t.kind == "timeout" {
		return nil, os.ErrDeadlineExceeded
	}
	code, body := http.StatusServiceUnavailable, `{}`
	if t.kind == "invalid-json" {
		code, body = http.StatusOK, `not-json`
	}
	return &http.Response{StatusCode: code, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
}

func TestExternalHealthFailurePreservesAdmittedHealthyLocal(t *testing.T) {
	for _, kind := range []string{"http-503", "timeout", "invalid-json", "stale-head"} {
		t.Run(kind, func(t *testing.T) {
			p, _, _, external := fixture(t)
			if !p.setMode("local") {
				t.Fatal("cannot admit healthy local fixture")
			}
			originalTransport := p.external.Client.Transport
			if kind == "stale-head" {
				external.mu.Lock()
				external.age = time.Minute
				external.mu.Unlock()
			} else {
				p.external.Client.Transport = externalProbeFailureTransport{base: originalTransport, kind: kind}
			}
			h := p.probe(context.Background())
			_, _, independentlyHealthy := p.probeNode(context.Background(), p.local)
			if !independentlyHealthy {
				t.Fatal("local control node is unexpectedly unhealthy")
			}
			if h.External || !h.Local || p.currentMode() != "local" || !p.healthy() {
				t.Fatalf("external %s abandoned healthy local node: health=%+v mode=%s ready=%t", kind, h, p.currentMode(), p.healthy())
			}
			if w := request(p, "/wallet/getnowblock", []byte(`null`)); w.Code != 200 {
				t.Fatalf("healthy local read refused: %d %s", w.Code, w.Body.String())
			}
			if p.fallbacks.Load() != 0 || p.ambiguous.Load() != 0 {
				t.Fatal("external-only health failure changed request fallback counters")
			}
			p.external.Client.Transport = originalTransport
			external.mu.Lock()
			external.age = 0
			external.mu.Unlock()
			h = p.probe(context.Background())
			if !h.External || !h.Local || p.currentMode() != "local" {
				t.Fatalf("unexpected recovery state: %+v %s", h, p.currentMode())
			}
		})
	}
}

func TestExternalHealthFailureDoesNotAuthorizeLocalAdmission(t *testing.T) {
	p, _, _, _ := fixture(t)
	p.external.Client.Transport = externalProbeFailureTransport{base: p.external.Client.Transport, kind: "http-503"}
	p.probe(context.Background())
	if p.currentMode() != "external" || p.healthy() {
		t.Fatal("external-mode readiness must still depend on external health")
	}
	w := httptest.NewRecorder()
	p.admin(w, httptest.NewRequest("POST", "/arm-local", nil))
	if w.Code != 503 || p.currentMode() != "external" {
		t.Fatalf("unverified local admission permitted: %d %s", w.Code, p.currentMode())
	}
	if w := request(p, "/wallet/getnowblock", []byte(`null`)); w.Code != 503 {
		t.Fatalf("external outage must not cause automatic local failback: %d", w.Code)
	}
}
