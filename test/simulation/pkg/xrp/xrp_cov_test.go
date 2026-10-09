package xrp

import (
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	binarycodec "github.com/Peersyst/xrpl-go/binary-codec"
	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	cryptocodec "github.com/cosmos/cosmos-sdk/crypto/codec"
	"github.com/cosmos/cosmos-sdk/crypto/hd"
	"github.com/cosmos/cosmos-sdk/crypto/keyring"

	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	"gitlab.com/thorchain/thornode/v3/cmd"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"

	. "gitlab.com/thorchain/thornode/v3/test/simulation/pkg/types"
)

const mnemonicCov = "dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog fossil"

func keysCov(t *testing.T) *thorclient.Keys {
	t.Helper()
	registry := codectypes.NewInterfaceRegistry()
	cryptocodec.RegisterInterfaces(registry)
	kr := keyring.NewInMemory(codec.NewProtoCodec(registry))
	if _, err := kr.NewAccount("dog", mnemonicCov, "", cmd.THORChainHDPath, hd.Secp256k1); err != nil {
		t.Fatal(err)
	}
	return thorclient.NewKeysWithKeybase(kr, "dog", "")
}

func pubKeyCov(t *testing.T, keys *thorclient.Keys) common.PubKey {
	t.Helper()
	priv, err := keys.GetPrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	pk, err := cryptocodec.ToCmtPubKeyInterface(priv.PubKey())
	if err != nil {
		t.Fatal(err)
	}
	pub, err := common.NewPubKeyFromCrypto(pk)
	if err != nil {
		t.Fatal(err)
	}
	return pub
}

// fakeRippledCov answers rippled JSON-RPC methods. Each method may have a queue of
// scripted results; the last entry repeats.
type fakeRippledCov struct {
	mu      sync.Mutex
	results map[string][]map[string]interface{}
	status  map[string]int
	calls   map[string]int
	params  map[string][]map[string]interface{}
}

func newFakeRippledCov(t *testing.T) (*fakeRippledCov, *httptest.Server) {
	f := &fakeRippledCov{
		results: map[string][]map[string]interface{}{},
		status:  map[string]int{},
		calls:   map[string]int{},
		params:  map[string][]map[string]interface{}{},
	}
	f.set("ledger", map[string]interface{}{"ledger_index": 100, "validated": true, "status": "success"})
	f.set("account_info", map[string]interface{}{
		"account_data": map[string]interface{}{"Balance": "2500000", "Sequence": 9},
		"status":       "success",
	})
	f.set("submit", map[string]interface{}{"engine_result": "tesSUCCESS", "status": "success"})
	f.set("tx", map[string]interface{}{"validated": true, "status": "success"})
	srv := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(srv.Close)
	return f, srv
}

func (f *fakeRippledCov) set(method string, results ...map[string]interface{}) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.results[method] = results
	f.calls[method] = 0
}

func (f *fakeRippledCov) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	var req struct {
		Method string                   `json:"method"`
		Params []map[string]interface{} `json:"params"`
	}
	_ = json.Unmarshal(body, &req)
	f.mu.Lock()
	defer f.mu.Unlock()
	if code := f.status[req.Method]; code != 0 {
		w.WriteHeader(code)
		_, _ = w.Write([]byte(req.Method + " http failure"))
		return
	}
	idx := f.calls[req.Method]
	f.calls[req.Method]++
	if len(req.Params) > 0 {
		f.params[req.Method] = append(f.params[req.Method], req.Params[0])
	}
	rs := f.results[req.Method]
	if len(rs) == 0 {
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"result": map[string]interface{}{"error": "unknownCmd"}})
		return
	}
	if idx >= len(rs) {
		idx = len(rs) - 1
	}
	_ = json.NewEncoder(w).Encode(map[string]interface{}{"result": rs[idx]})
}

func errResCov(e string) map[string]interface{} {
	return map[string]interface{}{"error": e, "status": "error"}
}

