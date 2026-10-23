package evm

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/ethclient"
)

// CheckTransaction decides reorg errata, so only the node's positive "not found"
// may say a transaction is gone; a lookup that fails must be an error.
func TestCheckTransactionNeverTurnsALookupFailureIntoAbsence(t *testing.T) {
	answer := ""
	status := http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(answer))
	}))
	defer srv.Close()
	client, err := ethclient.Dial(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	rpc, err := NewEthRPC(client, time.Second, "AVAX")
	if err != nil {
		t.Fatal(err)
	}
	hash := "0x88df016429689c079f3b2f6ad39fa052532c56795b733da78a91ebe6a713944b"

	// the node answers "not found": gone, no error
	answer = `{"jsonrpc":"2.0","id":1,"result":null}`
	presence, err := rpc.CheckTransaction("", hash)
	if err != nil || presence != TxNotFound {
		t.Fatalf("not found: got presence=%v err=%v, want TxNotFound, nil", presence, err)
	}

	// the lookup fails: an error, never "not on chain"
	status, answer = http.StatusBadGateway, `bad gateway`
	presence, err = rpc.CheckTransaction("", hash)
	if err == nil {
		t.Fatalf("failed lookup: got presence=%v err=nil, want an error", presence)
	}
	status, answer = http.StatusOK, `{"jsonrpc":"2.0","id":1,"error":{"code":-32603,"message":"internal error"}}`
	if _, err = rpc.CheckTransaction("", hash); err == nil {
		t.Fatal("rpc error: want an error")
	}
}
