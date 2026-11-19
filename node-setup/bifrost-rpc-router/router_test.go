package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func writeJSON(t testing.TB, filename string, v any) {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filename, b, 0600); err != nil {
		t.Fatal(err)
	}
}

func fixtureConfig(t testing.TB, providers map[string]map[string]string) (Config, string) {
	t.Helper()
	// Short Unix socket path limits on some platforms also apply to the validated configuration.
	dir, err := os.MkdirTemp("/tmp", "rpc-router-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	c := Config{Listen: "127.0.0.1:0", AdminSocket: filepath.Join(dir, "admin.sock"), StateFile: filepath.Join(dir, "routes.json"), Chains: map[string]Chain{}}
	for chain, backends := range providers {
		ch := Chain{Default: "normal", Active: 32, Waiting: 32, TimeoutSeconds: 5, QueueSeconds: 1, MaxRequestBytes: 1 << 20, Endpoints: map[string]Endpoint{"rpc": {Path: "/" + chain, Suffix: true}}, Backends: map[string]map[string]string{}}
		for backend, u := range backends {
			file := filepath.Join(dir, chain+"-"+backend+".json")
			writeJSON(t, file, Target{URL: u, AllowHTTP: true})
			ch.Backends[backend] = map[string]string{"rpc": file}
		}
		c.Chains[chain] = ch
	}
	filename := filepath.Join(dir, "config.json")
	writeJSON(t, filename, c)
	return c, filename
}

func fixtureRouter(t *testing.T, providers map[string]map[string]string) (*router, *httptest.Server) {
	t.Helper()
	_, filename := fixtureConfig(t, providers)
	r, err := newRouter(filename, true)
	if err != nil {
		t.Fatal(err)
	}
	s := httptest.NewServer(r)
	t.Cleanup(func() { s.Close(); r.close() })
	return r, s
}

func post(client *http.Client, u, body string) (int, string, error) {
	resp, err := client.Post(u, "application/json", strings.NewReader(body))
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b), err
}

func adminRequest(r *router, endpoint, body string, revision uint64) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", endpoint, strings.NewReader(body))
	req.Header.Set("If-Match", strconv.Quote(strconv.FormatUint(revision, 10)))
	w := httptest.NewRecorder()
	r.admin(w, req)
	return w
}

func switchRoute(t *testing.T, r *router, chain, backend string) {
	t.Helper()
	w := adminRequest(r, "/switch", fmt.Sprintf(`{"chain":%q,"backend":%q}`, chain, backend), r.current.Load().state.Version)
	if w.Code != 200 {
		t.Fatalf("switch: %d %s", w.Code, w.Body.String())
	}
}

func textServer(t *testing.T, text string) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, text) }))
	t.Cleanup(s.Close)
	return s
}

func TestTwoLayersPerChainKeepAliveSwitchAndRollback(t *testing.T) {
	normal := textServer(t, "normal")
	a := textServer(t, "provider-a")
	b := textServer(t, "provider-b")
	p2, s2 := fixtureRouter(t, map[string]map[string]string{"bsc": {"normal": a.URL, "alternate": b.URL}, "avax": {"normal": a.URL, "alternate": b.URL}})
	p1, s1 := fixtureRouter(t, map[string]map[string]string{"bsc": {"normal": normal.URL, "adapter": s2.URL + "/bsc"}, "avax": {"normal": normal.URL, "adapter": s2.URL + "/avax"}})
	client := s1.Client()
	transport := client.Transport.(*http.Transport).Clone()
	transport.MaxConnsPerHost = 1
	client = &http.Client{Transport: transport, Timeout: 5 * time.Second}
	defer transport.CloseIdleConnections()
	var last net.Conn
	check := func(chain, want string) {
		t.Helper()
		req, _ := http.NewRequest("POST", s1.URL+"/"+chain, strings.NewReader(`{"method":"eth_blockNumber"}`))
		trace := &httptrace.ClientTrace{GotConn: func(info httptrace.GotConnInfo) {
			if last != nil && last != info.Conn {
				t.Error("client connection replaced during route switch")
			}
			last = info.Conn
		}}
		resp, err := client.Do(req.WithContext(httptrace.WithClientTrace(context.Background(), trace)))
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil || resp.StatusCode != 200 || string(data) != want {
			t.Fatalf("%s got %d %s %v", chain, resp.StatusCode, data, err)
		}
	}
	check("bsc", "normal")
	switchRoute(t, p1, "bsc", "adapter")
	check("bsc", "provider-a")
	check("avax", "normal")
	switchRoute(t, p2, "bsc", "alternate")
	check("bsc", "provider-b")
	check("avax", "normal")
	switchRoute(t, p1, "bsc", "normal")
	check("bsc", "normal")
}

