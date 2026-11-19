package main

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

func TestHTTPSUpstreamNeverNegotiatesHTTP2(t *testing.T) {
	u := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ProtoMajor != 1 {
			t.Errorf("unexpected upstream HTTP version %s", r.Proto)
		}
		io.Copy(io.Discard, r.Body)
		fmt.Fprint(w, "verified HTTPS")
	}))
	u.EnableHTTP2 = true
	u.StartTLS()
	defer u.Close()
	r, s := fixtureRouter(t, map[string]map[string]string{"bsc": {"normal": u.URL}})
	pool := x509.NewCertPool()
	pool.AddCert(u.Certificate())
	r.current.Load().upstreams["bsc"]["normal"]["rpc"].transport.TLSClientConfig.RootCAs = pool
	code, body, err := post(s.Client(), s.URL+"/bsc", `{"method":"eth_blockNumber","params":[]}`)
	if err != nil || code != 200 || body != "verified HTTPS" {
		t.Fatalf("HTTPS failed: %d %q %v", code, body, err)
	}
}

func TestConfigValidation(t *testing.T) {
	for _, raw := range []string{`{"x":1,"x":2}`, `{} {}`, `{"x":[{"y":1,"y":2}]}`, `{"x":`, `[] garbage`} {
		var v any
		if strictJSON([]byte(raw), &v) == nil {
			t.Errorf("accepted %s", raw)
		}
	}
	for _, target := range []Target{
		{URL: "http://external.invalid/rpc"},
		{URL: "https://user:password@external.invalid/rpc"},
		{URL: "https://external.invalid/rpc?key=secret"},
		{URL: "https://external.invalid/rpc", Headers: map[string]string{"Host": "other"}},
		{URL: "https://external.invalid/rpc", Headers: map[string]string{"Authorization": "x\ny"}},
		{URL: "https://external.invalid/%2fescape"},
	} {
		if validateTarget(target) == nil {
			t.Error("accepted invalid target")
		}
	}
	mutations := map[string]func(*Config){
		"overlap": func(c *Config) {
			ch := c.Chains["bsc"]
			ch.Endpoints["rest"] = Endpoint{Path: "/bsc/sub"}
			c.Chains["bsc"] = ch
		},
		"missing route member": func(c *Config) {
			ch := c.Chains["bsc"]
			ch.Endpoints["rest"] = Endpoint{Path: "/other"}
			c.Chains["bsc"] = ch
		},
		"unbounded admission": func(c *Config) { ch := c.Chains["bsc"]; ch.Active = 10000; c.Chains["bsc"] = ch },
		"unknown default":     func(c *Config) { ch := c.Chains["bsc"]; ch.Default = "missing"; c.Chains["bsc"] = ch },
		"reserved path": func(c *Config) {
			ch := c.Chains["bsc"]
			ch.Endpoints["rpc"] = Endpoint{Path: "/readyz"}
			c.Chains["bsc"] = ch
		},
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			c, f := fixtureConfig(t, map[string]map[string]string{"bsc": {"normal": "https://example.invalid/rpc"}})
			mutate(&c)
			writeJSON(t, f, c)
			if _, err := loadCatalog(f); err == nil {
				t.Fatal("invalid config accepted")
			}
		})
	}
}

