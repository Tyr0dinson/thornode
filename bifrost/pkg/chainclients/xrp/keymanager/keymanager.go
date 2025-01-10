package keymanager

import (
	"encoding/hex"
	"fmt"

	cryptotypes "github.com/cosmos/cosmos-sdk/crypto/types"
	
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/xrp/keymanager/secp256k1"
)

type KeyManager struct {
	AccountID     string // base58 encoded address with 'r' prefix and checksum
	//MasterSeed    string
	//MasterSeedHex string
	PublicKey     string // base58 encoded w/ prefix and checksum
	PublicKeyHex  string // raw hex encoded pub key
	KeyType       CryptoAlgorithm
	Keys          Keys
}

func NewKeyManager(key cryptotypes.PrivKey, keyType CryptoAlgorithm) (*KeyManager, error) {
	var keys Keys
	var err error
	switch keyType {
	// case ED25519:
	// 	keys, err = ed25519.DeriveKeysFromSeed(key.Bytes())
	// 	if err != nil {
	// 		return nil, fmt.Errorf("fail generate xrp wallet from ed25519 seed: %v", err)
	// 	}
	case SECP256K1:
		keys, err = secp256k1.DeriveKeysFromSeed(key.Bytes())
		if err != nil {
			return nil, fmt.Errorf("fail generate xrp wallet from secp256k1 seed: %v", err)
		}

	default:
		return nil, fmt.Errorf("unsupported key type")
	}

	return &KeyManager{
		AccountID:     MasterPubKeyToAccountID(keys.GetFormattedPublicKey()),
		KeyType:       keyType,
		//MasterSeed:    masterSeed,
		//MasterSeedHex: hex.EncodeToString(masterSeedBytes),
		PublicKey:     EncodePublicKey(keys.GetFormattedPublicKey()),
		PublicKeyHex:  hex.EncodeToString(keys.GetFormattedPublicKey()),
		Keys:          keys,
	}, nil
}

func (m *KeyManager) Pubkey() string {
	return m.PublicKey
}

// func (m *KeyManager) ExportAsMnemonic() (string, error) {
// 	if m.mnemonic == "" {
// 		return "", fmt.Errorf("this key manager is not recover from mnemonic or auto generated ")
// 	}
// 	return m.mnemonic, nil
// }

// func (m *KeyManager) ExportAsPrivateKey() (string, error) {
// 	return hex.EncodeToString(m.privKey.Bytes()), nil
// }

func (m *KeyManager) Sign(msg []byte) ([]byte, error) {
	return m.Keys.Sign(msg)
}

// func (m *KeyManager) GetPrivKey() ctypes.PrivKey {
// 	return m.privKey
// }

// Get formatted address, passing in a prefix.
func (w *KeyManager) GetAddr() string {
	return w.AccountID
}

type Keys interface {
	GetFormattedPublicKey() []byte
	Sign(message []byte) ([]byte, error)
	Verify(message, signature []byte) (bool, error)
}

// Algorithm represents supported cryptographic algorithms.
type CryptoAlgorithm int

const (
	// SECP256K1 represents the secp256k1 elliptic curve algorithm.
	SECP256K1 CryptoAlgorithm = iota
	// ED25519 represents the Ed25519 elliptic curve algorithm.
	ED25519
)

// String returns the string representation of the CryptoAlgorithm.
func (a CryptoAlgorithm) String() string {
	switch a {
	case SECP256K1:
		return "secp256k1"
	case ED25519:
		return "ed25519"
	default:
		return "unknown"
	}
}
