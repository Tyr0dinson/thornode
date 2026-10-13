package txscript

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/ltcsuite/ltcd/chaincfg"
	"github.com/ltcsuite/ltcd/chaincfg/chainhash"
	"github.com/ltcsuite/ltcd/wire"
)

// ltcRegtestRPCEnv names a Litecoin Core regtest JSON-RPC endpoint with a
// funded wallet, e.g. http://user:pass@127.0.0.1:19443/wallet/<name>. When it
// is unset the end-to-end test is skipped.
//
// To run it locally:
//
//	docker run -d --name ltc-regtest -p 127.0.0.1:19443:19443 --entrypoint litecoind \
//	  registry.gitlab.com/thorchain/devops/node-launcher:litecoin-daemon-0.21.4 \
//	  -regtest -rpcport=19443 -rpcuser=u -rpcpassword=p -rpcbind=0.0.0.0 \
//	  -rpcallowip=0.0.0.0/0 -vbparams=mweb:9999999999:9999999999 -fallbackfee=0.0001
//	docker exec ltc-regtest litecoin-cli -regtest -rpcport=19443 -rpcuser=u -rpcpassword=p createwallet w
//	docker exec ltc-regtest litecoin-cli -regtest -rpcport=19443 -rpcuser=u -rpcpassword=p \
//	  generatetoaddress 101 $(docker exec ltc-regtest litecoin-cli -regtest -rpcport=19443 -rpcuser=u -rpcpassword=p getnewaddress)
//	LTC_REGTEST_RPC=http://u:p@127.0.0.1:19443/wallet/w go test ./bifrost/txscript/ltcd-txscript -run TestTaprootKeyPathSpendRegtest -v
const ltcRegtestRPCEnv = "LTC_REGTEST_RPC"

type regtestRPC struct {
	endpoint string
	user     string
	pass     string
	client   *http.Client
}

func newRegtestRPC(t *testing.T) *regtestRPC {
	t.Helper()
	raw := os.Getenv(ltcRegtestRPCEnv)
	if raw == "" {
		t.Skipf("%s not set; skipping Litecoin regtest end-to-end test", ltcRegtestRPCEnv)
	}
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse %s: %v", ltcRegtestRPCEnv, err)
	}
	rpc := &regtestRPC{client: &http.Client{Timeout: 60 * time.Second}}
	if u.User != nil {
		rpc.user = u.User.Username()
		rpc.pass, _ = u.User.Password()
		u.User = nil
	}
	rpc.endpoint = u.String()
	return rpc
}

