package substrate

import (
	"strings"
	"testing"

	"github.com/rs/zerolog/log"

	stypes "gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/substrate/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/config"
	"gitlab.com/thorchain/thornode/v3/constants"
	. "gopkg.in/check.v1"
)

func TestScannerSuite(t *testing.T) { TestingT(t) }

type ScannerSuite struct{}

var _ = Suite(&ScannerSuite{})

// newTestScanner creates a minimal scanner for unit testing parse functions.
func newTestScanner() *Scanner {
	s := &Scanner{
		logger: log.Logger,
		params: ChainParams[common.TAOChain],
		cfg: config.BifrostBlockScannerConfiguration{
			ChainID: common.TAOChain,
		},
	}
	s.currentFee.Swap(s.params.DefaultFee)
	return s
}

// buildTestExtrinsic constructs a hex-encoded signed extrinsic for testing.
// Uses a dummy signer and signature — only the call data varies.
func buildTestExtrinsic(callData []byte) string {
	signer := make([]byte, 32)
	for i := range signer {
		signer[i] = byte(i + 1)
	}

	ext := &stypes.Extrinsic{
		Signer:            signer,
		SignatureType:     stypes.SignatureTypeEd25519,
		Signature:         make([]byte, 64),
		Era:               stypes.ImmortalEra(),
		Nonce:             0,
		Tip:               0,
		MetadataMode:      stypes.MetadataModeDisabled,
		CheckMetadataHash: true,
		CallData:          callData,
	}

	return stypes.HexEncode(stypes.EncodeSignedExtrinsic(ext))
}

// ---------------------------------------------------------------------------
// isTransferCall
// ---------------------------------------------------------------------------

func (s *ScannerSuite) TestIsTransferCall(c *C) {
	tests := []struct {
		callIdx  byte
		expected bool
	}{
		{0, true}, // transferAllowDeathCallIndex
		{3, true}, // transfer_keep_alive
		{1, false},
		{2, false},
		{4, false},
		{7, false}, // force_set_balance, NOT a transfer
		{255, false},
	}
	for _, tt := range tests {
		sc := newTestScanner()
		got := sc.isTransferCall(tt.callIdx)
		c.Check(got, Equals, tt.expected,
			Commentf("isTransferCall(%d): got %v, want %v", tt.callIdx, got, tt.expected))
	}
}

// ---------------------------------------------------------------------------
// convertRaoToThorchain
// ---------------------------------------------------------------------------

func (s *ScannerSuite) TestConvertRaoToThorchain(c *C) {
	tests := []struct {
		rao      uint64
		expected uint64
	}{
		{0, 0},
		{10, 1},
		{100_000, 10_000},
		{1_000_000_000, 100_000_000}, // 1 TAO → 1e8
		{9, 0},                       // truncation
		{15, 1},                      // truncation
		{1, 0},                       // below threshold
	}
	for _, tt := range tests {
		result := common.TAOChain.NativeGasToThorchain(cosmos.NewUint(tt.rao))
		c.Check(result.Equal(cosmos.NewUint(tt.expected)), Equals, true,
			Commentf("convertRaoToThorchain(%d): got %s, want %d", tt.rao, result, tt.expected))
	}
}

// ---------------------------------------------------------------------------
// processExtrinsic — simple transfer
// ---------------------------------------------------------------------------

func (s *ScannerSuite) TestProcessExtrinsic_SimpleTransfer(c *C) {
	sc := newTestScanner()

	dest := make([]byte, 32)
	dest[0] = 0xAA
	amount := cosmos.NewUint(1_000_000_000)
	call := stypes.BalanceTransferKeepAlive(5, 3, dest, amount)

	extHex := buildTestExtrinsic(call)

	txItem, err := sc.processExtrinsic(extHex, 100, 0)
	c.Assert(err, IsNil)
	c.Assert(txItem, NotNil, Commentf("expected non-nil TxInItem"))

	c.Check(txItem.BlockHeight, Equals, int64(100))
	c.Check(txItem.Tx, Not(Equals), "", Commentf("tx hash should not be empty"))
	c.Check(txItem.Sender, Not(Equals), "", Commentf("sender should not be empty"))
	c.Check(txItem.To, Not(Equals), "", Commentf("to should not be empty"))

	// 1_000_000_000 rao / 10 = 100_000_000
	expectedAmount := cosmos.NewUint(100_000_000)
	c.Check(txItem.Coins[0].Amount.Equal(expectedAmount), Equals, true,
		Commentf("amount: got %s, want %s", txItem.Coins[0].Amount, expectedAmount))
	c.Check(len(txItem.Gas) > 0, Equals, true, Commentf("gas should not be empty"))
}

