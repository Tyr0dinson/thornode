package substrate

import (
	"strings"
	"testing"

	"github.com/rs/zerolog/log"

	taotypes "gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/bittensor/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/config"
	"gitlab.com/thorchain/thornode/v3/constants"
)

// newTestScanner creates a minimal scanner for unit testing parse functions.
func newTestScanner() *TAOScanner {
	return &TAOScanner{
		logger:                        log.Logger,
		balancesPalletIndex:           5,
		transferCallIndex:             transferKeepAliveCallIndex,
		systemPalletIndex:             0,
		remarkCallIndex:               0,
		utilityPalletIndex:            11,
		batchAllCallIndex:             2,
		transactionPaymentPalletIndex: 6,
	}
}

// buildTestExtrinsic constructs a hex-encoded signed extrinsic for testing.
// Uses a dummy signer and signature — only the call data varies.
func buildTestExtrinsic(callData []byte) string {
	signer := make([]byte, 32)
	for i := range signer {
		signer[i] = byte(i + 1)
	}

	ext := &taotypes.Extrinsic{
		Signer:        signer,
		SignatureType: taotypes.SignatureTypeEd25519,
		Signature:     make([]byte, 64),
		Era:           taotypes.ImmortalEra(),
		Nonce:         0,
		Tip:           0,
		MetadataMode:  taotypes.MetadataModeDisabled,
		CallData:      callData,
	}

	return taotypes.HexEncode(taotypes.EncodeSignedExtrinsic(ext))
}

// ---------------------------------------------------------------------------
// isTransferCall
// ---------------------------------------------------------------------------

func TestIsTransferCall(t *testing.T) {
	tests := []struct {
		callIdx  byte
		expected bool
	}{
		{0, true}, // transferAllowDeathCallIndex
		{3, true}, // transferKeepAliveCallIndex
		{1, false},
		{2, false},
		{4, false},
		{7, false}, // force_set_balance, NOT a transfer
		{255, false},
	}
	for _, tt := range tests {
		if got := isTransferCall(tt.callIdx); got != tt.expected {
			t.Errorf("isTransferCall(%d): got %v, want %v", tt.callIdx, got, tt.expected)
		}
	}
}

// ---------------------------------------------------------------------------
// convertRaoToThorchain
// ---------------------------------------------------------------------------

func TestConvertRaoToThorchain(t *testing.T) {
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
		result := convertRaoToThorchain(tt.rao)
		if !result.Equal(cosmos.NewUint(tt.expected)) {
			t.Errorf("convertRaoToThorchain(%d): got %s, want %d", tt.rao, result, tt.expected)
		}
	}
}

// ---------------------------------------------------------------------------
// processExtrinsic — simple transfer
// ---------------------------------------------------------------------------

func TestProcessExtrinsic_SimpleTransfer(t *testing.T) {
	s := newTestScanner()

	dest := make([]byte, 32)
	dest[0] = 0xAA
	amount := uint64(1_000_000_000)
	call := taotypes.BalanceTransferKeepAlive(5, 3, dest, amount)

	extHex := buildTestExtrinsic(call)

	txItem, err := s.processExtrinsic(extHex, 100, 0)
	if err != nil {
		t.Fatalf("processExtrinsic: %v", err)
	}
	if txItem == nil {
		t.Fatal("expected non-nil TxInItem")
	}

	if txItem.BlockHeight != 100 {
		t.Errorf("block height: got %d, want 100", txItem.BlockHeight)
	}
	if txItem.Tx == "" {
		t.Error("tx hash should not be empty")
	}
	if txItem.Sender == "" {
		t.Error("sender should not be empty")
	}
	if txItem.To == "" {
		t.Error("to should not be empty")
	}

	// 1_000_000_000 rao / 10 = 100_000_000
	expectedAmount := cosmos.NewUint(100_000_000)
	if !txItem.Coins[0].Amount.Equal(expectedAmount) {
		t.Errorf("amount: got %s, want %s", txItem.Coins[0].Amount, expectedAmount)
	}

	// gas should be set
	if len(txItem.Gas) == 0 {
		t.Error("gas should not be empty")
	}
}