func newClientCov(t *testing.T) (*Client, *fakeRippledCov, *thorclient.Keys) {
	t.Helper()
	f, srv := newFakeRippledCov(t)
	keys := keysCov(t)
	lc, err := NewConstructor(srv.URL)(common.XRPChain, keys)
	if err != nil {
		t.Fatal(err)
	}
	return lc.(*Client), f, keys
}

func TestNewClientErrorsCov(t *testing.T) {
	empty := thorclient.NewKeysWithKeybase(keyring.NewInMemory(codec.NewProtoCodec(codectypes.NewInterfaceRegistry())), "x", "")
	if _, err := NewClient(common.XRPChain, "http://127.0.0.1:1", empty); err == nil || !strings.Contains(err.Error(), "private key") {
		t.Fatalf("expected private key error, got %v", err)
	}
	if _, err := NewClient(common.XRPChain, "", keysCov(t)); err == nil || !strings.Contains(err.Error(), "rpc config") {
		t.Fatalf("expected rpc config error, got %v", err)
	}
}

func TestGetHeightCov(t *testing.T) {
	c, f, _ := newClientCov(t)
	h, err := c.GetHeight()
	if err != nil || h != 100 {
		t.Fatalf("height %d %v", h, err)
	}
	f.set("ledger", errResCov("noNetwork"))
	if _, err = c.GetHeight(); err == nil || !strings.Contains(err.Error(), "noNetwork") {
		t.Fatalf("expected ledger error, got %v", err)
	}
}

func TestGetAccountCov(t *testing.T) {
	c, f, keys := newClientCov(t)
	acct, err := c.GetAccount(nil)
	if err != nil {
		t.Fatal(err)
	}
	if acct.Sequence != 9 || len(acct.Coins) != 1 || acct.Coins[0].Amount.Uint64() != 250000000 || !acct.Coins[0].Asset.Equals(common.XRPAsset) {
		t.Fatalf("account = %+v", acct)
	}
	if got := f.params["account_info"][0]["account"]; got != c.localKm.AccountID {
		t.Fatalf("queried account %v, want %s", got, c.localKm.AccountID)
	}

	// explicit pubkey resolves to the same classic address
	pk := pubKeyCov(t, keys)
	if _, err = c.GetAccount(&pk); err != nil {
		t.Fatal(err)
	}
	if got := f.params["account_info"][1]["account"]; got != c.localKm.AccountID {
		t.Fatalf("explicit pubkey queried %v", got)
	}
	bad := common.PubKey("bad")
	if _, err = c.GetAccount(&bad); err == nil {
		t.Fatal("expected bad pubkey error")
	}

	// unfunded account
	f.set("account_info", errResCov("actNotFound"))
	acct, err = c.GetAccount(nil)
	if err != nil || acct.Sequence != 0 || !acct.Coins[0].Amount.IsZero() {
		t.Fatalf("not found account = %+v %v", acct, err)
	}

	f.set("account_info", errResCov("internal"))
	if _, err = c.GetAccount(nil); err == nil || !strings.Contains(err.Error(), "internal") {
		t.Fatalf("expected error, got %v", err)
	}
}

