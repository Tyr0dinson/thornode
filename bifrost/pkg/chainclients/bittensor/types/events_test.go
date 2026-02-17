package types

import (
	"encoding/binary"
	"testing"
)

// buildEvent builds a single event record for testing.
func buildEvent(phase byte, extrinsicIdx uint32, palletIdx, eventIdx byte, data []byte, topicCount int) []byte {
	var buf []byte

	// Phase
	buf = append(buf, phase)
	if phase == 0x00 { // ApplyExtrinsic
		idx := make([]byte, 4)
		binary.LittleEndian.PutUint32(idx, extrinsicIdx)
		buf = append(buf, idx...)
	}

	// Event ID
	buf = append(buf, palletIdx, eventIdx)

	// Event data
	buf = append(buf, data...)

	// Topics (compact-encoded count + topics)
	buf = append(buf, EncodeCompact(uint64(topicCount))...)
	for i := 0; i < topicCount; i++ {
		buf = append(buf, make([]byte, 32)...) // zero hash topic
	}

	return buf
}

// makeCompactDispatchInfo builds DispatchInfo with compact-encoded Weight fields.
// ref_time and proof_size are compact-encoded, followed by DispatchClass and Pays.
func makeCompactDispatchInfo(refTime, proofSize uint64, class, pays byte) []byte {
	var buf []byte
	buf = append(buf, EncodeCompact(refTime)...)
	buf = append(buf, EncodeCompact(proofSize)...)
	buf = append(buf, class, pays)
	return buf
}

// makeDefaultDispatchInfo returns a compact DispatchInfo with small values.
func makeDefaultDispatchInfo() []byte {
	return makeCompactDispatchInfo(261973000, 1493, 0, 0)
}

func TestParseExtrinsicResults_Success(t *testing.T) {
	pallets := EventPalletConfig{
		SystemPalletIndex:             0,
		BalancesPalletIndex:           5,
		UtilityPalletIndex:            11,
		TransactionPaymentPalletIndex: 6,
	}

	var events []byte

	// Event 1: ExtrinsicSuccess for extrinsic 0
	events = append(events, buildEvent(0x00, 0, 0, 0x00, makeDefaultDispatchInfo(), 0)...)

	// Event 2: Balances::Withdraw for extrinsic 1 (account=32, balance=8 = 40 bytes)
	events = append(events, buildEvent(0x00, 1, 5, 8, make([]byte, 40), 0)...)

	// Event 3: Balances::Transfer for extrinsic 1 (from=32, to=32, amount=8 = 72 bytes)
	events = append(events, buildEvent(0x00, 1, 5, 2, make([]byte, 72), 0)...)

	// Event 4: Balances::Deposit for extrinsic 1 (account=32, balance=8 = 40 bytes)
	events = append(events, buildEvent(0x00, 1, 5, 7, make([]byte, 40), 0)...)

	// Event 5: TransactionPayment::TransactionFeePaid (account=32, fee=8, tip=8 = 48 bytes)
	events = append(events, buildEvent(0x00, 1, 6, 0, make([]byte, 48), 0)...)

	// Event 6: ExtrinsicSuccess for extrinsic 1
	events = append(events, buildEvent(0x00, 1, 0, 0x00, makeDefaultDispatchInfo(), 0)...)

	data := EncodeCompact(6)
	data = append(data, events...)

	results, _, err := ParseExtrinsicResults(data, pallets)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}

	if !results[0] {
		t.Error("extrinsic 0 should be successful")
	}
	if !results[1] {
		t.Error("extrinsic 1 should be successful")
	}
}

func TestParseExtrinsicResults_Failed(t *testing.T) {
	pallets := EventPalletConfig{
		SystemPalletIndex:             0,
		BalancesPalletIndex:           5,
		UtilityPalletIndex:            11,
		TransactionPaymentPalletIndex: 6,
	}

	var events []byte

	// ExtrinsicSuccess for extrinsic 0
	events = append(events, buildEvent(0x00, 0, 0, 0x00, makeDefaultDispatchInfo(), 0)...)

	// ExtrinsicFailed for extrinsic 1:
	// DispatchError variant 0 (Other) = 1 byte + compact DispatchInfo
	di := makeDefaultDispatchInfo()
	failData := append([]byte{0x00}, di...) // DispatchError::Other + DispatchInfo
	events = append(events, buildEvent(0x00, 1, 0, 0x01, failData, 0)...)

	data := EncodeCompact(2)
	data = append(data, events...)

	results, _, err := ParseExtrinsicResults(data, pallets)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if results[0] != true {
		t.Error("extrinsic 0 should be successful")
	}
	if results[1] != false {
		t.Error("extrinsic 1 should be failed")
	}
}

