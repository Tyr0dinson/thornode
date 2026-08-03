package utxo

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/rs/zerolog"

	utxorpc "gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/utxo/rpc"
	"gitlab.com/thorchain/thornode/v3/common"
	ttypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

func TestRegisterPublicKeyCreatesXECWalletBeforeImport(t *testing.T) {
	ttypes.SetupConfigForTest()

	var mu sync.Mutex
	methods := make([]string, 0, 2)
	server := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		body, err := io.ReadAll(req.Body)
		if err != nil {
			t.Errorf("read request: %v", err)
			return
		}
		var call struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params []any           `json:"params"`
		}
		if err := json.Unmarshal(body, &call); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}

		mu.Lock()
		methods = append(methods, call.Method)
		mu.Unlock()

		if call.Method == "createwallet" {
			if len(call.Params) == 0 || call.Params[0] != "" {
				t.Errorf("createwallet name = %v, want empty name", call.Params)
			}
		}

		rw.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(rw).Encode(map[string]any{
			"jsonrpc": "2.0",
			"id":      call.ID,
			"result":  nil,
			"error":   nil,
		})
	}))
	defer server.Close()

	rpcClient, err := utxorpc.NewClient(
		server.URL, "", "", 0, 5*time.Second, common.XECChain, zerolog.Nop(),
	)
	if err != nil {
		t.Fatalf("create RPC client: %v", err)
	}
	client := &Client{rpc: rpcClient, log: zerolog.Nop()}
	client.cfg.ChainID = common.XECChain

	pubkey := common.PubKey("tthorpub1addwnpepqflvfv08t6qt95lmttd6wpf3ss8wx63e9vf6fvyuj2yy6nnyna576rfzjks")
	if common.CurrentChainNetwork == common.MainNet {
		pubkey = common.PubKey("thorpub1addwnpepqwprh5vd0rrk78kd98qjruuazwvapnxft7f86w7hlf768whxytpn5quf2gs")
	}
	if err := client.RegisterPublicKey(pubkey); err != nil {
		t.Fatalf("register XEC pubkey: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(methods) != 2 || methods[0] != "createwallet" || methods[1] != "importaddress" {
		t.Fatalf("RPC methods = %v, want [createwallet importaddress]", methods)
	}
}
