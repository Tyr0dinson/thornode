package xrp

import (
	"crypto/ecdsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"time"

	"github.com/Peersyst/xrpl-go/xrpl/rpc"
	"github.com/blang/semver"
	cryptocodec "github.com/cosmos/cosmos-sdk/crypto/codec"
	cosmossecp "github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	ethcrypto "github.com/ethereum/go-ethereum/crypto"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	stypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/bifrost/tss"
	"gitlab.com/thorchain/thornode/v3/bifrost/tss/go-tss/blame"
	tsscommon "gitlab.com/thorchain/thornode/v3/bifrost/tss/go-tss/common"
	"gitlab.com/thorchain/thornode/v3/bifrost/tss/go-tss/keysign"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/config"
	"gitlab.com/thorchain/thornode/v3/constants"
	thortypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// -------------------------------------------------------------------------------------
// test doubles
// -------------------------------------------------------------------------------------

// mockXrpBridgeCov implements the bridge calls used by SignTx and the TSS
// key signer; anything else panics on the nil embedded interface.
type mockXrpBridgeCov struct {
	thorclient.ThorchainBridge
	mu          sync.Mutex
	mimir       map[string]int64
	blamePosted []thortypes.Blame
}

func (m *mockXrpBridgeCov) GetMimir(key string) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.mimir[key], nil
}

func (m *mockXrpBridgeCov) GetKeysignPolicy(pk common.PubKey, height int64) (thorclient.KeysignPolicy, error) {
	return thorclient.KeysignPolicy{PubKey: pk, Height: height}, nil
}

func (m *mockXrpBridgeCov) GetThorchainVersion() (semver.Version, error) {
	return semver.MustParse("3.0.0"), nil
}

func (m *mockXrpBridgeCov) PostKeysignFailure(b thortypes.Blame, _ int64, _ string, _ common.Coins, _ common.PubKey) (common.TxID, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.blamePosted = append(m.blamePosted, b)
	return common.TxID("BLAMETX"), nil
}

type tssModeCov int

const (
	tssSignCov tssModeCov = iota
	tssBlameCov
	tssNotSelectedCov
	tssErrorCov
)

// fakeTssServerCov signs every requested message with the private key that
// belongs to the requested pool, or fails / stands down according to mode.
type fakeTssServerCov struct {
	mu    sync.Mutex
	keys  map[string]*ecdsa.PrivateKey
	modes map[string]tssModeCov
}

func (f *fakeTssServerCov) KeySign(req keysign.Request) (keysign.Response, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch f.modes[req.PoolPubKey] {
	case tssBlameCov:
		return keysign.Response{
			Status: tsscommon.Fail,
			Blame: blame.Blame{
				FailReason: "timeout",
				BlameNodes: []blame.Node{{Pubkey: "culprit"}},
			},
		}, nil
	case tssNotSelectedCov:
		return keysign.Response{}, nil
	case tssErrorCov:
		return keysign.Response{}, errors.New("tss down")
	}
	priv := f.keys[req.PoolPubKey]
	sigs := make([]keysign.Signature, 0, len(req.Messages))
	for _, msg := range req.Messages {
		hash, err := base64.StdEncoding.DecodeString(msg)
		if err != nil {
			return keysign.Response{}, err
		}
		sig, err := ethcrypto.Sign(hash, priv)
		if err != nil {
			return keysign.Response{}, err
		}
		sigs = append(sigs, keysign.NewSignature(msg,
			base64.StdEncoding.EncodeToString(sig[:32]),
			base64.StdEncoding.EncodeToString(sig[32:64]),
			base64.StdEncoding.EncodeToString(sig[64:])))
	}
	return keysign.Response{Signatures: sigs, Status: tsscommon.Success}, nil
}

// tssVaultCov derives a deterministic secp256k1 vault key from a seed byte.
func tssVaultCov(c *C, seed byte) (common.PubKey, *ecdsa.PrivateKey) {
	privBytes := make([]byte, 32)
	for i := range privBytes {
		privBytes[i] = seed
	}
	priv, err := ethcrypto.ToECDSA(privBytes)
	c.Assert(err, IsNil)
	cpk := &cosmossecp.PubKey{Key: ethcrypto.CompressPubkey(&priv.PublicKey)}
	tmpk, err := cryptocodec.ToCmtPubKeyInterface(cpk)
	c.Assert(err, IsNil)
	pk, err := common.NewPubKeyFromCrypto(tmpk)
	c.Assert(err, IsNil)
	return pk, priv
}

