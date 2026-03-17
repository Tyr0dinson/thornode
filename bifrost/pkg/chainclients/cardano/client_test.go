package cardano

import (
	"crypto/ed25519"
	"fmt"
	"math/big"
	"testing"

	sdkmath "cosmossdk.io/math"
	"github.com/blinklabs-io/gouroboros/cbor"
	"github.com/blinklabs-io/gouroboros/ledger"
	"github.com/blinklabs-io/gouroboros/ledger/babbage"
	lcommon "github.com/blinklabs-io/gouroboros/ledger/common"
	"github.com/blinklabs-io/gouroboros/ledger/conway"
	"github.com/blinklabs-io/gouroboros/ledger/mary"
	"github.com/blinklabs-io/gouroboros/ledger/shelley"
	"github.com/blinklabs-io/gouroboros/protocol/localstatequery"
	cmted25519 "github.com/cometbft/cometbft/crypto/ed25519"
	"github.com/cosmos/cosmos-sdk/crypto/codec"
	"github.com/rs/zerolog"
	"gitlab.com/thorchain/thornode/v3/bifrost/blockscanner"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/signercache"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/config"
	. "gopkg.in/check.v1"
)

func TestClientPackage(t *testing.T) { TestingT(t) }

type ClientTestSuite struct {
	client *Client
}

var _ = Suite(&ClientTestSuite{})

func (s *ClientTestSuite) SetUpTest(c *C) {
	storage, err := blockscanner.NewBlockScannerStorageCardano("", config.LevelDBOptions{})
	c.Assert(err, IsNil)

	signerCacheMgr, err := signercache.NewSignerCacheManager(storage.GetInternalDb())
	c.Assert(err, IsNil)

	scanner := &Scanner{
		logger: zerolog.Nop(),
		config: config.BifrostBlockScannerConfiguration{
			ChainID:            common.ADAChain,
			GasPriceResolution: 1000,
		},
		db:  storage,
		n2c: &mockClientWrapper{},
	}

	s.client = &Client{
		logger: zerolog.Nop(),
		config: config.BifrostChainConfiguration{
			ChainID:        common.ADAChain,
			SolvencyBlocks: 10,
		},
		signerCacheManager: signerCacheMgr,
		storage:            storage,
		chainScanner:       scanner,
		n2c:                &mockClientWrapper{},
	}
}

// ---------------------------------------------------------------------------
// ConfirmationCountReady
// ---------------------------------------------------------------------------

func (s *ClientTestSuite) TestConfirmationCountReady(c *C) {
	c.Assert(s.client.ConfirmationCountReady(types.TxIn{}), Equals, true)

	// current height >= block height with 0 required confirmations
	s.client.chainScanner.n2c = &mockClientWrapper{blockNo: 200}
	txIn := types.TxIn{
		Chain: common.ADAChain,
		TxArray: []*types.TxInItem{
			{BlockHeight: 100, Tx: "abc123"},
		},
	}
	c.Assert(s.client.ConfirmationCountReady(txIn), Equals, true)
}

// ---------------------------------------------------------------------------
// GetChain
// ---------------------------------------------------------------------------

func (s *ClientTestSuite) TestGetChain(c *C) {
	c.Assert(s.client.GetChain(), Equals, common.ADAChain)
}

// ---------------------------------------------------------------------------
// GetConfig
// ---------------------------------------------------------------------------

func (s *ClientTestSuite) TestGetConfig(c *C) {
	cfg := s.client.GetConfig()
	c.Assert(cfg.ChainID, Equals, common.ADAChain)
	c.Assert(cfg.SolvencyBlocks, Equals, int64(10))
}

// ---------------------------------------------------------------------------
// GetNetworkFee
// ---------------------------------------------------------------------------

func (s *ClientTestSuite) TestGetNetworkFee(c *C) {
	s.client.chainScanner = makeTestScanner(c)

	// default zero
	size, rate := s.client.chainScanner.GetNetworkFee()
	c.Assert(size, Equals, uint64(1))
	c.Assert(rate, Equals, uint64(0))

	// after setting
	s.client.chainScanner.networkFee.Store(200_000)
	size, rate = s.client.chainScanner.GetNetworkFee()
	c.Assert(size, Equals, uint64(1))
	c.Assert(rate, Equals, uint64(200_000))
}

// ---------------------------------------------------------------------------
// ShouldReportSolvency
// ---------------------------------------------------------------------------

func (s *ClientTestSuite) TestShouldReportSolvency(c *C) {
	c.Assert(s.client.ShouldReportSolvency(0), Equals, true)
	c.Assert(s.client.ShouldReportSolvency(7), Equals, false)
	c.Assert(s.client.ShouldReportSolvency(90), Equals, true)
}

// ---------------------------------------------------------------------------
// sign (local key path)
// ---------------------------------------------------------------------------