func (s *ScannerSuite) TestProcessExtrinsic_TransferAllowDeath(c *C) {
	sc := newTestScanner()

	dest := make([]byte, 32)
	call := stypes.BalanceTransferKeepAlive(5, 0, dest, cosmos.NewUint(500_000)) // allow_death index

	extHex := buildTestExtrinsic(call)

	txItem, err := sc.processExtrinsic(extHex, 50, 0)
	c.Assert(err, IsNil)
	c.Assert(txItem, NotNil, Commentf("expected non-nil TxInItem for allow_death transfer"))
}

func (s *ScannerSuite) TestProcessExtrinsic_ForceSetBalanceIgnored(c *C) {
	sc := newTestScanner()

	dest := make([]byte, 32)
	// Call index 7 is force_set_balance (governance-only), should be ignored
	call := stypes.BalanceTransferKeepAlive(5, 7, dest, cosmos.NewUint(500_000))

	extHex := buildTestExtrinsic(call)

	txItem, err := sc.processExtrinsic(extHex, 50, 0)
	c.Assert(err, IsNil)
	c.Check(txItem, IsNil, Commentf("expected nil TxInItem for force_set_balance (call index 7)"))
}

func (s *ScannerSuite) TestProcessExtrinsic_ZeroAmount(c *C) {
	sc := newTestScanner()

	dest := make([]byte, 32)
	call := stypes.BalanceTransferKeepAlive(5, 3, dest, cosmos.NewUint(0))

	extHex := buildTestExtrinsic(call)

	txItem, err := sc.processExtrinsic(extHex, 100, 0)
	c.Assert(err, IsNil)
	c.Check(txItem, IsNil, Commentf("expected nil TxInItem for zero/dust amount"))
}

func (s *ScannerSuite) TestProcessExtrinsic_LargeAmount(c *C) {
	sc := newTestScanner()

	dest := make([]byte, 32)
	amount := cosmos.NewUint(21_000_000_000_000_000) // 21M TAO
	call := stypes.BalanceTransferKeepAlive(5, 3, dest, amount)

	extHex := buildTestExtrinsic(call)

	txItem, err := sc.processExtrinsic(extHex, 100, 0)
	c.Assert(err, IsNil)
	c.Assert(txItem, NotNil, Commentf("expected non-nil TxInItem"))

	expectedAmount := common.TAOChain.NativeGasToThorchain(amount)
	c.Check(txItem.Coins[0].Amount.Equal(expectedAmount), Equals, true,
		Commentf("amount: got %s, want %s", txItem.Coins[0].Amount, expectedAmount))
}

// ---------------------------------------------------------------------------
// processExtrinsic — batch_all with memo
// ---------------------------------------------------------------------------

func (s *ScannerSuite) TestProcessExtrinsic_BatchWithMemo(c *C) {
	sc := newTestScanner()

	dest := make([]byte, 32)
	dest[0] = 0xBB
	amount := cosmos.NewUint(2_000_000_000)

	transferCall := stypes.BalanceTransferKeepAlive(5, 3, dest, amount)
	remarkCall := stypes.SystemRemark(0, 0, []byte("SWAP:BTC.BTC:bc1qtest"))
	batchCall := stypes.BatchAll(11, 2, [][]byte{transferCall, remarkCall})

	extHex := buildTestExtrinsic(batchCall)

	txItem, err := sc.processExtrinsic(extHex, 200, 0)
	c.Assert(err, IsNil)
	c.Assert(txItem, NotNil, Commentf("expected non-nil TxInItem"))

	c.Check(txItem.Memo, Equals, "SWAP:BTC.BTC:bc1qtest")

	expectedAmount := cosmos.NewUint(200_000_000) // 2e9 / 10
	c.Check(txItem.Coins[0].Amount.Equal(expectedAmount), Equals, true,
		Commentf("amount: got %s, want %s", txItem.Coins[0].Amount, expectedAmount))
}

