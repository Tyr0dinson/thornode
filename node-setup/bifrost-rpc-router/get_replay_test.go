package main

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
)

// getReplayFixture is a P1 whose selected "adapter" backend is the route under
// test; "normal" is the other backend, which no request may ever reach.
func getReplayFixture(t *testing.T, other, selected string) (*router, *httptest.Server) {
	t.Helper()
	r, s := fixtureRouter(t, map[string]map[string]string{"gaia": {"normal": other, "adapter": selected}})
	switchRoute(t, r, "gaia", "adapter")
	return r, s
}

func getReplayRequest(t *testing.T, client *http.Client, target string, headers http.Header) int {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, target, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header = headers.Clone()
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_, readErr := io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if readErr != nil {
		t.Fatal(readErr)
	}
	return resp.StatusCode
}

func getReplayCloseConnection(t *testing.T, w http.ResponseWriter) {
	t.Helper()
	conn, _, err := w.(http.Hijacker).Hijack()
	if err != nil {
		t.Error(err)
		return
	}
	conn.Close()
}

func getReplayWireCheck(t *testing.T, req *http.Request, wantURI string) {
	t.Helper()
	body, err := io.ReadAll(req.Body)
	if err != nil || req.Method != http.MethodGet || req.RequestURI != wantURI || len(body) != 0 {
		t.Errorf("GET wire changed: method=%s URI=%q body=%q err=%v", req.Method, req.RequestURI, body, err)
	}
	if req.ContentLength != 0 || len(req.TransferEncoding) != 0 || req.Header.Get("Transfer-Encoding") != "" || req.Header.Get("Content-Length") != "" {
		t.Errorf("bodyless GET gained framing: length=%d transfer=%v headers=%v", req.ContentLength, req.TransferEncoding, req.Header)
	}
	if req.ProtoMajor != 1 || req.ProtoMinor != 1 {
		t.Errorf("GET changed HTTP protocol: %s", req.Proto)
	}
	if _, exists := req.Header["Idempotency-Key"]; exists {
		t.Error("Idempotency-Key reached upstream")
	}
	if _, exists := req.Header["X-Idempotency-Key"]; exists {
		t.Error("X-Idempotency-Key reached upstream")
	}
}

func TestGETBroadcastWarmConnectionNeverReplays(t *testing.T) {
	for _, topology := range []string{"direct", "two_layers_provider_disconnect", "two_layers_adapter_disconnect"} {
		for _, method := range []string{"broadcast_tx_sync", "broadcast_tx_async", "broadcast_tx_commit"} {
			for _, hints := range []string{"none", "keys", "empty_keys"} {
				t.Run(topology+"/"+method+"/"+hints, func(t *testing.T) {
					const query = `tx=%220x73796e746865746963%22`
					var broadcasts, alternate atomic.Int32
					var connMu sync.Mutex
					var warmed string
					provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
						if req.URL.Path == "/comet/status" {
							getReplayWireCheck(t, req, "/comet/status")
							connMu.Lock()
							warmed = req.RemoteAddr
							connMu.Unlock()
							fmt.Fprint(w, `{"jsonrpc":"2.0","id":-1,"result":{}}`)
							return
						}
						getReplayWireCheck(t, req, "/comet/"+method+"?"+query)
						attempt := broadcasts.Add(1)
						connMu.Lock()
						if attempt == 1 && (warmed == "" || warmed != req.RemoteAddr) {
							t.Error("broadcast did not use the warmed provider connection")
						}
						connMu.Unlock()
						if topology != "two_layers_adapter_disconnect" {
							// The provider has received the broadcast; lose its response.
							getReplayCloseConnection(t, w)
							return
						}
						fmt.Fprint(w, `{"jsonrpc":"2.0","id":-1,"result":{"code":0}}`)
					}))
					t.Cleanup(provider.Close)
					fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
						alternate.Add(1)
						w.WriteHeader(http.StatusServiceUnavailable)
					}))
					t.Cleanup(fallback.Close)
					preferred := provider.URL + "/comet"
					var p2 *router
					if topology != "direct" {
						_, filename := fixtureConfig(t, map[string]map[string]string{"gaia": {"normal": preferred}})
						var err error
						p2, err = newRouter(filename, true)
						if err != nil {
							t.Fatal(err)
						}
						var hopMu sync.Mutex
						var warmedHop string
						var hopBroadcasts atomic.Int32
						s2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
							if req.URL.Path == "/gaia/status" {
								hopMu.Lock()
								warmedHop = req.RemoteAddr
								hopMu.Unlock()
								p2.ServeHTTP(w, req)
								return
							}
							attempt := hopBroadcasts.Add(1)
							hopMu.Lock()
							if attempt == 1 && (warmedHop == "" || warmedHop != req.RemoteAddr) {
								t.Error("broadcast did not use the warmed P1-to-P2 connection")
							}
							hopMu.Unlock()
							if topology == "two_layers_adapter_disconnect" {
								// P2 finishes forwarding, but its reply never reaches P1.
								p2.ServeHTTP(httptest.NewRecorder(), req)
								getReplayCloseConnection(t, w)
								return
							}
							p2.ServeHTTP(w, req)
						}))
						t.Cleanup(func() { s2.Close(); p2.close() })
						preferred = s2.URL + "/gaia"
					}
					p1, s1 := getReplayFixture(t, fallback.URL, preferred)
					if code := getReplayRequest(t, s1.Client(), s1.URL+"/gaia/status", nil); code != http.StatusOK {
						t.Fatalf("warmup returned HTTP %d", code)
					}
					headers := http.Header{}
					if hints != "none" {
						value := "never-replay"
						if hints == "empty_keys" {
							value = ""
						}
						headers.Set("Idempotency-Key", value)
						headers.Set("X-Idempotency-Key", value)
					}
					code := getReplayRequest(t, s1.Client(), s1.URL+"/gaia/"+method+"?"+query, headers)
					if code != http.StatusBadGateway || broadcasts.Load() != 1 || alternate.Load() != 0 {
						t.Fatalf("ambiguous GET replayed: HTTP=%d broadcasts=%d fallback=%d", code, broadcasts.Load(), alternate.Load())
					}
					if p1.current.Load().state.Routes["gaia"] != "adapter" || p2 != nil && p2.current.Load().state.Routes["gaia"] != "normal" {
						t.Error("GET broadcast changed routes")
					}
				})
			}
		}
	}
}

