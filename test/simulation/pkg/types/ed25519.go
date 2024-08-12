package types

import (
	"crypto/hmac"
	"crypto/sha512"
	"encoding/binary"
	"errors"
	"fmt"
	"math/big"
	"strconv"
	"strings"

	"github.com/cosmos/cosmos-sdk/crypto/hd"
	"github.com/cosmos/cosmos-sdk/crypto/keyring"
	eddsaKey "github.com/cosmos/cosmos-sdk/crypto/keys/ed25519"
	"github.com/cosmos/cosmos-sdk/crypto/types"
	"github.com/cosmos/go-bip39"
	"github.com/decred/dcrd/dcrec/edwards"
)

type Ed25519Algo struct {
}

var Ed25519 = Ed25519Algo{}

// Derive derives and returns the ed25519 private key for the given seed and HD path.
func (s Ed25519Algo) Derive() hd.DeriveFn {
	return func(mnemonic string, bip39Passphrase, hdPath string) ([]byte, error) {
		pk, err := mnemonicToEdd25519Key(mnemonic, bip39Passphrase, hdPath)
		if err != nil {
			return nil, err
		}

		return pk, nil
	}
}

// Generate generates a ed25519 private key from the given bytes.
func (s Ed25519Algo) Generate() hd.GenerateFn {
	return func(bz []byte) types.PrivKey {
		ed25519PrivKey := eddsaKey.GenPrivKeyFromSecret(bz)
		return ed25519PrivKey
	}
}

func (s Ed25519Algo) Name() hd.PubKeyType {
	return hd.Ed25519Type
}

// Define your Option function
func WithOptions(opts keyring.Options) keyring.Option {
	return func(o *keyring.Options) {
		*o = opts
	}
}

// Key generation
func mnemonicToEdd25519Key(mnemonic, bip39Passphrase, hdPath string) ([]byte, error) {
	words := strings.Split(mnemonic, " ")
	if len(words) != 12 && len(words) != 24 {
		return nil, errors.New("mnemonic length should either be 12 or 24")
	}
	seed, err := bip39.NewSeedWithErrorChecking(mnemonic, bip39Passphrase)
	if err != nil {
		return nil, err
	}
	masterPriv, ch := i64([]byte(""), seed)
	derivedPriv, err := derivePrivateKeyForPath(masterPriv, ch, hdPath)
	if err != nil {
		return nil, err
	}
	return derivedPriv[:], nil
}

func derivePrivateKey(privKeyBytes, chainCode [32]byte, index uint32, harden bool) ([32]byte, [32]byte) {
	var data []byte
	if harden {
		index |= 0x80000000
		data = append([]byte{byte(0)}, privKeyBytes[:]...)
	} else {
		// this can't return an error:
		_, ecPub, err := edwards.PrivKeyFromScalar(edwards.Edwards(), privKeyBytes[:])
		if err != nil {
			panic("it should not fail")
		}
		pubKeyBytes := ecPub.SerializeCompressed()
		data = pubKeyBytes
	}
	data = append(data, uint32ToBytes(index)...)
	data2, chainCode2 := i64(chainCode[:], data)
	x := addScalars(privKeyBytes[:], data2[:])
	return x, chainCode2
}

func derivePrivateKeyForPath(privKeyBytes, chainCode [32]byte, path string) ([32]byte, error) {
	data := privKeyBytes
	// fmt.Println("data: ", data)
	// fmt.Println("path: ", path)
	parts := strings.Split(path, "/")
	for _, part := range parts {
		// do we have an apostrophe?
		harden := part[len(part)-1:] == "'"
		// harden == private derivation, else public derivation:
		if harden {
			part = part[:len(part)-1]
		}
		idx, err := strconv.ParseInt(part, 10, 64)
		if err != nil {
			return [32]byte{}, fmt.Errorf("invalid BIP 32 path: %s", err)
		}
		if idx < 0 {
			return [32]byte{}, errors.New("invalid BIP 32 path: index negative or too large")
		}
		data, chainCode = derivePrivateKey(data, chainCode, uint32(idx), harden)
	}
	var derivedKey [32]byte
	n := copy(derivedKey[:], data[:])
	if n != 32 || len(data) != 32 {
		return [32]byte{}, fmt.Errorf("expected a (secp256k1) key of length 32, got length: %v", len(data))
	}

	return derivedKey, nil
}

func i64(key, data []byte) (il, ir [32]byte) {
	mac := hmac.New(sha512.New, key)
	// sha512 does not err
	_, _ = mac.Write(data)
	i := mac.Sum(nil)
	copy(il[:], i[:32])
	copy(ir[:], i[32:])
	return
}

func uint32ToBytes(i uint32) []byte {
	b := [4]byte{}
	binary.BigEndian.PutUint32(b[:], i)
	return b[:]
}

func addScalars(a, b []byte) [32]byte {
	aInt := new(big.Int).SetBytes(a)
	bInt := new(big.Int).SetBytes(b)
	sInt := new(big.Int).Add(aInt, bInt)
	x := sInt.Mod(sInt, edwards.Edwards().N).Bytes()
	x2 := [32]byte{}
	copy(x2[32-len(x):], x)
	return x2
}
