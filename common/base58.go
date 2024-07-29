package common

import (
	"fmt"
	"math/big"
)

const base58Alphabet = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"

// Base58Decode decodes a base58 encoded string to a byte array
func Base58Decode(input string) ([]byte, error) {
	alphabetMap := make(map[rune]int)
	for i, c := range base58Alphabet {
		alphabetMap[c] = i
	}

	result := big.NewInt(0)
	base := big.NewInt(58)

	for _, c := range input {
		charIndex, ok := alphabetMap[c]
		if !ok {
			return nil, fmt.Errorf("invalid character in base58 string: %c", c)
		}
		result.Mul(result, base)
		result.Add(result, big.NewInt(int64(charIndex)))
	}

	decoded := result.Bytes()

	// Add leading zeros
	leadingZeros := 0
	for leadingZeros < len(input) && input[leadingZeros] == base58Alphabet[0] {
		leadingZeros++
	}

	return append(make([]byte, leadingZeros), decoded...), nil
}

// Base58Encode encodes a byte array to a base58 encoded string
func Base58Encode(input []byte) string {
	bi := new(big.Int).SetBytes(input)
	base := big.NewInt(58)
	zero := big.NewInt(0)
	mod := &big.Int{}

	var result []byte

	for bi.Cmp(zero) != 0 {
		bi.DivMod(bi, base, mod)
		result = append(result, base58Alphabet[mod.Int64()])
	}

	// Leading zero bytes
	for _, b := range input {
		if b == 0x00 {
			result = append(result, base58Alphabet[0])
		} else {
			break
		}
	}

	// Reverse the result
	for i, j := 0, len(result)-1; i < j; i, j = i+1, j-1 {
		result[i], result[j] = result[j], result[i]
	}

	return string(result)
}
