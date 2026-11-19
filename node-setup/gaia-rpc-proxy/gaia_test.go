package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type gaiaTestNode struct {
	mu                                   sync.Mutex
	server                               *httptest.Server
	floor, resultsFloor, advertisedFloor int64
	head                                 int64
	network, hash                        string
	stamp                                time.Time
	catching, down                       bool
	calls                                map[string]int
	mutate                               func(string, int64, map[string]any)
}

func newGaiaTestNode(t *testing.T, floor int64) *gaiaTestNode {
	t.Helper()
	n := &gaiaTestNode{floor: floor, resultsFloor: floor, advertisedFloor: floor, head: 100, network: "cosmoshub-4", hash: strings.Repeat("A", 64), stamp: time.Now().UTC(), calls: map[string]int{}}
	n.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.UserAgent() != "Go-http-client/1.1" {
			t.Errorf("unexpected upstream request %s / %s", r.Method, r.UserAgent())
		}
		var q struct {
			ID     json.RawMessage    `json:"id"`
			Method string             `json:"method"`
			Params map[string]*string `json:"params"`
		}
		if json.NewDecoder(r.Body).Decode(&q) != nil {
			w.WriteHeader(400)
			return
		}
		h := int64(0)
		if v := q.Params["height"]; v != nil {
			_, _ = fmt.Sscan(*v, &h)
		}
		n.mu.Lock()
		defer n.mu.Unlock()
		n.calls[fmt.Sprintf("%s/%d", q.Method, h)]++
		if n.down {
			w.WriteHeader(503)
			return
		}
		requested := h
		if h == 0 {
			h = n.head
		}
		out := map[string]any{"jsonrpc": "2.0", "id": q.ID}
		floor := n.floor
		if q.Method == "block_results" {
			floor = n.resultsFloor
		}
		if q.Method != "status" && h < floor {
			out["error"] = map[string]any{"code": -32603, "message": "Internal error", "data": fmt.Sprintf("height %d is not available, lowest height is %d", h, floor)}
		} else {
			switch q.Method {
			case "status":
				syncInfo := map[string]any{"latest_block_height": fmt.Sprint(n.head), "latest_block_time": n.stamp.Format(time.RFC3339Nano), "catching_up": n.catching}
				if n.advertisedFloor != 0 {
					syncInfo["earliest_block_height"] = fmt.Sprint(n.advertisedFloor)
				}
				out["result"] = map[string]any{"node_info": map[string]any{"network": n.network}, "sync_info": syncInfo}
			case "block":
				out["result"] = map[string]any{"block_id": map[string]any{"hash": n.hash, "parts": map[string]any{"total": 1, "hash": n.hash}}, "block": map[string]any{"header": map[string]any{"chain_id": n.network, "height": fmt.Sprint(h), "time": n.stamp.Format(time.RFC3339Nano)}, "data": map[string]any{"txs": []string{"dHgx", "dHgy"}}}}
			case "block_results":
				out["result"] = map[string]any{"height": fmt.Sprint(h), "txs_results": []any{map[string]any{"code": 0}, map[string]any{"code": 0}}}
			default:
				t.Errorf("unsupported upstream method %s", q.Method)
				w.WriteHeader(400)
				return
			}
		}
		if n.mutate != nil {
			n.mutate(q.Method, requested, out)
		}
		_ = json.NewEncoder(w).Encode(out)
	}))
	t.Cleanup(n.server.Close)
	return n
}

func (n *gaiaTestNode) update(f func()) { n.mu.Lock(); defer n.mu.Unlock(); f() }
func (n *gaiaTestNode) count(method string, height int64) int {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.calls[fmt.Sprintf("%s/%d", method, height)]
}
func (n *gaiaTestNode) total() int {
	n.mu.Lock()
	defer n.mu.Unlock()
	total := 0
	for _, v := range n.calls {
		total += v
	}
	return total
}
func gaiaTestProxy(t *testing.T, p, a *gaiaTestNode) *GaiaProxy {
	t.Helper()
	pu, _ := url.Parse(p.server.URL)
	au, _ := url.Parse(a.server.URL)
	g := newGaiaProxy(pu, au, 2*time.Second)
	t.Cleanup(g.client.CloseIdleConnections)
	t.Cleanup(g.freshClient.CloseIdleConnections)
	return g
}
func gaiaTestRead(g *GaiaProxy, method string, height int64) *httptest.ResponseRecorder {
	params := `{}`
	if height != 0 {
		params = fmt.Sprintf(`{"height":"%d"}`, height)
	}
	r := httptest.NewRequest("POST", "/gaia", strings.NewReader(fmt.Sprintf(`{"jsonrpc":"2.0","id":7,"method":%q,"params":%s}`, method, params)))
	w := httptest.NewRecorder()
	g.ServeHTTP(w, r)
	return w
}
func gaiaTestOK(t *testing.T, w *httptest.ResponseRecorder) {
	t.Helper()
	if w.Code != 200 || bytes.Contains(w.Body.Bytes(), []byte(`"error"`)) {
		t.Fatalf("response %d: %s", w.Code, w.Body.String())
	}
}