func TestSwitchDrainsInflightWithoutReplay(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var count atomic.Int32
	old := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count.Add(1)
		close(entered)
		<-release
		fmt.Fprint(w, "old")
	}))
	defer old.Close()
	next := textServer(t, "new")
	r, s := fixtureRouter(t, map[string]map[string]string{"bsc": {"normal": old.URL, "next": next.URL}})
	done := make(chan string, 1)
	go func() {
		_, body, _ := post(s.Client(), s.URL+"/bsc", `{"method":"eth_sendRawTransaction"}`)
		done <- body
	}()
	<-entered
	switchRoute(t, r, "bsc", "next")
	code, body, err := post(s.Client(), s.URL+"/bsc", "new read")
	if code != 200 || body != "new" || err != nil {
		t.Fatalf("new: %d %q %v", code, body, err)
	}
	close(release)
	if got := <-done; got != "old" || count.Load() != 1 {
		t.Fatalf("inflight: %q count=%d", got, count.Load())
	}
}

func TestBroadcastAmbiguityDoesNotRetryOrSwitch(t *testing.T) {
	var calls, other atomic.Int32
	var payload string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		payload = string(b)
		calls.Add(1)
		conn, _, _ := w.(http.Hijacker).Hijack()
		conn.Close()
	}))
	defer up.Close()
	backup := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { other.Add(1) }))
	defer backup.Close()
	p2, s2 := fixtureRouter(t, map[string]map[string]string{"bsc": {"normal": up.URL, "backup": backup.URL}})
	p1, s1 := fixtureRouter(t, map[string]map[string]string{"bsc": {"normal": s2.URL + "/bsc", "backup": backup.URL}})
	body := ` {"id":1,"method":"eth_sendRawTransaction","params":["0xsynthetic"]} `
	req, _ := http.NewRequest("POST", s1.URL+"/bsc", strings.NewReader(body))
	req.Header.Set("Idempotency-Key", "do-not-retry")
	resp, err := s1.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 502 || calls.Load() != 1 || other.Load() != 0 || payload != body {
		t.Fatalf("unsafe replay: status=%d calls=%d backup=%d payload=%q", resp.StatusCode, calls.Load(), other.Load(), payload)
	}
	if p1.current.Load().state.Routes["bsc"] != "normal" || p2.current.Load().state.Routes["bsc"] != "normal" {
		t.Fatal("route changed on ambiguity")
	}
}

