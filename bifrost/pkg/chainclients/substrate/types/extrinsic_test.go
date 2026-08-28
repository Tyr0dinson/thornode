package types

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	. "gopkg.in/check.v1"
)

func TestExtrinsicSuite(t *testing.T) { TestingT(t) }

type ExtrinsicSuite struct{}

var _ = Suite(&ExtrinsicSuite{})

func loadAssetHubTestMetadata(c *C) *Metadata {
	raw, err := os.ReadFile("testdata/metadata-dot-assethub-2003001.hex")
	c.Assert(err, IsNil)
	metadata, err := ParseMetadata(strings.TrimSpace(string(raw)))
	c.Assert(err, IsNil)
	return metadata
}

func testExtrinsicLayout(c *C, assetPayment bool) ExtrinsicLayout {
	payment := "ChargeTransactionPayment"
	if assetPayment {
		payment = chargeAssetTxPaymentExtensionID
	}
	extensions := []SignedExtension{
		{Identifier: "CheckSpecVersion", ExtraType: "test", AdditionalType: "test"},
		{Identifier: "CheckTxVersion", ExtraType: "test", AdditionalType: "test"},
		{Identifier: "CheckGenesis", ExtraType: "test", AdditionalType: "test"},
		{Identifier: "CheckMortality", ExtraType: "test", AdditionalType: "test"},
		{Identifier: "CheckNonce", ExtraType: "test", AdditionalType: "test"},
		{Identifier: payment, ExtraType: "test", AdditionalType: "test"},
		{Identifier: "CheckMetadataHash", ExtraType: "test", AdditionalType: "test"},
	}
	layout, err := NewExtrinsicLayout(extensions)
	c.Assert(err, IsNil)
	return layout
}

func (s *ExtrinsicSuite) TestNewExtrinsicLayoutRejectsUnsupportedExtension(c *C) {
	extensions := []SignedExtension{
		{Identifier: "CheckSpecVersion", ExtraType: "test", AdditionalType: "test"},
		{Identifier: "CheckTxVersion", ExtraType: "test", AdditionalType: "test"},
		{Identifier: "CheckGenesis", ExtraType: "test", AdditionalType: "test"},
		{Identifier: "CheckMortality", ExtraType: "test", AdditionalType: "test"},
		{Identifier: "CheckNonce", ExtraType: "test", AdditionalType: "test"},
		{Identifier: "ChargeTransactionPayment", ExtraType: "test", AdditionalType: "test"},
		{Identifier: "UnknownExtension", ExtraType: "test", AdditionalType: "test"},
	}
	_, err := NewExtrinsicLayout(extensions)
	c.Assert(err, NotNil)
	c.Check(strings.Contains(err.Error(), "unsupported signed extension"), Equals, true)
}

func encodeTestExtrinsicWithAssetPayment(c *C, ext *Extrinsic, payment []byte) []byte {
	encoded, err := EncodeSignedExtrinsic(ext, testExtrinsicLayout(c, true))
	c.Assert(err, IsNil)
	_, prefixLen, _ := DecodeCompact(encoded)
	body := encoded[prefixLen:]

	tailLen := len(ext.CallData) + 1 // CheckMetadataHash mode
	optionPos := len(body) - tailLen - 1
	tipStart := optionPos - len(EncodeCompact(ext.Tip))

	replaced := make([]byte, 0, len(body)+len(payment)-2)
	replaced = append(replaced, body[:tipStart]...)
	replaced = append(replaced, payment...)
	replaced = append(replaced, body[optionPos+1:]...)

	result := EncodeCompact(uint64(len(replaced)))
	return append(result, replaced...)
}

func (s *ExtrinsicSuite) TestHexEncodeDecodeRoundTrip(c *C) {
	tests := []struct {
		name string
		data []byte
	}{
		{"empty", []byte{}},
		{"single byte", []byte{0x42}},
		{"multiple bytes", []byte{0xde, 0xad, 0xbe, 0xef}},
	}
	for _, tt := range tests {
		encoded := HexEncode(tt.data)
		decoded, err := HexDecode(encoded)
		c.Assert(err, IsNil, Commentf("%s", tt.name))
		c.Assert(bytes.Equal(decoded, tt.data), Equals, true,
			Commentf("%s: got %x, want %x", tt.name, decoded, tt.data))
	}
}