func TestGaiaHistoricalFallbackAndVerifiedFloor(t *testing.T) {
	p, a := newGaiaTestNode(t, 50), newGaiaTestNode(t, 1)
	p.update(func() { p.advertisedFloor = 20; p.resultsFloor = 1 })
	g := gaiaTestProxy(t, p, a)
	var clock atomic.Int64
	clock.Store(time.Now().UnixNano())
	g.now = func() time.Time { return time.Unix(0, clock.Load()) }
	gaiaTestOK(t, gaiaTestRead(g, "block", 10))
	before := p.count("block", 10)
	gaiaTestOK(t, gaiaTestRead(g, "block", 10))
	gaiaTestOK(t, gaiaTestRead(g, "block_results", 10))
	if p.count("block", 10) != before || p.count("block_results", 10) != 0 {
		t.Fatal("trusted old history did not bypass primary for both methods")
	}
	gaiaTestOK(t, gaiaTestRead(g, "block", 50))
	gaiaTestOK(t, gaiaTestRead(g, "block_results", 50))
	if a.count("block", 50) != 0 || a.count("block_results", 50) != 0 {
		t.Fatal("floor must use primary")
	}
	p.update(func() { p.floor = 60 })
	gaiaTestOK(t, gaiaTestRead(g, "block", 55))
	before = p.count("block", 55)
	gaiaTestOK(t, gaiaTestRead(g, "block", 55))
	if p.count("block", 55) != before {
		t.Fatal("advancing floor was not revalidated")
	}
	p.update(func() { p.floor = 1; p.resultsFloor = 1; p.advertisedFloor = 1 })
	clock.Add(int64(gaiaProofTTL + time.Second))
	p.update(func() { p.stamp = g.now() })
	a.update(func() { a.stamp = g.now() })
	archiveBlocksBefore := a.count("block", 10)
	archiveResultsBefore := a.count("block_results", 10)
	checksBefore := a.count("status", 0)
	gaiaTestOK(t, gaiaTestRead(g, "block", 10))
	gaiaTestOK(t, gaiaTestRead(g, "block_results", 10))
	if a.count("block", 10) != archiveBlocksBefore || a.count("block_results", 10) != archiveResultsBefore || p.count("block", 10) != 2 || p.count("block_results", 10) != 1 {
		t.Fatal("same handler did not recover primary history after floor expiry")
	}
	if a.count("status", 0) != checksBefore+1 || g.cachedProof().floor != 1 {
		t.Fatal("restored primary history did not revalidate the expired floor")
	}
}

func TestGaiaLaggingStatusFloorCorrection(t *testing.T) {
	p, a := newGaiaTestNode(t, 50), newGaiaTestNode(t, 1)
	p.update(func() { p.advertisedFloor = 20; p.resultsFloor = 70 })
	g := gaiaTestProxy(t, p, a)
	gaiaTestOK(t, gaiaTestRead(g, "block", 10))
	gaiaTestOK(t, gaiaTestRead(g, "block", 60))
	gaiaTestOK(t, gaiaTestRead(g, "block_results", 60))
	if p.count("block", 60) != 0 || p.count("block_results", 60) != 0 {
		t.Fatal("shared floor did not use maximum verified retention")
	}
	if p.count("block", 70) == 0 || p.count("block_results", 70) == 0 {
		t.Fatal("corrected boundary was not paired and verified")
	}
}

