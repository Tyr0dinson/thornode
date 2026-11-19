package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const tronBulkIntegrationRoute = "/wallet/gettransactioninfobyblocknum"
const tronFinalizedRequest = ` {"jsonrpc":"2.0","id":7,"method":"eth_getBlockByNumber","params":["finalized",false]} `

type tronIntegrationCall struct {
	Method, Path, Query, Body string
}

type tronIntegrationProvider struct {
	name, prefix string
	mu           sync.Mutex
	calls        []tronIntegrationCall
	hook         func(http.ResponseWriter, *http.Request, string) bool
}

func (p *tronIntegrationProvider) snapshot() []tronIntegrationCall {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]tronIntegrationCall(nil), p.calls...)
}

func tronBulkIntegrationReceipts(height int64) string {
	if height == 90002 {
		return " [ ]\n"
	}
	first := fmt.Sprintf(`{"id":%q,"blockNumber":%d,"receipt":{"result":"SUCCESS"},"log":[]}`, strings.Repeat("b", 64), height)
	if height == 90003 {
		return " [\n" + first + "\n]\n"
	}
	return " [\n" + first + ",\n" + fmt.Sprintf(`{"id":%q,"blockNumber":%d,"receipt":{"result":"FAILED"},"log":[]}`, strings.Repeat("c", 64), height) + "\n]\n"
}

func tronBulkIntegrationResponse(height int64) (int, string) {
	switch height {
	case 1:
		return 503, " {\"error\":\"history unavailable\"}\n"
	case 2:
		return 200, `{"Error":"receipt not retained"}`
	case 3:
		return 429, `{"error":"rate limited"}`
	case 4:
		return 500, `{"error":"internal error"}`
	case 5:
		return 206, tronBulkIntegrationReceipts(90003)
	default:
		return 200, tronBulkIntegrationReceipts(height)
	}
}

func (p *tronIntegrationProvider) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "fixture request read failed", 400)
		return
	}
	p.mu.Lock()
	p.calls = append(p.calls, tronIntegrationCall{r.Method, r.URL.Path, r.URL.RawQuery, string(body)})
	p.mu.Unlock()
	if p.hook != nil && p.hook(w, r, string(body)) {
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Fixture-Provider", p.name)
	if !strings.HasPrefix(r.URL.Path, p.prefix+"/") {
		http.NotFound(w, r)
		return
	}
	switch strings.TrimPrefix(r.URL.Path, p.prefix) {
	case tronBulkIntegrationRoute:
		var q struct{ Num int64 }
		if json.Unmarshal(body, &q) != nil {
			http.Error(w, "invalid fixture request", 400)
			return
		}
		code, data := tronBulkIntegrationResponse(q.Num)
		if code >= 400 {
			w.Header().Set("Retry-After", "7")
		}
		w.WriteHeader(code)
		fmt.Fprint(w, data)
	case "/wallet/gettransactioninfobyid":
		fmt.Fprint(w, " {}\n")
	case "/wallet/broadcasttransaction":
		fmt.Fprint(w, `{"result":true}`)
	case "/jsonrpc":
		fmt.Fprintf(w, `{"jsonrpc":"2.0","id":7,"result":{"source":%q,"number":"0x18696"}}`, p.name)
	default:
		http.NotFound(w, r)
	}
}

