package thorchain

import (
	"fmt"
	"strconv"

	"github.com/cosmos/cosmos-sdk/telemetry"
	"github.com/hashicorp/go-metrics"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"
	mem "gitlab.com/thorchain/thornode/v3/x/thorchain/memo"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// A solvency-gated chain (SolvencyGateInbound-<CHAIN> > 0) does not execute a
// finalised inbound when it is observed. The inbound credits its vault as usual,
// then is parked until a solvency consensus, reached in a later THORChain block,
// attests the vault's wallet balance at or above the inbound's external height
// and finds the wallet covering the vault's full ledger (see
// solventForSolvencyGate). An inbound credited for funds that never reached the
// vault (a spoofed observation) leaves the vault short, so it is not released,
// and the solvency checker halts the chain once the shortfall exceeds the
// permitted gap, before its swap or refund pays anything out.

const (
	// solvencyGateMaxReleasesPerBlock bounds the parked inbounds EndBlock executes
	// or drops in one block.
	solvencyGateMaxReleasesPerBlock = 100
	// solvencyGateMaxVaultsPerChain bounds the vaults whose parked inbounds are read
	// per chain and pass. Each vault is read from its lowest-height inbound, so a
	// vault that cannot be confirmed holds back only its own inbounds.
	solvencyGateMaxVaultsPerChain = 200

	eventSolvencyGatedInboundDropped = "solvency_gated_inbound_dropped"
)

// isSolvencyGated reports whether inbounds on the chain wait for solvency
// confirmation. It fails closed: an unreadable Mimir gates the chain.
func isSolvencyGated(ctx cosmos.Context, k keeper.Keeper, chain common.Chain) bool {
	key := fmt.Sprintf(constants.MimirTemplateSolvencyGateInbound, chain)
	value, err := k.GetMimir(ctx, key)
	if err != nil {
		ctx.Logger().Error("fail to get mimir, gating inbounds", "key", key, "error", err)
		return true
	}
	return value > 0
}

// parkSolvencyGatedInbound parks a finalised, vault-credited inbound if its chain
// is solvency-gated, and reports whether it did. It is parked under the vault's
// primary pubkey, which solvency confirmations are keyed by, not the observed
// pubkey, which is the vault's EdDSA or FROST key on SOL and XMR.
func parkSolvencyGatedInbound(ctx cosmos.Context, mgr Manager, voter ObservedTxVoter, vault Vault) (bool, error) {
	k := mgr.Keeper()
	tx := voter.Tx
	if !isSolvencyGated(ctx, k, tx.Tx.Chain) {
		return false, nil
	}
	in := types.SolvencyGatedInbound{
		Chain:    tx.Tx.Chain,
		PubKey:   vault.PubKey,
		Height:   tx.BlockHeight,
		TxID:     tx.Tx.ID,
		ParkedAt: ctx.BlockHeight(),
	}
	if err := k.SetSolvencyGatedInbound(ctx, in); err != nil {
		return false, fmt.Errorf("fail to park solvency gated inbound: %w", err)
	}
	reserveSolvencyGatedReference(ctx, mgr, voter)
	telemetry.IncrCounterWithLabels(
		[]string{"thornode", "solvency_gate", "parked"}, 1,
		[]metrics.Label{telemetry.NewLabel("chain", telemChain(in.Chain))},
	)
	ctx.Logger().Info("inbound parked until vault solvency is confirmed", "chain", in.Chain, "id", in.TxID, "height", in.Height, "vault", in.PubKey)
	return true, nil
}

// reserveSolvencyGatedReference records a parked inbound's use of a valid
// reference memo when it is parked, as finalisation would have. While it waits,
// other inbounds cannot use up the reference's allowance, and the release
// resolves the reference against its finalisation height (see
// lookupMemoFromReference), so the payment instruction it finalised with still
// applies.
func reserveSolvencyGatedReference(ctx cosmos.Context, mgr Manager, voter ObservedTxVoter) {
	if len(voter.Tx.Tx.Coins) == 0 {
		return
	}
	memo, err := ParseMemoWithTHORNames(ctx, mgr.Keeper(), voter.Tx.Tx.Memo)
	if err != nil || !memo.IsType(TxReferenceReadMemo) {
		return
	}
	asset := voter.Tx.Tx.Coins[0].Asset
	lookup := lookupMemoFromReference(ctx, mgr, asset, voter.Tx.Tx, voter.UnfinalizedHeight, ctx.BlockHeight())
	if lookup.memo == "" {
		return
	}
	trackReferenceMemoUsage(ctx, mgr, asset, voter.Tx.Tx.Memo, voter.Tx.Tx.ID)
}

// hasSolvencyGatedInbounds reports whether the vault has inbounds parked on any of
// its chains.
func hasSolvencyGatedInbounds(ctx cosmos.Context, k keeper.Keeper, vault Vault) bool {
	for _, chain := range vault.GetChains() {
		if k.HasSolvencyGatedInboundsForVault(ctx, chain, vault.PubKey) {
			return true
		}
	}
	return false
}

// confirmSolvencyGate records a solvent consensus for the vault on a gated chain
// at the given external height. Inbounds parked in an earlier THORChain block at
// or below that height are released by the next EndBlock.
func confirmSolvencyGate(ctx cosmos.Context, mgr Manager, vault Vault, walletCoins common.Coins, chain common.Chain, height int64) (gated bool) {
	k := mgr.Keeper()
	if !isSolvencyGated(ctx, k, chain) {
		return false
	}
	if !solventForSolvencyGate(ctx, mgr, vault, walletCoins, chain) {
		ctx.Logger().Info("vault solvent but its wallet does not cover its ledger, solvency gated inbounds stay parked", "chain", chain, "vault", vault.PubKey)
		return true
	}
	if err := k.SetSolvencyConfirmation(ctx, chain, vault.PubKey, height); err != nil {
		ctx.Logger().Error("fail to set solvency confirmation", "chain", chain, "vault", vault.PubKey, "error", err)
	}
	return true
}

// otherVaultFlaggedInsolvent reports whether an Asgard vault other than pubKey that
// still holds funds on the chain is flagged insolvent there
// (SolvencyHaltVault-<pubkey>-<chain>). On a solvency-gated chain Bifrost reports
// solvent vaults alongside insolvent ones, and their consensuses can land in
// different blocks, so a solvent report must not lift a halt another vault still
// holds. Vaults without funds on the chain are skipped: Bifrost does not report
// them, so they could never clear their flag. A read error returns true, which
// keeps the chain halted.
func otherVaultFlaggedInsolvent(ctx cosmos.Context, k keeper.Keeper, pubKey common.PubKey, chain common.Chain) (bool, error) {
	vaults, err := k.GetAsgardVaults(ctx)
	if err != nil {
		return true, fmt.Errorf("fail to get asgard vaults: %w", err)
	}
	for _, vault := range vaults {
		if vault.PubKey.Equals(pubKey) || !vault.HasFundsForChain(chain) {
			continue
		}
		flagged, err := k.GetMimir(ctx, solvencyHaltVaultKey(vault.PubKey, chain))
		if err != nil {
			return true, fmt.Errorf("fail to get solvency halt vault mimir: %w", err)
		}
		if flagged > 0 {
			return true, nil
		}
	}
	return false, nil
}

// solventForSolvencyGate is stricter than insolvencyCheck. That check lowers the
// vault by its pending outbounds, which the wallet still holds until they are
// broadcast, so a busy vault's wallet routinely exceeds its adjusted ledger, and
// the surplus could cover a spoofed inbound. Here the wallet must cover the full
// ledger, to within the permitted gap, for every asset of the chain. A vault with
// a broadcast outbound not yet observed reads short until the outbound is
// observed, which only delays its parked inbounds.
func solventForSolvencyGate(ctx cosmos.Context, mgr Manager, vault Vault, walletCoins common.Coins, chain common.Chain) bool {
	k := mgr.Keeper()
	permittedGapInRune := k.DollarConfigInRune(ctx, constants.PermittedSolvencyGapUSD)
	for _, c := range vault.Coins {
		if !c.Asset.Chain.Equals(chain) || c.IsEmpty() {
			continue
		}
		walletCoin := walletCoins.GetCoin(c.Asset)
		gap := c.Amount
		if !walletCoin.IsEmpty() {
			if walletCoin.Amount.GTE(c.Amount) {
				continue
			}
			gap = c.Amount.Sub(walletCoin.Amount)
		}
		pool, err := k.GetPool(ctx, c.Asset.GetLayer1Asset())
		if err != nil {
			ctx.Logger().Error("fail to get pool for solvency gate", "asset", c.Asset, "error", err)
			return false
		}
		gapInRune := pool.AssetValueInRune(gap)
		if gapInRune.IsZero() || gapInRune.GT(permittedGapInRune) {
			return false
		}
	}
	return true
}

// releaseSolvencyGatedInbounds executes parked inbounds whose vault has been
// confirmed solvent at or above their external height, by a consensus reached
// after they were parked. A consensus reached in the block that parked an inbound,
// or earlier, was checked against a vault balance that did not yet include it.
// Inbounds on a chain whose gate has been switched off are released unconditionally.
// Inbounds whose voter was reverted, or is gone, are dropped without a confirmation.
//
// Each vault's inbounds are read in external height order, so once a vault's
// lowest inbound cannot be released, neither can its others, and the next vault
// is read instead.
func releaseSolvencyGatedInbounds(ctx cosmos.Context, mgr Manager) error {
	k := mgr.Keeper()
	handler := NewInternalHandler(mgr)
	processed := 0
	for _, chain := range common.AllChains {
		gated, gatedRead := false, false
		for {
			if processed >= solvencyGateMaxReleasesPerBlock {
				return nil
			}
			heads, err := k.GetSolvencyGatedInboundHeads(ctx, chain, solvencyGateMaxVaultsPerChain)
			if err != nil {
				ctx.Logger().Error("fail to get solvency gated inbounds", "chain", chain, "error", err)
			}
			if len(heads) == 0 {
				break
			}
			if !gatedRead {
				gated, gatedRead = isSolvencyGated(ctx, k, chain), true
			}
			progressed := false
			for _, in := range heads {
				if processed >= solvencyGateMaxReleasesPerBlock {
					return nil
				}
				var voter ObservedTxVoter
				voter, err = k.GetObservedTxInVoter(ctx, in.TxID)
				if err != nil || voter.Reverted || voter.Tx.IsEmpty() {
					// An inbound reverted while parked (for example by errata) must not execute.
					reason := "reverted"
					if err != nil || voter.Tx.IsEmpty() {
						reason = "voter unavailable"
					}
					dropSolvencyGatedInbound(ctx, k, in, reason)
					telemetry.IncrCounterWithLabels(
						[]string{"thornode", "solvency_gate", "unparked"}, 1,
						[]metrics.Label{
							telemetry.NewLabel("chain", telemChain(in.Chain)),
							telemetry.NewLabel("result", "dropped"),
						},
					)
					processed++
					progressed = true
					continue
				}
				if gated {
					var confirmedHeight, confirmedAt int64
					confirmedHeight, confirmedAt, err = k.GetSolvencyConfirmation(ctx, chain, in.PubKey)
					if err != nil {
						ctx.Logger().Error("fail to get solvency confirmation", "chain", chain, "vault", in.PubKey, "error", err)
						continue
					}
					if confirmedHeight < in.Height || confirmedAt <= in.ParkedAt {
						continue
					}
				}
				releaseSolvencyGatedInbound(ctx, mgr, handler, in, voter)
				processed++
				progressed = true
			}
			if !progressed {
				break
			}
		}
	}
	return nil
}

// releaseSolvencyGatedInbound unparks and executes one inbound. Its state changes
// commit only if execution succeeds. Otherwise the inbound is refunded instead, as
// an inbound that failed execution would; it is dropped, with an event, only if the
// refund fails too, since retrying an execution that failed would fail every block.
func releaseSolvencyGatedInbound(ctx cosmos.Context, mgr Manager, handler cosmos.Handler, in types.SolvencyGatedInbound, voter ObservedTxVoter) {
	k := mgr.Keeper()
	k.DeleteSolvencyGatedInbound(ctx, in)

	result := "released"
	defer func() {
		telemetry.IncrCounterWithLabels(
			[]string{"thornode", "solvency_gate", "unparked"}, 1,
			[]metrics.Label{
				telemetry.NewLabel("chain", telemChain(in.Chain)),
				telemetry.NewLabel("result", result),
			},
		)
	}()

	execErr := runSolvencyGatedRelease(ctx, func(cacheCtx cosmos.Context) error {
		vault, err := k.GetVault(cacheCtx, in.PubKey)
		if err != nil {
			return fmt.Errorf("fail to get vault %s: %w", in.PubKey, err)
		}
		// The consensus tx stands in for the observation that finalised it. Its memo
		// is the voter's: a generated r:<ref> memo for a memoless inbound, which the
		// processing below resolves as it would have at finalisation, in.ParkedAt.
		tx := voter.Tx
		var memo mem.Memo
		memo, _ = ParseMemoWithTHORNames(cacheCtx, k, tx.Tx.Memo)
		signers := tx.GetSigners()
		if len(signers) == 0 {
			return fmt.Errorf("inbound has no signers")
		}
		return processFinalisedObservedTxIn(cacheCtx, mgr, signers[0], handler, tx, voter, vault, memo, in.ParkedAt)
	})
	if execErr == nil {
		ctx.Logger().Info("solvency gated inbound released", "chain", in.Chain, "id", in.TxID, "height", in.Height)
		return
	}
	ctx.Logger().Error("fail to process solvency gated inbound, refunding", "id", in.TxID, "error", execErr)

	refundErr := runSolvencyGatedRelease(ctx, func(cacheCtx cosmos.Context) error {
		return refundTx(cacheCtx, voter.Tx, mgr, CodeTxFail, fmt.Sprintf("solvency gated inbound failed: %s", execErr), "")
	})
	if refundErr == nil {
		result = "refunded"
		return
	}
	result = "error"
	ctx.Logger().Error("fail to refund solvency gated inbound", "id", in.TxID, "error", refundErr)
	dropSolvencyGatedInbound(ctx, k, in, fmt.Sprintf("execution failed: %s; refund failed: %s", execErr, refundErr))
}

// runSolvencyGatedRelease runs f in a cache context and commits its writes only if
// it succeeds. The inbound processing it runs normally executes inside a
// transaction, where a panic only fails that transaction; here it runs in EndBlock,
// so a panic is turned into an error instead of halting the chain.
func runSolvencyGatedRelease(ctx cosmos.Context, f func(cosmos.Context) error) (err error) {
	cacheCtx, commit := ctx.CacheContext()
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic: %v", r)
		}
	}()
	if err = f(cacheCtx); err != nil {
		return err
	}
	commit()
	return nil
}

// dropSolvencyGatedInbound removes a parked inbound that will not execute and
// emits an event, so the credited inbound is visible to operators.
func dropSolvencyGatedInbound(ctx cosmos.Context, k keeper.Keeper, in types.SolvencyGatedInbound, reason string) {
	k.DeleteSolvencyGatedInbound(ctx, in)
	ctx.EventManager().EmitEvent(cosmos.NewEvent(eventSolvencyGatedInboundDropped,
		cosmos.NewAttribute("chain", in.Chain.String()),
		cosmos.NewAttribute("tx_id", in.TxID.String()),
		cosmos.NewAttribute("vault", in.PubKey.String()),
		cosmos.NewAttribute("height", strconv.FormatInt(in.Height, 10)),
		cosmos.NewAttribute("reason", reason),
	))
	ctx.Logger().Info("solvency gated inbound dropped", "chain", in.Chain, "id", in.TxID, "reason", reason)
}