func TestParseExtrinsicResults_WithBatchEvents(t *testing.T) {
	pallets := EventPalletConfig{
		SystemPalletIndex:             0,
		BalancesPalletIndex:           5,
		UtilityPalletIndex:            11,
		TransactionPaymentPalletIndex: 6,
	}

	var events []byte

	// ExtrinsicSuccess for extrinsic 0 (timestamp)
	events = append(events, buildEvent(0x00, 0, 0, 0x00, makeDefaultDispatchInfo(), 0)...)

	// Balances::Withdraw for extrinsic 1
	events = append(events, buildEvent(0x00, 1, 5, 8, make([]byte, 40), 0)...)

	// Balances::Transfer for extrinsic 1
	events = append(events, buildEvent(0x00, 1, 5, 2, make([]byte, 72), 0)...)

	// Utility::ItemCompleted for extrinsic 1
	events = append(events, buildEvent(0x00, 1, 11, 3, nil, 0)...)

	// System::Remarked for extrinsic 1 (AccountId + Hash = 64 bytes)
	events = append(events, buildEvent(0x00, 1, 0, 0x05, make([]byte, 64), 0)...)

	// Utility::ItemCompleted for extrinsic 1
	events = append(events, buildEvent(0x00, 1, 11, 3, nil, 0)...)

	// Utility::BatchCompleted for extrinsic 1
	events = append(events, buildEvent(0x00, 1, 11, 1, nil, 0)...)

	// Balances::Deposit for extrinsic 1
	events = append(events, buildEvent(0x00, 1, 5, 7, make([]byte, 40), 0)...)

	// TransactionPayment::TransactionFeePaid for extrinsic 1
	events = append(events, buildEvent(0x00, 1, 6, 0, make([]byte, 48), 0)...)

	// ExtrinsicSuccess for extrinsic 1
	events = append(events, buildEvent(0x00, 1, 0, 0x00, makeDefaultDispatchInfo(), 0)...)

	data := EncodeCompact(10)
	data = append(data, events...)

	results, _, err := ParseExtrinsicResults(data, pallets)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !results[0] {
		t.Error("extrinsic 0 should be successful")
	}
	if !results[1] {
		t.Error("extrinsic 1 should be successful")
	}
}

func TestParseExtrinsicResults_UnknownPallet(t *testing.T) {
	pallets := EventPalletConfig{
		SystemPalletIndex:             0,
		BalancesPalletIndex:           5,
		UtilityPalletIndex:            11,
		TransactionPaymentPalletIndex: 6,
	}

	var events []byte

	// ExtrinsicSuccess for extrinsic 0
	events = append(events, buildEvent(0x00, 0, 0, 0x00, makeDefaultDispatchInfo(), 0)...)

	// Unknown pallet event for extrinsic 1 (pallet 99)
	events = append(events, buildEvent(0x00, 1, 99, 0, make([]byte, 20), 0)...)

	// ExtrinsicSuccess for extrinsic 1 (should NOT be reached due to unknown pallet)
	events = append(events, buildEvent(0x00, 1, 0, 0x00, makeDefaultDispatchInfo(), 0)...)

	data := EncodeCompact(3)
	data = append(data, events...)

	results, _, _ := ParseExtrinsicResults(data, pallets)

	// Extrinsic 0 should be known as successful
	if !results[0] {
		t.Error("extrinsic 0 should be successful")
	}

	// Extrinsic 1 should NOT be in results (unknown event stopped parsing)
	if _, ok := results[1]; ok {
		t.Error("extrinsic 1 should not be in results (parsing stopped at unknown pallet)")
	}
}