func (s *ClientTestSuite) TestSignLocalKey(c *C) {
	// generate a test ed25519 key pair
	pub, priv, err := ed25519.GenerateKey(nil)
	c.Assert(err, IsNil)

	addr, err := AddressFromBytes(pub)
	c.Assert(err, IsNil)

	s.client.localKey = priv
	s.client.localAddress = addr.String()

	// create a tx out item whose vault address matches the local address
	txOutItem := types.TxOutItem{
		VaultPubKeyEddsa: "", // GetAddress will return "" for empty pubkey
	}

	// when vault address matches local address, local signing is used
	// set localAddress to "" so it matches GetAddress("") which returns ""
	s.client.localAddress = ""
	data := []byte("test data to sign")
	sig, err := s.client.sign(data, txOutItem)
	c.Assert(err, IsNil)
	c.Assert(len(sig), Equals, ed25519.SignatureSize)

	// verify the signature
	c.Assert(ed25519.Verify(pub, data, sig), Equals, true)
}

// ---------------------------------------------------------------------------
// OnObservedTxIn
// ---------------------------------------------------------------------------

func (s *ClientTestSuite) TestOnObservedTxInOutbound(c *C) {
	txID := "2564357C2530B71662069100466EE9E17E5226CF5E9B36870B7D9E92ED53C6AA"

	txInItem := types.TxInItem{
		BlockHeight: 1,
		Tx:          "txhash123",
		Memo:        "OUT:" + txID,
		Sender:      "addr1v8xc93gy5emqdjh6vfs2sg3g54je20xe20qrenwkcaj4qaqrctsph",
		To:          "addr1v9mspu7na3ycmw523tqqmx5dejlrkuz94qt6v6yg0sh0tvgacgq8d",
		Coins: common.Coins{
			common.NewCoin(common.ADAAsset, sdkmath.NewUint(1000000)),
		},
		ObservedVaultPubKey: "",
	}

	s.client.OnObservedTxIn(txInItem, 1)

	// verify it was cached - the signer cache should have the signed entry
	cacheHash := txInItem.CacheHash(s.client.GetChain(), txID)
	c.Assert(s.client.signerCacheManager.HasSigned(cacheHash), Equals, true)
}

// ---------------------------------------------------------------------------
// GetLatestTxForVault
// ---------------------------------------------------------------------------

func (s *ClientTestSuite) TestGetLatestTxForVault(c *C) {
	// nonexistent vault returns errors (key not found in db)
	_, _, err := s.client.GetLatestTxForVault("nonexistent")
	c.Assert(err, NotNil)

	// store using the same cache keys that GetLatestTxForVault looks up
	vault := "test-vault"
	chain := s.client.GetChain().String()
	inboundKey := types.InboundCacheKey(vault, chain)
	broadcastKey := types.BroadcastCacheKey(vault, chain)

	err = s.client.signerCacheManager.SetSigned("hash1", inboundKey, "tx-inbound")
	c.Assert(err, IsNil)
	err = s.client.signerCacheManager.SetSigned("hash2", broadcastKey, "tx-broadcast")
	c.Assert(err, IsNil)

	lastObserved, lastBroadcast, err := s.client.GetLatestTxForVault(vault)
	c.Assert(err, IsNil)
	c.Assert(lastObserved, Equals, "tx-inbound")
	c.Assert(lastBroadcast, Equals, "tx-broadcast")
}

// ---------------------------------------------------------------------------
// GetConfirmationCount (via mock n2c)
// ---------------------------------------------------------------------------

func (s *ClientTestSuite) TestGetConfirmationCount(c *C) {
	s.client.n2c = &mockClientWrapper{blockNo: 150}

	txIn := types.TxIn{
		TxArray: []*types.TxInItem{
			{BlockHeight: 100},
			{BlockHeight: 120},
		},
	}

	// tip (150) - max height in TxArray (120) = 30
	count := s.client.GetConfirmationCount(txIn)
	c.Assert(count, Equals, int64(30))
}

func (s *ClientTestSuite) TestGetConfirmationCountError(c *C) {
	s.client.n2c = &mockClientWrapper{blockNoErr: fmt.Errorf("fail")}

	txIn := types.TxIn{
		TxArray: []*types.TxInItem{
			{BlockHeight: 100},
		},
	}

	// on error, returns 0
	count := s.client.GetConfirmationCount(txIn)
	c.Assert(count, Equals, int64(0))
}

func (s *ClientTestSuite) TestGetConfirmationCountEmptyTxArray(c *C) {
	s.client.n2c = &mockClientWrapper{blockNo: 200}

	// empty TxArray => early return 0
	count := s.client.GetConfirmationCount(types.TxIn{})
	c.Assert(count, Equals, int64(0))
}

// ---------------------------------------------------------------------------
// GetAccountByAddress (via mock n2c)
// ---------------------------------------------------------------------------

