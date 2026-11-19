package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type prefetchBarrierTransport struct {
	base    http.RoundTripper
	entered chan<- struct{}
	release <-chan struct{}
}

type prefetchLoadTransport struct {
	base         http.RoundTripper
	active, peak atomic.Int64
	mu           sync.Mutex
	failures     []string
}

func (d *prefetchLoadTransport) RoundTrip(r *http.Request) (response *http.Response, err error) {
	started := time.Now()
	defer func() {
		if err != nil {
			d.mu.Lock()
			defer d.mu.Unlock()
			if len(d.failures) < 8 {
				d.failures = append(d.failures, fmt.Sprintf("%s after %s: %T: %v", r.URL.Path, time.Since(started), err, err))
			}
		}
	}()
	n := d.active.Add(1)
	defer d.active.Add(-1)
	for old := d.peak.Load(); n > old; old = d.peak.Load() {
		if d.peak.CompareAndSwap(old, n) {
			break
		}
	}
	select {
	case <-time.After(10 * time.Millisecond):
		return d.base.RoundTrip(r)
	case <-r.Context().Done():
		return nil, r.Context().Err()
	}
}

func (d *prefetchLoadTransport) failureSummary() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.failures...)
}

const prefetchUpstreamTimeout = time.Second

func configurePrefetchUpstreams(t *testing.T, p *Proxy) {
	t.Helper()
	for _, upstream := range []*Upstream{p.local, p.external} {
		upstream.Client.CloseIdleConnections()
		// Changing Client.Timeout alone leaves the fixture's 100ms transport
		// deadlines in force. Use the same bounded budget for every phase.
		upstream.Client = newUpstream(upstream.API, upstream.RPC, prefetchUpstreamTimeout).Client
		transport := upstream.Client.Transport.(*http.Transport)
		t.Cleanup(transport.CloseIdleConnections)
	}
}

