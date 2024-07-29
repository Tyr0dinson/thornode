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

// func main() {
// 	address := "BEcrPLugbAY1zEJGUNYgzcsxMi72rPeVwG6qKm96LK5g"
// 	decoded, err := Base58Decode(address)
// 	if err != nil {
// 		fmt.Println("Error decoding address:", err)
// 		return
// 	}

// 	fmt.Println("Decoded address:", decoded)

// 	if len(decoded) == 32 {
// 		fmt.Println("Valid Solana address")
// 	} else {
// 		fmt.Println("Invalid Solana address")
// 	}
// }