func (s *ClientTestSuite) TestGetAccountByAddress(c *C) {
	addr, err := AddressFromBytes([]byte("test-pub-key"))
	c.Assert(err, IsNil)

	// seed storage with a block so getUtxosByAddress can resolve a point
	err = s.client.storage.SetBlockId(1, 100, lcommon.Blake2b256Hash([]byte("blk")).Bytes())
	c.Assert(err, IsNil)

	hash1 := lcommon.Blake2b256Hash([]byte{1})
	hash2 := lcommon.Blake2b256Hash([]byte{2})

	s.client.n2c = &mockClientWrapper{
		utxoResult: &localstatequery.UTxOsResult{
			Results: map[localstatequery.UtxoId]ledger.BabbageTransactionOutput{
				{Hash: hash1, Idx: 0}: {
					OutputAmount: mary.MaryTransactionOutputValue{Amount: 3_000_000},
				},
				{Hash: hash2, Idx: 1}: {
					OutputAmount: mary.MaryTransactionOutputValue{Amount: 7_000_000},
				},
			},
		},
	}

	acct, err := s.client.GetAccountByAddress(addr.String(), big.NewInt(1))
	c.Assert(err, IsNil)
	c.Assert(acct.Coins, HasLen, 1)
	c.Assert(acct.Coins[0].Asset, Equals, common.ADAAsset)
	// 10_000_000 lovelace (1e6) * 100 = 1_000_000_000 (1e8 THORChain units)
	c.Assert(acct.Coins[0].Amount.Equal(sdkmath.NewUint(1_000_000_000)), Equals, true)
}

func (s *ClientTestSuite) TestGetAccountByAddressNoUtxos(c *C) {
	addr, err := AddressFromBytes([]byte("empty-addr"))
	c.Assert(err, IsNil)

	err = s.client.storage.SetBlockId(1, 100, lcommon.Blake2b256Hash([]byte("blk")).Bytes())
	c.Assert(err, IsNil)

	s.client.n2c = &mockClientWrapper{
		utxoResult: &localstatequery.UTxOsResult{
			Results: map[localstatequery.UtxoId]ledger.BabbageTransactionOutput{},
		},
	}

	acct, err := s.client.GetAccountByAddress(addr.String(), big.NewInt(1))
	c.Assert(err, IsNil)
	c.Assert(acct.Coins, HasLen, 1)
	c.Assert(acct.Coins[0].Amount.Equal(sdkmath.NewUint(0)), Equals, true)
}

func (s *ClientTestSuite) TestGetAccountByAddressUtxoError(c *C) {
	addr, err := AddressFromBytes([]byte("err-addr"))
	c.Assert(err, IsNil)

	err = s.client.storage.SetBlockId(1, 100, lcommon.Blake2b256Hash([]byte("blk")).Bytes())
	c.Assert(err, IsNil)

	s.client.n2c = &mockClientWrapper{
		utxoErr: fmt.Errorf("network error"),
	}

	_, err = s.client.GetAccountByAddress(addr.String(), big.NewInt(1))
	c.Assert(err, NotNil)
}

// ---------------------------------------------------------------------------
// ConfirmationCountReady (insufficient confirmations)
// ---------------------------------------------------------------------------

func (s *ClientTestSuite) TestConfirmationCountReadyNotEnough(c *C) {
	s.client.chainScanner.n2c = &mockClientWrapper{blockNo: 105}

	txIn := types.TxIn{
		Chain:                common.ADAChain,
		ConfirmationRequired: 10,
		TxArray: []*types.TxInItem{
			{BlockHeight: 100, Tx: "abc123"},
		},
	}

	// currentHeight(105) - blockHeight(100) = 5, which is < 10 required
	c.Assert(s.client.ConfirmationCountReady(txIn), Equals, false)
}

func (s *ClientTestSuite) TestConfirmationCountReadyExact(c *C) {
	s.client.chainScanner.n2c = &mockClientWrapper{blockNo: 110}

	txIn := types.TxIn{
		Chain:                common.ADAChain,
		ConfirmationRequired: 10,
		TxArray: []*types.TxInItem{
			{BlockHeight: 100, Tx: "abc123"},
		},
	}

	// currentHeight(110) - blockHeight(100) = 10 >= 10 required
	c.Assert(s.client.ConfirmationCountReady(txIn), Equals, true)
}

func (s *ClientTestSuite) TestConfirmationCountReadyHeightError(c *C) {
	s.client.chainScanner.n2c = &mockClientWrapper{blockNoErr: fmt.Errorf("fail")}

	txIn := types.TxIn{
		Chain: common.ADAChain,
		TxArray: []*types.TxInItem{
			{BlockHeight: 100, Tx: "abc123"},
		},
	}

	// error getting height should return false
	c.Assert(s.client.ConfirmationCountReady(txIn), Equals, false)
}

// ---------------------------------------------------------------------------
// OnObservedTxIn (non-outbound memo, no-op)
// ---------------------------------------------------------------------------

func (s *ClientTestSuite) TestOnObservedTxInNonOutbound(c *C) {
	txInItem := types.TxInItem{
		BlockHeight: 1,
		Tx:          "txhash456",
		Memo:        "SWAP:ETH.ETH:0xABC", // not an outbound memo
		Sender:      "addr1v8xc93gy5emqdjh6vfs2sg3g54je20xe20qrenwkcaj4qaqrctsph",
		To:          "addr1v9mspu7na3ycmw523tqqmx5dejlrkuz94qt6v6yg0sh0tvgacgq8d",
		Coins: common.Coins{
			common.NewCoin(common.ADAAsset, sdkmath.NewUint(1000000)),
		},
	}

	// should not panic or cache anything — non-outbound memos are ignored
	s.client.OnObservedTxIn(txInItem, 1)

	cacheHash := txInItem.CacheHash(s.client.GetChain(), "")
	c.Assert(s.client.signerCacheManager.HasSigned(cacheHash), Equals, false)
}

