package main

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestFastPathDoesNotRefetchHistoricalBlock(t *testing.T) {
	p, a := newGaiaTestNode(t, 50), newGaiaTestNode(t, 1)
	g := gaiaTestProxy(t, p, a)
	gaiaTestOK(t, gaiaTestRead(g, "block", 40))
	gaiaTestOK(t, gaiaTestRead(g, "block_results", 40))
	if got := a.count("block", 40); got != 1 {
		t.Fatalf("historical block fetched %d times; want once", got)
	}
}

func TestFastPathAdmittedReadSurvivesProofExpiry(t *testing.T) {
	p, a := newGaiaTestNode(t, 50), newGaiaTestNode(t, 1)
	g := gaiaTestProxy(t, p, a)
	var clock atomic.Int64
	clock.Store(time.Now().UnixNano())
	g.now = func() time.Time { return time.Unix(0, clock.Load()) }
	proof, ok := g.agreement(context.Background(), 50)
	if !ok {
		t.Fatal("fixture agreement failed")
	}
	clock.Store(proof.until.Add(-time.Millisecond).UnixNano())
	a.update(func() {
		a.mutate = func(_ string, height int64, _ map[string]any) {
			if height == 40 {
				clock.Add(int64(2 * time.Millisecond))
			}
		}
	})
	gaiaTestOK(t, gaiaTestRead(g, "block", 40))
}

func TestFastPathAdmitsEightActiveRequests(t *testing.T) {
	p, a := newGaiaTestNode(t, 50), newGaiaTestNode(t, 1)
	g := gaiaTestProxy(t, p, a)
	if cap(g.admission.active) < 8 {
		t.Fatalf("active limit %d cannot accommodate eight-block prefetch", cap(g.admission.active))
	}
}

func TestFastPathCachesHeadWithCorrectCallerID(t *testing.T) {
	p, a := newGaiaTestNode(t, 50), newGaiaTestNode(t, 1)
	g := gaiaTestProxy(t, p, a)
	var clock atomic.Int64
	clock.Store(time.Now().UnixNano())
	g.now = func() time.Time { return time.Unix(0, clock.Load()) }
	gaiaTestOK(t, gaiaTestRead(g, "block", 0))
	r := httptest.NewRequest("POST", "/gaia", strings.NewReader(`{"jsonrpc":"2.0","id":"different-caller","method":"block","params":null}`))
	w := httptest.NewRecorder()
	g.ServeHTTP(w, r)
	gaiaTestOK(t, w)
	var envelope gaiaEnvelope
	if json.Unmarshal(w.Body.Bytes(), &envelope) != nil || string(envelope.ID) != `"different-caller"` {
		t.Fatal("cached response did not preserve caller ID")
	}
	if got := p.count("block", 0); got != 1 {
		t.Fatalf("latest head fetched %d times; want a shared one-second result", got)
	}
	clock.Add(int64(2 * time.Second))
	gaiaTestOK(t, gaiaTestRead(g, "block", 0))
	if got := p.count("block", 0); got != 2 {
		t.Fatalf("expired head was reused, upstream reads %d", got)
	}
}
