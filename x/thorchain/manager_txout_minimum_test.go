package thorchain

import (
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	. "gopkg.in/check.v1"
)

func newMinimumOutboundTest(c *C, chain common.Chain) (handlerTestWrapper, *TxOutStorage, TxOutItem) {
	w := getHandlerTestWrapper(c, 100, true, false)
	asset := chain.GetGasAsset()
	pool := NewPool()
	pool.Asset = asset
	pool.Status = PoolAvailable
	pool.Decimals = 8
	pool.BalanceRune = cosmos.NewUint(100 * common.One)
	pool.BalanceAsset = cosmos.NewUint(100 * common.One)
	pool.LPUnits = cosmos.NewUint(100 * common.One)
	c.Assert(w.keeper.SetPool(w.ctx, pool), IsNil)
	c.Assert(w.keeper.SaveNetworkFee(w.ctx, chain, NewNetworkFee(chain, 1, 10_000)), IsNil)

	vault := GetRandomVault()
	vault.Chains = []string{chain.String()}
	vault.Coins = common.NewCoins(common.NewCoin(asset, cosmos.NewUint(10*common.One)))
	c.Assert(w.keeper.SetVault(w.ctx, vault), IsNil)
	pubkey := GetRandomPubKey()
	if chain.GetSigningAlgo() == common.SigningAlgoEd25519 {
		pubkey = GetRandomEd25519PubKey()
	}
	address, err := pubkey.GetAddress(chain)
	c.Assert(err, IsNil)
	item := TxOutItem{
		Chain:            chain,
		ToAddress:        address,
		InHash:           GetRandomTxHash(),
		VaultPubKey:      vault.PubKey,
		VaultPubKeyEddsa: vault.PubKeyEddsa,
		Coin:             common.NewCoin(asset, chain.ExistentialDeposit()),
	}
	voter := NewObservedTxVoter(item.InHash, nil)
	w.keeper.SetObservedTxInVoter(w.ctx, voter)
	store := newTxOutStorage(w.keeper, w.mgr.GetConstants(), w.mgr.EventMgr(), w.mgr.GasMgr())
	return w, store, item
}

func assertMinimumOutboundRejected(c *C, w handlerTestWrapper, store *TxOutStorage, item TxOutItem) {
	pool, err := w.keeper.GetPool(w.ctx, item.Coin.Asset)
	c.Assert(err, IsNil)
	voter, err := w.keeper.GetObservedTxInVoter(w.ctx, item.InHash)
	c.Assert(err, IsNil)
	withheld, err := w.keeper.GetOutboundFeeWithheldRune(w.ctx, item.Coin.Asset)
	c.Assert(err, IsNil)
	reserve := w.keeper.GetBalanceOfModule(w.ctx, ReserveName, common.RuneAsset().Native())
	asgard := w.keeper.GetBalanceOfModule(w.ctx, AsgardName, common.RuneAsset().Native())
	eventCount := len(w.ctx.EventManager().Events())

	ok, err := store.TryAddTxOutItem(w.ctx, w.mgr, item, cosmos.ZeroUint())
	c.Assert(err, Equals, ErrNotEnoughToPayFee)
	c.Check(ok, Equals, false)
	c.Check(w.keeper.GetPendingOutbounds(w.ctx, item.Coin.Asset), HasLen, 0)
	outputs, err := store.GetOutboundItems(w.ctx)
	c.Assert(err, IsNil)
	c.Check(outputs, HasLen, 0)
	afterPool, err := w.keeper.GetPool(w.ctx, item.Coin.Asset)
	c.Assert(err, IsNil)
	c.Check(afterPool, DeepEquals, pool)
	afterVoter, err := w.keeper.GetObservedTxInVoter(w.ctx, item.InHash)
	c.Assert(err, IsNil)
	c.Check(afterVoter, DeepEquals, voter)
	afterWithheld, err := w.keeper.GetOutboundFeeWithheldRune(w.ctx, item.Coin.Asset)
	c.Assert(err, IsNil)
	c.Check(afterWithheld.Equal(withheld), Equals, true)
	c.Check(w.keeper.GetBalanceOfModule(w.ctx, ReserveName, common.RuneAsset().Native()), DeepEquals, reserve)
	c.Check(w.keeper.GetBalanceOfModule(w.ctx, AsgardName, common.RuneAsset().Native()), DeepEquals, asgard)
	c.Check(w.ctx.EventManager().Events(), HasLen, eventCount)
}

