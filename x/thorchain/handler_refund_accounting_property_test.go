package thorchain

import (
	"fmt"

	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
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
		outboundWindow := w.keeper.GetConfigInt64(w.ctx, constants.TxOutDelayMax)
		// CalcTxOutHeight may delay an outbound, so a retry could schedule a
		// second liability at a later height. Collect every item for this
		// inbound across the whole scheduling window rather than stopping at
		// the first populated height.
		outbounds := func() []TxOutItem {
			var matched []TxOutItem
			for height := w.ctx.BlockHeight(); height <= w.ctx.BlockHeight()+outboundWindow; height++ {
				outboundItems, getErr := w.mgr.TxOutStore().GetOutboundItems(w.ctx.WithBlockHeight(height))
				c.Assert(getErr, IsNil, Commentf(trace))
				for _, item := range outboundItems {
					if item.InHash.Equals(tx.ID) {
						matched = append(matched, item)
					}
				}
			}
			return matched
		}
		actions := func() []TxOutItem {
			voter, getErr := w.keeper.GetObservedTxInVoter(w.ctx, tx.ID)
			c.Assert(getErr, IsNil, Commentf(trace))
			return voter.Actions
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
		c.Assert(actions(), HasLen, 1, Commentf(trace))
		c.Assert(refundTx(w.ctx, observed, w.mgr, 0, "accounting refund retry", ""), IsNil, Commentf(trace))
		items = outbounds()
		c.Assert(items, HasLen, 1, Commentf(trace))
		c.Assert(items[0].Coin.Amount.Equal(firstObligation), Equals, true, Commentf(trace))
		retried := actions()
		c.Assert(retried, HasLen, 1, Commentf(trace))
		c.Assert(retried[0].Coin.Amount.Equal(firstObligation), Equals, true, Commentf(trace))
		v, err = w.keeper.GetVault(w.ctx, vault.PubKey)
		c.Assert(err, IsNil, Commentf(trace))
		c.Assert(v.GetCoin(asset).Amount.Uint64(), Equals, amount, Commentf(trace))
	}
}
