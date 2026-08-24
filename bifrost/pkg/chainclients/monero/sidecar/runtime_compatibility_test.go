package sidecar

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"gitlab.com/thorchain/thornode/v3/config"
)

func compatibleRuntimeInfo(capabilities ...string) Info {
	return Info{
		Protocol:          ProtocolName,
		APIMajor:          APIMajorV2,
		GitCommit:         strings.Repeat("a", 40),
		Network:           "chainnet",
		MoneroNettype:     "mainnet",
		MoneroGenesisHash: MoneroMainnetGenesisHash,
		StateSchema:       StateSchemaV4,
		Capabilities:      capabilities,
	}
}

func runtimeCompatibilityTestClient(t *testing.T, endpoint string) *Client {
	t.Helper()
	client, err := NewClientWithEndpoint(config.BifrostChainConfiguration{
		SignerExpectedNetwork: "chainnet",
		ChainNetwork:          "mainnet",
	}, endpoint)
	if err != nil {
		t.Fatalf("new sidecar client: %v", err)
	}
	return client
}

func expireRuntimeCompatibility(client *Client) {
	client.runtimeCompatibilityMu.Lock()
	client.runtimeCompatibilityCheckedAt = time.Time{}
	client.runtimeCompatibilityMu.Unlock()
}

func TestRuntimeCompatibilityCacheAndCryptoBoundaries(t *testing.T) {
	t.Parallel()

	var infoCalls atomic.Int32
	var dkgCalls atomic.Int32
	var prepareCalls atomic.Int32
	var infoMode atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/monero/info":
			infoCalls.Add(1)
			info := compatibleRuntimeInfo(CapabilityBalanceReconciliation)
			if infoMode.Load() == 1 {
				info.MoneroNettype = "stagenet"
				info.MoneroGenesisHash = MoneroStagenetGenesisHash
			}
			_ = json.NewEncoder(w).Encode(info)
		case "/v1/monero/dkg/round1":
			dkgCalls.Add(1)
			_ = json.NewEncoder(w).Encode(DKGRound1Resp{Commitments: "commitments"})
		case "/v1/monero/sign/prepare":
			prepareCalls.Add(1)
			_ = json.NewEncoder(w).Encode(SignPrepareResp{SessionID: "session", Preprocess: "preprocess"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := runtimeCompatibilityTestClient(t, server.URL)
	if _, err := client.EnableRuntimeCompatibility(time.Hour, CapabilityBalanceReconciliation); err != nil {
		t.Fatalf("enable runtime compatibility: %v", err)
	}
	if _, err := client.DKGRound1(DKGRound1Req{}); err != nil {
		t.Fatalf("cached dkg check: %v", err)
	}
	if _, err := client.SignPrepare(SignPrepareReq{}); err != nil {
		t.Fatalf("cached keysign check: %v", err)
	}
	if got := infoCalls.Load(); got != 1 {
		t.Fatalf("cached boundaries made %d info calls, want 1", got)
	}

	infoMode.Store(1)
	expireRuntimeCompatibility(client)
	if _, err := client.SignPrepare(SignPrepareReq{}); err == nil || !strings.Contains(err.Error(), "xmr keysign blocked") {
		t.Fatalf("expected runtime keysign mismatch rejection, got %v", err)
	}
	if got := prepareCalls.Load(); got != 1 {
		t.Fatalf("rejected keysign reached prepare endpoint %d times, want 1", got)
	}
	if _, err := client.DKGRound1(DKGRound1Req{}); err == nil || !strings.Contains(err.Error(), "xmr dkg blocked") {
		t.Fatalf("expected runtime dkg mismatch rejection, got %v", err)
	}
	if got := dkgCalls.Load(); got != 1 {
		t.Fatalf("rejected dkg reached round1 endpoint %d times, want 1", got)
	}

	infoMode.Store(0)
	if _, err := client.SignPrepare(SignPrepareReq{}); err != nil {
		t.Fatalf("keysign did not recover after signer compatibility was restored: %v", err)
	}
}

func TestRuntimeCompatibilityRefreshIsSingleFlight(t *testing.T) {
	t.Parallel()

	var infoCalls atomic.Int32
	var prepareCalls atomic.Int32
	refreshStarted := make(chan struct{})
	releaseRefresh := make(chan struct{})
	var releaseRefreshOnce sync.Once
	release := func() {
		releaseRefreshOnce.Do(func() { close(releaseRefresh) })
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/monero/info":
			if infoCalls.Add(1) == 2 {
				close(refreshStarted)
				<-releaseRefresh
			}
			_ = json.NewEncoder(w).Encode(compatibleRuntimeInfo())
		case "/v1/monero/sign/prepare":
			prepareCalls.Add(1)
			_ = json.NewEncoder(w).Encode(SignPrepareResp{SessionID: "session", Preprocess: "preprocess"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	defer release()

	client := runtimeCompatibilityTestClient(t, server.URL)
	if _, err := client.EnableRuntimeCompatibility(time.Hour); err != nil {
		t.Fatalf("enable runtime compatibility: %v", err)
	}
	expireRuntimeCompatibility(client)

	const callers = 12
	start := make(chan struct{})
	ready := make(chan struct{}, callers)
	errs := make(chan error, callers)
	var wg sync.WaitGroup
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ready <- struct{}{}
			<-start
			_, err := client.SignPrepare(SignPrepareReq{})
			errs <- err
		}()
	}
	for range callers {
		<-ready
	}
	close(start)
	select {
	case <-refreshStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("runtime compatibility refresh did not start")
	}
	release()
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent keysign boundary failed: %v", err)
		}
	}
	if got := infoCalls.Load(); got != 2 {
		t.Fatalf("concurrent boundaries made %d info calls, want startup plus one refresh", got)
	}
	if got := prepareCalls.Load(); got != callers {
		t.Fatalf("prepare endpoint calls = %d, want %d", got, callers)
	}
}

func TestSignCombineRevalidatesBeforeAmbiguousRetry(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name       string
		secondInfo func() Info
		want       string
	}{
		{
			name: "unavailable signer info",
			secondInfo: func() Info {
				return Info{}
			},
			want: "runtime signer compatibility refresh failed",
		},
		{
			name: "idempotent combine capability removed",
			secondInfo: func() Info {
				return compatibleRuntimeInfo()
			},
			want: "no longer advertises",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var infoCalls atomic.Int32
			var combineCalls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/v1/monero/info":
					if infoCalls.Add(1) == 1 {
						_ = json.NewEncoder(w).Encode(compatibleRuntimeInfo(CapabilityIdempotentCombine))
						return
					}
					if tc.name == "unavailable signer info" {
						http.Error(w, "unavailable", http.StatusServiceUnavailable)
						return
					}
					_ = json.NewEncoder(w).Encode(tc.secondInfo())
				case "/v1/monero/sign/combine":
					combineCalls.Add(1)
					w.WriteHeader(http.StatusServiceUnavailable)
					_, _ = w.Write([]byte(`{"code":"signer_busy"}`))
				default:
					http.NotFound(w, r)
				}
			}))
			defer server.Close()

			client := runtimeCompatibilityTestClient(t, server.URL)
			if _, err := client.EnableRuntimeCompatibility(time.Hour); err != nil {
				t.Fatalf("enable runtime compatibility: %v", err)
			}
			client.SetSignCombineIdempotent(true)
			client.signCombineInitialBackoff = time.Millisecond
			client.signCombineMaxBackoff = time.Millisecond

			_, err := client.SignCombineContext(context.Background(), SignCombineReq{
				MsgID: "msg", SessionID: "session", AllShares: map[string]string{"node": "share"},
			})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected %q retry rejection, got %v", tc.want, err)
			}
			if got := combineCalls.Load(); got != 1 {
				t.Fatalf("ambiguous combine was sent %d times, want exactly once", got)
			}
			if got := infoCalls.Load(); got != 2 {
				t.Fatalf("combine retry made %d info calls, want startup plus forced refresh", got)
			}
		})
	}
}