func TestGaiaExpiredFloorKeepsAsymmetricResultsOnArchive(t *testing.T) {
	p, a := newGaiaTestNode(t, 50), newGaiaTestNode(t, 1)
	p.update(func() { p.advertisedFloor = 20; p.resultsFloor = 1 })
	g := gaiaTestProxy(t, p, a)
	var clock atomic.Int64
	clock.Store(time.Now().UnixNano())
	g.now = func() time.Time { return time.Unix(0, clock.Load()) }
	gaiaTestOK(t, gaiaTestRead(g, "block", 10))
	clock.Add(int64(gaiaProofTTL + time.Second))
	p.update(func() { p.stamp = g.now() })
	a.update(func() { a.stamp = g.now() })
	gaiaTestOK(t, gaiaTestRead(g, "block_results", 10))
	if p.count("status", 0) != 2 || p.count("block_results", 10) != 0 || a.count("block_results", 10) != 1 {
		t.Fatal("expired floor let asymmetric primary results bypass archive")
	}
	clock.Add(int64(gaiaProofTTL + time.Second))
	p.update(func() { p.stamp = g.now() })
	a.update(func() { a.stamp = g.now() })
	a.update(func() { a.down = true })
	for range 2 {
		if w := gaiaTestRead(g, "block_results", 10); w.Code < 500 {
			t.Fatal("stale-floor outage served unpaired primary history")
		}
	}
	gaiaTestOK(t, gaiaTestRead(g, "block", 80))
	gaiaTestOK(t, gaiaTestRead(g, "block_results", 80))
}

func TestGaiaPrimaryIsolation(t *testing.T) {
	p, a := newGaiaTestNode(t, 1), newGaiaTestNode(t, 1)
	a.update(func() { a.down = true })
	g := gaiaTestProxy(t, p, a)
	for _, method := range []string{"status", "block", "block_results"} {
		gaiaTestOK(t, gaiaTestRead(g, method, 0))
	}
	gaiaTestOK(t, gaiaTestRead(g, "block", 10))
	if a.total() != 0 {
		t.Fatal("healthy primary depended on archive")
	}
}

func TestGaiaNoFallbackForUnprovenErrors(t *testing.T) {
	for _, tc := range []struct {
		name, method string
		height       int64
		data         string
	}{
		{"generic", "block", 10, "database failure"},
		{"future", "block", 110, "height 110 must be less than or equal to the current blockchain height 100"},
		{"unrelated", "block", 10, "height 9 is not available, lowest height is 50"},
		{"invalidfloor", "block", 10, "height 10 is not available, lowest height is 9"},
		{"latest", "block", 0, "height 10 is not available, lowest height is 50"},
		{"status", "status", 0, "height 10 is not available, lowest height is 50"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, a := newGaiaTestNode(t, 1), newGaiaTestNode(t, 1)
			p.update(func() {
				p.mutate = func(_ string, _ int64, out map[string]any) {
					delete(out, "result")
					out["error"] = map[string]any{"code": -32603, "message": "Internal error", "data": tc.data}
				}
			})
			g := gaiaTestProxy(t, p, a)
			w := gaiaTestRead(g, tc.method, tc.height)
			if w.Code != 200 || !strings.Contains(w.Body.String(), tc.data) || a.total() != 0 {
				t.Fatalf("unexpected fallback/response: %d %s archive=%d", w.Code, w.Body.String(), a.total())
			}
		})
	}
}

func TestGaiaAgreementFailClosed(t *testing.T) {
	for _, change := range []string{"hash", "network", "catching", "stale", "future", "missingcatching", "archiveoutage"} {
		t.Run(change, func(t *testing.T) {
			p, a := newGaiaTestNode(t, 50), newGaiaTestNode(t, 1)
			a.update(func() {
				switch change {
				case "hash":
					a.hash = strings.Repeat("B", 64)
				case "network":
					a.network = "other-chain"
				case "catching":
					a.catching = true
				case "stale":
					a.stamp = time.Now().Add(-10 * time.Minute)
				case "future":
					a.stamp = time.Now().Add(time.Minute)
				case "archiveoutage":
					a.down = true
				case "missingcatching":
					a.mutate = func(m string, _ int64, out map[string]any) {
						if m == "status" {
							delete(out["result"].(map[string]any)["sync_info"].(map[string]any), "catching_up")
						}
					}
				}
			})
			g := gaiaTestProxy(t, p, a)
			if w := gaiaTestRead(g, "block", 10); w.Code < 500 {
				t.Fatalf("unsafe archive accepted: %d %s", w.Code, w.Body.String())
			}
			if a.count("block", 10) != 0 {
				t.Fatal("history reached before agreement")
			}
			gaiaTestOK(t, gaiaTestRead(g, "block", 90))
		})
	}
}

