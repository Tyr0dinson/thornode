//go:build mainnet

package thorchain

import (
	"fmt"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"
)

// Migrate20to21 reimburses tcso (the Secured Ops wallet) from the Reserve for
// three outlays made on users' behalf, replacing the earlier phantom-ATOM
// write-down. tcso funded the first two from its own holdings; the third (the
// LTC refund) was paid by Maya Secured Ops (mso), and tcso is reimbursed for it
// by Itz's decision of 2026-11-05. It makes three RUNE payments to tcso, applied
// atomically (all or none), and moves no vault, pool or Asgard balance.
//
//  1. GAIA vault solvency top-up. The vault was credited for five GAIA inbounds
//     it never received on the Cosmos Hub. The original plan wrote that phantom
//     ATOM off the vault and the GAIA.ATOM pool and paired Reserve RUNE to
//     restore solvency. Instead tcso topped up the vault's ATOM backing
//     directly, so its books now match its Hub balance and the write-down is
//     unnecessary — running it would strip real, fully-backed ATOM.
//  2. TRON TRX manual refund. tcso swapped RUNE to TRX to fund it, but only part
//     of the TRX reached the user; repay pro-rata to the TRX delivered — not the
//     full RUNE swapped, since tcso kept the undelivered remainder (see
//     tronRefundRune).
//  3. LTC manual refund, paid in ETH. Maya Secured Ops (mso) sent the ETH; tcso
//     is reimbursed for its RUNE value by Itz's decision of 2026-11-05.
//
// The GAIA and LTC amounts use one conversion method: the item's USD value at
// the moment it broadcast, divided by the CoinGecko RUNE/USD price at that
// moment. The TRON item needs no price conversion — it is RUNE, repaid pro-rata
// to the TRX the user actually received, not the full amount tcso swapped (see
// tronRefundRune).
const (
	gaiaReimburseRecipient = "thor1gkvvzfnkyqv8dfhk0593tfhgr03a3ndgjqdalf"
	// gaiaReimburseRune = GAIA top-up USD value / RUNE price at broadcast.
	//   USD value : $727 (the Itz-approved basis; 515.77 ATOM x CoinGecko
	//               ATOM/USD ~$1.41 is ~$727.24, taken as $727)
	//   RUNE price: CoinGecko RUNE/USD 0.6258631063781314 at 2026-11-04 14:20Z
	//               (nearest point to the 14:22:03Z broadcast)
	//   727 / 0.6258631063781314 = 1161.59587071 RUNE (reproduces the constant)
	gaiaReimburseRune = uint64(116_159_587_071) // 1,161.59587071 RUNE
	// gaiaReimburseTopUpTx is tcso's GAIA vault solvency top-up (Cosmos Hub).
	gaiaReimburseTopUpTx = "025C8E56AD41BE9F784648596CB1586C1531241FBEF480427A1EC18D04373480"

	// tronRefundRune repays in RUNE, with no price reconversion, only the
	// pro-rata portion of the RUNE tcso swapped that matches the TRX the user
	// actually received. tcso swapped 9,000 RUNE for 20,298.806458 TRX
	// (THOR swap tx 757BE4A5...290AE, input THOR.RUNE 900000000000) and sent
	// 19,357 TRX to the user (TRON tx beddc704...), keeping the 941.806458 TRX
	// remainder. Pro-rata to the delivered TRX, 1e8 integer math (truncated):
	//   9000 x 19357 / 20298.806458 = 8582.42578746 RUNE (reproduces the constant)
	tronRefundRune = uint64(858_242_578_746) // 8,582.42578746 RUNE
	// tronRefundSwapTx is tcso's RUNE->TRX funding swap (THORChain).
	tronRefundSwapTx = "757BE4A5F0E8BD9EC46EDB937E50D2B8AAB030BF250ECEC59636966A84B290AE"

	// ltcRefundRune = USD value of the refund ETH / RUNE price at broadcast. The
	// ETH was paid from Maya Secured Ops (mso, 0xA388...bC80), not tcso; tcso is
	// reimbursed by Itz's decision of 2026-11-05.
	//   ETH spent : 0.48201341 ETH (Ethereum tx 0xf47e9979...89b17, from mso
	//               0xA388...bC80 to the user 0x1f2119..., block 26040610,
	//               broadcast 2026-09-23 13:57Z)
	//   USD value : 0.48201341 x CoinGecko ETH/USD 2715.8856 = 1309.093279...
	//   RUNE price: CoinGecko RUNE/USD 0.6465834862093985 at 2026-09-23 14:00Z
	//               (nearest point to the broadcast)
	//   1309.093279... / 0.6465834862093985 = 2024.63147783 RUNE
	// Computed from the full-precision USD product; the rounded $1,309.0933 does
	// not reproduce the last digits.
	ltcRefundRune = uint64(202_463_147_783) // 2,024.63147783 RUNE
	// ltcRefundEthTx is mso's ETH refund payment to the user (Ethereum).
	ltcRefundEthTx = "0xf47e997968660a3ef9255b7c516f50a8105414aae74563e620174f79b8289b17"
)

