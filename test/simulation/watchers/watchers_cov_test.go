package watchers

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestSecurityEventsWatcherDetectsSecurityEventCov(t *testing.T) {
	// Scan(1, 0) tails forever with background workers, so the fake server is
	// intentionally left running for the rest of the test binary.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/thorchain/block" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		switch r.URL.Query().Get("height") {
		case "1":
			_, _ = w.Write([]byte(`{"header":{"height":1,"time":"2020-01-01T00:00:00Z"},
				"begin_block_events":[{"type":"rewards"}],
				"txs":[{"hash":"AA","result":{"events":[{"type":"message"}]}}]}`))
		case "2":
			_, _ = w.Write([]byte(`{"header":{"height":2,"time":"2020-01-01T00:00:06Z"},
				"end_block_events":[{"type":"security","msg":"bad"}]}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))

	origURL := thornodeURL
	thornodeURL = server.URL
	t.Cleanup(func() { thornodeURL = origURL })

	w := NewSecurityEvents()
	if w.Name != "SecurityEvents" || w.Interval != time.Second {
		t.Fatalf("unexpected watcher %+v", w)
	}

	deadline := time.Now().Add(5 * time.Second)
	for {
		err := w.Fn(nil)
		if err != nil {
			if !strings.Contains(err.Error(), "security event detected at height 2") {
				t.Fatalf("unexpected error %v", err)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("security event was never reported")
		}
	}

	// no further blocks: the watcher returns nil after its short poll window
	if err := w.Fn(nil); err != nil {
		t.Fatalf("expected idle watcher to return nil, got %v", err)
	}
}

func TestWatcherFetchErrorsCov(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/bad-status":
			w.WriteHeader(http.StatusTeapot)
		default:
			_, _ = w.Write([]byte(`{not json`))
		}
	}))
	defer server.Close()
	cl := server.Client()
	dead := &http.Client{Transport: roundTripErrCov{}}

	if _, err := fetchInvariant(dead, server.URL); err == nil {
		t.Fatal("invariant transport error expected")
	}
	if _, err := fetchInvariant(cl, server.URL+"/bad-json"); err == nil {
		t.Fatal("invariant decode error expected")
	}
	if _, err := fetchMimir(dead, server.URL); err == nil {
		t.Fatal("mimir transport error expected")
	}
	if _, err := fetchMimir(cl, server.URL+"/bad-json"); err == nil {
		t.Fatal("mimir decode error expected")
	}
	if _, err := fetchActiveSolvencyHalts(cl, server.URL+"/bad-status"); err == nil || !strings.Contains(err.Error(), "418") {
		t.Fatalf("solvency halt status error expected, got %v", err)
	}
	if _, err := fetchActiveRagnarokChains(cl, server.URL+"/bad-status"); err == nil {
		t.Fatal("ragnarok chains error expected")
	}
	if _, err := fetchPendingRagnarokChains(dead, server.URL); err == nil {
		t.Fatal("outbound transport error expected")
	}
	if _, err := fetchPendingRagnarokChains(cl, server.URL+"/bad-status"); err == nil || !strings.Contains(err.Error(), "418") {
		t.Fatalf("outbound status error expected, got %v", err)
	}
	if _, err := fetchPendingRagnarokChains(cl, server.URL+"/bad-json"); err == nil {
		t.Fatal("outbound decode error expected")
	}
	if _, err := fetchRagnarokInProgress(dead, server.URL); err == nil {
		t.Fatal("ragnarok transport error expected")
	}
	if _, err := fetchRagnarokInProgress(cl, server.URL+"/bad-status"); err == nil || !strings.Contains(err.Error(), "418") {
		t.Fatalf("ragnarok status error expected, got %v", err)
	}
	if _, err := fetchRagnarokInProgress(cl, server.URL+"/bad-json"); err == nil {
		t.Fatal("ragnarok decode error expected")
	}
}

type roundTripErrCov struct{}

func (roundTripErrCov) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("transport down")
}
