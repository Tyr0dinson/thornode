package types

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"maps"
	"strings"
	"time"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	mem "gitlab.com/thorchain/thornode/v3/x/thorchain/memo"
)

var (
	// ErrObservedTxSkippedInactive completes only this delivery attempt. It is
	// not proof of storage, observation, or accounting completion.
	ErrObservedTxSkippedInactive = errors.New("observation skipped while inactive")

	// ErrObservedTxOrphaned identifies the exact observation location removed by
	// an errata handoff. Durable scanners may acknowledge that location, but must
	// allow the same transaction ID to be observed at a new canonical location.
	ErrObservedTxOrphaned = errors.New("observed transaction location is orphaned")

	// ErrObservedTxPermanentlyReverted distinguishes THORChain's permanent
	// TxID-level Reverted barrier from transient Observer rejection. Durable chain
	// scanners must quarantine the exact TxID before acknowledging this rejection.
	ErrObservedTxPermanentlyReverted = errors.New("observed transaction is permanently reverted")

	// ErrObservedTxNotApplicable identifies a durable observation which is valid
	// but this node is not authorized to attest. Scanners may acknowledge it
	// without quarantining the transaction or retaining it for futile replay.
	ErrObservedTxNotApplicable = errors.New("observed transaction is not applicable to this node")
)

type TxIn struct {
	Chain                common.Chain `json:"chain"`
	TxArray              []*TxInItem  `json:"txArray"`
	Filtered             bool         `json:"filtered"`
	MemPool              bool         `json:"mem_pool"` // indicate whether this item is in the mempool or not
	ConfirmationRequired int64        `json:"confirmation_required"`

	// whether this originated from a "instant observation" - e.g. by a member of the signing party
	// immediately after signing, and also has incorrect gas, requiring a re-observation to correct.
	AllowFutureObservation bool `json:"allow_future_observation"`

	// Persisted is an optional local durability acknowledgement. Chain scanners
	// with a restart journal use it to retain replay data until BlockScanner has
	// queued the observations and durably advanced its scan position.
	// Producers must provide a buffered channel with room for one result.
	// Observer does not block if the acknowledgement receiver is unavailable.
	Persisted chan error `json:"-"`
	// FinalizePersistence atomically publishes any chain-scanner replay metadata
	// after Observer persistence succeeds but before BlockScanner advances its
	// scan position. A crash in between only causes a safe duplicate rescan.
	FinalizePersistence func() error `json:"-"`
	// ObservationHeld reports per-transaction orphan rejections for the finalizer.
	// It is process-local and never authorizes observation or signature reuse.
	ObservationHeld func(string) bool `json:"-"`
	// FinalizeSkipped performs the chain client's inactive-skip completion.
	// Unfinished mandatory work must retain a durable owner for retry.
	FinalizeSkipped func() error `json:"-"`
	// AbortPersistence releases an in-process replay delivery reservation after
	// Observer persistence fails; the synced replay journal remains for retry.
	AbortPersistence func() `json:"-"`
}

// ObservationVaultAttribution is a local scanner snapshot of authenticated vault
// membership and router association. It never enters signed observation data.
type ObservationVaultAttribution struct {
	PubKey common.PubKey  `json:"pub_key"`
	Router common.Address `json:"router,omitempty"`
}