func (s *ExtrinsicSuite) TestHexDecodeStripsPrefix(c *C) {
	data, err := HexDecode("0xdeadbeef")
	c.Assert(err, IsNil)
	c.Assert(bytes.Equal(data, []byte{0xde, 0xad, 0xbe, 0xef}), Equals, true)
}

func (s *ExtrinsicSuite) TestHexDecodeWithoutPrefix(c *C) {
	data, err := HexDecode("deadbeef")
	c.Assert(err, IsNil)
	c.Assert(bytes.Equal(data, []byte{0xde, 0xad, 0xbe, 0xef}), Equals, true)
}

func (s *ExtrinsicSuite) TestHexDecodeInvalid(c *C) {
	_, err := HexDecode("0xZZZZ")
	c.Assert(err, NotNil)
}

func (s *ExtrinsicSuite) TestImmortalEra(c *C) {
	era := ImmortalEra()
	c.Assert(era, HasLen, 1)
	c.Assert(era[0], Equals, byte(0x00))
}

func (s *ExtrinsicSuite) TestMortalEra(c *C) {
	tests := []struct {
		name     string
		period   uint64
		block    uint64
		expected []byte
	}{
		{"period64_block0", 64, 0, []byte{0x05, 0x00}},
		{"period64_block42", 64, 42, []byte{0xa5, 0x02}},
		{"period64_block63", 64, 63, []byte{0xf5, 0x03}},
		{"period64_block64", 64, 64, []byte{0x05, 0x00}},
		{"period4_block0", 4, 0, []byte{0x01, 0x00}},
		{"period4_block3", 4, 3, []byte{0x31, 0x00}},
		{"period256_block100", 256, 100, []byte{0x47, 0x06}},
		{"period4096_block1000", 4096, 1000, []byte{0x8b, 0x3e}},
	}
	for _, tt := range tests {
		era := MortalEra(tt.period, tt.block)
		c.Assert(bytes.Equal(era, tt.expected), Equals, true,
			Commentf("%s: got %x, want %x", tt.name, era, tt.expected))
	}
}

// TestMortalEraSubstrateDecodeRoundTrip verifies the encoded mortal era can be
// decoded back to the correct period and phase using Substrate's decode formula:
//
//	period = 2 << (encoded & 0x0F)
//	quantize = max(period >> 12, 1)
//	phase = (encoded >> 4) * quantize
func (s *ExtrinsicSuite) TestMortalEraSubstrateDecodeRoundTrip(c *C) {
	tests := []struct {
		period uint64
		block  uint64
		wantP  uint64
		wantPh uint64
	}{
		{64, 0, 64, 0},
		{64, 42, 64, 42},
		{64, 63, 64, 63},
		{64, 64, 64, 0},
		{4, 0, 4, 0},
		{4, 3, 4, 3},
		{256, 100, 256, 100},
		{4096, 1000, 4096, 1000},
	}
	for _, tt := range tests {
		era := MortalEra(tt.period, tt.block)
		encoded := uint16(era[0]) | uint16(era[1])<<8

		period := uint64(2) << (encoded & 0x0F)
		quantize := period >> 12
		if quantize < 1 {
			quantize = 1
		}
		phase := uint64(encoded>>4) * quantize

		c.Assert(period, Equals, tt.wantP,
			Commentf("MortalEra(%d,%d) period (encoded=%04x)", tt.period, tt.block, encoded))
		c.Assert(phase, Equals, tt.wantPh,
			Commentf("MortalEra(%d,%d) phase (encoded=%04x)", tt.period, tt.block, encoded))
		c.Assert(phase < period, Equals, true,
			Commentf("MortalEra(%d,%d) phase(%d) >= period(%d)", tt.period, tt.block, phase, period))
	}
}

func (s *ExtrinsicSuite) TestMortalEraIsTwoBytes(c *C) {
	era := MortalEra(64, 10)
	c.Assert(era, HasLen, 2)
}

