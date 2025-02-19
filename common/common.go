package common

import (
	"bytes"

	"github.com/blang/semver"
)

var LatestVersion semver.Version = semver.MustParse("999.0.0")

// RemoveAttestations removes matching attestations from a quorum tx, and returns true if there are no more attestations.
func (q *QuorumTx) RemoveAttestations(atts []*Attestation) bool {
	newAtts := make([]*Attestation, 0)
	for _, att1 := range q.Attestations {
		found := false
		for _, att2 := range atts {
			if att1.Equals(att2) {
				found = true
			}
		}
		if !found {
			newAtts = append(newAtts, att1)
		}
	}
	q.Attestations = newAtts
	return len(newAtts) == 0
}

// Equals compares two Attestation and returns true iff they are equal
func (a *Attestation) Equals(att *Attestation) bool {
	if !bytes.Equal(a.Signature, att.Signature) {
		return false
	}
	if !bytes.Equal(a.PubKey, att.PubKey) {
		return false
	}
	return true
}
