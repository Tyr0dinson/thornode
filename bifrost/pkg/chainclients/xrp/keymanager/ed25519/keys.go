package ed25519

import (
	"crypto/ed25519"
)

// GetFormattedPublicKey returns the raw Ed25519 public key
// Ed25519 public keys are already in compressed form (32 bytes)
func (k *Keys) GetFormattedPublicKey() []byte {
	return k.publicKey
}

// Sign creates an Ed25519 signature for the provided message
// Note: Ed25519 already includes a pre-hashing step internally,
// but we're maintaining SHA-512 pre-hashing for interface consistency
func (k *Keys) Sign(message []byte) ([]byte, error) {
	// Sign the message hash
	signature := ed25519.Sign(k.privateKey, message)

	return signature, nil
}

// Verify checks if the provided signature is valid for the message
func (k *Keys) Verify(message, signature []byte) (bool, error) {
	// Verify the signature
	return ed25519.Verify(k.publicKey, message, signature), nil
}
