package types

import (
	"testing"
)

func TestEncodeCompactSingleByte(t *testing.T) {
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
		if len(result) != len(tt.expected) {
			t.Errorf("EncodeCompact(%d): expected %d bytes, got %d", tt.value, len(tt.expected), len(result))
			continue
		}
		for i := range result {
			if result[i] != tt.expected[i] {
				t.Errorf("EncodeCompact(%d): byte %d: expected 0x%02x, got 0x%02x", tt.value, i, tt.expected[i], result[i])
			}
		}
	}
}

func TestEncodeCompactTwoBytes(t *testing.T) {
	tests := []struct {
		value    uint64
		expected []byte
	}{
		{64, []byte{0x01, 0x01}},
		{16383, []byte{0xfd, 0xff}},
	}

	for _, tt := range tests {
		result := EncodeCompact(tt.value)
		if len(result) != len(tt.expected) {
			t.Errorf("EncodeCompact(%d): expected %d bytes, got %d", tt.value, len(tt.expected), len(result))
			continue
		}
		for i := range result {
			if result[i] != tt.expected[i] {
				t.Errorf("EncodeCompact(%d): byte %d: expected 0x%02x, got 0x%02x", tt.value, i, tt.expected[i], result[i])
			}
		}
	}
}

func TestEncodeCompactFourBytes(t *testing.T) {
	tests := []struct {
		value    uint64
		expected []byte
	}{
		{16384, []byte{0x02, 0x00, 0x01, 0x00}},
		{1073741823, []byte{0xfe, 0xff, 0xff, 0xff}},
	}

	for _, tt := range tests {
		result := EncodeCompact(tt.value)
		if len(result) != len(tt.expected) {
			t.Errorf("EncodeCompact(%d): expected %d bytes, got %d", tt.value, len(tt.expected), len(result))
			continue
		}
		for i := range result {
			if result[i] != tt.expected[i] {
				t.Errorf("EncodeCompact(%d): byte %d: expected 0x%02x, got 0x%02x", tt.value, i, tt.expected[i], result[i])
			}
		}
	}
}

func TestEncodeDecodeCompactRoundTrip(t *testing.T) {
	values := []uint64{
		0, 1, 42, 63, 64, 100, 255, 16383, 16384, 65535,
		1000000, 1073741823, 1073741824, 1000000000,
	}

	for _, v := range values {
		encoded := EncodeCompact(v)
		decoded, n, err := DecodeCompact(encoded)
		if err != nil {
			t.Errorf("DecodeCompact for value %d: %v", v, err)
			continue
		}
		if n != len(encoded) {
			t.Errorf("DecodeCompact for value %d: consumed %d bytes, expected %d", v, n, len(encoded))
		}
		if decoded != v {
			t.Errorf("DecodeCompact for value %d: got %d", v, decoded)
		}
	}
}

func TestEncodeU32LE(t *testing.T) {
	result := EncodeU32LE(0x01020304)
	expected := []byte{0x04, 0x03, 0x02, 0x01}
	for i := range result {
		if result[i] != expected[i] {
			t.Errorf("byte %d: expected 0x%02x, got 0x%02x", i, expected[i], result[i])
		}
	}
}

func TestDecodeCompactBigIntOverflow(t *testing.T) {
	// Construct a compact big-int with n=9 bytes (exceeds uint64 capacity of 8)
	// prefix byte: upper 6 bits = (9-4)=5, lower 2 bits = 0b11 → (5<<2)|3 = 23
	data := make([]byte, 10)
	data[0] = 23 // prefix for 9 bytes of data
	data[1] = 0xff
	data[2] = 0xff
	data[3] = 0xff
	data[4] = 0xff
	data[5] = 0xff
	data[6] = 0xff
	data[7] = 0xff
	data[8] = 0xff
	data[9] = 0x01

	_, _, err := DecodeCompact(data)
	if err == nil {
		t.Error("expected error for compact value exceeding uint64")
	}
}

func TestDecodeCompactBigInt8BytesOK(t *testing.T) {
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
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if consumed != 9 {
		t.Errorf("expected 9 bytes consumed, got %d", consumed)
	}
	if val != 0x40000000 {
		t.Errorf("expected 0x40000000, got 0x%x", val)
	}
}