func (s *ClientTestSuite) TestOnObservedTxInInvalidMemo(c *C) {
	txInItem := types.TxInItem{
		BlockHeight: 1,
		Tx:          "txhash789",
		Memo:        "this is not a valid memo format",
		Sender:      "addr1v8xc93gy5emqdjh6vfs2sg3g54je20xe20qrenwkcaj4qaqrctsph",
		To:          "addr1v9mspu7na3ycmw523tqqmx5dejlrkuz94qt6v6yg0sh0tvgacgq8d",
		Coins: common.Coins{
			common.NewCoin(common.ADAAsset, sdkmath.NewUint(1000000)),
		},
	}

	// should not panic — invalid memos are silently ignored
	s.client.OnObservedTxIn(txInItem, 1)
}

// ---------------------------------------------------------------------------
// ShouldReportSolvency (edge cases)
// ---------------------------------------------------------------------------

func (s *ClientTestSuite) TestShouldReportSolvencyDisabled(c *C) {
	s.client.config.SolvencyBlocks = 0
	c.Assert(s.client.ShouldReportSolvency(0), Equals, false)
	c.Assert(s.client.ShouldReportSolvency(10), Equals, false)
	c.Assert(s.client.ShouldReportSolvency(100), Equals, false)
}

func (s *ClientTestSuite) TestShouldReportSolvencyNegative(c *C) {
	s.client.config.SolvencyBlocks = -1
	c.Assert(s.client.ShouldReportSolvency(0), Equals, false)
	c.Assert(s.client.ShouldReportSolvency(10), Equals, false)
}

// ---------------------------------------------------------------------------
// GetConfirmationCount (multiple txs, uses max height)
// ---------------------------------------------------------------------------

func (s *ClientTestSuite) TestGetConfirmationCountUsesMaxHeight(c *C) {
	s.client.n2c = &mockClientWrapper{blockNo: 200}

	txIn := types.TxIn{
		TxArray: []*types.TxInItem{
			{BlockHeight: 50},
			{BlockHeight: 180}, // highest
			{BlockHeight: 100},
		},
	}

	// tip(200) - max height(180) = 20
	count := s.client.GetConfirmationCount(txIn)
	c.Assert(count, Equals, int64(20))
}

// ---------------------------------------------------------------------------
// GetAccountByAddress (single UTXO)
// ---------------------------------------------------------------------------

func (s *ClientTestSuite) TestGetAccountByAddressSingleUtxo(c *C) {
	addr, err := AddressFromBytes([]byte("single-utxo"))
	c.Assert(err, IsNil)

	err = s.client.storage.SetBlockId(1, 100, lcommon.Blake2b256Hash([]byte("blk")).Bytes())
	c.Assert(err, IsNil)

	hash1 := lcommon.Blake2b256Hash([]byte{1})

	s.client.n2c = &mockClientWrapper{
		utxoResult: &localstatequery.UTxOsResult{
			Results: map[localstatequery.UtxoId]ledger.BabbageTransactionOutput{
				{Hash: hash1, Idx: 0}: {
					OutputAmount: mary.MaryTransactionOutputValue{Amount: 5_000_000},
				},
			},
		},
	}

	acct, err := s.client.GetAccountByAddress(addr.String(), big.NewInt(1))
	c.Assert(err, IsNil)
	c.Assert(acct.Coins, HasLen, 1)
	// 5_000_000 lovelace * 100 = 500_000_000 in 1e8 units
	c.Assert(acct.Coins[0].Amount.Equal(sdkmath.NewUint(500_000_000)), Equals, true)
}

// ---------------------------------------------------------------------------
// SignTx
// ---------------------------------------------------------------------------

// makeVaultPubKey generates an ed25519 keypair and returns the bech32-encoded
// common.PubKey along with the raw private key and public key bytes.
func makeVaultPubKey(c *C) (common.PubKey, ed25519.PrivateKey, ed25519.PublicKey) {
	pub, priv, err := ed25519.GenerateKey(nil)
	c.Assert(err, IsNil)

	sdkPub, err := codec.FromCmtPubKeyInterface(cmted25519.PubKey(pub))
	c.Assert(err, IsNil)

	bech32Pub, err := cosmos.Bech32ifyPubKey(cosmos.Bech32PubKeyTypeAccPub, sdkPub)
	c.Assert(err, IsNil)

	pk, err := common.NewPubKey(bech32Pub)
	c.Assert(err, IsNil)

	return pk, priv, pub
}