func (s *ExtrinsicSuite) TestExtrinsicHashDeterministic(c *C) {
	data := []byte{0x01, 0x02, 0x03}
	hash1 := ExtrinsicHash(data)
	hash2 := ExtrinsicHash(data)
	c.Assert(hash1, Equals, hash2)
	c.Assert(hash1, HasLen, 66)
	c.Assert(hash1[:2], Equals, "0x")
}

func (s *ExtrinsicSuite) TestExtrinsicHashDifferentInput(c *C) {
	hash1 := ExtrinsicHash([]byte{0x01})
	hash2 := ExtrinsicHash([]byte{0x02})
	c.Assert(hash1, Not(Equals), hash2)
}

func (s *ExtrinsicSuite) TestBalanceTransferKeepAlive(c *C) {
	dest := make([]byte, 32)
	dest[0] = 0xAA
	call := BalanceTransferKeepAlive(5, 3, dest, cosmos.NewUint(1_000_000))

	c.Assert(call[0], Equals, byte(5))
	c.Assert(call[1], Equals, byte(3))
	c.Assert(call[2], Equals, byte(MultiAddressID))
	c.Assert(call[3], Equals, byte(0xAA))

	amountBytes := call[3+32:]
	decoded, _, err := DecodeCompact(amountBytes)
	c.Assert(err, IsNil)
	c.Assert(decoded.Equal(cosmos.NewUint(1_000_000)), Equals, true)
}

func (s *ExtrinsicSuite) TestSystemRemark(c *C) {
	memo := []byte("SWAP:BTC.BTC:bc1qxyz")
	call := SystemRemark(0, 0, memo)

	c.Assert(call[0], Equals, byte(0))
	c.Assert(call[1], Equals, byte(0))

	remarkLen, n, err := DecodeCompactToInt(call[2:])
	c.Assert(err, IsNil)
	c.Assert(remarkLen, Equals, len(memo))
	c.Assert(string(call[2+n:]), Equals, string(memo))
}

func (s *ExtrinsicSuite) TestSystemRemarkEmpty(c *C) {
	call := SystemRemark(0, 1, []byte{})
	c.Assert(call, HasLen, 3)

	remarkLen, _, err := DecodeCompactToInt(call[2:])
	c.Assert(err, IsNil)
	c.Assert(remarkLen, Equals, 0)
}

func (s *ExtrinsicSuite) TestBatchAll(c *C) {
	call1 := []byte{5, 3, 0x00}
	call2 := []byte{0, 0, 0x04}
	batch := BatchAll(11, 2, [][]byte{call1, call2})

	c.Assert(batch[0], Equals, byte(11))
	c.Assert(batch[1], Equals, byte(2))

	count, n, err := DecodeCompactToInt(batch[2:])
	c.Assert(err, IsNil)
	c.Assert(count, Equals, 2)

	remaining := batch[2+n:]
	expected := append(call1, call2...) //nolint:gocritic
	c.Assert(bytes.Equal(remaining, expected), Equals, true)
}

func (s *ExtrinsicSuite) TestEncodeSignedExtrinsicFormat(c *C) {
	signer := make([]byte, 32)
	signer[0] = 0x01
	sig := make([]byte, 64)
	sig[0] = 0xFF

	ext := &Extrinsic{
		Signer:        signer,
		SignatureType: SignatureTypeEd25519,
		Signature:     sig,
		Era:           MortalEra(64, 10),
		Nonce:         5,
		Tip:           0,
		MetadataMode:  MetadataModeDisabled,
		CallData:      []byte{5, 3, 0x00},
	}

	encoded, err := EncodeSignedExtrinsic(ext, testExtrinsicLayout(c, false))
	c.Assert(err, IsNil)

	_, consumed, err := DecodeCompact(encoded)
	c.Assert(err, IsNil)
	body := encoded[consumed:]

	c.Assert(body[0], Equals, byte(ExtrinsicFormatVersion))
	c.Assert(body[1], Equals, byte(MultiAddressID))
	c.Assert(body[2], Equals, byte(0x01))
	c.Assert(body[34], Equals, byte(SignatureTypeEd25519))
	c.Assert(body[35], Equals, byte(0xFF))
}