// Bifrost's TRON scanner overlaps block fetches; receipts are fetched serially
// within each block. Use 32 such streams plus concurrent head/RPC requests.
func TestBifrostPrefetchBlockReceiptWorkload(t *testing.T) {
	for _, mode := range []string{"local", "external", "historical", "local-external-unhealthy", "local-broadcast-retry", "auto-failback"} {
		t.Run(mode, func(t *testing.T) {
			p, store, localNode, externalNode := fixture(t)
			p.setMode("local")
			if mode == "auto-failback" {
				f := recovery(t)
				f.fullMinute(t)
				p, store, localNode, externalNode = f.p, f.s, f.local, f.external
			}
			if mode == "external" {
				p.setMode("external")
			}
			if mode == "historical" {
				p.cfg.BlockFloor = 95000
			}
			configurePrefetchUpstreams(t, p)
			if mode == "local-external-unhealthy" {
				p.external.Client.Transport = externalProbeFailureTransport{base: p.external.Client.Transport, kind: "http-503"}
				h := p.probe(context.Background())
				if h.External || !h.Local || p.currentMode() != "local" {
					t.Fatalf("external outage abandoned admitted local: %+v", h)
				}
			}
			local := &prefetchLoadTransport{base: p.local.Client.Transport}
			external := &prefetchLoadTransport{base: p.external.Client.Transport}
			p.local.Client.Transport, p.external.Client.Transport = local, external
			server := httptest.NewServer(p)
			defer server.Close()
			client := &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{MaxConnsPerHost: 64, MaxIdleConnsPerHost: 64}}
			defer client.CloseIdleConnections()
			started := time.Now()
			diagnostics := func() string {
				h := p.snapshot()
				now := time.Now()
				return fmt.Sprintf("elapsed=%s wall_elapsed=%s health_age=%s checked=%s fresh=%t health=%+v mode=%s unsafe=%t requests=%d fallbacks=%d ambiguous=%d admission={active:%d waiting:%d rejected:%d timed_out:%d canceled:%d} transport_errors={local:%q external:%q}",
					now.Sub(started), now.UTC().Sub(started.UTC()), now.Sub(h.Checked), h.Checked.Format(time.RFC3339Nano), p.fresh(h), h, p.currentMode(), p.unsafe.Load(), p.requests.Load(), p.fallbacks.Load(), p.ambiguous.Load(),
					len(p.admission.active), len(p.admission.waiting), p.admission.rejected.Load(), p.admission.timedOut.Load(), p.admission.canceled.Load(), local.failureSummary(), external.failureSummary())
			}
			defer func() {
				if t.Failed() {
					t.Log(diagnostics())
				}
			}()
			var calls atomic.Int64
			call := func(route string, body []byte) error {
				response, err := client.Post(server.URL+route, "application/json", bytes.NewReader(body))
				if err != nil {
					return fmt.Errorf("%s: %w; %s", route, err, diagnostics())
				}
				defer response.Body.Close()
				data, err := io.ReadAll(response.Body)
				if err != nil {
					return fmt.Errorf("%s reading response: %w; %s", route, err, diagnostics())
				}
				if response.StatusCode != 200 || !validResponse(route, body, data) {
					return fmt.Errorf("%s: HTTP %d, body=%q; %s", route, response.StatusCode, data, diagnostics())
				}
				calls.Add(1)
				return nil
			}
			workers := 33
			var broadcastBody []byte
			if mode == "local-broadcast-retry" {
				workers++
				localNode.mu.Lock()
				localNode.response = "duplicate"
				localNode.mu.Unlock()
				broadcastBody = signed(t, false, true, time.Now().Add(time.Hour))
			}
			start := make(chan struct{})
			errors := make(chan error, workers)
			if broadcastBody != nil {
				go func() {
					<-start
					response, err := client.Post(server.URL+"/wallet/broadcasttransaction", "application/json", bytes.NewReader(broadcastBody))
					if err == nil {
						_ = response.Body.Close()
						if response.StatusCode != http.StatusOK {
							err = fmt.Errorf("broadcast retry during prefetch returned HTTP %d", response.StatusCode)
						}
					}
					errors <- err
				}()
			}
			for worker := range 32 {
				go func() {
					<-start
					for window := range 2 {
						height := 90000 + window*32 + worker
						q, _ := json.Marshal(map[string]int{"num": height})
						if err := call("/wallet/getblockbynum", q); err != nil {
							errors <- err
							return
						}
						for receipt := range 16 {
							q = []byte(fmt.Sprintf(`{"value":"%064x"}`, height*100+receipt))
							if err := call("/wallet/gettransactioninfobyid", q); err != nil {
								errors <- err
								return
							}
						}
					}
					errors <- nil
				}()
			}
			go func() {
				<-start
				for range 64 {
					if err := call("/wallet/getnowblock", []byte("null")); err != nil {
						errors <- err
						return
					}
					if err := call("/jsonrpc", []byte(`{"jsonrpc":"2.0","id":1,"method":"eth_blockNumber","params":[]}`)); err != nil {
						errors <- err
						return
					}
				}
				errors <- nil
			}()
			close(start)
			for range workers {
				if err := <-errors; err != nil {
					t.Error(err)
				}
			}
			if calls.Load() != 1216 {
				t.Errorf("completed %d requests, want 1216", calls.Load())
			}
			if local.peak.Load() > maxActiveRequests || external.peak.Load() > maxActiveRequests {
				t.Fatal("upstream concurrency exceeded memory budget")
			}
			var wantBroadcasts uint64
			if broadcastBody != nil {
				wantBroadcasts = 2
				entry, _ := parseSigned(broadcastBody)
				stored, err := store.get(entry.ID)
				if err != nil || stored == nil || !stored.Accepted || stored.Attempts != 2 || !bytes.Equal(stored.Body, broadcastBody) {
					t.Errorf("prefetch broadcast journal invalid: %+v %v", stored, err)
				}
				localNode.mu.Lock()
				externalNode.mu.Lock()
				if len(localNode.writes) != 1 || len(externalNode.writes) != 1 || !bytes.Equal(localNode.writes[0], broadcastBody) || !bytes.Equal(externalNode.writes[0], broadcastBody) {
					t.Error("prefetch changed broadcast payload or retry count")
				}
				externalNode.mu.Unlock()
				localNode.mu.Unlock()
			}
			if p.fallbacks.Load() != 0 || p.broadcasts.Load() != wantBroadcasts {
				t.Fatal("load caused fallback or broadcast")
			}
			if p.admission.rejected.Load() != 0 || p.admission.timedOut.Load() != 0 || p.admission.canceled.Load() != 0 || len(p.admission.active) != 0 || len(p.admission.waiting) != 0 {
				t.Fatal("load caused admission errors or leaked permits")
			}
			if mode == "local-external-unhealthy" && external.peak.Load() != 0 {
				t.Fatal("workload dispatched to unhealthy external")
			}
			wantMode := "local"
			if mode == "external" {
				wantMode = "external"
			}
			if p.currentMode() != wantMode {
				t.Fatal("load changed routing")
			}
			t.Logf("%d requests; upstream peaks local=%d external=%d", calls.Load(), local.peak.Load(), external.peak.Load())
		})
	}
}

