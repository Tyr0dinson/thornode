//go:build mainnet

package thorchain

import (
	"fmt"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"
)

// 3.22.0 SOL->BTC stuck-payout repair. Four SOL-inbound swaps had their BTC outbound
// paid out by a separate bifrost recovery, which set OutHash on the scheduled
// TxOutItem. But each SOL inbound voter still carries an 80-byte TRUNCATED memo on
// its matching action, and ObservedTxVoter.AddOutTx compares the action memo to the
// observed outbound memo with EqualFold (matchActionItem, type_observed_tx.go). The
// truncated memo never equals the full OUT:<in_hash>, so the BTC out tx was never
// recorded in the voter's OutTxs, voter.Txs were never SetDone, and no outbound event
// was emitted. To Midgard/clients the swap looks unfinished forever even though the
// user WAS paid.
//
// This step repairs each voter in place, mirroring exactly the bookkeeping
// handler_common_outbound would have performed had the memo matched: rewrite the
// matching action's memo to the untruncated OUT:<in_hash>, record the already-
// broadcast BTC out tx (its ID read LIVE from the paid TxOutItem's OutHash, never
// hardcoded), AddOutTx (append OutTxs + SetDone), save the voter, and emit the
// outbound event. It moves NO funds: the BTC already left the vault via the recovery.
//
// Strictly PAID-only. An item whose scheduled outbound still has an empty OutHash is
// still unpaid, is still in the LackSigning/reschedule path, and is LEFT UNTOUCHED —
// DevCodex's forward fix (restore the full ref when the item is re-scheduled) owns
// those. The OutHash guard keeps the two disjoint. Idempotent: an item whose OutHash
// is already in the voter's OutTxs is skipped (no second OutTx, no second event).
// Fail-soft: a missing voter, a missing paid item, or any error skips that item
// (logged loudly) without halting the upgrade; each item stages in its OWN cache
// context and commits individually, so one failure never rolls back another.
var solBtcPayoutRepair322Enabled = true

// solBtcPayoutRepair322Item is one stuck SOL->BTC payout to repair.
type solBtcPayoutRepair322Item struct {
	inHash string // SOL inbound tx id: the swap's in hash and the voter key.
	dest   string // BTC destination the recovery paid.
	sats   uint64 // BTC.BTC amount the recovery paid, in sats (1e8).
}

// solBtcPayoutRepair322Items are the four stuck payouts (all memo_len 80). The BTC
// OutHash is deliberately NOT listed here: it is read live from the paid TxOutItem,
// since the bifrost recovery set it after this code was written.
var solBtcPayoutRepair322Items = []solBtcPayoutRepair322Item{
	{inHash: "YLuT9ucrEHzVs9kGpnxWqfU5tiUyoE4NbVn6HCSdaykpynzkS9PiNxHNc712d6LnJ4oe4Kxo9gDxsJ73Pimq2xM", dest: "bc1q0tsjdc7zsj72fh40mreame6mmm84llpndpr7n5", sats: 1874292},
	{inHash: "665AC1Eykegia919xTjKPY42J9dzVBadaEVZ2mMm3YTPjumRUVGPxZwmNae8r1QHF9QWF1N564Qsob8PdbnpaKfe", dest: "bc1q7r8avzxhwxek4dm503ne0spc6edu9p9chlwf7a", sats: 858241},
	{inHash: "3HNWkh9yYu6RgJuXhksm4xNTAbLavFpS6MYoUs15kzgneJbe5jiPVo5bQPyWuAJZpu54moDRMoEzdAD6TdBLdFXn", dest: "bc1qgt96khvsy053upau94glyl06cx52v53x07x8hf", sats: 1174369},
	{inHash: "2RSsLAAKpD7qnKDeAQDvvYFtNY4Q4bhid6SfLd5VrcAMbyJTbNg5B9gEcTB6rJf8hXLKh3sNMfwgTkHvrpiYV5QD", dest: "bc1qwt5gsmqnqrhra4ecjs77560uup9hxk9l0p8uem", sats: 826522},
}

