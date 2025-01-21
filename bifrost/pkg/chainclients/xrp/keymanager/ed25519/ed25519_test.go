package ed25519

import (
	"bytes"
	"encoding/hex"
	"testing"

	"github.com/stretchr/testify/require"
)

func setupTestKeys(t *testing.T) *Keys {
	seed, err := hex.DecodeString("f7f9ff93d716eaced222a3c52a3b2a36")
	require.NoError(t, err)
	keys, err := DeriveKeysFromSeed(seed)
	require.NoError(t, err)
	return keys
}

func TestGetFormattedPublicKey(t *testing.T) {
	keys := setupTestKeys(t)

	pubKey := keys.GetFormattedPublicKey()
	if len(pubKey) != 33 { // Compressed public key should be 33 bytes
		t.Errorf("Expected compressed public key length of 33 bytes, got %d", len(pubKey))
	}
	require.Equal(t, "edfb7c70e528fe161addfda8cb224bc19b9e6455916970f7992a356c3e77ac7ef8", hex.EncodeToString(pubKey))
}

func TestSignAndVerify(t *testing.T) {
	tests := []struct {
		name           string
		message        []byte
		expectedSigHex string
		invalidSig     bool
	}{
		{
			name:           "Valid message",
			message:        []byte("test message"),
			expectedSigHex: "a63759e72b3e85fa4eaf6e9cf6ef815e7d7ce660804d32c4c044906245ce540231f6bf332922da301f1188239803277bed6a8c6312a1b92511dab1662fb37703",
			invalidSig:     false,
		},
		{
			name:           "Invalid signature",
			message:        []byte("test message"),
			expectedSigHex: "a63759e73b3e85fa4eaf6e9cf6ef815e7d7ce660804d32c4c044906245ce540231f6bf332922da301f1188239803277bed6a8c6312a1b92511dab1662fb37703",
			invalidSig:     true,
		},
		{
			name:           "Empty message",
			message:        []byte{},
			expectedSigHex: "0a4ecf515598aafb84225300b33458031b40392046b9aa4557a5a0429787c90e7c77ec921fcf3386de04d9d728178ada03f66203624bcbc6e9e315e857289709",
			invalidSig:     false,
		},
		{
			name:           "Long message",
			message:        bytes.Repeat([]byte("a"), 1000),
			expectedSigHex: "4ec8c059fdbe7a5c7ef6898bd72112af0679de870b95861e6f262f0f281b6f8ad5b4011a3a9fdfca5e56d1acc3fc5f555790dc9ec5fae91ed8f22d0cc56ef108",
			invalidSig:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			keys := setupTestKeys(t)

			// Test signing
			signature, err := keys.Sign(tt.message)
			require.NoError(t, err)
			if !tt.invalidSig {
				require.Equal(t, tt.expectedSigHex, hex.EncodeToString(signature))
			} else {
				signature, _ = hex.DecodeString(tt.expectedSigHex)
			}

			// Test verification
			valid, err := keys.Verify(tt.message, signature)
			if tt.invalidSig {
				require.NoError(t, err)
				require.False(t, valid)

			} else {
				require.NoError(t, err)
				require.True(t, valid)
			}
		})
	}
}

func TestSignAndVerifyModifiedMessage(t *testing.T) {
	keys := setupTestKeys(t)
	originalMessage := []byte("test message")
	modifiedMessage := []byte("test message modified")

	signature, err := keys.Sign(originalMessage)
	if err != nil {
		t.Fatalf("Sign() error = %v", err)
	}

	valid, err := keys.Verify(modifiedMessage, signature)
	if err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	if valid {
		t.Error("Verify() succeeded with modified message")
	}
}