func (s *ScannerSuite) TestProcessExtrinsic_BatchRemarkBeforeTransfer(c *C) {
	sc := newTestScanner()

	dest := make([]byte, 32)
	amount := cosmos.NewUint(1_000_000_000)

	// Remark comes BEFORE transfer in the batch
	remarkCall := stypes.SystemRemark(0, 0, []byte("ADD:TAO.TAO"))
	transferCall := stypes.BalanceTransferKeepAlive(5, 3, dest, amount)
	batchCall := stypes.BatchAll(11, 2, [][]byte{remarkCall, transferCall})

	extHex := buildTestExtrinsic(batchCall)

	txItem, err := sc.processExtrinsic(extHex, 100, 0)
	c.Assert(err, IsNil)
	c.Assert(txItem, NotNil, Commentf("expected non-nil TxInItem"))

	c.Check(txItem.Memo, Equals, "ADD:TAO.TAO")

	expectedAmount := cosmos.NewUint(100_000_000)
	c.Check(txItem.Coins[0].Amount.Equal(expectedAmount), Equals, true,
		Commentf("amount: got %s, want %s", txItem.Coins[0].Amount, expectedAmount))
}

// ---------------------------------------------------------------------------
// processExtrinsic — batch edge cases
// ---------------------------------------------------------------------------

func (s *ScannerSuite) TestProcessExtrinsic_BatchMultiTransferRejected(c *C) {
	sc := newTestScanner()

	dest1 := make([]byte, 32)
	dest1[0] = 0x11
	dest2 := make([]byte, 32)
	dest2[0] = 0x22

	// Batch with multiple transfers must be rejected to prevent ambiguity
	transfer1 := stypes.BalanceTransferKeepAlive(5, 3, dest1, cosmos.NewUint(10_000_000_000))
	transfer2 := stypes.BalanceTransferKeepAlive(5, 3, dest2, cosmos.NewUint(100))
	remarkCall := stypes.SystemRemark(0, 0, []byte("memo"))
	batchCall := stypes.BatchAll(11, 2, [][]byte{transfer1, transfer2, remarkCall})

	extHex := buildTestExtrinsic(batchCall)

	_, err := sc.processExtrinsic(extHex, 100, 0)
	c.Assert(err, NotNil, Commentf("expected error for batch with multiple transfers"))
	c.Check(strings.Contains(err.Error(), "multiple transfer"), Equals, true,
		Commentf("error should mention multiple transfers, got: %v", err))
}

func (s *ScannerSuite) TestProcessExtrinsic_BatchRemarkOnlyReturnsNil(c *C) {
	sc := newTestScanner()

	remarkCall := stypes.SystemRemark(0, 0, []byte("just a remark"))
	batchCall := stypes.BatchAll(11, 2, [][]byte{remarkCall})

	extHex := buildTestExtrinsic(batchCall)

	txItem, err := sc.processExtrinsic(extHex, 100, 0)
	c.Assert(err, IsNil)
	c.Check(txItem, IsNil, Commentf("expected nil for batch with only remark (no transfer)"))
}

func (s *ScannerSuite) TestProcessExtrinsic_BatchMultipleRemarksRejected(c *C) {
	sc := newTestScanner()

	dest := make([]byte, 32)
	dest[0] = 0xCC
	amount := cosmos.NewUint(1_000_000_000)

	transferCall := stypes.BalanceTransferKeepAlive(5, 3, dest, amount)
	remark1 := stypes.SystemRemark(0, 0, []byte("SWAP:BTC.BTC:bc1q1"))
	remark2 := stypes.SystemRemark(0, 0, []byte("SWAP:BTC.BTC:bc1q2"))
	batchCall := stypes.BatchAll(11, 2, [][]byte{remark1, transferCall, remark2})

	extHex := buildTestExtrinsic(batchCall)

	_, err := sc.processExtrinsic(extHex, 100, 0)
	c.Assert(err, NotNil, Commentf("expected error for batch with multiple remarks"))
}

