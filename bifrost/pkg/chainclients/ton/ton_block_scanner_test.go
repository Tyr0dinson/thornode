package ton

import (
	"context"
	"time"

	"github.com/xssnick/tonutils-go/ton"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/ton/mock"
	"gitlab.com/thorchain/thornode/v3/bifrost/pubkeymanager"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/config"
	. "gopkg.in/check.v1"
)

// -------------------------------------------------------------------------------------
// Tests
// -------------------------------------------------------------------------------------

type BlockScannerTestSuite struct {
	scanner *TonBlockScanner
	bridge  thorclient.ThorchainBridge
}

const (
	vaultPubKey  = "tthorpub1zcjduepqzupdpx7334mjeuuggnvxv3ctkghu0qcz2eml30tgu9ukw58z6uusmmxmys"
	userAddress  = "0QBOG0gziTtbzAkfU43znt-nFqj4dT8dqwOsKNducdpKsB7M"
	vaultAddress = "0QBaxiHsHOr50V_1o6VSn2FQxlz9SxW5OFvqQq18Sy5M3xH0"
	depositMemo  = "+:TON.GRAM:tthor1xkz88t875m7ms7wa6220estwwdlc86pqsccrvm"
)

type mockVaultCache struct {
	address string
}

func (mc mockVaultCache) Reset() error         { return nil }
func (mc mockVaultCache) Has(addr string) bool { return mc.address == addr }

var _ = Suite(&BlockScannerTestSuite{})

func (s *BlockScannerTestSuite) newScanner() (*TonBlockScanner, error) {

	scanner, err := NewTonBlockScanner(
		config.BifrostBlockScannerConfiguration{
			ChainID:            common.TONChain,
			HTTPRequestTimeout: time.Second * 2,
			GasCacheBlocks:     1,
		},
		s.bridge,
		mock.NewMockApiClient(),
		pubkeymanager.NewMockPoolAddressValidator(),
	)
	if err != nil {
		return nil, err
	}

	scanner.globalNetworkFeeQueue = make(chan common.NetworkFee)
	scanner.vaults = mockVaultCache{address: vaultAddress}

	go func() {
		for range scanner.globalNetworkFeeQueue {
			time.Sleep(time.Millisecond * 200)
			continue
		}
	}()

	return scanner, nil
}

func (s *BlockScannerTestSuite) SetUpSuite(c *C) {
	clientConfig := config.BifrostClientConfiguration{
		ChainID:      "thorchain",
		ChainHost:    "localhost",
		SignerName:   "bob",
		SignerPasswd: "password",
	}

	var err error
	s.bridge, err = thorclient.NewThorchainBridge(
		clientConfig, nil, nil,
	)
	c.Assert(err, IsNil)

	// thorchain bech32 prefix so the eddsa vault pubkey decodes; idempotent, so
	// this does not depend on another suite's SetUpSuite running first.
	cosmos.GetConfig().SetBech32PrefixForAccount("tthor", "tthorpub")
	vaultPk, err := common.NewPubKey(vaultPubKey)
	c.Assert(err, IsNil)

	vaultAddr, err := vaultPk.GetAddress(common.TONChain)
	c.Assert(err, IsNil)
	c.Assert(vaultAddr, NotNil)
	c.Assert(vaultAddr.String(), Equals, vaultAddress)

	s.scanner, err = s.newScanner()
	c.Assert(err, IsNil)
}

func (s *BlockScannerTestSuite) TestGetHeight(c *C) {
	block, err := s.scanner.GetHeight()
	c.Assert(err, IsNil)
	c.Assert(block, Equals, int64(256673))
}

func (s *BlockScannerTestSuite) TestFetchMemPool(c *C) {
	txIn, err := s.scanner.FetchMemPool(time.Now().Unix())
	c.Assert(err, IsNil)
	c.Assert(txIn.Chain.IsEmpty(), Equals, true)
	c.Assert(txIn.TxArray, HasLen, 0)
}

func (s *BlockScannerTestSuite) TestFetchTxs_Err(c *C) {
	scanner, err := s.newScanner()
	c.Assert(err, IsNil)

	// height < 1 returns empty without scanning
	txIn, err := scanner.FetchTxs(0, 0)
	c.Assert(err, IsNil)
	c.Assert(txIn.TxArray, HasLen, 0)

	// non-existent master block surfaces an error
	_, err = scanner.FetchTxs(99, 99)
	c.Assert(err, NotNil)
}

func (s *BlockScannerTestSuite) TestFetchTxs_Ok(c *C) {
	scanner, err := s.newScanner()
	c.Assert(err, IsNil)

	// shard 121, master 120, 1 tx involving vault
	data, err := scanner.api.GetBlockData(
		context.TODO(),
		&ton.BlockIDExt{Workchain: 0, Shard: 0, SeqNo: 121},
	)
	c.Assert(err, IsNil)

	// verify the block has txs
	txs, err := data.ListTransactions()
	c.Assert(err, IsNil)
	c.Assert(len(txs) > 0, Equals, true)

	txIn, err := scanner.FetchTxs(120, 120)
	c.Assert(err, IsNil)
	c.Assert(txIn.TxArray, HasLen, 1)

	item := txIn.TxArray[0]
	c.Assert(item.To, Equals, vaultAddress)
	c.Assert(item.Sender != vaultAddress, Equals, true)
	c.Assert(item.Memo, Equals, depositMemo)
	c.Assert(item.Coins, HasLen, 1)
	c.Assert(item.Coins[0].Asset.Equals(common.GRAMAsset), Equals, true)
	c.Assert(item.Coins[0].Amount.IsZero(), Equals, false)
	c.Assert(item.BlockHeight, Equals, int64(120))
}

func (s *BlockScannerTestSuite) TestFetchTxs_NoTx(c *C) {
	scanner, err := s.newScanner()
	c.Assert(err, IsNil)

	// shard 27, master 30, 1 tx involving vault
	data, err := scanner.api.GetBlockData(
		context.TODO(),
		&ton.BlockIDExt{Workchain: 0, Shard: 0, SeqNo: 27},
	)
	c.Assert(err, IsNil)

	// verify the block has txs
	txs, err := data.ListTransactions()
	c.Assert(err, IsNil)
	c.Assert(len(txs) > 0, Equals, true)

	txIn, err := scanner.FetchTxs(30, 30)
	c.Assert(err, IsNil)
	c.Assert(txIn.TxArray, HasLen, 0)
}

func (s *BlockScannerTestSuite) TestVaultGate(c *C) {
	scanner, err := s.newScanner()
	c.Assert(err, IsNil)

	// the asgard vault address (derived from the eddsa pubkey) is recognized
	c.Assert(scanner.vaults.Has(vaultAddress), Equals, true)
	// a non-vault address is filtered out by the gate
	c.Assert(scanner.vaults.Has(userAddress), Equals, false)
}

func (s *BlockScannerTestSuite) TestGetNetworkFee(c *C) {
	scanner, err := s.newScanner()
	c.Assert(err, IsNil)

	// no block scanned yet
	c.Assert(scanner.feeRateCache, HasLen, 0)

	_, err = scanner.FetchTxs(120, 120)
	c.Assert(err, IsNil)
	c.Assert(scanner.feeRateCache, HasLen, 1)

	// the captured block carries a single external-in tx, sampled for fees
	c.Assert(scanner.feeRateCache[0], HasLen, 1)

	size, rate := scanner.GetNetworkFee()
	c.Assert(size, Equals, uint64(1))
	// median of the sampled fee, rounded to resolution, floored at minNetworkFee
	c.Assert(rate >= uint64(minNetworkFee), Equals, true)
}