// xrpRPCCov is a JSON-RPC server answering per method.
type xrpRPCCov struct {
	mu        sync.Mutex
	responses map[string]string
	status    map[string]int
	calls     []string
}

func (x *xrpRPCCov) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	var req struct {
		Method string `json:"method"`
	}
	_ = json.Unmarshal(body, &req)
	x.mu.Lock()
	x.calls = append(x.calls, req.Method)
	resp, ok := x.responses[req.Method]
	code := x.status[req.Method]
	x.mu.Unlock()
	if !ok {
		resp = `{"result":{"ledger_current_index":50,"status":"success"}}`
	}
	if code == 0 {
		code = http.StatusOK
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_, _ = io.WriteString(w, resp)
}

func (x *xrpRPCCov) count(method string) int {
	x.mu.Lock()
	defer x.mu.Unlock()
	n := 0
	for _, m := range x.calls {
		if m == method {
			n++
		}
	}
	return n
}

func (x *xrpRPCCov) set(method, resp string, code int) {
	x.mu.Lock()
	defer x.mu.Unlock()
	x.responses[method] = resp
	x.status[method] = code
}

func (s *XrpTestSuite) newCovClient(c *C, bridge thorclient.ThorchainBridge) (*Client, *xrpRPCCov, func()) {
	handler := &xrpRPCCov{responses: map[string]string{}, status: map[string]int{}}
	server := httptest.NewServer(handler)
	client, err := NewClient(s.thorKeys, config.BifrostChainConfiguration{
		ChainID:      common.XRPChain,
		ChainNetwork: "1234",
		RPCHost:      server.URL + "/",
		BlockScanner: config.BifrostBlockScannerConfiguration{
			ChainID:          common.XRPChain,
			StartBlockHeight: 1,
		},
	}, nil, bridge, s.m)
	c.Assert(err, IsNil)
	rpcCfg, err := rpc.NewClientConfig(server.URL + "/")
	c.Assert(err, IsNil)
	client.rpcClient = rpc.NewClient(rpcCfg)
	client.xrpScanner.rpcClient = rpc.NewClient(rpcCfg)
	return client, handler, server.Close
}

func (s *XrpTestSuite) localVaultCov(c *C) common.PubKey {
	priv, err := s.thorKeys.GetPrivateKey()
	c.Assert(err, IsNil)
	temp, err := cryptocodec.ToCmtPubKeyInterface(priv.PubKey())
	c.Assert(err, IsNil)
	pk, err := common.NewPubKeyFromCrypto(temp)
	c.Assert(err, IsNil)
	return pk
}

func txOutCov(c *C, vault common.PubKey, inHash string) stypes.TxOutItem {
	toAddress, err := common.NewAddress("rQwpQ54X5gJyLGg4QGp3HSkjdf3u37NqiZ")
	c.Assert(err, IsNil)
	return stypes.TxOutItem{
		Chain:       common.XRPChain,
		ToAddress:   toAddress,
		VaultPubKey: vault,
		Coins:       common.Coins{common.NewCoin(common.XRPAsset, cosmos.NewUint(2452835200))},
		Memo:        "OUT:" + inHash,
		MaxGas:      common.Gas{common.NewCoin(common.XRPAsset, cosmos.NewUint(23582400))},
		GasRate:     12,
		InHash:      common.TxID(inHash),
		Checkpoint:  []byte(`{"SeqNumber":7,"BlockHeight":10}`),
	}
}

// -------------------------------------------------------------------------------------
// tests
// -------------------------------------------------------------------------------------

func (s *XrpTestSuite) TestOnObservedTxInRecordsOutboundCov(c *C) {
	client, _, done := s.newCovClient(c, &mockXrpBridgeCov{})
	defer done()
	vault := s.localVaultCov(c)
	inHash := strings.Repeat("AB", 32)

	txIn := stypes.TxInItem{
		Tx:                  "OUTBOUNDTX",
		Memo:                "OUT:" + inHash,
		ObservedVaultPubKey: vault,
	}
	cacheHash := txIn.CacheHash(common.XRPChain, inHash)
	c.Assert(client.signerCacheManager.HasSigned(cacheHash), Equals, false)

	client.OnObservedTxIn(txIn, 10)
	c.Check(client.signerCacheManager.HasSigned(cacheHash), Equals, true)

	// The inbound is recorded as the vault's latest observed tx; nothing has
	// been broadcast yet so that lookup errors.
	observed, broadcast, err := client.GetLatestTxForVault(vault.String())
	c.Check(err, NotNil)
	c.Check(observed, Equals, "OUTBOUNDTX")
	c.Check(broadcast, Equals, "")

	// Non-outbound memos are ignored.
	other := stypes.TxInItem{Tx: "OTHER", Memo: "ADD:XRP.XRP", ObservedVaultPubKey: vault}
	client.OnObservedTxIn(other, 10)
	c.Check(client.signerCacheManager.HasSigned(other.CacheHash(common.XRPChain, "")), Equals, false)
}

