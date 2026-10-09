package thorchain

import (
	"fmt"
	"testing"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
)

func covCMustNoErr(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func covCCheckGauge(t *testing.T, sink *recordingSink, key string, want float32) {
	t.Helper()
	got, ok := sink.gauges[key]
	if !ok {
		t.Errorf("gauge %s not emitted", key)
		return
	}
	if got != want {
		t.Errorf("gauge %s = %v, want %v", key, got, want)
	}
}

func TestCovCVaultDetailTelemetry(t *testing.T) {
	ctx, mgr := setupBenchmarkManager(t)
	k := mgr.Keeper()
	const perVaultHeight = int64(1_000_003) // h%5 == 3
	const churnHeight = int64(1_000_010)    // h%10 == 0
	ctx = ctx.WithBlockHeight(perVaultHeight)

	// three active node accounts that form vault A's membership
	members := make([]string, 0, 3)
	for i := 0; i < 3; i++ {
		na := GetRandomValidatorNode(NodeActive)
		covCMustNoErr(t, k.SetNodeAccount(ctx, na))
		members = append(members, na.PubKeySet.Secp256k1.String())
	}

	pool := NewPool()
	pool.Asset = common.BTCAsset
	pool.Status = PoolAvailable
	pool.BalanceRune = cosmos.NewUint(1000 * common.One)
	pool.BalanceAsset = cosmos.NewUint(10 * common.One)
	pool.LPUnits = cosmos.NewUint(100)
	covCMustNoErr(t, k.SetPool(ctx, pool))

	// A: active, deficit, pending migrate txs, frozen BTC, insolvent, compromised
	vaultA := GetRandomVault()
	vaultA.Status = ActiveVault
	vaultA.StatusSince = perVaultHeight - 100
	vaultA.Membership = members
	vaultA.Chains = []string{common.BTCChain.String(), common.ETHChain.String()}
	vaultA.Deficit = common.NewCoins(common.NewCoin(common.BTCAsset, cosmos.NewUint(5*common.One)))
	vaultA.PendingTxBlockHeights = []int64{perVaultHeight - 10, perVaultHeight - 4, 0}
	vaultA.Frozen = []string{common.BTCChain.String(), "!!bad-chain!!"}
	covCMustNoErr(t, k.SetVault(ctx, vaultA))
	k.SetMimir(ctx, solvencyHaltVaultKey(vaultA.PubKey, common.BTCChain), 1)
	k.SetMimir(ctx, compromisedVaultMimirKeyPrefix+vaultA.PubKey.String(), 1)

	// B: retiring, holding RUNE and BTC, with its own BTC deficit
	vaultB := GetRandomVault()
	vaultB.Status = RetiringVault
	vaultB.StatusSince = perVaultHeight - 1
	vaultB.Membership = nil
	vaultB.Coins = common.NewCoins(
		common.NewCoin(common.RuneNative, cosmos.NewUint(7*common.One)),
		common.NewCoin(common.BTCAsset, cosmos.NewUint(common.One)),
	)
	vaultB.Deficit = common.NewCoins(common.NewCoin(common.BTCAsset, cosmos.NewUint(3*common.One)))
	covCMustNoErr(t, k.SetVault(ctx, vaultB))

	// C: inactive with a deficit still reported per vault
	vaultC := GetRandomVault()
	vaultC.Status = InactiveVault
	vaultC.Deficit = common.Coins{common.NewCoin(common.ETHAsset, cosmos.ZeroUint())}
	covCMustNoErr(t, k.SetVault(ctx, vaultC))

	// D: active, members without stored node accounts
	vaultD := GetRandomVault()
	vaultD.Status = ActiveVault
	vaultD.Membership = []string{GetRandomPubKey().String(), GetRandomPubKey().String()}
	covCMustNoErr(t, k.SetVault(ctx, vaultD))

	// E: active, no members
	vaultE := GetRandomVault()
	vaultE.Status = ActiveVault
	vaultE.Membership = []string{}
	covCMustNoErr(t, k.SetVault(ctx, vaultE))

	// churning halted from an earlier height
	k.SetMimir(ctx, "HaltChurning", 1)

	vkey := func(v Vault) string {
		return fmt.Sprintf("{vault_type=%s,pubkey=%s", v.Type.String(), v.PubKey.String())
	}

	// --- per-vault slot
	sink := installRecordingSink(t)
	emitVaultDetailTelemetry(ctx, mgr)
	covCCheckGauge(t, sink, "thornode.churn.halted", 1)
	covCCheckGauge(t, sink, "thornode.vault.deficit"+vkey(vaultA)+",asset=BTC.BTC}", 5)
	covCCheckGauge(t, sink, "thornode.vault.deficit"+vkey(vaultB)+",asset=BTC.BTC}", 3)
	if _, ok := sink.gauges["thornode.vault.deficit"+vkey(vaultC)+",asset=ETH.ETH}"]; ok {
		t.Error("zero deficit should not be emitted")
	}
	covCCheckGauge(t, sink, "thornode.vault.deficit_total{asset=BTC.BTC}", 8)
	covCCheckGauge(t, sink, "thornode.vault.status_blocks"+vkey(vaultA)+",status=ActiveVault}", 100)
	covCCheckGauge(t, sink, "thornode.vault.status_blocks"+vkey(vaultA)+",status=RetiringVault}", 0)
	covCCheckGauge(t, sink, "thornode.vault.pending_migrate_txs"+vkey(vaultA)+"}", 3)
	covCCheckGauge(t, sink, "thornode.vault.pending_migration_oldest_age"+vkey(vaultA)+"}", 10)
	covCCheckGauge(t, sink, "thornode.vault.migrate_round"+vkey(vaultB)+"}", 1)
	covCCheckGauge(t, sink, "thornode.vault.frozen"+vkey(vaultA)+",chain=BTC}", 1)
	covCCheckGauge(t, sink, "thornode.vault.frozen"+vkey(vaultA)+",chain=ETH}", 0)
	covCCheckGauge(t, sink, fmt.Sprintf("thornode.vault.insolvent{pubkey=%s,chain=BTC}", vaultA.PubKey), 1)
	covCCheckGauge(t, sink, fmt.Sprintf("thornode.vault.insolvent{pubkey=%s,chain=ETH}", vaultA.PubKey), 0)
	covCCheckGauge(t, sink, fmt.Sprintf("thornode.vault.compromised{pubkey=%s}", vaultA.PubKey), 1)
	covCCheckGauge(t, sink, fmt.Sprintf("thornode.vault.ineligible{pubkey=%s,reason=compromised}", vaultA.PubKey), 1)
	covCCheckGauge(t, sink, fmt.Sprintf("thornode.vault.ineligible{pubkey=%s,reason=insolvent}", vaultA.PubKey), 1)
	covCCheckGauge(t, sink, fmt.Sprintf("thornode.vault.ineligible{pubkey=%s,reason=compromised}", vaultD.PubKey), 0)
	// retiring value = 7 RUNE + 1 BTC at 100 RUNE/BTC
	covCCheckGauge(t, sink, "thornode.churn.retiring_value_rune", telem(cosmos.NewUint(107*common.One)))
	if sink.hasGauge("thornode.vault.keysign.signers_required") {
		t.Error("keysign gauges must only be emitted on the 10-block scan")
	}

	// --- 10-block churn scan, signer filtering active
	ctx = ctx.WithBlockHeight(churnHeight)
	sink = installRecordingSink(t)
	emitVaultDetailTelemetry(ctx, mgr)
	if sink.hasGauge("thornode.vault.deficit") {
		t.Error("per-vault gauges must only be emitted on slot 3")
	}
	covCCheckGauge(t, sink, "thornode.churn.expected_active_vaults", 1)
	aKey := fmt.Sprintf("{pubkey=%s}", vaultA.PubKey)
	covCCheckGauge(t, sink, "thornode.vault.keysign.signers_required"+aKey, 2) // threshold(3)+1
	covCCheckGauge(t, sink, "thornode.vault.keysign.signers_available"+aKey, 3)
	covCCheckGauge(t, sink, "thornode.vault.keysign.jailed_fallback_signers"+aKey, 0)
	covCCheckGauge(t, sink, "thornode.vault.keysign.never_signers"+aKey, 0)
	// members without a stored node account read back as empty, unjailed accounts
	covCCheckGauge(t, sink, fmt.Sprintf("thornode.vault.keysign.signers_available{pubkey=%s}", vaultD.PubKey), 2)
	if sink.hasGauge(fmt.Sprintf("thornode.vault.keysign.signers_required{pubkey=%s}", vaultE.PubKey)) {
		t.Error("vault without members should be skipped")
	}

	// --- 10-block churn scan with signer filtering disabled by mimir
	k.SetMimir(ctx, constants.MimirKeyDisableTSSSignerFiltering, 1)
	sink = installRecordingSink(t)
	emitVaultDetailTelemetry(ctx, mgr)
	covCCheckGauge(t, sink, "thornode.vault.keysign.signers_available"+aKey, 3)
	covCCheckGauge(t, sink, fmt.Sprintf("thornode.vault.keysign.signers_available{pubkey=%s}", vaultD.PubKey), 2)
	covCCheckGauge(t, sink, fmt.Sprintf("thornode.vault.keysign.never_signers{pubkey=%s}", vaultD.PubKey), 0)

	// --- neither slot: only the halted gauge
	sink = installRecordingSink(t)
	emitVaultDetailTelemetry(ctx.WithBlockHeight(churnHeight+1), mgr)
	covCCheckGauge(t, sink, "thornode.churn.halted", 1)
	if n := sink.gaugesWithPrefix("thornode.vault."); n != 0 {
		t.Errorf("off-slot height emitted %d vault gauges", n)
	}
}
