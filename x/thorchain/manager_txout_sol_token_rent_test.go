package thorchain

import (
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

// A SOL token outbound charges the user the recipient token account's rent on top of
// network gas (the token outbound surcharge), because the vault creates that account
// and pays its rent from its SOL. The rent must then be reserved consistently: a vault
// is selected only when its SOL covers network gas plus rent, the outbound's MaxGas
// includes the rent (and keeps it when the queued outbound's gas is re-priced), and a
// pending token outbound holds that amount of its vault's SOL.

// solTokenRentSurcharge is the default token outbound surcharge on SOL: the rent of a
// token account (1,488,440 lamports at 5080 lamports per byte), in 1e8 units, rounded up.
const solTokenRentSurcharge = 148_900

type solTokenRentFixture struct {
	w       handlerTestWrapper
	usdc    common.Asset
	maxGas  common.Coin // network gas of a SOL outbound
	reserve cosmos.Uint // network gas plus rent
	store   *TxOutStorage
}

func newSOLTokenRentFixture(c *C) solTokenRentFixture {
	w := getHandlerTestWrapper(c, 1, true, true)
	usdc, err := common.NewAsset("SOL.USDC-EPJFWDD5AUFQSSQEM2QN1XZYBAPC8G4WEGGKZWYTDT1V")
	c.Assert(err, IsNil)
	c.Assert(w.keeper.SaveNetworkFee(w.ctx, common.SOLChain, NewNetworkFee(common.SOLChain, 1, 10_000)), IsNil)
	setOutboundFeeMultiplierMimirs(w.ctx, w.keeper)
	for _, asset := range []common.Asset{common.SOLAsset, usdc} {
		c.Assert(w.keeper.SetPool(w.ctx, Pool{
			BalanceRune:  cosmos.NewUint(100_000 * common.One),
			BalanceAsset: cosmos.NewUint(100_000 * common.One),
			LPUnits:      cosmos.NewUint(100_000 * common.One),
			Asset:        asset,
			Decimals:     6,
			Status:       PoolAvailable,
		}), IsNil)
	}
	maxGas, err := w.mgr.GasMgr().GetMaxGas(w.ctx, common.SOLChain)
	c.Assert(err, IsNil)
	c.Assert(maxGas.Asset.Equals(common.SOLAsset), Equals, true)
	return solTokenRentFixture{
		w:       w,
		usdc:    usdc,
		maxGas:  maxGas,
		reserve: maxGas.Amount.AddUint64(solTokenRentSurcharge),
		store:   newTxOutStorage(w.keeper, w.mgr.GetConstants(), w.mgr.EventMgr(), w.mgr.GasMgr()),
	}
}

// vault saves an active SOL vault holding usdc USDC and sol SOL (1e8 units).
func (f solTokenRentFixture) vault(c *C, usdc, sol uint64) Vault {
	v := GetRandomVault()
	v.Type = AsgardVault
	v.Status = ActiveVault
	v.Chains = append(v.Chains, common.SOLChain.String())
	v.PubKeyEddsa = GetRandomEd25519PubKey()
	v.Coins = common.Coins{
		common.NewCoin(f.usdc, cosmos.NewUint(usdc)),
		common.NewCoin(common.SOLAsset, cosmos.NewUint(sol)),
	}
	c.Assert(f.w.keeper.SetVault(f.w.ctx, v), IsNil)
	return v
}

// outbound is a 100 USDC outbound to a fresh SOL address, with its inbound voter.
func (f solTokenRentFixture) outbound(c *C) TxOutItem {
	inTxID := GetRandomTxHash()
	f.w.keeper.SetObservedTxInVoter(f.w.ctx, NewObservedTxVoter(inTxID, common.ObservedTxs{
		common.ObservedTx{
			Tx:             GetRandomTx(),
			Status:         common.Status_incomplete,
			BlockHeight:    1,
			Signers:        []string{f.w.activeNodeAccount.NodeAddress.String()},
			FinaliseHeight: 1,
		},
	}))
	to, err := GetRandomEd25519PubKey().GetAddress(common.SOLChain)
	c.Assert(err, IsNil)
	return TxOutItem{
		Chain:     common.SOLChain,
		ToAddress: to,
		InHash:    inTxID,
		Coin:      common.NewCoin(f.usdc, cosmos.NewUint(100*common.One)),
	}
}

// A vault whose SOL covers network gas but not the rent is passed over for one that
// covers both, and the scheduled outbound's MaxGas includes the rent.
func (s TxOutStoreSuite) TestSOLTokenOutboundReservesRent(c *C) {
	f := newSOLTokenRentFixture(c)
	// The rent-short vault holds more value, so the security sort tries it first.
	short := f.vault(c, 10_000*common.One, f.maxGas.Amount.Uint64()+1)
	enough := f.vault(c, 1_000*common.One, 10*common.One)

	outputs, _, err := f.store.prepareTxOutItem(f.w.ctx, f.outbound(c))
	c.Assert(err, IsNil)
	c.Assert(outputs, HasLen, 1)
	c.Check(outputs[0].VaultPubKey.Equals(short.PubKey), Equals, false,
		Commentf("vault with %s SOL cannot pay gas %s plus rent", short.GetCoin(common.SOLAsset).Amount, f.maxGas.Amount))
	c.Check(outputs[0].VaultPubKey.Equals(enough.PubKey), Equals, true)
	c.Assert(outputs[0].MaxGas, HasLen, 1)
	c.Check(outputs[0].MaxGas[0].Asset.Equals(common.SOLAsset), Equals, true)
	c.Check(outputs[0].MaxGas[0].Amount.String(), Equals, f.reserve.String())

	// SOL itself reserves only network gas.
	f = newSOLTokenRentFixture(c)
	f.vault(c, 0, 10*common.One)
	sol := f.outbound(c)
	sol.Coin = common.NewCoin(common.SOLAsset, cosmos.NewUint(common.One))
	outputs, _, err = f.store.prepareTxOutItem(f.w.ctx, sol)
	c.Assert(err, IsNil)
	c.Assert(outputs, HasLen, 1)
	c.Check(outputs[0].MaxGas[0].Amount.String(), Equals, f.maxGas.Amount.String())
}

// A pending token outbound holds its vault's SOL for gas plus rent: a vault that can
// pay for one token outbound is passed over for a second one.
func (s TxOutStoreSuite) TestSOLTokenOutboundPendingHoldsRent(c *C) {
	f := newSOLTokenRentFixture(c)
	// One outbound's worth of SOL, plus a little: enough for a second outbound's
	// network gas, not for its rent. It holds more value than the vault added below,
	// so the security sort tries it first.
	once := f.vault(c, 10_000*common.One, f.reserve.Uint64()+f.maxGas.Amount.Uint64()+1)

	first, err := f.store.TryAddTxOutItem(f.w.ctx, f.w.mgr, f.outbound(c), cosmos.ZeroUint())
	c.Assert(err, IsNil)
	c.Assert(first, Equals, true)
	items, err := f.store.GetOutboundItems(f.w.ctx)
	c.Assert(err, IsNil)
	c.Assert(items, HasLen, 1)
	c.Assert(items[0].VaultPubKey.Equals(once.PubKey), Equals, true)
	c.Check(items[0].MaxGas[0].Amount.String(), Equals, f.reserve.String())

	other := f.vault(c, 1_000*common.One, 10*common.One)
	outputs, _, err := f.store.prepareTxOutItem(f.w.ctx, f.outbound(c))
	c.Assert(err, IsNil)
	c.Assert(outputs, HasLen, 1)
	c.Check(outputs[0].VaultPubKey.Equals(other.PubKey), Equals, true,
		Commentf("the pending outbound already holds %s of the first vault's SOL", f.reserve))
}

// Re-pricing a queued token outbound's gas keeps the rent in its MaxGas.
func (s TxOutStoreSuite) TestSOLTokenOutboundRepricingKeepsRent(c *C) {
	f := newSOLTokenRentFixture(c)
	f.vault(c, 1_000*common.One, 10*common.One)
	ok, err := f.store.TryAddTxOutItem(f.w.ctx, f.w.mgr, f.outbound(c), cosmos.ZeroUint())
	c.Assert(err, IsNil)
	c.Assert(ok, Equals, true)

	// The network fee rises before the outbound is signed.
	c.Assert(f.w.keeper.SaveNetworkFee(f.w.ctx, common.SOLChain, NewNetworkFee(common.SOLChain, 1, 20_000)), IsNil)
	maxGas, err := f.w.mgr.GasMgr().GetMaxGas(f.w.ctx, common.SOLChain)
	c.Assert(err, IsNil)
	c.Assert(maxGas.Amount.GT(f.maxGas.Amount), Equals, true)
	c.Assert(f.store.EndBlock(f.w.ctx, f.w.mgr), IsNil)

	items, err := f.store.GetOutboundItems(f.w.ctx)
	c.Assert(err, IsNil)
	c.Assert(items, HasLen, 1)
	c.Check(items[0].MaxGas[0].Amount.String(), Equals, maxGas.Amount.AddUint64(solTokenRentSurcharge).String())
}