func (s *ClientTestSuite) TestSignTxFullFlow(c *C) {
	// 1. Generate ed25519 keypair and bech32 PubKey
	vaultPubKey, priv, _ := makeVaultPubKey(c)

	// 2. Set client key and derive vault address
	s.client.localKey = priv
	vaultAddress := s.client.GetAddress(vaultPubKey)
	c.Assert(vaultAddress, Not(Equals), "")
	s.client.localAddress = vaultAddress

	// 3. Set TTL and fee params
	s.client.ttl = 400
	s.client.chainScanner.minFeeA.Store(44)
	s.client.chainScanner.minFeeB.Store(155_381)

	// 4. Store a block (height=1, slot=100) for UTXO lookup and TTL
	blockHash := lcommon.Blake2b256Hash([]byte("block1"))
	err := s.client.storage.SetBlockId(1, 100, blockHash.Bytes())
	c.Assert(err, IsNil)

	// 5. Set scanner mock for GetHeight
	s.client.chainScanner.n2c = &mockClientWrapper{blockNo: 1}

	// 6. Set client mock with one UTXO of 10 ADA (10_000_000 lovelace)
	utxoHash := lcommon.Blake2b256Hash([]byte("utxo1"))
	s.client.n2c = &mockClientWrapper{
		blockNo: 1,
		utxoResult: &localstatequery.UTxOsResult{
			Results: map[localstatequery.UtxoId]ledger.BabbageTransactionOutput{
				{Hash: utxoHash, Idx: 0}: {
					OutputAmount: mary.MaryTransactionOutputValue{Amount: 10_000_000},
				},
			},
		},
	}

	// 7. Create toAddress
	toAddr, err := AddressFromBytes([]byte("to-address-seed"))
	c.Assert(err, IsNil)

	// 8. Build TxOutItem: send 2 ADA, maxGas 0.5 ADA (all in 1e8 units)
	memo := "OUT:AABBCCDD"
	txOutItem := types.TxOutItem{
		Chain:            common.ADAChain,
		VaultPubKeyEddsa: vaultPubKey,
		ToAddress:        common.Address(toAddr.String()),
		Coins: common.Coins{
			common.NewCoin(common.ADAAsset, sdkmath.NewUint(200_000_000)),
		},
		MaxGas: common.Gas{
			common.NewCoin(common.ADAAsset, sdkmath.NewUint(50_000_000)),
		},
		Memo: memo,
	}

	// 9. Call SignTx
	txBytes, _, txIn, err := s.client.SignTx(txOutItem, 1)
	c.Assert(err, IsNil)
	c.Assert(txBytes, NotNil)
	c.Assert(txIn, NotNil)

	// --- TxInItem assertions ---
	c.Assert(txIn.Memo, Equals, memo)
	c.Assert(txIn.Sender, Equals, vaultAddress)
	c.Assert(txIn.To, Equals, toAddr.String())

	// Gas should be non-zero and under maxFee (500_000 lovelace * 100 = 50_000_000 in 1e8)
	c.Assert(txIn.Gas.IsEmpty(), Equals, false)
	gasCoin := txIn.Gas[0]
	c.Assert(gasCoin.Amount.GT(sdkmath.ZeroUint()), Equals, true)
	c.Assert(gasCoin.Amount.LT(sdkmath.NewUint(50_000_000)), Equals, true)

	// --- Decode and verify CBOR transaction ---
	decodedTx, err := ledger.NewConwayTransactionFromCbor(txBytes)
	c.Assert(err, IsNil)
	c.Assert(decodedTx, NotNil)

	// 1 input, 2 outputs (send + change)
	c.Assert(decodedTx.Inputs(), HasLen, 1)
	c.Assert(decodedTx.Outputs(), HasLen, 2)

	// Output[0] amount == 2_000_000 lovelace (2 ADA)
	c.Assert(decodedTx.Outputs()[0].Amount().Uint64(), Equals, uint64(2_000_000))

	// Output[1] (change) == 10_000_000 - 2_000_000 - fee
	fee := decodedTx.Fee().Uint64()
	c.Assert(fee > 0, Equals, true)
	expectedChange := uint64(10_000_000) - uint64(2_000_000) - fee
	c.Assert(decodedTx.Outputs()[1].Amount().Uint64(), Equals, expectedChange)

	// TTL = slot(100) + ttl(400) = 500
	c.Assert(decodedTx.Body.Ttl, Equals, uint64(500))

	// --- Spent UTXO is marked ---
	input := decodedTx.Inputs()[0]
	key := getUtxoKey(input.Id().Bytes(), input.Index())
	found, err := s.client.storage.HasSpentUtxo(key)
	c.Assert(err, IsNil)
	c.Assert(found, Equals, true)
}