func (s *XrpTestSuite) TestGetAddressInvalidPubKeyCov(c *C) {
	client := &Client{cfg: config.BifrostChainConfiguration{ChainID: common.XRPChain}}
	c.Check(client.GetAddress(common.PubKey("not-a-pubkey")), Equals, "")
}

func (s *XrpTestSuite) TestBroadcastTxPathsCov(c *C) {
	client, rpcSrv, done := s.newCovClient(c, &mockXrpBridgeCov{})
	defer done()
	vault := s.localVaultCov(c)
	txOut := txOutCov(c, vault, strings.Repeat("CD", 32))

	signed, _, txIn, err := client.SignTx(txOut, 100)
	c.Assert(err, IsNil)
	c.Assert(signed, NotNil)
	c.Assert(txIn, NotNil)
	client.accts.Set(vault, XrpMetadata{SeqNumber: 7, BlockHeight: 10})

	// Garbage bytes can't be hashed into an XRP tx id.
	_, err = client.BroadcastTx(txOut, []byte{0x01})
	c.Assert(err, NotNil)

	// Not on chain yet and the node rejects it.
	rpcSrv.set("tx", `{"result":{"error":"txnNotFound","status":"error"}}`, 0)
	rpcSrv.set("submit", `{"result":{"engine_result":"tecNO_DST_INSUF_XRP","engine_result_code":125,"engine_result_message":"no","tx_blob":"","tx_json":{}}}`, 0)
	_, err = client.BroadcastTx(txOut, signed)
	c.Assert(err, NotNil)
	c.Check(err.Error(), Matches, ".*tecNO_DST_INSUF_XRP.*")
	c.Check(client.signerCacheManager.HasSigned(txOut.CacheHash()), Equals, false)
	c.Check(client.accts.Get(vault).SeqNumber, Equals, int64(7))

	// Transport failure on submit.
	rpcSrv.set("submit", `oops`, http.StatusInternalServerError)
	_, err = client.BroadcastTx(txOut, signed)
	c.Assert(err, NotNil)
	c.Check(err.Error(), Matches, "broadcast msg failed.*")

	// Another validator already got it in: tefPAST_SEQ counts as success.
	rpcSrv.set("submit", `{"result":{"engine_result":"tefPAST_SEQ","engine_result_code":-190,"engine_result_message":"past","tx_blob":"","tx_json":{}}}`, 0)
	hash, err := client.BroadcastTx(txOut, signed)
	c.Assert(err, IsNil)
	c.Check(hash, Equals, txIn.Tx)
	c.Check(client.signerCacheManager.HasSigned(txOut.CacheHash()), Equals, true)
	c.Check(client.accts.Get(vault).SeqNumber, Equals, int64(8))

	// Fresh submit succeeds.
	rpcSrv.set("submit", `{"result":{"engine_result":"tesSUCCESS","engine_result_code":0,"engine_result_message":"ok","tx_blob":"","tx_json":{}}}`, 0)
	before := rpcSrv.count("submit")
	hash, err = client.BroadcastTx(txOut, signed)
	c.Assert(err, IsNil)
	c.Check(hash, Equals, txIn.Tx)
	c.Check(rpcSrv.count("submit"), Equals, before+1)

	// Already validated on chain: no resubmission.
	rpcSrv.set("tx", `{"result":{"validated":true,"hash":"`+txIn.Tx+`"}}`, 0)
	before = rpcSrv.count("submit")
	hash, err = client.BroadcastTx(txOut, signed)
	c.Assert(err, IsNil)
	c.Check(hash, Equals, txIn.Tx)
	c.Check(rpcSrv.count("submit"), Equals, before)

	// An unparsable tx lookup is treated as "needs broadcast".
	rpcSrv.set("tx", `{"result":{"validated":"maybe"}}`, 0)
	c.Check(client.txNeedsBroadcast(txIn.Tx), Equals, true)
}

