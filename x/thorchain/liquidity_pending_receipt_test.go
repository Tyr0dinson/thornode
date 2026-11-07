package thorchain

import (
	"fmt"

	"github.com/cometbft/cometbft/crypto/secp256k1"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

type ErrataPendingLiquiditySuite struct{}

var _ = Suite(&ErrataPendingLiquiditySuite{})

type pendingLiquidityFixture struct {
	ctx     cosmos.Context
	mgr     *Mgrs
	asset   common.Asset
	vault   Vault
	rune    common.Address
	refund  common.Address
	nodes   NodeAccounts
	keys    []secp256k1.PrivKey
	initial Pool
}

// newPendingLiquidityFixture uses the real keeper and observation/ADD handlers.
// Only the pre-existing committed pool and vault balances are fixtures.
func newPendingLiquidityFixture(c *C, asset common.Asset) pendingLiquidityFixture {
	ctx, mgr := setupManagerForTest(c)
	ctx = ctx.WithBlockHeight(100)
	f := pendingLiquidityFixture{ctx: ctx, mgr: mgr, asset: asset, rune: GetRandomTHORAddress()}
	for i := 0; i < 4; i++ {
		na, key := nodeWithPrivKey(c, mgr.Keeper(), ctx)
		f.nodes = append(f.nodes, na)
		f.keys = append(f.keys, key)
	}
	c.Assert(mgr.ValidatorMgr().BeginBlock(ctx, mgr, nil), IsNil)
	f.vault = NewVault(1, ActiveVault, AsgardVault, GetRandomPubKey(), common.Chains{asset.Chain}.Strings(), nil)
	f.vault.PubKeyFrost = GetRandomEd25519PubKey()
	f.vault.Coins = common.NewCoins(common.NewCoin(asset, cosmos.NewUint(1000*common.One)))
	c.Assert(mgr.Keeper().SetVault(ctx, f.vault), IsNil)
	f.refund = GetRandomBTCAddress()
	if asset.Chain.IsEVM() {
		f.refund = GetRandomETHAddress()
	}
	if asset.Equals(common.XMRAsset) {
		var err error
		f.refund, err = GetRandomEd25519PubKey().GetAddress(common.XMRChain)
		c.Assert(err, IsNil)
	}
	f.initial = NewPool()
	f.initial.Asset = asset
	f.initial.Status = PoolAvailable
	f.initial.Decimals = 8
	f.initial.BalanceRune = cosmos.NewUint(1000 * common.One)
	f.initial.BalanceAsset = cosmos.NewUint(1000 * common.One)
	f.initial.LPUnits = cosmos.NewUint(1000 * common.One)
	c.Assert(mgr.Keeper().SetPool(ctx, f.initial), IsNil)
	return f
}

// stage reaches non-final quorum, then final quorum, including custody credit
// and normal memo resolution. No pending LP or inbound voter is seeded by hand.
func (f pendingLiquidityFixture) stage(c *C, runeAddress common.Address, amount uint64) common.Tx {
	return f.stageMemo(c, fmt.Sprintf("+:%s:%s", f.asset, runeAddress), amount)
}

func (f pendingLiquidityFixture) stageMemo(c *C, memo string, amount uint64) common.Tx {
	tx := f.observeMemo(c, memo, amount)
	voter, err := f.mgr.Keeper().GetObservedTxInVoter(f.ctx, tx.ID)
	c.Assert(err, IsNil)
	c.Assert(voter.Tx.Status, Equals, common.Status_done)
	return tx
}

// observeMemo also permits refunded ADDs, whose outbound is still outstanding.
func (f pendingLiquidityFixture) observeMemo(c *C, memo string, amount uint64) common.Tx {
	to, err := f.vault.GetAddress(f.asset.Chain)
	c.Assert(err, IsNil)
	tx := common.NewTx(GetRandomTxHash(), f.refund, to,
		common.NewCoins(common.NewCoin(f.asset, cosmos.NewUint(amount*common.One))),
		common.Gas{common.NewCoin(f.asset.Chain.GetGasAsset(), cosmos.NewUint(1))},
		memo)
	if f.asset.Equals(common.XMRAsset) {
		tx.FromAddress, err = common.NewAddress(common.XMRUnknownSenderAddress(tx.ID.String(), to.String()))
		c.Assert(err, IsNil)
	}
	handler := NewObservedTxInHandler(f.mgr)
	observedKey, err := f.vault.AlgoPubKey(f.asset.Chain)
	c.Assert(err, IsNil)
	for _, finalHeight := range []int64{20, 10} {
		observed := NewObservedTx(tx, 10, observedKey, finalHeight)
		if f.asset.Equals(common.XMRAsset) {
			observed.XmrMinedHeight = 10
		}
		for _, na := range f.nodes[:3] {
			_, err = handler.Run(f.ctx, NewMsgObservedTxIn(ObservedTxs{observed}, na.NodeAddress))
			c.Assert(err, IsNil)
		}
		voter, voterErr := f.mgr.Keeper().GetObservedTxInVoter(f.ctx, tx.ID)
		c.Assert(voterErr, IsNil)
		c.Assert(voter.UpdatedVault, Equals, finalHeight == 10)
		if finalHeight == 10 {
			c.Assert(voter.FinalisedHeight, Equals, f.ctx.BlockHeight())
		}
	}
	return tx
}

func (f pendingLiquidityFixture) tryReverse(c *C, tx common.Tx) error {
	er := makeErrataTx(tx.ID, tx.Chain)
	quo := &common.QuorumErrataTx{ErrataTx: er}
	for _, key := range f.keys[:3] {
		quo.Attestations = append(quo.Attestations, attestErrataTx(c, key, er))
	}
	// Model the Cosmos transaction cache: a failed reversal must not leak any
	// voter, custody, accounting or event writes into the parent context.
	ctx, commit := f.ctx.CacheContext()
	_, err := NewErrataTxQuorumHandler(f.mgr).Run(ctx, types.NewMsgErrataTxQuorum(quo, f.nodes[0].NodeAddress))
	if err != nil {
		return err
	}
	commit()
	return nil
}

func (f pendingLiquidityFixture) reverse(c *C, tx common.Tx) {
	c.Assert(f.tryReverse(c, tx), IsNil)
	voter, err := f.mgr.Keeper().GetObservedTxInVoter(f.ctx, tx.ID)
	c.Assert(err, IsNil)
	c.Assert(voter.Reverted, Equals, true)
	errata, err := f.mgr.Keeper().GetErrataTxVoter(f.ctx, tx.ID, tx.Chain)
	c.Assert(err, IsNil)
	c.Assert(errata.BlockHeight, Equals, f.ctx.BlockHeight())
}

// TestRepeatedAssetDepositsAccumulate proves the current admission contract and
// loss of the first contribution's identity in the LP record, on both chains.
func (ErrataPendingLiquiditySuite) TestRepeatedAssetDepositsAccumulate(c *C) {
	for _, asset := range []common.Asset{common.XMRAsset, common.BTCAsset} {
		f := newPendingLiquidityFixture(c, asset)
		first := f.stage(c, f.rune, 10)
		second := f.stage(c, f.rune, 20)
		lp, err := f.mgr.Keeper().GetLiquidityProvider(f.ctx, asset, f.rune)
		c.Assert(err, IsNil)
		c.Check(lp.PendingAsset.String(), Equals, "3000000000")
		c.Check(lp.PendingTxID, Equals, second.ID)
		c.Check(lp.PendingTxID.Equals(first.ID), Equals, false)
		pool, err := f.mgr.Keeper().GetPool(f.ctx, asset)
		c.Assert(err, IsNil)
		c.Check(pool.PendingInboundAsset.String(), Equals, "3000000000")
		c.Check(pool.BalanceAsset.Equal(f.initial.BalanceAsset), Equals, true)
		c.Check(pool.LPUnits.Equal(f.initial.LPUnits), Equals, true)
	}
}

func (ErrataPendingLiquiditySuite) TestPendingErrataPreservesCommittedLiquidity(c *C) {
	for _, asset := range []common.Asset{common.XMRAsset, common.BTCAsset} {
		for _, orphanIndex := range []int{-1, 0, 1} {
			c.Logf("asset=%s orphanIndex=%d (-1 is a single deposit)", asset, orphanIndex)
			f := newPendingLiquidityFixture(c, asset)
			otherAddress := GetRandomTHORAddress()
			f.stage(c, otherAddress, 7)
			otherBefore, err := f.mgr.Keeper().GetLiquidityProvider(f.ctx, asset, otherAddress)
			c.Assert(err, IsNil)
			txs := []common.Tx{f.stage(c, f.rune, 10)}
			orphan := txs[0]
			remaining := cosmos.ZeroUint()
			if orphanIndex >= 0 {
				txs = append(txs, f.stage(c, f.rune, 20))
				orphan = txs[orphanIndex]
				remaining = txs[1-orphanIndex].Coins[0].Amount
			}
			f.ctx = f.ctx.WithEventManager(cosmos.NewEventManager())
			f.reverse(c, orphan)
			pool, err := f.mgr.Keeper().GetPool(f.ctx, asset)
			c.Assert(err, IsNil)
			lp, err := f.mgr.Keeper().GetLiquidityProvider(f.ctx, asset, f.rune)
			c.Assert(err, IsNil)
			vault, err := f.mgr.Keeper().GetVault(f.ctx, f.vault.PubKey)
			c.Assert(err, IsNil)
			otherAfter, err := f.mgr.Keeper().GetLiquidityProvider(f.ctx, asset, otherAddress)
			c.Assert(err, IsNil)
			c.Logf("after errata: custody=%s committedAsset=%s poolPending=%s lpPending=%s orphanAmount=%s remaining=%s", vault.GetCoin(asset).Amount, pool.BalanceAsset, pool.PendingInboundAsset, lp.PendingAsset, orphan.Coins[0].Amount, remaining)
			miskeyed, err := f.mgr.Keeper().GetLiquidityProvider(f.ctx, asset, orphan.FromAddress)
			c.Assert(err, IsNil)
			c.Check(miskeyed.LastAddHeight, Equals, int64(0), Commentf("errata must not persist a fabricated LP under the observed sender"))
			c.Check(vault.GetCoin(asset).Amount.Equal(f.initial.BalanceAsset.Add(cosmos.NewUint(7*common.One)).Add(remaining)), Equals, true)
			c.Check(pool.BalanceAsset.Equal(f.initial.BalanceAsset), Equals, true, Commentf("pending errata must not debit committed asset depth"))
			c.Check(pool.BalanceRune.Equal(f.initial.BalanceRune), Equals, true)
			c.Check(pool.LPUnits.Equal(f.initial.LPUnits), Equals, true)
			c.Check(lp.Units.IsZero(), Equals, true)
			c.Check(pool.PendingInboundAsset.Equal(cosmos.NewUint(7*common.One).Add(remaining)), Equals, true)
			c.Check(lp.PendingAsset.Equal(remaining), Equals, true)
			if orphanIndex < 0 || orphanIndex == 1 {
				c.Check(lp.PendingTxID.IsEmpty(), Equals, true)
			} else {
				c.Check(lp.PendingTxID, Equals, txs[1-orphanIndex].ID)
			}
			c.Check(otherAfter, DeepEquals, otherBefore)
			pendingEvents, committedEvents := 0, 0
			for _, event := range f.ctx.EventManager().Events() {
				if isPendingLiquidityWithdrawEvent(event) {
					pendingEvents++
					for _, attr := range event.Attributes {
						switch attr.Key {
						case "asset_amount":
							c.Check(attr.Value, Equals, orphan.Coins[0].Amount.String())
						case "rune_amount":
							c.Check(attr.Value, Equals, "0", Commentf("asset-only errata must not withdraw pending rune"))
						case asset.Chain.String() + "_txid":
							c.Check(attr.Value, Equals, orphan.ID.String())
						}
					}
				}
				if event.Type == "errata" {
					committedEvents++
					for _, attr := range event.Attributes {
						if attr.Key == "asset_amt" {
							c.Check(attr.Value, Equals, "0", Commentf("must not report a committed pool debit"))
						}
					}
				}
			}

			c.Check(pendingEvents, Equals, 1)
			c.Check(committedEvents, Equals, 1)

			// Recreate the handler and reload durable voters on duplicate delivery.
			f.reverse(c, orphan)
			afterDuplicate, err := f.mgr.Keeper().GetPool(f.ctx, asset)
			c.Assert(err, IsNil)
			c.Check(afterDuplicate, DeepEquals, pool)

			// Exercise withdrawal calculation in a disposable transaction: only
			// the surviving contribution may remain withdrawable.
			withdrawCtx, _ := f.ctx.CacheContext()
			withdrawMsg := NewMsgWithdrawLiquidity(GetRandomTx(), f.rune, cosmos.NewUint(MaxWithdrawBasisPoints), asset, common.EmptyAsset, f.nodes[0].NodeAddress)
			_, withdrawAsset, _, _, withdrawErr := withdraw(withdrawCtx, *withdrawMsg, f.mgr)
			c.Logf("withdrawal calculation: asset=%s expected=%s err=%v", withdrawAsset, remaining, withdrawErr)
			if withdrawErr == nil {
				c.Check(withdrawAsset.Equal(remaining), Equals, true, Commentf("withdrawal must exclude the orphan"))
			} else {
				c.Check(remaining.IsZero(), Equals, true)
			}

			// A later RUNE half goes through normal memo conversion and ADD.
			completeCtx, _ := f.ctx.CacheContext()
			runeTx := common.NewTx(GetRandomTxHash(), f.rune, GetRandomTHORAddress(), common.NewCoins(common.NewCoin(common.RuneAsset(), cosmos.NewUint(10*common.One))), common.Gas{common.NewCoin(common.RuneAsset(), cosmos.NewUint(1))}, fmt.Sprintf("+:%s:%s", asset, f.refund))
			memo, err := ParseMemoWithTHORNames(completeCtx, f.mgr.Keeper(), runeTx.Memo)
			c.Assert(err, IsNil)
			addMemo, ok := memo.(AddLiquidityMemo)
			c.Assert(ok, Equals, true)
			msg, err := getMsgAddLiquidityFromMemo(completeCtx, addMemo, NewObservedTx(runeTx, 10, f.vault.PubKey, 10), f.nodes[0].NodeAddress)
			c.Assert(err, IsNil)
			_, err = NewInternalHandler(f.mgr)(completeCtx, msg)
			c.Assert(err, IsNil)
			completedPool, err := f.mgr.Keeper().GetPool(completeCtx, asset)
			c.Assert(err, IsNil)
			c.Logf("completion: committedAsset=%s expected=%s", completedPool.BalanceAsset, pool.BalanceAsset.Add(remaining))
			c.Check(completedPool.BalanceAsset.Equal(pool.BalanceAsset.Add(remaining)), Equals, true, Commentf("later completion must exclude the orphan"))
		}
	}
}

// Storage existence is tested independently of the keeper's missing-LP default.
func liquidityProviderStored(c *C, k keeper.Keeper, ctx cosmos.Context, asset common.Asset, address common.Address) bool {
	iterator := k.GetLiquidityProviderIterator(ctx, asset)
	defer iterator.Close()
	for ; iterator.Valid(); iterator.Next() {
		var lp LiquidityProvider
		c.Assert(k.Cdc().Unmarshal(iterator.Value(), &lp), IsNil)
		if lp.GetAddress().Equals(address) {
			return true
		}
	}
	return false
}

func (ErrataPendingLiquiditySuite) TestRemoveRootMiddleTailAndAppend(c *C) {
	for _, order := range [][]int{{0, 2, 1, 3}, {2, 3, 0, 1}, {3, 1, 2, 0}} {
		f := newPendingLiquidityFixture(c, common.BTCAsset)
		var txs []common.Tx
		for i := 0; i < 4; i++ {
			txs = append(txs, f.stage(c, f.rune, uint64(i+1)*10))
		}
		remaining := cosmos.NewUint(100 * common.One)
		for _, i := range order {
			f.reverse(c, txs[i])
			remaining = remaining.Sub(txs[i].Coins[0].Amount)
			lp, err := f.mgr.Keeper().GetLiquidityProvider(f.ctx, f.asset, f.rune)
			c.Assert(err, IsNil)
			c.Assert(lp.PendingAsset.Equal(remaining), Equals, true)
			if remaining.IsZero() {
				c.Assert(lp.PendingBatch.IsEmpty(), Equals, true)
			} else {
				c.Assert(lp.PendingBatch, Equals, txs[0].ID)
			}
		}
		// A fully reverted lifecycle must not poison a new pending batch.
		fresh := f.stage(c, f.rune, 5)
		f.reverse(c, fresh)
	}
	// The original root remains the batch marker after its own reversal.
	f := newPendingLiquidityFixture(c, common.XMRAsset)
	first := f.stage(c, f.rune, 10)
	second := f.stage(c, f.rune, 20)
	f.reverse(c, first)
	third := f.stage(c, f.rune, 30)
	f.reverse(c, second)
	f.reverse(c, third)
}

func completePendingLiquidity(c *C, f pendingLiquidityFixture) {
	tx := common.NewTx(GetRandomTxHash(), f.rune, GetRandomTHORAddress(), common.NewCoins(common.NewCoin(common.RuneAsset(), cosmos.NewUint(10*common.One))), nil, fmt.Sprintf("+:%s:%s", f.asset, f.refund))
	memo, err := ParseMemoWithTHORNames(f.ctx, f.mgr.Keeper(), tx.Memo)
	c.Assert(err, IsNil)
	add, ok := memo.(AddLiquidityMemo)
	c.Assert(ok, Equals, true)
	msg, err := getMsgAddLiquidityFromMemo(f.ctx, add, NewObservedTx(tx, 10, f.vault.PubKey, 10), f.nodes[0].NodeAddress)
	c.Assert(err, IsNil)
	_, err = NewInternalHandler(f.mgr)(f.ctx, msg)
	c.Assert(err, IsNil)
}

func (ErrataPendingLiquiditySuite) TestCompletedBatchDoesNotConsumeNewPending(c *C) {
	f := newPendingLiquidityFixture(c, common.BTCAsset)
	old := f.stage(c, f.rune, 10)
	f.stage(c, f.rune, 20)
	completePendingLiquidity(c, f)
	fresh := f.stage(c, f.rune, 17)
	lpBefore, err := f.mgr.Keeper().GetLiquidityProvider(f.ctx, f.asset, f.rune)
	c.Assert(err, IsNil)
	before, err := f.mgr.Keeper().GetPool(f.ctx, f.asset)
	c.Assert(err, IsNil)
	f.reverse(c, old)
	lp, err := f.mgr.Keeper().GetLiquidityProvider(f.ctx, f.asset, f.rune)
	c.Assert(err, IsNil)
	pool, err := f.mgr.Keeper().GetPool(f.ctx, f.asset)
	c.Assert(err, IsNil)
	c.Assert(lp.PendingTxID, Equals, fresh.ID)
	c.Assert(lp.PendingAsset.Equal(cosmos.NewUint(17*common.One)), Equals, true)
	c.Assert(pool.PendingInboundAsset.Equal(lp.PendingAsset), Equals, true)
	c.Assert(pool.BalanceAsset.Equal(before.BalanceAsset.Sub(old.Coins[0].Amount)), Equals, true)
	c.Assert(lp, DeepEquals, lpBefore)
	c.Assert(pool.LPUnits.Equal(before.LPUnits), Equals, true)
	f.reverse(c, fresh)
}

func (ErrataPendingLiquiditySuite) TestRuneFirstCompletion(c *C) {
	f := newPendingLiquidityFixture(c, common.BTCAsset)
	completePendingLiquidity(c, f) // no asset yet: stages the RUNE half
	before, err := f.mgr.Keeper().GetLiquidityProvider(f.ctx, f.asset, f.rune)
	c.Assert(err, IsNil)
	c.Assert(before.PendingRune.Equal(cosmos.NewUint(10*common.One)), Equals, true)
	tx := f.stage(c, f.rune, 10)
	f.reverse(c, tx)
	lp, err := f.mgr.Keeper().GetLiquidityProvider(f.ctx, f.asset, f.rune)
	c.Assert(err, IsNil)
	c.Assert(lp.PendingRune.IsZero(), Equals, true)
	c.Assert(lp.PendingAsset.IsZero(), Equals, true)
	c.Assert(lp.PendingTxID.IsEmpty(), Equals, true)
}

func (ErrataPendingLiquiditySuite) TestAffiliateOwnership(c *C) {
	for _, self := range []bool{false, true} {
		f := newPendingLiquidityFixture(c, common.BTCAsset)
		affiliate := GetRandomTHORAddress()
		if self {
			affiliate = f.rune
		}
		tx := f.stageMemo(c, fmt.Sprintf("+:%s:%s:%s:1000", f.asset, f.rune, affiliate), 10)
		voter, err := f.mgr.Keeper().GetObservedTxInVoter(f.ctx, tx.ID)
		c.Assert(err, IsNil)
		c.Assert(voter.PendingReceipt, NotNil)
		c.Assert(voter.PendingReceipt.Address, Equals, f.rune)
		c.Assert(voter.PendingReceipt.AssetAmount.String(), Equals, "900000000")
		lpBefore, err := f.mgr.Keeper().GetLiquidityProvider(f.ctx, f.asset, f.rune)
		c.Assert(err, IsNil)
		poolBefore, err := f.mgr.Keeper().GetPool(f.ctx, f.asset)
		c.Assert(err, IsNil)
		f.reverse(c, tx)
		pool, err := f.mgr.Keeper().GetPool(f.ctx, f.asset)
		c.Assert(err, IsNil)
		c.Assert(pool.PendingInboundAsset.IsZero(), Equals, true)
		c.Assert(pool.BalanceAsset.Equal(f.initial.BalanceAsset), Equals, true)
		lp, err := f.mgr.Keeper().GetLiquidityProvider(f.ctx, f.asset, f.rune)
		c.Assert(err, IsNil)
		c.Assert(lp.PendingAsset.IsZero(), Equals, true)
		c.Assert(lp.Units.Equal(lpBefore.Units), Equals, true)
		c.Assert(pool.LPUnits.Equal(poolBefore.LPUnits), Equals, true)
	}
}

func (ErrataPendingLiquiditySuite) TestDuplicateReceiptSecurityEvent(c *C) {
	f := newPendingLiquidityFixture(c, common.BTCAsset)
	tx := f.stage(c, f.rune, 10)
	lp, err := f.mgr.Keeper().GetLiquidityProvider(f.ctx, f.asset, f.rune)
	c.Assert(err, IsNil)
	ctx := f.ctx.WithEventManager(cosmos.NewEventManager())
	// A second execution under the same inbound ID must drop, not overwrite, the proof.
	recordPendingLiquidity(ctx, f.mgr, &lp, tx.ID, tx.Coins[0].Amount)
	voter, err := f.mgr.Keeper().GetObservedTxInVoter(ctx, tx.ID)
	c.Assert(err, IsNil)
	c.Assert(voter.PendingReceipt, IsNil)
	found := false
	for _, event := range ctx.EventManager().Events() {
		for _, attr := range event.Attributes {
			if event.Type == "security" && attr.Key == "msg" && attr.Value == "duplicate pending liquidity receipt; using errata fallback" {
				found = true
			}
		}
	}
	c.Assert(found, Equals, true)
}

func (ErrataPendingLiquiditySuite) TestHistoricalNameOwnership(c *C) {
	f := newPendingLiquidityFixture(c, common.BTCAsset)
	name := types.NewTHORName("liquidity-owner", 10000, []types.THORNameAlias{{Chain: common.THORChain, Address: f.rune}})
	f.mgr.Keeper().SetTHORName(f.ctx, name)
	tx := f.stageMemo(c, "+:BTC.BTC:liquidity-owner.THOR", 10)
	name.SetAlias(common.THORChain, GetRandomTHORAddress())
	f.mgr.Keeper().SetTHORName(f.ctx, name)
	// Expiry must not prevent reversal either.
	c.Assert(f.mgr.Keeper().DeleteTHORName(f.ctx, name.Name), IsNil)
	f.reverse(c, tx)
	c.Assert(liquidityProviderStored(c, f.mgr.Keeper(), f.ctx, f.asset, name.GetAlias(common.THORChain)), Equals, false)
	lp, err := f.mgr.Keeper().GetLiquidityProvider(f.ctx, f.asset, f.rune)
	c.Assert(err, IsNil)
	c.Assert(lp.PendingAsset.IsZero(), Equals, true)
}

func (ErrataPendingLiquiditySuite) TestInvalidReceiptUsesAtomicFallback(c *C) {
	for _, failure := range []string{"missing", "owner", "empty-owner", "amount", "zero", "nil", "batch", "empty-batch", "both-empty", "pool-underflow", "lp-underflow", "non-add", "legacy", "asset", "nil-pending"} {
		c.Logf("failure=%s", failure)
		f := newPendingLiquidityFixture(c, common.BTCAsset)
		tx := f.stage(c, f.rune, 10)
		if failure == "amount" {
			// LP/pool totals cover 11 BTC, but this transaction contributed only 10.
			f.stage(c, f.rune, 20)
		}
		vaultBefore, vaultErr := f.mgr.Keeper().GetVault(f.ctx, f.vault.PubKey)
		c.Assert(vaultErr, IsNil)
		voter, err := f.mgr.Keeper().GetObservedTxInVoter(f.ctx, tx.ID)
		c.Assert(err, IsNil)
		pool, err := f.mgr.Keeper().GetPool(f.ctx, f.asset)
		c.Assert(err, IsNil)
		lp, err := f.mgr.Keeper().GetLiquidityProvider(f.ctx, f.asset, f.rune)
		c.Assert(err, IsNil)
		switch failure {
		case "missing":
			f.mgr.Keeper().RemoveLiquidityProvider(f.ctx, lp)
		case "owner":
			voter.PendingReceipt.Address = GetRandomTHORAddress()
		case "empty-owner":
			voter.PendingReceipt.Address = ""
		case "amount":
			voter.PendingReceipt.AssetAmount = cosmos.NewUint(11 * common.One)
		case "zero":
			voter.PendingReceipt.AssetAmount = cosmos.ZeroUint()
		case "nil":
			voter.PendingReceipt.AssetAmount = cosmos.Uint{}
		case "batch":
			voter.PendingReceipt.BatchID = GetRandomTxHash()
		case "empty-batch":
			voter.PendingReceipt.BatchID = ""
		case "both-empty":
			voter.PendingReceipt.BatchID = ""
			lp.PendingBatch = ""
			f.mgr.Keeper().SetLiquidityProvider(f.ctx, lp)
		case "pool-underflow":
			pool.PendingInboundAsset = cosmos.OneUint()
			c.Assert(f.mgr.Keeper().SetPool(f.ctx, pool), IsNil)
		case "lp-underflow":
			lp.PendingAsset = cosmos.OneUint()
			f.mgr.Keeper().SetLiquidityProvider(f.ctx, lp)
		case "non-add":
			voter.Tx.Tx.Memo = "DONATE:BTC.BTC"
		case "legacy":
			voter.PendingReceipt = nil
		case "asset":
			voter.PendingReceipt.Asset = common.ETHAsset
		case "nil-pending":
			lp.PendingAsset = cosmos.Uint{}
			f.mgr.Keeper().SetLiquidityProvider(f.ctx, lp)
		}
		f.mgr.Keeper().SetObservedTxInVoter(f.ctx, voter)
		before, err := f.mgr.Keeper().GetLiquidityProvider(f.ctx, f.asset, f.rune)
		c.Assert(err, IsNil)
		poolBefore, err := f.mgr.Keeper().GetPool(f.ctx, f.asset)
		c.Assert(err, IsNil)
		f.ctx = f.ctx.WithEventManager(cosmos.NewEventManager())
		f.reverse(c, tx)
		after, err := f.mgr.Keeper().GetLiquidityProvider(f.ctx, f.asset, f.rune)
		c.Assert(err, IsNil)
		c.Assert(after, DeepEquals, before)
		poolAfter, err := f.mgr.Keeper().GetPool(f.ctx, f.asset)
		c.Assert(err, IsNil)
		poolBefore.BalanceAsset = poolBefore.BalanceAsset.Sub(tx.Coins[0].Amount)
		c.Assert(poolAfter, DeepEquals, poolBefore)
		vault, err := f.mgr.Keeper().GetVault(f.ctx, f.vault.PubKey)
		c.Assert(err, IsNil)
		c.Assert(vault.GetCoin(f.asset).Amount.Equal(vaultBefore.GetCoin(f.asset).Amount.Sub(tx.Coins[0].Amount)), Equals, true)
		if failure == "missing" {
			c.Assert(liquidityProviderStored(c, f.mgr.Keeper(), f.ctx, f.asset, f.rune), Equals, false)
		}
		for _, evt := range f.ctx.EventManager().Events() {
			c.Assert(isPendingLiquidityWithdrawEvent(evt), Equals, false)
		}
		assertLiquidityFallbackSecurityEvent(c, f.ctx, tx)
		f.reverse(c, tx)
		again, err := f.mgr.Keeper().GetPool(f.ctx, f.asset)
		c.Assert(err, IsNil)
		c.Assert(again, DeepEquals, poolAfter)
	}
}

func (ErrataPendingLiquiditySuite) TestWithdrawalClosesOnlyItsBatch(c *C) {
	f := newPendingLiquidityFixture(c, common.BTCAsset)
	old := f.stage(c, f.rune, 10)
	f.stage(c, f.rune, 20)
	msg := NewMsgWithdrawLiquidity(GetRandomTx(), f.rune, cosmos.NewUint(MaxWithdrawBasisPoints), f.asset, common.EmptyAsset, f.nodes[0].NodeAddress)
	_, amount, _, _, err := withdraw(f.ctx, *msg, f.mgr)
	c.Assert(err, IsNil)
	c.Assert(amount.Equal(cosmos.NewUint(30*common.One)), Equals, true)
	c.Assert(liquidityProviderStored(c, f.mgr.Keeper(), f.ctx, f.asset, f.rune), Equals, false)
	fresh := f.stage(c, f.rune, 17)
	f.reverse(c, old)
	lp, err := f.mgr.Keeper().GetLiquidityProvider(f.ctx, f.asset, f.rune)
	c.Assert(err, IsNil)
	c.Assert(lp.PendingTxID, Equals, fresh.ID)
	f.reverse(c, fresh)
}

type countingLiquidityKeeper struct {
	keeper.Keeper
	reads, writes, scans int
}

func (k *countingLiquidityKeeper) GetObservedTxInVoterIterator(ctx cosmos.Context) cosmos.Iterator {
	k.scans++
	return k.Keeper.GetObservedTxInVoterIterator(ctx)
}

func (k *countingLiquidityKeeper) GetObservedTxInVoter(ctx cosmos.Context, id common.TxID) (ObservedTxVoter, error) {
	k.reads++
	return k.Keeper.GetObservedTxInVoter(ctx, id)
}

func (k *countingLiquidityKeeper) SetObservedTxInVoter(ctx cosmos.Context, voter ObservedTxVoter) {
	k.writes++
	k.Keeper.SetObservedTxInVoter(ctx, voter)
}

func (ErrataPendingLiquiditySuite) TestReceiptAccessBound(c *C) {
	for _, length := range []int{4, 64} {
		f := newPendingLiquidityFixture(c, common.BTCAsset)
		var txs []common.Tx
		for i := 0; i < length; i++ {
			txs = append(txs, f.stage(c, f.rune, 1))
		}
		count := &countingLiquidityKeeper{Keeper: f.mgr.Keeper()}
		f.mgr.K = count
		voter, err := count.GetObservedTxInVoter(f.ctx, txs[length/2].ID)
		c.Assert(err, IsNil)
		c.Assert(processErrataLiquidity(f.ctx, f.mgr, voter), IsNil)
		c.Logf("length=%d voter reads=%d writes=%d", length, count.reads, count.writes)
		c.Assert(count.reads, Equals, 1)
		c.Assert(count.writes, Equals, 0)
	}
}

func (ErrataPendingLiquiditySuite) TestUntrackedCommittedAddUsesKnownAmount(c *C) {
	f := newPendingLiquidityFixture(c, common.BTCAsset)
	tx := f.stage(c, f.rune, 10)
	completePendingLiquidity(c, f)
	voter, err := f.mgr.Keeper().GetObservedTxInVoter(f.ctx, tx.ID)
	c.Assert(err, IsNil)
	voter.PendingReceipt = nil
	f.mgr.Keeper().SetObservedTxInVoter(f.ctx, voter)
	before, err := f.mgr.Keeper().GetPool(f.ctx, f.asset)
	c.Assert(err, IsNil)
	lpBefore, err := f.mgr.Keeper().GetLiquidityProvider(f.ctx, f.asset, f.rune)
	c.Assert(err, IsNil)
	f.reverse(c, tx)
	after, err := f.mgr.Keeper().GetPool(f.ctx, f.asset)
	c.Assert(err, IsNil)
	c.Assert(after.BalanceAsset.Equal(before.BalanceAsset.Sub(tx.Coins[0].Amount)), Equals, true)
	c.Assert(after.LPUnits.Equal(before.LPUnits), Equals, true)
	lpAfter, err := f.mgr.Keeper().GetLiquidityProvider(f.ctx, f.asset, f.rune)
	c.Assert(err, IsNil)
	c.Assert(lpAfter, DeepEquals, lpBefore)
}

// A converted external deposit may stage, but cannot claim exact ownership of
// a different source coin. This used to be an ADD admission error.
func (ErrataPendingLiquiditySuite) TestTransformedPendingAddRemainsUsable(c *C) {
	f := newPendingLiquidityFixture(c, common.ETHAsset)
	tx := GetRandomTx()
	tx.Chain = common.BTCChain
	tx.Coins = common.NewCoins(common.NewCoin(common.BTCAsset, cosmos.NewUint(100*common.One)))
	tx.Memo = "+:ETH.ETH:" + f.rune.String()
	voter := NewObservedTxVoter(tx.ID, nil)
	voter.Tx = NewObservedTx(tx, 1, f.vault.PubKey, 1)
	f.mgr.Keeper().SetObservedTxInVoter(f.ctx, voter)
	err := NewAddLiquidityHandler(f.mgr).addLiquidity(f.ctx, f.asset, cosmos.ZeroUint(), cosmos.NewUint(common.One), f.rune, GetRandomETHAddress(), tx.ID, true, f.mgr.GetConstants())
	c.Assert(err, IsNil)
	lp, err := f.mgr.Keeper().GetLiquidityProvider(f.ctx, f.asset, f.rune)
	c.Assert(err, IsNil)
	c.Assert(lp.PendingAsset.String(), Equals, "100000000")
	c.Assert(lp.PendingBatch.IsEmpty(), Equals, true)
	after, err := f.mgr.Keeper().GetObservedTxInVoter(f.ctx, tx.ID)
	c.Assert(err, IsNil)
	c.Assert(after, DeepEquals, voter)
}

// No migration is needed for repeated historical deposits, even when their last
// TxID represents only part of the aggregate. New receipts own only new amounts.
func (ErrataPendingLiquiditySuite) TestUntrackedPendingRemainsUsable(c *C) {
	f := newPendingLiquidityFixture(c, common.BTCAsset)
	old := []common.Tx{f.stage(c, f.rune, 10), f.stage(c, f.rune, 20)}
	for _, tx := range old {
		voter, err := f.mgr.Keeper().GetObservedTxInVoter(f.ctx, tx.ID)
		c.Assert(err, IsNil)
		voter.PendingReceipt = nil
		f.mgr.Keeper().SetObservedTxInVoter(f.ctx, voter)
	}
	lp, err := f.mgr.Keeper().GetLiquidityProvider(f.ctx, f.asset, f.rune)
	c.Assert(err, IsNil)
	lp.PendingBatch = ""
	f.mgr.Keeper().SetLiquidityProvider(f.ctx, lp)
	fresh := f.stage(c, f.rune, 7)
	f.reverse(c, old[0]) // preserve the old claim, charge only its original 10 BTC
	f.reverse(c, fresh)  // remove exactly 7, leaving the untracked 30 BTC untouched
	lp, err = f.mgr.Keeper().GetLiquidityProvider(f.ctx, f.asset, f.rune)
	c.Assert(err, IsNil)
	c.Assert(lp.PendingAsset.String(), Equals, "3000000000")
	c.Assert(lp.PendingTxID.IsEmpty(), Equals, true)
	c.Assert(lp.PendingBatch, Equals, fresh.ID)
	pool, err := f.mgr.Keeper().GetPool(f.ctx, f.asset)
	c.Assert(err, IsNil)
	c.Assert(pool.BalanceAsset.Equal(f.initial.BalanceAsset.Sub(old[0].Coins[0].Amount)), Equals, true)
	vault, err := f.mgr.Keeper().GetVault(f.ctx, f.vault.PubKey)
	c.Assert(err, IsNil)
	c.Assert(vault.GetCoin(f.asset).Amount.Equal(pool.BalanceAsset.Add(pool.PendingInboundAsset)), Equals, true)
	withdrawCtx, _ := f.ctx.CacheContext()
	msg := NewMsgWithdrawLiquidity(GetRandomTx(), f.rune, cosmos.NewUint(MaxWithdrawBasisPoints), f.asset, common.EmptyAsset, f.nodes[0].NodeAddress)
	_, amount, _, _, err := withdraw(withdrawCtx, *msg, f.mgr)
	c.Assert(err, IsNil)
	c.Assert(amount.Equal(lp.PendingAsset), Equals, true)
	completePendingLiquidity(c, f)
	lp, err = f.mgr.Keeper().GetLiquidityProvider(f.ctx, f.asset, f.rune)
	c.Assert(err, IsNil)
	c.Assert(lp.PendingBatch.IsEmpty(), Equals, true)
	c.Assert(lp.PendingAsset.IsZero(), Equals, true)
	c.Assert(lp.Units.IsZero(), Equals, false)
}

func (ErrataPendingLiquiditySuite) TestReversedTailCompletionEvent(c *C) {
	f := newPendingLiquidityFixture(c, common.BTCAsset)
	f.stage(c, f.rune, 10)
	tail := f.stage(c, f.rune, 20)
	f.reverse(c, tail)
	f.ctx = f.ctx.WithEventManager(cosmos.NewEventManager())
	completePendingLiquidity(c, f)
	found := false
	for _, event := range f.ctx.EventManager().Events() {
		if event.Type != "add_liquidity" {
			continue
		}
		found = true
		for _, attribute := range event.Attributes {
			c.Assert(attribute.Key == "BTC_txid", Equals, false, Commentf("unknown surviving ID is omitted, not replaced by the orphan"))
			if attribute.Key == "asset_amount" {
				c.Assert(attribute.Value, Equals, "1000000000")
			}
		}
	}
	c.Assert(found, Equals, true)
}

type receiptFailureKeeper struct {
	keeper.Keeper
	failure string
	failed  bool
}

func (k *receiptFailureKeeper) GetLiquidityProvider(ctx cosmos.Context, asset common.Asset, address common.Address) (LiquidityProvider, error) {
	lp, err := k.Keeper.GetLiquidityProvider(ctx, asset, address)
	if k.failure == "owner" {
		lp.RuneAddress = GetRandomTHORAddress()
	}
	if k.failure == "asset" {
		lp.Asset = common.ETHAsset
	}
	return lp, err
}

func (k *receiptFailureKeeper) SetPool(ctx cosmos.Context, pool Pool) error {
	if err := k.Keeper.SetPool(ctx, pool); err != nil {
		return err
	}
	if k.failure == "write" && !k.failed {
		k.failed = true
		return fmt.Errorf("injected failure after pending pool write")
	}
	return nil
}

func (ErrataPendingLiquiditySuite) TestReceiptIdentityAndWriteFailureFallback(c *C) {
	for _, failure := range []string{"owner", "asset", "write"} {
		f := newPendingLiquidityFixture(c, common.BTCAsset)
		tx := f.stage(c, f.rune, 10)
		original := f.mgr.Keeper()
		lp, err := original.GetLiquidityProvider(f.ctx, f.asset, f.rune)
		c.Assert(err, IsNil)
		pool, err := original.GetPool(f.ctx, f.asset)
		c.Assert(err, IsNil)
		f.mgr.K = &receiptFailureKeeper{Keeper: original, failure: failure}
		f.ctx = f.ctx.WithEventManager(cosmos.NewEventManager())
		f.reverse(c, tx)
		after, err := original.GetLiquidityProvider(f.ctx, f.asset, f.rune)
		c.Assert(err, IsNil)
		c.Assert(after, DeepEquals, lp)
		poolAfter, err := original.GetPool(f.ctx, f.asset)
		c.Assert(err, IsNil)
		pool.BalanceAsset = pool.BalanceAsset.Sub(tx.Coins[0].Amount)
		c.Assert(poolAfter, DeepEquals, pool)
		for _, event := range f.ctx.EventManager().Events() {
			c.Assert(isPendingLiquidityWithdrawEvent(event), Equals, false)
		}
		assertLiquidityFallbackSecurityEvent(c, f.ctx, tx)
	}
}

func (ErrataPendingLiquiditySuite) TestAutomaticCompletionClosesReceiptBatch(c *C) {
	f := newPendingLiquidityFixture(c, common.BTCAsset)
	tx := f.stage(c, f.rune, 10)
	f.ctx = f.ctx.WithBlockHeight(200)
	f.mgr.Keeper().SetMimir(f.ctx, "PendingLiquidityAgeLimit", 1)
	pool, err := f.mgr.Keeper().GetPool(f.ctx, f.asset)
	c.Assert(err, IsNil)
	pm := newPoolMgr()
	c.Assert(pm.commitPendingLiquidity(f.ctx, pool, f.mgr), IsNil)
	lp, err := f.mgr.Keeper().GetLiquidityProvider(f.ctx, f.asset, f.rune)
	c.Assert(err, IsNil)
	c.Assert(lp.PendingBatch.IsEmpty(), Equals, true)
	c.Assert(lp.PendingAsset.IsZero(), Equals, true)
	poolBefore, err := f.mgr.Keeper().GetPool(f.ctx, f.asset)
	c.Assert(err, IsNil)
	f.reverse(c, tx)
	after, err := f.mgr.Keeper().GetLiquidityProvider(f.ctx, f.asset, f.rune)
	c.Assert(err, IsNil)
	c.Assert(after, DeepEquals, lp)
	poolAfter, err := f.mgr.Keeper().GetPool(f.ctx, f.asset)
	c.Assert(err, IsNil)
	c.Assert(poolAfter.BalanceAsset.Equal(poolBefore.BalanceAsset.Sub(tx.Coins[0].Amount)), Equals, true)
}

// An orphaned asset deposit naming somebody else's RUNE address must not leave
// an empty, unwithdrawable LP that binds that address to the orphan's sender.
func (ErrataPendingLiquiditySuite) TestPendingErrataReleasesEmptyAddressBinding(c *C) {
	f := newPendingLiquidityFixture(c, common.BTCAsset)
	orphan := f.stage(c, f.rune, 1)
	f.reverse(c, orphan)
	c.Check(liquidityProviderStored(c, f.mgr.Keeper(), f.ctx, f.asset, f.rune), Equals, false)

	// The RUNE owner can pair with their own BTC address after the reversal.
	f.refund = GetRandomBTCAddress()
	fresh := f.stage(c, f.rune, 2)
	lp, err := f.mgr.Keeper().GetLiquidityProvider(f.ctx, f.asset, f.rune)
	c.Assert(err, IsNil)
	c.Assert(lp.AssetAddress, Equals, f.refund)
	c.Assert(lp.PendingBatch, Equals, fresh.ID)
	c.Assert(lp.PendingAsset.Equal(fresh.Coins[0].Amount), Equals, true)
	completePendingLiquidity(c, f)
	completed, err := f.mgr.Keeper().GetLiquidityProvider(f.ctx, f.asset, f.rune)
	c.Assert(err, IsNil)
	c.Assert(completed.Units.IsZero(), Equals, false)
	c.Assert(completed.PendingAsset.IsZero(), Equals, true)
	// A late duplicate of the old errata must not remove the new position.
	f.reverse(c, orphan)
	after, err := f.mgr.Keeper().GetLiquidityProvider(f.ctx, f.asset, f.rune)
	c.Assert(err, IsNil)
	c.Assert(after, DeepEquals, completed)
}

func (ErrataPendingLiquiditySuite) TestPendingErrataKeepsExistingUnitPosition(c *C) {
	f := newPendingLiquidityFixture(c, common.BTCAsset)
	f.stage(c, f.rune, 10)
	completePendingLiquidity(c, f)
	original, err := f.mgr.Keeper().GetLiquidityProvider(f.ctx, f.asset, f.rune)
	c.Assert(err, IsNil)
	c.Assert(original.Units.IsZero(), Equals, false)
	orphan := f.stage(c, f.rune, 3)
	f.reverse(c, orphan)
	c.Assert(liquidityProviderStored(c, f.mgr.Keeper(), f.ctx, f.asset, f.rune), Equals, true)
	after, err := f.mgr.Keeper().GetLiquidityProvider(f.ctx, f.asset, f.rune)
	c.Assert(err, IsNil)
	c.Assert(after, DeepEquals, original)
}
