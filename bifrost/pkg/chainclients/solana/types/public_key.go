package types

import (
	"gitlab.com/thorchain/thornode/v3/common"
)

const (
	PublicKeyLength = 32
	MaxSeedLength   = 32
	MaxSeed         = 16
)

type PublicKey [PublicKeyLength]byte

func (p PublicKey) String() string {
	return p.ToBase58()
}

func (p PublicKey) Bytes() []byte {
	return p[:]
}

func (p PublicKey) EqualsString(s string) bool {
	return p.ToBase58() == s
}

func MustPublicKeyFromString(s string) PublicKey {
	d, _ := common.Base58Decode(s)
	return PublicKeyFromBytes(d)
}

func PublicKeyFromString(s string) (PublicKey, error) {
	d, err := common.Base58Decode(s)
	if err != nil {
		return PublicKey{}, err
	}
	return PublicKeyFromBytes(d), nil
}

func (p PublicKey) ToBase58() string {
	return common.Base58Encode(p[:])
}

func PublicKeyFromBytes(b []byte) PublicKey {
	var pubkey PublicKey
	if len(b) > PublicKeyLength {
		b = b[:PublicKeyLength]
	}
	copy(pubkey[PublicKeyLength-len(b):], b)
	return pubkey
}
