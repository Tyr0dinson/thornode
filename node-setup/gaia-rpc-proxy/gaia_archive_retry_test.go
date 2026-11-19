package main

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func gaiaTestPruned(response map[string]any, height string) {
	delete(response, "result")
	response["error"] = map[string]any{"code": -32603, "message": "Internal error", "data": "height " + height + " is not available, lowest height is 60"}
}

func TestGaiaArchiveRetryBudget(t *testing.T) {
	for _, tc := range []struct {
		name, method                                           string
		resultFailures, blockFailures, wantResults, wantBlocks int
		wantStatus                                             int
		wantRetries, wantRecovered, wantExhausted              uint64
	}{
		{"persistent", "block", 0, 10, 0, 5, 502, 4, 0, 1},
		{"second_fresh_recovers", "block", 0, 2, 0, 3, 200, 2, 1, 0},
		{"fourth_fresh_recovers", "block", 0, 4, 0, 5, 200, 4, 1, 0},
		{"results_do_not_fetch_block", "block_results", 1, 10, 2, 0, 200, 1, 1, 0},
		{"results_retry_limit", "block_results", 10, 10, 5, 0, 502, 4, 0, 1},
		{"results_fourth_fresh_recovers", "block_results", 4, 10, 5, 0, 200, 4, 1, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, a := newGaiaTestNode(t, 50), newGaiaTestNode(t, 1)
			g := gaiaTestProxy(t, p, a)
			var output bytes.Buffer
			g.logger = log.New(&output, "", 0)
			a.update(func() {
				a.mutate = func(method string, height int64, response map[string]any) {
					if height != 40 {
						return
					}
					if method == "block_results" && a.calls["block_results/40"] <= tc.resultFailures || method == "block" && a.calls["block/40"] <= tc.blockFailures {
						gaiaTestPruned(response, "40")
					}
				}
			})
			w := gaiaTestRead(g, tc.method, 40)
			if w.Code != tc.wantStatus || a.count("block", 40) != tc.wantBlocks || a.count("block_results", 40) != tc.wantResults {
				t.Fatalf("retry budget: code=%d blocks=%d results=%d", w.Code, a.count("block", 40), a.count("block_results", 40))
			}
			if g.archiveRetries.Load() != tc.wantRetries || g.archiveRetryRecovered.Load() != tc.wantRecovered || g.archiveRetryExhausted.Load() != tc.wantExhausted {
				t.Fatal("incorrect retry counters")
			}
			var metrics bytes.Buffer
			g.Metrics(&metrics)
			if !strings.Contains(metrics.String(), fmt.Sprintf("gaia_proxy_archive_retries_total %d\n", tc.wantRetries)) {
				t.Fatal("retry metrics missing")
			}
			if strings.Count(output.String(), "\n") > 3 || strings.Contains(output.String(), a.server.URL) || strings.Contains(output.String(), "lowest height") {
				t.Fatalf("unsafe retry logs: %s", output.String())
			}
			if tc.wantRecovered == 1 && !strings.Contains(output.String(), "event=recovered") {
				t.Fatal("recovery diagnostic missing")
			}
			if tc.wantExhausted == 1 && !strings.Contains(output.String(), "reason=pruned_retry_exhausted") {
				t.Fatal("exhaustion diagnostic missing")
			}
		})
	}
}

func TestGaiaArchiveDoesNotRetryUnprovenErrors(t *testing.T) {
	for _, fault := range []string{"generic", "wrong_height", "future", "wrong_code", "identity"} {
		t.Run(fault, func(t *testing.T) {
			p, a := newGaiaTestNode(t, 50), newGaiaTestNode(t, 1)
			g := gaiaTestProxy(t, p, a)
			a.update(func() {
				a.mutate = func(_ string, height int64, response map[string]any) {
					if height != 40 {
						return
					}
					gaiaTestPruned(response, "40")
					rpcError := response["error"].(map[string]any)
					switch fault {
					case "generic":
						rpcError["data"] = gaiaDiagnosticSecret
					case "wrong_height":
						gaiaTestPruned(response, "39")
					case "future":
						rpcError["data"] = "height 40 must be less than or equal to the current blockchain height 30"
					case "wrong_code":
						rpcError["code"] = -32602
					case "identity":
						response["id"] = gaiaDiagnosticSecret
					}
				}
			})
			w := gaiaTestRead(g, "block", 40)
			if w.Code != 502 || a.count("block", 40) != 1 || g.archiveRetries.Load() != 0 {
				t.Fatal("unproven error retried or accepted")
			}
		})
	}
}