type TxInItem struct {
	// VaultAttribution bridges extraction to the atomic metadata write. It is
	// consumed before Observer handoff and is not an admission flag or wire field.
	VaultAttribution []ObservationVaultAttribution `json:"-"`
	// EVMAttributionPending is a local finalizer disposition. Such an item is
	// withheld from Observer delivery while its existing recovery remains durable.
	EVMAttributionPending bool `json:"-"`
	// EVMAttributionAddresses retains failed local lookups when extraction emitted
	// no payload. These hints only schedule a fresh parse after membership returns.
	EVMAttributionAddresses []string `json:"-"`
	// EVMAttributionActiveOnly preserves the rule of each failed address lookup.
	EVMAttributionActiveOnly map[string]bool `json:"-"`
	EVMAttributionContracts  []string        `json:"-"`

	// RecoveryOwned identifies client recovery work. An inactive skip finishes only
	// its delivery attempt; accounting and correction evidence remain client-owned.
	// This local flag is persisted on deck and is not part of a signed observation.
	RecoveryOwned bool `json:"recovery_owned,omitempty"`
	// RequiredMigration has a durable scanner retry owner and cannot be skipped.
	RequiredMigration bool  `json:"required_migration,omitempty"`
	BlockHeight       int64 `json:"block_height"`
	// EVMFailedSynthetic marks the local gas-only view of a failed vault call.
	// It is persisted locally and never enters the signed observation payload.
	EVMFailedSynthetic bool `json:"evm_failed_synthetic,omitempty"`
	// BlockHash anchors this local observation to its original mined block.
	// It is persisted with the deck item and never enters the signed wire payload.
	BlockHash             string              `json:"block_hash,omitempty"`
	Tx                    string              `json:"tx"`
	Memo                  string              `json:"memo"`
	Sender                string              `json:"sender"`
	To                    string              `json:"to"` // to address
	Coins                 common.Coins        `json:"coins"`
	Gas                   common.Gas          `json:"gas"`
	ObservedVaultPubKey   common.PubKey       `json:"observed_vault_pub_key"`
	Aggregator            string              `json:"aggregator"`
	AggregatorTarget      string              `json:"aggregator_target"`
	AggregatorTargetLimit *cosmos.Uint        `json:"aggregator_target_limit"`
	SpentOutputRefs       []*common.OutputRef `json:"spent_output_refs,omitempty"`
	TxKey                 string              `json:"tx_key,omitempty"`
	// SolNonce is the post-tx state of the sending vault's durable-nonce account,
	// attached by the SOL scanner to observations of vault txs that created or
	// advanced it. Deliberately NOT part of Equals/EqualsObservedTx: dedup and the
	// commit-event match must not depend on it (a committed consensus tx can carry a
	// different observer's value, and the deck item must still clear).
	SolNonce             *common.SolNonceUpdate `json:"sol_nonce,omitempty"`
	CommittedUnFinalised bool                   `json:"committed_pre_final"`
	// CommittedFinalLeg is set on the copy left when one leg of a consolidation
	// has committed final (ConsolidationLegInbound or ConsolidationLegOutbound):
	// the copy now belongs to the other leg. Stateful, internal to the observer.
	CommittedFinalLeg string `json:"committed_final_leg,omitempty"`
	// PreliminaryLeg is the leg (same values) whose preliminary commit marked
	// this copy CommittedUnFinalised, so a repeated preliminary event of that
	// leg marks nothing further. Stateful, internal to the observer.
	PreliminaryLeg string `json:"preliminary_leg,omitempty"`

	// OnObservedTxStored and OnObservedTxRejected are local-only handoff
	// callbacks used by scanners which retain a durable replay record until the
	// observer has durably accepted (or explicitly rejected) the observation.
	// They must never be serialized into observer storage or sent over the wire.
	OnObservedTxStored   func()      `json:"-"`
	OnObservedTxRejected func(error) `json:"-"`
}
type TxInStatus byte

// The legs of a consolidation, as recorded in TxInItem.CommittedFinalLeg.
const (
	ConsolidationLegInbound  = "inbound"
	ConsolidationLegOutbound = "outbound"
)

func NewTxInItem(
	blockHeight int64,
	tx string,
	memo string,
	sender string,
	to string,
	coins common.Coins,
	gas common.Gas,
	observedVaultPubKey common.PubKey,
	aggregator string,
	aggregatorTarget string,
	aggregatorTargetLimit *cosmos.Uint,
) *TxInItem {
	return &TxInItem{
		BlockHeight:           blockHeight,
		Tx:                    tx,
		Memo:                  memo,
		Sender:                sender,
		To:                    to,
		Coins:                 coins,
		Gas:                   gas,
		ObservedVaultPubKey:   observedVaultPubKey,
		Aggregator:            aggregator,
		AggregatorTarget:      aggregatorTarget,
		AggregatorTargetLimit: aggregatorTargetLimit,
		CommittedUnFinalised:  false, // stateful parameter used internally in the observer
	}
}

const (
	Processing TxInStatus = iota
	Failed
)

func (t *TxInItem) Equals(other *TxInItem) bool {
	if t.BlockHeight != other.BlockHeight || !strings.EqualFold(t.BlockHash, other.BlockHash) {
		return false
	}
	if t.Tx != other.Tx {
		return false
	}
	if t.Memo != other.Memo {
		return false
	}
	if t.Sender != other.Sender {
		return false
	}
	if t.To != other.To {
		return false
	}
	if !t.Coins.EqualsEx(other.Coins) {
		return false
	}
	if !t.Gas.Equals(other.Gas) {
		return false
	}
	if !t.ObservedVaultPubKey.Equals(other.ObservedVaultPubKey) {
		return false
	}
	if normalizeString(t.TxKey) != normalizeString(other.TxKey) {
		return false
	}
	return true
}

