package types

import (
	"errors"
	"testing"

	"gitlab.com/thorchain/thornode/v3/common"
)

type retryLiteChainClient struct {
	signCalls      int
	broadcastCalls int
	broadcastErrs  []error
}

func (r *retryLiteChainClient) GetAccount(_ *common.PubKey) (*common.Account, error) {
	return &common.Account{}, nil
}

func (r *retryLiteChainClient) SignTx(SimTx) ([]byte, error) {
	r.signCalls++
	return []byte("signed"), nil
}

func (r *retryLiteChainClient) BroadcastTx([]byte) (string, error) {
	r.broadcastCalls++
	idx := r.broadcastCalls - 1
	if idx < len(r.broadcastErrs) && r.broadcastErrs[idx] != nil {
		return "", r.broadcastErrs[idx]
	}

	return "ok", nil
}

func TestSignAndBroadcastTxRetriesTronConfirmationFailures(t *testing.T) {
	prevDelay := tronSimTxBroadcastRetryDelay
	prevAttempts := tronSimTxMaxBroadcastAttempts
	tronSimTxBroadcastRetryDelay = 0
	tronSimTxMaxBroadcastAttempts = 4
	defer func() {
		tronSimTxBroadcastRetryDelay = prevDelay
		tronSimTxMaxBroadcastAttempts = prevAttempts
	}()

	client := &retryLiteChainClient{
		broadcastErrs: []error{
			errors.New("broadcasted tx a not confirmed on tron node"),
			errors.New("broadcasted tx b not observed on tron node"),
			nil,
		},
	}

	txid, err := SignAndBroadcastTx(client, SimTx{Chain: common.TRONChain})
	if err != nil {
		t.Fatalf("expected tron retry helper to recover, got %v", err)
	}
	if txid != "ok" {
		t.Fatalf("expected successful txid, got %s", txid)
	}
	if client.signCalls != 3 {
		t.Fatalf("expected 3 sign attempts, got %d", client.signCalls)
	}
	if client.broadcastCalls != 3 {
		t.Fatalf("expected 3 broadcast attempts, got %d", client.broadcastCalls)
	}
}

func TestSignAndBroadcastTxDoesNotRetryNonTronChains(t *testing.T) {
	prevDelay := tronSimTxBroadcastRetryDelay
	prevAttempts := tronSimTxMaxBroadcastAttempts
	tronSimTxBroadcastRetryDelay = 0
	tronSimTxMaxBroadcastAttempts = 4
	defer func() {
		tronSimTxBroadcastRetryDelay = prevDelay
		tronSimTxMaxBroadcastAttempts = prevAttempts
	}()

	client := &retryLiteChainClient{
		broadcastErrs: []error{
			errors.New("broadcasted tx a not confirmed on tron node"),
		},
	}

	_, err := SignAndBroadcastTx(client, SimTx{Chain: common.BTCChain})
	if err == nil {
		t.Fatal("expected non-tron tx to fail without retry")
	}
	if client.signCalls != 1 {
		t.Fatalf("expected 1 sign attempt, got %d", client.signCalls)
	}
	if client.broadcastCalls != 1 {
		t.Fatalf("expected 1 broadcast attempt, got %d", client.broadcastCalls)
	}
}

func TestShouldRetryTronSimTxBroadcast(t *testing.T) {
	if !shouldRetryTronSimTxBroadcast(errors.New("broadcasted tx a not confirmed on tron node")) {
		t.Fatal("expected confirmation failure to be retriable")
	}
	if !shouldRetryTronSimTxBroadcast(errors.New("broadcasted tx a not observed on tron node")) {
		t.Fatal("expected observation failure to be retriable")
	}
	if shouldRetryTronSimTxBroadcast(errors.New("other error")) {
		t.Fatal("expected unrelated errors to be non-retriable")
	}
	if shouldRetryTronSimTxBroadcast(nil) {
		t.Fatal("expected nil error to be non-retriable")
	}
}