func TestParseExtrinsicResults_Empty(t *testing.T) {
	pallets := EventPalletConfig{}

	results, _, err := ParseExtrinsicResults(nil, pallets)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if results != nil {
		t.Error("expected nil results for nil input")
	}
}

func TestParseExtrinsicResults_ModuleError(t *testing.T) {
	pallets := EventPalletConfig{
		SystemPalletIndex:             0,
		BalancesPalletIndex:           5,
		UtilityPalletIndex:            11,
		TransactionPaymentPalletIndex: 6,
	}

	var events []byte

	// ExtrinsicFailed with Module error (variant 3): 1 + 1 + 4 = 6 bytes + compact DispatchInfo
	di := makeDefaultDispatchInfo()
	failData := make([]byte, 6)
	failData[0] = 3 // DispatchError::Module
	failData[1] = 5 // module index
	failData = append(failData, di...)
	events = append(events, buildEvent(0x00, 0, 0, 0x01, failData, 0)...)

	data := EncodeCompact(1)
	data = append(data, events...)

	results, _, err := ParseExtrinsicResults(data, pallets)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if results[0] != false {
		t.Error("extrinsic 0 should be failed")
	}
}

func TestScanExtrinsicResults_FindsEventsAfterUnknownPallet(t *testing.T) {
	pallets := EventPalletConfig{
		SystemPalletIndex:             0,
		BalancesPalletIndex:           5,
		UtilityPalletIndex:            11,
		TransactionPaymentPalletIndex: 6,
	}

	var events []byte

	// ExtrinsicSuccess for extrinsic 0
	events = append(events, buildEvent(0x00, 0, 0, 0x00, makeDefaultDispatchInfo(), 0)...)

	// Unknown pallet event for extrinsic 1 (pallet 99, 20 bytes data)
	events = append(events, buildEvent(0x00, 1, 99, 0, make([]byte, 20), 0)...)

	// ExtrinsicSuccess for extrinsic 1 (after the unknown event)
	events = append(events, buildEvent(0x00, 1, 0, 0x00, makeDefaultDispatchInfo(), 0)...)

	data := EncodeCompact(3)
	data = append(data, events...)

	// Linear parser should miss extrinsic 1
	linearResults, _, _ := ParseExtrinsicResults(data, pallets)
	if _, ok := linearResults[1]; ok {
		t.Error("linear parser should not find extrinsic 1")
	}

	// Scan-based parser should find both
	scanResults := ScanExtrinsicResults(data, pallets, 2)
	if !scanResults[0] {
		t.Error("scan: extrinsic 0 should be successful")
	}
	if !scanResults[1] {
		t.Error("scan: extrinsic 1 should be successful")
	}
}

func TestScanExtrinsicResults_FindsFailedAfterUnknownPallet(t *testing.T) {
	pallets := EventPalletConfig{
		SystemPalletIndex:             0,
		BalancesPalletIndex:           5,
		UtilityPalletIndex:            11,
		TransactionPaymentPalletIndex: 6,
	}

	var events []byte

	// ExtrinsicSuccess for extrinsic 0
	events = append(events, buildEvent(0x00, 0, 0, 0x00, makeDefaultDispatchInfo(), 0)...)

	// Unknown pallet event for extrinsic 1 (pallet 50, non-zero data to avoid
	// false pattern matches within the payload)
	unknownData := make([]byte, 64)
	for i := range unknownData {
		unknownData[i] = 0xAB
	}
	events = append(events, buildEvent(0x00, 1, 50, 2, unknownData, 0)...)

	// ExtrinsicFailed for extrinsic 1
	di := makeDefaultDispatchInfo()
	failData := append([]byte{0x00}, di...) // DispatchError::Other + DispatchInfo
	events = append(events, buildEvent(0x00, 1, 0, 0x01, failData, 0)...)

	data := EncodeCompact(3)
	data = append(data, events...)

	scanResults := ScanExtrinsicResults(data, pallets, 2)
	if !scanResults[0] {
		t.Error("scan: extrinsic 0 should be successful")
	}
	if scanResults[1] != false {
		t.Error("scan: extrinsic 1 should be failed")
	}
}

