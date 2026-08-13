package types

import (
	"fmt"
	"strings"
)

// Cursor returns the fixed-width identity suffix used by lifecycle secondary
// indexes and persisted recovery cursors.
func (m MempoolSwap) Cursor() string {
	return fmt.Sprintf("%s/%010d", m.TxId, m.Index)
}

// Valid reports whether status is a persisted, actionable lifecycle state.
func (s MempoolSwapStatus) Valid() bool {
	switch s {
	case MempoolSwapStatus_RESERVED,
		MempoolSwapStatus_EXECUTING,
		MempoolSwapStatus_STAGED,
		MempoolSwapStatus_SETTLED,
		MempoolSwapStatus_ROLLBACK_PENDING,
		MempoolSwapStatus_FINALIZED:
		return true
	default:
		return false
	}
}

// Valid validates the identity, scheduling metadata, rollback payload, and
// deferred accounting deltas carried by a mempool-swap lifecycle record.
func (m MempoolSwap) Valid() error {
	if m.TxId.IsEmpty() {
		return fmt.Errorf("mempool swap tx id cannot be empty")
	}
	// The current mempool path owns only the primary advanced-queue item. Keep
	// this explicit until every completion/rollback call is index-aware; accepting
	// sidecar indexes here would create records recovery cannot safely delete.
	if m.Index != 0 {
		return fmt.Errorf("mempool swap index must be zero")
	}
	if m.VaultPubKey.IsEmpty() {
		return fmt.Errorf("mempool swap vault pub key cannot be empty")
	}
	if m.ReservedTor.IsNil() {
		return fmt.Errorf("mempool swap reserved TOR cannot be nil")
	}
	if m.Status == MempoolSwapStatus_FINALIZED && !m.ReservedTor.IsZero() {
		return fmt.Errorf("finalized mempool swap tombstone cannot reserve TOR")
	}
	if m.ReservedTor.IsZero() &&
		m.Status != MempoolSwapStatus_FINALIZED &&
		m.Status != MempoolSwapStatus_ROLLBACK_PENDING {
		return fmt.Errorf("active mempool swap reserved TOR must be positive")
	}
	if m.AdmittedHeight <= 0 {
		return fmt.Errorf("mempool swap admitted height must be positive")
	}
	if m.UpdatedHeight < m.AdmittedHeight {
		return fmt.Errorf("mempool swap updated height cannot precede admitted height")
	}
	if m.NextRecoveryHeight <= 0 {
		return fmt.Errorf("mempool swap next recovery height must be positive")
	}
	if !m.Status.Valid() {
		return fmt.Errorf("invalid mempool swap status: %s", m.Status)
	}
	if !m.LatestSwap.Tx.ID.Equals(m.TxId) {
		return fmt.Errorf("mempool swap tx id does not match latest swap")
	}
	if m.LatestSwap.Index != m.Index {
		return fmt.Errorf("mempool swap index does not match latest swap")
	}
	if err := validateMempoolSwapMessage(m.LatestSwap); err != nil {
		return err
	}
	if err := m.PendingEffects.Valid(); err != nil {
		return err
	}
	return nil
}

func validateMempoolSwapMessage(msg MsgSwap) error {
	if msg.TradeTarget.IsNil() || msg.AffiliateBasisPoints.IsNil() {
		return fmt.Errorf("mempool swap latest swap contains uninitialized amounts")
	}
	// Runtime admission only creates lifecycle records for observed external-L1
	// inbounds. Native sources have no confirmation window to optimize and can
	// enter mint/burn paths whose rollback contract is deliberately different.
	// Keep that policy in the durable schema as well so genesis restoration and
	// direct keeper callers cannot create a lifecycle that recovery does not support.
	if len(msg.Tx.Coins) != 1 {
		return fmt.Errorf("mempool swap latest swap must contain exactly one source coin")
	}
	if !msg.Tx.Coins[0].Asset.IsExternalL1Asset() {
		return fmt.Errorf("mempool swap latest swap source asset must be an external L1 asset")
	}
	if msg.AggregatorTargetLimit != nil && msg.AggregatorTargetLimit.IsNil() {
		return fmt.Errorf("mempool swap latest swap contains an uninitialized aggregator limit")
	}
	for _, coin := range msg.Tx.Coins {
		if coin.Amount.IsNil() {
			return fmt.Errorf("mempool swap latest swap contains an uninitialized coin amount")
		}
	}
	for _, coin := range msg.Tx.Gas {
		if coin.Amount.IsNil() {
			return fmt.Errorf("mempool swap latest swap contains an uninitialized gas amount")
		}
	}
	if msg.State == nil {
		return fmt.Errorf("mempool swap latest swap state cannot be nil")
	}
	if msg.State.Deposit.IsNil() || msg.State.Withdrawn.IsNil() ||
		msg.State.In.IsNil() || msg.State.Out.IsNil() {
		return fmt.Errorf("mempool swap latest swap state contains uninitialized amounts")
	}
	if err := msg.ValidateBasic(); err != nil {
		return fmt.Errorf("invalid mempool swap latest swap: %w", err)
	}
	return nil
}

