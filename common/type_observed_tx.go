package common

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"unsafe"

	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

const (
	MaxXMRSpentOutputRefs = 32
	xmrTxKeyHexLength     = 64
)

// ObservedTxs a list of ObservedTx
type ObservedTxs []ObservedTx

// NewObservedTx create a new instance of ObservedTx
func NewObservedTx(tx Tx, height int64, pk PubKey, finalisedHeight int64) ObservedTx {
	return ObservedTx{
		Tx:             tx,
		Status:         Status_incomplete,
		BlockHeight:    height,
		ObservedPubKey: pk,
		FinaliseHeight: finalisedHeight,
	}
}

// Valid check whether the observed tx represent valid information
func (m *ObservedTx) Valid() error {
	if err := m.Tx.Valid(); err != nil {
		return err
	}
	// Memo should not be empty, but it can't be checked here, because a
	// message failed validation will be rejected by THORNode.
	// Thus THORNode can't refund customer accordingly , which will result fund lost
	if m.BlockHeight <= 0 {
		return errors.New("block height can't be zero")
	}
	if m.ObservedPubKey.IsEmpty() {
		return errors.New("observed pool pubkey is empty")
	}
	if m.FinaliseHeight <= 0 {
		return errors.New("finalise block height can't be zero")
	}
	if err := ValidateSpentOutputRefs(m.Tx.Chain, m.Tx.ID, m.SpentOutputRefs); err != nil {
		return err
	}
	if err := ValidateXMRTxKey(m.Tx.Chain, m.XmrTxKey); err != nil {
		return err
	}
	if err := ValidateSolNonceUpdate(m.Tx.Chain, m.SolNonce); err != nil {
		return err
	}
	return nil
}

func ValidateXMRTxKey(chain Chain, txKey string) error {
	txKey = strings.TrimSpace(txKey)
	if txKey == "" {
		return nil
	}
	if !chain.Equals(XMRChain) {
		return errors.New("xmr tx key is only valid for XMR observed txs")
	}
	if len(txKey) != xmrTxKeyHexLength {
		return fmt.Errorf("xmr tx key must be %d hex characters", xmrTxKeyHexLength)
	}
	for _, c := range txKey {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return errors.New("xmr tx key must be hex")
		}
	}
	return nil
}

func ValidateSpentOutputRefs(chain Chain, spendTxHash TxID, refs []OutputRef) error {
	if len(refs) == 0 {
		return nil
	}
	if !chain.Equals(XMRChain) {
		return errors.New("spent output refs are only valid for XMR observed txs")
	}
	if len(refs) > MaxXMRSpentOutputRefs {
		return fmt.Errorf("spent output refs exceeds max %d", MaxXMRSpentOutputRefs)
	}
	seen := make(map[string]struct{}, len(refs))
	for _, ref := range refs {
		if strings.TrimSpace(ref.TxHash) == "" {
			return errors.New("spent output ref tx hash cannot be empty")
		}
		if strings.TrimSpace(ref.KeyImage) == "" {
			return errors.New("spent output ref key image cannot be empty")
		}
		if strings.TrimSpace(ref.SpendTxHash) == "" {
			return errors.New("spent output ref spend tx hash cannot be empty")
		}
		if !TxID(ref.SpendTxHash).Equals(spendTxHash) {
			return fmt.Errorf("spent output ref spend tx hash %s does not match observed tx %s", ref.SpendTxHash, spendTxHash)
		}
		key := spentOutputRefIdentity(ref)
		if _, ok := seen[key]; ok {
			return fmt.Errorf("duplicate spent output ref: %s", key)
		}
		seen[key] = struct{}{}
	}
	return nil
}

// OutputRefsFromPointers converts pointer-based API refs into consensus refs.
func OutputRefsFromPointers(refs []*OutputRef) []OutputRef {
	if len(refs) == 0 {
		return nil
	}

	outputRefs := make([]OutputRef, 0, len(refs))
	for _, ref := range refs {
		if ref == nil {
			outputRefs = append(outputRefs, OutputRef{})
			continue
		}
		outputRefs = append(outputRefs, *ref)
	}
	return outputRefs
}

// IsEmpty check whether the Tx is empty
func (m *ObservedTx) IsEmpty() bool {
	return m.Tx.IsEmpty()
}