func (s *ScannerSuite) TestProcessExtrinsic_BatchUnknownCallUsesFirstTransfer(c *C) {
	sc := newTestScanner()

	dest := make([]byte, 32)
	dest[0] = 0xDD
	amount := cosmos.NewUint(5_000_000_000) // 5 TAO

	transferCall := stypes.BalanceTransferKeepAlive(5, 3, dest, amount)
	remarkCall := stypes.SystemRemark(0, 0, []byte("ADD:TAO.TAO"))
	// Unknown call from pallet 99 — parser breaks here
	unknownCall := []byte{99, 1, 0xFF, 0xFF}
	batchCall := stypes.BatchAll(11, 2, [][]byte{transferCall, remarkCall, unknownCall})

	extHex := buildTestExtrinsic(batchCall)

	txItem, err := sc.processExtrinsic(extHex, 100, 0)
	c.Assert(err, IsNil)
	c.Assert(txItem, NotNil, Commentf("expected non-nil TxInItem"))

	// Transfer should still be captured despite unknown call
	expectedAmount := cosmos.NewUint(500_000_000) // 5e9 / 10
	c.Check(txItem.Coins[0].Amount.Equal(expectedAmount), Equals, true,
		Commentf("amount: got %s, want %s", txItem.Coins[0].Amount, expectedAmount))
	c.Check(txItem.Memo, Equals, "ADD:TAO.TAO")
}

// ---------------------------------------------------------------------------
// processExtrinsic — edge cases and error paths
// ---------------------------------------------------------------------------

func (s *ScannerSuite) TestProcessExtrinsic_UnsignedReturnsNil(c *C) {
	sc := newTestScanner()

	// Build an unsigned extrinsic (version byte 0x04, bit 7 not set)
	body := []byte{0x04, 5, 3}
	length := stypes.EncodeCompact(uint64(len(body)))
	extHex := stypes.HexEncode(append(length, body...))

	txItem, err := sc.processExtrinsic(extHex, 100, 0)
	c.Assert(err, IsNil)
	c.Check(txItem, IsNil, Commentf("expected nil for unsigned extrinsic"))
}

func (s *ScannerSuite) TestProcessExtrinsic_UnknownPalletReturnsNil(c *C) {
	sc := newTestScanner()

	call := []byte{99, 0, 0x00} // unknown pallet 99
	extHex := buildTestExtrinsic(call)

	txItem, err := sc.processExtrinsic(extHex, 100, 0)
	c.Assert(err, IsNil)
	c.Check(txItem, IsNil, Commentf("expected nil for unknown pallet"))
}

func (s *ScannerSuite) TestProcessExtrinsic_EmptyHex(c *C) {
	sc := newTestScanner()
	_, err := sc.processExtrinsic("", 100, 0)
	c.Check(err, NotNil, Commentf("expected error for empty hex"))
}

func (s *ScannerSuite) TestProcessExtrinsic_MalformedHex(c *C) {
	sc := newTestScanner()
	_, err := sc.processExtrinsic("0xZZZZ", 100, 0)
	c.Check(err, NotNil, Commentf("expected error for malformed hex"))
}

func (s *ScannerSuite) TestProcessExtrinsic_TruncatedExtrinsic(c *C) {
	sc := newTestScanner()

	// Valid length prefix claiming 200 bytes but only 3 bytes of body
	length := stypes.EncodeCompact(200)
	extHex := stypes.HexEncode(append(length, 0x84, 0x00, 0x01))

	_, err := sc.processExtrinsic(extHex, 100, 0)
	c.Check(err, NotNil, Commentf("expected error for truncated extrinsic"))
}

