package rpc

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/btcsuite/btcd/btcjson"
	"github.com/rs/zerolog"
	"gitlab.com/thorchain/thornode/v3/common"
)

func TestFiroBlockSkipsSpecialTransactionsBeforeDecoding(t *testing.T) {
	missingType := false
	server := newMockRPCServer(func(method string, params []interface{}) (interface{}, *btcjson.RPCError) {
		if method != "getblock" || len(params) != 2 || params[1] != float64(2) {
			t.Errorf("unexpected request: %s %v", method, params)
		}
		if missingType {
			return json.RawMessage(`{"tx":[{"txid":"missing-type"}]}`), nil
		}
		return json.RawMessage(`{"hash":"block", "height":10, "confirmations":1, "chainlock":true, "tx":[
			{"type":5,"txid":"special","vin":"not a Bitcoin input array","extraPayload":"abcd"},
			{"type":0,"txid":"ordinary","size":200,"vin":[{"txid":"previous","vout":7}],"vout":[{"n":0,"value":1,"scriptPubKey":{"hex":"76"}}]}
		]}`), nil
	})
	defer server.Close()
	client, err := NewClient(server.URL, "", "", 0, time.Second, common.FIROChain, zerolog.Nop())
	if err != nil {
		t.Fatal(err)
	}
	block, err := client.GetBlockVerboseTxsFIRO("block")
	if err != nil {
		t.Fatal(err)
	}
	if !block.ChainLock || block.Height != 10 || len(block.Tx) != 1 || block.Tx[0].Txid != "ordinary" || block.Tx[0].Vin[0].Vout != 7 {
		t.Fatalf("incorrect normalized block: %+v", block)
	}
	missingType = true
	_, err = client.GetBlockVerboseTxsFIRO("block")
	if err == nil {
		t.Fatal("missing type silently treated as ordinary")
	}
}

func TestFiroTransactionPreservesNativeFields(t *testing.T) {
	var tx FiroTxResult
	err := json.Unmarshal([]byte(`{"txid":"id","type":0,"height":42,"blockhash":"block","confirmations":2,"chainlock":true,"instantlock":false,"vin":[{"txid":"parent","vout":3,"valueSat":500}],"vout":[{"n":0,"value":1,"scriptPubKey":{"type":"exchangeaddress","hex":"e0"}}]}`), &tx)
	if err != nil {
		t.Fatal(err)
	}
	if tx.Txid != "id" || tx.Height != 42 || tx.BlockHash != "block" || !tx.ChainLock || tx.InstantLock || tx.Vin[0].Vout != 3 {
		t.Fatalf("lost FIRO fields: %+v", tx)
	}
}

func TestFiroImportRequiresCompletedRescan(t *testing.T) {
	label, scans, fail := "", 0, true
	server := newMockRPCServer(func(method string, params []interface{}) (interface{}, *btcjson.RPCError) {
		switch method {
		case "validateaddress":
			return map[string]any{"iswatchonly": true, "account": label}, nil
		case "importaddress":
			if len(params) != 3 {
				t.Errorf("unexpected import arguments: %v", params)
				return nil, btcjson.ErrRPCInvalidParams
			}
			if params[2] == true {
				scans++
				if fail {
					return nil, &btcjson.RPCError{Code: -1, Message: "interrupted rescan"}
				}
			} else {
				value, ok := params[1].(string)
				if !ok {
					t.Errorf("unexpected import label: %v", params[1])
					return nil, btcjson.ErrRPCInvalidParams
				}
				label = value
			}
			return nil, nil
		default:
			t.Errorf("unexpected RPC %s", method)
			return nil, btcjson.ErrRPCMethodNotFound
		}
	})
	defer server.Close()
	client, err := NewClient(server.URL, "", "", 0, time.Second, common.FIROChain, zerolog.Nop())
	if err != nil {
		t.Fatal(err)
	}
	err = client.EnsureWatchAddressFIRO("vault")
	if err == nil {
		t.Fatal("accepted interrupted rescan")
	}
	if label != "" {
		t.Fatal("marked failed scan complete")
	}
	fail = false
	err = client.EnsureWatchAddressFIRO("vault")
	if err != nil {
		t.Fatal(err)
	}
	if scans != 2 || label != firoRescanLabel {
		t.Fatal("did not retry partially imported address")
	}
	err = client.EnsureWatchAddressFIRO("vault")
	if err != nil {
		t.Fatal(err)
	}
	if scans != 2 {
		t.Fatal("repeated a completed wallet rescan")
	}
}