func (s *ExtrinsicSuite) TestSigningPayloadContainsAllFields(c *C) {
	callData := []byte{5, 3}
	era := MortalEra(64, 10)
	genesisHash := make([]byte, 32)
	genesisHash[0] = 0xAA
	blockHash := make([]byte, 32)
	blockHash[0] = 0xBB

	payload, err := SigningPayload(callData, era, 1, 0, testExtrinsicLayout(c, false), MetadataModeDisabled, 100, 1, genesisHash, blockHash)
	c.Assert(err, IsNil)
	c.Assert(payload[0], Equals, byte(5))
	c.Assert(payload[1], Equals, byte(3))
	c.Assert(payload[len(payload)-1], Equals, byte(0x00))
	c.Assert(payload[len(payload)-33], Equals, byte(0xBB))
	c.Assert(payload[len(payload)-65], Equals, byte(0xAA))
}

func (s *ExtrinsicSuite) TestSigningPayloadChargeAssetTxPayment(c *C) {
	callData := []byte{10, 3}
	era := MortalEra(64, 10)
	genesisHash := make([]byte, 32)
	blockHash := make([]byte, 32)

	without, err := SigningPayload(callData, era, 1, 0, testExtrinsicLayout(c, false), MetadataModeDisabled, 100, 1, genesisHash, blockHash)
	c.Assert(err, IsNil)
	with, err := SigningPayload(callData, era, 1, 0, testExtrinsicLayout(c, true), MetadataModeDisabled, 100, 1, genesisHash, blockHash)
	c.Assert(err, IsNil)

	// ChargeAssetTxPayment adds exactly one explicit byte: Option<AssetId>::None
	// (0x00) between the tip and the metadata mode byte. The implicit section
	// (spec version onward) is unchanged.
	c.Assert(len(with), Equals, len(without)+1)
	// Explicit section before the option byte: callData | era | nonce | tip
	prefixLen := len(callData) + len(era) + 1 /* compact nonce=1 */ + 1 /* compact tip=0 */
	c.Check(bytes.Equal(with[:prefixLen], without[:prefixLen]), Equals, true)
	c.Check(with[prefixLen], Equals, byte(0x00), Commentf("Option<AssetId>::None byte"))
	c.Check(bytes.Equal(with[prefixLen+1:], without[prefixLen:]), Equals, true,
		Commentf("metadata mode byte and implicit section must be unchanged"))
}

func (s *ExtrinsicSuite) TestSigningPayloadUsesLayoutOrder(c *C) {
	extensions := []SignedExtension{
		{Identifier: "CheckMetadataHash", ExtraType: "test", AdditionalType: "test"},
		{Identifier: "CheckNonce", ExtraType: "test", AdditionalType: "test"},
		{Identifier: "ChargeTransactionPayment", ExtraType: "test", AdditionalType: "test"},
		{Identifier: "CheckMortality", ExtraType: "test", AdditionalType: "test"},
		{Identifier: "CheckGenesis", ExtraType: "test", AdditionalType: "test"},
		{Identifier: "CheckTxVersion", ExtraType: "test", AdditionalType: "test"},
		{Identifier: "CheckSpecVersion", ExtraType: "test", AdditionalType: "test"},
	}
	layout, err := NewExtrinsicLayout(extensions)
	c.Assert(err, IsNil)

	callData := []byte{0xAA}
	era := []byte{0x01, 0x02}
	genesisHash := bytes.Repeat([]byte{0x33}, 32)
	blockHash := bytes.Repeat([]byte{0x44}, 32)
	payload, err := SigningPayload(
		callData, era, 1, 2, layout, MetadataModeDisabled,
		3, 4, genesisHash, blockHash,
	)
	c.Assert(err, IsNil)

	expected := append([]byte{}, callData...)
	expected = append(expected, MetadataModeDisabled)
	expected = append(expected, EncodeCompact(1)...)
	expected = append(expected, EncodeCompact(2)...)
	expected = append(expected, era...)
	expected = append(expected, 0x00) // CheckMetadataHash additional None
	expected = append(expected, blockHash...)
	expected = append(expected, genesisHash...)
	expected = append(expected, EncodeU32LE(4)...)
	expected = append(expected, EncodeU32LE(3)...)
	c.Check(payload, DeepEquals, expected)
}