// ---------------------------------------------------------------------------
// processExtrinsic — signature type handling
// ---------------------------------------------------------------------------

func (s *ScannerSuite) TestProcessExtrinsic_ECDSASignature(c *C) {
	sc := newTestScanner()

	dest := make([]byte, 32)
	dest[0] = 0xEE
	call := stypes.BalanceTransferKeepAlive(5, 3, dest, cosmos.NewUint(1_000_000_000))

	signer := make([]byte, 32)
	for i := range signer {
		signer[i] = byte(i + 1)
	}

	// ECDSA (type 0x02) uses 65-byte signatures
	ext := &stypes.Extrinsic{
		Signer:            signer,
		SignatureType:     0x02, // ECDSA
		Signature:         make([]byte, 65),
		Era:               stypes.ImmortalEra(),
		Nonce:             0,
		Tip:               0,
		MetadataMode:      stypes.MetadataModeDisabled,
		CheckMetadataHash: true,
		CallData:          call,
	}
	extHex := stypes.HexEncode(stypes.EncodeSignedExtrinsic(ext))

	txItem, err := sc.processExtrinsic(extHex, 100, 0)
	c.Assert(err, IsNil, Commentf("processExtrinsic with ECDSA sig"))
	c.Assert(txItem, NotNil, Commentf("expected non-nil TxInItem for ECDSA extrinsic"))

	expectedAmount := cosmos.NewUint(100_000_000) // 1e9 / 10
	c.Check(txItem.Coins[0].Amount.Equal(expectedAmount), Equals, true,
		Commentf("amount: got %s, want %s", txItem.Coins[0].Amount, expectedAmount))
}

func (s *ScannerSuite) TestProcessExtrinsic_UnknownSignatureTypeRejected(c *C) {
	sc := newTestScanner()

	dest := make([]byte, 32)
	call := stypes.BalanceTransferKeepAlive(5, 3, dest, cosmos.NewUint(500_000))

	signer := make([]byte, 32)
	for i := range signer {
		signer[i] = byte(i + 1)
	}

	// Unknown signature type 0x03
	ext := &stypes.Extrinsic{
		Signer:            signer,
		SignatureType:     0x03,
		Signature:         make([]byte, 64),
		Era:               stypes.ImmortalEra(),
		Nonce:             0,
		Tip:               0,
		MetadataMode:      stypes.MetadataModeDisabled,
		CheckMetadataHash: true,
		CallData:          call,
	}
	extHex := stypes.HexEncode(stypes.EncodeSignedExtrinsic(ext))

	_, err := sc.processExtrinsic(extHex, 100, 0)
	c.Check(err, NotNil, Commentf("expected error for unknown signature type 0x03"))
}

// ---------------------------------------------------------------------------
// processExtrinsic — mortal era parsing
// ---------------------------------------------------------------------------

func (s *ScannerSuite) TestProcessExtrinsic_MortalEra(c *C) {
	sc := newTestScanner()

	dest := make([]byte, 32)
	call := stypes.BalanceTransferKeepAlive(5, 3, dest, cosmos.NewUint(500_000))

	// Build extrinsic with mortal era instead of immortal
	signer := make([]byte, 32)
	for i := range signer {
		signer[i] = byte(i + 1)
	}

	ext := &stypes.Extrinsic{
		Signer:            signer,
		SignatureType:     stypes.SignatureTypeEd25519,
		Signature:         make([]byte, 64),
		Era:               stypes.MortalEra(64, 42),
		Nonce:             7,
		Tip:               100,
		MetadataMode:      stypes.MetadataModeDisabled,
		CheckMetadataHash: true,
		CallData:          call,
	}
	extHex := stypes.HexEncode(stypes.EncodeSignedExtrinsic(ext))

	txItem, err := sc.processExtrinsic(extHex, 100, 0)
	c.Assert(err, IsNil)
	c.Assert(txItem, NotNil, Commentf("expected non-nil TxInItem with mortal era"))

	expectedAmount := cosmos.NewUint(50_000) // 500_000 / 10
	c.Check(txItem.Coins[0].Amount.Equal(expectedAmount), Equals, true,
		Commentf("amount: got %s, want %s", txItem.Coins[0].Amount, expectedAmount))
}