func TestScanExtrinsicResults_RejectsInvalidDispatchInfo(t *testing.T) {
	pallets := EventPalletConfig{
		SystemPalletIndex: 0,
	}

	// Construct data that looks like ExtrinsicSuccess but with invalid DispatchInfo
	var data []byte
	data = append(data, EncodeCompact(1)...) // 1 event

	// Phase::ApplyExtrinsic(0), extrinsic index 0
	data = append(data, 0x00)
	data = append(data, 0x00, 0x00, 0x00, 0x00) // ext idx = 0
	data = append(data, 0x00)                   // System pallet
	data = append(data, 0x00)                   // ExtrinsicSuccess

	// DispatchInfo with invalid DispatchClass (= 5, should be 0-2)
	// compact(0) for ref_time + compact(0) for proof_size + invalid class + pays
	data = append(data, 0x00) // compact ref_time = 0
	data = append(data, 0x00) // compact proof_size = 0
	data = append(data, 5)    // Invalid DispatchClass
	data = append(data, 0)
	data = append(data, 0x00) // 0 topics

	scanResults := ScanExtrinsicResults(data, pallets, 1)
	if _, ok := scanResults[0]; ok {
		t.Error("scan should reject event with invalid DispatchClass")
	}
}

func TestScanExtrinsicResults_RejectsOutOfRangeExtrinsicIdx(t *testing.T) {
	pallets := EventPalletConfig{
		SystemPalletIndex: 0,
	}

	var data []byte
	data = append(data, EncodeCompact(1)...)

	// ExtrinsicSuccess for extrinsic index 100 (but numExtrinsics = 2)
	data = append(data, 0x00)
	data = append(data, 100, 0x00, 0x00, 0x00) // ext idx = 100
	data = append(data, 0x00, 0x00)            // System::ExtrinsicSuccess
	data = append(data, makeDefaultDispatchInfo()...)
	data = append(data, 0x00) // 0 topics

	scanResults := ScanExtrinsicResults(data, pallets, 2)
	if _, ok := scanResults[100]; ok {
		t.Error("scan should reject extrinsic index out of range")
	}
}

func TestScanExtrinsicResults_ConflictingMatchesExcluded(t *testing.T) {
	pallets := EventPalletConfig{
		SystemPalletIndex: 0,
	}

	// Build data where the scan finds BOTH ExtrinsicSuccess and ExtrinsicFailed
	// patterns for extrinsic 1. This simulates a false positive from unknown pallet
	// event data containing a matching byte pattern.
	var data []byte
	data = append(data, EncodeCompact(4)...) // 4 "events" (the scan doesn't use the count)

	// Real ExtrinsicSuccess for extrinsic 0
	data = append(data, buildEvent(0x00, 0, 0, 0x00, makeDefaultDispatchInfo(), 0)...)

	// Real ExtrinsicFailed for extrinsic 1
	di := makeDefaultDispatchInfo()
	failData := append([]byte{0x00}, di...) // DispatchError::Other + DispatchInfo
	data = append(data, buildEvent(0x00, 1, 0, 0x01, failData, 0)...)

	// Embed a fake ExtrinsicSuccess pattern for extrinsic 1 (simulating a collision
	// in unknown pallet event data). We do this by appending raw bytes that look
	// like Phase::ApplyExtrinsic(0x00) + extIdx=1 + System(0x00) + Success(0x00) +
	// valid DispatchInfo + topics(0x00).
	fakeSuccess := []byte{0x00}       // Phase
	fakeSuccess = append(fakeSuccess, // extIdx = 1 (LE u32)
		1, 0, 0, 0)
	fakeSuccess = append(fakeSuccess, 0x00) // System pallet
	fakeSuccess = append(fakeSuccess, 0x00) // ExtrinsicSuccess
	fakeSuccess = append(fakeSuccess, makeDefaultDispatchInfo()...)
	fakeSuccess = append(fakeSuccess, 0x00) // topics = 0
	data = append(data, fakeSuccess...)

	scanResults := ScanExtrinsicResults(data, pallets, 2)

	// Extrinsic 0 should be successful (only success pattern found)
	if !scanResults[0] {
		t.Error("scan: extrinsic 0 should be successful")
	}

	// Extrinsic 1 should be EXCLUDED (conflicting patterns — both success and failure found)
	if _, ok := scanResults[1]; ok {
		t.Error("scan: extrinsic 1 should be excluded due to conflicting success/failure patterns")
	}
}

