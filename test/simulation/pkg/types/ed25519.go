package types

import (
	"github.com/blocto/solana-go-sdk/pkg/hdwallet"
	"github.com/blocto/solana-go-sdk/types"
	"github.com/cosmos/cosmos-sdk/crypto/hd"
	"github.com/cosmos/cosmos-sdk/crypto/keyring"
	edKeys "github.com/cosmos/cosmos-sdk/crypto/keys/ed25519"
	ctypes "github.com/cosmos/cosmos-sdk/crypto/types"
	"github.com/cosmos/go-bip39"
)

type Ed25519Algo struct {
}

var Ed25519 = Ed25519Algo{}

// Derive derives and returns the ed25519 private key for the given seed and HD path.
func (s Ed25519Algo) Derive() hd.DeriveFn {
	return func(mnemonic string, bip39Passphrase, hdPath string) ([]byte, error) {
		seed := bip39.NewSeed(mnemonic, bip39Passphrase)
		derivedKey, err := hdwallet.Derived(hdPath, seed)
		if err != nil {
			return nil, err
		}
		account, _ := types.AccountFromSeed(derivedKey.PrivateKey)

		var pkArr = make([]byte, 64)
		copy(pkArr, account.PrivateKey[:])
		return pkArr, nil
	}
}

// Generate generates a ed25519 private key from the given bytes.
func (s Ed25519Algo) Generate() hd.GenerateFn {
	return func(bz []byte) ctypes.PrivKey {
		var bzArr = make([]byte, edKeys.PrivKeySize)
		copy(bzArr, bz)
		return &edKeys.PrivKey{Key: bzArr}
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