func TestGaiaResponseValidation(t *testing.T) {
	for _, archive := range []bool{false, true} {
		for _, fault := range []string{"id", "version", "height", "chain", "hash", "txcount", "missingtxs", "missingcode", "nullresult", "both"} {
			t.Run(fmt.Sprintf("archive=%t/%s", archive, fault), func(t *testing.T) {
				p, a := newGaiaTestNode(t, 1), newGaiaTestNode(t, 1)
				target := p
				if archive {
					p.floor = 50
					p.resultsFloor = 50
					p.advertisedFloor = 50
					target = a
				}
				method := "block"
				if fault == "txcount" || fault == "missingcode" {
					method = "block_results"
				}
				if fault == "txcount" && !archive {
					return
				}
				target.update(func() {
					target.mutate = func(m string, h int64, out map[string]any) {
						if h != 10 {
							return
						}
						result := out["result"].(map[string]any)
						switch fault {
						case "id":
							out["id"] = 99
						case "version":
							out["jsonrpc"] = "1.0"
						case "height":
							if m == "block" {
								result["block"].(map[string]any)["header"].(map[string]any)["height"] = "11"
							}
						case "chain":
							if m == "block" {
								result["block"].(map[string]any)["header"].(map[string]any)["chain_id"] = "wrong"
							}
						case "hash":
							if m == "block" {
								result["block_id"].(map[string]any)["hash"] = "bad"
							}
						case "txcount":
							if m == "block_results" {
								result["txs_results"] = []any{map[string]any{"code": 0}}
							}
						case "missingtxs":
							if m == "block" {
								delete(result["block"].(map[string]any)["data"].(map[string]any), "txs")
							}
						case "missingcode":
							if m == "block_results" {
								result["txs_results"] = []any{map[string]any{}}
							}
						case "nullresult":
							out["result"] = nil
						case "both":
							out["error"] = map[string]any{"code": -32603, "message": "Internal error"}
						}
					}
				})
				g := gaiaTestProxy(t, p, a)
				if fault == "txcount" {
					gaiaTestOK(t, gaiaTestRead(g, "block", 10))
				}
				if w := gaiaTestRead(g, method, 10); w.Code < 500 {
					t.Fatalf("invalid response accepted: %d %s", w.Code, w.Body.String())
				}
				if !archive && a.total() != 0 {
					t.Fatal("invalid primary response triggered archive")
				}
			})
		}
	}
}

func TestGaiaRejectRequests(t *testing.T) {
	p, a := newGaiaTestNode(t, 1), newGaiaTestNode(t, 1)
	g := gaiaTestProxy(t, p, a)
	for _, body := range []string{
		`[]`, `null`, `{`, `{}`, `{"jsonrpc":"2.0","id":1,"method":"broadcast_tx_sync"}`, `{"jsonrpc":"2.0","id":1,"method":"subscribe"}`,
		`{"jsonrpc":"2.0","id":null,"method":"block"}`, `{"jsonrpc":"2.0","method":"block"}`, `{"jsonrpc":"1.0","id":1,"method":"block"}`,
		`{"jsonrpc":"2.0","id":{},"method":"block"}`, `{"jsonrpc":"2.0","id":true,"method":"block"}`, `{"jsonrpc":"2.0","id":1.2,"method":"block"}`,
		`{"jsonrpc":"2.0","id":1,"method":"block","params":{"height":"0"}}`, `{"jsonrpc":"2.0","id":1,"method":"block","params":{"height":"-1"}}`,
		`{"jsonrpc":"2.0","id":1,"method":"block","params":{"height":"9223372036854775808"}}`, `{"jsonrpc":"2.0","id":1,"method":"block","params":{"height":10}}`,
		`{"jsonrpc":"2.0","id":1,"method":"block","params":{"height":"10","extra":1}}`, `{"jsonrpc":"2.0","id":1,"method":"status","params":{"height":"10"}}`,
		`{"jsonrpc":"2.0","id":1,"method":"block","params":["10"]}`, `{"jsonrpc":"2.0","id":1,"method":"block","method":"status"}`,
		`{"jsonrpc":"2.0","id":1,"method":"block","extra":1}`, `{"jsonrpc":"2.0","id":1,"method":"block"} {}`, strings.Repeat(" ", 65537),
	} {
		w := httptest.NewRecorder()
		g.ServeHTTP(w, httptest.NewRequest("POST", "/gaia", strings.NewReader(body)))
		if w.Code < 400 || w.Code >= 500 {
			t.Errorf("accepted request %q: %d", body[:min(len(body), 150)], w.Code)
		}
	}
	for _, route := range []string{"/gaia/websocket", "/gaia/broadcast_tx_sync", "/gaia/status?height=1", "/gaia/block?height=1&height=2", "/gaia/block?extra=1", "/gaia/block?height=-1"} {
		w := httptest.NewRecorder()
		g.ServeHTTP(w, httptest.NewRequest("GET", route, nil))
		if w.Code < 400 {
			t.Errorf("accepted %s", route)
		}
	}
	if p.total()+a.total() != 0 {
		t.Fatal("invalid requests reached upstream")
	}
}