func (s *ClientTestSuite) TestSignTxAlreadySigned(c *C) {
	vaultPubKey, priv, _ := makeVaultPubKey(c)

	s.client.localKey = priv
	vaultAddress := s.client.GetAddress(vaultPubKey)
	s.client.localAddress = vaultAddress
	s.client.ttl = 400
	s.client.chainScanner.minFeeA.Store(44)
	s.client.chainScanner.minFeeB.Store(155_381)

	blockHash := lcommon.Blake2b256Hash([]byte("block1"))
	err := s.client.storage.SetBlockId(1, 100, blockHash.Bytes())
	c.Assert(err, IsNil)

	s.client.chainScanner.n2c = &mockClientWrapper{blockNo: 1}

	utxoHash := lcommon.Blake2b256Hash([]byte("utxo1"))
	s.client.n2c = &mockClientWrapper{
		utxoResult: &localstatequery.UTxOsResult{
			Results: map[localstatequery.UtxoId]ledger.BabbageTransactionOutput{
				{Hash: utxoHash, Idx: 0}: {
					OutputAmount: mary.MaryTransactionOutputValue{Amount: 10_000_000},
				},
			},
		},
	}

	toAddr, err := AddressFromBytes([]byte("to-address-seed"))
	c.Assert(err, IsNil)

	txOutItem := types.TxOutItem{
		Chain:            common.ADAChain,
		VaultPubKeyEddsa: vaultPubKey,
		ToAddress:        common.Address(toAddr.String()),
		Coins: common.Coins{
			common.NewCoin(common.ADAAsset, sdkmath.NewUint(200_000_000)),
		},
		MaxGas: common.Gas{
			common.NewCoin(common.ADAAsset, sdkmath.NewUint(50_000_000)),
		},
		Memo: "OUT:AABBCCDD",
	}

	// First call succeeds
	txBytes, _, txIn, err := s.client.SignTx(txOutItem, 1)
	c.Assert(err, IsNil)
	c.Assert(txBytes, NotNil)
	c.Assert(txIn, NotNil)

	// Mark as signed in the signer cache (simulating what BroadcastTx does)
	err = s.client.signerCacheManager.SetSigned(
		txOutItem.CacheHash(),
		txOutItem.CacheVault(s.client.GetChain()),
		"txhash123",
	)
	c.Assert(err, IsNil)

	// Second call returns nil, nil, nil, nil (already signed)
	txBytes2, extra2, txIn2, err2 := s.client.SignTx(txOutItem, 1)
	c.Assert(err2, IsNil)
	c.Assert(txBytes2, IsNil)
	c.Assert(extra2, IsNil)
	c.Assert(txIn2, IsNil)
}

func (s *ClientTestSuite) TestSignTxNoADACoin(c *C) {
	vaultPubKey, priv, _ := makeVaultPubKey(c)

	s.client.localKey = priv
	s.client.localAddress = s.client.GetAddress(vaultPubKey)

	txOutItem := types.TxOutItem{
		Chain:            common.ADAChain,
		VaultPubKeyEddsa: vaultPubKey,
		ToAddress:        common.Address("addr_test1vze3395j26tfwymgtmk4e26gmam7vaq9vyejtj0lkpj70rc20g9xv"),
		Coins:            common.Coins{}, // no ADA coin
		Memo:             "OUT:AABB",
	}

	_, _, _, err := s.client.SignTx(txOutItem, 1)
	c.Assert(err, NotNil)
	c.Assert(err, ErrorMatches, "no ADA coins found")
}

// signTestTx sets up the client with an ed25519 keypair, 10 ADA UTXO, and signs
// a transaction sending 2 ADA with memo "OUT:AABBCCDD". Returns the TxOutItem
// and signed tx bytes. The signed tx has TTL = slot(100) + ttl(400) = 500.
func (s *ClientTestSuite) signTestTx(c *C) (types.TxOutItem, []byte) {
	vaultPubKey, priv, _ := makeVaultPubKey(c)

	s.client.localKey = priv
	vaultAddress := s.client.GetAddress(vaultPubKey)
	c.Assert(vaultAddress, Not(Equals), "")
	s.client.localAddress = vaultAddress

	s.client.ttl = 400
	s.client.chainScanner.minFeeA.Store(44)
	s.client.chainScanner.minFeeB.Store(155_381)

	blockHash := lcommon.Blake2b256Hash([]byte("block1"))
	err := s.client.storage.SetBlockId(1, 100, blockHash.Bytes())
	c.Assert(err, IsNil)

	s.client.chainScanner.n2c = &mockClientWrapper{blockNo: 1}

	utxoHash := lcommon.Blake2b256Hash([]byte("utxo1"))
	s.client.n2c = &mockClientWrapper{
		blockNo: 1,
		utxoResult: &localstatequery.UTxOsResult{
			Results: map[localstatequery.UtxoId]ledger.BabbageTransactionOutput{
				{Hash: utxoHash, Idx: 0}: {
					OutputAmount: mary.MaryTransactionOutputValue{Amount: 10_000_000},
				},
			},
		},
	}

	toAddr, err := AddressFromBytes([]byte("to-address-seed"))
	c.Assert(err, IsNil)

	txOutItem := types.TxOutItem{
		Chain:            common.ADAChain,
		VaultPubKeyEddsa: vaultPubKey,
		ToAddress:        common.Address(toAddr.String()),
		Coins: common.Coins{
			common.NewCoin(common.ADAAsset, sdkmath.NewUint(200_000_000)),
		},
		MaxGas: common.Gas{
			common.NewCoin(common.ADAAsset, sdkmath.NewUint(50_000_000)),
		},
		Memo: "OUT:AABBCCDD",
	}

	txBytes, _, _, err := s.client.SignTx(txOutItem, 1)
	c.Assert(err, IsNil)
	c.Assert(txBytes, NotNil)

	return txOutItem, txBytes
}