func TestFiroSpecialPreviousOutputDoesNotDecodePrivacyInputs(t *testing.T) {
	server := newMockRPCServer(func(method string, params []interface{}) (interface{}, *btcjson.RPCError) {
		if method != "getrawtransaction" {
			t.Errorf("unexpected RPC %s", method)
		}
		return json.RawMessage(`{"txid":"special","type":9,"vin":["sparkSpend"],"height":42,"confirmations":2,"chainlock":true,"vout":[{"n":0,"value":1.25,"scriptPubKey":{"hex":"76a914000000000000000000000000000000000000000088ac"}}]}`), nil
	})
	defer server.Close()
	client, err := NewClient(server.URL, "", "", 0, time.Second, common.FIROChain, zerolog.Nop())
	if err != nil {
		t.Fatal(err)
	}
	tx, err := client.GetRawTransactionVerboseFIRO("special")
	if err != nil {
		t.Fatal(err)
	}
	if tx.Type != 9 || tx.Height != 42 || len(tx.Vin) != 0 || len(tx.Vout) != 1 || tx.Vout[0].Value != 1.25 || tx.Vout[0].N != 0 {
		t.Fatalf("lost transparent previous output: %+v", tx)
	}
}

func TestFiroInstantSendNativeStatus(t *testing.T) {
	for _, status := range []string{`true`, `false`, `null`, `"true"`, `1`, `missing`} {
		t.Run(status, func(t *testing.T) {
			field := `,"instantlock":` + status
			if status == "missing" {
				field = ""
			}
			server := newMockRPCServer(func(method string, params []interface{}) (interface{}, *btcjson.RPCError) {
				switch method {
				case "getrawmempool":
					if len(params) != 1 || params[0] != true {
						t.Errorf("expected verbose mempool request: %v", params)
					}
					return json.RawMessage(`{"id":{"time":123` + field + `}}`), nil
				case "getrawtransaction":
					return json.RawMessage(`{"txid":"id","type":0,"vin":[]` + field + `}`), nil
				default:
					t.Errorf("unexpected RPC: %s", method)
					return nil, btcjson.ErrRPCMethodNotFound
				}
			})
			defer server.Close()
			client, err := NewClient(server.URL, "", "", 0, time.Second, common.FIROChain, zerolog.Nop())
			if err != nil {
				t.Fatal(err)
			}
			entries, mempoolErr := client.GetRawMempoolVerboseFIRO()
			tx, txErr := client.GetRawTransactionVerboseFIRO("id")
			if status == `"true"` || status == `1` {
				if mempoolErr == nil || txErr == nil {
					t.Fatal("accepted non-boolean FIRO lock status")
				}
				return
			}
			if mempoolErr != nil || txErr != nil {
				t.Fatalf("%v / %v", mempoolErr, txErr)
			}
			known := status == "true" || status == "false"
			if (entries["id"].InstantLock != nil) != known || tx.InstantLockKnown != known || tx.InstantLock != (status == "true") {
				t.Fatalf("incorrect lock normalization: %+v / %+v", entries, tx)
			}
		})
	}
}

func TestFiroInstantSendRejectsNullMempool(t *testing.T) {
	server := newMockRPCServer(func(string, []interface{}) (interface{}, *btcjson.RPCError) { return nil, nil })
	defer server.Close()
	client, err := NewClient(server.URL, "", "", 0, time.Second, common.FIROChain, zerolog.Nop())
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.GetRawMempoolVerboseFIRO()
	if err == nil {
		t.Fatal("accepted unknown mempool as empty")
	}
}