// Valid validates every deferred effect. Empty effect slices are valid at
// admission; individual zero-value effect records are not.
func (e MempoolSwapPendingEffects) Valid() error {
	for i, effect := range e.Pools {
		if effect.Asset.IsEmpty() {
			return fmt.Errorf("mempool swap pool effect %d has an empty asset", i)
		}
		amounts := []interface{ IsNil() bool }{
			effect.LiquidityFeeRune,
			effect.SwapSlip,
			effect.VolumeRune,
			effect.VolumeAsset,
			effect.ReserveRune,
			effect.TcyRune,
			effect.BalanceRuneIncrease,
			effect.BalanceRuneDecrease,
			effect.BalanceAssetIncrease,
			effect.BalanceAssetDecrease,
		}
		for _, amount := range amounts {
			if amount.IsNil() {
				return fmt.Errorf("mempool swap pool effect %d has an uninitialized amount", i)
			}
		}
	}
	for i, effect := range e.Clout {
		if effect.Address.IsEmpty() {
			return fmt.Errorf("mempool swap clout effect %d has an empty address", i)
		}
		if effect.Score.IsNil() {
			return fmt.Errorf("mempool swap clout effect %d has an uninitialized score", i)
		}
	}
	for i, effect := range e.Affiliates {
		if strings.TrimSpace(effect.Thorname) == "" {
			return fmt.Errorf("mempool swap affiliate effect %d has an empty THORName", i)
		}
		if effect.FeeRune.IsNil() {
			return fmt.Errorf("mempool swap affiliate effect %d has an uninitialized fee", i)
		}
	}
	for i, effect := range e.DynamicFees {
		if strings.TrimSpace(effect.Thorname) == "" {
			return fmt.Errorf("mempool swap dynamic-fee effect %d has an empty THORName", i)
		}
		if effect.PairAssetA.IsEmpty() || effect.PairAssetB.IsEmpty() {
			return fmt.Errorf("mempool swap dynamic-fee effect %d has an empty pair asset", i)
		}
		if effect.VolumeTor.IsNil() || effect.FeesTor.IsNil() {
			return fmt.Errorf("mempool swap dynamic-fee effect %d has an uninitialized amount", i)
		}
	}
	for i, effect := range e.Cleanups {
		if effect.Item.InHash.IsEmpty() || effect.Item.Coin.IsEmpty() {
			return fmt.Errorf("mempool swap cleanup effect %d has an invalid outbound", i)
		}
		if strings.TrimSpace(effect.BurnReason) == "" || strings.TrimSpace(effect.CleanupKey) == "" {
			return fmt.Errorf("mempool swap cleanup effect %d has incomplete identity", i)
		}
	}
	for i, effect := range e.Supplies {
		if effect.Asset.IsEmpty() ||
			(!effect.Asset.IsSyntheticAsset() && !effect.Asset.IsDerivedAsset() && !effect.Asset.IsRune()) {
			return fmt.Errorf("mempool swap supply effect %d must use a synthetic, derived, or RUNE asset", i)
		}
		if effect.SupplyIncrease.IsNil() || effect.SupplyDecrease.IsNil() {
			return fmt.Errorf("mempool swap supply effect %d has an uninitialized amount", i)
		}
		if effect.SupplyIncrease.IsZero() && effect.SupplyDecrease.IsZero() {
			return fmt.Errorf("mempool swap supply effect %d is empty", i)
		}
	}
	return nil
}