func TestProcessExtrinsic_TransferAllowDeath(t *testing.T) {
	s := newTestScanner()

	dest := make([]byte, 32)
	call := taotypes.BalanceTransferKeepAlive(5, 0, dest, 500_000) // allow_death index

	extHex := buildTestExtrinsic(call)

	txItem, err := s.processExtrinsic(extHex, 50, 0)
	if err != nil {
		t.Fatalf("processExtrinsic: %v", err)
	}
	if txItem == nil {
		t.Fatal("expected non-nil TxInItem for allow_death transfer")
	}
}

func TestProcessExtrinsic_ForceSetBalanceIgnored(t *testing.T) {
	s := newTestScanner()

	dest := make([]byte, 32)
	// Call index 7 is force_set_balance (governance-only), should be ignored
	call := taotypes.BalanceTransferKeepAlive(5, 7, dest, 500_000)

	extHex := buildTestExtrinsic(call)

	txItem, err := s.processExtrinsic(extHex, 50, 0)
	if err != nil {
		t.Fatalf("processExtrinsic: %v", err)
	}
	if txItem != nil {
		t.Error("expected nil TxInItem for force_set_balance (call index 7)")
	}
}

func TestProcessExtrinsic_ZeroAmount(t *testing.T) {
	s := newTestScanner()

	dest := make([]byte, 32)
	call := taotypes.BalanceTransferKeepAlive(5, 3, dest, 0)

	extHex := buildTestExtrinsic(call)

	txItem, err := s.processExtrinsic(extHex, 100, 0)
	if err != nil {
		t.Fatalf("processExtrinsic: %v", err)
	}
	if txItem != nil {
		t.Error("expected nil TxInItem for zero/dust amount")
	}
}

func TestProcessExtrinsic_LargeAmount(t *testing.T) {
	s := newTestScanner()

	dest := make([]byte, 32)
	amount := uint64(21_000_000_000_000_000) // 21M TAO
	call := taotypes.BalanceTransferKeepAlive(5, 3, dest, amount)

	extHex := buildTestExtrinsic(call)

	txItem, err := s.processExtrinsic(extHex, 100, 0)
	if err != nil {
		t.Fatalf("processExtrinsic: %v", err)
	}
	if txItem == nil {
		t.Fatal("expected non-nil TxInItem")
	}

	expectedAmount := cosmos.NewUint(amount / 10)
	if !txItem.Coins[0].Amount.Equal(expectedAmount) {
		t.Errorf("amount: got %s, want %s", txItem.Coins[0].Amount, expectedAmount)
	}
}

// ---------------------------------------------------------------------------
// processExtrinsic — batch_all with memo
// ---------------------------------------------------------------------------

func TestProcessExtrinsic_BatchWithMemo(t *testing.T) {
	s := newTestScanner()

	dest := make([]byte, 32)
	dest[0] = 0xBB
	amount := uint64(2_000_000_000)

	transferCall := taotypes.BalanceTransferKeepAlive(5, 3, dest, amount)
	remarkCall := taotypes.SystemRemark(0, 0, []byte("SWAP:BTC.BTC:bc1qtest"))
	batchCall := taotypes.BatchAll(11, 2, [][]byte{transferCall, remarkCall})

	extHex := buildTestExtrinsic(batchCall)

	txItem, err := s.processExtrinsic(extHex, 200, 0)
	if err != nil {
		t.Fatalf("processExtrinsic: %v", err)
	}
	if txItem == nil {
		t.Fatal("expected non-nil TxInItem")
	}

	if txItem.Memo != "SWAP:BTC.BTC:bc1qtest" {
		t.Errorf("memo: got %q, want %q", txItem.Memo, "SWAP:BTC.BTC:bc1qtest")
	}

	expectedAmount := cosmos.NewUint(200_000_000) // 2e9 / 10
	if !txItem.Coins[0].Amount.Equal(expectedAmount) {
		t.Errorf("amount: got %s, want %s", txItem.Coins[0].Amount, expectedAmount)
	}
}