func (r *regtestRPC) call(t *testing.T, out interface{}, method string, params ...interface{}) {
	t.Helper()
	if params == nil {
		params = []interface{}{}
	}
	body, err := json.Marshal(map[string]interface{}{"jsonrpc": "1.0", "id": "taproot-test", "method": method, "params": params})
	if err != nil {
		t.Fatalf("%s: marshal: %v", method, err)
	}
	req, err := http.NewRequest(http.MethodPost, r.endpoint, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("%s: request: %v", method, err)
	}
	req.Header.Set("Content-Type", "text/plain")
	if r.user != "" {
		req.SetBasicAuth(r.user, r.pass)
	}
	resp, err := r.client.Do(req)
	if err != nil {
		t.Fatalf("%s: http: %v", method, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("%s: read: %v", method, err)
	}
	var envelope struct {
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatalf("%s: decode (%s): %v", method, string(raw), err)
	}
	if envelope.Error != nil {
		t.Fatalf("%s: rpc error %d: %s", method, envelope.Error.Code, envelope.Error.Message)
	}
	if out != nil {
		if err := json.Unmarshal(envelope.Result, out); err != nil {
			t.Fatalf("%s: decode result %s: %v", method, string(envelope.Result), err)
		}
	}
}

func (r *regtestRPC) rawTx(t *testing.T, txid string) *wire.MsgTx {
	t.Helper()
	var rawHex string
	r.call(t, &rawHex, "getrawtransaction", txid, false)
	var tx wire.MsgTx
	if err := tx.Deserialize(bytes.NewReader(mustHex(t, rawHex))); err != nil {
		t.Fatalf("deserialize %s: %v", txid, err)
	}
	return &tx
}

// mineBlock mines one block to a fresh wallet address and returns its hash.
func (r *regtestRPC) mineBlock(t *testing.T) string {
	t.Helper()
	var mineTo string
	r.call(t, &mineTo, "getnewaddress", "", "bech32")
	var hashes []string
	r.call(t, &hashes, "generatetoaddress", 1, mineTo)
	if len(hashes) != 1 {
		t.Fatalf("generatetoaddress returned %d blocks", len(hashes))
	}
	return hashes[0]
}

// TestTaprootKeyPathSpendRegtest proves the primitives against Litecoin Core:
// it derives a P2TR address, has the node's wallet fund it twice, spends both
// outputs in one transaction signed with SigHashDefault and SigHashAll, and
// requires the node to accept and confirm that transaction.
func TestTaprootKeyPathSpendRegtest(t *testing.T) {
	rpc := newRegtestRPC(t)
	net := &chaincfg.RegressionNetParams

	var info struct {
		Softforks map[string]struct {
			Active bool `json:"active"`
		} `json:"softforks"`
	}
	rpc.call(t, &info, "getblockchaininfo")
	if !info.Softforks["taproot"].Active {
		t.Fatal("taproot is not active on the regtest node")
	}

	// Vault-style key: the internal key is tweaked into the output key.
	internal, err := btcec.NewPrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	signable := newPrivKeySchnorrSignable(t, internal)
	outputKey := signable.TaprootOutputKey()
	vaultScript, err := PayToTaprootScript(outputKey)
	if err != nil {
		t.Fatal(err)
	}
	vaultAddr, err := NewAddressTaproot(outputKey, net)
	if err != nil {
		t.Fatal(err)
	}
	if script, err := PayToAddrScript(vaultAddr); err != nil || !bytes.Equal(script, vaultScript) {
		t.Fatalf("address script %x (%v) != output script %x", script, err, vaultScript)
	}
	t.Logf("vault P2TR address %s", vaultAddr)

	var validation struct {
		IsValid        bool   `json:"isvalid"`
		WitnessVersion int    `json:"witness_version"`
		ScriptPubKey   string `json:"scriptPubKey"`
	}
	rpc.call(t, &validation, "validateaddress", vaultAddr.String())
	if !validation.IsValid || validation.WitnessVersion != 1 || validation.ScriptPubKey != hex.EncodeToString(vaultScript) {
		t.Fatalf("node rejected our address: %+v", validation)
	}

	// Fund the vault twice so the spend has two inputs.
	const fundingAmount = 1.25
	fundingSats := int64(fundingAmount * 1e8)
	spend := wire.NewMsgTx(2)
	prevOuts := NewMultiPrevOutFetcher(nil)
	for i := 0; i < 2; i++ {
		var txid string
		rpc.call(t, &txid, "sendtoaddress", vaultAddr.String(), fundingAmount)
		fundingTx := rpc.rawTx(t, txid)
		hash, err := chainhash.NewHashFromStr(txid)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for vout, txOut := range fundingTx.TxOut {
			if !bytes.Equal(txOut.PkScript, vaultScript) {
				continue
			}
			if txOut.Value != fundingSats {
				t.Fatalf("funding output %d value %d, want %d", vout, txOut.Value, fundingSats)
			}
			op := wire.OutPoint{Hash: *hash, Index: uint32(vout)}
			spend.AddTxIn(wire.NewTxIn(&op, nil, nil))
			prevOuts.AddPrevOut(op, wire.NewTxOut(txOut.Value, txOut.PkScript))
			found = true
			break
		}
		if !found {
			t.Fatalf("funding tx %s has no output to %s", txid, vaultAddr)
		}
	}
	rpc.mineBlock(t)

	// Spend back to the node's wallet.
	var destAddr string
	rpc.call(t, &destAddr, "getnewaddress", "", "bech32")
	dest, err := DecodeAddress(destAddr, net)
	if err != nil {
		t.Fatal(err)
	}
	destScript, err := PayToAddrScript(dest)
	if err != nil {
		t.Fatal(err)
	}
	const fee = 20_000
	spend.AddTxOut(wire.NewTxOut(2*fundingSats-fee, destScript))

	sigHashes, err := NewTaprootSigHashes(spend, prevOuts)
	if err != nil {
		t.Fatal(err)
	}
	for idx, hashType := range []SigHashType{SigHashDefault, SigHashAll} {
		witness, err := TaprootKeyPathSignature(spend, sigHashes, idx, hashType, prevOuts, signable)
		if err != nil {
			t.Fatalf("sign input %d: %v", idx, err)
		}
		spend.TxIn[idx].Witness = witness
	}

	var serialized bytes.Buffer
	if err := spend.Serialize(&serialized); err != nil {
		t.Fatal(err)
	}
	spendHex := hex.EncodeToString(serialized.Bytes())

	var accept []struct {
		Allowed      bool   `json:"allowed"`
		RejectReason string `json:"reject-reason"`
	}
	rpc.call(t, &accept, "testmempoolaccept", []string{spendHex})
	if len(accept) != 1 || !accept[0].Allowed {
		t.Fatalf("node rejected the taproot spend: %+v (tx %s)", accept, spendHex)
	}

	var sentTxID string
	rpc.call(t, &sentTxID, "sendrawtransaction", spendHex)
	if want := spend.TxHash().String(); sentTxID != want {
		t.Fatalf("broadcast txid %s, want %s", sentTxID, want)
	}
	blockHash := rpc.mineBlock(t)

	// The node may run without -txindex, so look the spend up by block.
	var confirmed struct {
		Confirmations int `json:"confirmations"`
		Vin           []struct {
			Witness []string `json:"txinwitness"`
		} `json:"vin"`
	}
	rpc.call(t, &confirmed, "getrawtransaction", sentTxID, true, blockHash)
	if confirmed.Confirmations < 1 {
		t.Fatalf("spend %s not confirmed", sentTxID)
	}
	if len(confirmed.Vin) != 2 || len(confirmed.Vin[0].Witness[0]) != 2*SchnorrSignatureSize || len(confirmed.Vin[1].Witness[0]) != 2*(SchnorrSignatureSize+1) {
		t.Fatalf("unexpected witness shape in confirmed tx: %+v", confirmed.Vin)
	}
	t.Logf("taproot key-path spend %s confirmed with %d confirmation(s)", sentTxID, confirmed.Confirmations)
}
