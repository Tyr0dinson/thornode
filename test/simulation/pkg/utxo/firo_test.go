package utxo

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/btcsuite/btcd/btcjson"
	"github.com/rs/zerolog"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/utxo/rpc"
	"gitlab.com/thorchain/thornode/v3/common"
)

func TestFIROAccountBalanceRequiresConfirmedInputs(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			ID     any    `json:"id"`
			Method string `json:"method"`
		}
		err := json.NewDecoder(r.Body).Decode(&request)
		if err != nil {
			t.Error(err)
			return
		}
		if request.Method != "listunspent" {
			t.Errorf("unexpected RPC %s", request.Method)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": request.ID, "result": []btcjson.ListUnspentResult{
			{Amount: 2, Confirmations: 0},
			{Amount: 3, Confirmations: 1},
		}})
	}))
	defer server.Close()
	for _, tc := range []struct {
		chain  common.Chain
		amount uint64
	}{{common.FIROChain, 3}, {common.DASHChain, 5}} {
		rpcClient, err := rpc.NewClient(server.URL, "thorchain", "password", 0, time.Second, tc.chain, zerolog.Nop())
		if err != nil {
			t.Fatal(err)
		}
		client := &Client{chain: tc.chain, rpc: rpcClient}
		account, err := client.GetAccount(nil)
		if err != nil {
			t.Fatal(err)
		}
		if got := account.Coins.GetCoin(tc.chain.GetGasAsset()).Amount.Uint64(); got != tc.amount*common.One {
			t.Fatalf("%s balance = %d, want %d", tc.chain, got, tc.amount*common.One)
		}
	}
}