func TestProcessExtrinsic_BatchRemarkBeforeTransfer(t *testing.T) {
	s := newTestScanner()

	dest := make([]byte, 32)
	amount := uint64(1_000_000_000)

	// Remark comes BEFORE transfer in the batch
	remarkCall := taotypes.SystemRemark(0, 0, []byte("ADD:TAO.TAO"))
	transferCall := taotypes.BalanceTransferKeepAlive(5, 3, dest, amount)
	batchCall := taotypes.BatchAll(11, 2, [][]byte{remarkCall, transferCall})

	extHex := buildTestExtrinsic(batchCall)

	txItem, err := s.processExtrinsic(extHex, 100, 0)
	if err != nil {
		t.Fatalf("processExtrinsic: %v", err)
	}
	if txItem == nil {
		t.Fatal("expected non-nil TxInItem")
	}

	if txItem.Memo != "ADD:TAO.TAO" {
		t.Errorf("memo: got %q, want %q", txItem.Memo, "ADD:TAO.TAO")
	}

	expectedAmount := cosmos.NewUint(100_000_000)
	if !txItem.Coins[0].Amount.Equal(expectedAmount) {
		t.Errorf("amount: got %s, want %s", txItem.Coins[0].Amount, expectedAmount)
	}
}

// ---------------------------------------------------------------------------
// processExtrinsic — batch edge cases
// ---------------------------------------------------------------------------

func TestProcessExtrinsic_BatchMultiTransferRejected(t *testing.T) {
	s := newTestScanner()

	dest1 := make([]byte, 32)
	dest1[0] = 0x11
	dest2 := make([]byte, 32)
	dest2[0] = 0x22

	// Batch with multiple transfers must be rejected to prevent ambiguity
	transfer1 := taotypes.BalanceTransferKeepAlive(5, 3, dest1, 10_000_000_000)
	transfer2 := taotypes.BalanceTransferKeepAlive(5, 3, dest2, 100)
	remarkCall := taotypes.SystemRemark(0, 0, []byte("memo"))
	batchCall := taotypes.BatchAll(11, 2, [][]byte{transfer1, transfer2, remarkCall})

	extHex := buildTestExtrinsic(batchCall)

	_, err := s.processExtrinsic(extHex, 100, 0)
	if err == nil {
		t.Fatal("expected error for batch with multiple transfers")
	}
	if !strings.Contains(err.Error(), "multiple transfer") {
		t.Errorf("error should mention multiple transfers, got: %v", err)
	}
}

func TestProcessExtrinsic_BatchRemarkOnlyReturnsNil(t *testing.T) {
	s := newTestScanner()

	remarkCall := taotypes.SystemRemark(0, 0, []byte("just a remark"))
	batchCall := taotypes.BatchAll(11, 2, [][]byte{remarkCall})

	extHex := buildTestExtrinsic(batchCall)

	txItem, err := s.processExtrinsic(extHex, 100, 0)
	if err != nil {
		t.Fatalf("processExtrinsic: %v", err)
	}
	if txItem != nil {
		t.Error("expected nil for batch with only remark (no transfer)")
	}
}

func TestProcessExtrinsic_BatchMultipleRemarksRejected(t *testing.T) {
	s := newTestScanner()

	dest := make([]byte, 32)
	dest[0] = 0xCC
	amount := uint64(1_000_000_000)

	transferCall := taotypes.BalanceTransferKeepAlive(5, 3, dest, amount)
	remark1 := taotypes.SystemRemark(0, 0, []byte("SWAP:BTC.BTC:bc1q1"))
	remark2 := taotypes.SystemRemark(0, 0, []byte("SWAP:BTC.BTC:bc1q2"))
	batchCall := taotypes.BatchAll(11, 2, [][]byte{remark1, transferCall, remark2})

	extHex := buildTestExtrinsic(batchCall)

	_, err := s.processExtrinsic(extHex, 100, 0)
	if err == nil {
		t.Fatal("expected error for batch with multiple remarks")
	}
}