// Equals compare two ObservedTx
func (m ObservedTx) Equals(tx2 ObservedTx) bool {
	if !m.Tx.EqualsEx(tx2.Tx) {
		return false
	}
	if !m.ObservedPubKey.Equals(tx2.ObservedPubKey) {
		return false
	}
	if m.BlockHeight != tx2.BlockHeight {
		return false
	}
	if m.FinaliseHeight != tx2.FinaliseHeight {
		return false
	}
	if !strings.EqualFold(m.Aggregator, tx2.Aggregator) {
		return false
	}
	if !strings.EqualFold(m.AggregatorTarget, tx2.AggregatorTarget) {
		return false
	}
	emptyAmt := cosmos.ZeroUint()
	if m.AggregatorTargetLimit == nil {
		m.AggregatorTargetLimit = &emptyAmt
	}
	if tx2.AggregatorTargetLimit == nil {
		tx2.AggregatorTargetLimit = &emptyAmt
	}
	if !m.AggregatorTargetLimit.Equal(*tx2.AggregatorTargetLimit) {
		return false
	}
	if !spentOutputRefsEqual(m.SpentOutputRefs, tx2.SpentOutputRefs) {
		return false
	}
	if !m.HasEquivalentXMRTxKey(tx2) {
		return false
	}
	// A different attested durable-nonce value is a different vote entry, so the
	// nonce commit can count agreement per exact value. It is deliberately NOT part
	// of EqualsConsensus: nonce reporting must never affect outbound consensus.
	if !SolNonceUpdatesEqual(m.SolNonce, tx2.SolNonce) {
		return false
	}
	return true
}

func (m ObservedTx) HasEquivalentSpentOutputRefs(tx2 ObservedTx) bool {
	if !m.Tx.Chain.Equals(XMRChain) && !tx2.Tx.Chain.Equals(XMRChain) {
		return true
	}
	// Spent-output refs are supporting XMR metadata. Signers can provide them,
	// while non-signing observers may only have the tx key needed to validate
	// the outbound. Do not split consensus when one side lacks refs; still reject
	// conflicting non-empty ref sets.
	if len(m.SpentOutputRefs) == 0 || len(tx2.SpentOutputRefs) == 0 {
		return true
	}
	return spentOutputRefsEqual(m.SpentOutputRefs, tx2.SpentOutputRefs)
}

func (m ObservedTx) HasEquivalentXMRTxKey(tx2 ObservedTx) bool {
	if !m.Tx.Chain.Equals(XMRChain) && !tx2.Tx.Chain.Equals(XMRChain) {
		return true
	}
	return normalizeString(m.XmrTxKey) == normalizeString(tx2.XmrTxKey)
}

func (m ObservedTx) EqualsConsensus(tx2 ObservedTx) bool {
	return m.ObservedPubKey.Equals(tx2.ObservedPubKey) &&
		m.Tx.EqualsEx(tx2.Tx) &&
		m.HasEquivalentSpentOutputRefs(tx2) &&
		m.HasEquivalentXMRTxKey(tx2)
}

func (m ObservedTx) EqualsConsensusIgnoreGas(tx2 ObservedTx) bool {
	return m.ObservedPubKey.Equals(tx2.ObservedPubKey) &&
		m.Tx.EqualsExIgnoreGas(tx2.Tx) &&
		m.HasEquivalentSpentOutputRefs(tx2) &&
		m.HasEquivalentXMRTxKey(tx2)
}

func (m ObservedTx) AttestationUniqueHash() string {
	base := observedTxXMRMetadataUniqueHash(m.Tx.Hash(m.BlockHeight), m.Tx.Chain, m.XmrTxKey, m.SpentOutputRefs)
	return solNonceUniqueHash(base, m.Tx.Chain, m.SolNonce)
}

// IsFinal indicates whether ObserveTx is final.
func (m *ObservedTx) IsFinal() bool {
	return m.FinaliseHeight == m.BlockHeight
}

func (m *ObservedTx) GetOutHashes() TxIDs {
	txIDs := make(TxIDs, 0)
	for _, o := range m.OutHashes {
		txID, err := NewTxID(o)
		if err != nil {
			continue
		}
		txIDs = append(txIDs, txID)
	}
	return txIDs
}

// signerAddressCache memoizes bech32 signer string -> address decoding. Voters
// re-decode the same ~100 node addresses on every attestation, and decoding is a
// pure function of the string and the account prefix, so the cache cannot change
// results. Bounded: once full, new strings are decoded without being stored.
var (
	signerAddressCache   = make(map[signerAddressKey]cosmos.AccAddress)
	signerAddressCacheMu sync.RWMutex
)

// signerAddressKey keys signerAddressCache: the account prefix the string was
// decoded under, and the string.
type signerAddressKey struct{ prefix, signer string }

const signerAddressCacheMax = 4096