// ---------------------------------------------------------------------------
// Extrinsic hash consistency between scanner and types
// ---------------------------------------------------------------------------

func (s *ScannerSuite) TestExtrinsicHashConsistency(c *C) {
	dest := make([]byte, 32)
	call := stypes.BalanceTransferKeepAlive(5, 3, dest, cosmos.NewUint(1_000_000))
	raw := stypes.EncodeSignedExtrinsic(&stypes.Extrinsic{
		Signer:            make([]byte, 32),
		SignatureType:     stypes.SignatureTypeEd25519,
		Signature:         make([]byte, 64),
		Era:               stypes.ImmortalEra(),
		Nonce:             0,
		Tip:               0,
		MetadataMode:      stypes.MetadataModeDisabled,
		CheckMetadataHash: true,
		CallData:          call,
	})

	// Both should produce the same hash for the same raw bytes
	hash1 := stypes.ExtrinsicHash(raw)
	hash2 := stypes.ExtrinsicHash(raw)
	c.Check(hash1, Equals, hash2, Commentf("inconsistent hashes"))
	c.Check(hash1[:2], Equals, "0x", Commentf("hash should have 0x prefix"))
}

// ---------------------------------------------------------------------------
// Fee cache tests
// ---------------------------------------------------------------------------

func (s *ScannerSuite) TestUpdateFeeCache(c *C) {
	sc := newTestScanner()

	sc.updateFeeCache(100_000)
	sc.updateFeeCache(200_000)
	sc.updateFeeCache(150_000)

	sc.mu.RLock()
	cacheLen := len(sc.feeCache)
	sc.mu.RUnlock()

	c.Check(cacheLen, Equals, 3)
}

func (s *ScannerSuite) TestUpdateFeeCacheOverflow(c *C) {
	sc := newTestScanner()

	// Fill past cache size
	for i := 0; i < FeeCacheSize+50; i++ {
		sc.updateFeeCache(uint64(1000 + i))
	}

	sc.mu.RLock()
	cacheLen := len(sc.feeCache)
	sc.mu.RUnlock()

	c.Check(cacheLen, Equals, FeeCacheSize)
}

func (s *ScannerSuite) TestGetNetworkFee_Default(c *C) {
	sc := newTestScanner()
	txSize, txRate := sc.GetNetworkFee()
	c.Check(txSize, Equals, uint64(1))
	c.Check(txRate, Equals, ChainParams[common.TAOChain].DefaultFee)
}

func (s *ScannerSuite) TestGetNetworkFee_Cached(c *C) {
	sc := newTestScanner()

	sc.updateFeeCache(80_000)
	sc.updateFeeCache(120_000)

	txSize, txRate := sc.GetNetworkFee()
	c.Check(txSize, Equals, uint64(1))
	expected := uint64(100_000) // (80k + 120k) / 2
	c.Check(txRate, Equals, expected)
}

// ---------------------------------------------------------------------------
// updateNetworkFee tests
// ---------------------------------------------------------------------------

func newTestScannerWithFeeQueue(resolution int64) (*Scanner, chan common.NetworkFee) {
	ch := make(chan common.NetworkFee, 4)
	sc := &Scanner{
		logger: log.Logger,
		params: ChainParams[common.TAOChain],
		cfg: config.BifrostBlockScannerConfiguration{
			ChainID:            common.TAOChain,
			GasPriceResolution: resolution,
		},
		globalNetworkFeeQueue: ch,
	}
	return sc, ch
}

func (s *ScannerSuite) TestReportNetworkFee_BootstrapsWithDefaultFee(c *C) {
	sc, ch := newTestScannerWithFeeQueue(1000)

	// Add fewer than FeeCacheSize samples
	for i := 0; i < FeeCacheSize-1; i++ {
		sc.updateFeeCache(100_000)
	}

	sc.updateNetworkFee(100)

	select {
	case fee := <-ch:
		c.Check(fee.TransactionRate, Equals, sc.params.DefaultFee,
			Commentf("fee rate: got %d, want %d (DefaultFee)", fee.TransactionRate, sc.params.DefaultFee))
	default:
		c.Error("expected DefaultFee to be reported on bootstrap with partial cache")
	}
}