func (t *TxInItem) EqualsObservedTx(other common.ObservedTx) bool {
	// Do not compare block height, as we only keep one deck item for a tx pre/post finalization.
	// The final block height will be ConfirmationCount higher than the unfinalized tx.

	txId, err := common.NewTxID(t.Tx)
	if err != nil {
		return false
	}
	if !txId.Equals(other.Tx.ID) {
		return false
	}
	if t.Memo != other.Tx.Memo {
		return false
	}
	if t.Sender != other.Tx.FromAddress.String() {
		return false
	}
	if t.To != other.Tx.ToAddress.String() {
		return false
	}
	if !t.Coins.EqualsEx(other.Tx.Coins) {
		return false
	}
	if !t.Gas.Equals(other.Tx.Gas) {
		return false
	}
	if !t.ObservedVaultPubKey.Equals(other.ObservedPubKey) {
		return false
	}
	if normalizeString(t.TxKey) != normalizeString(other.XmrTxKey) {
		return false
	}
	return true
}

func (t *TxInItem) Copy() *TxInItem {
	var refs []*common.OutputRef
	if len(t.SpentOutputRefs) > 0 {
		refs = make([]*common.OutputRef, len(t.SpentOutputRefs))
		for i, r := range t.SpentOutputRefs {
			cp := *r
			refs[i] = &cp
		}
	}
	return &TxInItem{
		VaultAttribution:         append([]ObservationVaultAttribution(nil), t.VaultAttribution...),
		EVMAttributionPending:    t.EVMAttributionPending,
		EVMAttributionAddresses:  append([]string(nil), t.EVMAttributionAddresses...),
		EVMAttributionActiveOnly: maps.Clone(t.EVMAttributionActiveOnly),
		EVMAttributionContracts:  append([]string(nil), t.EVMAttributionContracts...),
		RecoveryOwned:            t.RecoveryOwned,
		RequiredMigration:        t.RequiredMigration,
		BlockHeight:              t.BlockHeight,
		BlockHash:                t.BlockHash,
		EVMFailedSynthetic:       t.EVMFailedSynthetic,
		Tx:                       t.Tx,
		Memo:                     t.Memo,
		Sender:                   t.Sender,
		To:                       t.To,
		Coins:                    t.Coins.Copy(),
		Gas:                      common.Gas(common.Coins(t.Gas).Copy()),
		ObservedVaultPubKey:      t.ObservedVaultPubKey,
		Aggregator:               t.Aggregator,
		AggregatorTarget:         t.AggregatorTarget,
		AggregatorTargetLimit:    t.AggregatorTargetLimit,
		SpentOutputRefs:          refs,
		TxKey:                    t.TxKey,
		SolNonce:                 copySolNonce(t.SolNonce),
		CommittedUnFinalised:     t.CommittedUnFinalised,
		CommittedFinalLeg:        t.CommittedFinalLeg,
		PreliminaryLeg:           t.PreliminaryLeg,
		OnObservedTxStored:       t.OnObservedTxStored,
		OnObservedTxRejected:     t.OnObservedTxRejected,
	}
}

// IsEmpty return true only when every field in TxInItem is empty
func (t *TxInItem) IsEmpty() bool {
	return t.BlockHeight == 0 &&
		t.Tx == "" &&
		t.Memo == "" &&
		t.Sender == "" &&
		t.To == "" &&
		t.Coins.IsEmpty() &&
		t.Gas.IsEmpty() &&
		t.ObservedVaultPubKey.IsEmpty() &&
		t.TxKey == ""
}