func TestGETReadWireCompatibility(t *testing.T) {
	for _, layers := range []int{1, 2} {
		t.Run(fmt.Sprint(layers), func(t *testing.T) {
			suffixes := []string{
				"/status", "/block?height=123", "/block_results?height=123",
				`/abci_query?path=%22%2Fstore%2Fbank%2Fkey%22&data=0x00ff&height=123&prove=false`,
			}
			var calls atomic.Int32
			var connMu sync.Mutex
			var connection string
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				index := int(calls.Add(1)) - 1
				if index >= len(suffixes) {
					t.Error("unexpected extra GET")
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				getReplayWireCheck(t, req, "/comet"+suffixes[index])
				connMu.Lock()
				if connection != "" && connection != req.RemoteAddr {
					t.Error("successful GETs stopped reusing the upstream connection")
				}
				connection = req.RemoteAddr
				connMu.Unlock()
				fmt.Fprint(w, `{"jsonrpc":"2.0","id":-1,"result":{}}`)
			}))
			t.Cleanup(provider.Close)
			preferred := provider.URL + "/comet"
			if layers == 2 {
				_, s2 := fixtureRouter(t, map[string]map[string]string{"gaia": {"normal": preferred}})
				preferred = s2.URL + "/gaia"
			}
			fallback := textServer(t, "unexpected fallback")
			_, s1 := getReplayFixture(t, fallback.URL, preferred)
			for _, suffix := range suffixes {
				if code := getReplayRequest(t, s1.Client(), s1.URL+"/gaia"+suffix, nil); code != http.StatusOK {
					t.Errorf("GET %s returned HTTP %d", suffix, code)
				}
			}
			if calls.Load() != 4 {
				t.Errorf("read requests lost or replayed: calls=%d", calls.Load())
			}
		})
	}
}

func TestGETReadFailureIsNotRetriedOnOtherBackend(t *testing.T) {
	var first, second atomic.Int32
	const query = `height=123`
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		second.Add(1)
		fmt.Fprint(w, `{"jsonrpc":"2.0","id":-1,"result":{}}`)
	}))
	t.Cleanup(other.Close)
	selected := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		getReplayWireCheck(t, req, "/primary/block_results?"+query)
		first.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(selected.Close)
	p1, s1 := getReplayFixture(t, other.URL+"/backup", selected.URL+"/primary")
	code := getReplayRequest(t, s1.Client(), s1.URL+"/gaia/block_results?"+query, nil)
	if code != http.StatusServiceUnavailable || first.Load() != 1 || second.Load() != 0 || p1.current.Load().state.Routes["gaia"] != "adapter" {
		t.Fatalf("failed GET read was retried or rerouted: HTTP=%d attempts=%d/%d", code, first.Load(), second.Load())
	}
}
