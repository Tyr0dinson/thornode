package cardano

import (
	"crypto/ed25519"
	"testing"

	sdkmath "cosmossdk.io/math"
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
		config: config.BifrostChainConfiguration{
			ChainID:        common.ADAChain,
			SolvencyBlocks: 10,
		},
		signerCacheManager: signerCacheMgr,
		storage:            storage,
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
		Sender:      "sender",
		To:          "to",
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

	// after signing a tx, the vault key should be recorded
	vaultKey := "test-vault"
	err = s.client.signerCacheManager.SetSigned("hash1", vaultKey, "txhash1")
	c.Assert(err, IsNil)

	// the latest recorded tx should now be retrievable
	latest, _, err := s.client.GetLatestTxForVault(vaultKey)
	c.Assert(err, IsNil)
	c.Assert(latest, Equals, "txhash1")
}
