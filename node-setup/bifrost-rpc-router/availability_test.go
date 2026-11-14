package main

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

// metricSum adds every sample of name whose label set contains all of want.
// An unlabelled sample matches when want is empty.
func metricSum(t *testing.T, r *router, name string, want ...string) float64 {
	t.Helper()
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/metrics", nil))
	total := 0.0
next:
	for _, line := range strings.Split(w.Body.String(), "\n") {
		if !strings.HasPrefix(line, name+"{") && !strings.HasPrefix(line, name+" ") {
			continue
		}
		for _, s := range want {
			if !strings.Contains(line, s) {
				continue next
			}
		}
		fields := strings.Fields(line)
		v, err := strconv.ParseFloat(fields[len(fields)-1], 64)
		if err != nil {
			t.Fatalf("bad metric line %q", line)
		}
		total += v
	}
	return total
}

func get(r *router, path string) (int, string) {
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
	return w.Code, w.Body.String()
}

// A failed route-file write must reject that one change and nothing else: P1 is
// the only RPC path for every chain, so it keeps serving the route it has.
func TestStateWriteFailureKeepsServing(t *testing.T) {
	u := textServer(t, "ok")
	r, _ := fixtureRouter(t, map[string]map[string]string{"alpha": {"normal": u.URL, "alternate": u.URL}, "gamma": {"normal": u.URL}})
	persistOK := r.save
	r.save = func(string, State) error { return errors.New("disk fault") }
	revision := r.current.Load().state.Version
	if w := adminRequest(r, "/switch", `{"chain":"alpha","backend":"alternate"}`, revision); w.Code != 503 {
		t.Fatalf("unpersisted switch reported %d", w.Code)
	}
	if got := r.current.Load().state; got.Routes["alpha"] != "normal" || got.Version != revision {
		t.Fatalf("failed commit changed live state: %+v", got)
	}
	for _, p := range []string{"/alpha", "/gamma"} {
		if code, body := get(r, p); code != 200 || body != "ok" {
			t.Fatalf("%s stopped serving after a state write fault: %d %q", p, code, body)
		}
	}
	if code, _ := get(r, "/readyz"); code != 200 {
		t.Fatalf("readyz reported %d after a state write fault", code)
	}
	if n := metricSum(t, r, "rpc_router_state_faults_total"); n != 1 {
		t.Fatalf("state fault not counted: %v", n)
	}
	// The operator can retry at the same revision once the disk recovers.
	r.save = persistOK
	if w := adminRequest(r, "/switch", `{"chain":"alpha","backend":"alternate"}`, revision); w.Code != 200 {
		t.Fatalf("retry after recovery: %d %s", w.Code, w.Body.String())
	}
	if r.current.Load().state.Routes["alpha"] != "alternate" {
		t.Fatal("retry did not switch")
	}
}

// Editing a target file (for example rotating a credential) and then restarting
// without a reload must not leave every chain without a router.
func TestRestartAdoptsCatalogDrift(t *testing.T) {
	c, file := fixtureConfig(t, map[string]map[string]string{"alpha": {"normal": "https://example.invalid/rpc", "alternate": "https://example.invalid/alt"}, "gamma": {"normal": "https://example.invalid/gamma"}})
	r, err := newRouter(file, true)
	if err != nil {
		t.Fatal(err)
	}
	switchRoute(t, r, "alpha", "alternate")
	before := r.current.Load().state
	r.close()

	writeJSON(t, c.Chains["alpha"].Backends["normal"]["rpc"], Target{URL: "https://example.invalid/rotated"})
	r, err = newRouter(file, false)
	if err != nil {
		t.Fatalf("restart after target edit: %v", err)
	}
	after := r.current.Load().state
	if after.Routes["alpha"] != "alternate" || after.Routes["gamma"] != "normal" {
		t.Fatalf("drift recovery lost routes: %+v", after.Routes)
	}
	if after.Catalog == before.Catalog || after.Catalog != r.current.Load().catalog.digest || after.Version <= before.Version {
		t.Fatalf("drift recovery did not rebind state: before=%+v after=%+v", before, after)
	}
	r.close()
	// The rebound state is durable: an unchanged restart starts at the same revision.
	r, err = newRouter(file, false)
	if err != nil {
		t.Fatal(err)
	}
	if got := r.current.Load().state; got.Version != after.Version || got.Routes["alpha"] != "alternate" {
		t.Fatalf("rebound state not persisted: %+v", got)
	}
	r.close()

	// A selected backend that no longer exists falls back to the chain default;
	// other chains keep their routes.
	alpha := c.Chains["alpha"]
	delete(alpha.Backends, "alternate")
	c.Chains["alpha"] = alpha
	writeJSON(t, file, c)
	r, err = newRouter(file, false)
	if err != nil {
		t.Fatalf("restart after backend removal: %v", err)
	}
	defer r.close()
	if got := r.current.Load().state.Routes; got["alpha"] != "normal" || got["gamma"] != "normal" || len(got) != 2 {
		t.Fatalf("removed backend not defaulted: %+v", got)
	}
}

