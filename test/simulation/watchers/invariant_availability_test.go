package watchers

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func invariantTestServer(t *testing.T, handler func(http.ResponseWriter, *http.Request, int)) (func(), map[string]int) {
	t.Helper()
	counts := map[string]int{}
	var mu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		route := strings.TrimPrefix(r.URL.Path, "/thorchain/invariant/")
		mu.Lock()
		counts[route]++
		count := counts[route]
		mu.Unlock()
		handler(w, r, count)
	}))
	oldURL, oldClient := thornodeURL, httpClient
	oldAttempts, oldDelay := invariantRecheckAttempts, invariantRecheckDelay
	thornodeURL, httpClient = server.URL, server.Client()
	invariantRecheckAttempts, invariantRecheckDelay = 2, 0
	return func() {
		thornodeURL, httpClient = oldURL, oldClient
		invariantRecheckAttempts, invariantRecheckDelay = oldAttempts, oldDelay
		server.Close()
	}, counts
}

func healthyInvariant(w http.ResponseWriter, r *http.Request) {
	fmt.Fprintf(w, `{"invariant":%q,"broken":false,"msg":[]}`, strings.TrimPrefix(r.URL.Path, "/thorchain/invariant/"))
}

func TestInvariantUnavailableNeverPasses(t *testing.T) {
	cleanup, counts := invariantTestServer(t, func(w http.ResponseWriter, r *http.Request, count int) {
		w.WriteHeader(http.StatusServiceUnavailable)
	})
	defer cleanup()
	err := NewInvariants().Fn(nil)
	if err == nil || !strings.Contains(err.Error(), "HTTP 503") || !strings.Contains(err.Error(), "/thorchain/invariant/") {
		t.Fatalf("missing endpoint outage diagnostic: %v", err)
	}
	if len(counts) != 1 {
		t.Fatalf("unexpected route counts: %v", counts)
	}
	for _, n := range counts {
		if n != 3 {
			t.Fatalf("expected bounded retries, got %v", counts)
		}
	}
}

func TestInvariantPartialOutageAndMalformedJSON(t *testing.T) {
	for _, response := range []string{"outage", "malformed", "missing broken"} {
		t.Run(response, func(t *testing.T) {
			cleanup, _ := invariantTestServer(t, func(w http.ResponseWriter, r *http.Request, count int) {
				if strings.HasSuffix(r.URL.Path, "/bond") {
					if response == "outage" {
						w.WriteHeader(http.StatusServiceUnavailable)
					} else if response == "malformed" {
						fmt.Fprint(w, "{")
					} else {
						fmt.Fprint(w, `{"invariant":"bond","msg":[]}`)
					}
					return
				}
				healthyInvariant(w, r)
			})
			defer cleanup()
			err := NewInvariants().Fn(nil)
			if err == nil || !strings.Contains(err.Error(), "bond") {
				t.Fatalf("partial failure was accepted: %v", err)
			}
		})
	}
}

func TestInvariantTransientTransportRecovery(t *testing.T) {
	cleanup, counts := invariantTestServer(t, func(w http.ResponseWriter, r *http.Request, count int) {
		if count == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		healthyInvariant(w, r)
	})
	defer cleanup()
	if err := NewInvariants().Fn(nil); err != nil {
		t.Fatal(err)
	}
	if len(counts) != 10 {
		t.Fatalf("not all routes verified: %v", counts)
	}
}

func TestInvariantPinnedSnapshotAndBrokenResult(t *testing.T) {
	cleanup, _ := invariantTestServer(t, func(w http.ResponseWriter, r *http.Request, count int) {
		if r.Header.Get("x-cosmos-block-height") != "" && r.Header.Get("x-cosmos-block-height") != "42" {
			t.Errorf("unexpected requested height %q", r.Header.Get("x-cosmos-block-height"))
		}
		w.Header().Set(gatewayHeightHeader, "42")
		if strings.HasSuffix(r.URL.Path, "/asgard") {
			fmt.Fprint(w, `{"invariant":"asgard","broken":true,"msg":["bad state"]}`)
			return
		}
		healthyInvariant(w, r)
	})
	defer cleanup()
	err := NewInvariants().Fn(nil)
	if err == nil || !strings.Contains(err.Error(), "broken at height \"42\"") {
		t.Fatalf("pinned violation was accepted: %v", err)
	}
}

