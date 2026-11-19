package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestGaiaHeadCacheConcurrentIDsAndFullResult(t *testing.T) {
	p, a := newGaiaTestNode(t, 1), newGaiaTestNode(t, 1)
	p.update(func() {
		p.mutate = func(_ string, _ int64, response map[string]any) {
			response["extension"] = map[string]any{"preserved": true}
			response["result"].(map[string]any)["extra"] = map[string]any{"value": "full <result>", "items": []any{1, "two", nil}}
		}
	})
	g := gaiaTestProxy(t, p, a)
	var clock atomic.Int64
	clock.Store(time.Now().UnixNano())
	g.now = func() time.Time { return time.Unix(0, clock.Load()) }
	entered, release := make(chan struct{}), make(chan struct{})
	var enterOnce, releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	base := g.client.Transport.(*http.Transport)
	defer base.CloseIdleConnections()
	g.client.Transport = gaiaTestRoundTrip(func(r *http.Request) (*http.Response, error) {
		enterOnce.Do(func() { close(entered) })
		select {
		case <-release:
		case <-r.Context().Done():
			return nil, r.Context().Err()
		}
		return base.RoundTrip(r)
	})
	ids := []string{`0`, `9999`, `-7`, `"caller-a"`, `"caller-b"`, `"quote\"id"`, `"<tag>"`, `""`}
	results := make(chan string, len(ids))
	var wg sync.WaitGroup
	for _, id := range ids {
		wg.Go(func() {
			w := httptest.NewRecorder()
			r := httptest.NewRequest("POST", "/gaia", strings.NewReader(fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"method":"block","params":null}`, id)))
			g.ServeHTTP(w, r)
			var response map[string]json.RawMessage
			if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &response) != nil {
				t.Errorf("head response: %d", w.Code)
				return
			}
			got, ok := gaiaID(response["id"])
			want, _ := gaiaID(json.RawMessage(id))
			if !ok || got != want {
				t.Errorf("caller ID mismatch: got %s want %s", response["id"], id)
			}
			if string(response["extension"]) != `{"preserved":true}` {
				t.Error("top-level extension dropped")
			}
			var result map[string]any
			if json.Unmarshal(response["result"], &result) != nil || result["extra"] == nil || result["block"] == nil || result["block_id"] == nil {
				t.Error("complete result not preserved")
			}
			canonical, _ := json.Marshal(result)
			results <- string(canonical)
		})
	}
	<-entered
	gaiaTestWait(t, func() bool { return len(g.admission.active) == len(ids) })
	releaseOnce.Do(func() { close(release) })
	wg.Wait()
	close(results)
	first := ""
	for result := range results {
		if first == "" {
			first = result
		}
		if result != first {
			t.Fatal("cache changed result fields across callers")
		}
	}
	if p.count("block", 0) != 1 || a.total() != 0 || g.headMisses.Load() != 1 || g.headHits.Load() != uint64(len(ids)-1) {
		t.Fatal("head fetch was not singleflight")
	}
}

func TestGaiaHeadCacheFailureDoesNotServeStaleOrCacheErrors(t *testing.T) {
	for _, fault := range []string{"http", "rpc", "identity", "chain"} {
		t.Run(fault, func(t *testing.T) {
			p, a := newGaiaTestNode(t, 1), newGaiaTestNode(t, 1)
			g := gaiaTestProxy(t, p, a)
			var clock atomic.Int64
			clock.Store(time.Now().UnixNano())
			g.now = func() time.Time { return time.Unix(0, clock.Load()) }
			gaiaTestOK(t, gaiaTestRead(g, "block", 0))
			clock.Add(int64(time.Second))
			p.update(func() {
				p.down = fault == "http"
				p.mutate = func(_ string, _ int64, response map[string]any) {
					switch fault {
					case "rpc":
						delete(response, "result")
						response["error"] = map[string]any{"code": -32603, "message": "Internal error", "data": "temporary failure"}
					case "identity":
						response["id"] = "wrong"
					case "chain":
						response["result"].(map[string]any)["block"].(map[string]any)["header"].(map[string]any)["chain_id"] = "other-chain"
					}
				}
			})
			for range 2 {
				w := gaiaTestRead(g, "block", 0)
				if !strings.Contains(w.Body.String(), `"error"`) || g.head.fields != nil {
					t.Fatalf("stale/error response cached: %d %s", w.Code, w.Body.String())
				}
			}
			p.update(func() { p.down = false; p.mutate = nil })
			gaiaTestOK(t, gaiaTestRead(g, "block", 0))
			if p.count("block", 0) != 4 || a.total() != 0 {
				t.Fatal("failed head refresh was cached or fell back")
			}
		})
	}
}

func TestGaiaHeadCacheCanceledWaitDoesNotCancelFill(t *testing.T) {
	p, a := newGaiaTestNode(t, 1), newGaiaTestNode(t, 1)
	g := gaiaTestProxy(t, p, a)
	entered, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	base := g.client.Transport.(*http.Transport)
	defer base.CloseIdleConnections()
	g.client.Transport = gaiaTestRoundTrip(func(r *http.Request) (*http.Response, error) {
		close(entered)
		select {
		case <-release:
		case <-r.Context().Done():
			return nil, r.Context().Err()
		}
		return base.RoundTrip(r)
	})
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- gaiaTestRead(g, "block", 0) }()
	<-entered
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	r := httptest.NewRequest("POST", "/gaia", strings.NewReader(`{"jsonrpc":"2.0","id":2,"method":"block","params":{}}`)).WithContext(ctx)
	w := httptest.NewRecorder()
	g.ServeHTTP(w, r)
	if w.Code != 504 || len(g.admission.active) != 1 {
		t.Fatalf("waiter cancellation affected fill: %d", w.Code)
	}
	releaseOnce.Do(func() { close(release) })
	gaiaTestOK(t, <-done)
	gaiaTestOK(t, gaiaTestRead(g, "block", 0))
	if p.count("block", 0) != 1 || len(g.admission.active) != 0 {
		t.Fatal("waiter cancellation leaked or duplicated fill")
	}
}

func TestGaiaMetadataBoundAndUnknownResults(t *testing.T) {
	p, a := newGaiaTestNode(t, 50), newGaiaTestNode(t, 1)
	g := gaiaTestProxy(t, p, a)
	gaiaTestOK(t, gaiaTestRead(g, "block_results", 40))
	if a.count("block", 40) != 0 {
		t.Fatal("unknown metadata caused validation RPC")
	}
	for h := int64(1); h <= 129; h++ {
		g.rememberBlock(h, int(h))
	}
	if len(g.blocks) != 128 || !g.blockCountMatches(1, 999) || g.blockCountMatches(129, 999) {
		t.Fatal("metadata eviction or count validation incorrect")
	}
}

func TestGaiaExpiredProofRejectedBeforeAdmission(t *testing.T) {
	p, a := newGaiaTestNode(t, 50), newGaiaTestNode(t, 1)
	g := gaiaTestProxy(t, p, a)
	proof, ok := g.agreement(context.Background(), 50)
	if !ok {
		t.Fatal("fixture agreement failed")
	}
	if remaining := proof.until.Sub(g.now()); remaining < 4*time.Minute || remaining > 5*time.Minute {
		t.Fatal("agreement TTL is not five minutes")
	}
	proof.until = g.now().Add(-time.Second)
	_, err := g.archiveRead(context.Background(), gaiaReadRequest("block", 40), proof)
	if err == nil || a.count("block", 40) != 0 {
		t.Fatal("expired proof admitted new work")
	}
}
