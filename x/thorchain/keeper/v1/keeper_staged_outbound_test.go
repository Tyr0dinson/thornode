package keeperv1

import (
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

type KeeperStagedOutboundSuite struct{}

var _ = Suite(&KeeperStagedOutboundSuite{})

func (s *KeeperStagedOutboundSuite) TestSetStagedOutbound(c *C) {
	ctx, k := setupKeeperForTest(c)

	inHash := GetRandomTxHash()
	item := TxOutItem{
		Chain:       common.ETHChain,
		ToAddress:   GetRandomETHAddress(),
		VaultPubKey: GetRandomPubKey(),
		Coin:        common.NewCoin(common.ETHAsset, cosmos.NewUint(100*common.One)),
		Memo:        "OUT:test",
		InHash:      inHash,
	}

	// Set staged outbound
	err := k.SetStagedOutbound(ctx, item, 100)
	c.Assert(err, IsNil)

	// Verify it was saved
	outbounds, err := k.GetStagedOutbounds(ctx, inHash)
	c.Assert(err, IsNil)
	c.Assert(outbounds, HasLen, 1)
	c.Check(outbounds[0].TxOutItem.InHash, Equals, inHash)
	c.Check(outbounds[0].ScheduledHeight, Equals, int64(100))
	c.Check(outbounds[0].StagedHeight, Equals, ctx.BlockHeight())
}

func (s *KeeperStagedOutboundSuite) TestSetStagedOutboundEmptyInHash(c *C) {
	ctx, k := setupKeeperForTest(c)

	item := TxOutItem{
		Chain:       common.ETHChain,
		ToAddress:   GetRandomETHAddress(),
		VaultPubKey: GetRandomPubKey(),
		Coin:        common.NewCoin(common.ETHAsset, cosmos.NewUint(100*common.One)),
		Memo:        "OUT:test",
		InHash:      common.TxID(""), // Empty InHash
	}

	// Set staged outbound with empty InHash should fail
	err := k.SetStagedOutbound(ctx, item, 100)
	c.Assert(err, NotNil)
	c.Check(err.Error(), Equals, "cannot stage outbound with empty InHash")
}

func (s *KeeperStagedOutboundSuite) TestSetMultipleStagedOutbounds(c *C) {
	ctx, k := setupKeeperForTest(c)

	inHash := GetRandomTxHash()

	// Create first outbound
	item1 := TxOutItem{
		Chain:       common.ETHChain,
		ToAddress:   GetRandomETHAddress(),
		VaultPubKey: GetRandomPubKey(),
		Coin:        common.NewCoin(common.ETHAsset, cosmos.NewUint(100*common.One)),
		Memo:        "OUT:test1",
		InHash:      inHash,
	}

	// Create second outbound for same inHash
	item2 := TxOutItem{
		Chain:       common.BTCChain,
		ToAddress:   GetRandomBTCAddress(),
		VaultPubKey: GetRandomPubKey(),
		Coin:        common.NewCoin(common.BTCAsset, cosmos.NewUint(50*common.One)),
		Memo:        "OUT:test2",
		InHash:      inHash,
	}

	// Set both staged outbounds
	err := k.SetStagedOutbound(ctx, item1, 100)
	c.Assert(err, IsNil)
	err = k.SetStagedOutbound(ctx, item2, 110)
	c.Assert(err, IsNil)

	// Verify both were saved under the same inHash
	outbounds, err := k.GetStagedOutbounds(ctx, inHash)
	c.Assert(err, IsNil)
	c.Assert(outbounds, HasLen, 2)
	c.Check(outbounds[0].TxOutItem.Chain, Equals, common.ETHChain)
	c.Check(outbounds[0].ScheduledHeight, Equals, int64(100))
	c.Check(outbounds[1].TxOutItem.Chain, Equals, common.BTCChain)
	c.Check(outbounds[1].ScheduledHeight, Equals, int64(110))
}

func (s *KeeperStagedOutboundSuite) TestGetStagedOutboundsNonExistent(c *C) {
	ctx, k := setupKeeperForTest(c)

	// Get staged outbounds for non-existent inHash
	inHash := GetRandomTxHash()
	outbounds, err := k.GetStagedOutbounds(ctx, inHash)
	c.Assert(err, IsNil)
	c.Assert(outbounds, HasLen, 0)
}

func (s *KeeperStagedOutboundSuite) TestDeleteStagedOutbounds(c *C) {
	ctx, k := setupKeeperForTest(c)

	inHash := GetRandomTxHash()
	item := TxOutItem{
		Chain:       common.ETHChain,
		ToAddress:   GetRandomETHAddress(),
		VaultPubKey: GetRandomPubKey(),
		Coin:        common.NewCoin(common.ETHAsset, cosmos.NewUint(100*common.One)),
		Memo:        "OUT:test",
		InHash:      inHash,
	}

	// Set staged outbound
	err := k.SetStagedOutbound(ctx, item, 100)
	c.Assert(err, IsNil)

	// Verify it exists
	outbounds, err := k.GetStagedOutbounds(ctx, inHash)
	c.Assert(err, IsNil)
	c.Assert(outbounds, HasLen, 1)

	// Delete staged outbounds
	k.DeleteStagedOutbounds(ctx, inHash)

	// Verify it was deleted
	outbounds, err = k.GetStagedOutbounds(ctx, inHash)
	c.Assert(err, IsNil)
	c.Assert(outbounds, HasLen, 0)
}

func (s *KeeperStagedOutboundSuite) TestGetStagedOutboundIterator(c *C) {
	ctx, k := setupKeeperForTest(c)

	// Create multiple staged outbounds with different inHashes
	inHash1 := GetRandomTxHash()
	inHash2 := GetRandomTxHash()

	item1 := TxOutItem{
		Chain:       common.ETHChain,
		ToAddress:   GetRandomETHAddress(),
		VaultPubKey: GetRandomPubKey(),
		Coin:        common.NewCoin(common.ETHAsset, cosmos.NewUint(100*common.One)),
		Memo:        "OUT:test1",
		InHash:      inHash1,
	}
	item2 := TxOutItem{
		Chain:       common.BTCChain,
		ToAddress:   GetRandomBTCAddress(),
		VaultPubKey: GetRandomPubKey(),
		Coin:        common.NewCoin(common.BTCAsset, cosmos.NewUint(50*common.One)),
		Memo:        "OUT:test2",
		InHash:      inHash2,
	}

	err := k.SetStagedOutbound(ctx, item1, 100)
	c.Assert(err, IsNil)
	err = k.SetStagedOutbound(ctx, item2, 110)
	c.Assert(err, IsNil)

	// Test iterator
	iter := k.GetStagedOutboundIterator(ctx)
	c.Check(iter, NotNil)
	defer iter.Close()

	count := 0
	for ; iter.Valid(); iter.Next() {
		count++
	}
	c.Check(count, Equals, 2)
}

func (s *KeeperStagedOutboundSuite) TestGetStagedOutboundValue(c *C) {
	ctx, k := setupKeeperForTest(c)

	// Create pool for ETH so GetTOIsValue can calculate the value
	pool := NewPool()
	pool.Asset = common.ETHAsset
	pool.BalanceRune = cosmos.NewUint(100000 * common.One)
	pool.BalanceAsset = cosmos.NewUint(1000 * common.One)
	pool.Status = types.PoolStatus_Available
	err := k.SetPool(ctx, pool)
	c.Assert(err, IsNil)

	// Set a staged outbound
	inHash := GetRandomTxHash()
	spent := cosmos.NewUint(50)
	item := TxOutItem{
		Chain:       common.ETHChain,
		ToAddress:   GetRandomETHAddress(),
		VaultPubKey: GetRandomPubKey(),
		Coin:        common.NewCoin(common.ETHAsset, cosmos.NewUint(10*common.One)),
		Memo:        "OUT:test",
		InHash:      inHash,
		CloutSpent:  &spent,
	}

	err = k.SetStagedOutbound(ctx, item, 100)
	c.Assert(err, IsNil)

	// Get staged outbound value
	runeValue, cloutValue, err := k.GetStagedOutboundValue(ctx)
	c.Assert(err, IsNil)
	c.Check(runeValue.GT(cosmos.ZeroUint()), Equals, true, Commentf("expected rune value > 0, got %s", runeValue))
	c.Check(cloutValue.String(), Equals, "50")
}

func (s *KeeperStagedOutboundSuite) TestRestoreStagedOutboundList(c *C) {
	ctx, k := setupKeeperForTest(c)

	inHash := GetRandomTxHash()
	originalStagedHeight := int64(42)

	list := StagedOutboundList{
		InHash: inHash,
		Items: []StagedOutbound{
			{
				TxOutItem: TxOutItem{
					Chain:       common.ETHChain,
					ToAddress:   GetRandomETHAddress(),
					VaultPubKey: GetRandomPubKey(),
					Coin:        common.NewCoin(common.ETHAsset, cosmos.NewUint(100*common.One)),
					Memo:        "OUT:test",
					InHash:      inHash,
				},
				ScheduledHeight: 100,
				StagedHeight:    originalStagedHeight,
			},
		},
	}

	// Restore the list directly
	err := k.RestoreStagedOutboundList(ctx, list)
	c.Assert(err, IsNil)

	// Verify the staged height is preserved (not overwritten with ctx.BlockHeight())
	outbounds, err := k.GetStagedOutbounds(ctx, inHash)
	c.Assert(err, IsNil)
	c.Assert(outbounds, HasLen, 1)
	c.Check(outbounds[0].StagedHeight, Equals, originalStagedHeight)
	c.Check(outbounds[0].ScheduledHeight, Equals, int64(100))
}

func (s *KeeperStagedOutboundSuite) TestRestoreStagedOutboundListEmptyInHash(c *C) {
	ctx, k := setupKeeperForTest(c)

	list := StagedOutboundList{
		InHash: common.TxID(""),
		Items:  []StagedOutbound{},
	}

	err := k.RestoreStagedOutboundList(ctx, list)
	c.Assert(err, NotNil)
	c.Check(err.Error(), Equals, "cannot restore staged outbound list with empty InHash")
}

func (s *KeeperStagedOutboundSuite) TestGetStagedOutboundValueEmpty(c *C) {
	ctx, k := setupKeeperForTest(c)

	// Get staged outbound value when none exist
	runeValue, cloutValue, err := k.GetStagedOutboundValue(ctx)
	c.Assert(err, IsNil)
	c.Check(runeValue.IsZero(), Equals, true)
	c.Check(cloutValue.IsZero(), Equals, true)
}

func (s *KeeperStagedOutboundSuite) TestGetStagedOutboundValueMultiple(c *C) {
	ctx, k := setupKeeperForTest(c)

	// Create pools
	ethPool := NewPool()
	ethPool.Asset = common.ETHAsset
	ethPool.BalanceRune = cosmos.NewUint(100000 * common.One)
	ethPool.BalanceAsset = cosmos.NewUint(1000 * common.One)
	ethPool.Status = types.PoolStatus_Available
	err := k.SetPool(ctx, ethPool)
	c.Assert(err, IsNil)

	btcPool := NewPool()
	btcPool.Asset = common.BTCAsset
	btcPool.BalanceRune = cosmos.NewUint(200000 * common.One)
	btcPool.BalanceAsset = cosmos.NewUint(10 * common.One)
	btcPool.Status = types.PoolStatus_Available
	err = k.SetPool(ctx, btcPool)
	c.Assert(err, IsNil)

	// Set multiple staged outbounds with different inHashes
	inHash1 := GetRandomTxHash()
	spent1 := cosmos.NewUint(30)
	item1 := TxOutItem{
		Chain:       common.ETHChain,
		ToAddress:   GetRandomETHAddress(),
		VaultPubKey: GetRandomPubKey(),
		Coin:        common.NewCoin(common.ETHAsset, cosmos.NewUint(10*common.One)),
		Memo:        "OUT:test1",
		InHash:      inHash1,
		CloutSpent:  &spent1,
	}

	inHash2 := GetRandomTxHash()
	spent2 := cosmos.NewUint(70)
	item2 := TxOutItem{
		Chain:       common.BTCChain,
		ToAddress:   GetRandomBTCAddress(),
		VaultPubKey: GetRandomPubKey(),
		Coin:        common.NewCoin(common.BTCAsset, cosmos.NewUint(1*common.One)),
		Memo:        "OUT:test2",
		InHash:      inHash2,
		CloutSpent:  &spent2,
	}

	err = k.SetStagedOutbound(ctx, item1, 100)
	c.Assert(err, IsNil)
	err = k.SetStagedOutbound(ctx, item2, 110)
	c.Assert(err, IsNil)

	// Get staged outbound value
	runeValue, cloutValue, err := k.GetStagedOutboundValue(ctx)
	c.Assert(err, IsNil)
	c.Check(runeValue.GT(cosmos.ZeroUint()), Equals, true, Commentf("expected rune value > 0, got %s", runeValue))
	c.Check(cloutValue.String(), Equals, "100") // 30 + 70
}
