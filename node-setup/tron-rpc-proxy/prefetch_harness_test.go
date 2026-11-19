package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestPrefetchHarnessResponseHeaderBudget(t *testing.T) {
	for _, mode := range []string{"local", "external"} {
		t.Run(mode, func(t *testing.T) {
			p, _, local, external := fixture(t)
			p.setMode(mode)
			n, upstream := local, p.local
			if mode == "external" {
				n, upstream = external, p.external
			}
			// Delay actual response headers, not the wrapper before RoundTrip.
			// This fits the workload's 1s budget but exceeds the fixture's 100ms.
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				select {
				case <-time.After(200 * time.Millisecond):
					n.handle(w, r)
				case <-r.Context().Done():
				}
			}))
			defer server.Close()
			api, err := url.Parse(server.URL)
			if err != nil {
				t.Fatal(err)
			}
			upstream.API = api
			configurePrefetchUpstreams(t, p)
			transport := upstream.Client.Transport.(*http.Transport)
			if upstream.Client.Timeout != prefetchUpstreamTimeout || transport.ResponseHeaderTimeout != prefetchUpstreamTimeout || transport.TLSHandshakeTimeout != prefetchUpstreamTimeout {
				t.Errorf("workload timeout mismatch: client=%s response_headers=%s tls=%s want=%s", upstream.Client.Timeout, transport.ResponseHeaderTimeout, transport.TLSHandshakeTimeout, prefetchUpstreamTimeout)
			}
			load := &prefetchLoadTransport{base: transport}
			upstream.Client.Transport = load
			w := request(p, "/wallet/getnowblock", []byte("null"))
			if w.Code != http.StatusOK || !validResponse("/wallet/getnowblock", []byte("null"), w.Body.Bytes()) || p.fallbacks.Load() != 0 || p.currentMode() != mode || p.ambiguous.Load() != 0 {
				t.Fatalf("headers within workload budget rejected: HTTP %d body=%q mode=%s fallbacks=%d ambiguous=%d transport_errors=%q",
					w.Code, w.Body.String(), p.currentMode(), p.fallbacks.Load(), p.ambiguous.Load(), load.failureSummary())
			}
		})
	}
}

func TestPrefetchHarnessRejectsUnfreshHealth(t *testing.T) {
	for _, mode := range []string{"local", "external"} {
		for _, age := range []time.Duration{-time.Minute, time.Minute} {
			t.Run(mode+"/age="+age.String(), func(t *testing.T) {
				p, _, _, _ := fixture(t)
				p.setMode(mode)
				configurePrefetchUpstreams(t, p)
				// A future Checked value models a backward wall-clock step without
				// changing the host clock. Both bounds must remain fail-closed.
				p.healthMu.Lock()
				p.health.Checked = time.Now().UTC().Add(-age)
				p.healthMu.Unlock()
				local := &prefetchLoadTransport{base: p.local.Client.Transport}
				external := &prefetchLoadTransport{base: p.external.Client.Transport}
				p.local.Client.Transport, p.external.Client.Transport = local, external
				w := request(p, "/wallet/getnowblock", []byte("null"))
				if w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), "upstream health unverified") {
					t.Fatalf("unfresh health served: HTTP %d body=%q", w.Code, w.Body.String())
				}
				if p.fallbacks.Load() != 0 || p.currentMode() != mode || p.ambiguous.Load() != 0 || local.peak.Load() != 0 || external.peak.Load() != 0 {
					t.Fatal("unfresh request dispatched upstream or changed routing")
				}
				t.Logf("HTTP %d body=%q health_age=%s mode=%s fallbacks=%d ambiguous=%d", w.Code, w.Body.String(), time.Since(p.snapshot().Checked), p.currentMode(), p.fallbacks.Load(), p.ambiguous.Load())
			})
		}
	}
}