func (s *ExtrinsicSuite) TestEncodeParseRoundTripChargeAssetTxPayment(c *C) {
	metadata := loadAssetHubTestMetadata(c)
	signer := make([]byte, 32)
	signer[0] = 0x01
	sig := make([]byte, 64)
	sig[0] = 0xFF
	callData := []byte{10, 3, 0x00}

	ext := &Extrinsic{
		Signer:        signer,
		SignatureType: SignatureTypeEd25519,
		Signature:     sig,
		Era:           MortalEra(64, 10),
		Nonce:         5,
		Tip:           0,
		MetadataMode:  MetadataModeDisabled,
		CallData:      callData,
	}
	assetLayout := testExtrinsicLayout(c, true)
	standardLayout := testExtrinsicLayout(c, false)
	encoded, err := EncodeSignedExtrinsic(ext, assetLayout)
	c.Assert(err, IsNil)

	// Round trip with the matching validated layout recovers all fields.
	parsed, err := ParseSignedExtrinsic(encoded, metadata, assetLayout)
	c.Assert(err, IsNil)
	c.Check(bytes.Equal(parsed.Signer, signer), Equals, true)
	c.Check(parsed.Nonce, Equals, uint64(5))
	c.Check(parsed.Tip, Equals, uint64(0))
	c.Check(parsed.MetadataMode, Equals, byte(MetadataModeDisabled))
	c.Check(bytes.Equal(parsed.CallData, callData), Equals, true)

	// Asset payment encodes one extra Option<AssetId>::None byte.
	standardEncoded, err := EncodeSignedExtrinsic(ext, standardLayout)
	c.Assert(err, IsNil)
	c.Check(len(encoded), Equals, len(standardEncoded)+1)

	// Parsing without the flag misaligns by one byte: the option byte is
	// consumed as the metadata mode byte and the call data shifts. Pin the
	// shift so a params/codec mismatch cannot go unnoticed.
	misparsed, err := ParseSignedExtrinsic(encoded, nil, standardLayout)
	c.Assert(err, IsNil)
	c.Check(bytes.Equal(misparsed.CallData, callData), Equals, false)

	// A Some(assetId) variant (vault never signs one) must be rejected: flip
	// the option byte, which sits directly before the mode byte and call data.
	mutated := make([]byte, len(encoded))
	copy(mutated, encoded)
	optionPos := len(mutated) - len(callData) - 1 /* mode byte */ - 1 /* option byte */
	c.Assert(mutated[optionPos], Equals, byte(0x00))
	mutated[optionPos] = 0x01
	_, err = ParseSignedExtrinsic(mutated, metadata, assetLayout)
	c.Assert(err, NotNil)
	c.Check(strings.Contains(err.Error(), "asset transaction payment"), Equals, true,
		Commentf("got: %v", err))
}

func (s *ExtrinsicSuite) TestParseChargeAssetTxPaymentWithLocation(c *C) {
	metadata := loadAssetHubTestMetadata(c)
	callData := []byte{10, 3, 0x00, 0xAA}
	ext := &Extrinsic{
		Signer:        make([]byte, 32),
		SignatureType: SignatureTypeEd25519,
		Signature:     make([]byte, 64),
		Era:           MortalEra(128, 10),
		Nonce:         7,
		Tip:           0,
		MetadataMode:  MetadataModeDisabled,
		CallData:      callData,
	}

	payment := []byte{0x00, 0x01, 0x00, 0x02, 0x04, 50, 0x05}
	payment = append(payment, EncodeCompact(1984)...)
	encoded := encodeTestExtrinsicWithAssetPayment(c, ext, payment)

	parsed, err := ParseSignedExtrinsic(encoded, metadata, testExtrinsicLayout(c, true))
	c.Assert(err, IsNil)
	c.Check(parsed.Nonce, Equals, uint64(7))
	c.Check(parsed.Tip, Equals, uint64(0))
	c.Check(bytes.Equal(parsed.CallData, callData), Equals, true)
}