func TestInvariantBrokenWithoutSnapshotCannotBeForgiven(t *testing.T) {
	cleanup, counts := invariantTestServer(t, func(w http.ResponseWriter, r *http.Request, count int) {
		if count == 1 {
			fmt.Fprintf(w, `{"invariant":%q,"broken":true,"msg":["bad state"]}`, strings.TrimPrefix(r.URL.Path, "/thorchain/invariant/"))
			return
		}
		healthyInvariant(w, r)
	})
	defer cleanup()
	if err := NewInvariants().Fn(nil); err == nil || !strings.Contains(err.Error(), "is broken") {
		t.Fatalf("unproven recovery was accepted: %v", err)
	}
	if counts["asgard"] != 1 {
		t.Fatalf("unproven recovery was retried: %v", counts)
	}
}

func TestInvariantSameHeightRecoveryCannotBeForgiven(t *testing.T) {
	cleanup, _ := invariantTestServer(t, func(w http.ResponseWriter, r *http.Request, count int) {
		w.Header().Set(gatewayHeightHeader, "42")
		if count == 1 {
			fmt.Fprintf(w, `{"invariant":%q,"broken":true,"msg":["bad state"]}`, strings.TrimPrefix(r.URL.Path, "/thorchain/invariant/"))
			return
		}
		healthyInvariant(w, r)
	})
	defer cleanup()
	if err := NewInvariants().Fn(nil); err == nil || !strings.Contains(err.Error(), "without a newer snapshot") {
		t.Fatalf("same-height recovery was accepted: %v", err)
	}
}

func TestInvariantRejectsStalePinnedResponse(t *testing.T) {
	cleanup, _ := invariantTestServer(t, func(w http.ResponseWriter, r *http.Request, count int) {
		if r.Header.Get("x-cosmos-block-height") != "" {
			w.Header().Set(gatewayHeightHeader, "41")
		} else {
			w.Header().Set(gatewayHeightHeader, "42")
		}
		healthyInvariant(w, r)
	})
	defer cleanup()
	err := NewInvariants().Fn(nil)
	if err == nil || !strings.Contains(err.Error(), "requested height 42, observed height 41") {
		t.Fatalf("stale response was accepted: %v", err)
	}
}

func TestInvariantRejectsOlderVerificationSweep(t *testing.T) {
	height := "42"
	cleanup, _ := invariantTestServer(t, func(w http.ResponseWriter, r *http.Request, count int) {
		w.Header().Set(gatewayHeightHeader, height)
		healthyInvariant(w, r)
	})
	defer cleanup()
	watcher := NewInvariants()
	if err := watcher.Fn(nil); err != nil {
		t.Fatal(err)
	}
	height = "41"
	if err := watcher.Fn(nil); err == nil || !strings.Contains(err.Error(), "stale invariant verification") {
		t.Fatalf("older full sweep was accepted: %v", err)
	}
}

func TestInvariantCancellationBoundsRetries(t *testing.T) {
	cleanup, _ := invariantTestServer(t, func(w http.ResponseWriter, r *http.Request, count int) {
		w.WriteHeader(http.StatusServiceUnavailable)
	})
	defer cleanup()
	invariantRecheckDelay = time.Hour
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	err := verifyInvariants(ctx, []string{"asgard"})
	if err == nil || time.Since(start) > time.Second {
		t.Fatalf("cancellation did not bound verification: %v", err)
	}
}

func TestInvariantFetchReadsGatewayHeightHeader(t *testing.T) {
	for name, header := range map[string]string{
		"grpc-gateway prefixed": "Grpc-Metadata-X-Cosmos-Block-Height",
		"bare":                  "x-cosmos-block-height",
	} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set(header, "42")
				healthyInvariant(w, r)
			}))
			defer server.Close()
			_, observed, err := fetchInvariant(context.Background(), server.Client(), server.URL+"/thorchain/invariant/asgard", "")
			if err != nil {
				t.Fatal(err)
			}
			if observed != "42" {
				t.Fatalf("observed height %q, want 42", observed)
			}
		})
	}
}