func TestProcessExtrinsic_BatchUnknownCallUsesFirstTransfer(t *testing.T) {
	s := newTestScanner()

	dest := make([]byte, 32)
	dest[0] = 0xDD
	amount := uint64(5_000_000_000) // 5 TAO

	transferCall := taotypes.BalanceTransferKeepAlive(5, 3, dest, amount)
	remarkCall := taotypes.SystemRemark(0, 0, []byte("ADD:TAO.TAO"))
	// Unknown call from pallet 99 — parser breaks here
	unknownCall := []byte{99, 1, 0xFF, 0xFF}
	batchCall := taotypes.BatchAll(11, 2, [][]byte{transferCall, remarkCall, unknownCall})

	extHex := buildTestExtrinsic(batchCall)

	txItem, err := s.processExtrinsic(extHex, 100, 0)
	if err != nil {
		t.Fatalf("processExtrinsic: %v", err)
	}
	if txItem == nil {
		t.Fatal("expected non-nil TxInItem")
	}

	// Transfer should still be captured despite unknown call
	expectedAmount := cosmos.NewUint(500_000_000) // 5e9 / 10
	if !txItem.Coins[0].Amount.Equal(expectedAmount) {
		t.Errorf("amount: got %s, want %s", txItem.Coins[0].Amount, expectedAmount)
	}
	if txItem.Memo != "ADD:TAO.TAO" {
		t.Errorf("memo: got %q, want %q", txItem.Memo, "ADD:TAO.TAO")
	}
}

// ---------------------------------------------------------------------------
// processExtrinsic — edge cases and error paths
// ---------------------------------------------------------------------------

