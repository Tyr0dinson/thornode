package thorclient

import (
	"fmt"

	"github.com/cosmos/cosmos-sdk/crypto"
	ckeys "github.com/cosmos/cosmos-sdk/crypto/keyring"
	cryptotypes "github.com/cosmos/cosmos-sdk/crypto/types"
)

// Keys manages all the keys used by thorchain
type Keys struct {
	signerName string
	password   string // TODO this is a bad way , need to fix it
	kb         ckeys.Keyring
}

// NewKeysWithKeybase create a new instance of Keys
func NewKeysWithKeybase(kb ckeys.Keyring, name, password string) *Keys {
	return &Keys{
		signerName: name,
		password:   password,
		kb:         kb,
	}
}

// GetSignerInfo return signer info
func (k *Keys) GetSignerInfo() *ckeys.Record {
	record, err := k.kb.Key(k.signerName)
	if err != nil {
		panic(err)
	}
	return record
}

// GetPrivateKey return the private key
func (k *Keys) GetPrivateKey() (cryptotypes.PrivKey, error) {
	// return k.kb.ExportPrivateKeyObject(k.signerName)
	privKeyArmor, err := k.kb.ExportPrivKeyArmor(k.signerName, k.password)
	if err != nil {
		return nil, err
	}
	priKey, _, err := crypto.UnarmorDecryptPrivKey(privKeyArmor, k.password)
	if err != nil {
		return nil, fmt.Errorf("fail to unarmor private key: %w", err)
	}
	return priKey, nil
}

// GetKeybase return the keybase
func (k *Keys) GetKeybase() ckeys.Keyring {
	return k.kb
}
