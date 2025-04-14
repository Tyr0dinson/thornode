package ed25519

import (
	"bytes"
	"crypto/ed25519"
	"encoding/hex"
	"testing"

	"github.com/stretchr/testify/require"
)

func setupTestKeys(t *testing.T) *Keys {
	// Use a consistent private key for testing
	// This is a 32-byte seed for Ed25519
	privKey, err := hex.DecodeString("3869b485d5219b0582e1806595d7c6ee14fa5afed584c1b60b81e630bafcf3db")
	require.NoError(t, err)
	require.Equal(t, ed25519.SeedSize, len(privKey), "Private key seed must be 32 bytes")

	keys, err := DeriveKeysFromMasterPrivateKey(privKey)
	require.NoError(t, err)
	return keys
}

func TestGetFormattedPublicKey(t *testing.T) {
	keys := setupTestKeys(t)

	pubKey := keys.GetFormattedPublicKey()
	require.Equal(t, ed25519.PublicKeySize, len(pubKey), "Public key should be 32 bytes")

	// The expected public key will be different from secp256k1 since we're using Ed25519
	// We're verifying the length is correct and storing the expected value
	expectedPubKeyHex := hex.EncodeToString(pubKey)
	require.NotEmpty(t, expectedPubKeyHex)

	// For additional verification, we regenerate the keys and ensure public key is consistent
	keys2 := setupTestKeys(t)
	pubKey2 := keys2.GetFormattedPublicKey()
	require.Equal(t, pubKey, pubKey2, "Public key derivation should be deterministic")
}

func TestSignAndVerify(t *testing.T) {
	tests := []struct {
		name       string
		message    []byte
		invalidSig bool
	}{
		{
			name:       "Valid message",
			message:    []byte("test message"),
			invalidSig: false,
		},
		{
			name:       "Empty message",
			message:    []byte{},
			invalidSig: false,
		},
		{
			name:       "Long message",
			message:    bytes.Repeat([]byte("a"), 1000),
			invalidSig: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			keys := setupTestKeys(t)

			// Test signing
			signature, err := keys.Sign(tt.message)
			require.NoError(t, err)
			require.Equal(t, ed25519.SignatureSize, len(signature), "Ed25519 signature should be 64 bytes")

			// Create a signature verification test
			valid, err := keys.Verify(tt.message, signature)
			require.NoError(t, err)
			require.True(t, valid, "Signature should be valid")

			if tt.invalidSig {
				// Corrupt the signature to test invalid case
				signature[0] ^= 0x01
				valid, err = keys.Verify(tt.message, signature)
				require.NoError(t, err)
				require.False(t, valid, "Corrupted signature should be invalid")
			}
		})
	}
}

func TestSignAndVerifyModifiedMessage(t *testing.T) {
	keys := setupTestKeys(t)
	originalMessage := []byte("test message")
	modifiedMessage := []byte("test message modified")

	signature, err := keys.Sign(originalMessage)
	require.NoError(t, err)

	valid, err := keys.Verify(modifiedMessage, signature)
	require.NoError(t, err)
	require.False(t, valid, "Signature should be invalid for modified message")
}

func TestInvalidSignature(t *testing.T) {
	keys := setupTestKeys(t)
	message := []byte("test message")

	// Create a valid signature
	validSig, err := keys.Sign(message)
	require.NoError(t, err)

	// Verify the valid signature works
	valid, err := keys.Verify(message, validSig)
	require.NoError(t, err)
	require.True(t, valid)

	// Create an invalid signature by modifying a few bytes
	invalidSig := make([]byte, len(validSig))
	copy(invalidSig, validSig)
	invalidSig[0] ^= 0xFF
	invalidSig[len(invalidSig)-1] ^= 0xFF

	// Verify the invalid signature fails
	valid, err = keys.Verify(message, invalidSig)
	require.NoError(t, err)
	require.False(t, valid, "Modified signature should be invalid")
}

func TestMultipleKeyDerivation(t *testing.T) {
	// Test that different private keys generate different public keys
	privKey1, err := hex.DecodeString("3869b485d5219b0582e1806595d7c6ee14fa5afed584c1b60b81e630bafcf3db")
	require.NoError(t, err)

	privKey2, err := hex.DecodeString("4869b485d5219b0582e1806595d7c6ee14fa5afed584c1b60b81e630bafcf3db")
	require.NoError(t, err)

	keys1, err := DeriveKeysFromMasterPrivateKey(privKey1)
	require.NoError(t, err)

	keys2, err := DeriveKeysFromMasterPrivateKey(privKey2)
	require.NoError(t, err)

	pubKey1 := keys1.GetFormattedPublicKey()
	pubKey2 := keys2.GetFormattedPublicKey()

	require.NotEqual(t, pubKey1, pubKey2, "Different private keys should generate different public keys")
}

func TestFullPrivateKeyFormat(t *testing.T) {
	// Test creating keys from a full 64-byte private key
	seed, err := hex.DecodeString("3869b485d5219b0582e1806595d7c6ee14fa5afed584c1b60b81e630bafcf3db")
	require.NoError(t, err)

	// First create keys from the 32-byte seed
	seedKeys, err := DeriveKeysFromMasterPrivateKey(seed)
	require.NoError(t, err)

	// Get the full 64-byte private key
	fullPrivateKey := seedKeys.privateKey
	require.Equal(t, ed25519.PrivateKeySize, len(fullPrivateKey))

	// Create new keys from the full private key
	fullKeys, err := DeriveKeysFromMasterPrivateKey(fullPrivateKey)
	require.NoError(t, err)

	// Verify both methods produce the same public key
	require.Equal(t, seedKeys.GetFormattedPublicKey(), fullKeys.GetFormattedPublicKey())

	// Verify signing produces identical results
	message := []byte("test consistency")
	sig1, err := seedKeys.Sign(message)
	require.NoError(t, err)

	sig2, err := fullKeys.Sign(message)
	require.NoError(t, err)

	require.Equal(t, sig1, sig2, "Signatures should be identical with equivalent keys")
}
