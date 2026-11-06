package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/btcsuite/btcd/btcjson"
	"github.com/rs/zerolog"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/utxo/rpc"
	"gitlab.com/thorchain/thornode/v3/common"
	. "gitlab.com/thorchain/thornode/v3/test/simulation/pkg/types"
)

func TestFiroReadinessRequiresAdvancingChainLocks(t *testing.T) {
	for _, stalled := range []bool{false, true} {
		now := time.Unix(100, 0)
		calls := 0
		err := waitForFiroReadiness(func() (int64, error) {
			calls++
			if calls == 1 {
				return 0, errors.New("quorum bootstrapping")
			}
			if calls < 4 || stalled {
				return 1000, nil
			}
			return 1001, nil
		}, func() time.Time { return now }, func(d time.Duration) { now = now.Add(d) }, 5*time.Second)
		if (err != nil) != stalled {
			t.Fatalf("stalled=%t: %v", stalled, err)
		}
		if !stalled && calls != 4 {
			t.Fatalf("returned before ChainLocks advanced: %d reads", calls)
		}
	}
}

func TestFiroReadyHeightRequiresNativeFinality(t *testing.T) {
	for _, scenario := range []string{"tip", "parent", "unlocked", "unconfirmed", "wrong hash", "wrong network", "pruned", "syncing", "before activation"} {
		t.Run(scenario, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request struct {
					ID     any               `json:"id"`
					Method string            `json:"method"`
					Params []json.RawMessage `json:"params"`
				}
				err := json.NewDecoder(r.Body).Decode(&request)
				if err != nil {
					t.Error(err)
					return
				}
				var result any
				switch request.Method {
				case "getblockchaininfo":
					info := rpc.FiroChainInfo{Chain: "regtest", Blocks: 1001, Headers: 1001}
					switch scenario {
					case "wrong network":
						info.Chain = "main"
					case "pruned":
						info.Pruned = true
					case "syncing":
						info.Headers = 1010
					case "before activation":
						info.Blocks, info.Headers = 999, 999
					}
					result = info
				case "getblockhash":
					var height int64
					_ = json.Unmarshal(request.Params[0], &height)
					result = fmt.Sprintf("%064x", height)
				case "getblockheader":
					var hash string
					_ = json.Unmarshal(request.Params[0], &hash)
					height := int64(1001)
					if hash == fmt.Sprintf("%064x", 1000) {
						height = 1000
					}
					header := rpc.FiroHeader{Hash: hash, Height: height, Confirmations: 1, ChainLock: true}
					if scenario == "unlocked" || (scenario == "parent" && height == 1001) {
						header.ChainLock = false
					}
					if scenario == "unconfirmed" {
						header.Confirmations = 0
					}
					if scenario == "wrong hash" {
						header.Hash = "different"
					}
					result = header
				default:
					t.Errorf("unexpected RPC %s", request.Method)
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result})
			}))
			defer server.Close()
			client, err := rpc.NewClient(server.URL, "thorchain", "password", 0, time.Second, common.FIROChain, zerolog.Nop())
			if err != nil {
				t.Fatal(err)
			}
			height, err := firoReadyHeight(client)
			wantOK := scenario == "tip" || scenario == "parent"
			if (err == nil) != wantOK {
				t.Fatalf("height=%d error=%v", height, err)
			}
			if wantOK && (height < 1000 || height > 1001) {
				t.Fatalf("unexpected locked height %d", height)
			}
		})
	}
}

type firoFundingTestClient struct {
	fundingRetryClient
	read func(string) (*rpc.TxResult, error)
}

func (c *firoFundingTestClient) GetRawTransactionVerbose(id string) (*rpc.TxResult, error) {
	return c.read(id)
}

func TestFiroFundingWaitsWithoutBroadcastingTwice(t *testing.T) {
	for _, stalled := range []bool{false, true} {
		signs, broadcasts, reads, sleeps := 0, 0, 0, 0
		client := &firoFundingTestClient{
			fundingRetryClient: fundingRetryClient{
				sign:      func(SimTx) ([]byte, error) { signs++; return []byte("signed"), nil },
				broadcast: func([]byte) (string, error) { broadcasts++; return "funding", nil },
			},
			read: func(id string) (*rpc.TxResult, error) {
				reads++
				tx := &rpc.TxResult{InstantLock: true, TxRawResult: btcjson.TxRawResult{Txid: id}}
				if reads > 1 {
					tx.BlockHash, tx.Confirmations = "block", 1
				}
				if reads > 2 && !stalled {
					tx.ChainLock = true
				}
				return tx, nil
			},
		}
		id, err := signAndBroadcastFundingTx(client, SimTx{Chain: common.FIROChain}, func(time.Duration) { sleeps++ })
		if id != "funding" || signs != 1 || broadcasts != 1 {
			t.Fatalf("funding resubmitted: id=%s signs=%d broadcasts=%d", id, signs, broadcasts)
		}
		if stalled {
			if !errors.Is(err, ErrSimTxUncertain) || reads != 60 {
				t.Fatalf("stalled transfer lost its uncertainty: %v", err)
			}
		} else if err != nil || reads != 3 || sleeps != 2 {
			t.Fatalf("funding did not wait for confirmed ChainLock: reads=%d sleeps=%d error=%v", reads, sleeps, err)
		}
	}
}
