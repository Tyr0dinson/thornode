package observer

import "bytes"

// sameAttestationPayload is called under the retained state's lock before
// counting a state-sync item whose bucket key omits signed fields. Exact valid
// duplicates can still use AddAttestation's verification-free replay path.
func sameAttestationPayload(retained, incoming AttestableItem) bool {
	stored, err := retained.GetSignablePayload()
	if err != nil {
		return false
	}
	presented, err := incoming.GetSignablePayload()
	return err == nil && bytes.Equal(stored, presented)
}