func TestScanExtrinsicResults_Empty(t *testing.T) {
	pallets := EventPalletConfig{}
	results := ScanExtrinsicResults(nil, pallets, 0)
	if len(results) != 0 {
		t.Error("expected empty results for nil input")
	}
}

func TestDecodeDispatchInfoSize(t *testing.T) {
	// Small weights (single-byte compact: ref_time=10, proof_size=0)
	di := makeCompactDispatchInfo(10, 0, 0, 0)
	size, err := decodeDispatchInfoSize(di)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if size != 4 { // compact(10)=1 + compact(0)=1 + class(1) + pays(1)
		t.Errorf("expected size 4, got %d", size)
	}

	// Larger weights (four-byte compact ref_time, two-byte compact proof_size)
	di = makeCompactDispatchInfo(261973000, 1493, 2, 1)
	size, err = decodeDispatchInfoSize(di)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if size != 8 { // compact(261973000)=4 + compact(1493)=2 + class(1) + pays(1)
		t.Errorf("expected size 8, got %d", size)
	}

	// Invalid DispatchClass
	di = makeCompactDispatchInfo(0, 0, 3, 0)
	_, err = decodeDispatchInfoSize(di)
	if err == nil {
		t.Error("should reject DispatchClass > 2")
	}

	// Invalid Pays
	di = makeCompactDispatchInfo(0, 0, 0, 2)
	_, err = decodeDispatchInfoSize(di)
	if err == nil {
		t.Error("should reject Pays > 1")
	}

	// Too short
	_, err = decodeDispatchInfoSize([]byte{0x00})
	if err == nil {
		t.Error("should reject insufficient data")
	}
}

func TestParseExtrinsicResults_VariableSizeDispatchInfo(t *testing.T) {
	pallets := EventPalletConfig{
		SystemPalletIndex:             0,
		BalancesPalletIndex:           5,
		UtilityPalletIndex:            11,
		TransactionPaymentPalletIndex: 6,
	}

	var events []byte

	// ExtrinsicSuccess for extrinsic 0 with small DispatchInfo (4 bytes)
	events = append(events, buildEvent(0x00, 0, 0, 0x00,
		makeCompactDispatchInfo(10, 0, 2, 0), 0)...)

	// Balances::Withdraw for extrinsic 1
	events = append(events, buildEvent(0x00, 1, 5, 8, make([]byte, 40), 0)...)

	// Balances::Transfer for extrinsic 1
	events = append(events, buildEvent(0x00, 1, 5, 2, make([]byte, 72), 0)...)

	// ExtrinsicSuccess for extrinsic 1 with larger DispatchInfo (8 bytes)
	events = append(events, buildEvent(0x00, 1, 0, 0x00,
		makeCompactDispatchInfo(21233156999, 0, 1, 1), 0)...)

	data := EncodeCompact(4)
	data = append(data, events...)

	results, _, err := ParseExtrinsicResults(data, pallets)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !results[0] {
		t.Error("extrinsic 0 should be successful")
	}
	if !results[1] {
		t.Error("extrinsic 1 should be successful")
	}
}

func TestParseExtrinsicResults_RealBittensorData(t *testing.T) {
	// Real event data from a Bittensor mocknet block with 2 extrinsics
	data, _ := HexDecode("14000000000000002290753e551702000000010000000508ac859f8a216eeb1b320b4c76d118da3d7407fa523484d0a980126d3b4d0d220a29381000000000000000010000000507ac859f8a216eeb1b320b4c76d118da3d7407fa523484d0a980126d3b4d0d220a29381000000000000000010000000600ac859f8a216eeb1b320b4c76d118da3d7407fa523484d0a980126d3b4d0d220a00000000000000000000000000000000000001000000000007874398f10400010100")

	pallets := EventPalletConfig{
		SystemPalletIndex:             0,
		BalancesPalletIndex:           5,
		UtilityPalletIndex:            11,
		TransactionPaymentPalletIndex: 6,
	}

	results, _, err := ParseExtrinsicResults(data, pallets)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !results[0] {
		t.Error("extrinsic 0 should be successful")
	}
	if !results[1] {
		t.Error("extrinsic 1 should be successful")
	}
}