// ---------------------------------------------------------------------------
// BroadcastTx
// ---------------------------------------------------------------------------

func (s *ClientTestSuite) TestBroadcastTxInvalidCbor(c *C) {
	txOutItem := types.TxOutItem{Chain: common.ADAChain}
	_, err := s.client.BroadcastTx(txOutItem, []byte("not cbor"))
	c.Assert(err, NotNil)
	c.Assert(err, ErrorMatches, "failed to decode transaction.*")
}

func (s *ClientTestSuite) TestBroadcastTxExpiredTTL(c *C) {
	txOutItem, txBytes := s.signTestTx(c)

	// The signed tx has TTL=500. Store a block with slot=600 (>= 500) to make it expired.
	blockHash := lcommon.Blake2b256Hash([]byte("block-expired"))
	err := s.client.storage.SetBlockId(2, 600, blockHash.Bytes())
	c.Assert(err, IsNil)

	_, err = s.client.BroadcastTx(txOutItem, txBytes)
	c.Assert(err, NotNil)
	c.Assert(err, ErrorMatches, "transaction expired")
}

func (s *ClientTestSuite) TestBroadcastTxInvalidOutputCount(c *C) {
	txOutItem := types.TxOutItem{Chain: common.ADAChain}

	// Store a block so GetLastBlockId succeeds
	blockHash := lcommon.Blake2b256Hash([]byte("blk"))
	err := s.client.storage.SetBlockId(1, 100, blockHash.Bytes())
	c.Assert(err, IsNil)

	inputHash := lcommon.Blake2b256Hash([]byte("input"))
	input := shelley.NewShelleyTransactionInput(inputHash.String(), 0)

	// 0 outputs
	tx0 := &conway.ConwayTransaction{}
	tx0.Body.TxInputs = conway.NewConwayTransactionInputSet(
		[]shelley.ShelleyTransactionInput{input},
	)
	tx0.Body.TxOutputs = []babbage.BabbageTransactionOutput{}
	tx0.Body.Ttl = 999_999
	tx0.TxIsValid = true
	txBytes0, err := cbor.Encode(tx0)
	c.Assert(err, IsNil)

	_, err = s.client.BroadcastTx(txOutItem, txBytes0)
	c.Assert(err, NotNil)
	c.Assert(err, ErrorMatches, "invalid number of transaction outputs.*")

	// 3 outputs
	tx3 := &conway.ConwayTransaction{}
	tx3.Body.TxInputs = conway.NewConwayTransactionInputSet(
		[]shelley.ShelleyTransactionInput{input},
	)
	addr, err := AddressFromBytes([]byte("test-addr"))
	c.Assert(err, IsNil)
	tx3.Body.TxOutputs = []babbage.BabbageTransactionOutput{
		{OutputAddress: addr, OutputAmount: mary.MaryTransactionOutputValue{Amount: 1_000_000}},
		{OutputAddress: addr, OutputAmount: mary.MaryTransactionOutputValue{Amount: 2_000_000}},
		{OutputAddress: addr, OutputAmount: mary.MaryTransactionOutputValue{Amount: 3_000_000}},
	}
	tx3.Body.Ttl = 999_999
	tx3.TxIsValid = true
	txBytes3, err := cbor.Encode(tx3)
	c.Assert(err, IsNil)

	_, err = s.client.BroadcastTx(txOutItem, txBytes3)
	c.Assert(err, NotNil)
	c.Assert(err, ErrorMatches, "invalid number of transaction outputs.*")
}

func (s *ClientTestSuite) TestBroadcastTxSubmitError(c *C) {
	txOutItem, txBytes := s.signTestTx(c)

	// Set n2c to return a submit error
	s.client.n2c = &mockClientWrapper{submitErr: fmt.Errorf("network failure")}

	_, err := s.client.BroadcastTx(txOutItem, txBytes)
	c.Assert(err, NotNil)
	c.Assert(err, ErrorMatches, "failed to submit transaction.*")
}

func (s *ClientTestSuite) TestBroadcastTxAlreadySubmitted(c *C) {
	txOutItem, txBytes := s.signTestTx(c)

	// "probably already been included" is treated as success
	s.client.n2c = &mockClientWrapper{
		submitErr: fmt.Errorf("probably already been included"),
	}

	txHash, err := s.client.BroadcastTx(txOutItem, txBytes)
	c.Assert(err, IsNil)
	c.Assert(txHash, Not(Equals), "")

	// signer cache should be updated
	c.Assert(s.client.signerCacheManager.HasSigned(txOutItem.CacheHash()), Equals, true)
}

func (s *ClientTestSuite) TestBroadcastTxSuccess(c *C) {
	txOutItem, txBytes := s.signTestTx(c)

	// Default mock — no submit error
	s.client.n2c = &mockClientWrapper{}

	txHash, err := s.client.BroadcastTx(txOutItem, txBytes)
	c.Assert(err, IsNil)
	c.Assert(txHash, Not(Equals), "")

	// Verify hash matches decoded tx hash
	decodedTx, err := ledger.NewConwayTransactionFromCbor(txBytes)
	c.Assert(err, IsNil)
	c.Assert(txHash, Equals, decodedTx.Hash().String())

	// Signer cache should have the entry
	c.Assert(s.client.signerCacheManager.HasSigned(txOutItem.CacheHash()), Equals, true)
}

