package monero

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	tssp "gitlab.com/thorchain/thornode/v3/bifrost/tss/go-tss/tss"
	"gitlab.com/thorchain/thornode/v3/config"
)

func TestFrostSignerAdapterTranslation(t *testing.T) {
	t.Parallel()

	var gotPrepare xmrFrostSignPrepareRequest
	var gotPartial xmrFrostSignPartialRequest
	var gotCombine xmrFrostSignCombineRequest

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/monero/sign/prepare":
			if err := json.NewDecoder(r.Body).Decode(&gotPrepare); err != nil {
				t.Fatalf("decode prepare request: %v", err)
			}
			_ = json.NewEncoder(w).Encode(xmrFrostSignPrepareResponse{
				SessionID:  "session-1",
				Preprocess: "preprocess-hex",
			})
		case "/v1/monero/sign/partial":
			if err := json.NewDecoder(r.Body).Decode(&gotPartial); err != nil {
				t.Fatalf("decode partial request: %v", err)
			}
			_ = json.NewEncoder(w).Encode(xmrFrostSignPartialResponse{
				SignatureShare: "share-hex",
			})
		case "/v1/monero/sign/combine":
			if err := json.NewDecoder(r.Body).Decode(&gotCombine); err != nil {
				t.Fatalf("decode combine request: %v", err)
			}
			_ = json.NewEncoder(w).Encode(xmrFrostSignCombineResponse{
				TxHash:    "tx-hash",
				TxHex:     "tx-hex",
				FeeAtomic: 42,
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	signer, err := NewFrostSignerWithEndpoint(
		config.BifrostChainConfiguration{},
		server.URL,
	)
	if err != nil {
		t.Fatalf("new frost signer: %v", err)
	}
	adapter := newFrostSignerAdapter(signer)

	prepareReq := tssp.FrostSidecarPrepareReq{
		VaultPubKey:  "vault-pk",
		ToAddress:    "48abc",
		AmountAtomic: 1000,
		Memo:         "OUT:1",
		FeeRate:      7,
		DecoyContext: "ctx-123",
	}
	prepareResp, err := adapter.FrostSignPrepare(prepareReq)
	if err != nil {
		t.Fatalf("adapter prepare: %v", err)
	}
	if prepareResp.SessionID != "session-1" || prepareResp.Preprocess != "preprocess-hex" {
		t.Fatalf("unexpected prepare response: %+v", prepareResp)
	}
	if gotPrepare.VaultPubKey != prepareReq.VaultPubKey ||
		gotPrepare.ToAddress != prepareReq.ToAddress ||
		gotPrepare.AmountAtomic != prepareReq.AmountAtomic ||
		gotPrepare.Memo != prepareReq.Memo ||
		gotPrepare.FeeRate != prepareReq.FeeRate ||
		gotPrepare.DecoyContext != prepareReq.DecoyContext {
		t.Fatalf("prepare request translation mismatch: %+v", gotPrepare)
	}

	partialReq := tssp.FrostSidecarPartialReq{
		SessionID: "session-1",
		AllPreprocesses: map[string]string{
			"1": "aa",
			"2": "bb",
		},
	}
	partialResp, err := adapter.FrostSignPartial(partialReq)
	if err != nil {
		t.Fatalf("adapter partial: %v", err)
	}
	if partialResp.SignatureShare != "share-hex" {
		t.Fatalf("unexpected partial response: %+v", partialResp)
	}
	if gotPartial.SessionID != partialReq.SessionID || !reflect.DeepEqual(gotPartial.AllPreprocesses, partialReq.AllPreprocesses) {
		t.Fatalf("partial request translation mismatch: %+v", gotPartial)
	}

	combineReq := tssp.FrostSidecarCombineReq{
		SessionID: "session-1",
		AllShares: map[string]string{
			"1": "cc",
			"2": "dd",
		},
	}
	combineResp, err := adapter.FrostSignCombine(combineReq)
	if err != nil {
		t.Fatalf("adapter combine: %v", err)
	}
	if combineResp.TxHash != "tx-hash" || combineResp.TxHex != "tx-hex" || combineResp.FeeAtomic != 42 {
		t.Fatalf("unexpected combine response: %+v", combineResp)
	}
	if gotCombine.SessionID != combineReq.SessionID || !reflect.DeepEqual(gotCombine.AllShares, combineReq.AllShares) {
		t.Fatalf("combine request translation mismatch: %+v", gotCombine)
	}
}

func TestFrostSignerAdapterDKGFinalizePassesKeyBytesHex(t *testing.T) {
	t.Parallel()

	var gotFinalize xmrFrostDKGFinalizeRequest

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/monero/dkg/finalize":
			if err := json.NewDecoder(r.Body).Decode(&gotFinalize); err != nil {
				t.Fatalf("decode finalize request: %v", err)
			}
			_ = json.NewEncoder(w).Encode(xmrFrostDKGFinalizeResponse{
				Success:     true,
				KeyBytesHex: "deadbeef0123",
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	signer, err := NewFrostSignerWithEndpoint(
		config.BifrostChainConfiguration{},
		server.URL,
	)
	if err != nil {
		t.Fatalf("new frost signer: %v", err)
	}
	adapter := newFrostSignerAdapter(signer)

	finalizeReq := tssp.FrostSidecarDKGFinalizeReq{
		SessionID: "sess-1",
		GroupKey:  "aabbccdd",
		Address:   "4Addr",
	}
	finalizeResp, err := adapter.FrostDKGFinalize(finalizeReq)
	if err != nil {
		t.Fatalf("adapter finalize: %v", err)
	}
	if !finalizeResp.Success {
		t.Fatalf("expected success=true")
	}
	if finalizeResp.KeyBytesHex != "deadbeef0123" {
		t.Fatalf("expected key_bytes_hex=deadbeef0123, got %s", finalizeResp.KeyBytesHex)
	}
	if gotFinalize.SessionID != "sess-1" || gotFinalize.GroupKey != "aabbccdd" || gotFinalize.Address != "4Addr" {
		t.Fatalf("finalize request translation mismatch: %+v", gotFinalize)
	}
}
