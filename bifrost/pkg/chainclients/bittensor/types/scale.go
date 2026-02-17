package types

import (
	"encoding/binary"
	"fmt"
)

// EncodeCompact encodes a uint64 value as a SCALE compact integer.
func EncodeCompact(v uint64) []byte {
	switch {
	case v <= 63: // single-byte mode (6 bits)
		return []byte{byte(v << 2)}
	case v <= 16383: // two-byte mode (14 bits)
		val := uint16(v<<2) | 0x01
		buf := make([]byte, 2)
		binary.LittleEndian.PutUint16(buf, val)
		return buf
	case v <= 1073741823: // four-byte mode (30 bits)
		val := uint32(v<<2) | 0x02
		buf := make([]byte, 4)
		binary.LittleEndian.PutUint32(buf, val)
		return buf
	default: // big-integer mode
		return encodeCompactBigInt(v)
	}
}

// encodeCompactBigInt encodes a uint64 as a big-integer compact SCALE value.
func encodeCompactBigInt(v uint64) []byte {
	buf := make([]byte, 8)
	binary.LittleEndian.PutUint64(buf, v)

	// trim trailing zeroes
	n := 8
	for n > 0 && buf[n-1] == 0 {
		n--
	}
	buf = buf[:n]

	// prefix byte: upper 6 bits = (byte_length - 4), lower 2 bits = 0b11
	prefix := byte((n-4)<<2) | 0x03
	return append([]byte{prefix}, buf...)
}

// DecodeCompact decodes a SCALE compact integer from the given bytes, returning
// the value and the number of bytes consumed.
func DecodeCompact(data []byte) (uint64, int, error) {
	if len(data) == 0 {
		return 0, 0, fmt.Errorf("empty data for compact decode")
	}

	mode := data[0] & 0x03
	switch mode {
	case 0: // single-byte mode
		return uint64(data[0] >> 2), 1, nil
	case 1: // two-byte mode
		if len(data) < 2 {
			return 0, 0, fmt.Errorf("insufficient data for 2-byte compact")
		}
		val := binary.LittleEndian.Uint16(data[:2])
		return uint64(val >> 2), 2, nil
	case 2: // four-byte mode
		if len(data) < 4 {
			return 0, 0, fmt.Errorf("insufficient data for 4-byte compact")
		}
		val := binary.LittleEndian.Uint32(data[:4])
		return uint64(val >> 2), 4, nil
	case 3: // big-integer mode
		n := int(data[0]>>2) + 4
		if len(data) < 1+n {
			return 0, 0, fmt.Errorf("insufficient data for big compact, need %d bytes", 1+n)
		}
		if n > 8 {
			return 0, 0, fmt.Errorf("compact big-integer value exceeds uint64 capacity (%d bytes)", n)
		}
		buf := make([]byte, 8)
		copy(buf, data[1:1+n])
		return binary.LittleEndian.Uint64(buf), 1 + n, nil
	}

	return 0, 0, fmt.Errorf("unknown compact mode")
}

// EncodeU32LE encodes a uint32 in little-endian format.
func EncodeU32LE(v uint32) []byte {
	buf := make([]byte, 4)
	binary.LittleEndian.PutUint32(buf, v)
	return buf
}