func TestProcessExtrinsic_UnsignedReturnsNil(t *testing.T) {
	s := newTestScanner()

	// Build an unsigned extrinsic (version byte 0x04, bit 7 not set)
	body := []byte{0x04, 5, 3}
	length := taotypes.EncodeCompact(uint64(len(body)))
	raw := append(length, body...)
	extHex := taotypes.HexEncode(raw)

	txItem, err := s.processExtrinsic(extHex, 100, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if txItem != nil {
		t.Error("expected nil for unsigned extrinsic")
	}
}

func TestProcessExtrinsic_UnknownPalletReturnsNil(t *testing.T) {
	s := newTestScanner()

	call := []byte{99, 0, 0x00} // unknown pallet 99
	extHex := buildTestExtrinsic(call)

	txItem, err := s.processExtrinsic(extHex, 100, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if txItem != nil {
		t.Error("expected nil for unknown pallet")
	}
}

func TestProcessExtrinsic_EmptyHex(t *testing.T) {
	s := newTestScanner()
	_, err := s.processExtrinsic("", 100, 0)
	if err == nil {
		t.Error("expected error for empty hex")
	}
}

func TestProcessExtrinsic_MalformedHex(t *testing.T) {
	s := newTestScanner()
	_, err := s.processExtrinsic("0xZZZZ", 100, 0)
	if err == nil {
		t.Error("expected error for malformed hex")
	}
}

func TestProcessExtrinsic_TruncatedExtrinsic(t *testing.T) {
	s := newTestScanner()

	// Valid length prefix claiming 200 bytes but only 3 bytes of body
	length := taotypes.EncodeCompact(200)
	raw := append(length, 0x84, 0x00, 0x01)
	extHex := taotypes.HexEncode(raw)

	_, err := s.processExtrinsic(extHex, 100, 0)
	if err == nil {
		t.Error("expected error for truncated extrinsic")
	}
}

// ---------------------------------------------------------------------------
// processExtrinsic — signature type handling
// ---------------------------------------------------------------------------

func TestProcessExtrinsic_ECDSASignature(t *testing.T) {
	s := newTestScanner()

	dest := make([]byte, 32)
	dest[0] = 0xEE
	call := taotypes.BalanceTransferKeepAlive(5, 3, dest, 1_000_000_000)

	signer := make([]byte, 32)
	for i := range signer {
		signer[i] = byte(i + 1)
	}

	// ECDSA (type 0x02) uses 65-byte signatures
	ext := &taotypes.Extrinsic{
		Signer:        signer,
		SignatureType: 0x02, // ECDSA
		Signature:     make([]byte, 65),
		Era:           taotypes.ImmortalEra(),
		Nonce:         0,
		Tip:           0,
		MetadataMode:  taotypes.MetadataModeDisabled,
		CallData:      call,
	}
	extHex := taotypes.HexEncode(taotypes.EncodeSignedExtrinsic(ext))

	txItem, err := s.processExtrinsic(extHex, 100, 0)
	if err != nil {
		t.Fatalf("processExtrinsic with ECDSA sig: %v", err)
	}
	if txItem == nil {
		t.Fatal("expected non-nil TxInItem for ECDSA extrinsic")
	}

	expectedAmount := cosmos.NewUint(100_000_000) // 1e9 / 10
	if !txItem.Coins[0].Amount.Equal(expectedAmount) {
		t.Errorf("amount: got %s, want %s", txItem.Coins[0].Amount, expectedAmount)
	}
}

func TestProcessExtrinsic_UnknownSignatureTypeRejected(t *testing.T) {
	s := newTestScanner()

	dest := make([]byte, 32)
	call := taotypes.BalanceTransferKeepAlive(5, 3, dest, 500_000)

	signer := make([]byte, 32)
	for i := range signer {
		signer[i] = byte(i + 1)
	}

	// Unknown signature type 0x03
	ext := &taotypes.Extrinsic{
		Signer:        signer,
		SignatureType: 0x03,
		Signature:     make([]byte, 64),
		Era:           taotypes.ImmortalEra(),
		Nonce:         0,
		Tip:           0,
		MetadataMode:  taotypes.MetadataModeDisabled,
		CallData:      call,
	}
	extHex := taotypes.HexEncode(taotypes.EncodeSignedExtrinsic(ext))

	_, err := s.processExtrinsic(extHex, 100, 0)
	if err == nil {
		t.Error("expected error for unknown signature type 0x03")
	}
}

// ---------------------------------------------------------------------------
// processExtrinsic — mortal era parsing
// ---------------------------------------------------------------------------

func TestProcessExtrinsic_MortalEra(t *testing.T) {
	s := newTestScanner()

	dest := make([]byte, 32)
	call := taotypes.BalanceTransferKeepAlive(5, 3, dest, 500_000)

	// Build extrinsic with mortal era instead of immortal
	signer := make([]byte, 32)
	for i := range signer {
		signer[i] = byte(i + 1)
	}

	ext := &taotypes.Extrinsic{
		Signer:        signer,
		SignatureType: taotypes.SignatureTypeEd25519,
		Signature:     make([]byte, 64),
		Era:           taotypes.MortalEra(64, 42),
		Nonce:         7,
		Tip:           100,
		MetadataMode:  taotypes.MetadataModeDisabled,
		CallData:      call,
	}
	extHex := taotypes.HexEncode(taotypes.EncodeSignedExtrinsic(ext))

	txItem, err := s.processExtrinsic(extHex, 100, 0)
	if err != nil {
		t.Fatalf("processExtrinsic: %v", err)
	}
	if txItem == nil {
		t.Fatal("expected non-nil TxInItem with mortal era")
	}

	expectedAmount := cosmos.NewUint(50_000) // 500_000 / 10
	if !txItem.Coins[0].Amount.Equal(expectedAmount) {
		t.Errorf("amount: got %s, want %s", txItem.Coins[0].Amount, expectedAmount)
	}
}

// ---------------------------------------------------------------------------
// Extrinsic hash consistency between scanner and types
// ---------------------------------------------------------------------------

func TestExtrinsicHashConsistency(t *testing.T) {
	dest := make([]byte, 32)
	call := taotypes.BalanceTransferKeepAlive(5, 3, dest, 1_000_000)
	raw := taotypes.EncodeSignedExtrinsic(&taotypes.Extrinsic{
		Signer:        make([]byte, 32),
		SignatureType: taotypes.SignatureTypeEd25519,
		Signature:     make([]byte, 64),
		Era:           taotypes.ImmortalEra(),
		Nonce:         0,
		Tip:           0,
		MetadataMode:  taotypes.MetadataModeDisabled,
		CallData:      call,
	})

	// Both should produce the same hash for the same raw bytes
	hash1 := taotypes.ExtrinsicHash(raw)
	hash2 := taotypes.ExtrinsicHash(raw)
	if hash1 != hash2 {
		t.Errorf("inconsistent hashes: %s vs %s", hash1, hash2)
	}
	if hash1[:2] != "0x" {
		t.Error("hash should have 0x prefix")
	}
}

// ---------------------------------------------------------------------------
// Fee cache tests
// ---------------------------------------------------------------------------

func TestUpdateFeeCache(t *testing.T) {
	s := newTestScanner()

	s.updateFeeCache(100_000)
	s.updateFeeCache(200_000)
	s.updateFeeCache(150_000)

	s.mu.RLock()
	cacheLen := len(s.feeCache)
	s.mu.RUnlock()

	if cacheLen != 3 {
		t.Errorf("cache len: got %d, want 3", cacheLen)
	}

	avg := s.averageFee()
	expected := uint64(150_000) // (100k + 200k + 150k) / 3
	if avg != expected {
		t.Errorf("averageFee: got %d, want %d", avg, expected)
	}
}

func TestUpdateFeeCacheOverflow(t *testing.T) {
	s := newTestScanner()

	// Fill past cache size
	for i := 0; i < feeCacheSize+50; i++ {
		s.updateFeeCache(uint64(1000 + i))
	}

	s.mu.RLock()
	cacheLen := len(s.feeCache)
	s.mu.RUnlock()

	if cacheLen != feeCacheSize {
		t.Errorf("cache len: got %d, want %d", cacheLen, feeCacheSize)
	}

	// The oldest 50 entries (1000..1049) should have been evicted.
	// Remaining: 1050..1249, average = (1050+1249)/2 = 1149 (integer)
	avg := s.averageFee()
	expectedAvg := uint64(1149)
	if avg != expectedAvg {
		t.Errorf("averageFee: got %d, want %d", avg, expectedAvg)
	}
}

func TestAverageFee_Empty(t *testing.T) {
	s := newTestScanner()
	if avg := s.averageFee(); avg != 0 {
		t.Errorf("averageFee on empty cache: got %d, want 0", avg)
	}
}

func TestGetNetworkFee_Default(t *testing.T) {
	s := newTestScanner()
	txSize, txRate := s.GetNetworkFee()
	if txSize != 1 {
		t.Errorf("txSize: got %d, want 1", txSize)
	}
	if txRate != defaultFeeRao {
		t.Errorf("txRate: got %d, want %d (defaultFeeRao)", txRate, defaultFeeRao)
	}
}

func TestGetNetworkFee_Cached(t *testing.T) {
	s := newTestScanner()

	s.updateFeeCache(80_000)
	s.updateFeeCache(120_000)

	txSize, txRate := s.GetNetworkFee()
	if txSize != 1 {
		t.Errorf("txSize: got %d, want 1", txSize)
	}
	expected := uint64(100_000) // (80k + 120k) / 2
	if txRate != expected {
		t.Errorf("txRate: got %d, want %d", txRate, expected)
	}
}

// ---------------------------------------------------------------------------
// reportNetworkFee tests
// ---------------------------------------------------------------------------

func newTestScannerWithFeeQueue(resolution int64) (*TAOScanner, chan common.NetworkFee) {
	ch := make(chan common.NetworkFee, 4)
	s := &TAOScanner{
		logger: log.Logger,
		cfg: config.BifrostBlockScannerConfiguration{
			GasPriceResolution: resolution,
		},
		balancesPalletIndex:           5,
		transferCallIndex:             transferKeepAliveCallIndex,
		systemPalletIndex:             0,
		remarkCallIndex:               0,
		utilityPalletIndex:            11,
		batchAllCallIndex:             2,
		transactionPaymentPalletIndex: 6,
		globalNetworkFeeQueue:         ch,
	}
	return s, ch
}

func TestReportNetworkFee_SkipsPartialCache(t *testing.T) {
	s, ch := newTestScannerWithFeeQueue(1000)

	// Add fewer than feeCacheSize samples
	for i := 0; i < feeCacheSize-1; i++ {
		s.updateFeeCache(100_000)
	}

	s.reportNetworkFee(100)

	select {
	case <-ch:
		t.Error("should not report fee with partial cache")
	default:
		// expected — partial cache is skipped
	}
}

func TestReportNetworkFee_ReportsRoundedFee(t *testing.T) {
	s, ch := newTestScannerWithFeeQueue(1000)

	// Fill cache completely with 100_500 rao average → rounds up to 101_000
	for i := 0; i < feeCacheSize; i++ {
		s.updateFeeCache(100_500)
	}

	s.reportNetworkFee(100)

	select {
	case fee := <-ch:
		if fee.TransactionRate != 101_000 {
			t.Errorf("fee rate: got %d, want 101000 (rounded up to resolution)", fee.TransactionRate)
		}
		if fee.TransactionSize != 1 {
			t.Errorf("tx size: got %d, want 1", fee.TransactionSize)
		}
		if fee.Height != 100 {
			t.Errorf("height: got %d, want 100", fee.Height)
		}
		if !fee.Chain.Equals(common.TAOChain) {
			t.Errorf("chain: got %s, want TAO", fee.Chain)
		}
	default:
		t.Error("expected fee to be reported")
	}
}

func TestReportNetworkFee_SkipsUnchanged(t *testing.T) {
	s, ch := newTestScannerWithFeeQueue(1000)

	// Fill cache — average = 100_000, rounds to 100_000 (already aligned)
	for i := 0; i < feeCacheSize; i++ {
		s.updateFeeCache(100_000)
	}

	// First report should send
	s.reportNetworkFee(100)
	select {
	case <-ch:
		// expected
	default:
		t.Fatal("first report should send")
	}

	// Second report with identical fees should skip
	s.reportNetworkFee(200)
	select {
	case <-ch:
		t.Error("should skip unchanged fee")
	default:
		// expected — fee unchanged
	}
}

func TestReportNetworkFee_ReportsOnChange(t *testing.T) {
	s, ch := newTestScannerWithFeeQueue(1000)

	// Fill cache with 100_000
	for i := 0; i < feeCacheSize; i++ {
		s.updateFeeCache(100_000)
	}

	s.reportNetworkFee(100)
	<-ch // consume first report

	// Replace cache with higher fees (200_000)
	for i := 0; i < feeCacheSize; i++ {
		s.updateFeeCache(200_000)
	}

	s.reportNetworkFee(200)
	select {
	case fee := <-ch:
		if fee.TransactionRate != 200_000 {
			t.Errorf("fee rate: got %d, want 200000", fee.TransactionRate)
		}
	default:
		t.Error("expected fee to be reported after change")
	}
}

// ---------------------------------------------------------------------------
// processExtrinsic — actual fee usage
// ---------------------------------------------------------------------------

func TestProcessExtrinsic_UsesActualFee(t *testing.T) {
	s := newTestScanner()

	dest := make([]byte, 32)
	dest[0] = 0xAA
	amount := uint64(1_000_000_000)
	call := taotypes.BalanceTransferKeepAlive(5, 3, dest, amount)
	extHex := buildTestExtrinsic(call)

	// Pass actual fee of 150_000 rao
	txItem, err := s.processExtrinsic(extHex, 100, 150_000)
	if err != nil {
		t.Fatalf("processExtrinsic: %v", err)
	}
	if txItem == nil {
		t.Fatal("expected non-nil TxInItem")
	}

	// Gas should use actual fee: 150_000 rao / 10 = 15_000 thorchain units
	expectedGas := cosmos.NewUint(15_000)
	if len(txItem.Gas) == 0 {
		t.Fatal("gas should not be empty")
	}
	if !txItem.Gas[0].Amount.Equal(expectedGas) {
		t.Errorf("gas amount: got %s, want %s", txItem.Gas[0].Amount, expectedGas)
	}
}

func TestProcessExtrinsic_FallsBackToDefaultFee(t *testing.T) {
	s := newTestScanner()

	dest := make([]byte, 32)
	call := taotypes.BalanceTransferKeepAlive(5, 3, dest, 500_000)
	extHex := buildTestExtrinsic(call)

	// Pass 0 for actualFee — should fall back to defaultFeeRao
	txItem, err := s.processExtrinsic(extHex, 100, 0)
	if err != nil {
		t.Fatalf("processExtrinsic: %v", err)
	}
	if txItem == nil {
		t.Fatal("expected non-nil TxInItem")
	}

	// Gas should use defaultFeeRao: 100_000 rao / 10 = 10_000 thorchain units
	expectedGas := cosmos.NewUint(10_000)
	if len(txItem.Gas) == 0 {
		t.Fatal("gas should not be empty")
	}
	if !txItem.Gas[0].Amount.Equal(expectedGas) {
		t.Errorf("gas amount: got %s, want %s (should use defaultFeeRao)", txItem.Gas[0].Amount, expectedGas)
	}
}

// ---------------------------------------------------------------------------
// processExtrinsic — memo size validation
// ---------------------------------------------------------------------------

func TestProcessExtrinsic_OversizedMemoDropped(t *testing.T) {
	s := newTestScanner()

	dest := make([]byte, 32)
	dest[0] = 0xBB
	amount := uint64(2_000_000_000)

	// Build a memo that exceeds MaxMemoSize (250 bytes)
	longMemo := strings.Repeat("A", constants.MaxMemoSize+1)

	transferCall := taotypes.BalanceTransferKeepAlive(5, 3, dest, amount)
	remarkCall := taotypes.SystemRemark(0, 0, []byte(longMemo))
	batchCall := taotypes.BatchAll(11, 2, [][]byte{transferCall, remarkCall})
	extHex := buildTestExtrinsic(batchCall)

	txItem, err := s.processExtrinsic(extHex, 200, 0)
	if err != nil {
		t.Fatalf("processExtrinsic: %v", err)
	}
	if txItem != nil {
		t.Error("expected nil TxInItem for oversized memo")
	}
}

func TestProcessExtrinsic_MaxSizeMemoAccepted(t *testing.T) {
	s := newTestScanner()

	dest := make([]byte, 32)
	dest[0] = 0xCC
	amount := uint64(1_000_000_000)

	// Build a memo that is exactly MaxMemoSize (250 bytes) — should be accepted
	exactMemo := strings.Repeat("B", constants.MaxMemoSize)

	transferCall := taotypes.BalanceTransferKeepAlive(5, 3, dest, amount)
	remarkCall := taotypes.SystemRemark(0, 0, []byte(exactMemo))
	batchCall := taotypes.BatchAll(11, 2, [][]byte{transferCall, remarkCall})
	extHex := buildTestExtrinsic(batchCall)

	txItem, err := s.processExtrinsic(extHex, 200, 0)
	if err != nil {
		t.Fatalf("processExtrinsic: %v", err)
	}
	if txItem == nil {
		t.Fatal("expected non-nil TxInItem for max-size memo")
	}
	if txItem.Memo != exactMemo {
		t.Errorf("memo length: got %d, want %d", len(txItem.Memo), constants.MaxMemoSize)
	}
}

func TestProcessExtrinsic_NoMemoTransferAccepted(t *testing.T) {
	s := newTestScanner()

	dest := make([]byte, 32)
	dest[0] = 0xDD
	amount := uint64(1_000_000_000)

	// Direct transfer (no batch, no memo) — memo is empty, should always pass
	call := taotypes.BalanceTransferKeepAlive(5, 3, dest, amount)
	extHex := buildTestExtrinsic(call)

	txItem, err := s.processExtrinsic(extHex, 100, 0)
	if err != nil {
		t.Fatalf("processExtrinsic: %v", err)
	}
	if txItem == nil {
		t.Fatal("expected non-nil TxInItem")
	}
	if txItem.Memo != "" {
		t.Errorf("memo: got %q, want empty", txItem.Memo)
	}
}
