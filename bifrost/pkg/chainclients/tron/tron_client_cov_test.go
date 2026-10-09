package tron

import (
	"crypto/ecdsa"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"time"

	"github.com/blang/semver"
	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	cryptocodec "github.com/cosmos/cosmos-sdk/crypto/codec"
	"github.com/cosmos/cosmos-sdk/crypto/hd"
	"github.com/cosmos/cosmos-sdk/crypto/keyring"
	cosmossecp "github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	"github.com/ethereum/go-ethereum/crypto"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/tron/api"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/tron/rpc"
	"gitlab.com/thorchain/thornode/v3/bifrost/pubkeymanager"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/bifrost/tss"
	"gitlab.com/thorchain/thornode/v3/bifrost/tss/go-tss/blame"
	tsscommon "gitlab.com/thorchain/thornode/v3/bifrost/tss/go-tss/common"
	"gitlab.com/thorchain/thornode/v3/bifrost/tss/go-tss/keysign"
	"gitlab.com/thorchain/thornode/v3/cmd"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/common/tokenlist"
	"gitlab.com/thorchain/thornode/v3/config"
	thortypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// -------------------------------------------------------------------------------------
// test doubles
// -------------------------------------------------------------------------------------

type tronCovBridge struct {
	thorclient.ThorchainBridge
	mu              sync.Mutex
	mimir           int64
	lastObservedErr error
	asgards         thortypes.Vaults
	asgardErr       error
	asgardCalls     int
	timestampErr    error
	posted          []thortypes.Blame
}

func (b *tronCovBridge) GetMimir(string) (int64, error) { return b.mimir, nil }

func (b *tronCovBridge) GetBlockTimestamp(int64) (time.Time, error) {
	return time.UnixMilli(1749121581000), b.timestampErr
}

func (b *tronCovBridge) GetLastObservedInHeight(common.Chain) (int64, error) {
	return 0, b.lastObservedErr
}

func (b *tronCovBridge) GetAsgards() (thortypes.Vaults, error) {
	b.asgardCalls++
	return b.asgards, b.asgardErr
}

func (b *tronCovBridge) GetKeysignPolicy(pk common.PubKey, height int64) (thorclient.KeysignPolicy, error) {
	return thorclient.KeysignPolicy{PubKey: pk, Height: height}, nil
}

func (b *tronCovBridge) GetThorchainVersion() (semver.Version, error) {
	return semver.MustParse("3.0.0"), nil
}

func (b *tronCovBridge) PostKeysignFailure(bl thortypes.Blame, _ int64, _ string, _ common.Coins, _ common.PubKey) (common.TxID, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.posted = append(b.posted, bl)
	if bl.FailReason == "post-fails" {
		return "", errors.New("thornode rejected blame")
	}
	return common.TxID("BLAMETX"), nil
}

type tronTssModeCov int

const (
	tronTssSign tronTssModeCov = iota
	tronTssNotSelected
	tronTssBlame
	tronTssBlamePostFails
	tronTssNoCulprit
	tronTssError
)

type tronFakeTssCov struct {
	keys  map[string]*ecdsa.PrivateKey
	modes map[string]tronTssModeCov
}

func (f *tronFakeTssCov) KeySign(req keysign.Request) (keysign.Response, error) {
	culprit := []blame.Node{{Pubkey: "culprit"}}
	switch f.modes[req.PoolPubKey] {
	case tronTssNotSelected:
		return keysign.Response{}, nil
	case tronTssBlame:
		return keysign.Response{Status: tsscommon.Fail, Blame: blame.Blame{FailReason: "timeout", BlameNodes: culprit}}, nil
	case tronTssBlamePostFails:
		return keysign.Response{Status: tsscommon.Fail, Blame: blame.Blame{FailReason: "post-fails", BlameNodes: culprit}}, nil
	case tronTssNoCulprit:
		return keysign.Response{Status: tsscommon.Fail, Blame: blame.Blame{FailReason: "unknown"}}, nil
	case tronTssError:
		return keysign.Response{}, errors.New("tss down")
	}
	sigs := make([]keysign.Signature, 0, len(req.Messages))
	for _, msg := range req.Messages {
		hash, err := base64.StdEncoding.DecodeString(msg)
		if err != nil {
			return keysign.Response{}, err
		}
		sig, err := crypto.Sign(hash, f.keys[req.PoolPubKey])
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

func tronVaultCov(c *C, seed byte) (common.PubKey, *ecdsa.PrivateKey) {
	privBytes := make([]byte, 32)
	for i := range privBytes {
		privBytes[i] = seed
	}
	priv, err := crypto.ToECDSA(privBytes)
	c.Assert(err, IsNil)
	tmpk, err := cryptocodec.ToCmtPubKeyInterface(&cosmossecp.PubKey{Key: crypto.CompressPubkey(&priv.PublicKey)})
	c.Assert(err, IsNil)
	pk, err := common.NewPubKeyFromCrypto(tmpk)
	c.Assert(err, IsNil)
	return pk, priv
}

func tronKeysCov(c *C, create bool) *thorclient.Keys {
	registry := codectypes.NewInterfaceRegistry()
	cryptocodec.RegisterInterfaces(registry)
	kb := keyring.NewInMemory(codec.NewProtoCodec(registry))
	if create {
		_, _, err := kb.NewMnemonic("bob", keyring.English, cmd.THORChainHDPath, "password", hd.Secp256k1)
		c.Assert(err, IsNil)
	}
	return thorclient.NewKeysWithKeybase(kb, "bob", "password")
}

func staticTronServerCov(status int, body string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
}

func (s *TronTestSuite) tronCovConfig(startHeight int64) config.BifrostChainConfiguration {
	return config.BifrostChainConfiguration{
		ChainID: common.TRONChain,
		APIHost: s.api.URL,
		RPCHost: s.rpc.URL,
		BlockScanner: config.BifrostBlockScannerConfiguration{
			ChainID:            common.TRONChain,
			StartBlockHeight:   startHeight,
			HTTPRequestTimeout: 2 * time.Second,
		},
	}
}

func (s *TronTestSuite) newTronCovClient(c *C, bridge *tronCovBridge, startHeight int64) *TronClient {
	client, err := NewTronClient(tronKeysCov(c, true), s.tronCovConfig(startHeight), nil,
		bridge, s.metrics, pubkeymanager.NewMockPoolAddressValidator(), nil)
	c.Assert(err, IsNil)
	client.tronScanner.refBlocks = []RefBlock{{
		Timestamp: 1749121581000,
		Height:    55088560,
		Id:        "00000000034895b0e5029588a71f9689fb5c31e2c66989389d6ff09548726f6e",
	}}
	client.whitelist = map[string]tokenlist.ERC20Token{
		"TG3XXyExBkPp9nzdajDZsozEu4BkaSJozs": {
			Symbol:   "USDT",
			Address:  "TG3XXyExBkPp9nzdajDZsozEu4BkaSJozs",
			Decimals: 6,
		},
	}
	return client
}

func trxTxOutCov(vault common.PubKey, amount uint64) types.TxOutItem {
	return types.TxOutItem{
		Chain:       common.TRONChain,
		ToAddress:   "TU6nEM4GTca2L5AuDTnY1qp1rkQ2t8NxvM",
		VaultPubKey: vault,
		Coins:       common.Coins{common.NewCoin(common.TRXAsset, cosmos.NewUint(amount))},
		MaxGas:      common.Gas{common.NewCoin(common.TRXAsset, cosmos.NewUint(1_100_000))},
		GasRate:     1,
		Height:      1,
	}
}

// -------------------------------------------------------------------------------------
// tests
// -------------------------------------------------------------------------------------

func (s *TronTestSuite) TestNewTronClientCov(c *C) {
	if notMocknetCov() {
		c.Skip("mocknet only: reference-block settings are mocknet values")
	}
	bridge := &tronCovBridge{}
	pkMgr := pubkeymanager.NewMockPoolAddressValidator()

	_, err := NewTronClient(tronKeysCov(c, true), s.tronCovConfig(1), nil, bridge, s.metrics, nil, nil)
	c.Assert(err, ErrorMatches, "pubkey manager is nil")

	_, err = NewTronClient(tronKeysCov(c, false), s.tronCovConfig(1), nil, bridge, s.metrics, pkMgr, nil)
	c.Assert(err, ErrorMatches, "fail to get private key.*")

	cfg := s.tronCovConfig(1)
	cfg.BlockScanner.WhitelistTokens = []string{"TXYZopYRdj2D9XRtbG411XZZ3kM5VkAeBf"}
	_, err = NewTronClient(tronKeysCov(c, true), cfg, nil, bridge, s.metrics, pkMgr, nil)
	c.Assert(err, ErrorMatches, "reference address is empty")

	_, err = NewTronClient(tronKeysCov(c, true), s.tronCovConfig(1), nil, bridge, nil, pkMgr, nil)
	c.Assert(err, ErrorMatches, "metrics instance is nil")

	// Whitelisted tokens are resolved from the chain's token list.
	cfg.BlockScanner.ReferenceAddress = "TU6nEM4GTca2L5AuDTnY1qp1rkQ2t8NxvM"
	cfg.BlockScanner.StartBlockHeight = 7
	cfg.BlockScanner.DBPath = c.MkDir()
	client, err := NewTronClient(tronKeysCov(c, true), cfg, nil, bridge, s.metrics, pkMgr, nil)
	c.Assert(err, IsNil)
	c.Check(client.GetChain(), Equals, common.TRONChain)
	c.Assert(client.whitelist, HasLen, 1)
	c.Check(client.whitelist["TXYZopYRdj2D9XRtbG411XZZ3kM5VkAeBf"].Address, Equals, "TXYZopYRdj2D9XRtbG411XZZ3kM5VkAeBf")
	c.Check(client.IsBlockScannerHealthy(), Equals, false)
	height, err := client.GetBlockScannerHeight()
	c.Assert(err, IsNil)
	c.Check(height, Equals, int64(7))
}

func (s *TronTestSuite) TestRollbackAndVaultTxsCov(c *C) {
	bridge := &tronCovBridge{lastObservedErr: errors.New("thornode down")}
	client := s.newTronCovClient(c, bridge, 1)

	c.Check(client.RollbackBlockScanner(), ErrorMatches, ".*thornode down.*")
	bridge.lastObservedErr = nil
	c.Check(client.RollbackBlockScanner(), IsNil)

	vault := client.localKeyManager.Pubkey().String()
	_, _, err := client.GetLatestTxForVault(vault)
	c.Check(err, NotNil)

	chain := common.TRONChain.String()
	c.Assert(client.signerCacheManager.SetSigned("in", types.InboundCacheKey(vault, chain), "OBSERVED"), IsNil)
	c.Assert(client.signerCacheManager.SetSigned("out", types.BroadcastCacheKey(vault, chain), "BROADCAST"), IsNil)

	// OnObservedTxIn is a no-op for TRON: it must not touch the signer cache.
	client.OnObservedTxIn(types.TxInItem{Tx: "OTHER", Memo: "OUT:" + strings.Repeat("A", 64)}, 1)

	observed, broadcast, err := client.GetLatestTxForVault(vault)
	c.Assert(err, IsNil)
	c.Check(observed, Equals, "OBSERVED")
	c.Check(broadcast, Equals, "BROADCAST")
}

func (s *TronTestSuite) TestIsTronNotFoundErrCov(c *C) {
	c.Check(isTronNotFoundErr(nil), Equals, false)
	c.Check(isTronNotFoundErr(errors.New("failed to call x: response status: 404")), Equals, true)
	c.Check(isTronNotFoundErr(errors.New("failed to call x: response status: 500")), Equals, false)
}

func (s *TronTestSuite) TestRemoteSignCov(c *C) {
	bridge := &tronCovBridge{}
	client := s.newTronCovClient(c, bridge, 1)

	modes := []tronTssModeCov{
		tronTssSign, tronTssNotSelected, tronTssBlame,
		tronTssBlamePostFails, tronTssNoCulprit, tronTssError,
	}
	server := &tronFakeTssCov{keys: map[string]*ecdsa.PrivateKey{}, modes: map[string]tronTssModeCov{}}
	vaults := make([]common.PubKey, len(modes))
	for i, mode := range modes {
		pk, priv := tronVaultCov(c, byte(0x10+i))
		vaults[i] = pk
		server.keys[pk.String()] = priv
		server.modes[pk.String()] = mode
	}
	km, err := tss.NewKeySign(server, bridge)
	c.Assert(err, IsNil)
	km.Start()
	defer km.Stop()
	client.tssKeyManager = km

	hash := sha256.Sum256([]byte("tron tx"))
	type result struct {
		sig []byte
		err error
	}
	results := make([]result, len(modes))
	// Sign every vault concurrently: their keysign batches run in parallel
	// goroutines, which exercises KeySign's shared version cache under -race.
	var wg sync.WaitGroup
	for i := range modes {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i].sig, results[i].err = client.sign(hash[:], trxTxOutCov(vaults[i], 100), 10)
		}(i)
	}
	wg.Wait()

	// Successful TSS signature: R || S || V that recovers to the vault key.
	c.Assert(results[0].err, IsNil)
	c.Assert(results[0].sig, HasLen, 65)
	recovered, err := crypto.SigToPub(hash[:], results[0].sig)
	c.Assert(err, IsNil)
	c.Check(recovered.Equal(&server.keys[vaults[0].String()].PublicKey), Equals, true)

	c.Check(errors.Is(results[1].err, tss.ErrNotSelectedForKeysign), Equals, true)
	c.Check(results[1].sig, IsNil)

	// Blame is forwarded to THORChain and the keysign error still returned.
	var ksErr tss.KeysignError
	c.Assert(errors.As(results[2].err, &ksErr), Equals, true)
	c.Check(ksErr.Blame.FailReason, Equals, "timeout")

	// Failing to post blame surfaces both errors.
	c.Assert(results[3].err, NotNil)
	c.Check(results[3].err.Error(), Matches, "(?s).*thornode rejected blame.*")

	// No culprit to blame: error returned without posting.
	c.Assert(errors.As(results[4].err, &ksErr), Equals, true)
	c.Check(ksErr.Blame.BlameNodes, HasLen, 0)

	c.Check(results[5].err, ErrorMatches, ".*tss down.*")

	bridge.mu.Lock()
	defer bridge.mu.Unlock()
	c.Assert(bridge.posted, HasLen, 2)
	reasons := []string{bridge.posted[0].FailReason, bridge.posted[1].FailReason}
	c.Check(strings.Join(reasons, ","), Matches, "(timeout,post-fails|post-fails,timeout)")
}

func (s *TronTestSuite) TestLocalSignErrorsCov(c *C) {
	client := s.newTronCovClient(c, &tronCovBridge{}, 1)
	local := client.localKeyManager.Pubkey()

	// The message must be a 32 byte digest.
	_, err := client.sign([]byte("short"), trxTxOutCov(local, 100), 1)
	c.Check(err, NotNil)

	// A key manager whose private key doesn't match its advertised pubkey
	// produces signatures that fail verification.
	_, otherPriv := tronVaultCov(c, 0x77)
	client.localKeyManager = &KeyManager{priv: otherPriv, pub: local}
	hash := sha256.Sum256([]byte("x"))
	_, err = client.sign(hash[:], trxTxOutCov(local, 100), 1)
	c.Check(err, ErrorMatches, "unable to verify signature with secp pub key")

	_, err = NewLocalKeyManager(tronKeysCov(c, false))
	c.Check(err, ErrorMatches, "fail to get private key.*")
}

func (s *TronTestSuite) TestSignTronTransactionErrorsCov(c *C) {
	client := s.newTronCovClient(c, &tronCovBridge{}, 1)
	txOut := trxTxOutCov(client.localKeyManager.Pubkey(), 100)
	checkpoint := []byte("checkpoint")
	fakeSig := func([]byte, types.TxOutItem, int64) ([]byte, error) { return make([]byte, 65), nil }

	signed, cp, txIn, err := client.signTronTransaction(txOut, 1, api.Transaction{TxId: "zz"}, checkpoint, fakeSig)
	c.Check(err, NotNil)
	c.Check(signed, IsNil)
	c.Check(cp, DeepEquals, checkpoint)
	c.Check(txIn, IsNil)

	// The node can't report its height after signing.
	down := staticTronServerCov(http.StatusInternalServerError, "")
	defer down.Close()
	client.tronScanner.api = api.NewTronApi(down.URL, time.Second)
	signed, cp, txIn, err = client.signTronTransaction(txOut, 1, api.Transaction{TxId: "abcd"}, checkpoint, fakeSig)
	c.Check(err, ErrorMatches, ".*response status: 500")
	c.Check(signed, IsNil)
	c.Check(cp, DeepEquals, checkpoint)
	c.Check(txIn, IsNil)
}

func (s *TronTestSuite) TestGetTokenBalanceErrorsCov(c *C) {
	client := s.newTronCovClient(c, &tronCovBridge{}, 1)
	contract := "TG3XXyExBkPp9nzdajDZsozEu4BkaSJozs"
	owner := "TU6nEM4GTca2L5AuDTnY1qp1rkQ2t8NxvM"

	_, err := client.getTokenBalance("bc1qelysxslyl86yq57yx9lzhl50pfayj9wng54p3g", contract)
	c.Check(err, ErrorMatches, "address not valid")

	cases := []struct {
		status int
		body   string
		errRe  string
	}{
		{http.StatusInternalServerError, "", ".*response status: 500"},
		{http.StatusOK, "not json", ".*invalid character.*"},
		{http.StatusOK, `{"result":""}`, "response result is empty"},
		{http.StatusOK, `{"result":"0xzz"}`, ".*invalid byte.*"},
		{http.StatusOK, `{"result":"0x01"}`, ".*"},
	}
	for _, tc := range cases {
		srv := staticTronServerCov(tc.status, tc.body)
		client.rpc = rpc.NewTronRpc(srv.URL, time.Second)
		_, err = client.getTokenBalance(owner, contract)
		c.Check(err, ErrorMatches, tc.errRe, Commentf("body %q", tc.body))

		// The account lookup surfaces token balance failures.
		account, accErr := client.GetAccountByAddress(owner, nil)
		c.Check(accErr, NotNil)
		c.Check(account.Coins.GetCoin(common.TRXAsset).Amount.String(), Equals, "212331370000")
		srv.Close()
	}
}

func (s *TronTestSuite) TestSignTxErrorPathsCov(c *C) {
	bridge := &tronCovBridge{}
	client := s.newTronCovClient(c, bridge, 1)
	vault := client.localKeyManager.Pubkey()

	two := trxTxOutCov(vault, 100)
	two.Coins = append(two.Coins, common.NewCoin(common.TRXAsset, cosmos.NewUint(1)))
	_, _, _, err := client.SignTx(two, 1)
	c.Check(err, ErrorMatches, "multiple or no coins found")

	empty := trxTxOutCov(vault, 0)
	_, _, _, err = client.SignTx(empty, 1)
	c.Check(err, ErrorMatches, "coin is empty")

	notListed := trxTxOutCov(vault, 100)
	notListed.Coins = common.Coins{common.NewCoin(common.Asset{
		Chain: common.TRONChain, Symbol: "FOO-TXYZ", Ticker: "FOO",
	}, cosmos.NewUint(100))}
	_, _, _, err = client.SignTx(notListed, 1)
	c.Check(err, ErrorMatches, "token not whitelisted")

	badTo := s.checkpointTestTxOut(100_000_000)
	badTo.VaultPubKey = vault
	badTo.ToAddress = "bc1qelysxslyl86yq57yx9lzhl50pfayj9wng54p3g"
	_, _, _, err = client.SignTx(badTo, 1)
	c.Check(err, ErrorMatches, "address not valid")

	bridge.timestampErr = errors.New("no timestamp")
	_, _, _, err = client.SignTx(trxTxOutCov(vault, 1_000_000), 1)
	c.Check(err, ErrorMatches, "no timestamp")
	bridge.timestampErr = nil

	saved := client.tronScanner.refBlocks
	client.tronScanner.refBlocks = nil
	_, _, _, err = client.SignTx(trxTxOutCov(vault, 1_000_000), 1)
	c.Check(err, ErrorMatches, "no ref blocks found")
	client.tronScanner.refBlocks = saved

	// The node 404s everything: an expired cache entry whose broadcast is
	// definitively unknown is cleared, then building the transfer fails.
	notFound := staticTronServerCov(http.StatusNotFound, "")
	defer notFound.Close()
	client.api = api.NewTronApi(notFound.URL, time.Second)
	bridge.mimir = 1
	txOut := trxTxOutCov(vault, 2_000_000)
	c.Assert(client.signerCacheManager.SetSignedWithExpiry(txOut.CacheHash(),
		txOut.CacheVault(common.TRONChain), "GONE", time.Now().Add(-time.Hour).UnixMilli()), IsNil)
	_, _, _, err = client.SignTx(txOut, 1)
	c.Check(err, ErrorMatches, ".*createtransaction.*404")
	c.Check(client.signerCacheManager.HasSigned(txOut.CacheHash()), Equals, false)
}

func (s *TronTestSuite) TestReportSolvencyCov(c *C) {
	bridge := &tronCovBridge{}
	// previous height 9 -> next scanned block is 10
	client := s.newTronCovClient(c, bridge, 9)
	client.whitelist = nil
	queue := make(chan types.Solvency, 4)
	client.globalSolvencyQueue = queue

	c.Check(client.ReportSolvency(15), IsNil)
	c.Check(client.ReportSolvency(10), IsNil)
	c.Check(bridge.asgardCalls, Equals, 0)

	bridge.asgardErr = errors.New("no asgards")
	c.Check(client.ReportSolvency(20), ErrorMatches, "no asgards")
	bridge.asgardErr = nil

	// A vault whose account can't be resolved is skipped: nothing reported.
	bridge.asgards = thortypes.Vaults{{
		PubKey: common.PubKey("bogus"),
		Coins:  common.Coins{common.NewCoin(common.TRXAsset, cosmos.NewUint(1))},
	}}
	c.Check(client.ReportSolvency(20), IsNil)
	c.Check(len(queue), Equals, 0)
	c.Check(bridge.asgardCalls, Equals, 2)
}

func (s *TronTestSuite) TestCheckpointValidationCov(c *C) {
	txOut := trxTxOutCov(common.PubKey("vault"), 100)
	valid := api.Transaction{RawData: api.RawData{RefBlockBytes: "95b0", RefBlockHash: "e5029588a71f9689", Timestamp: 5}}
	c.Assert(valid.Rehash(), IsNil)
	c.Assert(validateUnsignedTronTransaction(valid), IsNil)

	withRet := valid
	withRet.Ret = []api.Ret{{ContractRet: "SUCCESS"}}
	c.Check(validateUnsignedTronTransaction(withRet), ErrorMatches, ".*not unsigned")

	noID := valid
	noID.TxId = " "
	c.Check(validateUnsignedTronTransaction(noID), ErrorMatches, ".*incomplete")

	badHex := valid
	badHex.RawDataHex = "zz"
	c.Check(validateUnsignedTronTransaction(badHex), ErrorMatches, "fail to decode TRON checkpoint raw_data_hex.*")

	wrongID := valid
	wrongID.TxId = strings.Repeat("0", 64)
	c.Check(validateUnsignedTronTransaction(wrongID), ErrorMatches, ".*txid does not match raw_data_hex")

	// Structured fields that disagree with the signed preimage.
	drift := valid
	drift.RawData.Timestamp = 6
	c.Check(validateUnsignedTronTransaction(drift), ErrorMatches, ".*fields do not match raw_data_hex")

	badRef := valid
	badRef.RawData.RefBlockHash = "zz"
	c.Check(validateUnsignedTronTransaction(badRef), ErrorMatches, "fail to validate TRON checkpoint transaction.*")

	_, err := encodeTronSignCheckpoint(withRet, txOut)
	c.Check(err, NotNil)

	cp, err := encodeTronSignCheckpoint(valid, txOut)
	c.Assert(err, IsNil)
	restored, err := decodeTronSignCheckpoint(cp, txOut)
	c.Assert(err, IsNil)
	c.Check(restored.TxId, Equals, valid.TxId)

	_, err = decodeTronSignCheckpoint(nil, txOut)
	c.Check(err, ErrorMatches, "invalid TRON sign checkpoint size: 0")
	_, err = decodeTronSignCheckpoint([]byte("{"), txOut)
	c.Check(err, ErrorMatches, "fail to decode TRON sign checkpoint.*")
	_, err = decodeTronSignCheckpoint([]byte(`{"surprise":1}`), txOut)
	c.Check(err, ErrorMatches, "fail to decode TRON sign checkpoint.*")
	_, err = decodeTronSignCheckpoint(append(append([]byte{}, cp...), []byte(" {}")...), txOut)
	c.Check(err, ErrorMatches, ".*trailing data")
	_, err = decodeTronSignCheckpoint([]byte(`{"version":99}`), txOut)
	c.Check(err, ErrorMatches, "unsupported TRON sign checkpoint version: 99")

	// Valid envelope but the checksum was not updated after an edit.
	tampered := strings.Replace(string(cp), `"checksum":"`, `"checksum":"00`, 1)
	_, err = decodeTronSignCheckpoint([]byte(tampered), txOut)
	c.Check(err, ErrorMatches, ".*checksum mismatch")

	// Unsigned tx inside the envelope is not valid.
	digest, err := tronJSONDigest(txOut)
	c.Assert(err, IsNil)
	bad := `{"version":` + big.NewInt(tronSignCheckpointVersion).String() +
		`,"tx_out_digest":"` + digest + `","unsigned_tx":{"txID":""},"checksum":""}`
	_, err = decodeTronSignCheckpoint([]byte(bad), txOut)
	c.Check(err, ErrorMatches, ".*incomplete")

	_, err = tronJSONDigest(make(chan int))
	c.Check(err, NotNil)
}
