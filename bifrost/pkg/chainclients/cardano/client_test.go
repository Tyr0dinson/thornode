package cardano

import (
	"crypto/ed25519"
	"fmt"
	"math/big"
	"testing"

	sdkmath "cosmossdk.io/math"
	"github.com/blinklabs-io/gouroboros/ledger"
	"github.com/blinklabs-io/gouroboros/ledger/babbage"
	lcommon "github.com/blinklabs-io/gouroboros/ledger/common"
	"github.com/blinklabs-io/gouroboros/ledger/mary"
	"github.com/blinklabs-io/gouroboros/protocol/localstatequery"
	"github.com/rs/zerolog"
	"gitlab.com/thorchain/thornode/v3/bifrost/blockscanner"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/signercache"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
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

	s.client = &Client{
		logger: zerolog.Nop(),
		config: config.BifrostChainConfiguration{
			ChainID:        common.ADAChain,
			SolvencyBlocks: 10,
		},
		signerCacheManager: signerCacheMgr,
		storage:            storage,
		n2c:                &mockClientWrapper{},
	}
}

// ---------------------------------------------------------------------------
// ConfirmationCountReady
// ---------------------------------------------------------------------------

func (s *ClientTestSuite) TestConfirmationCountReady(c *C) {
	c.Assert(s.client.ConfirmationCountReady(types.TxIn{}), Equals, true)

	// always true regardless of content
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
	// default zero
	size, rate := s.client.GetNetworkFee()
	c.Assert(size, Equals, uint64(1))
	c.Assert(rate, Equals, uint64(0))

	// after setting
	s.client.networkFee = 200_000
	size, rate = s.client.GetNetworkFee()
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

	// empty TxArray => max height is 0 => returns tip - 0
	count := s.client.GetConfirmationCount(types.TxIn{})
	c.Assert(count, Equals, int64(200))
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
				{Hash: hash1, Idx: 0}: babbage.BabbageTransactionOutput{
					OutputAmount: mary.MaryTransactionOutputValue{Amount: 3_000_000},
				},
				{Hash: hash2, Idx: 1}: babbage.BabbageTransactionOutput{
					OutputAmount: mary.MaryTransactionOutputValue{Amount: 7_000_000},
				},
			},
		},
	}

	acct, err := s.client.GetAccountByAddress(addr.String(), big.NewInt(1))
	c.Assert(err, IsNil)
	c.Assert(acct.Coins, HasLen, 1)
	c.Assert(acct.Coins[0].Asset, Equals, common.ADAAsset)
	c.Assert(acct.Coins[0].Amount.Equal(sdkmath.NewUint(10_000_000)), Equals, true)
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
