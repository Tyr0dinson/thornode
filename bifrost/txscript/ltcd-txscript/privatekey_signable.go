package txscript

import (
	"github.com/ltcsuite/ltcd/btcec/v2"
	"github.com/ltcsuite/ltcd/btcec/v2/ecdsa"
)

type PrivateKeySignable struct {
	privateKey *btcec.PrivateKey
}

// NewPrivateKeySignable create a new instance of PrivateKeySignable
func NewPrivateKeySignable(priKey *btcec.PrivateKey) *PrivateKeySignable {
	return &PrivateKeySignable{privateKey: priKey}
}

// Sign the given hash bytes
func (p *PrivateKeySignable) Sign(hash []byte) (*ecdsa.Signature, error) {
	return ecdsa.Sign(p.privateKey, hash), nil
}

// GetPubKey return the PubKey
func (p *PrivateKeySignable) GetPubKey() *btcec.PublicKey {
	return p.privateKey.PubKey()
}
