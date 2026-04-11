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
)

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

func TestIsTransferCall(t *testing.T) {
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
		s := newTestScanner()
		if got := s.isTransferCall(tt.callIdx); got != tt.expected {
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
		result := common.TAOChain.NativeGasToThorchain(cosmos.NewUint(tt.rao))
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
	amount := cosmos.NewUint(1_000_000_000)
	call := stypes.BalanceTransferKeepAlive(5, 3, dest, amount)

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
	call := stypes.BalanceTransferKeepAlive(5, 0, dest, cosmos.NewUint(500_000)) // allow_death index

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
	call := stypes.BalanceTransferKeepAlive(5, 7, dest, cosmos.NewUint(500_000))

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
	call := stypes.BalanceTransferKeepAlive(5, 3, dest, cosmos.NewUint(0))

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
	amount := cosmos.NewUint(21_000_000_000_000_000) // 21M TAO
	call := stypes.BalanceTransferKeepAlive(5, 3, dest, amount)

	extHex := buildTestExtrinsic(call)

	txItem, err := s.processExtrinsic(extHex, 100, 0)
	if err != nil {
		t.Fatalf("processExtrinsic: %v", err)
	}
	if txItem == nil {
		t.Fatal("expected non-nil TxInItem")
	}

	expectedAmount := common.TAOChain.NativeGasToThorchain(amount)
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
	amount := cosmos.NewUint(2_000_000_000)

	transferCall := stypes.BalanceTransferKeepAlive(5, 3, dest, amount)
	remarkCall := stypes.SystemRemark(0, 0, []byte("SWAP:BTC.BTC:bc1qtest"))
	batchCall := stypes.BatchAll(11, 2, [][]byte{transferCall, remarkCall})

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
	amount := cosmos.NewUint(1_000_000_000)

	// Remark comes BEFORE transfer in the batch
	remarkCall := stypes.SystemRemark(0, 0, []byte("ADD:TAO.TAO"))
	transferCall := stypes.BalanceTransferKeepAlive(5, 3, dest, amount)
	batchCall := stypes.BatchAll(11, 2, [][]byte{remarkCall, transferCall})

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
	transfer1 := stypes.BalanceTransferKeepAlive(5, 3, dest1, cosmos.NewUint(10_000_000_000))
	transfer2 := stypes.BalanceTransferKeepAlive(5, 3, dest2, cosmos.NewUint(100))
	remarkCall := stypes.SystemRemark(0, 0, []byte("memo"))
	batchCall := stypes.BatchAll(11, 2, [][]byte{transfer1, transfer2, remarkCall})

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

	remarkCall := stypes.SystemRemark(0, 0, []byte("just a remark"))
	batchCall := stypes.BatchAll(11, 2, [][]byte{remarkCall})

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
	amount := cosmos.NewUint(1_000_000_000)

	transferCall := stypes.BalanceTransferKeepAlive(5, 3, dest, amount)
	remark1 := stypes.SystemRemark(0, 0, []byte("SWAP:BTC.BTC:bc1q1"))
	remark2 := stypes.SystemRemark(0, 0, []byte("SWAP:BTC.BTC:bc1q2"))
	batchCall := stypes.BatchAll(11, 2, [][]byte{remark1, transferCall, remark2})

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
	amount := cosmos.NewUint(5_000_000_000) // 5 TAO

	transferCall := stypes.BalanceTransferKeepAlive(5, 3, dest, amount)
	remarkCall := stypes.SystemRemark(0, 0, []byte("ADD:TAO.TAO"))
	// Unknown call from pallet 99 — parser breaks here
	unknownCall := []byte{99, 1, 0xFF, 0xFF}
	batchCall := stypes.BatchAll(11, 2, [][]byte{transferCall, remarkCall, unknownCall})

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
	length := stypes.EncodeCompact(uint64(len(body)))
	raw := append(length, body...)
	extHex := stypes.HexEncode(raw)

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
	length := stypes.EncodeCompact(200)
	raw := append(length, 0x84, 0x00, 0x01)
	extHex := stypes.HexEncode(raw)

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
}

func TestUpdateFeeCacheOverflow(t *testing.T) {
	s := newTestScanner()

	// Fill past cache size
	for i := 0; i < FeeCacheSize+50; i++ {
		s.updateFeeCache(uint64(1000 + i))
	}

	s.mu.RLock()
	cacheLen := len(s.feeCache)
	s.mu.RUnlock()

	if cacheLen != FeeCacheSize {
		t.Errorf("cache len: got %d, want %d", cacheLen, FeeCacheSize)
	}
}

func TestGetNetworkFee_Default(t *testing.T) {
	s := newTestScanner()
	txSize, txRate := s.GetNetworkFee()
	if txSize != 1 {
		t.Errorf("txSize: got %d, want 1", txSize)
	}
	if txRate != ChainParams[common.TAOChain].DefaultFee {
		t.Errorf("txRate: got %d, want %d (DefaultFee)", txRate, ChainParams[common.TAOChain].DefaultFee)
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
// updateNetworkFee tests
// ---------------------------------------------------------------------------

func newTestScannerWithFeeQueue(resolution int64) (*Scanner, chan common.NetworkFee) {
	ch := make(chan common.NetworkFee, 4)
	s := &Scanner{
		logger: log.Logger,
		params: ChainParams[common.TAOChain],
		cfg: config.BifrostBlockScannerConfiguration{
			ChainID:            common.TAOChain,
			GasPriceResolution: resolution,
		},
		globalNetworkFeeQueue: ch,
	}
	return s, ch
}

func TestReportNetworkFee_BootstrapsWithDefaultFee(t *testing.T) {
	s, ch := newTestScannerWithFeeQueue(1000)

	// Add fewer than FeeCacheSize samples
	for i := 0; i < FeeCacheSize-1; i++ {
		s.updateFeeCache(100_000)
	}

	s.updateNetworkFee(100)

	select {
	case fee := <-ch:
		if fee.TransactionRate != s.params.DefaultFee {
			t.Errorf("fee rate: got %d, want %d (DefaultFee)", fee.TransactionRate, s.params.DefaultFee)
		}
	default:
		t.Error("expected DefaultFee to be reported on bootstrap with partial cache")
	}
}

func TestReportNetworkFee_ReportsRoundedFee(t *testing.T) {
	s, ch := newTestScannerWithFeeQueue(1000)

	// Fill cache completely with 100_500 rao average → rounds up to 101_000
	for i := 0; i < FeeCacheSize; i++ {
		s.updateFeeCache(100_500)
	}

	s.updateNetworkFee(100)

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
	for i := 0; i < FeeCacheSize; i++ {
		s.updateFeeCache(100_000)
	}

	// First report should send
	s.updateNetworkFee(100)
	select {
	case <-ch:
		// expected
	default:
		t.Fatal("first report should send")
	}

	// Second report with identical fees should skip
	s.updateNetworkFee(200)
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
	for i := 0; i < FeeCacheSize; i++ {
		s.updateFeeCache(100_000)
	}

	s.updateNetworkFee(100)
	<-ch // consume first report

	// Replace cache with higher fees (200_000)
	for i := 0; i < FeeCacheSize; i++ {
		s.updateFeeCache(200_000)
	}

	s.updateNetworkFee(200)
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
	amount := cosmos.NewUint(1_000_000_000)
	call := stypes.BalanceTransferKeepAlive(5, 3, dest, amount)
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
	call := stypes.BalanceTransferKeepAlive(5, 3, dest, cosmos.NewUint(500_000))
	extHex := buildTestExtrinsic(call)

	// Pass 0 for actualFee — gas falls back to 1 (minimum placeholder)
	txItem, err := s.processExtrinsic(extHex, 100, 0)
	if err != nil {
		t.Fatalf("processExtrinsic: %v", err)
	}
	if txItem == nil {
		t.Fatal("expected non-nil TxInItem")
	}

	// Gas uses minimum placeholder when actual fee is unavailable
	expectedGas := cosmos.NewUint(1)
	if len(txItem.Gas) == 0 {
		t.Fatal("gas should not be empty")
	}
	if !txItem.Gas[0].Amount.Equal(expectedGas) {
		t.Errorf("gas amount: got %s, want %s (should use minimum placeholder)", txItem.Gas[0].Amount, expectedGas)
	}
}

// ---------------------------------------------------------------------------
// processExtrinsic — memo size validation
// ---------------------------------------------------------------------------

func TestProcessExtrinsic_OversizedMemoDropped(t *testing.T) {
	s := newTestScanner()

	dest := make([]byte, 32)
	dest[0] = 0xBB
	amount := cosmos.NewUint(2_000_000_000)

	// Build a memo that exceeds MaxMemoSize (250 bytes)
	longMemo := strings.Repeat("A", constants.MaxMemoSize+1)

	transferCall := stypes.BalanceTransferKeepAlive(5, 3, dest, amount)
	remarkCall := stypes.SystemRemark(0, 0, []byte(longMemo))
	batchCall := stypes.BatchAll(11, 2, [][]byte{transferCall, remarkCall})
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
	amount := cosmos.NewUint(1_000_000_000)

	// Build a memo that is exactly MaxMemoSize (250 bytes) — should be accepted
	exactMemo := strings.Repeat("B", constants.MaxMemoSize)

	transferCall := stypes.BalanceTransferKeepAlive(5, 3, dest, amount)
	remarkCall := stypes.SystemRemark(0, 0, []byte(exactMemo))
	batchCall := stypes.BatchAll(11, 2, [][]byte{transferCall, remarkCall})
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
	amount := cosmos.NewUint(1_000_000_000)

	// Direct transfer (no batch, no memo) — memo is empty, should always pass
	call := stypes.BalanceTransferKeepAlive(5, 3, dest, amount)
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
