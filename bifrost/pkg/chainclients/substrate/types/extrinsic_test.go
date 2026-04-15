package types

import (
	"bytes"
	"testing"

	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	. "gopkg.in/check.v1"
)

func TestExtrinsicSuite(t *testing.T) { TestingT(t) }

type ExtrinsicSuite struct{}

var _ = Suite(&ExtrinsicSuite{})

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

	encoded := EncodeSignedExtrinsic(ext)

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

	payload := SigningPayload(callData, era, 1, 0, true, MetadataModeDisabled, 100, 1, genesisHash, blockHash)

	c.Assert(payload[0], Equals, byte(5))
	c.Assert(payload[1], Equals, byte(3))
	c.Assert(payload[len(payload)-1], Equals, byte(0x00))
	c.Assert(payload[len(payload)-33], Equals, byte(0xBB))
	c.Assert(payload[len(payload)-65], Equals, byte(0xAA))
}
