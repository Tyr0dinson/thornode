package types

import (
	"encoding/binary"
	"fmt"
	"math"
	"math/big"

	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

// EncodeCompact encodes a uint64 value as a SCALE compact integer.
func EncodeCompact(v uint64) []byte {
	return EncodeCompactUint(cosmos.NewUint(v))
}

// EncodeCompactUint encodes a cosmos.Uint value as a SCALE compact integer.
func EncodeCompactUint(v cosmos.Uint) []byte {
	bigInt := v.BigInt()
	if !bigInt.IsUint64() {
		return encodeCompactBigInt(bigInt)
	}

	n := v.Uint64()
	switch {
	case n < 64: // single-byte mode (6 bits)
		return []byte{byte(n << 2)}
	case n < 16384: // two-byte mode (14 bits)
		val := uint16(n<<2) | 0x01
		buf := make([]byte, 2)
		binary.LittleEndian.PutUint16(buf, val)
		return buf
	case n < 1073741824: // four-byte mode (30 bits)
		val := uint32(n<<2) | 0x02
		buf := make([]byte, 4)
		binary.LittleEndian.PutUint32(buf, val)
		return buf
	}

	// big integer mode
	return encodeCompactBigInt(bigInt)
}

// encodeCompactBigInt encodes a big.Int as a big-integer compact SCALE value.
func encodeCompactBigInt(v *big.Int) []byte {
	// Convert big.Int to little-endian bytes
	// big.Int.Bytes() returns big-endian, so we reverse
	bigEndian := v.Bytes()
	length := len(bigEndian)

	littleEndian := make([]byte, length)
	for i := range bigEndian {
		littleEndian[length-1-i] = bigEndian[i]
	}

	// Trim trailing zero bytes
	index := length
	for index > 0 && littleEndian[index-1] == 0 {
		index--
	}
	littleEndian = littleEndian[:index]

	// prefix byte: upper 6 bits = (byte_length - 4), lower 2 bits = 0b11
	prefix := byte((index-4)<<2) | 0x03
	return append([]byte{prefix}, littleEndian...)
}

// DecodeCompact decodes a SCALE compact integer from the given bytes, returning
// the value and the number of bytes consumed.
func DecodeCompact(data []byte) (cosmos.Uint, int, error) {
	if len(data) == 0 {
		return cosmos.ZeroUint(), 0, fmt.Errorf("empty data for compact decode")
	}

	mode := data[0] & 0x03
	switch mode {
	case 0: // single-byte mode
		return cosmos.NewUint(uint64(data[0] >> 2)), 1, nil
	case 1: // two-byte mode
		if len(data) < 2 {
			return cosmos.ZeroUint(), 0, fmt.Errorf("insufficient data for 2-byte compact")
		}
		val := binary.LittleEndian.Uint16(data[:2])
		return cosmos.NewUint(uint64(val >> 2)), 2, nil
	case 2: // four-byte mode
		if len(data) < 4 {
			return cosmos.ZeroUint(), 0, fmt.Errorf("insufficient data for 4-byte compact")
		}
		val := binary.LittleEndian.Uint32(data[:4])
		return cosmos.NewUint(uint64(val >> 2)), 4, nil
	case 3: // big-integer mode
		n := int(data[0]>>2) + 4
		if n > 16 {
			return cosmos.ZeroUint(), 0, fmt.Errorf("compact value too large: %d bytes (max 16)", n)
		}
		if len(data) < 1+n {
			return cosmos.ZeroUint(), 0, fmt.Errorf("insufficient data for big compact, need %d bytes", 1+n)
		}
		littleEndian := data[1 : 1+n]
		bigEndian := make([]byte, n)
		for i := range littleEndian {
			bigEndian[n-1-i] = littleEndian[i]
		}
		bigInt := new(big.Int).SetBytes(bigEndian)
		return cosmos.NewUintFromBigInt(bigInt), 1 + n, nil
	}

	return cosmos.ZeroUint(), 0, fmt.Errorf("unknown compact mode")
}

func DecodeCompactToInt(data []byte) (int, int, error) {
	value, consumed, err := DecodeCompact(data)
	if err != nil {
		return 0, 0, err
	}

	if value.GT(cosmos.NewUint(uint64(math.MaxInt64))) {
		return 0, 0, fmt.Errorf("decoded value too large")
	}

	return int(value.Uint64()), consumed, nil
}

// EncodeU32LE encodes an uint32 in little-endian format.
func EncodeU32LE(v uint32) []byte {
	buf := make([]byte, 4)
	binary.LittleEndian.PutUint32(buf, v)
	return buf
}