func TestExistingPolicyAdaptersRetainPathsHistoryAndPayloads(t *testing.T) {
	var calls atomic.Int32
	policy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		body, _ := io.ReadAll(r.Body)
		switch r.URL.Path {
		case "/wallet/gettransactioninfobyid":
			fmt.Fprint(w, `{"source":"external-history"}`)
		case "/wallet/broadcasttransaction":
			fmt.Fprint(w, string(body))
		case "/jsonrpc":
			fmt.Fprint(w, `{"source":"local-finalized"}`)
		case "/gaia/block":
			if r.URL.RawQuery != "height=12" {
				t.Error("lost height")
			}
			fmt.Fprint(w, `{"source":"gaia-archive"}`)
		case "/base":
			fmt.Fprint(w, `{"source":"base-provider"}`)
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer policy.Close()
	c, file := fixtureConfig(t, map[string]map[string]string{"tron": {"normal": policy.URL}, "gaia": {"normal": policy.URL + "/gaia"}, "base": {"normal": policy.URL + "/base"}})
	tron := c.Chains["tron"]
	tron.Endpoints = map[string]Endpoint{"rest": {Path: "/tron-rest", Suffix: true}, "rpc": {Path: "/tron-rpc"}}
	restFile := tron.Backends["normal"]["rpc"]
	rpcFile := filepath.Join(filepath.Dir(file), "tron-rpc.json")
	writeJSON(t, rpcFile, Target{URL: policy.URL + "/jsonrpc", AllowHTTP: true})
	tron.Backends["normal"] = map[string]string{"rest": restFile, "rpc": rpcFile}
	c.Chains["tron"] = tron
	writeJSON(t, file, c)
	r, err := newRouter(file, true)
	if err != nil {
		t.Fatal(err)
	}
	defer r.close()
	s2 := httptest.NewServer(r)
	defer s2.Close()
	c1, f1 := fixtureConfig(t, map[string]map[string]string{"tron": {"normal": s2.URL + "/tron-rest"}, "gaia": {"normal": s2.URL + "/gaia"}, "base": {"normal": s2.URL + "/base"}})
	tr := c1.Chains["tron"]
	tr.Endpoints = tron.Endpoints
	f := filepath.Join(filepath.Dir(f1), "rpc.json")
	writeJSON(t, f, Target{URL: s2.URL + "/tron-rpc", AllowHTTP: true})
	tr.Backends["normal"] = map[string]string{"rest": tr.Backends["normal"]["rpc"], "rpc": f}
	c1.Chains["tron"] = tr
	writeJSON(t, f1, c1)
	r1, err := newRouter(f1, true)
	if err != nil {
		t.Fatal(err)
	}
	defer r1.close()
	s1 := httptest.NewServer(r1)
	defer s1.Close()
	for _, test := range []struct{ path, body, want string }{
		{"/tron-rest/wallet/gettransactioninfobyid", "{}", `{"source":"external-history"}`},
		{"/tron-rest/wallet/broadcasttransaction", ` { "synthetic": "exact bytes" } `, ` { "synthetic": "exact bytes" } `},
		{"/tron-rpc", "{}", `{"source":"local-finalized"}`},
		{"/gaia/block?height=12", "", `{"source":"gaia-archive"}`},
		{"/base", "{}", `{"source":"base-provider"}`},
	} {
		code, body, err := post(s1.Client(), s1.URL+test.path, test.body)
		if err != nil || code != 200 || body != test.want {
			t.Fatalf("%s: %d %q %v", test.path, code, body, err)
		}
	}
	if calls.Load() != 5 {
		t.Fatalf("extra chain calls: %d", calls.Load())
	}
}

func TestStateCASPersistenceAndFailure(t *testing.T) {
	u := textServer(t, "ok")
	_, file := fixtureConfig(t, map[string]map[string]string{"bsc": {"normal": u.URL, "alternate": u.URL}})
	if _, err := newRouter(file, false); err == nil {
		t.Fatal("missing state silently initialized")
	}
	r, err := newRouter(file, true)
	if err != nil {
		t.Fatal(err)
	}
	if second, err := newRouter(file, false); err == nil {
		second.close()
		t.Fatal("state shared by processes")
	}
	switchRoute(t, r, "bsc", "alternate")
	if w := adminRequest(r, "/switch", `{"chain":"bsc","backend":"normal"}`, 1); w.Code != 412 {
		t.Fatal("stale revision accepted")
	}
	r.close()
	r, err = newRouter(file, false)
	if err != nil {
		t.Fatal(err)
	}
	defer r.close()
	if r.current.Load().state.Routes["bsc"] != "alternate" {
		t.Fatal("restart lost route")
	}
	r.save = func(string, State) error { return errors.New("disk fault") }
	if w := adminRequest(r, "/switch", `{"chain":"bsc","backend":"normal"}`, 2); w.Code != 503 {
		t.Fatal("persist failure accepted")
	}
	// A rejected write costs that one change; dispatch and readiness continue.
	for _, p := range []string{"/bsc", "/readyz"} {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest("GET", p, nil))
		if w.Code == 503 {
			t.Fatalf("state fault stopped %s", p)
		}
	}
	if got := r.current.Load().state; got.Routes["bsc"] != "alternate" || got.Version != 2 {
		t.Fatal("failed commit changed route or revision")
	}
	if r.faults.Load() != 1 {
		t.Fatal("state fault not counted")
	}
}

func TestHotReloadRetainsRouteAndRejectsInvalidCatalog(t *testing.T) {
	a := textServer(t, "a")
	b := textServer(t, "b")
	c, file := fixtureConfig(t, map[string]map[string]string{"bsc": {"normal": a.URL, "alternate": a.URL}})
	r, err := newRouter(file, true)
	if err != nil {
		t.Fatal(err)
	}
	defer r.close()
	s := httptest.NewServer(r)
	defer s.Close()
	switchRoute(t, r, "bsc", "alternate")
	writeJSON(t, c.Chains["bsc"].Backends["alternate"]["rpc"], Target{URL: b.URL, AllowHTTP: true})
	w := adminRequest(r, "/reload", "{}", 2)
	if w.Code != 200 {
		t.Fatalf("reload %d %s", w.Code, w.Body.String())
	}
	_, body, _ := post(s.Client(), s.URL+"/bsc", "{}")
	if body != "b" {
		t.Fatal("reload did not replace target")
	}
	delete(c.Chains["bsc"].Backends, "alternate")
	writeJSON(t, file, c)
	if w := adminRequest(r, "/reload", "{}", 3); w.Code != 400 {
		t.Fatal("removed active route")
	}
	_, body, _ = post(s.Client(), s.URL+"/bsc", "{}")
	if body != "b" {
		t.Fatal("invalid reload changed live route")
	}
}