func (s TxOutStoreSuite) TestOutboundMinimumFeeExemptPaths(c *C) {
	for _, chain := range []common.Chain{common.TAOChain, common.DOTChain} {
		for _, path := range []string{"migration", "ragnarok", "zero pool units", "inactive vault"} {
			for _, delta := range []int64{-1, 0, 1} {
				w, store, item := newMinimumOutboundTest(c, chain)
				item.Memo = NewRefundMemo(item.InHash).String()
				minimum := chain.ExistentialDeposit()
				amount := cosmos.NewUint(uint64(int64(minimum.Uint64()) + delta))
				item.Coin.Amount = amount
				switch path {
				case "migration":
					item.Memo = NewMigrateMemo(w.ctx.BlockHeight()).String()
				case "ragnarok":
					item.Memo = NewRagnarokMemo(w.ctx.BlockHeight()).String()
				case "zero pool units":
					pool, err := w.keeper.GetPool(w.ctx, item.Coin.Asset)
					c.Assert(err, IsNil)
					pool.LPUnits = cosmos.ZeroUint()
					c.Assert(w.keeper.SetPool(w.ctx, pool), IsNil)
				case "inactive vault":
					vault, err := w.keeper.GetVault(w.ctx, item.VaultPubKey)
					c.Assert(err, IsNil)
					vault.Status = InactiveVault
					c.Assert(w.keeper.SetVault(w.ctx, vault), IsNil)
				}
				if path == "ragnarok" || path == "inactive vault" {
					gas, _, err := w.mgr.GasMgr().GetGasDetails(w.ctx, chain)
					c.Assert(err, IsNil)
					item.Coin.Amount = item.Coin.Amount.Add(gas.Amount)
				}
				// Verify the boundary is reached after preparation, not rejected earlier.
				previewCtx, _ := w.ctx.CacheContext()
				prepared, _, err := store.prepareTxOutItem(previewCtx, item)
				c.Assert(err, IsNil, Commentf("%s %s delta=%d", chain, path, delta))
				c.Assert(prepared, HasLen, 1)
				c.Assert(prepared[0].Coin.Amount.Equal(amount), Equals, true)
				if delta < 0 {
					assertMinimumOutboundRejected(c, w, store, item)
					continue
				}
				ok, err := store.TryAddTxOutItem(w.ctx, w.mgr, item, cosmos.ZeroUint())
				c.Assert(err, IsNil, Commentf("%s %s delta=%d", chain, path, delta))
				c.Assert(ok, Equals, true)
				outputs := w.keeper.GetPendingOutbounds(w.ctx, item.Coin.Asset)
				c.Assert(outputs, HasLen, 1)
				c.Check(outputs[0].Coin.Amount.Equal(amount), Equals, true)
			}
		}
	}
}

func (s TxOutStoreSuite) TestOutboundMinimumRoundingRollsBackFees(c *C) {
	for _, chain := range []common.Chain{common.TAOChain, common.DOTChain} {
		w, store, item := newMinimumOutboundTest(c, chain)
		pool, err := w.keeper.GetPool(w.ctx, item.Coin.Asset)
		c.Assert(err, IsNil)
		pool.Decimals = 1
		c.Assert(w.keeper.SetPool(w.ctx, pool), IsNil)
		// Keep the fee nonzero even with one-decimal fee rounding.
		rate := chain.ThorchainToNativeGas(cosmos.NewUint(common.One)).Uint64()
		c.Assert(w.keeper.SaveNetworkFee(w.ctx, chain, NewNetworkFee(chain, 1, rate)), IsNil)
		fee, err := w.mgr.GasMgr().GetAssetOutboundFee(w.ctx, item.Coin.Asset, false)
		c.Assert(err, IsNil)
		c.Assert(fee.IsZero(), Equals, false)
		item.Coin.Amount = chain.DustThreshold().Add(fee)
		item.Memo = NewRefundMemo(item.InHash).String()

		previewCtx, _ := w.ctx.CacheContext()
		prepared, runeFee, err := store.prepareTxOutItem(previewCtx, item)
		c.Assert(err, IsNil)
		c.Assert(prepared, HasLen, 1)
		c.Assert(prepared[0].Coin.Amount.IsZero(), Equals, true)
		c.Assert(runeFee.IsZero(), Equals, false)
		assertMinimumOutboundRejected(c, w, store, item)
	}
}

