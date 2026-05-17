package common

import (
	"errors"
	"fmt"
	"math/big"
	"sync"
	"sync/atomic"

	"github.com/binance-chain/tss-lib/crypto/paillier"
	ecdsaKeygen "github.com/binance-chain/tss-lib/ecdsa/keygen"
)

const (
	paillierExpectedModulusBits = 2048
	paillierMinModulusBits      = paillierExpectedModulusBits - 8
	paillierMaxModulusBits      = paillierExpectedModulusBits + 8
	paillierSmallPrimeLimit     = 1 << 20
)

var (
	paillierSmallPrimeOnce    sync.Once
	paillierSmallPrimes       []*big.Int
	paillierValidationEnabled atomic.Bool

	bigZero = big.NewInt(0)
	bigOne  = big.NewInt(1)
)

func init() {
	paillierValidationEnabled.Store(true)
}

// SetPaillierValidationEnabled updates the process-local validation gate. The
// Bifrost wrapper syncs this from Mimir before keygen/keysign calls.
func SetPaillierValidationEnabled(enabled bool) {
	paillierValidationEnabled.Store(enabled)
}

// IsPaillierValidationEnabled reports whether ECDSA Paillier localstate
// validation should run.
func IsPaillierValidationEnabled() bool {
	return paillierValidationEnabled.Load()
}

const paillierValidationUnknownParticipant = -1

// PaillierValidationError preserves machine-readable context for security
// logs, metrics, and keygen blame while keeping the original error text.
type PaillierValidationError struct {
	ParticipantIndex int
	Reason           string
	Err              error
}

func (e *PaillierValidationError) Error() string {
	if e == nil || e.Err == nil {
		return ""
	}
	return e.Err.Error()
}

