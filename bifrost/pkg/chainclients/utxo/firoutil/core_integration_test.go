package firoutil

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/btcsuite/btcd/btcec"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/wire"
	"gitlab.com/thorchain/thornode/v3/bifrost/txscript/txscript"
)

// FIROD opts into a native daemon test. It creates and removes its own regtest
// wallet; it never connects to an operator's wallet or a public network.
func TestFiroCoreExchangeSpend(t *testing.T) {
	binary := os.Getenv("FIROD")
	if binary == "" {
		t.Skip("set FIROD to FIRO Core v0.14.18.2 firod to run")
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address, ok := listener.Addr().(*net.TCPAddr)
	listener.Close()
	if !ok {
		t.Fatal("listener did not return a TCP address")
	}
	port := address.Port
	dir := t.TempDir()
	var logs bytes.Buffer
	// A peerless wallet must put funding transactions in the public mempool,
	// rather than holding them in Dandelion's relay-only stem pool.
	cmd := exec.Command(binary, "-regtest", "-datadir="+dir, "-server", "-listen=0", "-discover=0", "-dandelion=0", "-txindex=1", "-rpcuser=firotest", "-rpcpassword=localtest", "-rpcport="+strconv.Itoa(port), "-printtoconsole=1")
	cmd.Stdout, cmd.Stderr = &logs, &logs
	err = cmd.Start()
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Timeout: 90 * time.Second}
	call := func(method string, params []any, result any) error {
		payload, _ := json.Marshal(map[string]any{"jsonrpc": "1.0", "id": 1, "method": method, "params": params})
		request, requestErr := http.NewRequest(http.MethodPost, fmt.Sprintf("http://127.0.0.1:%d", port), bytes.NewReader(payload))
		if requestErr != nil {
			return requestErr
		}
		request.SetBasicAuth("firotest", "localtest")
		response, responseErr := client.Do(request)
		if responseErr != nil {
			return responseErr
		}
		defer response.Body.Close()
		var envelope struct {
			Result json.RawMessage
			Error  *struct {
				Code    int
				Message string
			}
		}
		decodeErr := json.NewDecoder(response.Body).Decode(&envelope)
		if decodeErr != nil {
			return decodeErr
		}
		if envelope.Error != nil {
			return fmt.Errorf("RPC %s: %d %s", method, envelope.Error.Code, envelope.Error.Message)
		}
		if result == nil {
			return nil
		}
		return json.Unmarshal(envelope.Result, result)
	}
	t.Cleanup(func() {
		_ = call("stop", nil, nil)
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			_ = cmd.Process.Kill()
			<-done
		}
		if t.Failed() {
			t.Log(logs.String())
		}
	})
	deadline := time.Now().Add(30 * time.Second)
	for {
		err = call("getblockcount", nil, nil)
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal(err)
		}
		time.Sleep(100 * time.Millisecond)
	}
	must := func(method string, params []any, result any) {
		t.Helper()
		callErr := call(method, params, result)
		if callErr != nil {
			t.Fatal(callErr)
		}
	}
	// Exchange addresses activate at regtest height 1000. Coinbase outputs
	// cannot pay exchange addresses, so mine into Core's ordinary wallet first.
	must("generate", []any{1001}, nil)
	mockTime := time.Now().Unix() + 3600
	minePayment := func(id string) {
		// Core holds young, non-InstantSend transactions out of templates.
		// Advance regtest time; do not disable consensus or fabricate locks.
		for attempt := 0; attempt < 20; attempt++ {
			time.Sleep(100 * time.Millisecond) // allow Core's validation callbacks to run
			mockTime += 601
			must("setmocktime", []any{mockTime}, nil)
			must("generate", []any{1}, nil)
			var mined struct{ Confirmations int64 }
			must("getrawtransaction", []any{id, true}, &mined)
			if mined.Confirmations > 0 {
				return
			}
		}
		t.Fatal("Core did not mine transparent payment")
	}
	key, _ := btcec.PrivKeyFromBytes(btcec.S256(), []byte{1})
	vault, _ := NewExchangeAddress(key.PubKey().SerializeCompressed(), RegTest)
	must("importaddress", []any{vault.String(), "", false}, nil)
	for _, kind := range []Kind{PubKeyHash, ScriptHash, Exchange} {
		destination, _ := NewAddress(bytes.Repeat([]byte{2}, 20), kind, RegTest)
		var valid struct {
			IsValid bool `json:"isvalid"`
		}
		must("validateaddress", []any{destination.String()}, &valid)
		if !valid.IsValid {
			t.Fatalf("Core rejected address %s", destination)
		}
		var funding string
		must("sendtoaddress", []any{vault.String(), 1}, &funding)
		minePayment(funding)
		var unspent []struct {
			TxID   string `json:"txid"`
			Vout   uint32 `json:"vout"`
			Script string `json:"scriptPubKey"`
		}
		must("listunspent", []any{1, 9999999, []string{vault.String()}}, &unspent)
		var output *wire.OutPoint
		for _, item := range unspent {
			if item.TxID == funding {
				if item.Script != hex.EncodeToString(vault.Script()) {
					t.Fatal("Core exchange script mismatch")
				}
				hash, err := chainhash.NewHashFromStr(funding)
				if err != nil {
					t.Fatal(err)
				}
				output = wire.NewOutPoint(hash, item.Vout)
			}
		}
		if output == nil {
			t.Fatal("exchange funding missing from watch-only wallet")
		}
		tx := wire.NewMsgTx(1)
		tx.AddTxIn(wire.NewTxIn(output, nil, nil))
		tx.AddTxOut(wire.NewTxOut(99000000, destination.Script()))
		// Maximum extended memo: 79 bytes + marker, then 9 P2PKH outputs.
		memo := strings.Repeat("a", 250)
		first, _ := txscript.NullDataScript([]byte(memo[:79] + "^"))
		tx.AddTxOut(wire.NewTxOut(0, first))
		for rest := []byte(memo[79:]); len(rest) > 0; {
			hash := make([]byte, 20)
			n := copy(hash, rest)
			rest = rest[n:]
			script, _ := PayToPubKeyHashScript(hash)
			tx.AddTxOut(wire.NewTxOut(546, script))
		}
		previous := []*wire.TxOut{wire.NewTxOut(100000000, vault.Script())}
		hashes, err := SignatureHashes(tx, previous)
		if err != nil {
			t.Fatal(err)
		}
		wrongPrevious := []*wire.TxOut{wire.NewTxOut(100000000, vault.Script()[1:])}
		wrongHash := referenceHash(t, tx, wrongPrevious, 0)
		sig, _ := key.Sign(wrongHash)
		tx.TxIn[0].SignatureScript, _ = SignatureScript(wrongHash, sig, key.PubKey())
		var raw bytes.Buffer
		err = tx.Serialize(&raw)
		if err != nil {
			t.Fatal(err)
		}
		err = call("sendrawtransaction", []any{hex.EncodeToString(raw.Bytes()), false}, nil)
		if err == nil || !strings.Contains(err.Error(), "-26") {
			t.Fatalf("Core did not reject signature omitting OP_EXCHANGEADDR: %v", err)
		}
		sig, _ = key.Sign(hashes[0])
		tx.TxIn[0].SignatureScript, err = SignatureScript(hashes[0], sig, key.PubKey())
		if err != nil {
			t.Fatal(err)
		}
		err = VerifyTransaction(tx, previous)
		if err != nil {
			t.Fatal(err)
		}
		raw.Reset()
		err = tx.Serialize(&raw)
		if err != nil {
			t.Fatal(err)
		}
		var sent string
		must("sendrawtransaction", []any{hex.EncodeToString(raw.Bytes()), false}, &sent)
		if sent != tx.TxHash().String() {
			t.Fatal("Core and Go disagree on transaction ID")
		}
		minePayment(sent)
		var observed struct {
			Type          int
			Confirmations int64
			ChainLock     bool `json:"chainlock"`
		}
		must("getrawtransaction", []any{sent, true}, &observed)
		if observed.Type != 0 || observed.Confirmations != 1 || observed.ChainLock {
			t.Fatalf("unexpected ordinary single-node finality: %+v", observed)
		}
		t.Logf("Core accepted exchange spend to kind %d with 250-byte memo: %s", kind, sent)
	}
}
