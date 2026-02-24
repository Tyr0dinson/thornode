package cardano

import (
	"fmt"

	"github.com/blinklabs-io/gouroboros/ledger"
	lcommon "github.com/blinklabs-io/gouroboros/ledger/common"
	"github.com/blinklabs-io/gouroboros/ledger/conway"
	"github.com/blinklabs-io/gouroboros/protocol/chainsync"
	pcommon "github.com/blinklabs-io/gouroboros/protocol/common"
	"github.com/blinklabs-io/gouroboros/protocol/localstatequery"
	"github.com/rs/zerolog"
	"gitlab.com/thorchain/thornode/v3/bifrost/blockscanner"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/config"
	. "gopkg.in/check.v1"
)

// ---------------------------------------------------------------------------
// mockClientWrapper satisfies the ClientWrapper interface for testing.
// Shared by both ScannerTestSuite and ClientTestSuite (same package).
// ---------------------------------------------------------------------------

type mockClientWrapper struct {
	blockNo           int64
	blockNoErr        error
	protocolParams    lcommon.ProtocolParameters
	protocolParamsErr error
	chainPoint        *pcommon.Point
	chainPointErr     error
	genesisConfig     *localstatequery.GenesisConfigResult
	genesisConfigErr  error
	utxoResult        *localstatequery.UTxOsResult
	utxoErr           error
	submitErr         error
}

func (m *mockClientWrapper) GetChainBlockNo() (int64, error) {
	return m.blockNo, m.blockNoErr
}
func (m *mockClientWrapper) GetCurrentProtocolParams() (lcommon.ProtocolParameters, error) {
	return m.protocolParams, m.protocolParamsErr
}
func (m *mockClientWrapper) GetChainPoint() (*pcommon.Point, error) {
	return m.chainPoint, m.chainPointErr
}
func (m *mockClientWrapper) GetGenesisConfig() (*localstatequery.GenesisConfigResult, error) {
	return m.genesisConfig, m.genesisConfigErr
}
func (m *mockClientWrapper) GetUTxOByAddress(_ []ledger.Address, _ *pcommon.Point) (*localstatequery.UTxOsResult, error) {
	return m.utxoResult, m.utxoErr
}
func (m *mockClientWrapper) SubmitTx(_ uint16, _ []byte) error {
	return m.submitErr
}

// ---------------------------------------------------------------------------
// test helpers
// ---------------------------------------------------------------------------

type ScannerTestSuite struct{}

var _ = Suite(&ScannerTestSuite{})

func makeTestScanner(c *C) *Scanner {
	storage, err := blockscanner.NewBlockScannerStorageCardano("", config.LevelDBOptions{})
	c.Assert(err, IsNil)

	return &Scanner{
		logger: zerolog.Nop(),
		config: config.BifrostBlockScannerConfiguration{
			ChainID:            common.ADAChain,
			GasPriceResolution: 1000,
		},
		db:  storage,
		n2c: &mockClientWrapper{},
	}
}

// ---------------------------------------------------------------------------
// FetchMemPool
// ---------------------------------------------------------------------------

func (s *ScannerTestSuite) TestFetchMemPool(c *C) {
	scanner := makeTestScanner(c)

	txIn, err := scanner.FetchMemPool(42)
	c.Assert(err, IsNil)
	c.Assert(txIn, DeepEquals, types.TxIn{})
}

// ---------------------------------------------------------------------------
// GetNetworkFee
// ---------------------------------------------------------------------------

func (s *ScannerTestSuite) TestGetNetworkFee(c *C) {
	scanner := makeTestScanner(c)

	// default
	size, fee := scanner.GetNetworkFee()
	c.Assert(size, Equals, uint64(1))
	c.Assert(fee, Equals, uint64(0))

	// after setting
	scanner.networkFee = 250_000
	size, fee = scanner.GetNetworkFee()
	c.Assert(size, Equals, uint64(1))
	c.Assert(fee, Equals, uint64(250_000))
}

// ---------------------------------------------------------------------------
// GetNetworkMinFees
// ---------------------------------------------------------------------------

func (s *ScannerTestSuite) TestGetNetworkMinFees(c *C) {
	scanner := makeTestScanner(c)

	// default
	a, b := scanner.GetNetworkMinFees()
	c.Assert(a, Equals, uint64(0))
	c.Assert(b, Equals, uint64(0))

	// after setting
	scanner.minFeeA = 44
	scanner.minFeeB = 155_381
	a, b = scanner.GetNetworkMinFees()
	c.Assert(a, Equals, uint64(44))
	c.Assert(b, Equals, uint64(155_381))
}

// ---------------------------------------------------------------------------
// rollForwardHandler
// ---------------------------------------------------------------------------

// mockBlockHeader implements ledger.BlockHeader for testing
type mockBlockHeader struct {
	blockNumber uint64
	slotNumber  uint64
	hash        lcommon.Blake2b256
}