func TestSignAndBroadcastTxCov(t *testing.T) {
	c, f, _ := newClientCov(t)
	dest := "rPEPPER7kfTD9w2To4CQk6UCfuHM9c6GDY"
	signed, err := c.SignTx(SimTx{
		Chain:     common.XRPChain,
		ToAddress: common.Address(dest),
		Coin:      common.NewCoin(common.XRPAsset, cosmos.NewUint(123400)),
		Memo:      "=:BTC.BTC:addr",
	})
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := binarycodec.Decode(hex.EncodeToString(signed))
	if err != nil {
		t.Fatal(err)
	}
	if decoded["Destination"] != dest || decoded["Amount"] != "1234" || decoded["Account"] != c.localKm.AccountID {
		t.Fatalf("decoded = %v", decoded)
	}
	if decoded["Sequence"] != uint32(9) || decoded["LastLedgerSequence"] != uint32(120) || decoded["NetworkID"] != uint32(1234) {
		t.Fatalf("autofill fields = %v %v %v", decoded["Sequence"], decoded["LastLedgerSequence"], decoded["NetworkID"])
	}
	if _, ok := decoded["TxnSignature"].(string); !ok {
		t.Fatal("missing signature")
	}
	memos, _ := decoded["Memos"].([]any)
	if len(memos) != 1 {
		t.Fatalf("memos = %v", decoded["Memos"])
	}

	// no memo => no Memos field
	noMemo, err := c.SignTx(SimTx{ToAddress: common.Address(dest), Coin: common.NewCoin(common.XRPAsset, cosmos.NewUint(100))})
	if err != nil {
		t.Fatal(err)
	}
	d2, _ := binarycodec.Decode(hex.EncodeToString(noMemo))
	if _, ok := d2["Memos"]; ok {
		t.Fatal("unexpected memos")
	}

	// broadcast: accepted + validated on first poll
	txid, err := c.BroadcastTx(signed)
	if err != nil {
		t.Fatal(err)
	}
	if len(txid) != 64 || f.calls["submit"] != 1 || f.calls["tx"] != 1 {
		t.Fatalf("txid %s submit %d tx %d", txid, f.calls["submit"], f.calls["tx"])
	}
	if f.params["tx"][0]["transaction"] != txid {
		t.Fatalf("validated wrong tx %v", f.params["tx"][0])
	}

	// submit rejected
	f.set("submit", map[string]interface{}{"engine_result": "temBAD", "engine_result_message": "bad"})
	if _, err = c.BroadcastTx(signed); err == nil || !strings.Contains(err.Error(), "temBAD") {
		t.Fatalf("expected submit failure, got %v", err)
	}

	// validation fails permanently
	f.set("submit", map[string]interface{}{"engine_result": "tesSUCCESS"})
	f.set("tx", errResCov("invalidParams"))
	if _, err = c.BroadcastTx(signed); err == nil || !strings.Contains(err.Error(), "invalidParams") {
		t.Fatalf("expected validation failure, got %v", err)
	}

	// sign error from account lookup
	f.set("account_info", errResCov("internal"))
	if _, err = c.SignTx(SimTx{ToAddress: common.Address(dest), Coin: common.NewCoin(common.XRPAsset, cosmos.NewUint(100))}); err == nil || !strings.Contains(err.Error(), "fail to get account") {
		t.Fatalf("expected account error, got %v", err)
	}
	// autofill error (bad destination)
	f.set("account_info", map[string]interface{}{"account_data": map[string]interface{}{"Balance": "1", "Sequence": 1}})
	if _, err = c.SignTx(SimTx{ToAddress: common.Address("notanaddress"), Coin: common.NewCoin(common.XRPAsset, cosmos.NewUint(100))}); err == nil {
		t.Fatal("expected autofill error for invalid destination")
	}
}