func TestGaiaGETAndStringID(t *testing.T) {
	p, a := newGaiaTestNode(t, 1), newGaiaTestNode(t, 1)
	g := gaiaTestProxy(t, p, a)
	for _, route := range []string{"/gaia/status", "/gaia/block?height=10", "/gaia/block_results?height=10"} {
		w := httptest.NewRecorder()
		g.ServeHTTP(w, httptest.NewRequest("GET", route, nil))
		gaiaTestOK(t, w)
	}
	w := httptest.NewRecorder()
	g.ServeHTTP(w, httptest.NewRequest("POST", "/gaia/", strings.NewReader(`{"jsonrpc":"2.0","id":"comet-7","method":"block","params":{"height":null}}`)))
	gaiaTestOK(t, w)
	if !strings.Contains(w.Body.String(), `"id":"comet-7"`) {
		t.Fatal("ID lost")
	}
}

func TestGaiaConcurrentSingleflight(t *testing.T) {
	p, a := newGaiaTestNode(t, 50), newGaiaTestNode(t, 1)
	g := gaiaTestProxy(t, p, a)
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			w := gaiaTestRead(g, "block_results", 10)
			if w.Code != 200 {
				t.Errorf("concurrent read: %d %s", w.Code, w.Body.String())
			}
		})
	}
	wg.Wait()
	if p.count("status", 0) != 1 || a.count("status", 0) != 1 {
		t.Fatal("agreement refresh was not singleflight")
	}
	if cap(g.admission.active) != 8 || cap(g.admission.waiting) != 16 {
		t.Fatal("wrong independent admission limits")
	}
}

