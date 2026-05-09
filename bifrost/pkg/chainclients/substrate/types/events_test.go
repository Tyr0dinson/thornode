package types

import (
	"encoding/binary"
	"testing"

	. "gopkg.in/check.v1"
)

func TestEventsSuite(t *testing.T) { TestingT(t) }

type EventsSuite struct{}

var _ = Suite(&EventsSuite{})

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

// buildTransferEvent builds a Balances::Transfer event record (from + to + amount
// layout). Topics is always empty (0x00 trailing byte), which is what real
// Substrate runtimes emit for balance events.
func buildTransferEvent(extIdx uint32, balancesPallet byte, balanceSize int) []byte {
	return buildEvent(0x00, extIdx, balancesPallet, 0x02, make([]byte, 32*2+balanceSize), 0)
}

func (s *EventsSuite) TestParseExtrinsicResults_Success(c *C) {
	for _, balanceSize := range []int{8, 16} {
		pallets := EventPalletConfig{
			SystemPalletIndex:             0,
			BalancesPalletIndex:           5,
			UtilityPalletIndex:            11,
			TransactionPaymentPalletIndex: 6,
			BalanceSize:                   balanceSize,
		}

		var events []byte

		// Event 1: ExtrinsicSuccess for extrinsic 0
		events = append(events, buildEvent(0x00, 0, 0, 0x00, makeDefaultDispatchInfo(), 0)...)

		// Event 2: Balances::Withdraw for extrinsic 1 (account + balance)
		events = append(events, buildEvent(0x00, 1, 5, 8, make([]byte, 32+balanceSize), 0)...)

		// Event 3: Balances::Transfer for extrinsic 1 (from + to + amount)
		events = append(events, buildEvent(0x00, 1, 5, 2, make([]byte, 32*2+balanceSize), 0)...)

		// Event 4: Balances::Deposit for extrinsic 1 (account + balance)
		events = append(events, buildEvent(0x00, 1, 5, 7, make([]byte, 32+balanceSize), 0)...)

		// Event 5: TransactionPayment::TransactionFeePaid (account + fee + tip)
		events = append(events, buildEvent(0x00, 1, 6, 0, make([]byte, 32+balanceSize*2), 0)...)

		// Event 6: ExtrinsicSuccess for extrinsic 1
		events = append(events, buildEvent(0x00, 1, 0, 0x00, makeDefaultDispatchInfo(), 0)...)

		data := EncodeCompact(6)
		data = append(data, events...)

		results, _, err := ParseExtrinsicResults(data, pallets)
		c.Assert(err, IsNil, Commentf("balanceSize=%d", balanceSize))
		c.Assert(len(results), Equals, 2, Commentf("balanceSize=%d", balanceSize))
		c.Check(results[0], Equals, true, Commentf("balanceSize=%d: extrinsic 0 should be successful", balanceSize))
		c.Check(results[1], Equals, true, Commentf("balanceSize=%d: extrinsic 1 should be successful", balanceSize))
	}
}

