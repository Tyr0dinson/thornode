package utxo

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/btcsuite/btcd/wire"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"

	. "gitlab.com/thorchain/thornode/v3/test/simulation/pkg/types"
)

// fakeUTXONodeCov is a minimal JSON-RPC node: it reports the client address as
// already imported, lists the configured unspent outputs and records calls.
type fakeUTXONodeCov struct {
	mu        sync.Mutex
	calls     map[string]int
	unspent   []map[string]any
	zecUTXOs  []map[string]any
	failList  bool
	importErr bool
	imported  bool
	listAddrs []string
}

func newFakeUTXONodeCov(t *testing.T, f *fakeUTXONodeCov) string {
	t.Helper()
	f.calls = map[string]int{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     any               `json:"id"`
			Method string            `json:"method"`
			Params []json.RawMessage `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		f.mu.Lock()
		f.calls[req.Method]++
		f.mu.Unlock()

		reply := func(result any) {
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "1.0", "id": req.ID, "result": result, "error": nil})
		}
		replyErr := func(code int, msg string) {
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "1.0", "id": req.ID, "result": nil, "error": map[string]any{"code": code, "message": msg}})
		}

		switch req.Method {
		case "getaddressinfo":
			reply(map[string]any{"ismine": false, "iswatchonly": f.imported})
		case "importaddress":
			if f.importErr {
				replyErr(-4, "import failed")
				return
			}
			reply(nil)
		case "listunspent":
			if f.failList {
				replyErr(-1, "list failed")
				return
			}
			var addrs []string
			_ = json.Unmarshal(req.Params[2], &addrs)
			f.mu.Lock()
			f.listAddrs = append(f.listAddrs, addrs...)
			f.mu.Unlock()
			reply(f.unspent)
		case "getblockcount":
			reply(3_000_000)
		case "getblockchaininfo":
			// ZEC signing picks its consensus branch from the upgrade schedule
			reply(map[string]any{
				"blocks":    3_000_000,
				"upgrades":  map[string]any{"c2d6d0b4": map[string]any{"activationheight": 0}},
				"consensus": map[string]any{"chaintip": "c2d6d0b4", "nextblock": "c2d6d0b4"},
			})
		case "getaddressutxos":
			if f.failList {
				replyErr(-1, "list failed")
				return
			}
			reply(f.zecUTXOs)
		default:
			t.Errorf("unexpected method %s", req.Method)
			replyErr(-32601, "Method not found")
		}
	}))
	t.Cleanup(server.Close)
	return server.URL
}

func newCovClient(t *testing.T, chain common.Chain, node *fakeUTXONodeCov) *Client {
	t.Helper()
	node.imported = true
	host := newFakeUTXONodeCov(t, node)
	lc, err := NewConstructor(host)(chain, newTestKeys(t, "covuser", testMnemonic))
	if err != nil {
		t.Fatalf("new %s client: %v", chain, err)
	}
	return lc.(*Client)
}

func TestSignTxSignsSpendableInputsPerChainCov(t *testing.T) {
	memo := "=:ETH.ETH:0x1234567890123456789012345678901234567890:0/1/0:" + strings.Repeat("x", 40)
	for _, chain := range []common.Chain{common.BTCChain, common.LTCChain, common.BCHChain, common.DOGEChain, common.ZECChain} {
		t.Run(chain.String(), func(t *testing.T) {
			node := &fakeUTXONodeCov{}
			c := newCovClient(t, chain, node)
			node.unspent = []map[string]any{
				{"txid": strings.Repeat("22", 32), "vout": 1, "address": c.address.String(), "amount": 5.0, "confirmations": 1, "spendable": true},
				{"txid": strings.Repeat("11", 32), "vout": 0, "address": c.address.String(), "amount": 50.0, "confirmations": 10, "spendable": true},
			}
			node.zecUTXOs = []map[string]any{
				{"txid": strings.Repeat("11", 32), "address": c.address.String(), "outputIndex": 0, "satoshis": 5_000_000_000, "height": 2_999_990},
			}

			pay := cosmos.NewUint(200_000_000)
			signed, err := c.SignTx(SimTx{
				Chain:     chain,
				ToAddress: c.address,
				Coin:      common.NewCoin(chain.GetGasAsset(), pay),
				Memo:      memo,
			})
			if err != nil {
				t.Fatalf("sign: %v", err)
			}
			if len(signed) == 0 {
				t.Fatal("empty signed tx")
			}

			if chain.Equals(common.ZECChain) {
				// the inputs are reserved until a broadcast succeeds
				if len(c.pendingSpentUtxos) != 1 || c.spentUtxos[strings.Repeat("11", 32)+"-0"] {
					t.Fatalf("ZEC inputs must be pending, got %v / %v", c.pendingSpentUtxos, c.spentUtxos)
				}
				if !c.pendingSpentUtxoLocked(strings.Repeat("11", 32) + "-0") {
					t.Fatal("pending input must be locked")
				}
				// a second signing has no unreserved UTXOs left
				if _, err = c.SignTx(SimTx{Chain: chain, ToAddress: c.address, Coin: common.NewCoin(chain.GetGasAsset(), pay)}); err == nil ||
					!strings.Contains(err.Error(), "insufficient balance") {
					t.Fatalf("expected reserved inputs to be skipped, got %v", err)
				}
				return
			}

			tx := wire.NewMsgTx(wire.TxVersion)
			if err = tx.Deserialize(bytes.NewReader(signed)); err != nil {
				t.Fatalf("deserialize: %v", err)
			}
			// the oldest (most confirmed) UTXO covers the payment alone
			if len(tx.TxIn) != 1 || tx.TxIn[0].PreviousOutPoint.Hash.String() != strings.Repeat("11", 32) {
				t.Fatalf("expected the 50-coin input to be spent, got %+v", tx.TxIn)
			}
			if tx.TxOut[0].Value != int64(pay.Uint64()) {
				t.Fatalf("payment output %d != %d", tx.TxOut[0].Value, pay.Uint64())
			}
			scripts, err := c.getMemoScripts(memo)
			if err != nil {
				t.Fatal(err)
			}
			// pay, change, OP_RETURN, continuation outputs
			if len(tx.TxOut) != 2+len(scripts) {
				t.Fatalf("expected %d outputs, got %d", 2+len(scripts), len(tx.TxOut))
			}
			cont := int64(len(scripts)-1) * chain.P2WPKHOutputValue()
			wantChange := int64(50*common.One) - int64(pay.Uint64()) - int64(c.getGasSats()*common.One) - cont
			if tx.TxOut[1].Value != wantChange {
				t.Fatalf("change %d != %d", tx.TxOut[1].Value, wantChange)
			}
			if tx.TxOut[2].Value != 0 || !bytes.Equal(tx.TxOut[2].PkScript, scripts[0]) {
				t.Fatal("memo OP_RETURN output mismatch")
			}
			signedIn := len(tx.TxIn[0].SignatureScript) > 0 || len(tx.TxIn[0].Witness) > 0
			if !signedIn {
				t.Fatal("input must carry a signature")
			}
		})
	}
}

func TestSignTxRejectsDustAndInsufficientFundsCov(t *testing.T) {
	node := &fakeUTXONodeCov{}
	c := newCovClient(t, common.BTCChain, node)
	_, err := c.SignTx(SimTx{Chain: common.BTCChain, ToAddress: c.address, Coin: common.NewCoin(common.BTCAsset, common.BTCChain.DustThreshold())})
	if err == nil || !strings.Contains(err.Error(), "below dust threshold") {
		t.Fatalf("expected dust error, got %v", err)
	}

	node.unspent = []map[string]any{{"txid": strings.Repeat("11", 32), "vout": 0, "amount": 0.5, "confirmations": 1}}
	_, err = c.SignTx(SimTx{Chain: common.BTCChain, ToAddress: c.address, Coin: common.NewCoin(common.BTCAsset, cosmos.NewUint(common.One))})
	if err == nil || !strings.Contains(err.Error(), "insufficient balance") {
		t.Fatalf("expected insufficient balance, got %v", err)
	}

	node.failList = true
	_, err = c.SignTx(SimTx{Chain: common.BTCChain, ToAddress: c.address, Coin: common.NewCoin(common.BTCAsset, cosmos.NewUint(common.One))})
	if err == nil || !strings.Contains(err.Error(), "fail to get UTXOs") {
		t.Fatalf("expected list error, got %v", err)
	}
}

func TestBuildTxErrorsCov(t *testing.T) {
	node := &fakeUTXONodeCov{}
	c := newCovClient(t, common.LTCChain, node)
	src, err := c.getSourceScript(SimTx{})
	if err != nil {
		t.Fatal(err)
	}

	node.unspent = []map[string]any{{"txid": "not-a-hash", "vout": 0, "amount": 5.0}}
	if _, _, err = c.buildTx(SimTx{Coin: common.NewCoin(common.LTCAsset, cosmos.NewUint(1000))}, src); err == nil || !strings.Contains(err.Error(), "fail to parse txID") {
		t.Fatalf("expected txid parse error, got %v", err)
	}

	node.unspent = []map[string]any{{"txid": strings.Repeat("11", 32), "vout": 0, "amount": 5.0}}
	if _, _, err = c.buildTx(SimTx{ToAddress: "bogus", Coin: common.NewCoin(common.LTCAsset, cosmos.NewUint(1000))}, src); err == nil || !strings.Contains(err.Error(), "fail to decode next address") {
		t.Fatalf("expected address decode error, got %v", err)
	}

	tooLong := strings.Repeat("m", 1000)
	if _, _, err = c.buildTx(SimTx{ToAddress: c.address, Coin: common.NewCoin(common.LTCAsset, cosmos.NewUint(1000)), Memo: tooLong}, src); err == nil || !strings.Contains(err.Error(), "fail to generate memo scripts") {
		t.Fatalf("expected memo error, got %v", err)
	}

	// exact payment with no change output
	node.unspent = []map[string]any{{"txid": strings.Repeat("11", 32), "vout": 0, "amount": 1.0001}}
	tx, amounts, err := c.buildTx(SimTx{ToAddress: c.address, Coin: common.NewCoin(common.LTCAsset, cosmos.NewUint(common.One))}, src)
	if err != nil {
		t.Fatal(err)
	}
	if len(tx.TxOut) != 1 || amounts[strings.Repeat("11", 32)+"-0"] != 100_010_000 {
		t.Fatalf("expected single output without change, got %d outputs, amounts %v", len(tx.TxOut), amounts)
	}
}

func TestGetAccountCov(t *testing.T) {
	node := &fakeUTXONodeCov{}
	c := newCovClient(t, common.DOGEChain, node)
	node.unspent = []map[string]any{
		{"txid": strings.Repeat("11", 32), "vout": 0, "amount": 1.5},
		{"txid": strings.Repeat("22", 32), "vout": 0, "amount": 2.25},
	}
	acct, err := c.GetAccount(nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := acct.Coins.GetCoin(common.DOGEAsset).Amount.Uint64(); got != 375_000_000 {
		t.Fatalf("expected 3.75 DOGE, got %d", got)
	}

	pk := c.pubKey
	if _, err = c.GetAccount(&pk); err != nil {
		t.Fatal(err)
	}
	if node.listAddrs[len(node.listAddrs)-1] != c.address.String() {
		t.Fatalf("pubkey must resolve to the client address, got %v", node.listAddrs)
	}

	bad := common.PubKey("bogus")
	if _, err = c.GetAccount(&bad); err == nil || !strings.Contains(err.Error(), "fail to get address from pubkey") {
		t.Fatalf("expected pubkey error, got %v", err)
	}

	node.failList = true
	if _, err = c.GetAccount(nil); err == nil || !strings.Contains(err.Error(), "fail to get UTXOs") {
		t.Fatalf("expected list error, got %v", err)
	}
}

func TestNewClientImportsUntrackedAddressCov(t *testing.T) {
	node := &fakeUTXONodeCov{}
	host := newFakeUTXONodeCov(t, node)
	if _, err := NewClient(common.BTCChain, host, newTestKeys(t, "covuser", testMnemonic)); err != nil {
		t.Fatal(err)
	}
	if node.calls["importaddress"] != 1 {
		t.Fatalf("expected one importaddress, got %v", node.calls)
	}

	node.importErr = true
	if _, err := NewClient(common.BTCChain, host, newTestKeys(t, "covuser", testMnemonic)); err == nil || !strings.Contains(err.Error(), "fail to import address") {
		t.Fatalf("expected import error, got %v", err)
	}

	// ZEC never imports
	before := node.calls["getaddressinfo"]
	if _, err := NewClient(common.ZECChain, host, newTestKeys(t, "covuser", testMnemonic)); err != nil {
		t.Fatal(err)
	}
	if node.calls["getaddressinfo"] != before {
		t.Fatal("ZEC startup must not query address info")
	}
}

func TestMiscHelpersCov(t *testing.T) {
	c := &Client{chain: common.BTCChain}
	if _, err := c.SignContractTx(SimContractTx{}); err == nil || err.Error() != "not implemented" {
		t.Fatalf("expected not implemented, got %v", err)
	}
	for chain, want := range map[common.Chain]float64{
		common.DOGEChain: 0.1, common.BCHChain: 0.0001, common.LTCChain: 0.0001, common.BTCChain: 0.0001, common.ZECChain: 0.001,
	} {
		if got := (&Client{chain: chain}).getGasSats(); got != want {
			t.Errorf("%s gas %v != %v", chain, got, want)
		}
	}
	// a short memo needs no continuation outputs, so only the gas is added
	if got := c.getPaymentAmount(SimTx{Coin: common.NewCoin(common.BTCAsset, cosmos.NewUint(common.One)), Memo: "m"}); got != 1.0001 {
		t.Fatalf("payment amount %v", got)
	}
	if scripts, err := c.getMemoScripts(""); err != nil || scripts != nil {
		t.Fatalf("empty memo must yield no scripts, got %v %v", scripts, err)
	}
	if c.getChainCfgBTC() == nil || c.getChainCfgLTC() == nil || c.getChainCfgBCH() == nil || c.getChainCfgDOGE() == nil || c.getChainCfgZEC() == nil {
		t.Fatal("chain params must be defined for the build network")
	}

	// bad source addresses are reported per chain
	for _, chain := range []common.Chain{common.BTCChain, common.LTCChain, common.BCHChain, common.DOGEChain, common.ZECChain} {
		bad := &Client{chain: chain, address: "bogus"}
		if _, err := bad.getSourceScript(SimTx{}); err == nil || !strings.Contains(err.Error(), "fail to decode source address") {
			t.Errorf("%s: expected source decode error, got %v", chain, err)
		}
	}
}