func (s *ClientTestSuite) TestBroadcastTxSuccessSingleOutput(c *C) {
	// Setup with a small UTXO so change falls below dust and is absorbed into fee
	vaultPubKey, priv, _ := makeVaultPubKey(c)

	s.client.localKey = priv
	vaultAddress := s.client.GetAddress(vaultPubKey)
	s.client.localAddress = vaultAddress
	s.client.ttl = 400
	s.client.chainScanner.minFeeA.Store(44)
	s.client.chainScanner.minFeeB.Store(155_381)

	blockHash := lcommon.Blake2b256Hash([]byte("block1"))
	err := s.client.storage.SetBlockId(1, 100, blockHash.Bytes())
	c.Assert(err, IsNil)

	s.client.chainScanner.n2c = &mockClientWrapper{blockNo: 1}

	// 1.7 ADA — enough to cover 1 ADA send + maxFee 0.5 ADA, but change below dust threshold
	utxoHash := lcommon.Blake2b256Hash([]byte("utxo-small"))
	s.client.n2c = &mockClientWrapper{
		blockNo: 1,
		utxoResult: &localstatequery.UTxOsResult{
			Results: map[localstatequery.UtxoId]ledger.BabbageTransactionOutput{
				{Hash: utxoHash, Idx: 0}: {
					OutputAmount: mary.MaryTransactionOutputValue{Amount: 1_700_000},
				},
			},
		},
	}

	toAddr, err := AddressFromBytes([]byte("to-address-seed"))
	c.Assert(err, IsNil)

	txOutItem := types.TxOutItem{
		Chain:            common.ADAChain,
		VaultPubKeyEddsa: vaultPubKey,
		ToAddress:        common.Address(toAddr.String()),
		Coins: common.Coins{
			common.NewCoin(common.ADAAsset, sdkmath.NewUint(100_000_000)), // 1 ADA
		},
		MaxGas: common.Gas{
			common.NewCoin(common.ADAAsset, sdkmath.NewUint(50_000_000)),
		},
		Memo: "OUT:AABBCCDD",
	}

	txBytes, _, _, err := s.client.SignTx(txOutItem, 1)
	c.Assert(err, IsNil)

	// Verify the tx has only 1 output (change absorbed into fee)
	decodedTx, err := ledger.NewConwayTransactionFromCbor(txBytes)
	c.Assert(err, IsNil)
	c.Assert(decodedTx.Outputs(), HasLen, 1)

	// Now broadcast
	s.client.n2c = &mockClientWrapper{}
	txHash, err := s.client.BroadcastTx(txOutItem, txBytes)
	c.Assert(err, IsNil)
	c.Assert(txHash, Not(Equals), "")
}

func (s *ClientTestSuite) TestSignTxInsufficientFunds(c *C) {
	vaultPubKey, priv, _ := makeVaultPubKey(c)

	s.client.localKey = priv
	vaultAddress := s.client.GetAddress(vaultPubKey)
	s.client.localAddress = vaultAddress
	s.client.ttl = 400
	s.client.chainScanner.minFeeA.Store(44)
	s.client.chainScanner.minFeeB.Store(155_381)

	blockHash := lcommon.Blake2b256Hash([]byte("block1"))
	err := s.client.storage.SetBlockId(1, 100, blockHash.Bytes())
	c.Assert(err, IsNil)

	s.client.chainScanner.n2c = &mockClientWrapper{blockNo: 1}

	// Only 1 ADA available (1_000_000 lovelace)
	utxoHash := lcommon.Blake2b256Hash([]byte("utxo-small"))
	s.client.n2c = &mockClientWrapper{
		utxoResult: &localstatequery.UTxOsResult{
			Results: map[localstatequery.UtxoId]ledger.BabbageTransactionOutput{
				{Hash: utxoHash, Idx: 0}: {
					OutputAmount: mary.MaryTransactionOutputValue{Amount: 1_000_000},
				},
			},
		},
	}

	toAddr, err := AddressFromBytes([]byte("to-address-seed"))
	c.Assert(err, IsNil)

	// Try to send 5 ADA (500_000_000 in 1e8) with only 1 ADA available
	txOutItem := types.TxOutItem{
		Chain:            common.ADAChain,
		VaultPubKeyEddsa: vaultPubKey,
		ToAddress:        common.Address(toAddr.String()),
		Coins: common.Coins{
			common.NewCoin(common.ADAAsset, sdkmath.NewUint(500_000_000)),
		},
		MaxGas: common.Gas{
			common.NewCoin(common.ADAAsset, sdkmath.NewUint(50_000_000)),
		},
		Memo: "OUT:AABB",
	}

	_, _, _, err = s.client.SignTx(txOutItem, 1)
	c.Assert(err, NotNil)
	c.Assert(err, ErrorMatches, ".*insufficient funds.*")
}