// Keep every chain, endpoint and limit from the shipped examples. Only the
// listener, private file locations and provider URLs become isolated fixtures.
func tronIntegrationExampleRouter(t *testing.T, role string, targetURL func(string, string, string, Endpoint) string) (*router, *httptest.Server) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("examples", role+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var c Config
	if err := strictJSON(data, &c); err != nil {
		t.Fatal(err)
	}
	dir, err := os.MkdirTemp("/tmp", "tron-router-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	c.Listen = "127.0.0.1:0"
	c.AdminSocket, c.StateFile = filepath.Join(dir, "admin.sock"), filepath.Join(dir, "routes.json")
	for name, ch := range c.Chains {
		for backend, files := range ch.Backends {
			for id, original := range files {
				filename := filepath.Join(dir, filepath.Base(original))
				writeJSON(t, filename, Target{URL: targetURL(name, backend, id, ch.Endpoints[id]), AllowHTTP: true})
				files[id] = filename
			}
		}
	}
	filename := filepath.Join(dir, "config.json")
	writeJSON(t, filename, c)
	r, err := newRouter(filename, true)
	if err != nil {
		t.Fatal(err)
	}
	s := httptest.NewServer(r)
	t.Cleanup(func() { s.Close(); r.close() })
	return r, s
}

// proxy stands in for tron-rpc-proxy, which is TRON's P2: P1 reaches it directly
// and the generic P2 carries no TRON chain.
type tronIntegrationPair struct {
	p1, p2        *router
	s1, s2        *httptest.Server
	native, proxy *tronIntegrationProvider
	proxyServer   *httptest.Server
}

func tronIntegrationFixture(t *testing.T, nativeHook, proxyHook func(http.ResponseWriter, *http.Request, string) bool) *tronIntegrationPair {
	t.Helper()
	f := &tronIntegrationPair{
		native: &tronIntegrationProvider{name: "native", prefix: "/native-tron", hook: nativeHook},
		proxy:  &tronIntegrationProvider{name: "proxy", hook: proxyHook},
	}
	native := httptest.NewServer(f.native)
	f.proxyServer = httptest.NewServer(f.proxy)
	t.Cleanup(native.Close)
	t.Cleanup(f.proxyServer.Close)
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		fmt.Fprint(w, r.URL.Path)
	}))
	t.Cleanup(other.Close)
	f.p2, f.s2 = tronIntegrationExampleRouter(t, "adapter", func(chain, backend, id string, ep Endpoint) string {
		if chain == "tron" {
			t.Fatal("the generic P2 must not carry TRON")
		}
		return other.URL + "/adapter/" + chain + "/" + backend + "/" + id
	})
	generic := f.p2.current.Load().catalog.config.Chains
	f.p1, f.s1 = tronIntegrationExampleRouter(t, "gateway", func(chain, backend, id string, ep Endpoint) string {
		if chain != "tron" {
			if _, ok := generic[chain]; ok && backend == "adapter" {
				return f.s2.URL + ep.Path
			}
			return other.URL + "/gateway/" + chain + "/" + backend + "/" + id
		}
		provider, server := f.native, native
		switch backend {
		case "native":
		case "adapter":
			provider, server = f.proxy, f.proxyServer
		default:
			t.Fatalf("P1 TRON must choose native or adapter, got %q", backend)
		}
		if id == "rpc" {
			return server.URL + provider.prefix + "/jsonrpc"
		}
		return server.URL + provider.prefix
	})
	return f
}

// setRoute switches P1's TRON route only when it differs, as an operator would.
func (f *tronIntegrationPair) setRoute(t *testing.T, route string) {
	t.Helper()
	if f.p1.current.Load().state.Routes["tron"] != route {
		switchRoute(t, f.p1, "tron", route)
	}
}

func tronIntegrationUnchangedState(t *testing.T, routers ...*router) func() {
	t.Helper()
	states := make([][]byte, len(routers))
	for i, r := range routers {
		states[i], _ = json.Marshal(r.current.Load().state)
	}
	return func() {
		t.Helper()
		for i, r := range routers {
			got, _ := json.Marshal(r.current.Load().state)
			persisted, err := os.ReadFile(r.current.Load().catalog.config.StateFile)
			if err != nil || string(got) != string(states[i]) || string(persisted) != string(states[i]) {
				t.Fatalf("router %d changed its route state without an operator switch: %v", i+1, err)
			}
		}
	}
}

func tronIntegrationClient(t *testing.T, server *httptest.Server) func(string, string) (int, string, http.Header) {
	t.Helper()
	transport := server.Client().Transport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.MaxConnsPerHost, transport.MaxIdleConnsPerHost = 1, 1
	t.Cleanup(transport.CloseIdleConnections)
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second}
	var previous net.Conn
	return func(route, body string) (int, string, http.Header) {
		t.Helper()
		req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, server.URL+route, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Idempotency-Key", "must-not-replay")
		var connection httptrace.GotConnInfo
		req = req.WithContext(httptrace.WithClientTrace(req.Context(), &httptrace.ClientTrace{GotConn: func(info httptrace.GotConnInfo) { connection = info }}))
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		data, readErr := io.ReadAll(resp.Body)
		resp.Body.Close()
		if readErr != nil {
			t.Fatal(readErr)
		}
		if previous != nil && (!connection.Reused || connection.Conn != previous) {
			t.Error("P1 replaced the persistent client connection")
		}
		previous = connection.Conn
		return resp.StatusCode, string(data), resp.Header
	}
}

func (f *tronIntegrationPair) checkOtherRoutes(t *testing.T) {
	t.Helper()
	for _, pair := range []struct {
		role string
		r    *router
		s    *httptest.Server
	}{{"gateway", f.p1, f.s1}, {"adapter", f.p2, f.s2}} {
		for name, ch := range pair.r.current.Load().catalog.config.Chains {
			if name == "tron" {
				continue
			}
			if pair.r.current.Load().state.Routes[name] != ch.Default {
				t.Fatalf("TRON switch changed %s/%s", pair.role, name)
			}
			role, backend := pair.role, ch.Default
			if inner, ok := f.p2.current.Load().state.Routes[name]; ok && role == "gateway" && backend == "adapter" {
				role, backend = "adapter", inner
			}
			want := "/" + role + "/" + name + "/" + backend + "/rpc"
			code, body, err := post(pair.s.Client(), pair.s.URL+ch.Endpoints["rpc"].Path, "{}")
			if err != nil || code != 200 || body != want {
				t.Fatalf("other route %s/%s: %d %q %v; want %q", pair.role, name, code, body, err, want)
			}
		}
	}
}

