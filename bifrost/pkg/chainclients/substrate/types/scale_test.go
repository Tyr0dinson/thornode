package types

import (
	"bytes"
	"testing"

	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	. "gopkg.in/check.v1"
)

func TestScaleSuite(t *testing.T) { TestingT(t) }

type ScaleSuite struct{}

var _ = Suite(&ScaleSuite{})

func (s *ScaleSuite) TestEncodeCompactSingleByte(c *C) {
	tests := []struct {
		value    uint64
		expected []byte
	}{
		{0, []byte{0x00}},
		{1, []byte{0x04}},
		{42, []byte{0xa8}},
		{63, []byte{0xfc}},
	}

	for _, tt := range tests {
		result := EncodeCompact(tt.value)
		c.Assert(bytes.Equal(result, tt.expected), Equals, true,
			Commentf("EncodeCompact(%d): got %x, want %x", tt.value, result, tt.expected))
	}
}

func (s *ScaleSuite) TestEncodeCompactTwoBytes(c *C) {
	tests := []struct {
		value    uint64
		expected []byte
	}{
		{64, []byte{0x01, 0x01}},
		{16383, []byte{0xfd, 0xff}},
	}

	for _, tt := range tests {
		result := EncodeCompact(tt.value)
		c.Assert(bytes.Equal(result, tt.expected), Equals, true,
			Commentf("EncodeCompact(%d): got %x, want %x", tt.value, result, tt.expected))
	}
}

func (s *ScaleSuite) TestEncodeCompactFourBytes(c *C) {
	tests := []struct {
		value    uint64
		expected []byte
	}{
		{16384, []byte{0x02, 0x00, 0x01, 0x00}},
		{1073741823, []byte{0xfe, 0xff, 0xff, 0xff}},
	}

	for _, tt := range tests {
		result := EncodeCompact(tt.value)
		c.Assert(bytes.Equal(result, tt.expected), Equals, true,
			Commentf("EncodeCompact(%d): got %x, want %x", tt.value, result, tt.expected))
	}
}

func (s *ScaleSuite) TestEncodeDecodeCompactRoundTrip(c *C) {
	values := []uint64{
		0, 1, 42, 63, 64, 100, 255, 16383, 16384, 65535,
		1000000, 1073741823, 1073741824, 1000000000,
	}

	for _, v := range values {
		encoded := EncodeCompact(v)
		decoded, n, err := DecodeCompact(encoded)
		c.Assert(err, IsNil, Commentf("DecodeCompact for value %d", v))
		c.Assert(n, Equals, len(encoded), Commentf("DecodeCompact for value %d", v))
		c.Assert(decoded.Equal(cosmos.NewUint(v)), Equals, true,
			Commentf("DecodeCompact for value %d: got %s", v, decoded.String()))
	}
}

func (s *ScaleSuite) TestEncodeU32LE(c *C) {
	result := EncodeU32LE(0x01020304)
	expected := []byte{0x04, 0x03, 0x02, 0x01}
	c.Assert(bytes.Equal(result, expected), Equals, true)
}

func (s *ScaleSuite) TestDecodeCompactBigIntOverflow(c *C) {
	// Construct a compact big-int with n=17 bytes (exceeds u128 capacity of 16)
	// prefix byte: upper 6 bits = (17-4)=13, lower 2 bits = 0b11 → (13<<2)|3 = 55
	data := make([]byte, 18)
	data[0] = 55 // prefix for 17 bytes of data
	for i := 1; i < 17; i++ {
		data[i] = 0xff
	}
	data[17] = 0x01
	_, _, err := DecodeCompact(data)
	c.Assert(err, NotNil, Commentf("expected error for compact value exceeding u128"))
}

func (s *ScaleSuite) TestDecodeCompactBigInt8BytesOK(c *C) {
	// A valid big-int compact with exactly 8 bytes should work
	// prefix: (8-4)<<2 | 3 = (4<<2)|3 = 19
	data := make([]byte, 9)
	data[0] = 19 // prefix for 8 bytes of data
	data[1] = 0x00
	data[2] = 0x00
	data[3] = 0x00
	data[4] = 0x40 // 0x4000000000000000 = 2^62
	data[5] = 0x00
	data[6] = 0x00
	data[7] = 0x00
	data[8] = 0x00

	val, consumed, err := DecodeCompact(data)
	c.Assert(err, IsNil)
	c.Assert(consumed, Equals, 9)
	c.Assert(val.Equal(cosmos.NewUint(0x40000000)), Equals, true)
}