func TestBroadcastTxDecodeErrorsCov(t *testing.T) {
	c, _, _ := newClientCov(t)
	if _, err := c.BroadcastTx([]byte{0xff}); err == nil {
		t.Fatal("expected decode error")
	}

	// valid blob without LastLedgerSequence
	blob, err := binarycodec.Encode(map[string]any{
		"TransactionType": "Payment",
		"Account":         c.localKm.AccountID,
		"Destination":     "rPEPPER7kfTD9w2To4CQk6UCfuHM9c6GDY",
		"Amount":          "10",
		"Fee":             "10",
		"Sequence":        uint32(1),
		"SigningPubKey":   c.localKm.PublicKeyHex,
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := hex.DecodeString(blob)
	if _, err = c.BroadcastTx(raw); err == nil || !strings.Contains(err.Error(), "missing LastLedgerSequence") {
		t.Fatalf("expected missing LastLedgerSequence, got %v", err)
	}
}

func TestSubmitTxPathsCov(t *testing.T) {
	c, f, _ := newClientCov(t)
	signed, err := c.SignTx(SimTx{ToAddress: common.Address("rPEPPER7kfTD9w2To4CQk6UCfuHM9c6GDY"), Coin: common.NewCoin(common.XRPAsset, cosmos.NewUint(100))})
	if err != nil {
		t.Fatal(err)
	}
	blob := hex.EncodeToString(signed)

	// queued submit is accepted
	f.set("submit", map[string]interface{}{"engine_result": "terQUEUED", "queued": true})
	if err = c.submitTx(blob, "hash", 200); err != nil {
		t.Fatalf("queued: %v", err)
	}

	// non-retryable submit error
	f.set("submit", errResCov("invalidTransaction"))
	if err = c.submitTx(blob, "hash", 200); err == nil || !strings.Contains(err.Error(), "invalidTransaction") {
		t.Fatalf("expected submit error, got %v", err)
	}

	// retryable submit error, tx found anyway
	f.set("submit", errResCov("timeout"))
	f.set("tx", map[string]interface{}{"validated": false})
	if err = c.submitTx(blob, "hash", 200); err != nil {
		t.Fatalf("expected success when tx found, got %v", err)
	}

	// retryable submit error, tx lookup fails permanently
	f.set("tx", errResCov("badSyntax"))
	if err = c.submitTx(blob, "hash", 200); err == nil || !strings.Contains(err.Error(), "badSyntax") {
		t.Fatalf("expected tx lookup error, got %v", err)
	}

	// retryable submit error, tx not found, ledger already past last sequence
	f.set("tx", errResCov("txnNotFound"))
	err = c.submitTx(blob, "hash", 50)
	if err == nil || !strings.Contains(err.Error(), "not accepted before ledger 50") || !strings.Contains(err.Error(), "txnNotFound") {
		t.Fatalf("expected expiry error, got %v", err)
	}

	// ledger lookup fails permanently
	f.set("ledger", errResCov("noCurrent"))
	if err = c.submitTx(blob, "hash", 50); err == nil || !strings.Contains(err.Error(), "noCurrent") {
		t.Fatalf("expected ledger error, got %v", err)
	}
}

func TestWaitForValidationPathsCov(t *testing.T) {
	c, f, _ := newClientCov(t)

	if err := c.waitForValidation("hash", 200); err != nil {
		t.Fatalf("validated: %v", err)
	}

	// not validated and ledger already past
	f.set("tx", map[string]interface{}{"validated": false})
	err := c.waitForValidation("hash", 50)
	if err == nil || !strings.Contains(err.Error(), "not validated before ledger 50") || !strings.Contains(err.Error(), "not yet validated") {
		t.Fatalf("expected expiry error, got %v", err)
	}

	// retryable tx error and ledger past
	f.set("tx", errResCov("txnNotFound"))
	if err = c.waitForValidation("hash", 50); err == nil || !strings.Contains(err.Error(), "txnNotFound") {
		t.Fatalf("expected wrapped txnNotFound, got %v", err)
	}

	// ledger error is returned
	f.set("ledger", errResCov("noNetwork"))
	if err = c.waitForValidation("hash", 50); err == nil || !strings.Contains(err.Error(), "noNetwork") {
		t.Fatalf("expected ledger error, got %v", err)
	}

	// getTx http error (non-200) is not retryable
	f.status["tx"] = http.StatusInternalServerError
	if err = c.waitForValidation("hash", 50); err == nil || !strings.Contains(err.Error(), "http failure") {
		t.Fatalf("expected http error, got %v", err)
	}
}

func TestIsAcceptedNilAndRetryableCov(t *testing.T) {
	if isAcceptedXRPSubmitResponse(nil) {
		t.Fatal("nil response must not be accepted")
	}
	if isRetryableXRPError(nil) || isAccountNotFoundXRPError(nil) {
		t.Fatal("nil errors are not retryable/not-found")
	}
	if !isAccountNotFoundXRPError(errString("Account not found.")) {
		t.Fatal("account not found text should match")
	}
	if !isRetryableXRPError(timeoutErrCov{}) {
		t.Fatal("net timeout should be retryable")
	}
	if !isRetryableXRPError(errString("Transaction not found")) {
		t.Fatal("transaction not found should be retryable")
	}
}

type errString string

func (e errString) Error() string { return string(e) }

type timeoutErrCov struct{}

func (timeoutErrCov) Error() string   { return "net op" }
func (timeoutErrCov) Timeout() bool   { return true }
func (timeoutErrCov) Temporary() bool { return true }