func TestTRONBulkIntegrationSwitch(t *testing.T) {
	f := tronIntegrationFixture(t, nil, nil)
	if f.p1.current.Load().state.Routes["tron"] != "adapter" {
		t.Fatal("TRON must start on its proxy")
	}
	request := tronIntegrationClient(t, f.s1)
	for _, route := range []string{"adapter", "native", "adapter", "native", "adapter"} {
		f.setRoute(t, route)
		checkState := tronIntegrationUnchangedState(t, f.p1, f.p2)
		selected, unused := f.native, f.proxy
		if route == "adapter" {
			selected, unused = f.proxy, f.native
		}
		unusedBefore := len(unused.snapshot())
		for _, height := range []int64{90001, 90002, 90003, 1, 2, 3, 4, 5} {
			body := fmt.Sprintf(" { \"num\" : %d }\n", height)
			before := len(selected.snapshot())
			code, data, headers := request("/tron-rest"+tronBulkIntegrationRoute+"?visible=true", body)
			wantCode, want := tronBulkIntegrationResponse(height)
			if code != wantCode || data != want || headers.Get("Content-Type") != "application/json" || headers.Get("X-Fixture-Provider") != selected.name {
				t.Fatalf("%s height %d: %d %q %v; want %d %q", route, height, code, data, headers, wantCode, want)
			}
			if code >= 400 && headers.Get("Retry-After") != "7" {
				t.Fatal("provider retry header lost")
			}
			wantCall := tronIntegrationCall{"POST", selected.prefix + tronBulkIntegrationRoute, "visible=true", body}
			if got := selected.snapshot()[before:]; !reflect.DeepEqual(got, []tronIntegrationCall{wantCall}) {
				t.Fatalf("request changed or replayed: %+v; want %+v", got, wantCall)
			}
			checkState()
		}
		// Empty individual history is also passed through, never routed implicitly.
		code, data, _ := request("/tron-rest/wallet/gettransactioninfobyid", `{"value":"missing"}`)
		if code != 200 || data != " {}\n" {
			t.Fatal("individual missing history was rewritten")
		}
		before := len(selected.snapshot())
		code, data, _ = request("/tron-rpc", tronFinalizedRequest)
		want := fmt.Sprintf(`{"jsonrpc":"2.0","id":7,"result":{"source":%q,"number":"0x18696"}}`, selected.name)
		if code != 200 || data != want {
			t.Fatalf("finalized RPC: %d %s; want %s", code, data, want)
		}
		if got := selected.snapshot()[before:]; !reflect.DeepEqual(got, []tronIntegrationCall{{"POST", selected.prefix + "/jsonrpc", "", tronFinalizedRequest}}) {
			t.Fatalf("finalized RPC mapping changed: %+v", got)
		}
		if len(unused.snapshot()) != unusedBefore {
			t.Fatal("TRON escaped the selected native or proxy route")
		}
		checkState()
		f.checkOtherRoutes(t)
	}
}

func TestTRONNativeWorksWithProxyUnavailable(t *testing.T) {
	f := tronIntegrationFixture(t, nil, nil)
	f.proxyServer.Close()
	f.setRoute(t, "native")
	request := tronIntegrationClient(t, f.s1)
	for _, endpoint := range []string{"/tron-rest" + tronBulkIntegrationRoute, "/tron-rpc"} {
		code, _, headers := request(endpoint, `{"num":90001}`)
		if code != 200 || headers.Get("X-Fixture-Provider") != "native" {
			t.Fatal("native route depends on the proxy")
		}
	}
	if len(f.proxy.snapshot()) != 0 {
		t.Fatal("native route visited the proxy")
	}
	f.setRoute(t, "adapter")
	checkState := tronIntegrationUnchangedState(t, f.p1, f.p2)
	nativeBefore := len(f.native.snapshot())
	code, _, _ := request("/tron-rest"+tronBulkIntegrationRoute, `{"num":90001}`)
	if code != 502 || len(f.native.snapshot()) != nativeBefore {
		t.Fatal("failed proxy route implicitly fell back to native")
	}
	checkState()
	f.setRoute(t, "native")
	code, _, _ = request("/tron-rpc", tronFinalizedRequest)
	if code != 200 {
		t.Fatal("manual native rollback requires a client reconnect")
	}
}

