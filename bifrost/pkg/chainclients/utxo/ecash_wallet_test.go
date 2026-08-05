package utxo

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rs/zerolog"

	utxorpc "gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/utxo/rpc"
	"gitlab.com/thorchain/thornode/v3/common"
	ttypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

func TestXECWalletRPCsUseBareAddress(t *testing.T) {
	ttypes.SetupConfigForTest()

	var mu sync.Mutex
	methods := make([]string, 0, 3)
	var importedAddress string
	var listedAddress string
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
		if call.Method == "importaddress" {
			if len(call.Params) == 0 {
				t.Error("importaddress missing address")
			} else if address, ok := call.Params[0].(string); !ok {
				t.Errorf("importaddress address type = %T, want string", call.Params[0])
			} else {
				mu.Lock()
				importedAddress = address
				mu.Unlock()
			}
		}
		if call.Method == "listunspent" {
			if len(call.Params) < 3 {
				t.Errorf("listunspent params = %v, want address filter", call.Params)
			} else if addresses, ok := call.Params[2].([]any); !ok || len(addresses) != 1 {
				t.Errorf("listunspent address filter = %v, want one address", call.Params[2])
			} else if address, ok := addresses[0].(string); !ok {
				t.Errorf("listunspent address type = %T, want string", addresses[0])
			} else {
				mu.Lock()
				listedAddress = address
				mu.Unlock()
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

	rpcClient, rpcErr := utxorpc.NewClient(
		server.URL, "", "", 0, 5*time.Second, common.XECChain, zerolog.Nop(),
	)
	if rpcErr != nil {
		t.Fatalf("create RPC client: %v", rpcErr)
	}
	client := &Client{rpc: rpcClient, log: zerolog.Nop()}
	client.cfg.ChainID = common.XECChain

	pubkey := common.PubKey("tthorpub1addwnpepqflvfv08t6qt95lmttd6wpf3ss8wx63e9vf6fvyuj2yy6nnyna576rfzjks")
	if common.CurrentChainNetwork == common.MainNet {
		pubkey = common.PubKey("thorpub1addwnpepqwprh5vd0rrk78kd98qjruuazwvapnxft7f86w7hlf768whxytpn5quf2gs")
	}
	if registerErr := client.RegisterPublicKey(pubkey); registerErr != nil {
		t.Fatalf("register XEC pubkey: %v", registerErr)
	}
	if _, accountErr := client.GetAccount(pubkey, nil); accountErr != nil {
		t.Fatalf("get XEC account: %v", accountErr)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(methods) != 3 || methods[0] != "createwallet" || methods[1] != "importaddress" || methods[2] != "listunspent" {
		t.Fatalf("RPC methods = %v, want [createwallet importaddress listunspent]", methods)
	}
	expectedAddress, addressErr := pubkey.GetAddress(common.XECChain)
	if addressErr != nil {
		t.Fatalf("derive expected XEC address: %v", addressErr)
	}
	if importedAddress != expectedAddress.String() {
		t.Fatalf("imported address = %q, want %q", importedAddress, expectedAddress)
	}
	if strings.Contains(importedAddress, ":") {
		t.Fatalf("imported XEC address must be prefixless, got %q", importedAddress)
	}
	if listedAddress != expectedAddress.String() {
		t.Fatalf("listed address = %q, want %q", listedAddress, expectedAddress)
	}
}