func TestVolumeProvisioningDoesNotReinitializeLostState(t *testing.T) {
	c, f := fixtureConfig(t, map[string]map[string]string{"bsc": {"normal": "https://example.invalid/rpc"}})
	c.StateFile = filepath.Join(filepath.Dir(f), "state", "private", "routes.json")
	c.AdminSocket = filepath.Join(filepath.Dir(f), "runtime", "private", "admin.sock")
	writeJSON(t, f, c)
	if r, err := newRouter(f, false); err == nil {
		r.close()
		t.Fatal("serve initialized missing state")
	}
	r, err := newRouter(f, true)
	if err != nil {
		t.Fatal(err)
	}
	r.close()
	for _, dir := range []string{filepath.Dir(c.StateFile)} {
		info, err := os.Stat(dir)
		if err != nil || info.Mode().Perm() != 0700 {
			t.Fatal("state directory not private")
		}
	}
	l, err := listenAdmin(c.AdminSocket)
	if err != nil {
		t.Fatal(err)
	}
	l.Close()
	info, err := os.Stat(filepath.Dir(c.AdminSocket))
	if err != nil || info.Mode().Perm() != 0700 {
		t.Fatal("admin directory not private")
	}
	if err := os.Chmod(filepath.Dir(c.AdminSocket), 0755); err != nil {
		t.Fatal(err)
	}
	if l, err := listenAdmin(c.AdminSocket); err == nil {
		l.Close()
		t.Fatal("insecure existing directory accepted")
	}
	if err := os.Remove(c.StateFile); err != nil {
		t.Fatal(err)
	}
	if r, err := newRouter(f, false); err == nil {
		r.close()
		t.Fatal("serve recreated lost state")
	}
}

func TestRestartAdoptsCatalogDriftAndRefusesCorruption(t *testing.T) {
	c, file := fixtureConfig(t, map[string]map[string]string{"bsc": {"normal": "https://example.invalid/rpc"}})
	r, err := newRouter(file, true)
	if err != nil {
		t.Fatal(err)
	}
	r.close()
	data, err := os.ReadFile(c.StateFile)
	if err != nil {
		t.Fatal(err)
	}
	writeJSON(t, c.Chains["bsc"].Backends["normal"]["rpc"], Target{URL: "https://example.invalid/changed"})
	// Catalog drift is adopted at a new revision bound to the new digest
	// rather than leaving every chain without a router.
	r, err = newRouter(file, false)
	if err != nil {
		t.Fatalf("restart after catalog drift: %v", err)
	}
	if got := r.current.Load(); got.state.Version != 2 || got.state.Catalog != got.catalog.digest || got.state.Routes["bsc"] != "normal" || r.faults.Load() != 0 {
		t.Fatalf("drift not rebound: %+v", got.state)
	}
	r.close()
	writeJSON(t, c.Chains["bsc"].Backends["normal"]["rpc"], Target{URL: "https://example.invalid/rpc", AllowHTTP: true})
	for _, bad := range []string{`{}`, string(data[:len(data)/2])} {
		if err := os.WriteFile(c.StateFile, []byte(bad), 0600); err != nil {
			t.Fatal(err)
		}
		if r, err := newRouter(file, true); err == nil {
			r.close()
			t.Fatal("init overwrote corrupt state")
		}
	}
}

func TestTRONRouteGroupIsAtomic(t *testing.T) {
	a := textServer(t, "a")
	b := textServer(t, "b")
	c, f := fixtureConfig(t, map[string]map[string]string{"tron": {"normal": a.URL, "other": b.URL}})
	ch := c.Chains["tron"]
	ch.Endpoints = map[string]Endpoint{"rpc": {Path: "/tron-rpc"}, "rest": {Path: "/tron-rest", Suffix: true}}
	for _, endpoints := range ch.Backends {
		endpoints["rest"] = endpoints["rpc"]
	}
	c.Chains["tron"] = ch
	writeJSON(t, f, c)
	r, err := newRouter(f, true)
	if err != nil {
		t.Fatal(err)
	}
	defer r.close()
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			s := r.current.Load()
			backend := s.state.Routes["tron"]
			if len(s.upstreams["tron"][backend]) != 2 {
				t.Error("split TRON endpoints")
				return
			}
		}
	}()
	for i := 0; i < 20; i++ {
		switchRoute(t, r, "tron", "other")
		switchRoute(t, r, "tron", "normal")
	}
	close(stop)
	wg.Wait()
}

