package cardano

import (
	"encoding/hex"
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
func (m *mockClientWrapper) Close() {}

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
// rollBackwardHandler
// ---------------------------------------------------------------------------

func makeIds(ids ...string) [][]byte {
	result := make([][]byte, len(ids))
	for i, id := range ids {
		result[i], _ = hex.DecodeString(id)
	}

	return result
}

// storeTestBlocks stores blocks 1..n via rollForwardHandler with slot = height*100.
func storeTestBlocks(c *C, scanner *Scanner, n uint64) {
	for i := uint64(1); i <= n; i++ {
		hash := lcommon.Blake2b256Hash([]byte{byte(i)})
		header := &mockBlockHeader{
			blockNumber: i,
			slotNumber:  i * 100,
			hash:        hash,
		}
		err := scanner.rollForwardHandler(
			chainsync.CallbackContext{}, 0, header, pcommon.Tip{},
		)
		c.Assert(err, IsNil)
	}
}

func (s *ScannerTestSuite) TestRollBackwardHandler(c *C) {
	scanner := makeTestScanner(c)
	errataQueue := make(chan types.ErrataBlock, 10)
	scanner.globalErrataQueue = errataQueue

	// store 5 blocks: height 1..5, slot 100..500
	storeTestBlocks(c, scanner, 5)

	// store observed txs at heights 3, 4, and 5
	c.Assert(scanner.db.SetObservedTxs(3, makeIds("tx3a")), IsNil)
	c.Assert(scanner.db.SetObservedTxs(4, makeIds("tx4a", "tx4b")), IsNil)
	c.Assert(scanner.db.SetObservedTxs(5, makeIds("tx5a")), IsNil)

	// rollback to slot 200 (block 2) — blocks 3, 4, 5 are invalidated
	point := pcommon.NewPoint(200, []byte("rollback-hash"))
	tip := pcommon.Tip{Point: pcommon.NewPoint(200, []byte("rollback-hash"))}
	err := scanner.rollBackwardHandler(chainsync.CallbackContext{}, point, tip)
	c.Assert(err, IsNil)

	// collect errata blocks from queue
	errata := make(map[int64][]string)
	for len(errataQueue) > 0 {
		errataBlock := <-errataQueue
		c.Assert(errataBlock.Txs[0].Chain, Equals, common.ADAChain)

		var txIds []string
		for _, tx := range errataBlock.Txs {
			txIds = append(txIds, string(tx.TxID))
		}
		errata[errataBlock.Height] = txIds
	}

	// should have errata for heights 3, 4, 5
	// rollBackwardHandler hex-encodes the raw bytes, so expected values are hex
	c.Assert(len(errata), Equals, 3)
	c.Assert(errata[3], DeepEquals, []string{"tx3a"})
	c.Assert(errata[4], DeepEquals, []string{"tx4a", "tx4b"})
	c.Assert(errata[5], DeepEquals, []string{"tx5a"})

	// invalidated block IDs should be deleted
	_, _, _, err = scanner.db.GetBlockId(3)
	c.Assert(err, NotNil)
	_, _, _, err = scanner.db.GetBlockId(4)
	c.Assert(err, NotNil)
	_, _, _, err = scanner.db.GetBlockId(5)
	c.Assert(err, NotNil)

	// blocks 1 and 2 should still exist
	h, sl, _, err := scanner.db.GetBlockId(1)
	c.Assert(err, IsNil)
	c.Assert(h, Equals, uint64(1))
	c.Assert(sl, Equals, uint64(100))

	h, sl, _, err = scanner.db.GetBlockId(2)
	c.Assert(err, IsNil)
	c.Assert(h, Equals, uint64(2))
	c.Assert(sl, Equals, uint64(200))

	// GetLastBlockId should now return block 2
	h, sl, _, err = scanner.db.GetLastBlockId()
	c.Assert(err, IsNil)
	c.Assert(h, Equals, uint64(2))
	c.Assert(sl, Equals, uint64(200))

	// observed txs should be cleaned up at invalidated heights
	txs, err := scanner.db.GetObservedTxs(3)
	c.Assert(err, IsNil)
	c.Assert(txs, IsNil)
	txs, err = scanner.db.GetObservedTxs(4)
	c.Assert(err, IsNil)
	c.Assert(txs, IsNil)
	txs, err = scanner.db.GetObservedTxs(5)
	c.Assert(err, IsNil)
	c.Assert(txs, IsNil)

	// scan position should be set to the rollback height
	scanPos, err := scanner.db.GetScanPos()
	c.Assert(err, IsNil)
	c.Assert(scanPos, Equals, int64(2))
}

func (s *ScannerTestSuite) TestRollBackwardHandlerNoInvalidatedBlocks(c *C) {
	scanner := makeTestScanner(c)
	errataQueue := make(chan types.ErrataBlock, 10)
	scanner.globalErrataQueue = errataQueue

	// store 3 blocks: height 1..3, slot 100..300
	storeTestBlocks(c, scanner, 3)

	// rollback to slot 300 (block 3's slot) — nothing invalidated
	point := pcommon.NewPoint(300, []byte("hash"))
	tip := pcommon.Tip{Point: pcommon.NewPoint(300, []byte("hash"))}
	err := scanner.rollBackwardHandler(chainsync.CallbackContext{}, point, tip)
	c.Assert(err, IsNil)

	// no errata should be sent
	c.Assert(len(errataQueue), Equals, 0)

	// all blocks should still exist
	for i := uint64(1); i <= 3; i++ {
		h, _, _, err := scanner.db.GetBlockId(i)
		c.Assert(err, IsNil)
		c.Assert(h, Equals, i)
	}
}

func (s *ScannerTestSuite) TestRollBackwardHandlerNoObservedTxs(c *C) {
	scanner := makeTestScanner(c)
	errataQueue := make(chan types.ErrataBlock, 10)
	scanner.globalErrataQueue = errataQueue

	// store 3 blocks but no observed txs
	storeTestBlocks(c, scanner, 3)

	// rollback to slot 100 — blocks 2 and 3 invalidated but no txs to errata
	point := pcommon.NewPoint(100, []byte("hash"))
	tip := pcommon.Tip{Point: pcommon.NewPoint(100, []byte("hash"))}
	err := scanner.rollBackwardHandler(chainsync.CallbackContext{}, point, tip)
	c.Assert(err, IsNil)

	// no errata sent (no observed txs at invalidated heights)
	c.Assert(len(errataQueue), Equals, 0)

	// invalidated block IDs should still be deleted
	_, _, _, err = scanner.db.GetBlockId(2)
	c.Assert(err, NotNil)
	_, _, _, err = scanner.db.GetBlockId(3)
	c.Assert(err, NotNil)

	// block 1 should still exist
	h, _, _, err := scanner.db.GetBlockId(1)
	c.Assert(err, IsNil)
	c.Assert(h, Equals, uint64(1))

	// scan position should be set to block 1
	scanPos, err := scanner.db.GetScanPos()
	c.Assert(err, IsNil)
	c.Assert(scanPos, Equals, int64(1))
}

func (s *ScannerTestSuite) TestRollBackwardHandlerNilErrataQueue(c *C) {
	scanner := makeTestScanner(c)
	// globalErrataQueue is nil — should not panic

	storeTestBlocks(c, scanner, 3)
	c.Assert(scanner.db.SetObservedTxs(3, makeIds("txA")), IsNil)

	// rollback to slot 200 — block 3 invalidated
	point := pcommon.NewPoint(200, []byte("hash"))
	tip := pcommon.Tip{Point: pcommon.NewPoint(200, []byte("hash"))}
	err := scanner.rollBackwardHandler(chainsync.CallbackContext{}, point, tip)
	c.Assert(err, IsNil)

	// block 3 should still be cleaned up even without errata queue
	_, _, _, err = scanner.db.GetBlockId(3)
	c.Assert(err, NotNil)

	txs, err := scanner.db.GetObservedTxs(3)
	c.Assert(err, IsNil)
	c.Assert(txs, IsNil)
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
