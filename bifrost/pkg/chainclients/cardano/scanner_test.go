package cardano

import (
	lcommon "github.com/blinklabs-io/gouroboros/ledger/common"
	"github.com/blinklabs-io/gouroboros/protocol/chainsync"
	pcommon "github.com/blinklabs-io/gouroboros/protocol/common"
	"github.com/rs/zerolog"
	"gitlab.com/thorchain/thornode/v3/bifrost/blockscanner"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/config"
	. "gopkg.in/check.v1"
)

type ScannerTestSuite struct{}

var _ = Suite(&ScannerTestSuite{})

func makeTestScanner(c *C) *Scanner {
	storage, err := blockscanner.NewBlockScannerStorageCardano("", config.LevelDBOptions{})
	c.Assert(err, IsNil)

	return &Scanner{
		logger: zerolog.Nop(),
		config: config.BifrostBlockScannerConfiguration{
			ChainID:            common.ADAChain,
			GasPriceResolution: 100_000,
		},
		db: storage,
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