// signerAddress is cosmos.AccAddressFromBech32 with memoization of successes.
// The returned address is a fresh copy the caller may modify.
func signerAddress(signer string) (cosmos.AccAddress, error) {
	addr, cached, err := lookupSignerAddress(signer)
	if cached {
		return append(cosmos.AccAddress(nil), addr...), nil
	}
	return addr, err
}

// lookupSignerAddress is cosmos.AccAddressFromBech32 with memoization of
// successes. If cached, addr is the memoized copy, which is never written and
// must not be modified; otherwise it is the caller's own.
func lookupSignerAddress(signer string) (addr cosmos.AccAddress, cached bool, err error) {
	key := signerAddressKey{cosmos.GetConfig().GetBech32AccountAddrPrefix(), signer}
	signerAddressCacheMu.RLock()
	addr, ok := signerAddressCache[key]
	signerAddressCacheMu.RUnlock()
	if ok {
		return addr, true, nil
	}
	addr, err = cosmos.AccAddressFromBech32(signer)
	if err != nil {
		return nil, false, err
	}
	signerAddressCacheMu.Lock()
	if len(signerAddressCache) < signerAddressCacheMax {
		signerAddressCache[key] = append(cosmos.AccAddress(nil), addr...)
	}
	signerAddressCacheMu.Unlock()
	return addr, false, nil
}

// SignerAddressString returns the bytes of the address GetSigners yields for
// signer, as a string, or false if GetSigners skips signer (it does not decode).
// It allocates only when the decoding is not memoized.
func SignerAddressString(signer string) (string, bool) {
	addr, cached, err := lookupSignerAddress(signer)
	if err != nil {
		return "", false
	}
	if cached && len(addr) > 0 {
		// the memoized copy is never written, so it can back a string
		return unsafe.String(&addr[0], len(addr)), true
	}
	return string(addr), true
}

// GetSigners return all the node address that had sign the tx
func (m *ObservedTx) GetSigners() []cosmos.AccAddress {
	addrs := make([]cosmos.AccAddress, 0)
	for _, a := range m.Signers {
		addr, err := signerAddress(a)
		if err != nil {
			continue
		}
		addrs = append(addrs, addr)
	}
	return addrs
}

// String implement fmt.Stringer
func (m *ObservedTx) String() string {
	return m.Tx.String()
}

// HasSigned - check if given address has signed
func (m *ObservedTx) HasSigned(signer cosmos.AccAddress) bool {
	// Same result as scanning GetSigners(): a signer string can only decode to
	// signer if it equals signer's bech32 encoding up to case, so only such
	// strings need decoding.
	encoded := signer.String()
	for _, a := range m.Signers {
		if !strings.EqualFold(a, encoded) {
			continue
		}
		addr, err := signerAddress(a)
		if err == nil && addr.Equals(signer) {
			return true
		}
	}
	return false
}

// Sign add the given node account to signers list
// if the given signer is already in the list, it will return false, otherwise true
func (m *ObservedTx) Sign(signer cosmos.AccAddress) bool {
	if m.HasSigned(signer) {
		return false
	}
	m.Signers = append(m.Signers, signer.String())
	return true
}

// SetDone check the ObservedTx status, update it's status to done if the outbound tx had been processed
func (m *ObservedTx) SetDone(hash TxID, numOuts int) {
	// As an Asset->RUNE affiliate fee could also be RUNE,
	// allow multiple blank TxID OutHashes.
	// SetDone is still expected to only be called once (per ObservedTx) for each.
	if !hash.Equals(BlankTxID) {
		for _, done := range m.GetOutHashes() {
			if done.Equals(hash) {
				return
			}
		}
	}
	m.OutHashes = append(m.OutHashes, hash.String())
	if m.IsDone(numOuts) {
		m.Status = Status_done
	}
}

// IsDone will only return true when the number of out hashes is larger or equals the input number
func (m *ObservedTx) IsDone(numOuts int) bool {
	return len(m.OutHashes) >= numOuts
}

// MarshalJSON marshal Status to JSON in string form
func (x Status) MarshalJSON() ([]byte, error) {
	return json.Marshal(x.String())
}

// UnmarshalJSON convert string form back to Status
func (x *Status) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	if val, ok := Status_value[s]; ok {
		*x = Status(val)
		return nil
	}
	return fmt.Errorf("%s is not a valid status", s)
}

func (txs ObservedTxs) Contains(tx ObservedTx) bool {
	for _, item := range txs {
		if item.Equals(tx) {
			return true
		}
	}
	return false
}
