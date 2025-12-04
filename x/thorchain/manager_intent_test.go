package thorchain

import (
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"
)

type IntentMgrTestSuite struct{}

var _ = Suite(&IntentMgrTestSuite{})

type TestIntentMgrKeeper struct {
	keeper.Keeper
	pools   map[string]Pool
	intents map[string]Intent
}

func NewTestIntentMgrKeeper(k keeper.Keeper) *TestIntentMgrKeeper {
	return &TestIntentMgrKeeper{
		Keeper:  k,
		pools:   make(map[string]Pool),
		intents: make(map[string]Intent),
	}
}

func (k *TestIntentMgrKeeper) GetPool(ctx cosmos.Context, asset common.Asset) (Pool, error) {
	if pool, ok := k.pools[asset.String()]; ok {
		return pool, nil
	}
	return k.Keeper.GetPool(ctx, asset)
}

func (k *TestIntentMgrKeeper) SetPool(ctx cosmos.Context, pool Pool) error {
	k.pools[pool.Asset.String()] = pool
	return k.Keeper.SetPool(ctx, pool)
}

func (k *TestIntentMgrKeeper) GetIntent(ctx cosmos.Context, txID common.TxID) (Intent, error) {
	if intent, ok := k.intents[txID.String()]; ok {
		return intent, nil
	}
	return k.Keeper.GetIntent(ctx, txID)
}

func (k *TestIntentMgrKeeper) SetIntent(ctx cosmos.Context, intent Intent) {
	k.intents[intent.TxID.String()] = intent
	k.Keeper.SetIntent(ctx, intent)
}

// TestExpireIntent_RefundWithL1Pool tests that an intent with an L1 pool asset is refunded directly
func (s *IntentMgrTestSuite) TestExpireIntent_RefundWithL1Pool(c *C) {
	w := getHandlerTestWrapper(c, 1, true, true)

	// Create a pool for BTC (L1 pool asset)
	pool := NewPool()
	pool.Asset = common.BTCAsset
	pool.Status = PoolAvailable
	pool.BalanceAsset = cosmos.NewUint(100 * common.One)
	pool.BalanceRune = cosmos.NewUint(100 * common.One)
	c.Assert(w.keeper.SetPool(w.ctx, pool), IsNil)

	// Create an expired intent with L1 pool asset
	intentTx := GetRandomTx()
	userAddr := GetRandomBTCAddress()
	intent := Intent{
		TxID:          intentTx.ID,
		SourceAddress: userAddr,
		Source:        common.NewCoin(common.BTCAsset, cosmos.NewUint(1*common.One)),
		TargetAsset:   common.ETHAsset,
		TargetAddress: GetRandomETHAddress(),
		HighTarget:    cosmos.NewUint(20 * common.One),
		LowTarget:     cosmos.NewUint(10 * common.One),
		ExpiryHeight:  w.ctx.BlockHeight() - 1, // Already expired
		Status:        IntentActive,
	}
	w.keeper.SetIntent(w.ctx, intent)

	mgr := NewDummyMgrWithKeeper(w.keeper)
	intentMgr := NewIntentManager(mgr).(*IntentMgr)

	// Call expireIntent
	err := intentMgr.expireIntent(w.ctx, intent)
	c.Assert(err, IsNil)

	// Verify intent status is expired (not auctioning)
	updatedIntent, err := w.keeper.GetIntent(w.ctx, intentTx.ID)
	c.Assert(err, IsNil)
	c.Check(updatedIntent.Status, Equals, IntentExpired)
}

// TestExpireIntent_RefundNativeAsset tests that an intent with a native asset (RUNE) is refunded directly
func (s *IntentMgrTestSuite) TestExpireIntent_RefundNativeAsset(c *C) {
	w := getHandlerTestWrapper(c, 1, true, true)

	// Create an expired intent with RUNE (native asset)
	intentTx := GetRandomTx()
	userAddr := GetRandomTHORAddress()
	intent := Intent{
		TxID:          intentTx.ID,
		SourceAddress: userAddr,
		Source:        common.NewCoin(common.RuneAsset(), cosmos.NewUint(100*common.One)),
		TargetAsset:   common.BTCAsset,
		TargetAddress: GetRandomBTCAddress(),
		HighTarget:    cosmos.NewUint(2 * common.One),
		LowTarget:     cosmos.NewUint(1 * common.One),
		ExpiryHeight:  w.ctx.BlockHeight() - 1, // Already expired
		Status:        IntentActive,
	}
	w.keeper.SetIntent(w.ctx, intent)

	mgr := NewDummyMgrWithKeeper(w.keeper)
	intentMgr := NewIntentManager(mgr).(*IntentMgr)

	// Call expireIntent
	err := intentMgr.expireIntent(w.ctx, intent)
	c.Assert(err, IsNil)

	// Verify intent status is expired (not auctioning)
	updatedIntent, err := w.keeper.GetIntent(w.ctx, intentTx.ID)
	c.Assert(err, IsNil)
	c.Check(updatedIntent.Status, Equals, IntentExpired)
}

