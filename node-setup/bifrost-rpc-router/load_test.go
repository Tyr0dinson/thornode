package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func TestConcurrentSwitchesUnder32RequestWorkload(t *testing.T) {
	var total atomic.Int64
	provider := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		total.Add(1)
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(body)
	})
	a, b := httptest.NewServer(provider), httptest.NewServer(provider)
	defer a.Close()
	defer b.Close()
	p2, s2 := fixtureRouter(t, map[string]map[string]string{"alpha": {"normal": a.URL, "other": b.URL}})
	p1, s1 := fixtureRouter(t, map[string]map[string]string{"alpha": {"normal": a.URL, "adapter": s2.URL + "/alpha"}})
	start := make(chan struct{})
	var wg sync.WaitGroup
	for worker := 0; worker < 32; worker++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			<-start
			for i := 0; i < 64; i++ {
				payload := fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"eth_getBlockByNumber","params":["0x10",true]}`, worker*1000+i)
				code, body, err := post(s1.Client(), s1.URL+"/alpha", payload)
				if err != nil || code != 200 || body != payload {
					t.Errorf("request lost/corrupted: %d %v", code, err)
					return
				}
			}
		}(worker)
	}
	close(start)
	for i := 0; i < 10; i++ {
		switchRoute(t, p2, "alpha", "other")
		switchRoute(t, p1, "alpha", "adapter")
		switchRoute(t, p2, "alpha", "normal")
		switchRoute(t, p1, "alpha", "normal")
	}
	wg.Wait()
	if total.Load() != 32*64 {
		t.Fatalf("lost or duplicated requests: %d", total.Load())
	}
	if p1.admission["alpha"].rejected.Load() != 0 || p2.admission["alpha"].rejected.Load() != 0 {
		t.Fatal("rejected normal prefetch load")
	}
}

// The shipped examples must load, and must keep the layering rule: the P1
// chain switches between "native" and "adapter".
func TestExampleCatalogsValidate(t *testing.T) {
	for _, name := range []string{"gateway", "adapter"} {
		t.Run(name, func(t *testing.T) {
			b, err := os.ReadFile(filepath.Join("examples", name+".json"))
			if err != nil {
				t.Fatal(err)
			}
			var c Config
			if err := strictJSON(b, &c); err != nil {
				t.Fatal(err)
			}
			ch, ok := c.Chains["base"]
			if len(c.Chains) != 1 || !ok {
				t.Fatalf("%s example must describe base only", name)
			}
			if name == "gateway" {
				if len(ch.Backends) != 2 || ch.Backends["native"] == nil || ch.Backends["adapter"] == nil || ch.Default != "adapter" {
					t.Fatal("base must switch between native and adapter, default adapter")
				}
			} else if ch.HeadCacheMS == 0 || ch.ReadAheadBlocks == 0 || !ch.ReadAheadHeaders || !ch.CallAhead {
				t.Fatal("the adapter example must show the options base uses")
			}
			_, f := fixtureConfig(t, map[string]map[string]string{"alpha": {"normal": "https://example.invalid"}})
			dir := filepath.Dir(f)
			c.AdminSocket = filepath.Join(dir, "control.sock")
			c.StateFile = filepath.Join(dir, "state.json")
			for _, ch := range c.Chains {
				for _, endpoints := range ch.Backends {
					for id, original := range endpoints {
						file := filepath.Join(dir, filepath.Base(original))
						writeJSON(t, file, Target{URL: "https://example.invalid/rpc"})
						endpoints[id] = file
					}
				}
			}
			writeJSON(t, f, c)
			if _, err := loadCatalog(f); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestBatchAndJSONRPCErrorAreNotRewritten(t *testing.T) {
	payload := `[{"jsonrpc":"2.0","id":1,"error":{"code":-32000,"message":"missing history"}},{"id":2,"result":null}]`
	u := textServer(t, payload)
	_, s := fixtureRouter(t, map[string]map[string]string{"alpha": {"normal": u.URL}})
	code, body, err := post(s.Client(), s.URL+"/alpha", `[{"id":1,"method":"eth_getBlockByNumber"},{"id":2,"method":"eth_getTransactionReceipt"}]`)
	if code != 200 || body != payload || err != nil {
		t.Fatal("RPC error converted to success or rewritten")
	}
}

func TestMetricsAndPublicControlIsolation(t *testing.T) {
	u := textServer(t, "ok")
	r, s := fixtureRouter(t, map[string]map[string]string{"alpha": {"normal": u.URL, "other": u.URL}})
	code, _, err := post(s.Client(), s.URL+"/alpha", "{}")
	if err != nil || code != 200 {
		t.Fatal("request failed")
	}
	switchRoute(t, r, "alpha", "other")
	resp, err := s.Client().Get(s.URL + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`rpc_router_revision 2`, `rpc_router_selected{chain="alpha",backend="other"} 1`, `rpc_router_requests_total{chain="alpha",backend="normal",endpoint="rpc"} 1`} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("metric missing: %s", want)
		}
	}
	if strings.Contains(string(data), u.URL) {
		t.Fatal("provider URL in metrics")
	}
	for _, endpoint := range []string{"/routes", "/switch", "/reload"} {
		resp, err := s.Client().Get(s.URL + endpoint)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != 404 {
			t.Fatal("admin exposed")
		}
	}
	for _, endpoint := range []string{"/healthz", "/readyz"} {
		resp, err := s.Client().Get(s.URL + endpoint)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Fatal("control endpoint failed")
		}
	}
}

func FuzzStrictJSON(f *testing.F) {
	for _, seed := range []string{`{}`, `{"listen":"127.0.0.1:8080"}`, `{"x":1,"x":2}`, `[null,true,1,"a"]`} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, s string) {
		if len(s) > 4096 {
			return
		}
		var out any
		err := strictJSON([]byte(s), &out)
		if err == nil && !json.Valid([]byte(s)) {
			t.Fatalf("invalid JSON accepted: %q", s)
		}
		if strings.TrimSpace(s) == "" && err == nil {
			t.Fatal("empty JSON accepted")
		}
	})
}