func gaiaTestWait(t *testing.T, f func() bool) {
	t.Helper()
	until := time.Now().Add(time.Second)
	for !f() {
		if time.Now().After(until) {
			t.Fatal("condition timeout")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestGaiaAdmissionAndCancellation(t *testing.T) {
	entered := make(chan struct{}, 16)
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		entered <- struct{}{}
		<-r.Context().Done()
	}))
	defer s.Close()
	u, _ := url.Parse(s.URL)
	g := newGaiaProxy(u, u, 2*time.Second)
	defer g.client.CloseIdleConnections()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var wg sync.WaitGroup
	request := func() {
		defer wg.Done()
		r := httptest.NewRequest("POST", "/gaia", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"status"}`)).WithContext(ctx)
		g.ServeHTTP(httptest.NewRecorder(), r)
	}
	for range maxActiveRequests {
		wg.Add(1)
		go request()
	}
	gaiaTestWait(t, func() bool { return len(entered) == maxActiveRequests })
	for range maxQueuedRequests {
		wg.Add(1)
		go request()
	}
	gaiaTestWait(t, func() bool { return len(g.admission.waiting) == maxQueuedRequests })
	if w := gaiaTestRead(g, "status", 0); w.Code != 503 {
		t.Fatalf("full admission = %d", w.Code)
	}
	independent := newRequestAdmission()
	if err := independent.acquire(context.Background()); err != nil {
		t.Fatal(err)
	}
	independent.release()
	cancel()
	wg.Wait()
	if len(g.admission.active) != 0 || len(g.admission.waiting) != 0 {
		t.Fatal("permits leaked on cancellation")
	}
}

func TestGaiaColdStartResultsFirst(t *testing.T) {
	p, a := newGaiaTestNode(t, 50), newGaiaTestNode(t, 1)
	p.update(func() { p.advertisedFloor = 20; p.resultsFloor = 1 })
	g := gaiaTestProxy(t, p, a)
	// Without a verified floor, successful primary results cannot establish
	// block retention. This request intentionally remains primary-first.
	gaiaTestOK(t, gaiaTestRead(g, "block_results", 10))
	if p.count("block_results", 10) != 1 || a.total() != 0 || g.cachedProof().valid {
		t.Fatal("cold results read trusted an undiscovered floor")
	}
	gaiaTestOK(t, gaiaTestRead(g, "block", 10))
	gaiaTestOK(t, gaiaTestRead(g, "block_results", 10))
	if p.count("block_results", 10) != 1 || a.count("block_results", 10) != 1 || a.count("block", 10) != 1 {
		t.Fatal("results did not use archive history without refetching the block")
	}
}

type gaiaTestRoundTrip func(*http.Request) (*http.Response, error)

func (f gaiaTestRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestGaiaTotalDeadline(t *testing.T) {
	for _, tc := range []struct {
		name   string
		stopAt int
	}{
		{"primary", 1},
		{"agreement", 3},
		{"archive_results", 8},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, a := newGaiaTestNode(t, 50), newGaiaTestNode(t, 1)
			g := gaiaTestProxy(t, p, a)
			g.timeout = 200 * time.Millisecond
			transport := g.client.Transport
			defer transport.(*http.Transport).CloseIdleConnections()
			calls := 0
			var firstDeadline time.Time
			g.client.Transport = gaiaTestRoundTrip(func(r *http.Request) (*http.Response, error) {
				calls++
				deadline, ok := r.Context().Deadline()
				if !ok {
					t.Error("upstream request has no deadline")
				}
				if firstDeadline.IsZero() {
					firstDeadline = deadline
				}
				if !deadline.Equal(firstDeadline) {
					t.Errorf("deadline extended on RPC %d", calls)
				}
				if calls == tc.stopAt {
					<-r.Context().Done()
					return nil, r.Context().Err()
				}
				timer := time.NewTimer(5 * time.Millisecond)
				defer timer.Stop()
				select {
				case <-timer.C:
				case <-r.Context().Done():
					return nil, r.Context().Err()
				}
				return transport.RoundTrip(r)
			})
			started := time.Now()
			w := gaiaTestRead(g, "block_results", 10)
			if w.Code != http.StatusGatewayTimeout || calls != tc.stopAt {
				t.Fatalf("deadline stage %s: code=%d calls=%d body=%s", tc.name, w.Code, calls, w.Body.String())
			}
			if elapsed := time.Since(started); elapsed > time.Second || firstDeadline.After(started.Add(g.timeout+20*time.Millisecond)) {
				t.Fatalf("total deadline was not bounded: elapsed=%s deadline=%s", elapsed, firstDeadline)
			}
			if len(g.admission.active) != 0 || len(g.admission.waiting) != 0 || len(g.checking) != 0 {
				t.Fatal("deadline leaked admission or agreement permit")
			}
			if tc.stopAt == 1 && a.total() != 0 {
				t.Fatal("primary timeout triggered archive")
			}
		})
	}
}

func TestGaiaDeadlineWhileWaiting(t *testing.T) {
	for _, stage := range []string{"admission", "agreement"} {
		t.Run(stage, func(t *testing.T) {
			p, a := newGaiaTestNode(t, 50), newGaiaTestNode(t, 1)
			g := gaiaTestProxy(t, p, a)
			g.timeout = 40 * time.Millisecond
			if stage == "admission" {
				for range cap(g.admission.active) {
					g.admission.active <- struct{}{}
					defer g.admission.release()
				}
			} else {
				g.checking <- struct{}{}
				defer func() { <-g.checking }()
			}
			started := time.Now()
			w := gaiaTestRead(g, "block", 10)
			want := http.StatusGatewayTimeout
			if stage == "admission" {
				want = http.StatusServiceUnavailable
			}
			if w.Code != want || time.Since(started) > time.Second {
				t.Fatalf("unbounded %s wait: %d", stage, w.Code)
			}
			if len(g.admission.waiting) != 0 || a.total() != 0 {
				t.Fatal("waiting cancellation leaked a permit or dispatched archive")
			}
			if stage == "admission" && p.total() != 0 {
				t.Fatal("queued request reached primary")
			}
			if stage == "agreement" && len(g.admission.active) != 0 {
				t.Fatal("agreement cancellation retained active permit")
			}
		})
	}
}

func TestGaiaHTTPTimeoutDoesNotFallback(t *testing.T) {
	p, a := newGaiaTestNode(t, 1), newGaiaTestNode(t, 1)
	g := gaiaTestProxy(t, p, a)
	g.timeout = 50 * time.Millisecond
	entered := make(chan struct{}, 1)
	canceled := make(chan struct{}, 1)
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		entered <- struct{}{}
		<-r.Context().Done()
		canceled <- struct{}{}
	}))
	defer s.Close()
	g.primary, _ = url.Parse(s.URL)
	started := time.Now()
	w := gaiaTestRead(g, "block", 10)
	if w.Code != http.StatusGatewayTimeout || time.Since(started) > time.Second || a.total() != 0 {
		t.Fatalf("primary HTTP timeout: %d archive=%d", w.Code, a.total())
	}
	gaiaTestWait(t, func() bool { return len(entered) == 1 && len(canceled) == 1 })
	if len(g.admission.active) != 0 {
		t.Fatal("HTTP cancellation leaked admission")
	}
}

func TestGaiaNeverFollowsRedirects(t *testing.T) {
	for _, archive := range []bool{false, true} {
		for _, code := range []int{301, 302, 303, 307, 308} {
			t.Run(fmt.Sprintf("archive=%t/status=%d", archive, code), func(t *testing.T) {
				p, a := newGaiaTestNode(t, 50), newGaiaTestNode(t, 1)
				g := gaiaTestProxy(t, p, a)
				if archive {
					gaiaTestOK(t, gaiaTestRead(g, "block", 10))
				}
				var targetCalls atomic.Int64
				target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { targetCalls.Add(1); w.WriteHeader(200) }))
				defer target.Close()
				source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, code) }))
				defer source.Close()
				u, _ := url.Parse(source.URL + "/private-route-token")
				if archive {
					g.archive = u
				} else {
					g.primary = u
				}
				archiveBefore := a.total()
				w := gaiaTestRead(g, "block", 10)
				if w.Code != http.StatusBadGateway || targetCalls.Load() != 0 || a.total() != archiveBefore {
					t.Fatalf("redirect followed or triggered fallback: code=%d target=%d", w.Code, targetCalls.Load())
				}
				var metrics bytes.Buffer
				g.Metrics(&metrics)
				if strings.Contains(w.Body.String()+metrics.String(), "private-route-token") || strings.Contains(w.Body.String()+metrics.String(), source.URL) {
					t.Fatal("diagnostics exposed upstream URL")
				}
			})
		}
	}
}

func TestGaiaOversizedResponse(t *testing.T) {
	for _, archive := range []bool{false, true} {
		for _, advertised := range []bool{false, true} {
			t.Run(fmt.Sprintf("archive=%t/content_length=%t", archive, advertised), func(t *testing.T) {
				p, a := newGaiaTestNode(t, 50), newGaiaTestNode(t, 1)
				g := gaiaTestProxy(t, p, a)
				if archive {
					gaiaTestOK(t, gaiaTestRead(g, "block", 10))
				}
				var served atomic.Int64
				s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					served.Add(1)
					if advertised {
						w.Header().Set("Content-Length", fmt.Sprint(gaiaMaxResponse+1))
						w.WriteHeader(200)
						return
					}
					w.(http.Flusher).Flush()
					chunk := bytes.Repeat([]byte(" "), 64<<10)
					for sent := 0; sent <= gaiaMaxResponse; sent += len(chunk) {
						if _, err := w.Write(chunk); err != nil {
							return
						}
					}
				}))
				defer s.Close()
				u, _ := url.Parse(s.URL)
				if archive {
					g.archive = u
				} else {
					g.primary = u
				}
				archiveBefore := a.total()
				w := gaiaTestRead(g, "block", 10)
				if w.Code != http.StatusBadGateway || served.Load() != 1 || a.total() != archiveBefore {
					t.Fatalf("oversized response accepted or caused fallback: %d", w.Code)
				}
				if w.Body.Len() > 1024 || len(g.admission.active) != 0 {
					t.Fatal("oversized response escaped or retained admission")
				}
			})
		}
	}
}
