package types

import (
	"crypto/sha256"
	"errors"
	"fmt"

	"filippo.io/edwards25519"
	"github.com/mr-tron/base58"
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
	d, _ := base58.Decode(s)
	return PublicKeyFromBytes(d)
}

func PublicKeyFromString(s string) (PublicKey, error) {
	d, err := base58.Decode(s)
	if err != nil {
		return PublicKey{}, err
	}
	return PublicKeyFromBytes(d), nil
}

func (p PublicKey) ToBase58() string {
	return base58.Encode(p[:])
}

func PublicKeyFromBytes(b []byte) PublicKey {
	var pubkey PublicKey
	if len(b) > PublicKeyLength {
		b = b[:PublicKeyLength]
	}
	copy(pubkey[PublicKeyLength-len(b):], b)
	return pubkey
}

// pdaMarker is the suffix Solana reserves for program-derived addresses. An owner
// ending in this marker is rejected by create_with_seed to keep the seed-derived
// and PDA address spaces disjoint.
var pdaMarker = []byte("ProgramDerivedAddress")

// CreateWithSeed deterministically derives an account address from a base key, an
// ASCII seed and the owning program, matching Solana's Pubkey::create_with_seed
// (sha256(base || seed || owner)). It is the derivation used to give a vault's
// durable-nonce account a fixed address every keysign party can compute from the
// vault pubkey alone — no shared state, no coordination.
//
// The seed must be at most MaxSeedLength bytes and the owner must not end in the
// PDA marker, mirroring the runtime's constraints; both are satisfied by the fixed
// nonce seed + SystemProgram owner this package uses, but are validated so a future
// caller cannot silently derive an address the runtime would reject.
func CreateWithSeed(base PublicKey, seed string, owner PublicKey) (PublicKey, error) {
	if len(seed) > MaxSeedLength {
		return PublicKey{}, fmt.Errorf("seed length %d exceeds max %d", len(seed), MaxSeedLength)
	}
	if len(owner) >= len(pdaMarker) {
		if string(owner[len(owner)-len(pdaMarker):]) == string(pdaMarker) {
			return PublicKey{}, fmt.Errorf("owner ends with PDA marker, illegal for create_with_seed")
		}
	}
	h := sha256.New()
	h.Write(base[:])
	h.Write([]byte(seed))
	h.Write(owner[:])
	return PublicKeyFromBytes(h.Sum(nil)), nil
}

// PublicKeyFromStringStrict decodes a base58 address and rejects anything that is
// not exactly 32 bytes. PublicKeyFromString left-pads short input, which is fine for
// addresses that have already been validated but would silently turn a mistyped
// config value (such as a token mint) into a different, valid-looking key.
func PublicKeyFromStringStrict(s string) (PublicKey, error) {
	d, err := base58.Decode(s)
	if err != nil {
		return PublicKey{}, err
	}
	if len(d) != PublicKeyLength {
		return PublicKey{}, fmt.Errorf("invalid public key length %d, want %d", len(d), PublicKeyLength)
	}
	return PublicKeyFromBytes(d), nil
}

// isOnCurve reports whether b decodes to a point on the ed25519 curve. Program
// derived addresses must be off the curve so no private key can sign for them.
// edwards25519 accepts non-canonical encodings, matching curve25519-dalek's
// decompress that the Solana runtime uses.
func isOnCurve(b []byte) bool {
	_, err := new(edwards25519.Point).SetBytes(b)
	return err == nil
}

// errInvalidSeeds is returned for seeds the runtime would reject or for a hash that
// lands on the curve (the caller then tries the next bump).
var errInvalidSeeds = errors.New("invalid seeds, address must fall off the curve")

// CreateProgramAddress matches Solana's Pubkey::create_program_address:
// sha256(seeds... || programID || "ProgramDerivedAddress"), rejected when the result
// lies on the curve.
func CreateProgramAddress(seeds [][]byte, programID PublicKey) (PublicKey, error) {
	if len(seeds) > MaxSeed {
		return PublicKey{}, fmt.Errorf("too many seeds: %d > %d", len(seeds), MaxSeed)
	}
	h := sha256.New()
	for _, seed := range seeds {
		if len(seed) > MaxSeedLength {
			return PublicKey{}, fmt.Errorf("seed length %d exceeds max %d", len(seed), MaxSeedLength)
		}
		h.Write(seed)
	}
	h.Write(programID[:])
	h.Write(pdaMarker)
	sum := h.Sum(nil)
	if isOnCurve(sum) {
		return PublicKey{}, errInvalidSeeds
	}
	return PublicKeyFromBytes(sum), nil
}

// FindProgramAddress matches Solana's Pubkey::find_program_address: the first bump,
// counting down from 255, whose program address falls off the curve.
func FindProgramAddress(seeds [][]byte, programID PublicKey) (PublicKey, uint8, error) {
	withBump := make([][]byte, len(seeds)+1)
	copy(withBump, seeds)
	for bump := 255; bump >= 0; bump-- {
		withBump[len(seeds)] = []byte{byte(bump)}
		addr, err := CreateProgramAddress(withBump, programID)
		if err == nil {
			return addr, uint8(bump), nil
		}
		if !errors.Is(err, errInvalidSeeds) {
			return PublicKey{}, 0, err
		}
	}
	return PublicKey{}, 0, errors.New("unable to find a viable program address bump seed")
}

// FindAssociatedTokenAddress returns the canonical SPL Token account that wallet
// holds for mint: the associated token program address for seeds
// [wallet, TokenProgramID, mint]. It is a pure function of its inputs, so every
// keysign party and observer derives the same account.
func FindAssociatedTokenAddress(wallet, mint PublicKey) (PublicKey, error) {
	addr, _, err := FindProgramAddress(
		[][]byte{wallet[:], TokenProgramID[:], mint[:]},
		AssociatedTokenProgramID,
	)
	return addr, err
}