func (s *EventsSuite) TestParseExtrinsicResults_Failed(c *C) {
	pallets := EventPalletConfig{
		SystemPalletIndex:             0,
		BalancesPalletIndex:           5,
		UtilityPalletIndex:            11,
		TransactionPaymentPalletIndex: 6,
		BalanceSize:                   8,
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
	c.Assert(err, IsNil)
	c.Check(results[0], Equals, true, Commentf("extrinsic 0 should be successful"))
	c.Check(results[1], Equals, false, Commentf("extrinsic 1 should be failed"))
}

func (s *EventsSuite) TestParseExtrinsicResults_WithBatchEvents(c *C) {
	for _, balanceSize := range []int{8, 16} {
		pallets := EventPalletConfig{
			SystemPalletIndex:             0,
			BalancesPalletIndex:           5,
			UtilityPalletIndex:            11,
			TransactionPaymentPalletIndex: 6,
			BalanceSize:                   balanceSize,
		}

		var events []byte

		// ExtrinsicSuccess for extrinsic 0 (timestamp)
		events = append(events, buildEvent(0x00, 0, 0, 0x00, makeDefaultDispatchInfo(), 0)...)

		// Balances::Withdraw for extrinsic 1
		events = append(events, buildEvent(0x00, 1, 5, 8, make([]byte, 32+balanceSize), 0)...)

		// Balances::Transfer for extrinsic 1
		events = append(events, buildEvent(0x00, 1, 5, 2, make([]byte, 32*2+balanceSize), 0)...)

		// Utility::ItemCompleted for extrinsic 1
		events = append(events, buildEvent(0x00, 1, 11, 3, nil, 0)...)

		// System::Remarked for extrinsic 1 (AccountId + Hash = 64 bytes)
		events = append(events, buildEvent(0x00, 1, 0, 0x05, make([]byte, 64), 0)...)

		// Utility::ItemCompleted for extrinsic 1
		events = append(events, buildEvent(0x00, 1, 11, 3, nil, 0)...)

		// Utility::BatchCompleted for extrinsic 1
		events = append(events, buildEvent(0x00, 1, 11, 1, nil, 0)...)

		// Balances::Deposit for extrinsic 1
		events = append(events, buildEvent(0x00, 1, 5, 7, make([]byte, 32+balanceSize), 0)...)

		// TransactionPayment::TransactionFeePaid for extrinsic (account + fee + tip)
		events = append(events, buildEvent(0x00, 1, 6, 0, make([]byte, 32+balanceSize*2), 0)...)

		// ExtrinsicSuccess for extrinsic 1
		events = append(events, buildEvent(0x00, 1, 0, 0x00, makeDefaultDispatchInfo(), 0)...)

		data := EncodeCompact(10)
		data = append(data, events...)

		results, _, err := ParseExtrinsicResults(data, pallets)
		c.Assert(err, IsNil, Commentf("balanceSize=%d", balanceSize))
		c.Check(results[0], Equals, true, Commentf("balanceSize=%d: extrinsic 0 should be successful", balanceSize))
		c.Check(results[1], Equals, true, Commentf("balanceSize=%d: extrinsic 1 should be successful", balanceSize))
	}
}

func (s *EventsSuite) TestParseExtrinsicResults_UnknownPallet(c *C) {
	pallets := EventPalletConfig{
		SystemPalletIndex:             0,
		BalancesPalletIndex:           5,
		UtilityPalletIndex:            11,
		TransactionPaymentPalletIndex: 6,
		BalanceSize:                   8,
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

	c.Check(results[0], Equals, true, Commentf("extrinsic 0 should be successful"))

	_, ok := results[1]
	c.Check(ok, Equals, false, Commentf("extrinsic 1 should not be in results (parsing stopped at unknown pallet)"))
}

func (s *EventsSuite) TestParseExtrinsicResults_Empty(c *C) {
	pallets := EventPalletConfig{}

	results, _, err := ParseExtrinsicResults(nil, pallets)
	c.Assert(err, IsNil)
	c.Check(results, IsNil, Commentf("expected nil results for nil input"))
}

func (s *EventsSuite) TestParseExtrinsicResults_ModuleError(c *C) {
	pallets := EventPalletConfig{
		SystemPalletIndex:             0,
		BalancesPalletIndex:           5,
		UtilityPalletIndex:            11,
		TransactionPaymentPalletIndex: 6,
		BalanceSize:                   8,
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
	c.Assert(err, IsNil)
	c.Check(results[0], Equals, false, Commentf("extrinsic 0 should be failed"))
}

func (s *EventsSuite) TestScanExtrinsicResults_FindsEventsAfterUnknownPallet(c *C) {
	pallets := EventPalletConfig{
		SystemPalletIndex:             0,
		BalancesPalletIndex:           5,
		BalancesTransferEventIndex:    2,
		UtilityPalletIndex:            11,
		TransactionPaymentPalletIndex: 6,
		BalanceSize:                   8,
	}

	var events []byte

	// Transfer + ExtrinsicSuccess for extrinsic 0
	events = append(events, buildTransferEvent(0, 5, 8)...)
	events = append(events, buildEvent(0x00, 0, 0, 0x00, makeDefaultDispatchInfo(), 0)...)

	// Unknown pallet event for extrinsic 1 (pallet 99, 20 bytes data)
	events = append(events, buildEvent(0x00, 1, 99, 0, make([]byte, 20), 0)...)

	// Transfer + ExtrinsicSuccess for extrinsic 1 (after the unknown event)
	events = append(events, buildTransferEvent(1, 5, 8)...)
	events = append(events, buildEvent(0x00, 1, 0, 0x00, makeDefaultDispatchInfo(), 0)...)

	data := EncodeCompact(5)
	data = append(data, events...)

	// Linear parser should miss extrinsic 1
	linearResults, _, _ := ParseExtrinsicResults(data, pallets)
	_, ok := linearResults[1]
	c.Check(ok, Equals, false, Commentf("linear parser should not find extrinsic 1"))

	// Scan-based parser should find both
	scanResults, _ := ScanExtrinsicResults(data, pallets, 2)
	c.Check(scanResults[0], Equals, true, Commentf("scan: extrinsic 0 should be successful"))
	c.Check(scanResults[1], Equals, true, Commentf("scan: extrinsic 1 should be successful"))
}

func (s *EventsSuite) TestScanExtrinsicResults_FindsFailedAfterUnknownPallet(c *C) {
	pallets := EventPalletConfig{
		SystemPalletIndex:             0,
		BalancesPalletIndex:           5,
		BalancesTransferEventIndex:    2,
		UtilityPalletIndex:            11,
		TransactionPaymentPalletIndex: 6,
		BalanceSize:                   8,
	}

	var events []byte

	// Transfer + ExtrinsicSuccess for extrinsic 0
	events = append(events, buildTransferEvent(0, 5, 8)...)
	events = append(events, buildEvent(0x00, 0, 0, 0x00, makeDefaultDispatchInfo(), 0)...)

	// Unknown pallet event for extrinsic 1 (pallet 50, non-zero data to avoid
	// false pattern matches within the payload)
	unknownData := make([]byte, 64)
	for i := range unknownData {
		unknownData[i] = 0xAB
	}
	events = append(events, buildEvent(0x00, 1, 50, 2, unknownData, 0)...)

	// ExtrinsicFailed for extrinsic 1 — no Transfer needed for failure path.
	di := makeDefaultDispatchInfo()
	failData := append([]byte{0x00}, di...) // DispatchError::Other + DispatchInfo
	events = append(events, buildEvent(0x00, 1, 0, 0x01, failData, 0)...)

	data := EncodeCompact(4)
	data = append(data, events...)

	scanResults, _ := ScanExtrinsicResults(data, pallets, 2)
	c.Check(scanResults[0], Equals, true, Commentf("scan: extrinsic 0 should be successful"))
	c.Check(scanResults[1], Equals, false, Commentf("scan: extrinsic 1 should be failed"))
}

func (s *EventsSuite) TestScanExtrinsicResults_RejectsInvalidDispatchInfo(c *C) {
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

	scanResults, _ := ScanExtrinsicResults(data, pallets, 1)
	_, ok := scanResults[0]
	c.Check(ok, Equals, false, Commentf("scan should reject event with invalid DispatchClass"))
}

func (s *EventsSuite) TestScanExtrinsicResults_RejectsOutOfRangeExtrinsicIdx(c *C) {
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

	scanResults, _ := ScanExtrinsicResults(data, pallets, 2)
	_, ok := scanResults[100]
	c.Check(ok, Equals, false, Commentf("scan should reject extrinsic index out of range"))
}

func (s *EventsSuite) TestScanExtrinsicResults_ConflictingMatchesExcluded(c *C) {
	pallets := EventPalletConfig{
		SystemPalletIndex:          0,
		BalancesPalletIndex:        5,
		BalancesTransferEventIndex: 2,
		BalanceSize:                8,
	}

	// Build data where the scan finds BOTH ExtrinsicSuccess and ExtrinsicFailed
	// patterns for extrinsic 1. This simulates a false positive from unknown pallet
	// event data containing a matching byte pattern.
	var data []byte
	data = append(data, EncodeCompact(5)...) // 5 "events" (the scan doesn't use the count)

	// Real Transfer + ExtrinsicSuccess for extrinsic 0
	data = append(data, buildTransferEvent(0, 5, 8)...)
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

	scanResults, _ := ScanExtrinsicResults(data, pallets, 2)

	c.Check(scanResults[0], Equals, true, Commentf("scan: extrinsic 0 should be successful"))

	_, ok := scanResults[1]
	c.Check(ok, Equals, false, Commentf("scan: extrinsic 1 should be excluded due to conflicting success/failure patterns"))
}

func (s *EventsSuite) TestScanExtrinsicResults_Empty(c *C) {
	pallets := EventPalletConfig{}
	results, _ := ScanExtrinsicResults(nil, pallets, 0)
	c.Check(len(results), Equals, 0, Commentf("expected empty results for nil input"))
}

func (s *EventsSuite) TestDecodeDispatchInfoSize(c *C) {
	// Small weights (single-byte compact: ref_time=10, proof_size=0)
	di := makeCompactDispatchInfo(10, 0, 0, 0)
	size, err := decodeDispatchInfoSize(di)
	c.Assert(err, IsNil)
	c.Check(size, Equals, 4, Commentf("compact(10)=1 + compact(0)=1 + class(1) + pays(1)"))

	// Larger weights (four-byte compact ref_time, two-byte compact proof_size)
	di = makeCompactDispatchInfo(261973000, 1493, 2, 1)
	size, err = decodeDispatchInfoSize(di)
	c.Assert(err, IsNil)
	c.Check(size, Equals, 8, Commentf("compact(261973000)=4 + compact(1493)=2 + class(1) + pays(1)"))

	// Invalid DispatchClass
	di = makeCompactDispatchInfo(0, 0, 3, 0)
	_, err = decodeDispatchInfoSize(di)
	c.Check(err, NotNil, Commentf("should reject DispatchClass > 2"))

	// Invalid Pays
	di = makeCompactDispatchInfo(0, 0, 0, 2)
	_, err = decodeDispatchInfoSize(di)
	c.Check(err, NotNil, Commentf("should reject Pays > 1"))

	// Too short
	_, err = decodeDispatchInfoSize([]byte{0x00})
	c.Check(err, NotNil, Commentf("should reject insufficient data"))
}

func (s *EventsSuite) TestParseExtrinsicResults_VariableSizeDispatchInfo(c *C) {
	for _, balanceSize := range []int{8, 16} {
		pallets := EventPalletConfig{
			SystemPalletIndex:             0,
			BalancesPalletIndex:           5,
			UtilityPalletIndex:            11,
			TransactionPaymentPalletIndex: 6,
			BalanceSize:                   balanceSize,
		}

		var events []byte

		// ExtrinsicSuccess for extrinsic 0 with small DispatchInfo (4 bytes)
		events = append(events, buildEvent(0x00, 0, 0, 0x00,
			makeCompactDispatchInfo(10, 0, 2, 0), 0)...)

		// Balances::Withdraw for extrinsic 1
		events = append(events, buildEvent(0x00, 1, 5, 8, make([]byte, 32+balanceSize), 0)...)

		// Balances::Transfer for extrinsic 1
		events = append(events, buildEvent(0x00, 1, 5, 2, make([]byte, 32*2+balanceSize), 0)...)

		// ExtrinsicSuccess for extrinsic 1 with larger DispatchInfo (8 bytes)
		events = append(events, buildEvent(0x00, 1, 0, 0x00,
			makeCompactDispatchInfo(21233156999, 0, 1, 1), 0)...)

		data := EncodeCompact(4)
		data = append(data, events...)

		results, _, err := ParseExtrinsicResults(data, pallets)
		c.Assert(err, IsNil, Commentf("balanceSize=%d", balanceSize))
		c.Check(results[0], Equals, true, Commentf("balanceSize=%d: extrinsic 0 should be successful", balanceSize))
		c.Check(results[1], Equals, true, Commentf("balanceSize=%d: extrinsic 1 should be successful", balanceSize))
	}
}

func (s *EventsSuite) TestParseExtrinsicResults_RealBittensorData(c *C) {
	// Real event data from a Bittensor mocknet block with 2 extrinsics
	data, _ := HexDecode("14000000000000002290753e551702000000010000000508ac859f8a216eeb1b320b4c76d118da3d7407fa523484d0a980126d3b4d0d220a29381000000000000000010000000507ac859f8a216eeb1b320b4c76d118da3d7407fa523484d0a980126d3b4d0d220a29381000000000000000010000000600ac859f8a216eeb1b320b4c76d118da3d7407fa523484d0a980126d3b4d0d220a00000000000000000000000000000000000001000000000007874398f10400010100")

	pallets := EventPalletConfig{
		SystemPalletIndex:             0,
		BalancesPalletIndex:           5,
		UtilityPalletIndex:            11,
		TransactionPaymentPalletIndex: 6,
		BalanceSize:                   8,
	}

	results, _, err := ParseExtrinsicResults(data, pallets)
	c.Assert(err, IsNil)
	c.Check(results[0], Equals, true, Commentf("extrinsic 0 should be successful"))
	c.Check(results[1], Equals, true, Commentf("extrinsic 1 should be successful"))
}

func (s *EventsSuite) TestDispatchErrorSizeEstimates_KnownVariant(c *C) {
	// Known variant (e.g., Module = 3) should return exact size
	estimates := dispatchErrorSizeEstimates(3)
	c.Assert(len(estimates), Equals, 1, Commentf("expected 1 estimate for Module variant"))
	c.Check(estimates[0], Equals, 6, Commentf("expected size 6 for Module variant"))
}

func (s *EventsSuite) TestDispatchErrorSizeEstimates_UnknownVariant(c *C) {
	// Unknown variant (e.g., 99) should return all plausible sizes
	estimates := dispatchErrorSizeEstimates(99)
	c.Assert(len(estimates), Equals, 3, Commentf("expected 3 estimates for unknown variant"))

	expected := map[int]bool{1: true, 2: true, 6: true}
	for _, e := range estimates {
		c.Check(expected[e], Equals, true, Commentf("unexpected estimate: %d", e))
	}
}

func (s *EventsSuite) TestScanExtrinsicResults_UnknownDispatchErrorVariant(c *C) {
	pallets := EventPalletConfig{
		SystemPalletIndex:          0,
		BalancesPalletIndex:        5,
		BalancesTransferEventIndex: 2,
		BalanceSize:                8,
	}

	var events []byte

	// Transfer + ExtrinsicSuccess for extrinsic 0
	events = append(events, buildTransferEvent(0, 5, 8)...)
	events = append(events, buildEvent(0x00, 0, 0, 0x00, makeDefaultDispatchInfo(), 0)...)

	// ExtrinsicFailed for extrinsic 1 with an unknown DispatchError variant (99).
	// Use variant 99 with no extra data (size=1, like a unit variant), then DispatchInfo.
	di := makeDefaultDispatchInfo()
	failData := append([]byte{99}, di...) // Unknown DispatchError variant + DispatchInfo
	events = append(events, buildEvent(0x00, 1, 0, 0x01, failData, 0)...)

	data := EncodeCompact(3)
	data = append(data, events...)

	// Linear parser stops at unknown pallet events, but scan should find both
	scanResults, _ := ScanExtrinsicResults(data, pallets, 2)
	c.Check(scanResults[0], Equals, true, Commentf("scan: extrinsic 0 should be successful"))
	c.Check(scanResults[1], Equals, false, Commentf("scan: extrinsic 1 should be failed (unknown DispatchError variant matched via estimate)"))
}

func (s *EventsSuite) TestParseExtrinsicResults_ExtractsFee(c *C) {
	for _, balanceSize := range []int{8, 16} {
		pallets := EventPalletConfig{
			SystemPalletIndex:             0,
			BalancesPalletIndex:           5,
			UtilityPalletIndex:            11,
			TransactionPaymentPalletIndex: 6,
			BalanceSize:                   balanceSize,
		}

		var events []byte

		// ExtrinsicSuccess for extrinsic 0
		events = append(events, buildEvent(0x00, 0, 0, 0x00, makeDefaultDispatchInfo(), 0)...)

		// TransactionPayment::TransactionFeePaid for extrinsic 1
		// AccountId(32) + actual_fee(balanceSize) + tip(balanceSize)
		feeData := make([]byte, 32+balanceSize*2)
		binary.LittleEndian.PutUint64(feeData[32:40], 125_000) // actual_fee = 125_000 rao
		events = append(events, buildEvent(0x00, 1, 6, 0, feeData, 0)...)

		// ExtrinsicSuccess for extrinsic 1
		events = append(events, buildEvent(0x00, 1, 0, 0x00, makeDefaultDispatchInfo(), 0)...)

		data := EncodeCompact(3)
		data = append(data, events...)

		results, fees, err := ParseExtrinsicResults(data, pallets)
		c.Assert(err, IsNil, Commentf("balanceSize=%d", balanceSize))
		c.Check(results[0], Equals, true, Commentf("balanceSize=%d: extrinsic 0 should be successful", balanceSize))
		c.Check(results[1], Equals, true, Commentf("balanceSize=%d: extrinsic 1 should be successful", balanceSize))
		c.Check(fees[1], Equals, uint64(125_000), Commentf("balanceSize=%d: fee for extrinsic 1", balanceSize))
		_, ok := fees[0]
		c.Check(ok, Equals, false, Commentf("balanceSize=%d: extrinsic 0 should not have a fee", balanceSize))
	}
}

// -----------------------------------------------------------------------------
// A12 gate: scan-matched ExtrinsicSuccess requires a matching Balances::Transfer
// event at the same extIdx. Without the gate, the pattern-based fallback could
// false-positive a transfer credit from unrelated unknown-pallet event bytes.
// -----------------------------------------------------------------------------

func (s *EventsSuite) TestScanExtrinsicResults_SuccessWithTransferCredited(c *C) {
	for _, balanceSize := range []int{8, 16} {
		pallets := EventPalletConfig{
			SystemPalletIndex:             0,
			BalancesPalletIndex:           5,
			BalancesTransferEventIndex:    2,
			UtilityPalletIndex:            11,
			TransactionPaymentPalletIndex: 6,
			BalanceSize:                   balanceSize,
		}

		var events []byte

		// extrinsic 0: Transfer + ExtrinsicSuccess.
		events = append(events, buildTransferEvent(0, 5, balanceSize)...)
		events = append(events, buildEvent(0x00, 0, 0, 0x00, makeDefaultDispatchInfo(), 0)...)

		// Unknown pallet event between extrinsics to force scan resolution.
		events = append(events, buildEvent(0x00, 1, 99, 0, make([]byte, 20), 0)...)

		// extrinsic 1: Transfer + ExtrinsicSuccess.
		events = append(events, buildTransferEvent(1, 5, balanceSize)...)
		events = append(events, buildEvent(0x00, 1, 0, 0x00, makeDefaultDispatchInfo(), 0)...)

		data := EncodeCompact(5)
		data = append(data, events...)

		results, _ := ScanExtrinsicResults(data, pallets, 2)
		c.Check(results[0], Equals, true,
			Commentf("balanceSize=%d: ext 0 should be credited (success + transfer)", balanceSize))
		c.Check(results[1], Equals, true,
			Commentf("balanceSize=%d: ext 1 should be credited (success + transfer)", balanceSize))
	}
}

func (s *EventsSuite) TestScanExtrinsicResults_SuccessWithoutTransferBlocked(c *C) {
	// Core A12 regression test. Without the transfer-event gate, a scan-matched
	// ExtrinsicSuccess (for example from a pattern collision in unknown-pallet
	// event bytes) would false-credit the user. With the gate, a success match
	// that lacks a corresponding Balances::Transfer is silently dropped.
	for _, balanceSize := range []int{8, 16} {
		pallets := EventPalletConfig{
			SystemPalletIndex:             0,
			BalancesPalletIndex:           5,
			BalancesTransferEventIndex:    2,
			UtilityPalletIndex:            11,
			TransactionPaymentPalletIndex: 6,
			BalanceSize:                   balanceSize,
		}

		var events []byte

		// Baseline: ext 0 is fully compliant (transfer + success) → must be credited.
		events = append(events, buildTransferEvent(0, 5, balanceSize)...)
		events = append(events, buildEvent(0x00, 0, 0, 0x00, makeDefaultDispatchInfo(), 0)...)

		// Unknown pallet event forces scan resolution for ext 1.
		events = append(events, buildEvent(0x00, 1, 99, 0, make([]byte, 20), 0)...)

		// Real ExtrinsicSuccess for ext 1 but NO corresponding Balances::Transfer.
		// Pre-fix: scanResults[1] == true (false credit).
		// Post-fix: scanResults[1] absent.
		events = append(events, buildEvent(0x00, 1, 0, 0x00, makeDefaultDispatchInfo(), 0)...)

		data := EncodeCompact(4)
		data = append(data, events...)

		results, _ := ScanExtrinsicResults(data, pallets, 2)
		c.Check(results[0], Equals, true,
			Commentf("balanceSize=%d: ext 0 should be credited", balanceSize))

		_, ok := results[1]
		c.Check(ok, Equals, false,
			Commentf("balanceSize=%d: ext 1 must NOT be credited without a Transfer event", balanceSize))
	}
}

func (s *EventsSuite) TestScanExtrinsicResults_FailureDoesNotRequireTransfer(c *C) {
	// The gate applies only to successes. A failed extrinsic with no Transfer
	// must still be reported as false — that instructs the caller to skip it.
	pallets := EventPalletConfig{
		SystemPalletIndex:             0,
		BalancesPalletIndex:           5,
		BalancesTransferEventIndex:    2,
		UtilityPalletIndex:            11,
		TransactionPaymentPalletIndex: 6,
		BalanceSize:                   8,
	}

	var events []byte

	// Baseline credit for ext 0.
	events = append(events, buildTransferEvent(0, 5, 8)...)
	events = append(events, buildEvent(0x00, 0, 0, 0x00, makeDefaultDispatchInfo(), 0)...)

	// Unknown pallet event for ext 1 — forces scan. Use non-zero filler so
	// the bytes don't accidentally form a valid pattern for a different extIdx.
	unknownData := make([]byte, 64)
	for i := range unknownData {
		unknownData[i] = 0xAB
	}
	events = append(events, buildEvent(0x00, 1, 50, 2, unknownData, 0)...)

	// ExtrinsicFailed for ext 1, no Transfer required.
	di := makeDefaultDispatchInfo()
	failData := append([]byte{0x00}, di...)
	events = append(events, buildEvent(0x00, 1, 0, 0x01, failData, 0)...)

	data := EncodeCompact(4)
	data = append(data, events...)

	results, _ := ScanExtrinsicResults(data, pallets, 2)
	c.Check(results[0], Equals, true)
	c.Check(results[1], Equals, false,
		Commentf("failure must still be reported regardless of Transfer presence"))
}

func (s *EventsSuite) TestScanExtrinsicResults_TransferForDifferentExtIdxDoesNotSatisfyGate(c *C) {
	// The gate is keyed per-extIdx. A transfer at ext 2 must not vouch for a
	// success at ext 1.
	pallets := EventPalletConfig{
		SystemPalletIndex:          0,
		BalancesPalletIndex:        5,
		BalancesTransferEventIndex: 2,
		BalanceSize:                8,
	}

	var events []byte

	// Success for ext 1, Transfer for ext 2 (mismatched).
	events = append(events, buildEvent(0x00, 1, 0, 0x00, makeDefaultDispatchInfo(), 0)...)
	events = append(events, buildTransferEvent(2, 5, 8)...)

	data := EncodeCompact(2)
	data = append(data, events...)

	results, _ := ScanExtrinsicResults(data, pallets, 3)
	_, ok := results[1]
	c.Check(ok, Equals, false,
		Commentf("transfer for ext 2 must not vouch for success at ext 1"))
}

func (s *EventsSuite) TestScanExtrinsicResults_BalancesDepositDoesNotSatisfyGate(c *C) {
	// Only Balances::Transfer (event index 2) satisfies the gate.
	// Balances::Deposit (event index 7) must not count.
	pallets := EventPalletConfig{
		SystemPalletIndex:          0,
		BalancesPalletIndex:        5,
		BalancesTransferEventIndex: 2,
		BalanceSize:                8,
	}

	var events []byte

	// Success for ext 1.
	events = append(events, buildEvent(0x00, 1, 0, 0x00, makeDefaultDispatchInfo(), 0)...)

	// Balances::Deposit (event 7) for ext 1 — wrong event, should NOT satisfy gate.
	events = append(events, buildEvent(0x00, 1, 5, 7, make([]byte, 32+8), 0)...)

	data := EncodeCompact(2)
	data = append(data, events...)

	results, _ := ScanExtrinsicResults(data, pallets, 2)
	_, ok := results[1]
	c.Check(ok, Equals, false,
		Commentf("Balances::Deposit must not satisfy the Transfer-only gate"))
}

func (s *EventsSuite) TestScanExtrinsicResults_TransferWithNonEmptyTopicsNotMatched(c *C) {
	// A Transfer-shaped record followed by a non-0x00 byte at the topics
	// position shouldn't be recognized as a transfer. The scan requires the
	// trailing byte to be 0x00 (compact-encoded empty Vec<H256>).
	pallets := EventPalletConfig{
		SystemPalletIndex:          0,
		BalancesPalletIndex:        5,
		BalancesTransferEventIndex: 2,
		BalanceSize:                8,
	}

	var events []byte

	// Success for ext 1.
	events = append(events, buildEvent(0x00, 1, 0, 0x00, makeDefaultDispatchInfo(), 0)...)

	// Transfer-shaped event for ext 1 but with topicCount=1 → trailing compact
	// byte is 0x04, not 0x00. Scan must reject it as a Transfer match, so the
	// gate for ext 1 stays unsatisfied.
	events = append(events, buildEvent(0x00, 1, 5, 2, make([]byte, 32*2+8), 1)...)

	data := EncodeCompact(2)
	data = append(data, events...)

	results, _ := ScanExtrinsicResults(data, pallets, 2)
	_, ok := results[1]
	c.Check(ok, Equals, false,
		Commentf("transfer-shaped bytes with non-empty topics must not satisfy gate"))
}

func (s *EventsSuite) TestScanExtrinsicResults_TransferPatternTruncatedRejected(c *C) {
	// A Transfer-shaped prefix at the very end of the data (with no room for
	// the trailing topics byte) must be rejected by the bounds check.
	pallets := EventPalletConfig{
		SystemPalletIndex:          0,
		BalancesPalletIndex:        5,
		BalancesTransferEventIndex: 2,
		BalanceSize:                8,
	}

	var events []byte
	events = append(events, buildEvent(0x00, 1, 0, 0x00, makeDefaultDispatchInfo(), 0)...)

	// Transfer-like bytes truncated right before the topics byte:
	// phase(1) + extIdx(4) + pallet(1) + event(1) + from(32) + to(32) + amount(8) = 79 bytes
	// with no trailing topics byte — scan's pos+totalLen<len(data) must reject.
	truncated := []byte{0x00}
	truncated = append(truncated, 1, 0, 0, 0)          // extIdx = 1
	truncated = append(truncated, 5)                   // Balances pallet
	truncated = append(truncated, 2)                   // Transfer event
	truncated = append(truncated, make([]byte, 72)...) // from+to+amount, no topics byte
	events = append(events, truncated...)

	data := EncodeCompact(1)
	data = append(data, events...)

	results, _ := ScanExtrinsicResults(data, pallets, 2)
	_, ok := results[1]
	c.Check(ok, Equals, false,
		Commentf("truncated transfer pattern at end of data must not satisfy gate"))
}

func (s *EventsSuite) TestScanExtrinsicResults_ConflictingMatchesExcludedEvenWithTransfer(c *C) {
	// If the scan sees both success and failure patterns for the same extIdx,
	// conflict exclusion drops both even when a Transfer exists (so the gate
	// would otherwise admit the success). This verifies conflict exclusion
	// runs independently of the transfer gate.
	pallets := EventPalletConfig{
		SystemPalletIndex:          0,
		BalancesPalletIndex:        5,
		BalancesTransferEventIndex: 2,
		BalanceSize:                8,
	}

	var data []byte
	data = append(data, EncodeCompact(5)...)

	// ext 0: Transfer + Success → credited.
	data = append(data, buildTransferEvent(0, 5, 8)...)
	data = append(data, buildEvent(0x00, 0, 0, 0x00, makeDefaultDispatchInfo(), 0)...)

	// ext 1: real Failed + real Transfer (gate would admit if success also matched).
	di := makeDefaultDispatchInfo()
	failData := append([]byte{0x00}, di...)
	data = append(data, buildEvent(0x00, 1, 0, 0x01, failData, 0)...)
	data = append(data, buildTransferEvent(1, 5, 8)...)

	// Embed a fake ExtrinsicSuccess pattern for ext 1 (collision simulation).
	fakeSuccess := []byte{0x00, 1, 0, 0, 0, 0x00, 0x00}
	fakeSuccess = append(fakeSuccess, makeDefaultDispatchInfo()...)
	fakeSuccess = append(fakeSuccess, 0x00)
	data = append(data, fakeSuccess...)

	results, _ := ScanExtrinsicResults(data, pallets, 2)
	c.Check(results[0], Equals, true)
	_, ok := results[1]
	c.Check(ok, Equals, false,
		Commentf("conflict exclusion must drop ext 1 even when gate would admit"))
}

func (s *EventsSuite) TestScanExtrinsicResults_MultipleExtrinsicsOnlyTransferredCredited(c *C) {
	// Multi-extrinsic block: gate is keyed per-extIdx so only extrinsics with
	// matching Transfer events are credited.
	pallets := EventPalletConfig{
		SystemPalletIndex:          0,
		BalancesPalletIndex:        5,
		BalancesTransferEventIndex: 2,
		BalanceSize:                8,
	}

	var events []byte

	// ext 0: Transfer + Success.
	events = append(events, buildTransferEvent(0, 5, 8)...)
	events = append(events, buildEvent(0x00, 0, 0, 0x00, makeDefaultDispatchInfo(), 0)...)

	// Unknown pallet event — forces scan for the block.
	events = append(events, buildEvent(0x00, 1, 99, 0, make([]byte, 20), 0)...)

	// ext 1: Success only, no Transfer → must be blocked.
	events = append(events, buildEvent(0x00, 1, 0, 0x00, makeDefaultDispatchInfo(), 0)...)

	// ext 2: Transfer + Success.
	events = append(events, buildTransferEvent(2, 5, 8)...)
	events = append(events, buildEvent(0x00, 2, 0, 0x00, makeDefaultDispatchInfo(), 0)...)

	data := EncodeCompact(5)
	data = append(data, events...)

	results, _ := ScanExtrinsicResults(data, pallets, 3)
	c.Check(results[0], Equals, true, Commentf("ext 0 has Transfer → credited"))
	_, ok := results[1]
	c.Check(ok, Equals, false, Commentf("ext 1 has no Transfer → blocked"))
	c.Check(results[2], Equals, true, Commentf("ext 2 has Transfer → credited"))
}

// buildFeeEvent constructs a TransactionPayment::TransactionFeePaid event with
// a 32-byte zero AccountId, the requested actual_fee value LE-encoded over
// balanceSize bytes, and a zero tip. Topics is always empty.
func buildFeeEvent(extIdx uint32, txPaymentPallet byte, balanceSize int, actualFee uint64) []byte {
	data := make([]byte, 32+balanceSize+balanceSize)
	binary.LittleEndian.PutUint64(data[32:32+8], actualFee)
	return buildEvent(0x00, extIdx, txPaymentPallet, 0x00, data, 0)
}

// TestScanExtrinsicResults_RecoversFeesAfterUnknownPallet pins the regression
// behind the simtest TAO solvency mismatch: when bittensor's events stream
// has an unknown pallet event before a later extrinsic's TransactionFeePaid,
// the linear parser stops and the scan fallback used to fill in success but
// not fees. Outbound observations then reported gas of 1 unit and thornode
// under-debited the vault. ScanExtrinsicResults now back-fills the missed
// fees, so the scanner caller can merge them into feeMap.
func (s *EventsSuite) TestScanExtrinsicResults_RecoversFeesAfterUnknownPallet(c *C) {
	tests := []struct {
		name        string
		balanceSize int
	}{
		{name: "TAO u64 balances", balanceSize: 8},
	}

	for _, tt := range tests {
		pallets := EventPalletConfig{
			SystemPalletIndex:             0,
			BalancesPalletIndex:           5,
			BalancesTransferEventIndex:    2,
			UtilityPalletIndex:            11,
			TransactionPaymentPalletIndex: 6,
			BalanceSize:                   tt.balanceSize,
		}

		var events []byte
		// ext 0: fee event before the unknown pallet event — captured by linear parser.
		events = append(events, buildFeeEvent(0, 6, tt.balanceSize, 169570)...)
		// Unknown pallet 99 stops the linear parser.
		events = append(events, buildEvent(0x00, 0, 99, 0, make([]byte, 20), 0)...)
		// ext 1: fee event after the unknown event — only the scan can recover this.
		events = append(events, buildFeeEvent(1, 6, tt.balanceSize, 222222)...)

		data := EncodeCompact(3)
		data = append(data, events...)

		// Linear parser stops at the unknown event and so misses ext 1's fee.
		_, linearFees, _ := ParseExtrinsicResults(data, pallets)
		c.Check(linearFees[0], Equals, uint64(169570),
			Commentf("%s: linear parser captured ext 0 fee", tt.name))
		_, ok := linearFees[1]
		c.Check(ok, Equals, false,
			Commentf("%s: linear parser missed ext 1 fee", tt.name))

		// Scan recovers ext 1's fee.
		_, scanFees := ScanExtrinsicResults(data, pallets, 2)
		c.Check(scanFees[1], Equals, uint64(222222),
			Commentf("%s: scan recovered ext 1 fee", tt.name))
	}
}