func TestBoundedQueueAndCancellation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		a := &admission{active: make(chan struct{}, 32), waiting: make(chan struct{}, 32), queue: time.Second}
		for i := 0; i < 32; i++ {
			if a.acquire(context.Background()) != nil {
				t.Fatal("active admission")
			}
		}
		ctx, cancel := context.WithCancel(context.Background())
		var wg sync.WaitGroup
		for i := 0; i < 32; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if a.acquire(ctx) == nil {
					a.release()
					t.Error("waiter admitted before cancel")
				}
			}()
		}
		synctest.Wait()
		if len(a.waiting) != 32 {
			t.Fatal("queue not full")
		}
		if a.acquire(context.Background()) == nil {
			t.Fatal("65th request admitted")
		}
		cancel()
		wg.Wait()
		for i := 0; i < 32; i++ {
			a.release()
		}
		if len(a.active) != 0 || len(a.waiting) != 0 {
			t.Fatal("permits leaked")
		}
	})
}

func TestOutageDoesNotEscapeSelectedRoute(t *testing.T) {
	broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "provider down", 503) }))
	defer broken.Close()
	var count atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { count.Add(1) }))
	defer other.Close()
	r, s := fixtureRouter(t, map[string]map[string]string{"bsc": {"normal": broken.URL, "other": other.URL}})
	code, _, err := post(s.Client(), s.URL+"/bsc", "{}")
	if err != nil || code != 503 || count.Load() != 0 {
		t.Fatal("outage changed backend")
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/readyz", nil))
	if !strings.Contains(w.Body.String(), "not provider") {
		t.Fatal("readiness misrepresents provider health")
	}
	broken.Close()
	code, _, err = post(s.Client(), s.URL+"/bsc", "{}")
	if err != nil || code != 502 || count.Load() != 0 {
		t.Fatal("connection refusal changed backend")
	}
}

func TestHotReloadDrainsOldProviderAndRestartUsesNewCatalog(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	old := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		close(entered)
		<-release
		fmt.Fprint(w, "old")
	}))
	defer old.Close()
	next := textServer(t, "new")
	c, f := fixtureConfig(t, map[string]map[string]string{"bsc": {"normal": old.URL}})
	r, err := newRouter(f, true)
	if err != nil {
		t.Fatal(err)
	}
	s := httptest.NewServer(r)
	done := make(chan string, 1)
	go func() { _, b, _ := post(s.Client(), s.URL+"/bsc", "{}"); done <- b }()
	<-entered
	writeJSON(t, c.Chains["bsc"].Backends["normal"]["rpc"], Target{URL: next.URL, AllowHTTP: true})
	if w := adminRequest(r, "/reload", "{}", 1); w.Code != 200 {
		t.Fatalf("reload %s", w.Body.String())
	}
	_, body, _ := post(s.Client(), s.URL+"/bsc", "{}")
	if body != "new" {
		t.Error("new requests did not switch")
	}
	close(release)
	if <-done != "old" {
		t.Error("inflight did not drain")
	}
	s.Close()
	r.close()
	r, err = newRouter(f, false)
	if err != nil {
		t.Fatal(err)
	}
	defer r.close()
	if r.current.Load().state.Version != 2 {
		t.Fatal("reload not durable")
	}
}

func TestPrivateAdminSocketAndGracefulShutdown(t *testing.T) {
	u := textServer(t, "ok")
	c, f := fixtureConfig(t, map[string]map[string]string{"bsc": {"normal": u.URL}})
	r, err := newRouter(f, true)
	if err != nil {
		t.Fatal(err)
	}
	defer r.close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- serve(ctx, r) }()
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", c.AdminSocket)
	}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: time.Second}
	deadline := time.Now().Add(3 * time.Second)
	for {
		resp, err := client.Get("http://admin/routes")
		if err == nil {
			var state State
			err = json.NewDecoder(resp.Body).Decode(&state)
			resp.Body.Close()
			if err != nil || state.Version != 1 {
				t.Fatal("invalid status")
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("admin did not start")
		}
		time.Sleep(5 * time.Millisecond)
	}
	info, err := os.Stat(c.AdminSocket)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("socket not private")
	}
	if err := adminCommand(c.AdminSocket, "status", "", "", 0); err != nil {
		t.Fatal(err)
	}
	if err := adminCommand(c.AdminSocket, "switch", "bsc", "normal", 1); err != nil {
		t.Fatal(err)
	}
	if err := adminCommand(c.AdminSocket, "switch", "bsc", "normal", 1); err == nil {
		t.Fatal("CLI silently retried stale switch")
	}
	if err := adminCommand(c.AdminSocket, "reload", "", "", 2); err != nil {
		t.Fatal(err)
	}
	for _, command := range []string{"switch", "reload", "unknown"} {
		if err := adminCommand(c.AdminSocket, command, "", "", 0); err == nil {
			t.Fatal("invalid CLI request accepted")
		}
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("shutdown stuck")
	}
	file := filepath.Join(filepath.Dir(c.AdminSocket), "not-a-socket")
	if err := os.WriteFile(file, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if l, err := listenAdmin(file); err == nil {
		l.Close()
		t.Fatal("overwrote regular file")
	}
}