// migrate20to21Mainnet pays tcso the three reimbursements atomically: it stages
// them in a cached context and commits only if all three succeed, so any failure
// writes nothing. Mainnet only; it logs loudly and never halts the upgrade.
func (m Migrator) migrate20to21Mainnet(ctx cosmos.Context) error {
	k := m.mgr.Keeper()
	cacheCtx, commit := ctx.CacheContext()
	payments := []struct {
		amount uint64
		source string
		what   string
	}{
		{gaiaReimburseRune, gaiaReimburseTopUpTx, "GAIA vault solvency top-up"},
		{tronRefundRune, tronRefundSwapTx, "TRON TRX manual refund funding swap"},
		{ltcRefundRune, ltcRefundEthTx, "LTC manual refund paid in ETH from mso"},
	}
	for _, p := range payments {
		if err := reimburseTcso(cacheCtx, k, gaiaReimburseRecipient, p.amount, p.source, p.what); err != nil {
			// All or none: abandon the cached context so no payment is written.
			ctx.Logger().Error("Migrate20to21 tcso reimbursement failed; no payment applied",
				"error", err, "failed_item", p.what)
			return nil
		}
	}
	commit()
	// Log the payments only after the batch commits, so the logs never claim a
	// reimbursement that an aborted (uncommitted) cache context rolled back.
	for _, p := range payments {
		ctx.Logger().Info("reimbursed tcso",
			"recipient", gaiaReimburseRecipient, "amount", cosmos.NewUint(p.amount).String(),
			"reason", p.what, "source", p.source)
	}
	ctx.Logger().Info("reimbursed tcso for the GAIA top-up and the TRON and LTC manual refunds",
		"recipient", gaiaReimburseRecipient,
		"total_rune", cosmos.NewUint(gaiaReimburseRune+tronRefundRune+ltcRefundRune).String())

	// 3.22.0 bond-slash refunds. Separate from the tcso batch above: each event
	// runs in its own cache context and soft-skips independently, so a drift or a
	// toggle on one never blocks the others or the tcso reimbursement.
	m.runSlashRefunds322(ctx)
	return nil
}

// reimburseTcso sends runeAmt (in 1e8 RUNE) from the Reserve to recipientStr as
// reimbursement for what, citing sourceRef. It returns an error (writing
// nothing) when the recipient is unparsable, the Reserve cannot cover the
// amount, or the send fails, so the caller can abandon the whole batch.
func reimburseTcso(ctx cosmos.Context, k keeper.Keeper, recipientStr string, runeAmt uint64, sourceRef, what string) error {
	recipient, err := cosmos.AccAddressFromBech32(recipientStr)
	if err != nil {
		return fmt.Errorf("parse tcso recipient %q for %s: %w", recipientStr, what, err)
	}

	amount := cosmos.NewUint(runeAmt)
	coins := common.NewCoins(common.NewCoin(common.RuneAsset(), amount))

	if bal := k.GetRuneBalanceOfModule(ctx, ReserveName); bal.LT(amount) {
		return fmt.Errorf("reserve balance %s below %s for %s (source %s)", bal.String(), amount.String(), what, sourceRef)
	}

	if err := k.SendFromModuleToAccount(ctx, ReserveName, recipient, coins); err != nil {
		return fmt.Errorf("reimburse tcso for %s (source %s): %w", what, sourceRef, err)
	}

	// Success is logged by the caller after the batch commits, not here: this send
	// lands in a cache context that is discarded if a later payment fails.
	return nil
}