func TestDispatchErrorSizeEstimates_KnownVariant(t *testing.T) {
	// Known variant (e.g., Module = 3) should return exact size
	estimates := dispatchErrorSizeEstimates(3)
	if len(estimates) != 1 || estimates[0] != 6 {
		t.Errorf("expected [6] for Module variant, got %v", estimates)
	}
}

func TestDispatchErrorSizeEstimates_UnknownVariant(t *testing.T) {
	// Unknown variant (e.g., 99) should return all plausible sizes
	estimates := dispatchErrorSizeEstimates(99)
	if len(estimates) != 3 {
		t.Fatalf("expected 3 estimates for unknown variant, got %d", len(estimates))
	}
	// Should contain 1, 2, 6
	expected := map[int]bool{1: true, 2: true, 6: true}
	for _, e := range estimates {
		if !expected[e] {
			t.Errorf("unexpected estimate: %d", e)
		}
	}
}

func TestScanExtrinsicResults_UnknownDispatchErrorVariant(t *testing.T) {
	pallets := EventPalletConfig{
		SystemPalletIndex: 0,
	}

	var events []byte

	// ExtrinsicSuccess for extrinsic 0
	events = append(events, buildEvent(0x00, 0, 0, 0x00, makeDefaultDispatchInfo(), 0)...)

	// ExtrinsicFailed for extrinsic 1 with an unknown DispatchError variant (99).
	// Use variant 99 with no extra data (size=1, like a unit variant), then DispatchInfo.
	di := makeDefaultDispatchInfo()
	failData := append([]byte{99}, di...) // Unknown DispatchError variant + DispatchInfo
	events = append(events, buildEvent(0x00, 1, 0, 0x01, failData, 0)...)

	data := EncodeCompact(2)
	data = append(data, events...)

	// Linear parser stops at unknown pallet events, but scan should find both
	scanResults := ScanExtrinsicResults(data, pallets, 2)
	if !scanResults[0] {
		t.Error("scan: extrinsic 0 should be successful")
	}
	if scanResults[1] != false {
		t.Error("scan: extrinsic 1 should be failed (unknown DispatchError variant matched via estimate)")
	}
}

func TestParseExtrinsicResults_ExtractsFee(t *testing.T) {
	pallets := EventPalletConfig{
		SystemPalletIndex:             0,
		BalancesPalletIndex:           5,
		UtilityPalletIndex:            11,
		TransactionPaymentPalletIndex: 6,
	}

	var events []byte

	// ExtrinsicSuccess for extrinsic 0
	events = append(events, buildEvent(0x00, 0, 0, 0x00, makeDefaultDispatchInfo(), 0)...)

	// TransactionPayment::TransactionFeePaid for extrinsic 1
	// AccountId(32) + actual_fee(8) + tip(8) = 48 bytes
	feeData := make([]byte, 48)
	binary.LittleEndian.PutUint64(feeData[32:40], 125_000) // actual_fee = 125_000 rao
	events = append(events, buildEvent(0x00, 1, 6, 0, feeData, 0)...)

	// ExtrinsicSuccess for extrinsic 1
	events = append(events, buildEvent(0x00, 1, 0, 0x00, makeDefaultDispatchInfo(), 0)...)

	data := EncodeCompact(3)
	data = append(data, events...)

	results, fees, err := ParseExtrinsicResults(data, pallets)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !results[0] {
		t.Error("extrinsic 0 should be successful")
	}
	if !results[1] {
		t.Error("extrinsic 1 should be successful")
	}
	if fees[1] != 125_000 {
		t.Errorf("fee for extrinsic 1: got %d, want 125000", fees[1])
	}
	if _, ok := fees[0]; ok {
		t.Error("extrinsic 0 should not have a fee (no TransactionFeePaid event)")
	}
}