func (s *XrpTestSuite) TestSignTxExpiredCacheGateCov(c *C) {
	bridge := &mockXrpBridgeCov{mimir: map[string]int64{}}
	client, rpcSrv, done := s.newCovClient(c, bridge)
	defer done()
	vault := s.localVaultCov(c)
	txOut := txOutCov(c, vault, strings.Repeat("EF", 32))

	expired := time.Now().Add(-time.Hour).UnixMilli()
	c.Assert(client.signerCacheManager.SetSignedWithExpiry(txOut.CacheHash(),
		txOut.CacheVault(common.XRPChain), "SIG1", expired), IsNil)

	// Gate off: an expired entry still blocks re-signing.
	signed, _, _, err := client.SignTx(txOut, 100)
	c.Assert(err, IsNil)
	c.Check(signed, IsNil)
	c.Check(client.signerCacheManager.HasSigned(txOut.CacheHash()), Equals, true)

	// Gate on, tx validated on chain: still skip.
	bridge.mimir[fmt.Sprintf(constants.MimirTemplateSkipSignerCache, common.XRPChain)] = 1
	rpcSrv.set("tx", `{"result":{"validated":true,"hash":"SIG1"}}`, 0)
	signed, _, _, err = client.SignTx(txOut, 100)
	c.Assert(err, IsNil)
	c.Check(signed, IsNil)
	c.Check(client.signerCacheManager.HasSigned(txOut.CacheHash()), Equals, true)
}

func (s *XrpTestSuite) TestSignTxRemoteTssCov(c *C) {
	bridge := &mockXrpBridgeCov{}
	client, _, done := s.newCovClient(c, bridge)
	defer done()

	signVault, signPriv := tssVaultCov(c, 0x11)
	blameVault, _ := tssVaultCov(c, 0x22)
	idleVault, _ := tssVaultCov(c, 0x33)
	errVault, _ := tssVaultCov(c, 0x44)
	server := &fakeTssServerCov{
		keys: map[string]*ecdsa.PrivateKey{signVault.String(): signPriv},
		modes: map[string]tssModeCov{
			signVault.String():  tssSignCov,
			blameVault.String(): tssBlameCov,
			idleVault.String():  tssNotSelectedCov,
			errVault.String():   tssErrorCov,
		},
	}
	km, err := tss.NewKeySign(server, bridge)
	c.Assert(err, IsNil)
	km.Start()
	defer km.Stop()
	client.tssKeyManager = km

	type result struct {
		signed, checkpoint []byte
		txIn               *stypes.TxInItem
		err                error
	}
	vaults := []common.PubKey{signVault, blameVault, idleVault, errVault}
	results := make([]result, len(vaults))
	// Sign every vault concurrently: their keysign batches run in parallel
	// goroutines, which exercises KeySign's shared version cache under -race.
	var wg sync.WaitGroup
	for i, v := range vaults {
		wg.Add(1)
		go func(i int, v common.PubKey) {
			defer wg.Done()
			r := &results[i]
			r.signed, r.checkpoint, r.txIn, r.err = client.SignTx(
				txOutCov(c, v, strings.Repeat(fmt.Sprintf("%02X", i+1), 32)), 100)
		}(i, v)
	}
	wg.Wait()

	// TSS signature is converted to DER, verified and the tx encoded.
	ok := results[0]
	c.Assert(ok.err, IsNil)
	c.Check(len(ok.signed) > 0, Equals, true)
	c.Assert(ok.txIn, NotNil)
	addr, err := signVault.GetAddress(common.XRPChain)
	c.Assert(err, IsNil)
	c.Check(ok.txIn.Sender, Equals, addr.String())
	c.Check(ok.txIn.Tx, Not(Equals), "")

	// Keysign blame is forwarded to THORChain; once posted, SignTx reports no
	// error but hands back the checkpoint so the retry reuses the sequence.
	blamed := results[1]
	c.Check(blamed.err, IsNil)
	c.Check(blamed.signed, IsNil)
	c.Check(string(blamed.checkpoint), Equals, `{"SeqNumber":7,"BlockHeight":10}`)
	bridge.mu.Lock()
	c.Assert(bridge.blamePosted, HasLen, 1)
	c.Check(bridge.blamePosted[0].FailReason, Equals, "timeout")
	bridge.mu.Unlock()

	// Not selected for the keysign party: stand down silently.
	idle := results[2]
	c.Check(idle.err, IsNil)
	c.Check(idle.signed, IsNil)
	c.Check(idle.checkpoint, IsNil)

	// TSS transport failure surfaces as a sign error with the checkpoint.
	failed := results[3]
	c.Assert(failed.err, NotNil)
	c.Check(failed.err.Error(), Matches, ".*xrp remote sign.*tss down.*")
	c.Check(failed.checkpoint, NotNil)
}
