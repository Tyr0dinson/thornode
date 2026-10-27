package thorchain

import (
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

// zly (!5273): a pending token outbound's MaxGas holds its vault's SOL (network gas plus
// rent), but vault selection deducted only the pending outbounds of the SAME asset. An
// outbound of another token, or of SOL itself, could then be scheduled against SOL a
// pending token outbound already holds, although another funded vault exists.

// solVaultWith saves an active SOL vault holding coins.
func (f solTokenRentFixture) solVaultWith(c *C, coins ...common.Coin) Vault {
	v := GetRandomVault()
	v.Type = AsgardVault
	v.Status = ActiveVault
	v.Chains = append(v.Chains, common.SOLChain.String())
	v.PubKeyEddsa = GetRandomEd25519PubKey()
	v.Coins = coins
	c.Assert(f.w.keeper.SetVault(f.w.ctx, v), IsNil)
	return v
}

func (f solTokenRentFixture) usdt(c *C) common.Asset {
	usdt, err := common.NewAsset("SOL.USDT-ES9VMFRZACERMJFRF4H2FYD4KCONKY11MCCE8BENWNYB")
	c.Assert(err, IsNil)
	c.Assert(f.w.keeper.SetPool(f.w.ctx, Pool{
		BalanceRune:  cosmos.NewUint(100_000 * common.One),
		BalanceAsset: cosmos.NewUint(100_000 * common.One),
		LPUnits:      cosmos.NewUint(100_000 * common.One),
		Asset:        usdt,
		Decimals:     6,
		Status:       PoolAvailable,
	}), IsNil)
	return usdt
}

// A pending USDC outbound holds its vault's SOL for a USDT outbound scheduled next.
func (s TxOutStoreSuite) TestSOLPendingTokenOutboundHoldsSOLForAnotherToken(c *C) {
	f := newSOLTokenRentFixture(c)
	usdt := f.usdt(c)
	// One token outbound's worth of SOL, plus a little: enough for a second token
	// outbound's network gas, not for its rent. It holds more value than the vault added
	// below, so it is tried first.
	once := f.solVaultWith(c,
		common.NewCoin(f.usdc, cosmos.NewUint(10_000*common.One)),
		common.NewCoin(usdt, cosmos.NewUint(10_000*common.One)),
		common.NewCoin(common.SOLAsset, cosmos.NewUint(f.reserve.Uint64()+f.maxGas.Amount.Uint64()+1)),
	)

	ok, err := f.store.TryAddTxOutItem(f.w.ctx, f.w.mgr, f.outbound(c), cosmos.ZeroUint())
	c.Assert(err, IsNil)
	c.Assert(ok, Equals, true)
	items, err := f.store.GetOutboundItems(f.w.ctx)
	c.Assert(err, IsNil)
	c.Assert(items, HasLen, 1)
	c.Assert(items[0].VaultPubKey.Equals(once.PubKey), Equals, true)

	other := f.solVaultWith(c,
		common.NewCoin(f.usdc, cosmos.NewUint(1_000*common.One)),
		common.NewCoin(usdt, cosmos.NewUint(1_000*common.One)),
		common.NewCoin(common.SOLAsset, cosmos.NewUint(10*common.One)),
	)
	next := f.outbound(c)
	next.Coin = common.NewCoin(usdt, cosmos.NewUint(100*common.One))
	outputs, _, err := f.store.prepareTxOutItem(f.w.ctx, next)
	c.Assert(err, IsNil)
	c.Assert(outputs, HasLen, 1)
	c.Check(outputs[0].VaultPubKey.Equals(other.PubKey), Equals, true,
		Commentf("the pending USDC outbound already holds %s of the first vault's SOL", f.reserve))
}

// A pending USDC outbound holds its vault's SOL for a native SOL outbound too: once the
// SOL outbound is queued, the vault's pending outbounds must not commit more SOL (coin
// plus MaxGas) than it holds.
func (s TxOutStoreSuite) TestSOLPendingTokenOutboundHoldsSOLForNativeSOL(c *C) {
	f := newSOLTokenRentFixture(c)
	payout := uint64(common.One)
	fee, err := f.w.mgr.GasMgr().GetAssetOutboundFee(f.w.ctx, common.SOLAsset, false)
	c.Assert(err, IsNil)
	// Without the pending outbound's reservation this vault could pay the whole SOL
	// outbound and its gas; with it, it is short by more than the outbound fee (which
	// stays in the vault), though by less than the rent.
	shortfall := uint64(solTokenRentSurcharge / 2)
	c.Assert(shortfall > fee.Uint64(), Equals, true)
	solBalance := f.reserve.Uint64() + payout + f.maxGas.Amount.Uint64() - shortfall
	once := f.solVaultWith(c,
		common.NewCoin(f.usdc, cosmos.NewUint(10_000*common.One)),
		common.NewCoin(common.SOLAsset, cosmos.NewUint(solBalance)),
	)
	ok, err := f.store.TryAddTxOutItem(f.w.ctx, f.w.mgr, f.outbound(c), cosmos.ZeroUint())
	c.Assert(err, IsNil)
	c.Assert(ok, Equals, true)
	items, err := f.store.GetOutboundItems(f.w.ctx)
	c.Assert(err, IsNil)
	c.Assert(items, HasLen, 1)
	c.Assert(items[0].VaultPubKey.Equals(once.PubKey), Equals, true)

	f.solVaultWith(c,
		common.NewCoin(f.usdc, cosmos.NewUint(1_000*common.One)),
		common.NewCoin(common.SOLAsset, cosmos.NewUint(10*common.One)),
	)
	sol := f.outbound(c)
	sol.Coin = common.NewCoin(common.SOLAsset, cosmos.NewUint(payout))
	ok, err = f.store.TryAddTxOutItem(f.w.ctx, f.w.mgr, sol, cosmos.ZeroUint())
	c.Assert(err, IsNil)
	c.Assert(ok, Equals, true)

	items, err = f.store.GetOutboundItems(f.w.ctx)
	c.Assert(err, IsNil)
	committed := uint64(0)
	for _, item := range items {
		if !item.VaultPubKey.Equals(once.PubKey) {
			continue
		}
		if item.Coin.Asset.Equals(common.SOLAsset) {
			committed += item.Coin.Amount.Uint64()
		}
		committed += item.MaxGas.ToCoins().GetCoin(common.SOLAsset).Amount.Uint64()
	}
	c.Check(committed <= solBalance, Equals, true,
		Commentf("pending outbounds commit %d SOL of a vault holding %d", committed, solBalance))
}
