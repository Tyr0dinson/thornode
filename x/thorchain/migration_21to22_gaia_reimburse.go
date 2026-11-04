//go:build mainnet

package thorchain

import (
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"
)

// Reimburse the treasury for the GAIA vault solvency top-up. The treasury
// covered the GAIA vault's ATOM shortfall that had solvency-halted the chain;
// this step pays the treasury back the USD value of that top-up in RUNE from
// the Reserve.
//
// The RUNE amount is the USD value of the top-up priced in RUNE at the time the
// top-up transaction broadcast, hardcoded in 1e8 RUNE so every node applies the
// identical amount (deterministic). The step soft-skips (logs, never halts the
// upgrade) if the Reserve cannot cover it.
const (
	gaiaReimburseRecipient = "thor1gkvvzfnkyqv8dfhk0593tfhgr03a3ndgjqdalf"
	// gaiaReimburseRune is the reimbursement in 1e8 RUNE: the USD value of the
	// top-up divided by the RUNE price at the moment the top-up broadcast.
	//   USD value : $727 (515.77 ATOM x CoinGecko ATOM/USD $1.41)
	//   RUNE price: CoinGecko RUNE/USD 0.6258631063781314, sampled at
	//               2026-11-04T14:20:00Z (the nearest point to the 14:22:03Z
	//               broadcast; ~$0.625863 rounded)
	//   $727 / 0.6258631063781314 = 1,161.59587071 RUNE
	// The constant is computed from the full-precision price; the rounded
	// $0.625863 does not reproduce its last digits. Hardcoded in 1e8 so every
	// node applies the identical amount (deterministic).
	gaiaReimburseRune = uint64(116_159_587_071) // 1,161.59587071 RUNE
	// gaiaReimburseTopUpTx is the treasury's GAIA vault solvency top-up
	// transaction (Cosmos Hub).
	gaiaReimburseTopUpTx = "025C8E56AD41BE9F784648596CB1586C1531241FBEF480427A1EC18D04373480"
)

// migrate21to22Mainnet reimburses the treasury for the GAIA vault solvency
// top-up in RUNE from the Reserve. Mainnet only; soft-skips on insufficient
// Reserve rather than halting the upgrade.
func (m Migrator) migrate21to22Mainnet(ctx cosmos.Context) error {
	return reimburseGaiaVaultTopUp(ctx, m.mgr.Keeper(), gaiaReimburseRecipient, gaiaReimburseRune, gaiaReimburseTopUpTx)
}

// reimburseGaiaVaultTopUp sends runeAmt (in 1e8 RUNE) from the Reserve to
// recipientStr. It soft-skips (logs and returns nil, never halting the upgrade)
// if the recipient is unparseable or the Reserve cannot cover the amount, and
// moves nothing in either case.
func reimburseGaiaVaultTopUp(ctx cosmos.Context, k keeper.Keeper, recipientStr string, runeAmt uint64, topUpTx string) error {
	recipient, err := cosmos.AccAddressFromBech32(recipientStr)
	if err != nil {
		ctx.Logger().Error("GAIA vault solvency top-up reimbursement skipped: bad recipient",
			"error", err, "recipient", recipientStr)
		return nil
	}

	amount := cosmos.NewUint(runeAmt)
	coins := common.NewCoins(common.NewCoin(common.RuneAsset(), amount))

	if bal := k.GetRuneBalanceOfModule(ctx, ReserveName); bal.LT(amount) {
		ctx.Logger().Error("GAIA vault solvency top-up reimbursement skipped: Reserve below amount",
			"reserve", bal.String(), "amount", amount.String(), "topup_tx", topUpTx)
		return nil
	}

	if err := k.SendFromModuleToAccount(ctx, ReserveName, recipient, coins); err != nil {
		ctx.Logger().Error("fail to reimburse treasury for GAIA vault solvency top-up",
			"error", err, "recipient", recipient.String(),
			"amount", amount.String(), "topup_tx", topUpTx)
		return nil
	}

	ctx.Logger().Info("reimbursed treasury for GAIA vault solvency top-up",
		"recipient", recipient.String(), "amount", amount.String(), "topup_tx", topUpTx)
	return nil
}