func TestRuntimeCompatibilityWaitHonorsCancellation(t *testing.T) {
	t.Parallel()

	refreshStarted := make(chan struct{})
	releaseRefresh := make(chan struct{})
	var releaseRefreshOnce sync.Once
	release := func() {
		releaseRefreshOnce.Do(func() { close(releaseRefresh) })
	}
	var infoCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if infoCalls.Add(1) == 2 {
			close(refreshStarted)
			<-releaseRefresh
		}
		_ = json.NewEncoder(w).Encode(compatibleRuntimeInfo())
	}))
	defer server.Close()
	defer release()

	client := runtimeCompatibilityTestClient(t, server.URL)
	if _, err := client.EnableRuntimeCompatibility(time.Hour); err != nil {
		t.Fatalf("enable runtime compatibility: %v", err)
	}
	expireRuntimeCompatibility(client)
	refreshDone := make(chan error, 1)
	go func() {
		_, _, err := client.requireRuntimeCompatibility(context.Background(), false)
		refreshDone <- err
	}()
	select {
	case <-refreshStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("runtime compatibility refresh did not start")
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err := client.requireRuntimeCompatibility(ctx, false)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("waiting compatibility caller did not honor cancellation: %v", err)
	}
	release()
	if err = <-refreshDone; err != nil {
		t.Fatalf("primary compatibility refresh failed: %v", err)
	}
}