func TestPrefetch32AndChainIsolation(t *testing.T) {
	entered := make(chan struct{}, 32)
	release := make(chan struct{})
	blocked := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { entered <- struct{}{}; <-release; fmt.Fprint(w, "ok") }))
	defer blocked.Close()
	fast := textServer(t, "fast")
	p2, s2 := fixtureRouter(t, map[string]map[string]string{"bsc": {"normal": blocked.URL}, "avax": {"normal": fast.URL}})
	p1, s1 := fixtureRouter(t, map[string]map[string]string{"bsc": {"normal": s2.URL + "/bsc"}, "avax": {"normal": s2.URL + "/avax"}})
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			code, _, err := post(s1.Client(), s1.URL+"/bsc", "{}")
			if err != nil || code != 200 {
				t.Errorf("prefetch: %d %v", code, err)
			}
		}()
	}
	for i := 0; i < 32; i++ {
		select {
		case <-entered:
		case <-time.After(5 * time.Second):
			t.Fatal("32 requests did not reach upstream")
		}
	}
	if len(p1.admission["bsc"].active) != 32 || len(p2.admission["bsc"].active) != 32 {
		t.Error("prefetch did not stay concurrent")
	}
	code, body, err := post(s1.Client(), s1.URL+"/avax", "{}")
	if code != 200 || body != "fast" || err != nil {
		t.Error("busy BSC blocked AVAX")
	}
	code, _, err = post(s1.Client(), s1.URL+"/switch", "{}")
	if code != 404 || err != nil {
		t.Error("admin exposed on data port")
	}
	close(release)
	wg.Wait()
}

func TestHeadersPathsAndRedirects(t *testing.T) {
	var calls atomic.Int32
	u := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("Authorization") != "Bearer target" || r.Header.Get("Cookie") != "" || r.Header.Get("Idempotency-Key") != "" || r.Header.Get("X-Forwarded-For") != "" {
			t.Error("caller headers leaked")
		}
		if r.Header.Get("Content-Encoding") != "identity" {
			t.Error("body encoding lost")
		}
		if r.URL.Path != "/secret/rpc" {
			t.Errorf("path %s", r.URL.Path)
		}
		w.Header().Set("Location", "https://secret.invalid/credential")
		w.WriteHeader(302)
	}))
	defer u.Close()
	c, file := fixtureConfig(t, map[string]map[string]string{"bsc": {"normal": u.URL + "/secret/rpc"}})
	writeJSON(t, c.Chains["bsc"].Backends["normal"]["rpc"], Target{URL: u.URL + "/secret/rpc", AllowHTTP: true, Headers: map[string]string{"Authorization": "Bearer target"}})
	r, err := newRouter(file, true)
	if err != nil {
		t.Fatal(err)
	}
	defer r.close()
	s := httptest.NewServer(r)
	defer s.Close()
	req, _ := http.NewRequest("POST", s.URL+"/bsc", bytes.NewReader([]byte("{}")))
	for _, key := range []string{"Authorization", "Cookie", "Idempotency-Key", "X-Forwarded-For"} {
		req.Header.Set(key, "caller")
	}
	req.Header.Set("Content-Encoding", "identity")
	resp, err := s.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 502 || resp.Header.Get("Location") != "" || calls.Load() != 1 {
		t.Fatal("redirect escaped")
	}
	for _, p := range []string{"/bsc/../readyz", "/bsc/%2fsecret", "/bsc//secret"} {
		code, _, _ := post(s.Client(), s.URL+p, "{}")
		if code != 400 {
			t.Fatalf("unsafe path %s: %d", p, code)
		}
	}
}

func TestDeadlineCancellationAndAdmission(t *testing.T) {
	a := &admission{active: make(chan struct{}, 1), waiting: make(chan struct{}, 1), queue: 10 * time.Millisecond}
	if err := a.acquire(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if a.acquire(ctx) == nil {
		t.Fatal("canceled request admitted")
	}
	if a.acquire(context.Background()) == nil {
		t.Fatal("queue timeout admitted")
	}
	a.release()
	if len(a.active) != 0 || len(a.waiting) != 0 {
		t.Fatal("permits leaked")
	}
	started, canceled := make(chan struct{}), make(chan struct{})
	u := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		close(started)
		<-r.Context().Done()
		close(canceled)
	}))
	defer u.Close()
	_, s := fixtureRouter(t, map[string]map[string]string{"bsc": {"normal": u.URL}})
	ctx, cancel = context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, "POST", s.URL+"/bsc", strings.NewReader("{}"))
	done := make(chan struct{})
	go func() {
		resp, _ := s.Client().Do(req)
		if resp != nil {
			resp.Body.Close()
		}
		close(done)
	}()
	<-started
	cancel()
	<-done
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("upstream not canceled")
	}
}
