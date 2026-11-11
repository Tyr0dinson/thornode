//go:build mainnet

package thorchain

import (
	"fmt"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// 3.22.0 BTC dust re-pay. One BTC outbound was recorded done but never landed on
// chain (its signed transaction was conflicted by a later spend), so the user was
// never paid the 9,417 sats owed. The coins never left the signing vault, which
// still holds them above its books, so re-pay the user with a fresh BTC outbound.
//
// This is its own toggleable step in its own cache context, kept separate from the
// bond-slash refunds that run alongside it so either can soft-skip or be turned off
// without affecting the other. The recipient is paid the full amount owed; the
// vault pays the outbound gas on top (UnSafeAddTxOutItem bypasses the outbound-fee
// deduction, as the other migration refund helpers do).
var btcDustRepay322Enabled = true

const (
	// btcDustRepay322InHash is the original inbound the unlanded outbound refunded.
	btcDustRepay322InHash = "10BF655F32906F7F9E521377112A88D4C3287440C919F20ED47C35295E2D6AF2"
	// btcDustRepay322Dest is the user's BTC address from that outbound.
	btcDustRepay322Dest = "bc1qz9pmxn7anf8r0us80nsyfegf27hrua99ll7865"
	// btcDustRepay322Sats is paid in full to the recipient.
	btcDustRepay322Sats = uint64(9417)
)

// btcDustRepay322Vault is the vault that still holds the coins that never left.
// If it is no longer an active asgard at the cut, the step falls back to any
// funded active asgard. A var so tests can point it at a fixture vault.
var btcDustRepay322Vault = "thorpub1addwnpepqva65k2jxjvafm6dufxh8zca2ghue4hsrwgx27tzjyj5e3uagqku7ql58h5"

// runBtcDustRepay322 schedules the re-pay in its own cache context. It never
// returns an error, so it cannot fail the upgrade; a guard failure soft-skips
// (logged loudly) and leaves nothing written.
func (m Migrator) runBtcDustRepay322(ctx cosmos.Context) {
	if !btcDustRepay322Enabled {
		ctx.Logger().Info("3.22.0 btc dust re-pay skipped: disabled")
		return
	}
	cacheCtx, commit := ctx.CacheContext()
	vaultPubKey, err := m.applyBtcDustRepay322(cacheCtx)
	if err != nil {
		ctx.Logger().Error("3.22.0 btc dust re-pay skipped", "error", err)
		return
	}
	commit()
	ctx.Logger().Info("3.22.0 btc dust re-pay scheduled",
		"dest", btcDustRepay322Dest, "sats", btcDustRepay322Sats, "vault", vaultPubKey)
}

// applyBtcDustRepay322 schedules the outbound into ctx (a cache context the caller
// discards on error). It pins the outbound to the vault that still holds the coins
// when that vault is active and funded, and otherwise falls back to any funded
// active asgard. Returns the vault it scheduled from.
func (m Migrator) applyBtcDustRepay322(ctx cosmos.Context) (common.PubKey, error) {
	k := m.mgr.Keeper()

	dest, err := common.NewAddress(btcDustRepay322Dest)
	if err != nil {
		return common.EmptyPubKey, fmt.Errorf("btc dust re-pay dest: %w", err)
	}
	inTxID, err := common.NewTxID(btcDustRepay322InHash)
	if err != nil {
		return common.EmptyPubKey, fmt.Errorf("btc dust re-pay in hash: %w", err)
	}
	coin := common.NewCoin(common.BTCAsset, cosmos.NewUint(btcDustRepay322Sats))

	active, err := k.GetAsgardVaultsByStatus(ctx, types.VaultStatus_ActiveVault)
	if err != nil {
		return common.EmptyPubKey, fmt.Errorf("fail to get active asgard vaults: %w", err)
	}
	// Never assign an outbound to a flagged vault: this bypasses prepareTxOutItem,
	// so keep the same safety invariant the other migration refund helpers keep.
	active = filterCompromisedVaults(ctx, k, active)
	if len(active) == 0 {
		return common.EmptyPubKey, fmt.Errorf("no active asgard vaults")
	}

	maxGasCoin, err := m.mgr.GasMgr().GetMaxGas(ctx, common.BTCChain)
	if err != nil {
		return common.EmptyPubKey, fmt.Errorf("fail to get max gas: %w", err)
	}
	// The gas asset is BTC, and UnSafeAddTxOutItem does not re-check balance, so
	// require the vault to hold the recipient amount AND the outbound gas, or the
	// pinned outbound would be unsignable.
	need := coin.Amount.Add(maxGasCoin.Amount)

	// Prefer the vault that still holds the unlanded coins; fall back to any funded
	// active asgard if it is no longer active at the cut.
	var chosen Vault
	for _, v := range active {
		if v.PubKey.String() == btcDustRepay322Vault && v.GetCoin(common.BTCAsset).Amount.GTE(need) {
			chosen = v
			break
		}
	}
	if chosen.IsEmpty() {
		for _, v := range active {
			if v.GetCoin(common.BTCAsset).Amount.GTE(need) {
				chosen = v
				break
			}
		}
	}
	if chosen.IsEmpty() {
		return common.EmptyPubKey, fmt.Errorf("no active asgard holds %s plus gas", coin)
	}

	// Idempotency: this is a one-off fund movement, safe only because store
	// migrations run exactly once at the upgrade height. A voter/OutHash guard
	// cannot be used here: the original outbound is recorded done (it was
	// conflicted on chain, not unsigned), so the voter would always short-circuit
	// a legitimate re-pay. Do not call this outside the one-shot migration.

	txOut := TxOutItem{
		Chain:            common.BTCChain,
		InHash:           inTxID,
		ToAddress:        dest,
		VaultPubKey:      chosen.PubKey,
		VaultPubKeyEddsa: txOutVaultPubKeyEddsa(common.BTCChain, chosen.PubKeyEddsa),
		VaultPubKeyFrost: chosen.PubKeyFrost,
		Coin:             coin,
		MaxGas:           common.Gas{maxGasCoin},
		Memo:             fmt.Sprintf("REFUND:%s", inTxID.String()),
	}
	if err := m.mgr.TxOutStore().UnSafeAddTxOutItem(ctx, m.mgr, txOut, ctx.BlockHeight()); err != nil {
		return common.EmptyPubKey, fmt.Errorf("fail to add btc dust re-pay outbound: %w", err)
	}
	return chosen.PubKey, nil
}