func TestTLSVerificationAndBodyLimits(t *testing.T) {
	var calls atomic.Int32
	u := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
	defer u.Close()
	_, s := fixtureRouter(t, map[string]map[string]string{"bsc": {"normal": u.URL}})
	code, _, _ := post(s.Client(), s.URL+"/bsc", "{}")
	if code != 502 || calls.Load() != 0 {
		t.Fatal("untrusted TLS accepted")
	}
	code, _, _ = post(s.Client(), s.URL+"/bsc", strings.Repeat("x", (1<<20)+1))
	if code != 413 || calls.Load() != 0 {
		t.Fatal("oversized body dispatched")
	}
	var failures int
	b := &boundedResponse{ReadCloser: io.NopCloser(strings.NewReader("12345")), remaining: 4, failure: func(string) { failures++ }}
	if _, err := io.ReadAll(b); err == nil || failures != 1 {
		t.Fatal("response limit failed")
	}
}

func BenchmarkTwoLayerLoopback(b *testing.B) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		fmt.Fprint(w, `{"result":"0x1"}`)
	}))
	defer up.Close()
	_, f2 := fixtureConfig(b, map[string]map[string]string{"bsc": {"normal": up.URL}})
	r2, err := newRouter(f2, true)
	if err != nil {
		b.Fatal(err)
	}
	defer r2.close()
	s2 := httptest.NewServer(r2)
	defer s2.Close()
	_, f1 := fixtureConfig(b, map[string]map[string]string{"bsc": {"normal": s2.URL + "/bsc"}})
	r1, err := newRouter(f1, true)
	if err != nil {
		b.Fatal(err)
	}
	defer r1.close()
	s1 := httptest.NewServer(r1)
	defer s1.Close()
	for name, u := range map[string]string{"direct": up.URL, "two_layer": s1.URL + "/bsc"} {
		b.Run(name, func(b *testing.B) {
			client := &http.Client{Timeout: 5 * time.Second}
			for i := 0; i < b.N; i++ {
				code, _, err := post(client, u, `{"method":"eth_blockNumber"}`)
				if err != nil || code != 200 {
					b.Fatal(code, err)
				}
			}
		})
	}
}

// A caller that stops sending its body must not hold an admission permit past
// the chain deadline: ServeHTTP's connection read deadline unblocks the read.
func TestStalledRequestBodyReleasesAdmission(t *testing.T) {
	u := textServer(t, "{}")
	r, _ := fixtureRouter(t, map[string]map[string]string{"eth": {"normal": u.URL}})
	done := make(chan struct{})
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		defer close(done)
		ctx, cancel := context.WithTimeout(req.Context(), 100*time.Millisecond)
		defer cancel()
		r.ServeHTTP(w, req.WithContext(ctx))
	}))
	defer s.Close()
	conn, err := net.Dial("tcp", s.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := fmt.Fprintf(conn, "POST /eth HTTP/1.1\r\nHost: fixture\r\nContent-Type: application/json\r\nContent-Length: 100\r\n\r\n{"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		conn.Close()
		<-done
		t.Fatal("stalled HTTP body ignored the chain deadline")
	}
	if len(r.admission["eth"].active) != 0 {
		t.Fatal("stalled HTTP body leaked admission")
	}
}