// TestExpireIntent_AuctionNoPoolAsset tests that an intent with no L1 pool goes to auction
func (s *IntentMgrTestSuite) TestExpireIntent_AuctionNoPoolAsset(c *C) {
	w := getHandlerTestWrapper(c, 1, true, true)

	// Create an expired intent with an asset that has NO pool
	// Use a made-up asset that doesn't exist
	noPoolAsset, err := common.NewAsset("UNKNOWN.NOPOOL")
	c.Assert(err, IsNil)

	intentTx := GetRandomTx()
	userAddr := GetRandomBTCAddress()
	intent := Intent{
		TxID:          intentTx.ID,
		SourceAddress: userAddr,
		Source:        common.NewCoin(noPoolAsset, cosmos.NewUint(100*common.One)),
		TargetAsset:   common.BTCAsset,
		TargetAddress: GetRandomBTCAddress(),
		HighTarget:    cosmos.NewUint(2 * common.One),
		LowTarget:     cosmos.NewUint(1 * common.One),
		ExpiryHeight:  w.ctx.BlockHeight() - 1, // Already expired
		Status:        IntentActive,
	}
	w.keeper.SetIntent(w.ctx, intent)

	mgr := NewDummyMgrWithKeeper(w.keeper)
	intentMgr := NewIntentManager(mgr).(*IntentMgr)

	// Call expireIntent
	err = intentMgr.expireIntent(w.ctx, intent)
	c.Assert(err, IsNil)

	// Verify intent status is auctioning (not expired directly)
	updatedIntent, err := w.keeper.GetIntent(w.ctx, intentTx.ID)
	c.Assert(err, IsNil)
	c.Check(updatedIntent.Status, Equals, IntentAuctioning)

	// Verify auction was created
	auction, err := w.keeper.GetIntentAuction(w.ctx, intentTx.ID)
	c.Assert(err, IsNil)
	c.Check(auction.Status, Equals, IntentAuctionStatus_AuctionActive)
}

// TestExpireIntent_RefundTradeAsset tests that a trade asset is refunded directly
func (s *IntentMgrTestSuite) TestExpireIntent_RefundTradeAsset(c *C) {
	w := getHandlerTestWrapper(c, 1, true, true)

	// Create an expired intent with a trade asset
	tradeAsset := common.BTCAsset.GetTradeAsset()

	intentTx := GetRandomTx()
	userAddr := GetRandomTHORAddress()
	intent := Intent{
		TxID:          intentTx.ID,
		SourceAddress: userAddr,
		Source:        common.NewCoin(tradeAsset, cosmos.NewUint(100*common.One)),
		TargetAsset:   common.ETHAsset,
		TargetAddress: GetRandomETHAddress(),
		HighTarget:    cosmos.NewUint(20 * common.One),
		LowTarget:     cosmos.NewUint(10 * common.One),
		ExpiryHeight:  w.ctx.BlockHeight() - 1, // Already expired
		Status:        IntentActive,
	}
	w.keeper.SetIntent(w.ctx, intent)

	mgr := NewDummyMgrWithKeeper(w.keeper)
	intentMgr := NewIntentManager(mgr).(*IntentMgr)

	// Call expireIntent
	err := intentMgr.expireIntent(w.ctx, intent)
	c.Assert(err, IsNil)

	// Verify intent status is expired (not auctioning)
	updatedIntent, err := w.keeper.GetIntent(w.ctx, intentTx.ID)
	c.Assert(err, IsNil)
	c.Check(updatedIntent.Status, Equals, IntentExpired)
}