// runSolBtcPayoutRepair322 repairs each stuck SOL->BTC payout in its own cache
// context. It never returns an error, so it cannot fail the upgrade; a per-item
// guard failure soft-skips (logged) and leaves that item unchanged, and an item that
// is still unpaid or already repaired is left untouched.
func (m Migrator) runSolBtcPayoutRepair322(ctx cosmos.Context) {
	if !solBtcPayoutRepair322Enabled {
		ctx.Logger().Info("3.22.0 sol->btc payout repair skipped: disabled")
		return
	}
	for _, item := range solBtcPayoutRepair322Items {
		cacheCtx, commit := ctx.CacheContext()
		repaired, err := m.applySolBtcPayoutRepair322(cacheCtx, item)
		if err != nil {
			ctx.Logger().Error("3.22.0 sol->btc payout repair item skipped",
				"in_hash", item.inHash, "error", err)
			continue
		}
		if !repaired {
			ctx.Logger().Info("3.22.0 sol->btc payout repair item left untouched (unpaid or already repaired)",
				"in_hash", item.inHash)
			continue
		}
		commit()
		ctx.Logger().Info("3.22.0 sol->btc payout repair applied",
			"in_hash", item.inHash, "dest", item.dest, "sats", item.sats)
	}
}

// applySolBtcPayoutRepair322 repairs a single voter into ctx (a cache context the
// caller discards on error or when nothing was repaired). It returns repaired=true
// only when it rewrote the action memo, recorded the BTC out tx, saved the voter and
// emitted the event — all or none. repaired=false with a nil error means the item was
// legitimately left untouched (voter absent, still unpaid, or already repaired).
func (m Migrator) applySolBtcPayoutRepair322(ctx cosmos.Context, item solBtcPayoutRepair322Item) (bool, error) {
	k := m.mgr.Keeper()

	inTxID, err := common.NewTxID(item.inHash)
	if err != nil {
		return false, fmt.Errorf("sol->btc repair in hash: %w", err)
	}
	dest, err := common.NewAddress(item.dest)
	if err != nil {
		return false, fmt.Errorf("sol->btc repair dest: %w", err)
	}

	voter, err := k.GetObservedTxInVoter(ctx, inTxID)
	if err != nil {
		return false, fmt.Errorf("sol->btc repair get voter: %w", err)
	}
	// GetObservedTxInVoter returns a voter whose TxID is the queried hash even when
	// the record is absent, so TxID is not an existence signal. An absent voter (and
	// any voter with no scheduled actions) has no action to repair: skip, do not
	// hard-fail.
	if len(voter.Actions) == 0 {
		return false, nil
	}

	// (a) Find the PAID TxOutItem and read its OutHash from live state. found=false
	// means the outbound is still unpaid (OutHash empty) — leave it alone.
	outHash, found, err := findPaidSolBtcOutHash(ctx, k, voter, item.inHash, item.dest)
	if err != nil {
		return false, err
	}
	if !found {
		return false, nil
	}

	// Idempotency: already repaired (this OutHash is already recorded) — skip, so a
	// re-run adds no second OutTx and emits no second event.
	for _, o := range voter.OutTxs {
		if o.ID.Equals(outHash) {
			return false, nil
		}
	}

	coin := common.NewCoin(common.BTCAsset, cosmos.NewUint(item.sats))
	fullMemo := fmt.Sprintf("OUT:%s", inTxID.String())

	// (b) Rewrite the truncated memo on the matching BTC action so matchActionItem can
	// match. The action this payout fulfils is the one whose chain/dest/coin match the
	// BTC payment; its memo is the 80-byte truncated OUT:<in_hash>. Mutate by index so
	// the change lands on the stored voter, not a range copy.
	matched := false
	var fromAddr common.Address
	for i := range voter.Actions {
		a := voter.Actions[i]
		if !a.Chain.Equals(common.BTCChain) ||
			!common.EquivalentAddresses(a.Chain, a.ToAddress, dest) ||
			!a.Coin.Asset.Equals(common.BTCAsset) ||
			!a.Coin.Amount.Equal(coin.Amount) {
			continue
		}
		voter.Actions[i].Memo = fullMemo
		// Best-effort sender for the event: the action's vault BTC address. Not used
		// for matching (matchActionItem ignores the from address), so a failure here
		// only leaves the event's from field empty.
		if !voter.Actions[i].VaultPubKey.IsEmpty() {
			if addr, aErr := voter.Actions[i].VaultPubKey.GetAddress(common.BTCChain); aErr == nil {
				fromAddr = addr
			}
		}
		matched = true
		break
	}
	if !matched {
		return false, fmt.Errorf("sol->btc repair: no matching BTC action for dest %s amount %s", item.dest, coin.Amount)
	}

	// Build the BTC out tx as the signer's observation would have, ID = the OutHash
	// read live from the paid TxOutItem. NewTx derives Chain from the coin (BTC).
	outTx := common.NewTx(outHash, fromAddr, dest, common.NewCoins(coin), nil, fullMemo)

	// (c) Record the out tx (append OutTxs + SetDone voter.Txs) and emit the outbound
	// event, exactly as handler_common_outbound does. AddOutTx must now match after the
	// memo fix; if it does not, surface it rather than save a no-op.
	if !voter.AddOutTx(outTx) {
		return false, fmt.Errorf("sol->btc repair: out tx did not match action after memo fix (in_hash %s)", item.inHash)
	}
	if err := m.mgr.EventMgr().EmitEvent(ctx, NewEventOutbound(inTxID, outTx)); err != nil {
		return false, fmt.Errorf("sol->btc repair emit outbound event: %w", err)
	}
	k.SetObservedTxInVoter(ctx, voter)
	return true, nil
}

