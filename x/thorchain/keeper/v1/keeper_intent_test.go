package keeperv1

import (
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
