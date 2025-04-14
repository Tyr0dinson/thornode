package ed25519

import (
	"crypto/ed25519"
	"crypto/sha512"
	"fmt"
)

// DeriveKeysFromMasterPrivateKey creates a new Keys instance from a provided private key
// For Ed25519, the private key must be exactly 32 bytes (seed) or 64 bytes (full private key)
func DeriveKeysFromMasterPrivateKey(masterPrivateKey []byte) (*Keys, error) {
	var privateKey ed25519.PrivateKey
	var publicKey ed25519.PublicKey

	// Handle both seed format (32 bytes) and full private key format (64 bytes)
	switch len(masterPrivateKey) {
	case ed25519.SeedSize: // 32 bytes
		// Generate a private key from the seed
		privateKey = ed25519.NewKeyFromSeed(masterPrivateKey)
		publicKey = privateKey.Public().(ed25519.PublicKey)
	case ed25519.PrivateKeySize: // 64 bytes
		// Use the provided full private key
		privateKey = masterPrivateKey
		publicKey = privateKey.Public().(ed25519.PublicKey)
	default:
		return nil, fmt.Errorf("invalid Ed25519 private key length: got %d, want 32 or 64", len(masterPrivateKey))
	}

	return &Keys{
		privateKey: privateKey,
		publicKey:  publicKey,
	}, nil
}

// DeriveKeysFromSeedAndDerivationPath creates keys using a seed and derivation path
// This matches Solana's approach for HD wallet key derivation
func DeriveKeysFromSeedAndDerivationPath(seed []byte, derivationPath string) (*Keys, error) {
	// For now, since we don't have a full BIP32/44 implementation,
	// we'll do a simple hash-based derivation that produces deterministic results
	// This is a placeholder for a full implementation that would match Solana's approach

	if derivationPath == "" {
		// If no derivation path, just use the seed directly
		return DeriveKeysFromMasterPrivateKey(seed[:32])
	}

	// Hash the seed with the derivation path to create a new seed
	h := sha512.New()
	h.Write(seed)
	h.Write([]byte(derivationPath))
	derivedSeed := h.Sum(nil)

	// Use first 32 bytes as the Ed25519 seed
	return DeriveKeysFromMasterPrivateKey(derivedSeed[:32])
}