// findPaidSolBtcOutHash scans the bounded live outbound window for the PAID BTC
// outbound of inHash to dest, returning its OutHash. found=false means no paid copy
// exists (OutHash still empty => still unpaid => leave it alone). It returns an error
// only when the window could not be fully inspected (a TxOut decode failure), so the
// caller skips the item rather than act on a partial scan.
//
// The window mirrors the ZEC/TRON slash-refund scans: [h-max(TxOutDelayMax,
// SigningTransactionPeriod), h+TxOutDelayMax], with the floor pulled down to the
// voter's OutboundHeight/Height so a copy sitting at its original scheduled height
// (e.g. with signing halted, never rescheduled forward) is still reached. It scans
// the WHOLE window and takes the last paid copy seen; completed TxOut blocks are
// never pruned, so a paid copy stays readable at its height.
func findPaidSolBtcOutHash(ctx cosmos.Context, k keeper.Keeper, voter ObservedTxVoter, inHash, dest string) (common.TxID, bool, error) {
	back := k.GetConfigInt64(ctx, constants.TxOutDelayMax)
	if sp := k.GetConfigInt64(ctx, constants.SigningTransactionPeriod); sp > back {
		back = sp
	}
	lo := ctx.BlockHeight() - back
	if voter.OutboundHeight > 0 && voter.OutboundHeight < lo {
		lo = voter.OutboundHeight
	}
	if voter.Height > 0 && voter.Height < lo {
		lo = voter.Height
	}
	if lo < 1 {
		lo = 1
	}
	hi := ctx.BlockHeight() + k.GetConfigInt64(ctx, constants.TxOutDelayMax)

	var out common.TxID
	found := false
	for h := hi; h >= lo; h-- {
		blk, err := k.GetTxOut(ctx, h)
		if err != nil {
			// A decode failure means the window was only partially inspected; propagate
			// so the caller skips this item rather than treating the paid copy as absent.
			return common.BlankTxID, false, fmt.Errorf("sol->btc repair scan txout height %d: %w", h, err)
		}
		if blk == nil {
			continue
		}
		for i := range blk.TxArray {
			it := blk.TxArray[i]
			if it.InHash.String() != inHash || it.ToAddress.String() != dest {
				continue
			}
			if it.OutHash.IsEmpty() || it.OutHash.Equals(common.BlankTxID) {
				continue
			}
			out = it.OutHash
			found = true
		}
	}
	return out, found, nil
}