// Unreadable state is still refused: there is no route choice to preserve.
func TestRestartStillRefusesCorruptState(t *testing.T) {
	c, file := fixtureConfig(t, map[string]map[string]string{"alpha": {"normal": "https://example.invalid/rpc"}})
	r, err := newRouter(file, true)
	if err != nil {
		t.Fatal(err)
	}
	r.close()
	for _, bad := range []string{`{}`, `{"version":3,"catalog":"x","routes":`} {
		if err := os.WriteFile(c.StateFile, []byte(bad), 0600); err != nil {
			t.Fatal(err)
		}
		for _, initialize := range []bool{false, true} {
			if r, err := newRouter(file, initialize); err == nil {
				r.close()
				t.Fatalf("corrupt state %q accepted (init=%t)", bad, initialize)
			}
		}
	}
}

// rpc_router_errors_total drives the request-error alert. It must count
// provider and transport failures only: an ordinary JSON-RPC error reply (which
// some daemons send as HTTP 500) and a caller that hangs up early are not outages.
func TestErrorMetricCountsOnlyUpstreamFailures(t *testing.T) {
	release := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch req.URL.Path {
		case "/rpcerror":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(500)
			fmt.Fprint(w, `{"result":null,"error":{"code":-8,"message":"Block height out of range"},"id":1}`)
		case "/notfound":
			w.WriteHeader(404)
		case "/busy":
			w.WriteHeader(503)
		case "/limited":
			w.WriteHeader(429)
		case "/slow":
			w.Header().Set("Content-Length", "1048576")
			w.WriteHeader(200)
			fmt.Fprint(w, `{"result":"complete"}`)
			w.(http.Flusher).Flush()
			select {
			case <-req.Context().Done():
			case <-release:
			}
		default:
			fmt.Fprint(w, "ok")
		}
	}))
	defer upstream.Close()
	defer close(release)
	r, s := fixtureRouter(t, map[string]map[string]string{"delta": {"normal": upstream.URL}})
	labels := []string{`chain="delta"`, `backend="normal"`}
	errorsTotal := func() float64 { return metricSum(t, r, "rpc_router_errors_total", labels...) }

	for _, p := range []string{"/delta/ok", "/delta/rpcerror", "/delta/notfound"} {
		resp, err := http.Get(s.URL + p)
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}
	if n := errorsTotal(); n != 0 {
		t.Fatalf("RPC-level replies counted as %v upstream errors", n)
	}
	if n := metricSum(t, r, "rpc_router_upstream_status_total", append(labels, `class="5xx"`)...); n != 1 {
		t.Fatalf("5xx replies not classified: %v", n)
	}
	if n := metricSum(t, r, "rpc_router_upstream_status_total", append(labels, `class="4xx"`)...); n != 1 {
		t.Fatalf("4xx replies not classified: %v", n)
	}

	// The caller decodes the JSON it needs, then closes before HTTP EOF. This is
	// Bifrost's normal client behaviour.
	resp, err := http.Get(s.URL + "/delta/slow")
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, len(`{"result":"complete"}`))
	if _, err := io.ReadFull(resp.Body, buf); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	s.CloseClientConnections()
	deadline := time.Now().Add(5 * time.Second)
	for metricSum(t, r, "rpc_router_client_canceled_total", labels...) != 1 {
		if time.Now().After(deadline) {
			t.Fatalf("early client close not counted as a cancellation; errors=%v", errorsTotal())
		}
		time.Sleep(10 * time.Millisecond)
	}
	if n := errorsTotal(); n != 0 {
		t.Fatalf("early client close counted as %v upstream errors", n)
	}

	// Provider overload and rate limiting are real failures.
	for _, p := range []string{"/delta/busy", "/delta/limited"} {
		resp, err := http.Get(s.URL + p)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
	}
	if n := errorsTotal(); n != 2 {
		t.Fatalf("503 and 429 counted as %v upstream errors, want 2", n)
	}
	// An unreachable provider is a real failure too.
	upstream.CloseClientConnections()
	upstream.Close()
	if resp, err := http.Get(s.URL + "/delta/ok"); err == nil {
		resp.Body.Close()
	}
	if n := errorsTotal(); n != 3 {
		t.Fatalf("transport failure counted as %v upstream errors, want 3", n)
	}
}