func (d prefetchBarrierTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	d.entered <- struct{}{}
	select {
	case <-d.release:
		return d.base.RoundTrip(r)
	case <-r.Context().Done():
		return nil, r.Context().Err()
	}
}

// Retain the regression for the original 16-active/immediate-rejection gate.
// Hold half of Bifrost's prefetch window in flight while the other half arrives.
func TestBifrostPrefetch32Admission(t *testing.T) {
	p, _, _, _ := fixture(t)
	p.setMode("local")
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	entered := make(chan struct{}, 32)
	p.local.Client.Timeout = 5 * time.Second
	p.local.Client.Transport = prefetchBarrierTransport{p.local.Client.Transport, entered, release}
	results := make(chan *httptest.ResponseRecorder, 32)
	launch := func(i int) {
		go func() {
			results <- request(p, "/wallet/getblockbynum", []byte(fmt.Sprintf(`{"num":%d}`, 90000+i)))
		}()
	}
	for i := range 16 {
		launch(i)
	}
	for range 16 {
		select {
		case <-entered:
		case <-time.After(2 * time.Second):
			t.Fatal("did not establish 16 active requests")
		}
	}
	for i := 16; i < 32; i++ {
		launch(i)
	}
	var responses []*httptest.ResponseRecorder
	select {
	case w := <-results:
		responses = append(responses, w)
	case <-time.After(200 * time.Millisecond):
	}
	unblock()
	for len(responses) < 32 {
		select {
		case w := <-results:
			responses = append(responses, w)
		case <-time.After(5 * time.Second):
			t.Fatal("prefetch window did not drain")
		}
	}
	for _, w := range responses {
		if w.Code != http.StatusOK {
			t.Errorf("32-block prefetch rejected: HTTP %d: %s", w.Code, w.Body.String())
		}
	}
	if p.currentMode() != "local" || p.fallbacks.Load() != 0 {
		t.Fatal("healthy burst changed upstream routing")
	}
}

func TestBifrostPrefetch32ActiveWithQueuedHead(t *testing.T) {
	p, _, _, _ := fixture(t)
	p.setMode("local")
	release := make(chan struct{})
	var once sync.Once
	var wg sync.WaitGroup
	unblock := func() { once.Do(func() { close(release) }) }
	defer func() { unblock(); wg.Wait() }()
	entered := make(chan struct{}, 33)
	p.local.Client.Timeout = 5 * time.Second
	p.local.Client.Transport = prefetchBarrierTransport{p.local.Client.Transport, entered, release}
	results := make(chan *httptest.ResponseRecorder, 33)
	launch := func(route string, body []byte) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results <- request(p, route, body)
		}()
	}
	for i := range 32 {
		launch("/wallet/getblockbynum", []byte(fmt.Sprintf(`{"num":%d}`, 90000+i)))
	}
	for range 32 {
		select {
		case <-entered:
		case <-time.After(2 * time.Second):
			t.Fatal("full 32-block window did not reach upstream concurrently")
		}
	}
	launch("/wallet/getnowblock", []byte("null"))
	waitForAdmissionQueue(t, p.admission, 1)
	if len(p.admission.active) != 32 {
		t.Fatal("queued head request changed active concurrency")
	}
	unblock()
	wg.Wait()
	for range 33 {
		if w := <-results; w.Code != http.StatusOK {
			t.Errorf("prefetch/head request rejected: HTTP %d: %s", w.Code, w.Body.String())
		}
	}
	if len(p.admission.active) != 0 || len(p.admission.waiting) != 0 || p.currentMode() != "local" || p.fallbacks.Load() != 0 {
		t.Fatal("burst leaked admission permits or changed upstream routing")
	}
}
