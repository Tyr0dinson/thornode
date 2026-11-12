package rpc

import (
	"encoding/json"
	"fmt"
	"reflect"
	"testing"

	"github.com/btcsuite/btcd/btcjson"
)

func TestSharedRPCResultDecoding(t *testing.T) {
	// The extended response must preserve every shared field while also
	// decoding Dash finality from the same JSON object.
	const txJSON = `{
		"hex":"0102", "txid":"tx", "hash":"hash", "size":225, "vsize":225,
		"weight":900, "version":2, "locktime":123,
		"vin":[
			{"txid":"parent", "vout":2, "scriptSig":{"asm":"sig", "hex":"0304"}, "sequence":1234},
			{"coinbase":"0506", "sequence":4294967295}
		],
		"vout":[{"value":1.23456789, "n":0, "scriptPubKey":{
			"asm":"OP_DUP", "hex":"76", "reqSigs":1, "type":"pubkeyhash", "addresses":["dash-address"]
		}}],
		"blockhash":"block", "confirmations":3, "time":100, "blocktime":99,
		"instantlock":true, "instantlock_internal":true, "chainlock":true
	}`
	var tx TxResult
	var wantTx btcjson.TxRawResult
	err := json.Unmarshal([]byte(txJSON), &tx)
	if err != nil {
		t.Fatal(err)
	}
	err = json.Unmarshal([]byte(txJSON), &wantTx)
	if err != nil {
		t.Fatal(err)
	}
	if !tx.InstantLock || !tx.InstantLockInternal || !tx.ChainLock {
		t.Fatal("shared response lost Dash finality")
	}
	if got := tx.TxRawResult; !reflect.DeepEqual(got, wantTx) {
		t.Fatalf("transaction fields: got %+v, want %+v", got, wantTx)
	}
	blockJSON := fmt.Sprintf(`{
		"hash":"block", "confirmations":3, "strippedsize":500, "size":501, "weight":2001,
		"height":123, "version":4, "versionHex":"00000004", "merkleroot":"root",
		"tx":[%s], "rawtx":[%s], "time":100, "nonce":42, "bits":"bits",
		"difficulty":1.5, "previousblockhash":"previous", "nextblockhash":"next", "chainlock":true
	}`, txJSON, txJSON)
	var block BlockResult
	var wantBlock btcjson.GetBlockVerboseTxResult
	err = json.Unmarshal([]byte(blockJSON), &block)
	if err != nil {
		t.Fatal(err)
	}
	err = json.Unmarshal([]byte(blockJSON), &wantBlock)
	if err != nil {
		t.Fatal(err)
	}
	if !block.ChainLock {
		t.Fatal("shared response lost block finality")
	}
	if got := block.GetBlockVerboseTxResult; !reflect.DeepEqual(got, wantBlock) {
		t.Fatalf("block fields: got %+v, want %+v", got, wantBlock)
	}
}

func TestQuorumLockFields(t *testing.T) {
	for _, value := range []string{"true", "false", "unknown"} {
		var entry GetRawMempoolVerboseResult
		// The daemon returns additional fields, including local receipt metadata.
		// Only the lock status is needed by the client.
		data := fmt.Sprintf(`{"instantlock":%q,"size":225,"vsize":225,"weight":900,
			"fee":0.00001,"fees":{"base":0.00001,"modified":0.00001},
			"time":1700000000,"height":123,"depends":["parent"]}`, value)
		err := json.Unmarshal([]byte(data), &entry)
		if err != nil {
			t.Fatal(err)
		}
		if entry.InstantLock != value {
			t.Fatalf("mempool lock: got %q, want %q", entry.InstantLock, value)
		}
	}
	var missing GetRawMempoolVerboseResult
	err := json.Unmarshal([]byte(`{}`), &missing)
	if err != nil {
		t.Fatal(err)
	}
	if missing.InstantLock != "" {
		t.Fatal("missing lock status must remain unknown")
	}
	err = json.Unmarshal([]byte(`{"instantlock":true}`), &missing)
	if err == nil {
		t.Fatal("boolean mempool lock status must be rejected")
	}
	for _, tc := range []struct {
		json    string
		instant bool
	}{
		{`{}`, false},
		{`{"instantlock":true,"instantlock_internal":true}`, true},
		{`{"instantlock":true,"chainlock":true}`, false},
		{`{"chainlock":true}`, false},
	} {
		var tx TxResult
		err = json.Unmarshal([]byte(tc.json), &tx)
		if err != nil {
			t.Fatal(err)
		}
		if tx.InstantSendLocked() != tc.instant {
			t.Fatalf("wrong finality for %s", tc.json)
		}
	}
}