func (s TxOutStoreSuite) TestOutboundMinimumRejectsWholeSplit(c *C) {
	for _, chain := range []common.Chain{common.TAOChain, common.DOTChain} {
		w, store, item := newMinimumOutboundTest(c, chain)
		minimum := chain.ExistentialDeposit()
		pool, err := w.keeper.GetPool(w.ctx, item.Coin.Asset)
		c.Assert(err, IsNil)
		pool.LPUnits = cosmos.ZeroUint()
		c.Assert(w.keeper.SetPool(w.ctx, pool), IsNil)
		// Tiny vaults must pass discovery's fee-affordability check.
		c.Assert(w.keeper.SaveNetworkFee(w.ctx, chain, NewNetworkFee(chain, 1, 1)), IsNil)
		gas, _, err := w.mgr.GasMgr().GetGasDetails(w.ctx, chain)
		c.Assert(err, IsNil)
		vault, err := w.keeper.GetVault(w.ctx, item.VaultPubKey)
		c.Assert(err, IsNil)
		for i, amount := range []cosmos.Uint{minimum.MulUint64(2), minimum.SubUint64(1)} {
			if i > 0 {
				vault = GetRandomVault()
				vault.Chains = []string{chain.String()}
			}
			vault.Coins = common.NewCoins(common.NewCoin(item.Coin.Asset, amount.Add(minimum).Add(gas.Amount)))
			c.Assert(w.keeper.SetVault(w.ctx, vault), IsNil)
		}
		item.VaultPubKey = common.EmptyPubKey
		item.VaultPubKeyEddsa = common.EmptyPubKey
		item.Coin.Amount = minimum.MulUint64(3).SubUint64(1)
		item.Memo = NewRefundMemo(item.InHash).String()
		previewCtx, _ := w.ctx.CacheContext()
		prepared, _, err := store.prepareTxOutItem(previewCtx, item)
		c.Assert(err, IsNil)
		c.Assert(prepared, HasLen, 2)
		c.Assert(prepared[0].Coin.Amount.Add(prepared[1].Coin.Amount).Equal(item.Coin.Amount), Equals, true)
		assertMinimumOutboundRejected(c, w, store, item)
	}
}

func (s TxOutStoreSuite) TestOutboundMinimumPreservesDustRule(c *C) {
	w, store, item := newMinimumOutboundTest(c, common.DOTChain)
	fee, err := w.mgr.GasMgr().GetAssetOutboundFee(w.ctx, item.Coin.Asset, false)
	c.Assert(err, IsNil)
	item.Memo = NewRefundMemo(item.InHash).String()
	item.Coin.Amount = common.DOTChain.ExistentialDeposit().Add(fee)
	assertMinimumOutboundRejected(c, w, store, item)
}

func (s TxOutStoreSuite) TestOutboundMinimumLeavesOtherChainsUnchanged(c *C) {
	w, store, item := newMinimumOutboundTest(c, common.XRPChain)
	item.Memo = NewMigrateMemo(w.ctx.BlockHeight()).String()
	item.Coin.Amount = common.XRPChain.ExistentialDeposit().SubUint64(1)
	ok, err := store.TryAddTxOutItem(w.ctx, w.mgr, item, cosmos.ZeroUint())
	c.Assert(err, IsNil)
	c.Assert(ok, Equals, true)
}
