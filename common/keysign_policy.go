package common

const keysignPolicyHeightInterval int64 = 20

// RoundKeysignPolicyHeight returns the stable THORChain height used for
// keysign-party policy checks. A small block bucket avoids different Bifrosts
// choosing different signer sets when they are a few blocks apart locally.
func RoundKeysignPolicyHeight(height int64) int64 {
	if height <= 0 {
		return 0
	}
	return height / keysignPolicyHeightInterval * keysignPolicyHeightInterval
}