func TestGaiaArchiveRetryStillValidates(t *testing.T) {
	for _, fault := range []string{"identity", "height", "chain", "count", "expired", "replaced"} {
		t.Run(fault, func(t *testing.T) {
			p, a := newGaiaTestNode(t, 50), newGaiaTestNode(t, 1)
			g := gaiaTestProxy(t, p, a)
			var clock atomic.Int64
			clock.Store(time.Now().UnixNano())
			g.now = func() time.Time { return time.Unix(0, clock.Load()) }
			method := "block"
			if fault == "count" {
				method = "block_results"
				g.rememberBlock(40, 2)
			}
			a.update(func() {
				a.mutate = func(m string, height int64, response map[string]any) {
					if height != 40 || m != method {
						return
					}
					if a.calls[m+"/40"] == 1 {
						gaiaTestPruned(response, "40")
						return
					}
					result := response["result"].(map[string]any)
					switch fault {
					case "identity":
						response["id"] = "wrong"
					case "height":
						result["block"].(map[string]any)["header"].(map[string]any)["height"] = "41"
					case "chain":
						result["block"].(map[string]any)["header"].(map[string]any)["chain_id"] = "wrong"
					case "count":
						result["txs_results"] = []any{map[string]any{"code": 0}}
					case "expired":
						clock.Add(int64(gaiaProofTTL + time.Second))
					case "replaced":
						g.proofMu.Lock()
						g.proof.generation++
						g.proofMu.Unlock()
					}
				}
			})
			w := gaiaTestRead(g, method, 40)
			wantStatus, wantRecovered := 502, uint64(0)
			if fault == "expired" || fault == "replaced" {
				wantStatus, wantRecovered = 200, 1
			}
			if w.Code != wantStatus || g.archiveRetries.Load() != 1 || g.archiveRetryRecovered.Load() != wantRecovered || a.count(method, 40) != 2 {
				t.Fatalf("invalid recovery accepted: %d", w.Code)
			}
		})
	}
}

func TestGaiaArchiveCanceledDoesNotRetry(t *testing.T) {
	for _, cancelBefore := range []bool{false, true} {
		p, a := newGaiaTestNode(t, 50), newGaiaTestNode(t, 1)
		g := gaiaTestProxy(t, p, a)
		proof, ok := g.agreement(context.Background(), 50)
		if !ok {
			t.Fatal("fixture agreement failed")
		}
		ctx, cancel := context.WithCancel(context.Background())
		a.update(func() {
			a.mutate = func(_ string, height int64, response map[string]any) {
				if height == 40 {
					gaiaTestPruned(response, "40")
					cancel()
				}
			}
		})
		if cancelBefore {
			cancel()
		}
		_, err := g.archiveRead(ctx, gaiaReadRequest("block", 40), proof)
		cancel()
		wantCalls := 1
		if cancelBefore {
			wantCalls = 0
		}
		if err == nil || a.count("block", 40) != wantCalls || g.archiveRetries.Load() != 0 {
			t.Fatal("canceled read retried")
		}
	}
}

func TestGaiaArchiveFreshRetryDeadlineAndTransport(t *testing.T) {
	p, a := newGaiaTestNode(t, 50), newGaiaTestNode(t, 1)
	g := gaiaTestProxy(t, p, a)
	pooled := g.client.Transport.(*http.Transport)
	fresh := g.freshClient.Transport.(*http.Transport)
	if fresh == pooled || !fresh.DisableKeepAlives || pooled.DisableKeepAlives || fresh.MaxConnsPerHost != pooled.MaxConnsPerHost || fresh.ResponseHeaderTimeout != pooled.ResponseHeaderTimeout || fresh.TLSHandshakeTimeout != pooled.TLSHandshakeTimeout || g.freshClient.Timeout != g.client.Timeout {
		t.Fatal("fresh transport changed bounds or shared pool")
	}
	if g.freshClient.CheckRedirect(nil, nil) != http.ErrUseLastResponse {
		t.Fatal("fresh client allows redirects")
	}
	proof, ok := g.agreement(context.Background(), 50)
	if !ok {
		t.Fatal("fixture agreement failed")
	}
	a.update(func() {
		a.mutate = func(_ string, height int64, response map[string]any) {
			if height == 40 {
				gaiaTestPruned(response, "40")
			}
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	deadline, _ := ctx.Deadline()
	g.freshClient.Transport = gaiaTestRoundTrip(func(r *http.Request) (*http.Response, error) {
		got, ok := r.Context().Deadline()
		if !ok || !got.Equal(deadline) {
			t.Error("fresh retry extended original deadline")
		}
		<-r.Context().Done()
		return nil, r.Context().Err()
	})
	_, err := g.archiveRead(ctx, gaiaReadRequest("block", 40), proof)
	if err == nil || g.archiveRetries.Load() != 1 || g.archiveRetryRecovered.Load() != 0 {
		t.Fatal("deadline did not stop retries")
	}
}
