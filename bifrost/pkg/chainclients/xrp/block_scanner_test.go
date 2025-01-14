package xrp

import (
	"encoding/hex"
	"fmt"

	sdkmath "cosmossdk.io/math"
	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	cryptocodec "github.com/cosmos/cosmos-sdk/crypto/codec"
	"github.com/cosmos/cosmos-sdk/crypto/hd"
	cKeys "github.com/cosmos/cosmos-sdk/crypto/keyring"

	//protov2 "google.golang.org/protobuf/proto"

	"github.com/rs/zerolog/log"
	"gitlab.com/thorchain/thornode/v3/bifrost/metrics"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/config"

	"gitlab.com/thorchain/thornode/v3/cmd"
	. "gopkg.in/check.v1"

	"github.com/Peersyst/xrpl-go/xrpl/transaction"
	"github.com/Peersyst/xrpl-go/xrpl/transaction/types"
)

// -------------------------------------------------------------------------------------
// Tests
// -------------------------------------------------------------------------------------

type BlockScannerTestSuite struct {
	m      *metrics.Metrics
	bridge thorclient.ThorchainBridge
	keys   *thorclient.Keys
}

var _ = Suite(&BlockScannerTestSuite{})

func (s *BlockScannerTestSuite) SetUpSuite(c *C) {
	s.m = GetMetricForTest(c)
	c.Assert(s.m, NotNil)
	cfg := config.BifrostClientConfiguration{
		ChainID:         "thorchain",
		ChainHost:       "localhost",
		SignerName:      "bob",
		SignerPasswd:    "password",
		ChainHomeFolder: "",
	}

	registry := codectypes.NewInterfaceRegistry()
	cryptocodec.RegisterInterfaces(registry)
	cdc := codec.NewProtoCodec(registry)
	kb := cKeys.NewInMemory(cdc)
	_, _, err := kb.NewMnemonic(cfg.SignerName, cKeys.English, cmd.THORChainHDPath, cfg.SignerPasswd, hd.Secp256k1)
	c.Assert(err, IsNil)
	thorKeys := thorclient.NewKeysWithKeybase(kb, cfg.SignerName, cfg.SignerPasswd)
	c.Assert(err, IsNil)
	s.bridge, err = thorclient.NewThorchainBridge(cfg, s.m, thorKeys)
	c.Assert(err, IsNil)
	s.keys = thorKeys
}

func (s *BlockScannerTestSuite) TestCalculateAverageFees(c *C) {
	cfg := config.BifrostBlockScannerConfiguration{ChainID: common.XRPChain}
	blockScanner := XrpBlockScanner{cfg: cfg}

	blockScanner.updateFeeCache(common.NewCoin(common.XRPAsset, sdkmath.NewUint(10)))
	c.Check(len(blockScanner.feeCache), Equals, 1)
	c.Check(blockScanner.averageFee().String(), Equals, fmt.Sprintf("%d", uint64(10)))

	blockScanner.updateFeeCache(common.NewCoin(common.XRPAsset, sdkmath.NewUint(10)))
	c.Check(len(blockScanner.feeCache), Equals, 2)
	c.Check(blockScanner.averageFee().String(), Equals, fmt.Sprintf("%d", uint64(10)))

	// two txs at double fee should average to 75% of last
	blockScanner.updateFeeCache(common.NewCoin(common.XRPAsset, sdkmath.NewUint(20)))
	blockScanner.updateFeeCache(common.NewCoin(common.XRPAsset, sdkmath.NewUint(20)))
	c.Check(len(blockScanner.feeCache), Equals, 4)
	c.Check(blockScanner.averageFee().String(), Equals, fmt.Sprintf("%d", uint64(15)))

	// skip transactions with zero fee
	blockScanner.updateFeeCache(common.NewCoin(common.XRPAsset, sdkmath.NewUint(0)))
	c.Check(len(blockScanner.feeCache), Equals, 4)
	c.Check(blockScanner.averageFee().String(), Equals, fmt.Sprintf("%d", uint64(15)))

	// ensure we only cache the transaction limit number of blocks
	for i := 0; i < FeeCacheTransactions; i++ {
		blockScanner.updateFeeCache(common.NewCoin(common.XRPAsset, sdkmath.NewUint(12)))
	}
	c.Check(len(blockScanner.feeCache), Equals, FeeCacheTransactions)
	c.Check(blockScanner.averageFee().String(), Equals, fmt.Sprintf("%d", uint64(12)))
}

func (s *BlockScannerTestSuite) TestProcessTxs(c *C) {
	cfg := config.BifrostBlockScannerConfiguration{ChainID: common.XRPChain}

	blockScanner := XrpBlockScanner{
		cfg:    cfg,
		logger: log.Logger.With().Str("module", "blockscanner").Str("chain", common.XRPChain.String()).Logger(),
	}

	tx := transaction.Payment{
		BaseTx: transaction.BaseTx{
			Account: types.Address("rs3xN42EFLE23gUDG2Rw4rwxhR9MnjwZKQ"),
			SigningPubKey: "03dd7eb4b0479d2898cabdd4ccfd30f2277e57cb9c13ccabb8ec5170ed13dd149a",
			Fee: types.XRPCurrencyAmount(20),
			Memos: []types.MemoWrapper{
				{
					Memo: types.Memo{
						MemoData: hex.EncodeToString([]byte("hello")),
					},
				},
			},
		},
		Amount: types.XRPCurrencyAmount(1000000),
		Destination: types.Address("rELnd6Ae5ZYDhHkaqjSVg2vgtBnzjeDshm"),
	}

	txFlat := tx.Flatten()

	txInItems, err := blockScanner.processTxs(1, []transaction.FlatTransaction{txFlat})
	c.Assert(err, IsNil)

	// proccessTxs should filter out everything besides the valid MsgSend
	c.Assert(len(txInItems), Equals, 1)
	c.Assert(txInItems[0].Memo, Equals, "hello")
}