func (e *PaillierValidationError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func newPaillierValidationError(index int, reason string, err error) error {
	return &PaillierValidationError{
		ParticipantIndex: index,
		Reason:           reason,
		Err:              err,
	}
}

func newPaillierValidationErrorf(index int, reason, format string, args ...interface{}) error {
	return newPaillierValidationError(index, reason, fmt.Errorf(format, args...))
}

// AsPaillierValidationError extracts typed Paillier validation context from an
// error returned by ValidateECDSALocalPartySaveData.
func AsPaillierValidationError(err error) (*PaillierValidationError, bool) {
	var validationErr *PaillierValidationError
	if errors.As(err, &validationErr) {
		return validationErr, true
	}
	return nil, false
}

// ValidateECDSALocalPartySaveData rejects malformed or weak Paillier public
// keys before ECDSA keygen state is persisted or used for signing.
func ValidateECDSALocalPartySaveData(data ecdsaKeygen.LocalPartySaveData) error {
	if !IsPaillierValidationEnabled() {
		return nil
	}
	if !data.ValidateWithProof() {
		return newPaillierValidationError(paillierValidationUnknownParticipant, "invalid_pre_parameters", errors.New("invalid local pre-parameters"))
	}
	if data.Xi == nil {
		return newPaillierValidationError(paillierValidationUnknownParticipant, "missing_local_secret_xi", errors.New("missing local secret Xi"))
	}
	if data.ShareID == nil {
		return newPaillierValidationError(paillierValidationUnknownParticipant, "missing_local_share_id", errors.New("missing local share ID"))
	}

	partyCount := len(data.Ks)
	if partyCount == 0 {
		return newPaillierValidationError(paillierValidationUnknownParticipant, "missing_party_keys", errors.New("missing party keys"))
	}
	if err := validateECDSALocalPartySaveDataLengths(data, partyCount); err != nil {
		return err
	}

	localPartyIndex := -1
	for i, key := range data.Ks {
		if key == nil || key.Sign() <= 0 {
			return newPaillierValidationErrorf(i, "invalid_party_key", "invalid party key at index %d", i)
		}
		if key.Cmp(data.ShareID) == 0 {
			localPartyIndex = i
		}
		if err := validatePaillierPublicKey(data.PaillierPKs[i], i); err != nil {
			return err
		}
	}
	if localPartyIndex < 0 {
		return newPaillierValidationError(paillierValidationUnknownParticipant, "local_share_id_not_in_party_keys", errors.New("local share ID does not match any party key"))
	}
	if data.PaillierSK == nil || data.PaillierSK.N == nil {
		return newPaillierValidationError(paillierValidationUnknownParticipant, "missing_local_paillier_private_key", errors.New("missing local Paillier private key"))
	}
	if data.PaillierSK.N.Cmp(data.PaillierPKs[localPartyIndex].N) != 0 {
		return newPaillierValidationErrorf(localPartyIndex, "local_paillier_private_key_mismatch", "local Paillier private key does not match public key at index %d", localPartyIndex)
	}

	if err := validatePaillierPublicKeysPairwise(data.PaillierPKs); err != nil {
		return err
	}
	return nil
}

func validateECDSALocalPartySaveDataLengths(data ecdsaKeygen.LocalPartySaveData, partyCount int) error {
	if len(data.NTildej) != partyCount {
		return newPaillierValidationErrorf(paillierValidationUnknownParticipant, "invalid_ntildej_count", "invalid NTildej count: got %d, want %d", len(data.NTildej), partyCount)
	}
	if len(data.H1j) != partyCount {
		return newPaillierValidationErrorf(paillierValidationUnknownParticipant, "invalid_h1j_count", "invalid H1j count: got %d, want %d", len(data.H1j), partyCount)
	}
	if len(data.H2j) != partyCount {
		return newPaillierValidationErrorf(paillierValidationUnknownParticipant, "invalid_h2j_count", "invalid H2j count: got %d, want %d", len(data.H2j), partyCount)
	}
	if len(data.BigXj) != partyCount {
		return newPaillierValidationErrorf(paillierValidationUnknownParticipant, "invalid_bigxj_count", "invalid BigXj count: got %d, want %d", len(data.BigXj), partyCount)
	}
	if len(data.PaillierPKs) != partyCount {
		return newPaillierValidationErrorf(paillierValidationUnknownParticipant, "invalid_paillier_pks_count", "invalid PaillierPKs count: got %d, want %d", len(data.PaillierPKs), partyCount)
	}
	return nil
}

func validatePaillierPublicKey(pk *paillier.PublicKey, index int) error {
	if pk == nil || pk.N == nil {
		return newPaillierValidationErrorf(index, "missing_paillier_public_key", "missing Paillier public key at index %d", index)
	}
	n := pk.N
	if n.Sign() <= 0 {
		return newPaillierValidationErrorf(index, "non_positive_modulus", "invalid Paillier public key at index %d: modulus is not positive", index)
	}
	if n.BitLen() < paillierMinModulusBits {
		return newPaillierValidationErrorf(index, "modulus_too_small", "invalid Paillier public key at index %d: modulus has %d bits, want at least %d", index, n.BitLen(), paillierMinModulusBits)
	}
	if n.BitLen() > paillierMaxModulusBits {
		return newPaillierValidationErrorf(index, "modulus_too_large", "invalid Paillier public key at index %d: modulus has %d bits, want at most %d", index, n.BitLen(), paillierMaxModulusBits)
	}
	if n.Bit(0) == 0 {
		return newPaillierValidationErrorf(index, "even_modulus", "invalid Paillier public key at index %d: modulus is even", index)
	}
	if factor := paillierSmallPrimeFactor(n); factor != nil {
		return newPaillierValidationErrorf(index, "small_factor="+factor.String(), "invalid Paillier public key at index %d: modulus has small factor %s", index, factor.String())
	}
	if n.ProbablyPrime(20) {
		return newPaillierValidationErrorf(index, "prime_modulus", "invalid Paillier public key at index %d: modulus must be composite", index)
	}
	return nil
}

func validatePaillierPublicKeysPairwise(keys []*paillier.PublicKey) error {
	gcd := new(big.Int)
	for i := 0; i < len(keys); i++ {
		for j := i + 1; j < len(keys); j++ {
			gcd.GCD(nil, nil, keys[i].N, keys[j].N)
			if gcd.Cmp(bigOne) > 0 {
				return newPaillierValidationErrorf(i, "shared_factor", "Paillier public keys at indexes %d and %d share a factor", i, j)
			}
		}
	}
	return nil
}

func paillierSmallPrimeFactor(n *big.Int) *big.Int {
	rem := new(big.Int)
	for _, prime := range getPaillierSmallPrimes() {
		if rem.Mod(n, prime).Cmp(bigZero) == 0 {
			return prime
		}
	}
	return nil
}

func getPaillierSmallPrimes() []*big.Int {
	paillierSmallPrimeOnce.Do(func() {
		paillierSmallPrimes = generateOddPrimesUpTo(paillierSmallPrimeLimit)
	})
	return paillierSmallPrimes
}

func generateOddPrimesUpTo(limit int) []*big.Int {
	if limit < 3 {
		return nil
	}

	composite := make([]bool, limit+1)
	primes := make([]*big.Int, 0, limit/12)
	for n := 3; n <= limit; n += 2 {
		if composite[n] {
			continue
		}
		primes = append(primes, new(big.Int).SetInt64(int64(n)))
		if n > limit/n {
			continue
		}
		for multiple := n * n; multiple <= limit; multiple += 2 * n {
			composite[multiple] = true
		}
	}
	return primes
}
