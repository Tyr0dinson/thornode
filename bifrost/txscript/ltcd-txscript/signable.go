package txscript

import (
	"github.com/ltcsuite/ltcd/btcec/v2"
	"github.com/ltcsuite/ltcd/btcec/v2/ecdsa"
)

// Signable is a interface which represent something that knows how to sign some bytes
type Signable interface {
	GetPubKey() *btcec.PublicKey
	Sign(hash []byte) (*ecdsa.Signature, error)
}
