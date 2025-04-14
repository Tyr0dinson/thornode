package ed25519

import (
	"crypto/ed25519"
)

// Keys implements the Keys interface for Ed25519 cryptography
type Keys struct {
	privateKey ed25519.PrivateKey
	publicKey  ed25519.PublicKey
}

// EdDSASignature is the raw signature for Ed25519
// Ed25519 signatures are a simple byte array, unlike the R/S format in ECDSA
type EdDSASignature []byte
