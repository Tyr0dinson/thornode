//go:build mainnet

package thorchain

import (
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

type Migrate20to21SolBtcPayoutRepaySuite struct{}

var _ = Suite(&Migrate20to21SolBtcPayoutRepaySuite{})

// seedSolBtcStuckSwap seeds the stuck state for one SOL->BTC payout: an inbound voter
// whose matching BTC action carries the 80-byte TRUNCATED memo (so AddOutTx cannot
// match), plus a scheduled BTC outbound for it. When paid, the scheduled item carries
// a non-empty OutHash (as the bifrost recovery set it); when not, it stays empty.
// Returns the voter's in TxID and the BTC OutHash seeded on the paid item.
func seedSolBtcStuckSwap(c *C, ctx cosmos.Context, mgr *Mgrs, item solBtcPayoutRepair322Item, paid bool) (common.TxID, common.TxID) {
	k := mgr.Keeper()

	vault := GetRandomVault()
	vault.Status = ActiveVault
	vault.Type = AsgardVault
	c.Assert(k.SetVault(ctx, vault), IsNil)

	inTxID, err := common.NewTxID(item.inHash)
	c.Assert(err, IsNil)
	dest, err := common.NewAddress(item.dest)
	c.Assert(err, IsNil)
	coin := common.NewCoin(common.BTCAsset, cosmos.NewUint(item.sats))

	fullMemo := "OUT:" + inTxID.String()
	truncMemo := fullMemo[:80] // the 80-byte truncation that blocks matchActionItem

	action := TxOutItem{
		Chain:       common.BTCChain,
		InHash:      inTxID,
		ToAddress:   dest,
		VaultPubKey: vault.PubKey,
		Coin:        coin,
		Memo:        truncMemo,
	}

	// The scheduled outbound block. Paid items carry the recovery's OutHash.
	outHash := GetRandomTxHash()
	stored := action
	if paid {
		stored.OutHash = outHash
	}
	blk := NewTxOut(ctx.BlockHeight())
	blk.TxArray = []TxOutItem{stored}
	c.Assert(k.SetTxOut(ctx, blk), IsNil)

	voter := NewObservedTxVoter(inTxID, nil)
	voter.Height = ctx.BlockHeight()
	voter.FinalisedHeight = ctx.BlockHeight()
	voter.OutboundHeight = ctx.BlockHeight()
	voter.Tx = NewObservedTx(
		common.NewTx(inTxID, GetRandomBTCAddress(), GetRandomBTCAddress(), common.NewCoins(coin), nil, truncMemo),
		ctx.BlockHeight(), vault.PubKey, ctx.BlockHeight())
	voter.Txs = []common.ObservedTx{voter.Tx}
	voter.Actions = []TxOutItem{action}
	k.SetObservedTxInVoter(ctx, voter)

	return inTxID, outHash
}

func countOutboundEvents(ctx cosmos.Context) int {
	n := 0
	for _, e := range ctx.EventManager().Events() {
		if e.Type == types.OutboundEventType {
			n++
		}
	}
	return n
}

// A PAID item (OutHash set by the recovery) is repaired: the action memo is restored
// to the full OUT:<in_hash>, the BTC out tx is recorded in OutTxs with the live
// OutHash, the voter's Txs are SetDone, and one outbound event is emitted.
func (s *Migrate20to21SolBtcPayoutRepaySuite) TestSolBtcPayoutRepair322RepairsPaidItem(c *C) {
	ctx, mgr := setupManagerForTest(c)
	item := solBtcPayoutRepair322Items[0]
	inTxID, outHash := seedSolBtcStuckSwap(c, ctx, mgr, item, true)

	before := countOutboundEvents(ctx)
	NewMigrator(mgr).runSolBtcPayoutRepair322(ctx)

	voter, err := mgr.Keeper().GetObservedTxInVoter(ctx, inTxID)
	c.Assert(err, IsNil)
	// OutTxs now holds the BTC payment keyed by the live OutHash.
	c.Assert(voter.OutTxs, HasLen, 1)
	c.Check(voter.OutTxs[0].ID.Equals(outHash), Equals, true)
	c.Check(voter.OutTxs[0].Chain.Equals(common.BTCChain), Equals, true)
	c.Check(voter.OutTxs[0].ToAddress.String(), Equals, item.dest)
	c.Check(voter.OutTxs[0].Coins.Contains(common.NewCoin(common.BTCAsset, cosmos.NewUint(item.sats))), Equals, true)
	// The action memo was restored to the untruncated OUT:<in_hash>.
	c.Assert(voter.Actions, HasLen, 1)
	c.Check(voter.Actions[0].Memo, Equals, "OUT:"+inTxID.String())
	// The voter's Txs were SetDone (one action, one OutTx).
	c.Assert(voter.Txs, HasLen, 1)
	c.Check(voter.Txs[0].IsDone(len(voter.Actions)), Equals, true)
	// Exactly one outbound event was emitted.
	c.Check(countOutboundEvents(ctx)-before, Equals, 1)
}

// An UNPAID item (scheduled outbound OutHash still empty) is left completely
// untouched: no OutTx, action memo unchanged, no event. DevCodex's reschedule path
// owns these; this migration must never touch them.
func (s *Migrate20to21SolBtcPayoutRepaySuite) TestSolBtcPayoutRepair322LeavesUnpaidItem(c *C) {
	ctx, mgr := setupManagerForTest(c)
	item := solBtcPayoutRepair322Items[1]
	inTxID, _ := seedSolBtcStuckSwap(c, ctx, mgr, item, false)

	before := countOutboundEvents(ctx)
	NewMigrator(mgr).runSolBtcPayoutRepair322(ctx)

	voter, err := mgr.Keeper().GetObservedTxInVoter(ctx, inTxID)
	c.Assert(err, IsNil)
	c.Check(voter.OutTxs, HasLen, 0)
	c.Assert(voter.Actions, HasLen, 1)
	// Memo stays truncated (unchanged).
	c.Check(voter.Actions[0].Memo, Equals, ("OUT:" + inTxID.String())[:80])
	c.Check(voter.Txs[0].IsDone(len(voter.Actions)), Equals, false)
	c.Check(countOutboundEvents(ctx)-before, Equals, 0)
}

// Running the repair twice is idempotent: the second run adds no second OutTx and
// emits no second event.
func (s *Migrate20to21SolBtcPayoutRepaySuite) TestSolBtcPayoutRepair322Idempotent(c *C) {
	ctx, mgr := setupManagerForTest(c)
	item := solBtcPayoutRepair322Items[2]
	inTxID, outHash := seedSolBtcStuckSwap(c, ctx, mgr, item, true)

	NewMigrator(mgr).runSolBtcPayoutRepair322(ctx)
	afterFirst := countOutboundEvents(ctx)

	NewMigrator(mgr).runSolBtcPayoutRepair322(ctx)

	voter, err := mgr.Keeper().GetObservedTxInVoter(ctx, inTxID)
	c.Assert(err, IsNil)
	c.Assert(voter.OutTxs, HasLen, 1)
	c.Check(voter.OutTxs[0].ID.Equals(outHash), Equals, true)
	// No second outbound event on the re-run.
	c.Check(countOutboundEvents(ctx), Equals, afterFirst)
}

// The toggle gates the whole step: disabled repairs nothing.
func (s *Migrate20to21SolBtcPayoutRepaySuite) TestSolBtcPayoutRepair322Disabled(c *C) {
	ctx, mgr := setupManagerForTest(c)
	item := solBtcPayoutRepair322Items[3]
	inTxID, _ := seedSolBtcStuckSwap(c, ctx, mgr, item, true)

	orig := solBtcPayoutRepair322Enabled
	solBtcPayoutRepair322Enabled = false
	defer func() { solBtcPayoutRepair322Enabled = orig }()

	NewMigrator(mgr).runSolBtcPayoutRepair322(ctx)

	voter, err := mgr.Keeper().GetObservedTxInVoter(ctx, inTxID)
	c.Assert(err, IsNil)
	c.Check(voter.OutTxs, HasLen, 0)
}

// A missing voter soft-skips: no panic, nothing written.
func (s *Migrate20to21SolBtcPayoutRepaySuite) TestSolBtcPayoutRepair322MissingVoterSoftSkips(c *C) {
	ctx, mgr := setupManagerForTest(c)
	// Seed nothing; every voter is absent.
	NewMigrator(mgr).runSolBtcPayoutRepair322(ctx)

	for _, item := range solBtcPayoutRepair322Items {
		inTxID, err := common.NewTxID(item.inHash)
		c.Assert(err, IsNil)
		voter, err := mgr.Keeper().GetObservedTxInVoter(ctx, inTxID)
		c.Assert(err, IsNil)
		// Absent voter: no actions, nothing recorded.
		c.Check(voter.Actions, HasLen, 0)
		c.Check(voter.OutTxs, HasLen, 0)
	}
}
