package keeperv1

import (
	"github.com/cosmos/cosmos-sdk/runtime"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

type KeeperIntentSuite struct{}

var _ = Suite(&KeeperIntentSuite{})

func (KeeperIntentSuite) TestIntent_SetAndGet(c *C) {
	ctx, k := setupKeeperForTest(c)

	// Create an intent
	txID := common.TxID("ABCD1234567890ABCD1234567890ABCD1234567890ABCD1234567890ABCD1234")
	intent := types.Intent{
		TxID:          txID,
		SourceAddress: common.Address("thor1sender"),
		Source:        common.NewCoin(common.DOGEAsset, cosmos.NewUint(100)),
		TargetAsset:   common.BTCAsset,
		TargetAddress: common.Address("bc1qtest"),
		HighTarget:    cosmos.NewUint(20),
		LowTarget:     cosmos.NewUint(10),
		ExpiryHeight:  1000,
		Status:        types.IntentStatus_IntentActive,
	}

	// Set intent
	k.SetIntent(ctx, intent)

	// Get intent
	retrieved, err := k.GetIntent(ctx, txID)
	c.Assert(err, IsNil)
	c.Check(retrieved.TxID.Equals(txID), Equals, true)
	c.Check(retrieved.Status, Equals, types.IntentStatus_IntentActive)
	c.Check(retrieved.Source.Asset.Equals(common.DOGEAsset), Equals, true)
	c.Check(retrieved.TargetAsset.Equals(common.BTCAsset), Equals, true)
}

func (KeeperIntentSuite) TestIntent_Exists(c *C) {
	ctx, k := setupKeeperForTest(c)

	txID := common.TxID("ABCD1234567890ABCD1234567890ABCD1234567890ABCD1234567890ABCD1234")
	intent := types.Intent{
		TxID:          txID,
		SourceAddress: common.Address("thor1sender"),
		Source:        common.NewCoin(common.DOGEAsset, cosmos.NewUint(100)),
		TargetAsset:   common.BTCAsset,
		Status:        types.IntentStatus_IntentActive,
	}

	// Check non-existence
	exists := k.IntentExists(ctx, txID)
	c.Check(exists, Equals, false)

	// Set and check existence
	k.SetIntent(ctx, intent)
	exists = k.IntentExists(ctx, txID)
	c.Check(exists, Equals, true)
}

func (KeeperIntentSuite) TestIntent_ActiveIndex(c *C) {
	ctx, k := setupKeeperForTest(c)

	// Create active intent
	txID1 := common.TxID("AAAA1234567890ABCD1234567890ABCD1234567890ABCD1234567890ABCD1234")
	intent1 := types.Intent{
		TxID:          txID1,
		SourceAddress: common.Address("thor1sender1"),
		Source:        common.NewCoin(common.DOGEAsset, cosmos.NewUint(100)),
		TargetAsset:   common.BTCAsset,
		Status:        types.IntentStatus_IntentActive,
	}
	k.SetIntent(ctx, intent1)

	// Create cancelled intent
	txID2 := common.TxID("BBBB1234567890ABCD1234567890ABCD1234567890ABCD1234567890ABCD1234")
	intent2 := types.Intent{
		TxID:          txID2,
		SourceAddress: common.Address("thor1sender2"),
		Source:        common.NewCoin(common.DOGEAsset, cosmos.NewUint(100)),
		TargetAsset:   common.BTCAsset,
		Status:        types.IntentStatus_IntentCancelled,
	}
	k.SetIntent(ctx, intent2)

	// Get active intents - should only return the active one
	activeIntents, err := k.GetActiveIntents(ctx)
	c.Assert(err, IsNil)
	c.Check(len(activeIntents), Equals, 1)
	c.Check(activeIntents[0].TxID.Equals(txID1), Equals, true)
	c.Check(activeIntents[0].Status, Equals, types.IntentStatus_IntentActive)

	// Add another active intent
	txID3 := common.TxID("CCCC1234567890ABCD1234567890ABCD1234567890ABCD1234567890ABCD1234")
	intent3 := types.Intent{
		TxID:          txID3,
		SourceAddress: common.Address("thor1sender3"),
		Source:        common.NewCoin(common.DOGEAsset, cosmos.NewUint(100)),
		TargetAsset:   common.BTCAsset,
		Status:        types.IntentStatus_IntentActive,
	}
	k.SetIntent(ctx, intent3)

	// Get active intents - should return both active ones
	activeIntents, err = k.GetActiveIntents(ctx)
	c.Assert(err, IsNil)
	c.Check(len(activeIntents), Equals, 2)

	// Update first intent to completed
	intent1.Status = types.IntentStatus_IntentCompleted
	k.SetIntent(ctx, intent1)

	// Get active intents - should only return the third one now
	activeIntents, err = k.GetActiveIntents(ctx)
	c.Assert(err, IsNil)
	c.Check(len(activeIntents), Equals, 1)
	c.Check(activeIntents[0].TxID.Equals(txID3), Equals, true)
	c.Check(activeIntents[0].Status, Equals, types.IntentStatus_IntentActive)

	// Update third intent to expired
	intent3.Status = types.IntentStatus_IntentExpired
	k.SetIntent(ctx, intent3)

	// Get active intents - should return empty list
	activeIntents, err = k.GetActiveIntents(ctx)
	c.Assert(err, IsNil)
	c.Check(len(activeIntents), Equals, 0)
}

func (KeeperIntentSuite) TestIntent_TerminalSolveRefundIndex(c *C) {
	ctx, k := setupKeeperForTest(c)

	intentTxID := common.TxID("TERMIDX1234567890ABCD1234567890ABCD1234567890ABCD1234567890ABC1")
	intent := types.Intent{
		TxID:          intentTxID,
		SourceAddress: common.Address("thor1sender1"),
		Source:        common.NewCoin(common.DOGEAsset, cosmos.NewUint(100)),
		TargetAsset:   common.BTCAsset,
		Status:        types.IntentStatus_IntentActive,
	}
	k.SetIntent(ctx, intent)

	solveTxID := common.TxID("TERMSOLVE1234567890ABCD1234567890ABCD1234567890ABCD1234567890AB1")
	solve := types.IntentSolve{
		SolveTxID:     solveTxID,
		IntentTxID:    intentTxID,
		SolverAddress: common.Address("thor1solver1"),
		TargetCoin:    common.NewCoin(common.BTCAsset, cosmos.NewUint(15)),
		MatchHeight:   100,
	}
	k.SetIntentSolve(ctx, solve)

	terminalIntents, err := k.GetTerminalSolveRefundIntents(ctx)
	c.Assert(err, IsNil)
	c.Check(len(terminalIntents), Equals, 0)

	intent.Status = types.IntentStatus_IntentCompleted
	k.SetIntent(ctx, intent)

	terminalIntents, err = k.GetTerminalSolveRefundIntents(ctx)
	c.Assert(err, IsNil)
	c.Check(len(terminalIntents), Equals, 1)
	c.Check(terminalIntents[0].TxID.Equals(intentTxID), Equals, true)

	k.RemoveIntentSolve(ctx, solveTxID)

	terminalIntents, err = k.GetTerminalSolveRefundIntents(ctx)
	c.Assert(err, IsNil)
	c.Check(len(terminalIntents), Equals, 0)

	newSolveTxID := common.TxID("TERMSOLVE2234567890ABCD1234567890ABCD1234567890ABCD1234567890AB2")
	solve.SolveTxID = newSolveTxID
	k.SetIntentSolve(ctx, solve)

	terminalIntents, err = k.GetTerminalSolveRefundIntents(ctx)
	c.Assert(err, IsNil)
	c.Check(len(terminalIntents), Equals, 1)
	c.Check(terminalIntents[0].TxID.Equals(intentTxID), Equals, true)
}

func (KeeperIntentSuite) TestIntent_RemoveIntent(c *C) {
	ctx, k := setupKeeperForTest(c)

	// Create two active intents
	txID1 := common.TxID("AAAA1234567890ABCD1234567890ABCD1234567890ABCD1234567890ABCD1234")
	intent1 := types.Intent{
		TxID:          txID1,
		SourceAddress: common.Address("thor1sender1"),
		Source:        common.NewCoin(common.DOGEAsset, cosmos.NewUint(100)),
		TargetAsset:   common.BTCAsset,
		Status:        types.IntentStatus_IntentActive,
	}
	k.SetIntent(ctx, intent1)

	txID2 := common.TxID("BBBB1234567890ABCD1234567890ABCD1234567890ABCD1234567890ABCD1234")
	intent2 := types.Intent{
		TxID:          txID2,
		SourceAddress: common.Address("thor1sender2"),
		Source:        common.NewCoin(common.DOGEAsset, cosmos.NewUint(100)),
		TargetAsset:   common.BTCAsset,
		Status:        types.IntentStatus_IntentActive,
	}
	k.SetIntent(ctx, intent2)

	// Verify both are in active index
	activeIntents, err := k.GetActiveIntents(ctx)
	c.Assert(err, IsNil)
	c.Check(len(activeIntents), Equals, 2)

	// Remove first intent
	k.RemoveIntent(ctx, txID1)

	// Verify it no longer exists
	c.Check(k.IntentExists(ctx, txID1), Equals, false)

	// Verify it's removed from active index
	activeIntents, err = k.GetActiveIntents(ctx)
	c.Assert(err, IsNil)
	c.Check(len(activeIntents), Equals, 1)
	c.Check(activeIntents[0].TxID.Equals(txID2), Equals, true)
	c.Check(activeIntents[0].Status, Equals, types.IntentStatus_IntentActive)

	// Remove second intent
	k.RemoveIntent(ctx, txID2)

	// Verify active index is now empty
	activeIntents, err = k.GetActiveIntents(ctx)
	c.Assert(err, IsNil)
	c.Check(len(activeIntents), Equals, 0)
}

func (KeeperIntentSuite) TestIntent_RemoveIntentCascading(c *C) {
	ctx, k := setupKeeperForTest(c)

	intentTxID := common.TxID("CASC1234567890ABCD1234567890ABCD1234567890ABCD1234567890ABCD1234")
	intent := types.Intent{
		TxID:          intentTxID,
		SourceAddress: common.Address("thor1sender1"),
		Source:        common.NewCoin(common.DOGEAsset, cosmos.NewUint(100)),
		TargetAsset:   common.BTCAsset,
		Status:        types.IntentStatus_IntentActive,
	}
	k.SetIntent(ctx, intent)

	// Create associated solves
	solveTxID1 := common.TxID("SOLVA1234567890ABCD1234567890ABCD1234567890ABCD1234567890ABCD123")
	solve1 := types.IntentSolve{
		SolveTxID:     solveTxID1,
		IntentTxID:    intentTxID,
		SolverAddress: common.Address("thor1solver1"),
		TargetCoin:    common.NewCoin(common.BTCAsset, cosmos.NewUint(15)),
		MatchHeight:   100,
	}
	k.SetIntentSolve(ctx, solve1)

	solveTxID2 := common.TxID("SOLVB1234567890ABCD1234567890ABCD1234567890ABCD1234567890ABCD123")
	solve2 := types.IntentSolve{
		SolveTxID:     solveTxID2,
		IntentTxID:    intentTxID,
		SolverAddress: common.Address("thor1solver2"),
		TargetCoin:    common.NewCoin(common.BTCAsset, cosmos.NewUint(18)),
		MatchHeight:   200,
	}
	k.SetIntentSolve(ctx, solve2)

	// Create associated auction
	auction := types.IntentAuction{
		IntentTxID:   intentTxID,
		Status:       types.IntentAuctionStatus_AuctionActive,
		ExpiryHeight: 500,
	}
	k.SetIntentAuction(ctx, auction)

	// Create associated bids
	bidTxID1 := common.TxID("BIDA1234567890ABCD1234567890ABCD1234567890ABCD1234567890ABCD1234")
	bid1 := types.IntentAuctionBid{
		BidTxID:       bidTxID1,
		IntentTxID:    intentTxID,
		BidderAddress: common.Address("thor1bidder1"),
		BidAmount:     common.NewCoin(common.RuneAsset(), cosmos.NewUint(10)),
	}
	k.SetIntentAuctionBid(ctx, bid1)

	bidTxID2 := common.TxID("BIDB1234567890ABCD1234567890ABCD1234567890ABCD1234567890ABCD1234")
	bid2 := types.IntentAuctionBid{
		BidTxID:       bidTxID2,
		IntentTxID:    intentTxID,
		BidderAddress: common.Address("thor1bidder2"),
		BidAmount:     common.NewCoin(common.RuneAsset(), cosmos.NewUint(12)),
	}
	k.SetIntentAuctionBid(ctx, bid2)

	// Verify everything exists before removal
	c.Check(k.IntentExists(ctx, intentTxID), Equals, true)
	c.Check(k.IntentSolveExists(ctx, solveTxID1), Equals, true)
	c.Check(k.IntentSolveExists(ctx, solveTxID2), Equals, true)
	c.Check(k.IntentAuctionExists(ctx, intentTxID), Equals, true)
	c.Check(k.IntentAuctionBidExists(ctx, bidTxID1), Equals, true)
	c.Check(k.IntentAuctionBidExists(ctx, bidTxID2), Equals, true)

	// Remove intent - should cascade to all child records
	k.RemoveIntent(ctx, intentTxID)

	// Verify intent is gone
	c.Check(k.IntentExists(ctx, intentTxID), Equals, false)

	// Verify solves are gone
	c.Check(k.IntentSolveExists(ctx, solveTxID1), Equals, false)
	c.Check(k.IntentSolveExists(ctx, solveTxID2), Equals, false)
	solves, err := k.GetIntentSolvesByIntentID(ctx, intentTxID)
	c.Assert(err, IsNil)
	c.Check(len(solves), Equals, 0)

	// Verify auction is gone
	c.Check(k.IntentAuctionExists(ctx, intentTxID), Equals, false)

	// Verify bids are gone
	c.Check(k.IntentAuctionBidExists(ctx, bidTxID1), Equals, false)
	c.Check(k.IntentAuctionBidExists(ctx, bidTxID2), Equals, false)
	bids, err := k.GetIntentAuctionBidsByIntentID(ctx, intentTxID)
	c.Assert(err, IsNil)
	c.Check(len(bids), Equals, 0)
}

func (KeeperIntentSuite) TestIntentSolve_SetAndGet(c *C) {
	ctx, k := setupKeeperForTest(c)

	solveTxID := common.TxID("SOLVE1234567890ABCD1234567890ABCD1234567890ABCD1234567890ABCD123")
	intentTxID := common.TxID("INTENT123456789ABCD1234567890ABCD1234567890ABCD1234567890ABCD123")

	solve := types.IntentSolve{
		SolveTxID:     solveTxID,
		IntentTxID:    intentTxID,
		SolverAddress: common.Address("thor1solver"),
		TargetCoin:    common.NewCoin(common.BTCAsset, cosmos.NewUint(15)),
		MatchHeight:   500,
	}

	// Set solve
	k.SetIntentSolve(ctx, solve)

	// Get solve
	retrieved, err := k.GetIntentSolve(ctx, solveTxID)
	c.Assert(err, IsNil)
	c.Check(retrieved.SolveTxID.Equals(solveTxID), Equals, true)
	c.Check(retrieved.IntentTxID.Equals(intentTxID), Equals, true)
	c.Check(retrieved.MatchHeight, Equals, int64(500))
}

func (KeeperIntentSuite) TestIntentSolve_GetByIntentID(c *C) {
	ctx, k := setupKeeperForTest(c)

	intentTxID := common.TxID("INTENT123456789ABCD1234567890ABCD1234567890ABCD1234567890ABCD123")

	// Create multiple solves for the same intent
	solve1 := types.IntentSolve{
		SolveTxID:     common.TxID("SOLVE11234567890ABCD1234567890ABCD1234567890ABCD1234567890ABCD123"),
		IntentTxID:    intentTxID,
		SolverAddress: common.Address("thor1solver1"),
		TargetCoin:    common.NewCoin(common.BTCAsset, cosmos.NewUint(15)),
		MatchHeight:   500,
	}
	k.SetIntentSolve(ctx, solve1)

	solve2 := types.IntentSolve{
		SolveTxID:     common.TxID("SOLVE21234567890ABCD1234567890ABCD1234567890ABCD1234567890ABCD123"),
		IntentTxID:    intentTxID,
		SolverAddress: common.Address("thor1solver2"),
		TargetCoin:    common.NewCoin(common.BTCAsset, cosmos.NewUint(18)),
		MatchHeight:   600,
	}
	k.SetIntentSolve(ctx, solve2)

	// Get solves by intent ID
	solves, err := k.GetIntentSolvesByIntentID(ctx, intentTxID)
	c.Assert(err, IsNil)
	c.Check(len(solves), Equals, 2)
}

func (KeeperIntentSuite) TestIntentSolve_GetByMatchHeight(c *C) {
	ctx, k := setupKeeperForTest(c)

	matchHeight := int64(500)

	// Create solves with same match height
	solve1 := types.IntentSolve{
		SolveTxID:     common.TxID("SOLVE11234567890ABCD1234567890ABCD1234567890ABCD1234567890ABCD123"),
		IntentTxID:    common.TxID("INTENT1234567890ABCD1234567890ABCD1234567890ABCD1234567890ABCD123"),
		SolverAddress: common.Address("thor1solver1"),
		TargetCoin:    common.NewCoin(common.BTCAsset, cosmos.NewUint(15)),
		MatchHeight:   matchHeight,
	}
	k.SetIntentSolve(ctx, solve1)

	solve2 := types.IntentSolve{
		SolveTxID:     common.TxID("SOLVE21234567890ABCD1234567890ABCD1234567890ABCD1234567890ABCD123"),
		IntentTxID:    common.TxID("INTENT2234567890ABCD1234567890ABCD1234567890ABCD1234567890ABCD123"),
		SolverAddress: common.Address("thor1solver2"),
		TargetCoin:    common.NewCoin(common.BTCAsset, cosmos.NewUint(18)),
		MatchHeight:   matchHeight,
	}
	k.SetIntentSolve(ctx, solve2)

	// Get solves by match height
	solves, err := k.GetIntentSolvesByMatchHeight(ctx, matchHeight)
	c.Assert(err, IsNil)
	c.Check(len(solves), Equals, 2)
}

func (KeeperIntentSuite) TestIntentSolve_GetByMatchHeight_PrefixCollision(c *C) {
	ctx, k := setupKeeperForTest(c)

	// Create solves at height 50 and 500. Without the "/" suffix in the key,
	// querying height 50 would also match height 500 entries since "50" is
	// a string prefix of "500".
	solve50 := types.IntentSolve{
		SolveTxID:     common.TxID("SOLVE50234567890ABCD1234567890ABCD1234567890ABCD1234567890ABCD123"),
		IntentTxID:    common.TxID("INTEN50234567890ABCD1234567890ABCD1234567890ABCD1234567890ABCD123"),
		SolverAddress: common.Address("thor1solver50"),
		TargetCoin:    common.NewCoin(common.BTCAsset, cosmos.NewUint(10)),
		MatchHeight:   50,
	}
	k.SetIntentSolve(ctx, solve50)

	solve500a := types.IntentSolve{
		SolveTxID:     common.TxID("SOLV500A34567890ABCD1234567890ABCD1234567890ABCD1234567890ABCD123"),
		IntentTxID:    common.TxID("INTE500A34567890ABCD1234567890ABCD1234567890ABCD1234567890ABCD123"),
		SolverAddress: common.Address("thor1solver500a"),
		TargetCoin:    common.NewCoin(common.BTCAsset, cosmos.NewUint(20)),
		MatchHeight:   500,
	}
	k.SetIntentSolve(ctx, solve500a)

	solve500b := types.IntentSolve{
		SolveTxID:     common.TxID("SOLV500B34567890ABCD1234567890ABCD1234567890ABCD1234567890ABCD123"),
		IntentTxID:    common.TxID("INTE500B34567890ABCD1234567890ABCD1234567890ABCD1234567890ABCD123"),
		SolverAddress: common.Address("thor1solver500b"),
		TargetCoin:    common.NewCoin(common.BTCAsset, cosmos.NewUint(25)),
		MatchHeight:   500,
	}
	k.SetIntentSolve(ctx, solve500b)

	// Query height 50 - should return only the one solve at height 50
	solves50, err := k.GetIntentSolvesByMatchHeight(ctx, 50)
	c.Assert(err, IsNil)
	c.Check(len(solves50), Equals, 1)
	c.Check(solves50[0].SolveTxID.Equals(solve50.SolveTxID), Equals, true)

	// Query height 500 - should return only the two solves at height 500
	solves500, err := k.GetIntentSolvesByMatchHeight(ctx, 500)
	c.Assert(err, IsNil)
	c.Check(len(solves500), Equals, 2)
}

func (KeeperIntentSuite) TestIntentSolve_Remove(c *C) {
	ctx, k := setupKeeperForTest(c)

	solveTxID := common.TxID("SOLVE1234567890ABCD1234567890ABCD1234567890ABCD1234567890ABCD123")
	intentTxID := common.TxID("INTENT123456789ABCD1234567890ABCD1234567890ABCD1234567890ABCD123")
	matchHeight := int64(500)

	solve := types.IntentSolve{
		SolveTxID:     solveTxID,
		IntentTxID:    intentTxID,
		SolverAddress: common.Address("thor1solver"),
		TargetCoin:    common.NewCoin(common.BTCAsset, cosmos.NewUint(15)),
		MatchHeight:   matchHeight,
	}

	// Set and verify
	k.SetIntentSolve(ctx, solve)
	c.Check(k.IntentSolveExists(ctx, solveTxID), Equals, true)

	// Remove
	k.RemoveIntentSolve(ctx, solveTxID)

	// Verify removed
	c.Check(k.IntentSolveExists(ctx, solveTxID), Equals, false)

	// Verify indices also removed
	solves, err := k.GetIntentSolvesByIntentID(ctx, intentTxID)
	c.Assert(err, IsNil)
	c.Check(len(solves), Equals, 0)

	solves, err = k.GetIntentSolvesByMatchHeight(ctx, matchHeight)
	c.Assert(err, IsNil)
	c.Check(len(solves), Equals, 0)
}

func (KeeperIntentSuite) TestIntentSolve_RemoveCorruptedRecord(c *C) {
	ctx, k := setupKeeperForTest(c)

	solveTxID := common.TxID("SOLVE9999999990ABCD1234567890ABCD1234567890ABCD1234567890ABCD123")
	intentTxID := common.TxID("INTENT99999999ABCD1234567890ABCD1234567890ABCD1234567890ABCD123")
	matchHeight := int64(500)

	solve := types.IntentSolve{
		SolveTxID:     solveTxID,
		IntentTxID:    intentTxID,
		SolverAddress: common.Address("thor1solver"),
		TargetCoin:    common.NewCoin(common.BTCAsset, cosmos.NewUint(15)),
		MatchHeight:   matchHeight,
	}
	k.SetIntentSolve(ctx, solve)

	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	store.Set(k.GetKey(prefixIntentSolve, solveTxID.String()), []byte("corrupted"))

	_, err := k.GetIntentSolve(ctx, solveTxID)
	c.Assert(err, NotNil)

	k.RemoveIntentSolve(ctx, solveTxID)

	c.Check(k.IntentSolveExists(ctx, solveTxID), Equals, false)

	solves, err := k.GetIntentSolvesByIntentID(ctx, intentTxID)
	c.Assert(err, IsNil)
	c.Check(len(solves), Equals, 0)

	solves, err = k.GetIntentSolvesByMatchHeight(ctx, matchHeight)
	c.Assert(err, IsNil)
	c.Check(len(solves), Equals, 0)
}

func (KeeperIntentSuite) TestIntent_TerminalSolveRefundIndex_CleansStaleEntries(c *C) {
	ctx, k := setupKeeperForTest(c)

	intentTxID := common.TxID("TERMSTALE1234567890ABCD1234567890ABCD1234567890ABCD1234567890A")
	intent := types.Intent{
		TxID:          intentTxID,
		SourceAddress: common.Address("thor1sender1"),
		Source:        common.NewCoin(common.DOGEAsset, cosmos.NewUint(100)),
		TargetAsset:   common.BTCAsset,
		Status:        types.IntentStatus_IntentCompleted,
	}
	k.SetIntent(ctx, intent)

	solveTxID := common.TxID("TERMSTALESOLVE1234567890ABCD1234567890ABCD1234567890ABCD123456789")
	solve := types.IntentSolve{
		SolveTxID:     solveTxID,
		IntentTxID:    intentTxID,
		SolverAddress: common.Address("thor1solver1"),
		TargetCoin:    common.NewCoin(common.BTCAsset, cosmos.NewUint(15)),
		MatchHeight:   100,
	}
	k.SetIntentSolve(ctx, solve)

	terminalIntents, err := k.GetTerminalSolveRefundIntents(ctx)
	c.Assert(err, IsNil)
	c.Check(len(terminalIntents), Equals, 1)

	intent.Status = types.IntentStatus_IntentActive
	k.setIntent(ctx, k.GetKey(prefixIntent, intentTxID.String()), intent)

	terminalIntents, err = k.GetTerminalSolveRefundIntents(ctx)
	c.Assert(err, IsNil)
	c.Check(len(terminalIntents), Equals, 0)
}

func (KeeperIntentSuite) TestIntent_RemoveIntentWithCorruptedSolve(c *C) {
	ctx, k := setupKeeperForTest(c)

	intentTxID := common.TxID("INTENT77777777ABCD1234567890ABCD1234567890ABCD1234567890ABCD123")
	matchHeight := int64(700)
	intent := types.Intent{
		TxID:          intentTxID,
		SourceAddress: common.Address("thor1sender1"),
		Source:        common.NewCoin(common.DOGEAsset, cosmos.NewUint(100)),
		TargetAsset:   common.BTCAsset,
		Status:        types.IntentStatus_IntentActive,
	}
	k.SetIntent(ctx, intent)

	solveTxID := common.TxID("SOLVE777777770ABCD1234567890ABCD1234567890ABCD1234567890ABCD123")
	solve := types.IntentSolve{
		SolveTxID:     solveTxID,
		IntentTxID:    intentTxID,
		SolverAddress: common.Address("thor1solver"),
		TargetCoin:    common.NewCoin(common.BTCAsset, cosmos.NewUint(15)),
		MatchHeight:   matchHeight,
	}
	k.SetIntentSolve(ctx, solve)

	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	store.Set(k.GetKey(prefixIntentSolve, solveTxID.String()), []byte("corrupted"))

	k.RemoveIntent(ctx, intentTxID)

	c.Check(k.IntentExists(ctx, intentTxID), Equals, false)
	c.Check(k.IntentSolveExists(ctx, solveTxID), Equals, false)

	solves, err := k.GetIntentSolvesByIntentID(ctx, intentTxID)
	c.Assert(err, IsNil)
	c.Check(len(solves), Equals, 0)

	solves, err = k.GetIntentSolvesByMatchHeight(ctx, matchHeight)
	c.Assert(err, IsNil)
	c.Check(len(solves), Equals, 0)
}

func (KeeperIntentSuite) TestIntentSolve_RemoveMissingPrimaryRecordSweepsIndexes(c *C) {
	ctx, k := setupKeeperForTest(c)

	solveTxID := common.TxID("SOLVE888888880ABCD1234567890ABCD1234567890ABCD1234567890ABCD123")
	intentTxID := common.TxID("INTENT88888888ABCD1234567890ABCD1234567890ABCD1234567890ABCD123")
	matchHeight := int64(800)

	solve := types.IntentSolve{
		SolveTxID:     solveTxID,
		IntentTxID:    intentTxID,
		SolverAddress: common.Address("thor1solver"),
		TargetCoin:    common.NewCoin(common.BTCAsset, cosmos.NewUint(15)),
		MatchHeight:   matchHeight,
	}
	k.SetIntentSolve(ctx, solve)

	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	store.Delete(k.GetKey(prefixIntentSolve, solveTxID.String()))

	k.RemoveIntentSolve(ctx, solveTxID)

	c.Check(k.IntentSolveExists(ctx, solveTxID), Equals, false)

	solves, err := k.GetIntentSolvesByIntentID(ctx, intentTxID)
	c.Assert(err, IsNil)
	c.Check(len(solves), Equals, 0)

	solves, err = k.GetIntentSolvesByMatchHeight(ctx, matchHeight)
	c.Assert(err, IsNil)
	c.Check(len(solves), Equals, 0)
}