func normalizeString(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

// CacheHash calculate the has used for signer cache
func (t *TxInItem) CacheHash(chain common.Chain, inboundHash string) string {
	str := fmt.Sprintf("%s|%s|%s|%s|%s", chain, t.To, t.Coins, t.Memo, inboundHash)
	return fmt.Sprintf("%X", sha256.Sum256([]byte(str)))
}

func (t *TxInItem) CacheVault(chain common.Chain) string {
	return InboundCacheKey(t.ObservedVaultPubKey.String(), chain.String())
}

// GetTotalTransactionValue return the total value of the requested asset
func (t TxIn) GetTotalTransactionValue(asset common.Asset, excludeFrom []common.Address) cosmos.Uint {
	total := cosmos.ZeroUint()
	for _, item := range t.TxArray {
		fromAsgard := false
		for _, fromAddress := range excludeFrom {
			if strings.EqualFold(fromAddress.String(), item.Sender) {
				fromAsgard = true
			}
		}
		if fromAsgard {
			continue
		}
		// skip confirmation counting if it is internal tx
		m, err := mem.ParseMemo(common.LatestVersion, item.Memo)
		if err == nil && m.IsInternal() {
			continue
		}
		c := item.Coins.GetCoin(asset)
		if c.IsEmpty() {
			continue
		}
		total = total.Add(c.Amount)
	}
	return total
}

// GetTotalGas return the total gas
func (t TxIn) GetTotalGas() cosmos.Uint {
	total := cosmos.ZeroUint()
	for _, item := range t.TxArray {
		if item.Gas == nil {
			continue
		}
		if err := item.Gas.Valid(); err != nil {
			continue
		}
		total = total.Add(item.Gas[0].Amount)
	}
	return total
}

func InboundCacheKey(vault, chain string) string {
	return fmt.Sprintf("inbound-%s-%s", vault, chain)
}

func copySolNonce(u *common.SolNonceUpdate) *common.SolNonceUpdate {
	if u == nil {
		return nil
	}
	cp := *u
	return &cp
}

// ObservationOccurrence is local scanner provenance, not signed consensus data.
type ObservationOccurrence struct {
	Chain                common.Chain
	TxID                 common.TxID
	Height               int64
	BlockHash            string
	ConfirmationRequired int64
}

// ObservationVaultAttributor restores authenticated local vault attribution for
// recovery-owned transactions after the current vault registry has pruned a key.
// Implementations must retain the evidence with the durable recovery obligation;
// returning no keys is uncertainty and never permission to discard the item.
type ObservationVaultAttributor interface {
	RecoveryObservationVaults(TxInItem) (common.PubKeys, error)
}

// ObservationValidation binds canonical evidence to a chain-view generation.
type ObservationValidation struct {
	EvidenceID   [32]byte
	CheckedAt    time.Time
	TipCheckedAt time.Time
	ExpiresAt    time.Time
	TipHeight    int64
	Occurrence   ObservationOccurrence
	TipHash      string
	Generation   uint64
}

// ObservationValidator is implemented by clients covered by orphan fencing.
// Validation must use fresh daemon evidence and must not synchronously deliver
// invalidation callbacks: gossip may hold its state lock while calling it.
type ObservationValidator interface {
	ValidateObservation(context.Context, ObservationOccurrence) (ObservationValidation, error)
	ObservationGeneration() uint64
	InvalidateObservationView()
	// RequestObservationRecovery durably records an intent before Observer cleanup.
	// Observer may call it under its deck lock: use local storage only, without
	// RPC, replay, or invalidation callbacks. The scanner
	// acknowledges durable local cleanup before making the intent replayable.
	RequestObservationRecovery([]ObservationOccurrence) error
	// ScheduleStartupObservationRecovery records already-cleaned work before
	// Start. restoreDeck must finish cleanup before starting any scanner. Also
	// imports legacy Observer obligations whose cleanup was already persisted.
	// Ordinary pending invalidations must never be downgraded by this call.
	ScheduleStartupObservationRecovery([]ObservationOccurrence) error
}

// ObservationReadiness describes one stable canonical batch view. Per-block
// uncertainty holds only its items; none of these results authorizes signing.
type ObservationReadiness struct {
	Orphaned  []ObservationOccurrence
	Uncertain []ObservationOccurrence
}

// ObservationRequirementRecorder retains the actual admitted payload's policy
// before a scanner can acknowledge durable handoff, including filtered replay.
type ObservationRequirementRecorder interface {
	RecordObservationRequirements(TxIn) error
}

// FinalizeObservation separates a stored handoff from an inactive skip. A client
// with a delivery finalizer must explicitly define what a skipped attempt means.
func (t TxIn) FinalizeObservation(outcome error) error {
	if errors.Is(outcome, ErrObservedTxSkippedInactive) {
		if t.FinalizeSkipped != nil {
			return t.FinalizeSkipped()
		}
		if t.FinalizePersistence != nil {
			return errors.New("scanner has no inactive completion path")
		}
		return nil
	}
	if outcome != nil {
		return outcome
	}
	if t.FinalizePersistence != nil {
		return t.FinalizePersistence()
	}
	return nil
}

// ErrObservationFinalizationPending means a bounded durable page completed but
// more pages remain. Retry the same finalizer without re-delivering the batch.
var ErrObservationFinalizationPending = errors.New("observation finalization has more durable work")
