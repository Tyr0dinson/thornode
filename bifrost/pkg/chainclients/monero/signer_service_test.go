package monero

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rs/zerolog"
)

type mockSignerEngine struct {
	healthErr error

	frostDKGRound1Resp   xmrFrostDKGRound1Response
	frostDKGRound1Err    error
	frostDKGRound2Resp   xmrFrostDKGRound2Response
	frostDKGRound2Err    error
	frostDKGRound3Resp   xmrFrostDKGRound3Response
	frostDKGRound3Err    error
	frostSignPrepareResp xmrFrostSignPrepareResponse
	frostSignPrepareErr  error
	frostSignPartialResp xmrFrostSignPartialResponse
	frostSignPartialErr  error
	frostSignCombineResp xmrFrostSignCombineResponse
	frostSignCombineErr  error
	frostBroadcastResp   xmrFrostBroadcastResponse
	frostBroadcastErr    error
}

func (m *mockSignerEngine) HealthCheck(_ context.Context) error {
	return m.healthErr
}

func (m *mockSignerEngine) FrostDKGRound1(_ xmrFrostDKGRound1Request) (xmrFrostDKGRound1Response, error) {
	return m.frostDKGRound1Resp, m.frostDKGRound1Err
}

func (m *mockSignerEngine) FrostDKGRound2(_ xmrFrostDKGRound2Request) (xmrFrostDKGRound2Response, error) {
	return m.frostDKGRound2Resp, m.frostDKGRound2Err
}

func (m *mockSignerEngine) FrostDKGRound3(_ xmrFrostDKGRound3Request) (xmrFrostDKGRound3Response, error) {
	return m.frostDKGRound3Resp, m.frostDKGRound3Err
}

func (m *mockSignerEngine) FrostSignPrepare(_ xmrFrostSignPrepareRequest) (xmrFrostSignPrepareResponse, error) {
	return m.frostSignPrepareResp, m.frostSignPrepareErr
}

func (m *mockSignerEngine) FrostSignPartial(_ xmrFrostSignPartialRequest) (xmrFrostSignPartialResponse, error) {
	return m.frostSignPartialResp, m.frostSignPartialErr
}

func (m *mockSignerEngine) FrostSignCombine(_ xmrFrostSignCombineRequest) (xmrFrostSignCombineResponse, error) {
	return m.frostSignCombineResp, m.frostSignCombineErr
}

func (m *mockSignerEngine) FrostBroadcast(_ xmrFrostBroadcastRequest) (xmrFrostBroadcastResponse, error) {
	return m.frostBroadcastResp, m.frostBroadcastErr
}

func TestHTTPSignerServiceFrostDKGRound1(t *testing.T) {
	t.Parallel()

	engine := &mockSignerEngine{
		frostDKGRound1Resp: xmrFrostDKGRound1Response{Commitments: "commit_hex"},
	}
	service := NewHTTPSignerServiceWithAuth("127.0.0.1:0", engine, zerolog.New(io.Discard), "", "", "")
	server := httptest.NewServer(service.server.Handler)
	defer server.Close()

	reqBody, err := json.Marshal(xmrFrostDKGRound1Request{
		VaultPubKey:      "thorpub1vault",
		Threshold:        2,
		N:                3,
		ParticipantIndex: 1,
		SessionID:        "dkg-session",
	})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}

	resp, err := http.Post(server.URL+"/v1/monero/dkg/round1", "application/json", bytes.NewReader(reqBody))
	if err != nil {
		t.Fatalf("post dkg round1: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200, got %d", resp.StatusCode)
	}
}

func TestHTTPSignerServiceFrostSignPrepare(t *testing.T) {
	t.Parallel()

	engine := &mockSignerEngine{
		frostSignPrepareResp: xmrFrostSignPrepareResponse{SessionID: "session-1", Preprocess: "preprocess_hex"},
	}
	service := NewHTTPSignerServiceWithAuth("127.0.0.1:0", engine, zerolog.New(io.Discard), "", "", "")
	server := httptest.NewServer(service.server.Handler)
	defer server.Close()

	reqBody, err := json.Marshal(xmrFrostSignPrepareRequest{
		VaultPubKey:  "thorpub1vault",
		ToAddress:    "48abc",
		AmountAtomic: 100,
		DecoyContext: "decoy-context",
	})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}

	resp, err := http.Post(server.URL+"/v1/monero/sign/prepare", "application/json", bytes.NewReader(reqBody))
	if err != nil {
		t.Fatalf("post sign prepare: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200, got %d", resp.StatusCode)
	}
}

func TestHTTPSignerServiceFrostBroadcast(t *testing.T) {
	t.Parallel()

	engine := &mockSignerEngine{
		frostBroadcastResp: xmrFrostBroadcastResponse{TxHash: "abc123"},
	}
	service := NewHTTPSignerServiceWithAuth("127.0.0.1:0", engine, zerolog.New(io.Discard), "", "", "")
	server := httptest.NewServer(service.server.Handler)
	defer server.Close()

	reqBody, err := json.Marshal(xmrFrostBroadcastRequest{TxHex: "deadbeef", MsgID: "broadcast-msg-id"})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}

	resp, err := http.Post(server.URL+"/v1/monero/broadcast", "application/json", bytes.NewReader(reqBody))
	if err != nil {
		t.Fatalf("post broadcast: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200, got %d", resp.StatusCode)
	}
}

