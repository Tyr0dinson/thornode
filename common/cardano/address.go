package cardano

import (
	"crypto/ed25519"
	"fmt"

	"github.com/gcash/bchutil/bech32"
	"golang.org/x/crypto/blake2b"
)

const blake2b224Len = 28

type Address []byte

// String returns the bech32 representation of the address
func (addr Address) String() string {
	header := addr[0]
	network := header & 0x0F
	var hrp string
	if network == 0 {
		hrp = "addr_test"
	} else if network == 1 {
		hrp = "addr"
	} else {
		panic(fmt.Sprintf("invalid network: %d", network))
	}

	addr5Bit, err := bech32.ConvertBits(addr, 8, 5, true)
	if err != nil {
		panic(err)
	}
	res, err := bech32.Encode(hrp, addr5Bit)
	if err != nil {
		panic(err)
	}
	return res
}

func NewThorPubKeyBech32(pubKey []byte) (string, error) {
	bz, err := bech32.ConvertBits(pubKey, 8, 5, true)
	if err != nil {
		return "", err
	}
	return bech32.Encode("tthorpub", bz)
}

func NewAddressFromBech32(addrBech32 string) (Address, error) {
	hrp, data, err := bech32.Decode(addrBech32)
	if err != nil {
		return nil, err
	}
	if hrp != "addr" && hrp != "addr_test" {
		return nil, fmt.Errorf("invalid hrp: %s", hrp)
	}
	addrBz, err := bech32.ConvertBits(data, 5, 8, false)
	if err != nil {
		return nil, err
	}
	return Address(addrBz), nil
}

func blake2b224(data []byte) (result [blake2b224Len]byte, err error) {
	b2b, err := blake2b.New(blake2b224Len, nil)
	if err != nil {
		err = fmt.Errorf("error blake2b224 init: %w", err)
		return
	}
	b2b.Write(data)
	copy(result[:], b2b.Sum(nil)[:blake2b224Len])
	return
}

func newPaymentOnlyAddressFromPubkey(header byte, pub []byte) (Address, error) {
	keyHash, err := blake2b224(pub)
	if err != nil {
		return nil, err
	}
	addr := []byte{header}
	addr = append(addr, keyHash[:]...)
	return Address(addr[:]), err
}

func PaymentOnlyMainnetAddressFromPubkey(pub []byte) (Address, error) {
	// see CIP-19 for header explanation. This header encodes the following:
	// - 4 MSBs: payment only address
	// - 4 LSBs: mainnet
	return newPaymentOnlyAddressFromPubkey(byte(0b01100001), pub)
}

func PaymentOnlyTestnetAddressFromPubkey(pub []byte) (Address, error) {
	// - 4 MSBs: payment only address
	// - 4 LSBs: testnet
	return newPaymentOnlyAddressFromPubkey(byte(0b01100000), pub)
}

func PrivateKeyFromBech32(privBech32 string) (ed25519.PrivateKey, error) {
	hrp, data, err := bech32.Decode(privBech32)
	if err != nil {
		return nil, err
	}
	if hrp != "addr_sk" {
		return nil, fmt.Errorf("invalid hrp: %s", hrp)
	}
	privBz, err := bech32.ConvertBits(data, 5, 8, false)
	if err != nil {
		return nil, err
	}
	return ed25519.NewKeyFromSeed(privBz), nil
}