func TestTRONBroadcastAmbiguityNeitherReplaysNorSwitches(t *testing.T) {
	for _, route := range []string{"native", "adapter"} {
		t.Run(route, func(t *testing.T) {
			var disconnect atomic.Bool
			hook := func(w http.ResponseWriter, r *http.Request, body string) bool {
				if r.Header.Get("Idempotency-Key") != "" {
					t.Error("caller replay hint reached provider")
				}
				if !disconnect.Load() {
					return false
				}
				conn, _, err := w.(http.Hijacker).Hijack()
				if err != nil {
					t.Error(err)
				} else {
					conn.Close()
				}
				return true
			}
			f := tronIntegrationFixture(t, hook, hook)
			f.setRoute(t, route)
			selected, unused := f.native, f.proxy
			if route == "adapter" {
				selected, unused = f.proxy, f.native
			}
			request := tronIntegrationClient(t, f.s1)
			checkState := tronIntegrationUnchangedState(t, f.p1, f.p2)
			for _, tc := range []struct{ endpoint, body string }{
				{"/tron-rest/wallet/broadcasttransaction", ` {"raw_data_hex":"synthetic","signature":["test-only"]} `},
				{"/tron-rpc", ` {"jsonrpc":"2.0","id":8,"method":"eth_sendRawTransaction","params":["0xsynthetic"]} `},
			} {
				// Warm this endpoint's upstream connection before the ambiguous close.
				disconnect.Store(false)
				code, _, _ := request(tc.endpoint, tc.body)
				if code != 200 {
					t.Fatal("warm request failed")
				}
				disconnect.Store(true)
				before := len(selected.snapshot())
				code, body, _ := request(tc.endpoint, tc.body)
				if code != 502 || body != "upstream unavailable; request outcome may be ambiguous\n" {
					t.Fatalf("ambiguous response: %d %q", code, body)
				}
				if got := selected.snapshot()[before:]; len(got) != 1 || got[0].Body != tc.body || len(unused.snapshot()) != 0 {
					t.Fatalf("ambiguous broadcast replayed, changed or escaped route: %+v", got)
				}
				checkState()
			}
		})
	}
}

func TestTRON32RequestsDrainDuringSwitch(t *testing.T) {
	entered, release := make(chan struct{}, 32), make(chan struct{})
	hook := func(w http.ResponseWriter, r *http.Request, body string) bool {
		entered <- struct{}{}
		select {
		case <-release:
		case <-r.Context().Done():
		}
		return false
	}
	f := tronIntegrationFixture(t, nil, hook)
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	f.setRoute(t, "adapter")
	type result struct {
		code int
		body string
		err  error
	}
	done := make(chan result, 32)
	for i := 0; i < 32; i++ {
		go func() {
			code, body, err := post(f.s1.Client(), f.s1.URL+"/tron-rest"+tronBulkIntegrationRoute, `{"num":90001}`)
			done <- result{code, body, err}
		}()
	}
	for i := 0; i < 32; i++ {
		select {
		case <-entered:
		case <-time.After(5 * time.Second):
			t.Fatal("32 simultaneous requests did not reach the TRON proxy")
		}
	}
	a := f.p1.admission["tron"]
	if len(a.active) != 32 || cap(a.active) != 32 || cap(a.waiting) != 32 {
		t.Fatal("TRON admission limits changed")
	}
	// A queued finalized request must take the new paired route when admitted;
	// already-dispatched REST requests must drain from their original provider.
	queued := make(chan result, 1)
	go func() {
		code, body, err := post(f.s1.Client(), f.s1.URL+"/tron-rpc", tronFinalizedRequest)
		queued <- result{code, body, err}
	}()
	deadline := time.Now().Add(time.Second)
	for len(a.waiting) != 1 {
		if time.Now().After(deadline) {
			t.Fatal("finalized request did not enter the bounded P1 queue")
		}
		time.Sleep(time.Millisecond)
	}
	switchRoute(t, f.p1, "tron", "native")
	f.checkOtherRoutes(t)
	unblock()
	for i := 0; i < 32; i++ {
		select {
		case got := <-done:
			if got.err != nil || got.code != 200 || got.body != tronBulkIntegrationReceipts(90001) {
				t.Fatalf("in-flight proxy request did not drain: %+v", got)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("proxy drain stalled")
		}
	}
	select {
	case got := <-queued:
		if got.err != nil || got.code != 200 || !strings.Contains(got.body, `"source":"native"`) {
			t.Fatalf("queued request did not select the new route: %+v", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("queued finalized request stalled")
	}
	if len(f.proxy.snapshot()) != 32 || len(f.native.snapshot()) != 1 {
		t.Fatal("draining workload replayed or escaped the selected routes")
	}
	if len(a.active) != 0 || len(a.waiting) != 0 || a.rejected.Load() != 0 {
		t.Fatal("workload leaked permits or rejected a request")
	}
}