func TestHTTPSignerServiceHealth(t *testing.T) {
	t.Parallel()

	service := NewHTTPSignerServiceWithAuth("127.0.0.1:0", &mockSignerEngine{}, zerolog.New(io.Discard), "", "", "")
	server := httptest.NewServer(service.server.Handler)
	defer server.Close()

	resp, err := http.Get(server.URL + "/health")
	if err != nil {
		t.Fatalf("get health: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200, got %d", resp.StatusCode)
	}
}

func TestHTTPSignerServiceHealthUnhealthyEngine(t *testing.T) {
	t.Parallel()

	service := NewHTTPSignerServiceWithAuth("127.0.0.1:0", &mockSignerEngine{healthErr: errors.New("signer unavailable")}, zerolog.New(io.Discard), "", "", "")
	server := httptest.NewServer(service.server.Handler)
	defer server.Close()

	resp, err := http.Get(server.URL + "/health")
	if err != nil {
		t.Fatalf("get health: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("expected status 503, got %d", resp.StatusCode)
	}
}

func TestHTTPSignerServiceHealthNilEngine(t *testing.T) {
	t.Parallel()

	service := NewHTTPSignerServiceWithAuth("127.0.0.1:0", nil, zerolog.New(io.Discard), "", "", "")
	server := httptest.NewServer(service.server.Handler)
	defer server.Close()

	resp, err := http.Get(server.URL + "/health")
	if err != nil {
		t.Fatalf("get health: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("expected status 503, got %d", resp.StatusCode)
	}
}

func TestIsAlreadyRelayedError(t *testing.T) {
	t.Parallel()

	if !isAlreadyRelayedError(errors.New("tx already in pool")) {
		t.Fatalf("expected already-relayed error to be recognized")
	}
	if isAlreadyRelayedError(errors.New("connection refused")) {
		t.Fatalf("expected non-relay error to be rejected")
	}
}

func TestHTTPSignerServiceRejectsUnauthorizedWhenAuthConfigured(t *testing.T) {
	t.Parallel()

	engine := &mockSignerEngine{
		frostDKGRound1Resp: xmrFrostDKGRound1Response{Commitments: "commit"},
	}
	service := NewHTTPSignerServiceWithAuth("127.0.0.1:0", engine, zerolog.New(io.Discard), "user", "pass", "token")
	server := httptest.NewServer(service.server.Handler)
	defer server.Close()

	reqBody, err := json.Marshal(xmrFrostDKGRound1Request{VaultPubKey: "thorpub1vault", SessionID: "s1", Threshold: 2, N: 3, ParticipantIndex: 1})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}

	resp, err := http.Post(server.URL+"/v1/monero/dkg/round1", "application/json", bytes.NewReader(reqBody))
	if err != nil {
		t.Fatalf("post request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected status 401, got %d", resp.StatusCode)
	}
}

func TestHTTPSignerServiceAllowsAuthorizedRawToken(t *testing.T) {
	t.Parallel()

	engine := &mockSignerEngine{
		frostDKGRound1Resp: xmrFrostDKGRound1Response{Commitments: "commit"},
	}
	service := NewHTTPSignerServiceWithAuth("127.0.0.1:0", engine, zerolog.New(io.Discard), "", "", "token")
	server := httptest.NewServer(service.server.Handler)
	defer server.Close()

	reqBody, err := json.Marshal(xmrFrostDKGRound1Request{VaultPubKey: "thorpub1vault", SessionID: "s1", Threshold: 2, N: 3, ParticipantIndex: 1})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}

	req, err := http.NewRequest(http.MethodPost, server.URL+"/v1/monero/dkg/round1", bytes.NewReader(reqBody))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "token")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200, got %d", resp.StatusCode)
	}
}

func TestHTTPSignerServiceStillAllowsBearerToken(t *testing.T) {
	t.Parallel()

	engine := &mockSignerEngine{
		frostDKGRound1Resp: xmrFrostDKGRound1Response{Commitments: "commit"},
	}
	service := NewHTTPSignerServiceWithAuth("127.0.0.1:0", engine, zerolog.New(io.Discard), "", "", "token")
	server := httptest.NewServer(service.server.Handler)
	defer server.Close()

	reqBody, err := json.Marshal(xmrFrostDKGRound1Request{VaultPubKey: "thorpub1vault", SessionID: "s1", Threshold: 2, N: 3, ParticipantIndex: 1})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}

	req, err := http.NewRequest(http.MethodPost, server.URL+"/v1/monero/dkg/round1", bytes.NewReader(reqBody))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer token")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200, got %d", resp.StatusCode)
	}
}

func TestHTTPSignerServiceStartRejectsUnauthenticatedNonLoopback(t *testing.T) {
	t.Parallel()

	service := NewHTTPSignerServiceWithAuth("0.0.0.0:0", &mockSignerEngine{}, zerolog.New(io.Discard), "", "", "")
	err := service.Start()
	if err == nil {
		t.Fatalf("expected start failure for unauthenticated non-loopback listen")
	}
}
