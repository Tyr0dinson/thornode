package thorchain

import (
	"bytes"

	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/utxo/firoutil"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
)

// firoRefundAddresses exercises all supported transparent destination scripts.
func firoRefundAddresses(c *C, kind firoutil.Kind) (valid, wrong common.Address) {
	wrongNetwork := firoutil.MainNet
	if common.FIRONetwork() == firoutil.MainNet {
		wrongNetwork = firoutil.RegTest
	}
	current, err := firoutil.NewAddress(bytes.Repeat([]byte{1}, 20), kind, common.FIRONetwork())
	c.Assert(err, IsNil)
	other, err := firoutil.NewAddress(bytes.Repeat([]byte{1}, 20), kind, wrongNetwork)
	c.Assert(err, IsNil)
	return common.Address(current.String()), common.Address(other.String())
}

func (s TxOutStoreSuite) TestFiroRefundAddressNetwork(c *C) {
	for _, kind := range []firoutil.Kind{firoutil.PubKeyHash, firoutil.ScriptHash, firoutil.Exchange} {
		valid, wrong := firoRefundAddresses(c, kind)
		sender, err := GetRandomPubKey().GetAddress(common.FIROChain)
		c.Assert(err, IsNil)
		for _, tc := range []struct{ refund, want common.Address }{
			{wrong, sender}, {valid, valid}, {common.NoAddress, sender},
		} {
			ctx, k := setupKeeperForTest(c)
			mgr := NewDummyMgrWithKeeper(k)
			mgr.gasMgr = newGasMgr(mgr.GetConstants(), k)
			txOutStore := newTxOutStorage(k, mgr.GetConstants(), mgr.EventMgr(), mgr.GasMgr())
			mgr.txOutStore = txOutStore
			k.SetMimir(ctx, constants.MinTxOutVolumeThreshold.String(), 0)
			k.SetMimir(ctx, constants.EnableMemolessOutbound.String(), 0)
			c.Assert(k.SaveNetworkFee(ctx, common.FIROChain, NewNetworkFee(common.FIROChain, 200, 1)), IsNil)

			pool := NewPool()
			pool.Asset = common.FIROAsset
			pool.Decimals = 8
			pool.BalanceRune = cosmos.NewUint(100 * common.One)
			pool.BalanceAsset = cosmos.NewUint(100 * common.One)
			c.Assert(k.SetPool(ctx, pool), IsNil)
			vault := NewVault(ctx.BlockHeight(), ActiveVault, AsgardVault, GetRandomPubKey(), common.Chains{common.FIROChain}.Strings(), nil)
			vault.AddFunds(common.NewCoins(common.NewCoin(common.FIROAsset, cosmos.NewUint(10*common.One))))
			c.Assert(k.SetVault(ctx, vault), IsNil)
			var vaultAddr common.Address
			vaultAddr, err = vault.PubKey.GetAddress(common.FIROChain)
			c.Assert(err, IsNil)

			memo := "=:BTC.BTC:" + GetRandomBTCAddress().String()
			if !tc.refund.IsEmpty() {
				memo += "/" + tc.refund.String()
			}
			tx := NewObservedTx(common.NewTx(GetRandomTxHash(), sender, vaultAddr,
				common.NewCoins(common.NewCoin(common.FIROAsset, cosmos.NewUint(common.One))),
				common.Gas{common.NewCoin(common.FIROAsset, cosmos.NewUint(1_000))}, memo),
				ctx.BlockHeight(), vault.PubKey, ctx.BlockHeight())
			voter := NewObservedTxVoter(tx.Tx.ID, nil)
			voter.Height = ctx.BlockHeight()
			voter.Tx = tx
			k.SetObservedTxInVoter(ctx, voter)

			c.Assert(refundTx(ctx, tx, mgr, CodeSwapFail, "test refund", ""), IsNil)
			var items []TxOutItem
			items, err = txOutStore.GetOutboundItems(ctx)
			c.Assert(err, IsNil)
			c.Assert(items, HasLen, 1)
			c.Check(items[0].ToAddress, Equals, tc.want, Commentf("refund address: %s", tc.refund))
			c.Check(items[0].Coin.Asset, Equals, common.FIROAsset)
		}
	}
}

func (s *SlashingSuite) TestFiroReverseSwapRefundAddressNetwork(c *C) {
	for _, kind := range []firoutil.Kind{firoutil.PubKeyHash, firoutil.ScriptHash, firoutil.Exchange} {
		valid, wrong := firoRefundAddresses(c, kind)
		sender, err := GetRandomPubKey().GetAddress(common.FIROChain)
		c.Assert(err, IsNil)
		for _, tc := range []struct{ refund, want common.Address }{
			{wrong, sender}, {valid, valid}, {common.NoAddress, sender},
		} {
			ctx, mgr := setupManagerForTest(c)
			mgr.Keeper().SetMimir(ctx, constants.EnableAdvSwapQueue.String(), 0)
			vault := GetRandomVault()
			originalTx := ObservedTx{Tx: common.Tx{
				ID: GetRandomTxHash(), FromAddress: sender,
				Coins: common.NewCoins(common.NewCoin(common.FIROAsset, cosmos.NewUint(common.One))),
			}}
			toi := TxOutItem{
				Chain: common.BTCChain, InHash: originalTx.Tx.ID, VaultPubKey: vault.PubKey,
				ToAddress: GetRandomBTCAddress(), Coin: common.NewCoin(common.BTCAsset, cosmos.NewUint(common.One)),
			}
			slasher := newSlasher(mgr.Keeper(), mgr.EventMgr())
			c.Assert(slasher.sendFailedSwapReverseSwap(ctx, mgr, toi, vault, originalTx,
				SwapMemo{RefundAddress: tc.refund}, originalTx.Tx.ID), IsNil)
			items := mgr.Keeper().GetSwapQueueIterator(ctx)
			c.Assert(items.Valid(), Equals, true)
			var swap MsgSwap
			c.Assert(mgr.Keeper().Cdc().Unmarshal(items.Value(), &swap), IsNil)
			items.Next()
			c.Check(items.Valid(), Equals, false)
			items.Close()
			c.Check(swap.TargetAsset, Equals, common.FIROAsset)
			c.Check(swap.Destination, Equals, tc.want, Commentf("refund address: %s", tc.refund))
		}
	}
}

func (s *QuotesSuite) TestFiroQuoteRefundAddressNetwork(c *C) {
	for _, kind := range []firoutil.Kind{firoutil.PubKeyHash, firoutil.ScriptHash, firoutil.Exchange} {
		ctx, mgr := setupManagerForTest(c)
		valid, wrong := firoRefundAddresses(c, kind)
		addr, err := quoteParseRefundAddress(ctx, mgr, valid.String(), common.FIROChain)
		c.Assert(err, IsNil)
		c.Check(addr, Equals, valid)
		_, err = quoteParseRefundAddress(ctx, mgr, wrong.String(), common.FIROChain)
		c.Check(err, ErrorMatches, ".*not valid for FIRO on the current network")
	}
}
