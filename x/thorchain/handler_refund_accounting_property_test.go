package thorchain

import (
	"fmt"

	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

// The observed vault keeps the physical coin until the outbound is observed.
// A refund therefore creates a bounded outbound liability; retrying the same
// inbound must not create a second liability.
func (AccountingPropertySuite) TestRefundOutboundObligationConservation(c *C) {
	for _, seed := range accountingPropertySeeds() {
		w := getHandlerTestWrapper(c, 1, true, false)
		vaultKey := accountingVaultPubKey(c, "A")
		senderKey := accountingVaultPubKey(c, "B")
		asset := common.DOGEAsset
		amount := uint64(100*common.One) + uint64(seed*1000)
		trace := fmt.Sprintf("seed=%d refund DOGE=%d", seed, amount)
		pool := NewPool()
		pool.Asset = asset
		pool.Status = PoolAvailable
		pool.BalanceRune = cosmos.NewUint(100 * common.One)
		pool.BalanceAsset = cosmos.NewUint(100 * common.One)
		pool.LPUnits = cosmos.NewUint(100 * common.One)
		c.Assert(w.keeper.SetPool(w.ctx, pool), IsNil, Commentf(trace))
		vault := NewVault(1, ActiveVault, AsgardVault, vaultKey, common.Chains{common.DOGEChain}.Strings(), []ChainContract{})
		vault.Coins = common.NewCoins(common.NewCoin(asset, cosmos.NewUint(amount)))
		c.Assert(w.keeper.SetVault(w.ctx, vault), IsNil, Commentf(trace))
		c.Assert(w.keeper.SaveNetworkFee(w.ctx, common.DOGEChain, NetworkFee{
			Chain: common.DOGEChain, TransactionSize: 1, TransactionFeeRate: 10_000,
		}), IsNil, Commentf(trace))
		from, err := senderKey.GetAddress(common.DOGEChain)
		c.Assert(err, IsNil, Commentf(trace))
		to, err := vaultKey.GetAddress(common.DOGEChain)
		c.Assert(err, IsNil, Commentf(trace))
		tx := common.NewTx(accountingTxID(seed, "refund"), from, to,
			common.NewCoins(common.NewCoin(asset, cosmos.NewUint(amount))),
			common.Gas{common.NewCoin(asset, cosmos.NewUint(10_000))}, "WITHDRAW:DOGE.DOGE")
		tx.Chain = common.DOGEChain
		observed := NewObservedTx(tx, 1024, vault.PubKey, 1024)
		outbounds := func() []TxOutItem {
			for height := w.ctx.BlockHeight(); height <= w.ctx.BlockHeight()+10; height++ {
				items, err := w.mgr.TxOutStore().GetOutboundItems(w.ctx.WithBlockHeight(height))
				c.Assert(err, IsNil, Commentf(trace))
				if len(items) != 0 {
					return items
				}
			}
			return nil
		}
		c.Assert(refundTx(w.ctx, observed, w.mgr, 0, "accounting refund", ""), IsNil, Commentf(trace))
		items := outbounds()
		c.Assert(items, HasLen, 1, Commentf(trace))
		c.Assert(items[0].Coin.Asset.Equals(asset), Equals, true, Commentf(trace))
		c.Assert(items[0].Coin.Amount.IsZero(), Equals, false, Commentf(trace))
		c.Assert(items[0].Coin.Amount.LTE(cosmos.NewUint(amount)), Equals, true, Commentf(trace))
		v, err := w.keeper.GetVault(w.ctx, vault.PubKey)
		c.Assert(err, IsNil, Commentf(trace))
		c.Assert(v.GetCoin(asset).Amount.Uint64(), Equals, amount, Commentf(trace))
		firstObligation := items[0].Coin.Amount
		c.Assert(refundTx(w.ctx, observed, w.mgr, 0, "accounting refund retry", ""), IsNil, Commentf(trace))
		items = outbounds()
		c.Assert(items, HasLen, 1, Commentf(trace))
		c.Assert(items[0].Coin.Amount.Equal(firstObligation), Equals, true, Commentf(trace))
		v, err = w.keeper.GetVault(w.ctx, vault.PubKey)
		c.Assert(err, IsNil, Commentf(trace))
		c.Assert(v.GetCoin(asset).Amount.Uint64(), Equals, amount, Commentf(trace))
	}
}
