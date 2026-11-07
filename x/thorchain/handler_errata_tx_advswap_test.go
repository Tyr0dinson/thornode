package thorchain

import (
	"fmt"
	"strings"

	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

type HandlerErrataAdvSwapSuite struct{}

var _ = Suite(&HandlerErrataAdvSwapSuite{})

// recordingTxOutStore records every outbound, including native THORChain
// ones that TxOutStoreDummy drops.
type recordingTxOutStore struct {
	*TxOutStoreDummy
	items []TxOutItem
}

func (r *recordingTxOutStore) TryAddTxOutItem(ctx cosmos.Context, mgr Manager, toi TxOutItem, minOut cosmos.Uint) (bool, error) {
	r.items = append(r.items, toi)
	return r.TxOutStoreDummy.TryAddTxOutItem(ctx, mgr, toi, minOut)
}

type errataAdvSwapFixture struct {
	ctx    cosmos.Context
	mgr    *Mgrs
	out    *recordingTxOutStore
	na     NodeAccount
	txID   common.TxID
	from   common.Address
	source common.Coin
	target common.Coin
	swap   MsgSwap
}

var (
	errataAdvSwapPoolBalance = cosmos.NewUint(1000 * common.One)
	errataAdvSwapIn          = cosmos.NewUint(40_000_000)
	errataAdvSwapOut         = cosmos.NewUint(40 * common.One)
)

// setupErrataAdvSwap queues a partially filled BTC -> RUNE limit swap whose
// inbound has reached finalised consensus and was credited to an asgard vault.
func setupErrataAdvSwap(c *C) errataAdvSwapFixture {
	return setupErrataAdvSwapTo(c, common.RuneAsset(), GetRandomTHORAddress())
}

// setupErrataAdvSwapTo is setupErrataAdvSwap with a chosen target asset. An ETH
// pool backs non-RUNE targets.
func setupErrataAdvSwapTo(c *C, targetAsset common.Asset, dest common.Address) errataAdvSwapFixture {
	ctx, mgr := setupManagerForTest(c)
	out := &recordingTxOutStore{TxOutStoreDummy: NewTxStoreDummy()}
	mgr.txOutStore = out
	ctx = ctx.WithBlockHeight(100)
	k := mgr.Keeper()

	na := GetRandomValidatorNode(NodeActive)
	c.Assert(k.SetNodeAccount(ctx, na), IsNil)

	pool := NewPool()
	pool.Asset = common.BTCAsset
	pool.Status = PoolAvailable
	pool.BalanceAsset = errataAdvSwapPoolBalance
	pool.BalanceRune = errataAdvSwapPoolBalance
	pool.LPUnits = errataAdvSwapPoolBalance
	c.Assert(k.SetPool(ctx, pool), IsNil)
	pool.Asset = common.ETHAsset
	c.Assert(k.SetPool(ctx, pool), IsNil)
	pool.Asset = common.TCY
	c.Assert(k.SetPool(ctx, pool), IsNil)

	vault := GetRandomVault()
	vault.Status = ActiveVault
	vault.Type = AsgardVault
	vault.Coins = common.NewCoins(common.NewCoin(common.BTCAsset, cosmos.NewUint(10*common.One)))
	c.Assert(k.SetVault(ctx, vault), IsNil)

	txID := GetRandomTxHash()
	from := GetRandomBTCAddress()
	source := common.NewCoin(common.BTCAsset, cosmos.NewUint(common.One))
	target := common.NewCoin(targetAsset, cosmos.NewUint(500*common.One))
	tx := common.NewTx(txID, from, GetRandomBTCAddress(), common.NewCoins(source), common.Gas{common.NewCoin(common.BTCAsset, cosmos.NewUint(1))},
		fmt.Sprintf("=<:%s:%s:%s", targetAsset, dest, target.Amount))

	obs := common.NewObservedTx(tx, 90, vault.PubKey, 90)
	voter := NewObservedTxVoter(txID, []common.ObservedTx{obs})
	voter.Tx = obs
	voter.Height = 90
	voter.FinalisedHeight = 90
	voter.UpdatedVault = true
	k.SetObservedTxInVoter(ctx, voter)

	swap := NewMsgSwap(tx, targetAsset, dest, target.Amount, common.NoAddress, cosmos.ZeroUint(), "", "", nil, types.SwapType_limit, 0, 0, types.SwapVersion_v2, na.NodeAddress)
	swap.InitialBlockHeight = 90
	swap.State = &types.SwapState{
		Quantity: 1,
		Deposit:  source.Amount,
		In:       errataAdvSwapIn,
		Out:      errataAdvSwapOut,
	}
	c.Assert(k.SetAdvSwapQueueItem(ctx, *swap), IsNil)

	return errataAdvSwapFixture{ctx: ctx, mgr: mgr, out: out, na: na, txID: txID, from: from, source: source, target: target, swap: *swap}
}

func (f errataAdvSwapFixture) errata(c *C) {
	handler := NewErrataTxHandler(f.mgr)
	_, err := handler.handle(f.ctx, *NewMsgErrataTx(f.txID, common.BTCChain, f.na.NodeAddress))
	c.Assert(err, IsNil)
}

func (f errataAdvSwapFixture) markReverted(c *C) {
	voter, err := f.mgr.Keeper().GetObservedTxInVoter(f.ctx, f.txID)
	c.Assert(err, IsNil)
	voter.SetReverted()
	f.mgr.Keeper().SetObservedTxInVoter(f.ctx, voter)
}

func (f errataAdvSwapFixture) cancel() error {
	msg := types.NewMsgModifyLimitSwap(f.from, f.source, f.target, cosmos.ZeroUint(), f.na.NodeAddress, common.EmptyAsset, cosmos.ZeroUint())
	_, err := NewModifyLimitSwapHandler(f.mgr).Run(f.ctx, msg)
	return err
}

func (f errataAdvSwapFixture) expire(c *C) {
	ttlHeight := f.ctx.BlockHeight() + 1
	c.Assert(f.mgr.Keeper().AddToLimitSwapTTL(f.ctx, ttlHeight, f.txID), IsNil)
	ctx := f.ctx.WithBlockHeight(ttlHeight)
	c.Assert(newSwapQueueAdv(f.mgr.Keeper()).processExpiredLimitSwaps(ctx, f.mgr), IsNil)
}

func (f errataAdvSwapFixture) assertNothingPaid(c *C) {
	c.Assert(f.out.items, HasLen, 0)
	c.Assert(f.mgr.Keeper().HasAdvSwapQueueItem(f.ctx, f.txID, 0), Equals, false)
}

// An errata for a queued swap's inbound must remove the queue item, so a later
// cancel finds nothing to settle and pays nothing.
func (s *HandlerErrataAdvSwapSuite) TestErrataThenCancelPaysNothing(c *C) {
	f := setupErrataAdvSwap(c)
	f.errata(c)

	voter, err := f.mgr.Keeper().GetObservedTxInVoter(f.ctx, f.txID)
	c.Assert(err, IsNil)
	c.Assert(voter.Reverted, Equals, true)
	c.Assert(f.mgr.Keeper().HasAdvSwapQueueItem(f.ctx, f.txID, 0), Equals, false)

	c.Assert(f.cancel(), ErrorMatches, "could not find matching limit swap")
	f.assertNothingPaid(c)
}

// The same holds when the swap would otherwise settle at its TTL.
func (s *HandlerErrataAdvSwapSuite) TestErrataThenExpiryPaysNothing(c *C) {
	f := setupErrataAdvSwap(c)
	f.errata(c)
	f.expire(c)
	f.assertNothingPaid(c)
}

// Only the filled portion ever entered the pool, so the errata unwinds that
// portion and returns the unpaid output instead of debiting the full deposit.
func (s *HandlerErrataAdvSwapSuite) TestErrataUnwindsOnlyTheFilledPortion(c *C) {
	f := setupErrataAdvSwap(c)
	f.errata(c)

	pool, err := f.mgr.Keeper().GetPool(f.ctx, common.BTCAsset)
	c.Assert(err, IsNil)
	c.Assert(pool.BalanceAsset.String(), Equals, common.SafeSub(errataAdvSwapPoolBalance, errataAdvSwapIn).String())
	c.Assert(pool.BalanceRune.String(), Equals, errataAdvSwapPoolBalance.Add(errataAdvSwapOut).String())
}

// Defense in depth: a swap whose inbound is reverted pays nothing on cancel even
// if its queue item survived the errata.
func (s *HandlerErrataAdvSwapSuite) TestRevertedInboundCancelPaysNothing(c *C) {
	f := setupErrataAdvSwap(c)
	f.markReverted(c)
	c.Assert(f.cancel(), IsNil)
	f.assertNothingPaid(c)
	c.Assert(f.eventAttrs(types.LimitSwapCloseEventType), DeepEquals, []map[string]string{{
		"txid": f.txID.String(), "reason": revertedInboundCloseReason, "block_height": fmt.Sprintf("%d", f.ctx.BlockHeight()),
	}})
}

// eventAttrs returns the attributes of every emitted event of the given type.
func (f errataAdvSwapFixture) eventAttrs(typ string) []map[string]string {
	var out []map[string]string
	for _, e := range f.ctx.EventManager().Events() {
		if e.Type != typ {
			continue
		}
		attrs := map[string]string{}
		for _, a := range e.Attributes {
			attrs[a.Key] = a.Value
		}
		out = append(out, attrs)
	}
	return out
}

// Defense in depth: a swap whose inbound is reverted pays nothing at its TTL
// even if its queue item survived the errata.
func (s *HandlerErrataAdvSwapSuite) TestRevertedInboundExpiryPaysNothing(c *C) {
	f := setupErrataAdvSwap(c)
	f.markReverted(c)
	f.expire(c)
	f.assertNothingPaid(c)
}

// Defense in depth: refundTx never refunds an inbound that was reverted.
func (s *HandlerErrataAdvSwapSuite) TestRefundTxSkipsRevertedInbound(c *C) {
	f := setupErrataAdvSwap(c)
	f.markReverted(c)
	voter, err := f.mgr.Keeper().GetObservedTxInVoter(f.ctx, f.txID)
	c.Assert(err, IsNil)
	c.Assert(refundTx(f.ctx, voter.Tx, f.mgr, CodeSwapFail, "reverted", ""), IsNil)
	c.Assert(f.out.items, HasLen, 0)
}

// Only the reverted inbound's own coins are skipped; another coin refunded
// under the same tx id is still paid.
func (s *HandlerErrataAdvSwapSuite) TestRefundTxKeepsCoinsOutsideRevertedInbound(c *C) {
	f := setupErrataAdvSwap(c)
	f.markReverted(c)
	voter, err := f.mgr.Keeper().GetObservedTxInVoter(f.ctx, f.txID)
	c.Assert(err, IsNil)
	refund := voter.Tx
	refund.Tx.Coins = append(common.Coins{}, refund.Tx.Coins...)
	refund.Tx.Coins = append(refund.Tx.Coins, common.NewCoin(common.ETHAsset, cosmos.NewUint(common.One)))
	c.Assert(refundTx(f.ctx, refund, f.mgr, CodeSwapFail, "reverted", ""), IsNil)
	c.Assert(f.out.items, HasLen, 1)
	c.Assert(f.out.items[0].Coin.Asset.Equals(common.ETHAsset), Equals, true)
}

// Control: an unreverted swap still settles normally on cancel, so the tests
// above exercise the reverted gate and not a broken fixture.
func (s *HandlerErrataAdvSwapSuite) TestUnrevertedCancelStillSettles(c *C) {
	f := setupErrataAdvSwap(c)
	c.Assert(f.cancel(), IsNil)
	c.Assert(f.out.items, HasLen, 2)
	c.Assert(f.out.items[0].Coin.Equals(common.NewCoin(common.RuneAsset(), errataAdvSwapOut)), Equals, true)
	c.Assert(f.out.items[1].Coin.Equals(common.NewCoin(common.BTCAsset, common.SafeSub(f.source.Amount, errataAdvSwapIn))), Equals, true)
}

func (f errataAdvSwapFixture) poolAsset(c *C, asset common.Asset) string {
	pool, err := f.mgr.Keeper().GetPool(f.ctx, asset)
	c.Assert(err, IsNil)
	return pool.BalanceAsset.String()
}

// The output of an L1, trade, secured or native non-RUNE target left the target
// pool's asset depth and is credited back to it; only the filled input leaves the source pool.
func (s *HandlerErrataAdvSwapSuite) TestErrataCreditsAssetOutputToTargetPool(c *C) {
	targets := []struct {
		asset common.Asset
		dest  common.Address
	}{
		{common.ETHAsset, GetRandomETHAddress()},
		{common.ETHAsset.GetTradeAsset(), GetRandomTHORAddress()},
		{common.ETHAsset.GetSecuredAsset(), GetRandomTHORAddress()},
		{common.TCY, GetRandomTHORAddress()},
	}
	for _, t := range targets {
		f := setupErrataAdvSwapTo(c, t.asset, t.dest)
		f.errata(c)
		c.Check(f.poolAsset(c, t.asset.GetLayer1Asset()), Equals, errataAdvSwapPoolBalance.Add(errataAdvSwapOut).String(), Commentf("%s", t.asset))
		c.Check(f.poolAsset(c, common.BTCAsset), Equals, common.SafeSub(errataAdvSwapPoolBalance, errataAdvSwapIn).String(), Commentf("%s", t.asset))
		f.assertNothingPaid(c)
	}
}

// A synth output was minted into Asgard by the swap and is burned, leaving the
// target pool's asset depth untouched.
func (s *HandlerErrataAdvSwapSuite) TestErrataBurnsSynthOutput(c *C) {
	synth := common.ETHAsset.GetSyntheticAsset()
	f := setupErrataAdvSwapTo(c, synth, GetRandomTHORAddress())
	k := f.mgr.Keeper()
	minted := common.NewCoin(synth, errataAdvSwapOut)
	c.Assert(k.MintToModule(f.ctx, ModuleName, minted), IsNil)
	c.Assert(k.SendFromModuleToModule(f.ctx, ModuleName, AsgardName, common.NewCoins(minted)), IsNil)

	f.withIntermediateRune(c)

	f.errata(c)
	c.Assert(k.GetTotalSupply(f.ctx, synth).IsZero(), Equals, true)
	c.Assert(f.poolAsset(c, common.ETHAsset), Equals, errataAdvSwapPoolBalance.String())
	c.Check(f.pool(c, common.ETHAsset).BalanceRune.String(), Equals, errataAdvSwapPoolBalance.String())
	c.Check(f.pool(c, common.BTCAsset).BalanceRune.String(), Equals, errataAdvSwapPoolBalance.String())
	f.assertNothingPaid(c)
}

var errataAdvSwapAffiliateFee = cosmos.NewUint(10_000_000)

// withAffiliateFee re-queues the main swap with the affiliate fee carved out of
// its deposit. Production never builds this shape: affiliate fees are skimmed
// from the main swap's output (see queueOutputFundedAffiliate), so the main
// deposit always equals the inbound. Tests using it guard the debit math in case
// a future change shrinks the main deposit.
func (f errataAdvSwapFixture) withAffiliateFee(c *C) {
	f.swap.State.Deposit = common.SafeSub(f.source.Amount, errataAdvSwapAffiliateFee)
	c.Assert(f.mgr.Keeper().SetAdvSwapQueueItem(f.ctx, f.swap), IsNil)
}

func (f errataAdvSwapFixture) assertSourceDebit(c *C, debit cosmos.Uint) {
	pool, err := f.mgr.Keeper().GetPool(f.ctx, common.BTCAsset)
	c.Assert(err, IsNil)
	c.Assert(pool.BalanceAsset.String(), Equals, common.SafeSub(errataAdvSwapPoolBalance, debit).String())
}

// Affiliate sub-swaps share the inbound's tx id at non-contiguous indices
// (index 1 is never used), and every one of them must be purged and unwound.
// Hypothetical inbound-funded shape; see withAffiliateFee.
func (s *HandlerErrataAdvSwapSuite) TestErrataPurgesAffiliateSubSwaps(c *C) {
	f := setupErrataAdvSwap(c)
	f.withAffiliateFee(c)
	k := f.mgr.Keeper()
	affiliate := f.swap
	affiliate.Index = 2
	affiliate.SwapType = types.SwapType_market
	affiliate.Destination = GetRandomTHORAddress()
	affiliate.State = &types.SwapState{
		Quantity: 1,
		Deposit:  errataAdvSwapAffiliateFee,
		In:       errataAdvSwapAffiliateFee,
		Out:      errataAdvSwapOut,
	}
	c.Assert(k.SetAdvSwapQueueItem(f.ctx, affiliate), IsNil)

	f.errata(c)
	c.Assert(k.HasAdvSwapQueueItem(f.ctx, f.txID, 0), Equals, false)
	c.Assert(k.HasAdvSwapQueueItem(f.ctx, f.txID, 2), Equals, false)
	f.assertSourceDebit(c, errataAdvSwapIn.Add(errataAdvSwapAffiliateFee))
	pool, err := k.GetPool(f.ctx, common.BTCAsset)
	c.Assert(err, IsNil)
	c.Assert(pool.BalanceRune.String(), Equals, errataAdvSwapPoolBalance.Add(errataAdvSwapOut.MulUint64(2)).String())
	f.assertNothingPaid(c)
}

// An inbound-funded sub-swap that already settled took its input into the pool,
// so that input is debited even though only the main swap is still queued.
// Hypothetical shape; see withAffiliateFee.
func (s *HandlerErrataAdvSwapSuite) TestErrataDebitsSettledAffiliateInput(c *C) {
	f := setupErrataAdvSwap(c)
	f.withAffiliateFee(c)
	f.errata(c)
	f.assertSourceDebit(c, errataAdvSwapIn.Add(errataAdvSwapAffiliateFee))
	f.assertNothingPaid(c)
}

// Defense in depth: an affiliate collector conversion of a reverted inbound
// delivers nothing to the collector at settlement.
func (s *HandlerErrataAdvSwapSuite) TestRevertedInboundCollectorConversionPaysNothing(c *C) {
	f := setupErrataAdvSwap(c)
	k := f.mgr.Keeper()
	affAddr := GetRandomTHORAddress()
	conversion := f.swap
	conversion.Index = 2
	conversion.SwapType = types.SwapType_market
	conversion.Tx.Memo = fmt.Sprintf("=:THOR.RUNE:%s:0:tn", affAddr)
	conversion.AffiliateAddress = affAddr
	conversion.Destination = common.Address(k.GetModuleAccAddress(AffiliateCollectorName).String())
	conversion.State = &types.SwapState{
		Quantity: 1,
		Deposit:  errataAdvSwapIn,
		In:       errataAdvSwapIn,
		Out:      errataAdvSwapOut,
	}
	c.Assert(affiliateCollectorConversionName(k, conversion), Equals, "tn")
	c.Assert(k.SetAdvSwapQueueItem(f.ctx, conversion), IsNil)

	f.markReverted(c)
	c.Assert(settleSwap(f.ctx, f.mgr, conversion, "done"), IsNil)
	c.Assert(f.out.items, HasLen, 0)
	c.Assert(k.HasAdvSwapQueueItem(f.ctx, f.txID, 2), Equals, false)
}

// queueOutputFundedAffiliate settles the main swap into the given output asset
// and queues its affiliate fee swap the way production does, from that output.
func queueOutputFundedAffiliate(c *C, target common.Asset) (errataAdvSwapFixture, cosmos.Uint) {
	f := setupErrataAdvSwapTo(c, target, GetRandomTHORAddress())
	k := f.mgr.Keeper()
	k.SetMimir(f.ctx, "EnableAdvSwapQueue", 1)
	c.Assert(k.RemoveAdvSwapQueueItem(f.ctx, f.txID, 0), IsNil)

	voter, err := k.GetObservedTxInVoter(f.ctx, f.txID)
	c.Assert(err, IsNil)
	voter.Tx.Tx.Memo = fmt.Sprintf("=:%s:%s::%s:1000", target, f.swap.Destination, GetRandomTHORAddress())
	k.SetObservedTxInVoter(f.ctx, voter)

	settled := common.NewCoin(target, errataAdvSwapOut)
	mainTx := voter.Tx.Tx
	mainTx.Coins = common.NewCoins(settled)
	fee, err := skimAffiliateFees(f.ctx, f.mgr, mainTx, f.na.NodeAddress, settled, mainTx.Memo)
	c.Assert(err, IsNil)
	c.Assert(fee.IsZero(), Equals, false)
	indices, err := k.GetAdvSwapQueueItemIndices(f.ctx, f.txID)
	c.Assert(err, IsNil)
	c.Assert(indices, DeepEquals, []int{2})
	return f, fee
}

func (f errataAdvSwapFixture) pool(c *C, asset common.Asset) Pool {
	pool, err := f.mgr.Keeper().GetPool(f.ctx, asset)
	c.Assert(err, IsNil)
	return pool
}

// In production an affiliate fee swap is funded from the main swap's output when
// the main swap settles. If the errata lands while that affiliate swap is still
// queued, the whole inbound already entered the source pool and is debited, and
// the affiliate's unswapped output-asset deposit returns to its pool.
func (s *HandlerErrataAdvSwapSuite) TestErrataReturnsOutputFundedAffiliateDeposit(c *C) {
	f, fee := queueOutputFundedAffiliate(c, common.ETHAsset)
	before := f.pool(c, common.ETHAsset)

	f.errata(c)
	c.Assert(f.mgr.Keeper().HasAdvSwapQueueItem(f.ctx, f.txID, 2), Equals, false)
	f.assertSourceDebit(c, f.source.Amount)
	after := f.pool(c, common.ETHAsset)
	c.Assert(after.BalanceAsset.String(), Equals, before.BalanceAsset.Add(fee).String())
	c.Assert(after.BalanceRune.String(), Equals, before.BalanceRune.String())
	c.Assert(f.out.items, HasLen, 0)
}

// A partly filled output-funded swap already put its filled input into the pool,
// so only the unswapped remainder returns as asset and the filled output as RUNE.
func (s *HandlerErrataAdvSwapSuite) TestErrataUnwindsPartlyFilledOutputFundedSwap(c *C) {
	f, fee := queueOutputFundedAffiliate(c, common.ETHAsset)
	k := f.mgr.Keeper()
	item, err := k.GetAdvSwapQueueItem(f.ctx, f.txID, 2)
	c.Assert(err, IsNil)
	filledIn := fee.QuoUint64(4)
	filledOut := cosmos.NewUint(common.One)
	item.State.In = filledIn
	item.State.Out = filledOut
	c.Assert(k.SetAdvSwapQueueItem(f.ctx, item), IsNil)
	before := f.pool(c, common.ETHAsset)

	f.errata(c)
	after := f.pool(c, common.ETHAsset)
	c.Assert(after.BalanceAsset.String(), Equals, before.BalanceAsset.Add(common.SafeSub(fee, filledIn)).String())
	c.Assert(after.BalanceRune.String(), Equals, before.BalanceRune.Add(filledOut).String())
}

// A trade or secured output is credited to the recipient in full; the affiliate
// swap withdraws its deposit from that account only when it executes. A purged
// unexecuted affiliate swap therefore leaves the pool untouched.
func (s *HandlerErrataAdvSwapSuite) TestErrataKeepsTradeAndSecuredAffiliateDepositOutOfPool(c *C) {
	for _, target := range []common.Asset{common.ETHAsset.GetTradeAsset(), common.ETHAsset.GetSecuredAsset()} {
		f, _ := queueOutputFundedAffiliate(c, target)
		before := f.pool(c, common.ETHAsset)

		f.errata(c)
		c.Check(f.mgr.Keeper().HasAdvSwapQueueItem(f.ctx, f.txID, 2), Equals, false, Commentf("%s", target))
		f.assertSourceDebit(c, f.source.Amount)
		after := f.pool(c, common.ETHAsset)
		c.Check(after.BalanceAsset.String(), Equals, before.BalanceAsset.String(), Commentf("%s", target))
		c.Check(after.BalanceRune.String(), Equals, before.BalanceRune.String(), Commentf("%s", target))
	}
}

// errataAdvSwapIntermediateRune is the RUNE an executed BTC -> ETH sub-swap
// moved from the BTC pool to the ETH pool.
var errataAdvSwapIntermediateRune = cosmos.NewUint(3 * common.One)

// withIntermediateRune records the RUNE the queued double swap moved from the
// BTC pool to the target pool and moves it.
func (f errataAdvSwapFixture) withIntermediateRune(c *C) {
	k := f.mgr.Keeper()
	btc := f.pool(c, common.BTCAsset)
	btc.BalanceRune = common.SafeSub(btc.BalanceRune, errataAdvSwapIntermediateRune)
	c.Assert(k.SetPool(f.ctx, btc), IsNil)
	target := f.pool(c, f.target.Asset.GetLayer1Asset())
	target.BalanceRune = target.BalanceRune.Add(errataAdvSwapIntermediateRune)
	c.Assert(k.SetPool(f.ctx, target), IsNil)
	f.swap.State.IntermediateRune = errataAdvSwapIntermediateRune
	c.Assert(k.SetAdvSwapQueueItem(f.ctx, f.swap), IsNil)
}

func (s *HandlerErrataAdvSwapSuite) TestErrataDoubleSwapReturnsIntermediateRune(c *C) {
	f := setupErrataAdvSwapTo(c, common.ETHAsset, GetRandomETHAddress())
	f.withIntermediateRune(c)

	f.errata(c)
	c.Check(f.pool(c, common.BTCAsset).BalanceRune.String(), Equals, errataAdvSwapPoolBalance.String())
	c.Check(f.pool(c, common.ETHAsset).BalanceRune.String(), Equals, errataAdvSwapPoolBalance.String())
	c.Check(f.poolAsset(c, common.ETHAsset), Equals, errataAdvSwapPoolBalance.Add(errataAdvSwapOut).String())
}

// A queue item stored before SwapState had the intermediate RUNE field decodes
// with a nil amount, which unwinds as zero.
func (s *HandlerErrataAdvSwapSuite) TestErrataUnwindsStateWithoutIntermediateRune(c *C) {
	f := setupErrataAdvSwapTo(c, common.ETHAsset, GetRandomETHAddress())
	f.swap.State.IntermediateRune = cosmos.Uint{}
	c.Assert(f.swap.State.GetIntermediateRuneOrZero().IsZero(), Equals, true)
	er := &common.ErrataTx{Id: f.txID, Chain: common.BTCChain}
	c.Assert(unwindRevertedAdvSwaps(f.ctx, f.mgr, er, common.NewCoins(f.source), []MsgSwap{f.swap}), IsNil)
	c.Check(f.pool(c, common.BTCAsset).BalanceRune.String(), Equals, errataAdvSwapPoolBalance.String())
	c.Check(f.pool(c, common.ETHAsset).BalanceRune.String(), Equals, errataAdvSwapPoolBalance.String())
}

// A double swap through a single pool moved no RUNE between pools.
func (s *HandlerErrataAdvSwapSuite) TestErrataSinglePoolDoubleSwapMovesNoRune(c *C) {
	synth := common.BTCAsset.GetSyntheticAsset()
	f := setupErrataAdvSwapTo(c, synth, GetRandomTHORAddress())
	k := f.mgr.Keeper()
	minted := common.NewCoin(synth, errataAdvSwapOut)
	c.Assert(k.MintToModule(f.ctx, ModuleName, minted), IsNil)
	c.Assert(k.SendFromModuleToModule(f.ctx, ModuleName, AsgardName, common.NewCoins(minted)), IsNil)
	f.swap.State.IntermediateRune = errataAdvSwapIntermediateRune
	c.Assert(k.SetAdvSwapQueueItem(f.ctx, f.swap), IsNil)

	f.errata(c)
	c.Assert(k.GetTotalSupply(f.ctx, synth).IsZero(), Equals, true)
	c.Check(f.pool(c, common.BTCAsset).BalanceRune.String(), Equals, errataAdvSwapPoolBalance.String())
}

// A partly executed streaming swap is purged, emits its terminal streaming swap
// event and is unwound like a limit swap.
func (s *HandlerErrataAdvSwapSuite) TestErrataPurgesStreamingSwap(c *C) {
	f := setupErrataAdvSwap(c)
	k := f.mgr.Keeper()
	c.Assert(k.RemoveAdvSwapQueueItem(f.ctx, f.txID, 0), IsNil)
	f.swap.SwapType = types.SwapType_market
	f.swap.StreamInterval = 1
	f.swap.State.Quantity = 5
	f.swap.State.Count = 2
	c.Assert(f.swap.IsStreaming(), Equals, true)
	c.Assert(k.SetAdvSwapQueueItem(f.ctx, f.swap), IsNil)

	f.errata(c)
	c.Assert(k.HasAdvSwapQueueItem(f.ctx, f.txID, 0), Equals, false)
	streams := f.eventAttrs(types.StreamingSwapEventType)
	c.Assert(streams, HasLen, 1)
	c.Check(streams[0]["tx_id"], Equals, f.txID.String())
	c.Check(streams[0]["quantity"], Equals, "5")
	c.Check(streams[0]["count"], Equals, "2")
	// the purge closes the stream without recording the unpaid output
	c.Check(streams[0]["out"], Equals, common.NewCoin(f.target.Asset, cosmos.ZeroUint()).String())
	c.Assert(f.eventAttrs(types.LimitSwapCloseEventType), HasLen, 0)
	pool := f.pool(c, common.BTCAsset)
	c.Assert(pool.BalanceAsset.String(), Equals, common.SafeSub(errataAdvSwapPoolBalance, errataAdvSwapIn).String())
	c.Assert(pool.BalanceRune.String(), Equals, errataAdvSwapPoolBalance.Add(errataAdvSwapOut).String())
	f.expire(c)
	f.assertNothingPaid(c)
}

// A stored queue item without swap state is purged without panicking.
func (s *HandlerErrataAdvSwapSuite) TestErrataPurgesItemWithoutState(c *C) {
	f := setupErrataAdvSwap(c)
	k := f.mgr.Keeper()
	stateless := f.swap
	stateless.Index = 2
	stateless.State = nil
	c.Assert(k.SetAdvSwapQueueItem(f.ctx, stateless), IsNil)

	f.errata(c)
	c.Assert(k.HasAdvSwapQueueItem(f.ctx, f.txID, 0), Equals, false)
	c.Assert(k.HasAdvSwapQueueItem(f.ctx, f.txID, 2), Equals, false)
	f.assertNothingPaid(c)
}

// The unwind does not depend on re-parsing the inbound memo.
func (s *HandlerErrataAdvSwapSuite) TestErrataUnwindsWhenMemoNoLongerParses(c *C) {
	f := setupErrataAdvSwap(c)
	k := f.mgr.Keeper()
	voter, err := k.GetObservedTxInVoter(f.ctx, f.txID)
	c.Assert(err, IsNil)
	voter.Tx.Tx.Memo = fmt.Sprintf("=<:THOR.RUNE:%s:%s:nosuchname:50", f.swap.Destination, f.target.Amount)
	_, err = ParseMemoWithTHORNames(f.ctx, k, voter.Tx.Tx.Memo)
	c.Assert(err, NotNil)
	k.SetObservedTxInVoter(f.ctx, voter)

	f.errata(c)
	c.Assert(k.HasAdvSwapQueueItem(f.ctx, f.txID, 0), Equals, false)
	f.assertSourceDebit(c, errataAdvSwapIn)
}

// On a depleted pool the debit is capped and the errata event reports the
// capped amount.
func (s *HandlerErrataAdvSwapSuite) TestErrataCapsDebitAtPoolDepth(c *C) {
	f := setupErrataAdvSwap(c)
	k := f.mgr.Keeper()
	pool := f.pool(c, common.BTCAsset)
	depth := cosmos.NewUint(1000)
	pool.BalanceAsset = depth
	c.Assert(k.SetPool(f.ctx, pool), IsNil)

	f.errata(c)
	c.Assert(f.pool(c, common.BTCAsset).BalanceAsset.IsZero(), Equals, true)
	found := false
	for _, e := range f.ctx.EventManager().Events() {
		if e.Type != "errata" {
			continue
		}
		for _, a := range e.Attributes {
			if a.Key == "asset_amt" && a.Value == depth.String() {
				found = true
			}
		}
	}
	c.Assert(found, Equals, true)
}

// A saver withdrawal queues its synth redemption under the withdraw inbound's tx
// id, funded from the saver's existing claim. Reverting the withdraw inbound
// must neither purge nor block that redemption.
func (s *HandlerErrataAdvSwapSuite) TestErrataKeepsSaverRedemption(c *C) {
	f := setupErrataAdvSwap(c)
	k := f.mgr.Keeper()
	voter, err := k.GetObservedTxInVoter(f.ctx, f.txID)
	c.Assert(err, IsNil)
	voter.Tx.Tx.Memo = "-:BTC/BTC:10000"
	k.SetObservedTxInVoter(f.ctx, voter)

	synth := common.NewCoin(common.BTCAsset.GetSyntheticAsset(), f.source.Amount)
	redemption := f.swap
	redemption.Tx.Coins = common.NewCoins(synth)
	redemption.Tx.Memo = fmt.Sprintf("=:BTC.BTC:%s", f.from)
	redemption.TargetAsset = common.BTCAsset
	redemption.Destination = f.from
	redemption.SwapType = types.SwapType_market
	redemption.TradeTarget = cosmos.ZeroUint()
	c.Assert(k.SetAdvSwapQueueItem(f.ctx, redemption), IsNil)
	btc := f.pool(c, common.BTCAsset)

	f.errata(c)
	c.Assert(k.HasAdvSwapQueueItem(f.ctx, f.txID, 0), Equals, true)
	after := f.pool(c, common.BTCAsset)
	c.Assert(after.BalanceAsset.String(), Equals, btc.BalanceAsset.String())
	c.Assert(after.BalanceRune.String(), Equals, btc.BalanceRune.String())

	c.Assert(settleSwap(f.ctx, f.mgr, redemption, "done"), IsNil)
	c.Assert(f.out.items, HasLen, 2)
	c.Assert(f.out.items[0].Coin.Asset.Equals(redemption.TargetAsset), Equals, true)
	c.Assert(f.out.items[1].Coin.Asset.Equals(synth.Asset), Equals, true)
}

func (s *HandlerErrataAdvSwapSuite) TestIsSwapInboundMemo(c *C) {
	for memo, want := range map[string]bool{
		"=:ETH.ETH:addr":        true,
		"s:ETH.ETH:addr":        true,
		"SWAP:ETH.ETH:addr":     true,
		"=<:ETH.ETH:addr:1":     true,
		"-:BTC/BTC:10000":       false,
		"m=<:1BTC.BTC:2ETH.ETH": false,
		"r:123":                 false,
		"+:BTC.BTC":             false,
		"":                      false,
		"not a memo":            false,
	} {
		c.Check(isSwapInboundMemo(memo), Equals, want, Commentf("%q", memo))
	}
}

// A synth output that cannot be burned does not block the revert: the swap is
// still purged and nothing is paid, and the stranded synth is flagged with a
// security event.
func (s *HandlerErrataAdvSwapSuite) TestErrataFlagsUnburnableSynthOutput(c *C) {
	synth := common.ETHAsset.GetSyntheticAsset()
	f := setupErrataAdvSwapTo(c, synth, GetRandomTHORAddress())
	f.withIntermediateRune(c)

	f.errata(c)
	c.Assert(f.mgr.Keeper().HasAdvSwapQueueItem(f.ctx, f.txID, 0), Equals, false)
	f.assertSourceDebit(c, errataAdvSwapIn)
	c.Assert(f.poolAsset(c, common.ETHAsset), Equals, errataAdvSwapPoolBalance.String())
	// the unburned synth keeps the intermediate RUNE that minted it
	c.Check(f.pool(c, common.ETHAsset).BalanceRune.String(), Equals, errataAdvSwapPoolBalance.Add(errataAdvSwapIntermediateRune).String())
	c.Check(f.pool(c, common.BTCAsset).BalanceRune.String(), Equals, common.SafeSub(errataAdvSwapPoolBalance, errataAdvSwapIntermediateRune).String())
	var msgOK, coinOK bool
	for _, e := range f.ctx.EventManager().Events() {
		if e.Type != "security" {
			continue
		}
		for _, a := range e.Attributes {
			switch a.Key {
			case "msg":
				msgOK = msgOK || strings.HasPrefix(a.Value, "errata: fail to burn reverted synth output")
			case "coin":
				coinOK = coinOK || a.Value == common.NewCoin(synth, errataAdvSwapOut).String()
			}
		}
	}
	c.Assert(msgOK, Equals, true)
	c.Assert(coinOK, Equals, true)
	f.expire(c)
	f.assertNothingPaid(c)
}