func (h *mockBlockHeader) Hash() lcommon.Blake2b256       { return h.hash }
func (h *mockBlockHeader) PrevHash() lcommon.Blake2b256   { return lcommon.Blake2b256{} }
func (h *mockBlockHeader) BlockNumber() uint64            { return h.blockNumber }
func (h *mockBlockHeader) SlotNumber() uint64             { return h.slotNumber }
func (h *mockBlockHeader) IssuerVkey() lcommon.IssuerVkey { return lcommon.IssuerVkey{} }
func (h *mockBlockHeader) BlockBodySize() uint64          { return 0 }
func (h *mockBlockHeader) Era() lcommon.Era               { return lcommon.Era{} }
func (h *mockBlockHeader) Cbor() []byte                   { return nil }
func (h *mockBlockHeader) BlockBodyHash() lcommon.Blake2b256 {
	return lcommon.Blake2b256{}
}

func (s *ScannerTestSuite) TestRollForwardHandler(c *C) {
	scanner := makeTestScanner(c)

	// store several blocks
	for i := uint64(1); i <= 5; i++ {
		hash := lcommon.Blake2b256Hash([]byte{byte(i)})
		header := &mockBlockHeader{
			blockNumber: i,
			slotNumber:  i * 100,
			hash:        hash,
		}

		err := scanner.rollForwardHandler(
			chainsync.CallbackContext{},
			0,
			header,
			pcommon.Tip{},
		)
		c.Assert(err, IsNil)
	}

	// all blocks should be retrievable
	for i := uint64(1); i <= 5; i++ {
		height, slot, _, err := scanner.db.GetBlockId(i)
		c.Assert(err, IsNil)
		c.Assert(height, Equals, i)
		c.Assert(slot, Equals, i*100)
	}

	// GetLastBlockId should return the most recent
	height, slot, _, err := scanner.db.GetLastBlockId()
	c.Assert(err, IsNil)
	c.Assert(height, Equals, uint64(5))
	c.Assert(slot, Equals, uint64(500))
}

func (s *ScannerTestSuite) TestRollForwardHandlerUnknownType(c *C) {
	scanner := makeTestScanner(c)

	// passing an unrecognized type should not panic, just be a no-op
	err := scanner.rollForwardHandler(
		chainsync.CallbackContext{},
		0,
		"not a block header",
		pcommon.Tip{},
	)
	c.Assert(err, IsNil)
}

// ---------------------------------------------------------------------------
// GetHeight (via mock n2c)
// ---------------------------------------------------------------------------

func (s *ScannerTestSuite) TestGetHeight(c *C) {
	scanner := makeTestScanner(c)
	scanner.n2c = &mockClientWrapper{blockNo: 42}

	height, err := scanner.GetHeight()
	c.Assert(err, IsNil)
	c.Assert(height, Equals, int64(42))

	scanner.n2c = &mockClientWrapper{blockNoErr: fmt.Errorf("timeout")}
	_, err = scanner.GetHeight()
	c.Assert(err, NotNil)
}

// ---------------------------------------------------------------------------
// updateNetworkFee (protocol params path via mock n2c)
// ---------------------------------------------------------------------------

func (s *ScannerTestSuite) TestUpdateNetworkFee(c *C) {
	queue := make(chan common.NetworkFee, 1)

	scanner := makeTestScanner(c)
	scanner.globalNetworkFeeQueue = queue

	// minFeeA and minFee are 0, so try to get parameters which fails

	// minFeeA and minFee are 0, so try to get parameters which succeeds
	scanner.n2c = &mockClientWrapper{
		protocolParams: &conway.ConwayProtocolParameters{
			MinFeeA: 43,
			MinFeeB: 155_951,
		},
	}

	err := scanner.updateNetworkFee(99)
	c.Assert(err, IsNil)

	c.Assert(scanner.minFeeA, Equals, uint64(43))
	c.Assert(scanner.minFeeB, Equals, uint64(155_951))
	// 577 * 43 + 155951 + 5 = 180767 => 181000
	c.Assert(scanner.networkFee, Equals, uint64(181_000))

	select {
	case nf := <-queue:
		c.Assert(nf.Chain, Equals, common.ADAChain)
		c.Assert(nf.Height, Equals, int64(99))
		c.Assert(nf.TransactionSize, Equals, uint64(1))
		c.Assert(nf.TransactionRate, Equals, scanner.networkFee)
	default:
		c.Fatal("expected network fee on queue")
	}

	// height divisible by networkFeeUpdatePeriod (100) triggers the n2c call
	scanner.n2c = &mockClientWrapper{
		protocolParams: &conway.ConwayProtocolParameters{
			MinFeeA: 44,
			MinFeeB: 155_381,
		},
	}
	err = scanner.updateNetworkFee(100)
	c.Assert(err, IsNil)

	c.Assert(scanner.minFeeA, Equals, uint64(44))
	c.Assert(scanner.minFeeB, Equals, uint64(155_381))
	// 577 * 44 + 155_381 + 5 = 180774 => 181000
	c.Assert(scanner.networkFee, Equals, uint64(181_000))

	// no update sent, because rounded fee is still 181_000
	select {
	case <-queue:
		c.Fatal("should not enqueue when fees are unchanged")
	default:
	}

	// same fees as already set => early return, nothing enqueued
	err = scanner.updateNetworkFee(200)
	c.Assert(err, IsNil)

	select {
	case <-queue:
		c.Fatal("should not enqueue when fees are unchanged")
	default:
	}
}