func (s *ScannerSuite) TestReportNetworkFee_ReportsRoundedFee(c *C) {
	sc, ch := newTestScannerWithFeeQueue(1000)

	// Fill cache completely with 100_500 rao average → rounds up to 101_000
	for i := 0; i < FeeCacheSize; i++ {
		sc.updateFeeCache(100_500)
	}

	sc.updateNetworkFee(100)

	select {
	case fee := <-ch:
		c.Check(fee.TransactionRate, Equals, uint64(101_000),
			Commentf("fee rate: got %d, want 101000 (rounded up to resolution)", fee.TransactionRate))
		c.Check(fee.TransactionSize, Equals, uint64(1))
		c.Check(fee.Height, Equals, int64(100))
		c.Check(fee.Chain.Equals(common.TAOChain), Equals, true,
			Commentf("chain: got %s, want TAO", fee.Chain))
	default:
		c.Error("expected fee to be reported")
	}
}

func (s *ScannerSuite) TestReportNetworkFee_SkipsUnchanged(c *C) {
	sc, ch := newTestScannerWithFeeQueue(1000)

	// Fill cache — average = 100_000, rounds to 100_000 (already aligned)
	for i := 0; i < FeeCacheSize; i++ {
		sc.updateFeeCache(100_000)
	}

	// First report should send
	sc.updateNetworkFee(100)
	select {
	case <-ch:
		// expected
	default:
		c.Fatal("first report should send")
	}

	// Second report with identical fees should skip
	sc.updateNetworkFee(200)
	select {
	case <-ch:
		c.Error("should skip unchanged fee")
	default:
		// expected — fee unchanged
	}
}

func (s *ScannerSuite) TestReportNetworkFee_ReportsOnChange(c *C) {
	sc, ch := newTestScannerWithFeeQueue(1000)

	// Fill cache with 100_000
	for i := 0; i < FeeCacheSize; i++ {
		sc.updateFeeCache(100_000)
	}

	sc.updateNetworkFee(100)
	<-ch // consume first report

	// Replace cache with higher fees (200_000)
	for i := 0; i < FeeCacheSize; i++ {
		sc.updateFeeCache(200_000)
	}

	sc.updateNetworkFee(200)
	select {
	case fee := <-ch:
		c.Check(fee.TransactionRate, Equals, uint64(200_000),
			Commentf("fee rate: got %d, want 200000", fee.TransactionRate))
	default:
		c.Error("expected fee to be reported after change")
	}
}

// ---------------------------------------------------------------------------
// processExtrinsic — actual fee usage
// ---------------------------------------------------------------------------

func (s *ScannerSuite) TestProcessExtrinsic_UsesActualFee(c *C) {
	sc := newTestScanner()

	dest := make([]byte, 32)
	dest[0] = 0xAA
	amount := cosmos.NewUint(1_000_000_000)
	call := stypes.BalanceTransferKeepAlive(5, 3, dest, amount)
	extHex := buildTestExtrinsic(call)

	// Pass actual fee of 150_000
	txItem, err := sc.processExtrinsic(extHex, 100, 150_000)
	c.Assert(err, IsNil)
	c.Assert(txItem, NotNil, Commentf("expected non-nil TxInItem"))

	// Should use actual fee: 150_000 / 10 = 15_000 + 1 (+1 from CoinCeil)
	expectedGas := cosmos.NewUint(15_001)
	c.Assert(len(txItem.Gas) > 0, Equals, true, Commentf("gas should not be empty"))
	c.Check(txItem.Gas[0].Amount.Equal(expectedGas), Equals, true,
		Commentf("gas amount: got %s, want %s", txItem.Gas[0].Amount, expectedGas))
}

