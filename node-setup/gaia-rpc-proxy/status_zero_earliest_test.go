package main

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"
)

func TestGaiaStatusEarliestHeightValidation(t *testing.T) {
	for _, tc := range []struct {
		name     string
		earliest any
		latest   string
		want     bool
	}{
		{"omitted", nil, "100", true},
		{"empty", "", "100", true},
		{"zero_sentinel", "0", "100", true},
		{"positive", "50", "100", true},
		{"negative", "-1", "100", false},
		{"noncanonical_zero", "00", "100", false},
		{"fraction", "1.5", "100", false},
		{"nonnumeric", "unknown", "100", false},
		{"overflow", "9223372036854775808", "100", false},
		{"numeric_type", 0, "100", false},
		{"zero_current_height", "0", "0", false},
		{"missing_current_height", "0", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			q := gaiaReadRequest("status", 0)
			syncInfo := map[string]any{"latest_block_height": tc.latest, "latest_block_time": time.Now().UTC().Format(time.RFC3339Nano), "catching_up": false}
			if tc.earliest != nil {
				syncInfo["earliest_block_height"] = tc.earliest
			}
			body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": q.ID, "result": map[string]any{"node_info": map[string]any{"network": "cosmoshub-4"}, "sync_info": syncInfo}})
			if err != nil {
				t.Fatal(err)
			}
			var e gaiaEnvelope
			got := json.Unmarshal(body, &e) == nil && gaiaValidate(q, &e)
			if got != tc.want {
				t.Fatalf("status validation = %v, want %v", got, tc.want)
			}
		})
	}
	if _, ok := gaiaHeight("0"); ok {
		t.Fatal("zero sentinel must not weaken positive block-height validation")
	}
}

func gaiaStatusZeroEarliest(method string, _ int64, out map[string]any) {
	if method == "status" {
		out["result"].(map[string]any)["sync_info"].(map[string]any)["earliest_block_height"] = "0"
	}
}

func TestGaiaStatusZeroEarliestReadiness(t *testing.T) {
	p, a := newGaiaTestNode(t, 50), newGaiaTestNode(t, 1)
	p.update(func() { p.mutate = gaiaStatusZeroEarliest })
	g := gaiaTestProxy(t, p, a)
	w := httptest.NewRecorder()
	handler(g).ServeHTTP(w, httptest.NewRequest("GET", "/readyz", nil))
	if w.Code != 200 {
		t.Fatalf("zero-earliest primary must remain ready: %d %s", w.Code, w.Body.String())
	}
	p.update(func() { p.network = "wrong-chain" })
	w = httptest.NewRecorder()
	handler(g).ServeHTTP(w, httptest.NewRequest("GET", "/readyz", nil))
	if w.Code != 503 {
		t.Fatal("zero sentinel bypassed chain identity check")
	}
}

func TestGaiaStatusZeroEarliestHistoricalRouting(t *testing.T) {
	p, a := newGaiaTestNode(t, 50), newGaiaTestNode(t, 1)
	p.update(func() { p.mutate = gaiaStatusZeroEarliest })
	a.update(func() { a.mutate = gaiaStatusZeroEarliest })
	g := gaiaTestProxy(t, p, a)
	gaiaTestOK(t, gaiaTestRead(g, "block", 10))
	gaiaTestOK(t, gaiaTestRead(g, "block_results", 10))
	if g.cachedProof().floor != 50 || p.count("block", 50) == 0 || p.count("block_results", 50) == 0 {
		t.Fatal("zero advertisement must still discover and verify actual history boundary")
	}
	if a.count("block", 10) == 0 || a.count("block_results", 10) == 0 || p.count("block_results", 10) != 0 {
		t.Fatal("historical reads did not use the verified archive route")
	}
	gaiaTestOK(t, gaiaTestRead(g, "block", 50))
	gaiaTestOK(t, gaiaTestRead(g, "block_results", 50))
	if a.count("block", 50) != 0 || a.count("block_results", 50) != 0 {
		t.Fatal("zero advertisement incorrectly routed retained history to archive")
	}
}
