package types

import (
	"bytes"

	coskey "github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	"github.com/cosmos/cosmos-sdk/types/bech32"
	apicommon "gitlab.com/thorchain/thornode/v3/api/common"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

const enshrinedBifrostSigner = "thor1zxhfu0qmmq6gmgq4sgz0xgq69h0nhqx5yrseu5"

var enshrinedBifrostSignerAcc = mustDecodeEnshrinedBifrostSigner()

func mustDecodeEnshrinedBifrostSigner() cosmos.AccAddress {
	_, address, err := bech32.DecodeAndConvert(enshrinedBifrostSigner)
	if err != nil {
		panic(err)
	}
	return address
}

// EnshrinedBifrostSigner returns a fresh copy of the fixed envelope principal
// used for proposer-injected quorum messages. It is not a user or attester
// identity and must never confer account authority.
func EnshrinedBifrostSigner() cosmos.AccAddress {
	return append(cosmos.AccAddress(nil), enshrinedBifrostSignerAcc...)
}

// IsEnshrinedBifrostSigner reports whether signer is the fixed envelope
// principal used by the enshrined Bifrost proposal path.
func IsEnshrinedBifrostSigner(signer cosmos.AccAddress) bool {
	return bytes.Equal(signer, enshrinedBifrostSignerAcc)
}

// signersFromAttestations extracts signers from any type of attestation struct
// that has a PubKey field, and returns either []cosmos.AccAddress or [][]byte
func signersFromAttestations[T ~[]byte, A any](attestations []A, pubKeyGetter func(A) []byte) []T {
	signers := make([]T, len(attestations))
	for i, attestation := range attestations {
		pubKey := pubKeyGetter(attestation)
		pub := &coskey.PubKey{Key: pubKey}
		// Convert the address to the expected type
		signers[i] = T(pub.Address())
	}
	return signers
}

func commonPub(a *common.Attestation) []byte       { return a.PubKey }
func apiCommonPub(a *apicommon.Attestation) []byte { return a.PubKey }

func quorumSignersCommon(attestations []*common.Attestation) []cosmos.AccAddress {
	return signersFromAttestations[cosmos.AccAddress](attestations, commonPub)
}

func quorumSignersApiCommon(attestations []*apicommon.Attestation) [][]byte {
	return signersFromAttestations[[]byte](attestations, apiCommonPub)
}