func (s *ScannerSuite) TestProcessExtrinsic_FallsBackToDefaultFee(c *C) {
	sc := newTestScanner()

	dest := make([]byte, 32)
	call := stypes.BalanceTransferKeepAlive(5, 3, dest, cosmos.NewUint(500_000))
	extHex := buildTestExtrinsic(call)

	// Pass 0 for actualFee — gas falls back to 1 (minimum placeholder)
	txItem, err := sc.processExtrinsic(extHex, 100, 0)
	c.Assert(err, IsNil)
	c.Assert(txItem, NotNil, Commentf("expected non-nil TxInItem"))

	// Gas uses minimum placeholder when actual fee is unavailable
	expectedGas := cosmos.NewUint(1)
	c.Assert(len(txItem.Gas) > 0, Equals, true, Commentf("gas should not be empty"))
	c.Check(txItem.Gas[0].Amount.Equal(expectedGas), Equals, true,
		Commentf("gas amount: got %s, want %s (should use minimum placeholder)", txItem.Gas[0].Amount, expectedGas))
}

// ---------------------------------------------------------------------------
// processExtrinsic — memo size validation
// ---------------------------------------------------------------------------

func (s *ScannerSuite) TestProcessExtrinsic_OversizedMemoDropped(c *C) {
	sc := newTestScanner()

	dest := make([]byte, 32)
	dest[0] = 0xBB
	amount := cosmos.NewUint(2_000_000_000)

	// Build a memo that exceeds MaxMemoSize (250 bytes)
	longMemo := strings.Repeat("A", constants.MaxMemoSize+1)

	transferCall := stypes.BalanceTransferKeepAlive(5, 3, dest, amount)
	remarkCall := stypes.SystemRemark(0, 0, []byte(longMemo))
	batchCall := stypes.BatchAll(11, 2, [][]byte{transferCall, remarkCall})
	extHex := buildTestExtrinsic(batchCall)

	txItem, err := sc.processExtrinsic(extHex, 200, 0)
	c.Assert(err, IsNil)
	c.Check(txItem, IsNil, Commentf("expected nil TxInItem for oversized memo"))
}

func (s *ScannerSuite) TestProcessExtrinsic_MaxSizeMemoAccepted(c *C) {
	sc := newTestScanner()

	dest := make([]byte, 32)
	dest[0] = 0xCC
	amount := cosmos.NewUint(1_000_000_000)

	// Build a memo that is exactly MaxMemoSize (250 bytes) — should be accepted
	exactMemo := strings.Repeat("B", constants.MaxMemoSize)

	transferCall := stypes.BalanceTransferKeepAlive(5, 3, dest, amount)
	remarkCall := stypes.SystemRemark(0, 0, []byte(exactMemo))
	batchCall := stypes.BatchAll(11, 2, [][]byte{transferCall, remarkCall})
	extHex := buildTestExtrinsic(batchCall)

	txItem, err := sc.processExtrinsic(extHex, 200, 0)
	c.Assert(err, IsNil)
	c.Assert(txItem, NotNil, Commentf("expected non-nil TxInItem for max-size memo"))
	c.Check(txItem.Memo, Equals, exactMemo,
		Commentf("memo length: got %d, want %d", len(txItem.Memo), constants.MaxMemoSize))
}

func (s *ScannerSuite) TestProcessExtrinsic_NoMemoTransferAccepted(c *C) {
	sc := newTestScanner()

	dest := make([]byte, 32)
	dest[0] = 0xDD
	amount := cosmos.NewUint(1_000_000_000)

	// Direct transfer (no batch, no memo) — memo is empty, should always pass
	call := stypes.BalanceTransferKeepAlive(5, 3, dest, amount)
	extHex := buildTestExtrinsic(call)

	txItem, err := sc.processExtrinsic(extHex, 100, 0)
	c.Assert(err, IsNil)
	c.Assert(txItem, NotNil, Commentf("expected non-nil TxInItem"))
	c.Check(txItem.Memo, Equals, "", Commentf("memo: got %q, want empty", txItem.Memo))
}
