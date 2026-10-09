package sidecar

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

// noHealthEngineCov implements Engine without the optional healthChecker.
type noHealthEngineCov struct{ Engine }

func serveCov(t *testing.T, s *Server, method, path, body string, header map[string]string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	for k, v := range header {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	s.server.Handler.ServeHTTP(rec, req)
	require.Equal(t, "application/json", rec.Header().Get("Content-Type"))
	out := map[string]any{}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	return rec.Code, out
}

func TestServerRoutesCov(t *testing.T) {
	t.Parallel()

	boom := errors.New("engine boom")
	okEngine := &mockEngine{
		dkgRound1Resp:   DKGRound1Resp{Commitments: "c1"},
		dkgRound2Resp:   DKGRound2Resp{EncryptedShares: map[string]string{"2": "s2"}},
		dkgRound3Resp:   DKGRound3Resp{Address: "addr", GroupKey: "gk"},
		signPrepareResp: SignPrepareResp{SessionID: "sid", Preprocess: "pp"},
		signPartialResp: SignPartialResp{SignatureShare: "share"},
		signCombineResp: SignCombineResp{TxHash: "txh", TxKey: "tk"},
		broadcastResp:   BroadcastResp{TxHash: "bh"},
	}
	errEngine := &mockEngine{
		dkgRound1Err: boom, dkgRound2Err: boom, dkgRound3Err: boom,
		signPrepareErr: boom, signPartialErr: boom, signCombineErr: boom, broadcastErr: boom,
	}

	routes := []struct {
		path     string
		body     string
		key      string
		expected any
	}{
		{"/v1/monero/dkg/round1", `{"session_id":"s"}`, "commitments", "c1"},
		{"/v1/monero/dkg/round2", `{"session_id":"s"}`, "encrypted_shares", map[string]any{"2": "s2"}},
		{"/v1/monero/dkg/round3", `{"session_id":"s"}`, "address", "addr"},
		{"/v1/monero/sign/prepare", `{"msg_id":"m"}`, "preprocess", "pp"},
		{"/v1/monero/sign/partial", `{"msg_id":"m"}`, "signature_share", "share"},
		{"/v1/monero/sign/combine", `{"msg_id":"m"}`, "tx_hash", "txh"},
		{"/v1/monero/broadcast", `{"tx_hex":"aa"}`, "tx_hash", "bh"},
	}

	okSrv := NewServer("127.0.0.1:0", okEngine, zerolog.New(io.Discard))
	errSrv := NewServer("127.0.0.1:0", errEngine, zerolog.New(io.Discard))
	authSrv := NewServerWithAuth("127.0.0.1:0", okEngine, zerolog.New(io.Discard), " user ", " pass ", "")

	for _, rt := range routes {
		t.Run(rt.path, func(t *testing.T) {
			// success
			code, out := serveCov(t, okSrv, http.MethodPost, rt.path, rt.body, nil)
			require.Equal(t, http.StatusOK, code)
			require.Equal(t, rt.expected, out[rt.key])

			// wrong method
			code, out = serveCov(t, okSrv, http.MethodGet, rt.path, "", nil)
			require.Equal(t, http.StatusMethodNotAllowed, code)
			require.Equal(t, "method not allowed", out["error"])

			// unauthorized
			code, out = serveCov(t, authSrv, http.MethodPost, rt.path, rt.body, nil)
			require.Equal(t, http.StatusUnauthorized, code)
			require.Equal(t, "unauthorized", out["error"])

			// basic auth accepted (credentials are trimmed on construction)
			req := httptest.NewRequest(http.MethodPost, rt.path, strings.NewReader(rt.body))
			req.SetBasicAuth("user", "pass")
			rec := httptest.NewRecorder()
			authSrv.server.Handler.ServeHTTP(rec, req)
			require.Equal(t, http.StatusOK, rec.Code)

			// unknown field
			code, out = serveCov(t, okSrv, http.MethodPost, rt.path, `{"bogus":1}`, nil)
			require.Equal(t, http.StatusBadRequest, code)
			require.Contains(t, out["error"], "invalid request body")
			require.Contains(t, out["error"], "unknown field")

			// trailing object
			code, out = serveCov(t, okSrv, http.MethodPost, rt.path, rt.body+rt.body, nil)
			require.Equal(t, http.StatusBadRequest, code)
			require.Contains(t, out["error"], "single json object")

			// trailing garbage
			code, out = serveCov(t, okSrv, http.MethodPost, rt.path, rt.body+"}", nil)
			require.Equal(t, http.StatusBadRequest, code)
			require.Contains(t, out["error"], "invalid request body")

			// engine error
			code, out = serveCov(t, errSrv, http.MethodPost, rt.path, rt.body, nil)
			require.Equal(t, http.StatusBadRequest, code)
			require.Equal(t, "engine boom", out["error"])
		})
	}
}

func TestServerAuthWrongBearerAndBasicCov(t *testing.T) {
	t.Parallel()
	s := NewServerWithAuth("127.0.0.1:0", &mockEngine{dkgRound1Resp: DKGRound1Resp{Commitments: "c"}}, zerolog.New(io.Discard), "u", "p", "tok")

	code, _ := serveCov(t, s, http.MethodPost, "/v1/monero/dkg/round1", `{}`, map[string]string{"Authorization": "Bearer wrong"})
	require.Equal(t, http.StatusUnauthorized, code)

	req := httptest.NewRequest(http.MethodPost, "/v1/monero/dkg/round1", strings.NewReader(`{}`))
	req.SetBasicAuth("u", "wrong")
	rec := httptest.NewRecorder()
	s.server.Handler.ServeHTTP(rec, req)
	require.Equal(t, http.StatusUnauthorized, rec.Code)

	// Bearer rejected but valid basic auth still passes.
	req = httptest.NewRequest(http.MethodPost, "/v1/monero/dkg/round1", strings.NewReader(`{}`))
	req.SetBasicAuth("u", "p")
	rec = httptest.NewRecorder()
	s.server.Handler.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
}

func TestServerHealthCov(t *testing.T) {
	t.Parallel()
	s := NewServer("127.0.0.1:0", noHealthEngineCov{&mockEngine{}}, zerolog.New(io.Discard))

	code, out := serveCov(t, s, http.MethodGet, "/health", "", nil)
	require.Equal(t, http.StatusOK, code)
	require.Equal(t, "ok", out["status"])

	code, out = serveCov(t, s, http.MethodPost, "/health", "", nil)
	require.Equal(t, http.StatusMethodNotAllowed, code)
	require.Equal(t, "method not allowed", out["error"])

	unhealthy := NewServer("127.0.0.1:0", &mockEngine{healthErr: errors.New("down")}, zerolog.New(io.Discard))
	code, out = serveCov(t, unhealthy, http.MethodGet, "/health", "", nil)
	require.Equal(t, http.StatusServiceUnavailable, code)
	require.Equal(t, "engine is unhealthy: down", out["error"])
}

func TestServerStartStopCov(t *testing.T) {
	t.Parallel()

	require.EqualError(t, NewServer("127.0.0.1:0", nil, zerolog.New(io.Discard)).Start(), "engine is nil")
	require.NoError(t, (&Server{}).Stop(context.Background()))

	// An authenticated server may bind a non-loopback address; use an
	// invalid port so ListenAndServe fails immediately.
	authed := NewServerWithAuth("0.0.0.0:-1", &mockEngine{}, zerolog.New(io.Discard), "", "", "tok")
	require.Error(t, authed.Start())

	s := NewServer("127.0.0.1:0", &mockEngine{}, zerolog.New(io.Discard))
	done := make(chan error, 1)
	go func() { done <- s.Start() }()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, s.Stop(ctx))
	select {
	case err := <-done:
		require.NoError(t, err, "Start returns nil after a graceful shutdown")
	case <-time.After(5 * time.Second):
		t.Fatal("server did not stop")
	}
}

func TestServerHelpersCov(t *testing.T) {
	t.Parallel()

	require.False(t, IsAlreadyRelayedError(nil))
	require.True(t, IsAlreadyRelayedError(errors.New("Key image ALREADY SPENT")))
	require.True(t, IsAlreadyRelayedError(errors.New("tx already relayed")))

	for addr, want := range map[string]bool{
		"127.0.0.1:8080": true,
		"localhost:1":    true,
		"[::1]:1":        true,
		":8080":          false,
		"":               false,
		"10.0.0.1:80":    false,
		"example.com:80": false,
		"127.0.0.1":      true,
	} {
		require.Equal(t, want, isLoopbackListenAddr(addr), addr)
	}
}
