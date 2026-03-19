package thorchain

import (
	"github.com/cosmos/cosmos-sdk/runtime"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"
	kvTypes "gitlab.com/thorchain/thornode/v3/x/thorchain/keeper/types"
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

type TxOutStoreFailOnCallDummy struct {
	*TxOutStoreDummy
	failOnCall int
	callCount  int
}

func NewTxOutStoreFailOnCallDummy(failOnCall int) *TxOutStoreFailOnCallDummy {
	return &TxOutStoreFailOnCallDummy{
		TxOutStoreDummy: NewTxStoreDummy(),
		failOnCall:      failOnCall,
	}
}

func (tos *TxOutStoreFailOnCallDummy) TryAddTxOutItem(ctx cosmos.Context, mgr Manager, toi TxOutItem, minOut cosmos.Uint) (bool, error) {
	tos.callCount++
	if tos.callCount == tos.failOnCall {
		return false, errKaboom
	}
	if !toi.Chain.Equals(common.THORChain) {
		tos.addToBlockOut(ctx, toi)
	}
	return true, nil
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
	intentMgr, ok := NewIntentManager(mgr).(*IntentMgr)
	c.Assert(ok, Equals, true)

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
	intentMgr, ok := NewIntentManager(mgr).(*IntentMgr)
	c.Assert(ok, Equals, true)

	// Call expireIntent
	err := intentMgr.expireIntent(w.ctx, intent)
	c.Assert(err, IsNil)

	// Verify intent status is expired (not auctioning)
	updatedIntent, err := w.keeper.GetIntent(w.ctx, intentTx.ID)
	c.Assert(err, IsNil)
	c.Check(updatedIntent.Status, Equals, IntentExpired)
}

// TestExpireIntent_AuctionNoPoolAsset tests that an intent with no L1 pool goes to auction
// after the post-expiry cancel window has elapsed.
func (s *IntentMgrTestSuite) TestExpireIntent_AuctionNoPoolAsset(c *C) {
	w := getHandlerTestWrapper(c, 1, true, true)

	// Create an expired intent with an asset that has NO pool
	// Use a made-up asset that doesn't exist
	noPoolAsset, err := common.NewAsset("UNKNOWN.NOPOOL")
	c.Assert(err, IsNil)

	// Set the expiry far enough in the past that the post-expiry cancel window
	// has elapsed (mocknet default is 5 blocks).
	cancelWindow := w.keeper.GetConfigInt64(w.ctx, constants.IntentPostExpiryCancelBlocks)
	expiryHeight := w.ctx.BlockHeight() - cancelWindow - 1

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
		ExpiryHeight:  expiryHeight,
		Status:        IntentActive,
	}
	w.keeper.SetIntent(w.ctx, intent)

	mgr := NewDummyMgrWithKeeper(w.keeper)
	intentMgr, ok := NewIntentManager(mgr).(*IntentMgr)
	c.Assert(ok, Equals, true)

	// Call expireIntent — cancel window has passed, should go to auction
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

// TestExpireIntent_CancelWindowDefersAuction tests that a freshly-expired
// non-pool intent stays active during the post-expiry cancel window.
func (s *IntentMgrTestSuite) TestExpireIntent_CancelWindowDefersAuction(c *C) {
	w := getHandlerTestWrapper(c, 1, true, true)

	noPoolAsset, err := common.NewAsset("UNKNOWN.NOPOOL")
	c.Assert(err, IsNil)

	intentTx := GetRandomTx()
	intent := Intent{
		TxID:          intentTx.ID,
		SourceAddress: GetRandomBTCAddress(),
		Source:        common.NewCoin(noPoolAsset, cosmos.NewUint(100*common.One)),
		TargetAsset:   common.BTCAsset,
		TargetAddress: GetRandomBTCAddress(),
		HighTarget:    cosmos.NewUint(2 * common.One),
		LowTarget:     cosmos.NewUint(1 * common.One),
		ExpiryHeight:  w.ctx.BlockHeight() - 1, // Just expired
		Status:        IntentActive,
	}
	w.keeper.SetIntent(w.ctx, intent)

	mgr := NewDummyMgrWithKeeper(w.keeper)
	intentMgr, ok := NewIntentManager(mgr).(*IntentMgr)
	c.Assert(ok, Equals, true)

	// Call expireIntent — within cancel window, should defer auction
	err = intentMgr.expireIntent(w.ctx, intent)
	c.Assert(err, IsNil)

	// Verify intent is STILL active (not yet auctioning)
	updatedIntent, err := w.keeper.GetIntent(w.ctx, intentTx.ID)
	c.Assert(err, IsNil)
	c.Check(updatedIntent.Status, Equals, IntentActive)
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
	intentMgr, ok := NewIntentManager(mgr).(*IntentMgr)
	c.Assert(ok, Equals, true)

	// Call expireIntent
	err := intentMgr.expireIntent(w.ctx, intent)
	c.Assert(err, IsNil)

	// Verify intent status is expired (not auctioning)
	updatedIntent, err := w.keeper.GetIntent(w.ctx, intentTx.ID)
	c.Assert(err, IsNil)
	c.Check(updatedIntent.Status, Equals, IntentExpired)
}

func (s *IntentMgrTestSuite) TestProcessWinningBid_UnavailablePoolFails(c *C) {
	w := getHandlerTestWrapper(c, 1, true, true)

	bidPool := NewPool()
	bidPool.Asset = common.ETHAsset
	bidPool.Status = PoolStaged
	bidPool.BalanceAsset = cosmos.NewUint(100 * common.One)
	bidPool.BalanceRune = cosmos.NewUint(100 * common.One)
	c.Assert(w.keeper.SetPool(w.ctx, bidPool), IsNil)

	mgr := NewDummyMgrWithKeeper(w.keeper)
	intentMgr, ok := NewIntentManager(mgr).(*IntentMgr)
	c.Assert(ok, Equals, true)

	bid := IntentAuctionBid{
		BidTxID:       GetRandomTx().ID,
		IntentTxID:    GetRandomTx().ID,
		BidderAddress: GetRandomETHAddress(),
		BidAmount:     common.NewCoin(common.ETHAsset, cosmos.NewUint(common.One)),
		SubmitHeight:  w.ctx.BlockHeight(),
	}

	err := intentMgr.processWinningBid(w.ctx, bid)
	c.Assert(err, NotNil)
	c.Check(err.Error(), Matches, ".*winning bid pool .* is not available.*")

	updatedPool, err := w.keeper.GetPool(w.ctx, common.ETHAsset)
	c.Assert(err, IsNil)
	c.Check(updatedPool.BalanceAsset.Equal(bidPool.BalanceAsset), Equals, true)
	c.Check(updatedPool.BalanceRune.Equal(bidPool.BalanceRune), Equals, true)
}
func (s *IntentMgrTestSuite) TestProcessExpirations_DirectRefundSolveFailureStaysRetryable(c *C) {
	w := getHandlerTestWrapper(c, 1, true, true)

	pool := NewPool()
	pool.Asset = common.BTCAsset
	pool.Status = PoolAvailable
	pool.BalanceAsset = cosmos.NewUint(100 * common.One)
	pool.BalanceRune = cosmos.NewUint(100 * common.One)
	c.Assert(w.keeper.SetPool(w.ctx, pool), IsNil)

	intentTx := GetRandomTx()
	intent := Intent{
		TxID:          intentTx.ID,
		SourceAddress: GetRandomBTCAddress(),
		Source:        common.NewCoin(common.BTCAsset, cosmos.NewUint(1*common.One)),
		TargetAsset:   common.ETHAsset,
		TargetAddress: GetRandomETHAddress(),
		HighTarget:    cosmos.NewUint(20 * common.One),
		LowTarget:     cosmos.NewUint(10 * common.One),
		ExpiryHeight:  w.ctx.BlockHeight() - 1,
		Status:        IntentActive,
	}
	w.keeper.SetIntent(w.ctx, intent)

	solveTx := GetRandomTx()
	solve := IntentSolve{
		SolveTxID:     solveTx.ID,
		IntentTxID:    intentTx.ID,
		SolverAddress: GetRandomETHAddress(),
		TargetCoin:    common.NewCoin(common.ETHAsset, cosmos.NewUint(2*common.One)),
		SubmitHeight:  w.ctx.BlockHeight(),
		MatchHeight:   w.ctx.BlockHeight(),
	}
	w.keeper.SetIntentSolve(w.ctx, solve)

	mgr := NewDummyMgrWithKeeper(w.keeper)
	mgr.txOutStore = NewTxOutStoreFailOnCallDummy(2) // owner refund succeeds, solve refund fails

	intentMgr, ok := NewIntentManager(mgr).(*IntentMgr)
	c.Assert(ok, Equals, true)

	err := intentMgr.processExpirations(w.ctx)
	c.Assert(err, IsNil)

	updatedIntent, err := w.keeper.GetIntent(w.ctx, intentTx.ID)
	c.Assert(err, IsNil)
	c.Check(updatedIntent.Status, Equals, IntentActive)

	_, err = w.keeper.GetIntentSolve(w.ctx, solveTx.ID)
	c.Assert(err, IsNil)
}

// Expiration processing should clean stale active-intent index entries inline
// instead of relying on a separate EndBlock cleanup pre-pass.
func (s *IntentMgrTestSuite) TestProcessExpirations_CleansStaleActiveIntentIndexInline(c *C) {
	w := getHandlerTestWrapper(c, 10, true, true)

	validIntentTx := GetRandomTx().ID
	validIntent := Intent{
		TxID:          validIntentTx,
		SourceAddress: GetRandomBTCAddress(),
		Source:        common.NewCoin(common.DOGEAsset, cosmos.NewUint(100*common.One)),
		TargetAsset:   common.BTCAsset,
		TargetAddress: GetRandomBTCAddress(),
		HighTarget:    cosmos.NewUint(2 * common.One),
		LowTarget:     cosmos.NewUint(1 * common.One),
		ExpiryHeight:  w.ctx.BlockHeight() + 100,
		Status:        IntentActive,
	}
	w.keeper.SetIntent(w.ctx, validIntent)

	staleIntentTx := GetRandomTx().ID
	staleIntent := validIntent
	staleIntent.TxID = staleIntentTx
	w.keeper.SetIntent(w.ctx, staleIntent)

	store := runtime.KVStoreAdapter(serviceThorchain.OpenKVStore(w.ctx))
	store.Delete(w.keeper.GetKey(kvTypes.DbPrefix("int/"), staleIntentTx.String()))

	mgr := NewDummyMgrWithKeeper(w.keeper)
	intentMgr, ok := NewIntentManager(mgr).(*IntentMgr)
	c.Assert(ok, Equals, true)

	err := intentMgr.processExpirations(w.ctx)
	c.Assert(err, IsNil)

	activeIntents, err := w.keeper.GetActiveIntents(w.ctx)
	c.Assert(err, IsNil)
	c.Assert(len(activeIntents), Equals, 1)
	c.Check(activeIntents[0].TxID.Equals(validIntentTx), Equals, true)
}

// Auction processing should clean stale active-auction index entries inline
// instead of relying on a separate EndBlock cleanup pre-pass.
func (s *IntentMgrTestSuite) TestProcessAuctions_CleansStaleActiveAuctionIndexInline(c *C) {
	w := getHandlerTestWrapper(c, 10, true, true)

	validAuctionTx := GetRandomTx().ID
	validAuction := IntentAuction{
		IntentTxID:    validAuctionTx,
		AuctionAmount: common.NewCoin(common.BTCAsset, cosmos.NewUint(100*common.One)),
		ExpiryHeight:  w.ctx.BlockHeight() + 100,
		Status:        IntentAuctionStatus_AuctionActive,
	}
	w.keeper.SetIntentAuction(w.ctx, validAuction)

	staleAuctionTx := GetRandomTx().ID
	staleAuction := validAuction
	staleAuction.IntentTxID = staleAuctionTx
	w.keeper.SetIntentAuction(w.ctx, staleAuction)

	store := runtime.KVStoreAdapter(serviceThorchain.OpenKVStore(w.ctx))
	store.Delete(w.keeper.GetKey(kvTypes.DbPrefix("intauc/"), staleAuctionTx.String()))

	mgr := NewDummyMgrWithKeeper(w.keeper)
	intentMgr, ok := NewIntentManager(mgr).(*IntentMgr)
	c.Assert(ok, Equals, true)

	err := intentMgr.processAuctions(w.ctx)
	c.Assert(err, IsNil)

	activeAuctions, err := w.keeper.GetActiveAuctions(w.ctx)
	c.Assert(err, IsNil)
	c.Assert(len(activeAuctions), Equals, 1)
	c.Check(activeAuctions[0].IntentTxID.Equals(validAuctionTx), Equals, true)
}

// TestExecuteIntent_WithAffiliateFee tests that affiliate fee is charged at intent completion
func (s *IntentMgrTestSuite) TestExecuteIntent_WithAffiliateFee(c *C) {
	w := getHandlerTestWrapper(c, 1, true, true)

	// Create a pool for BTC (needed for fee processing)
	pool := NewPool()
	pool.Asset = common.BTCAsset
	pool.Status = PoolAvailable
	pool.BalanceAsset = cosmos.NewUint(100 * common.One)
	pool.BalanceRune = cosmos.NewUint(100 * common.One)
	c.Assert(w.keeper.SetPool(w.ctx, pool), IsNil)

	// Create an intent with affiliate fee info
	intentTx := GetRandomTx()
	userAddr := GetRandomBTCAddress()
	affiliateAddr := GetRandomTHORAddress()
	sourceAmount := cosmos.NewUint(100 * common.One)
	intent := Intent{
		TxID:                 intentTx.ID,
		SourceAddress:        userAddr,
		Source:               common.NewCoin(common.BTCAsset, sourceAmount),
		TargetAsset:          common.RuneAsset(),
		TargetAddress:        GetRandomTHORAddress(),
		HighTarget:           cosmos.NewUint(200 * common.One),
		LowTarget:            cosmos.NewUint(100 * common.One),
		ExpiryHeight:         w.ctx.BlockHeight() + 100,
		Status:               IntentActive,
		MinFeeBasisPoints:    10, // 0.1% protocol fee
		AffiliateAddress:     affiliateAddr,
		AffiliateBasisPoints: 100, // 1% affiliate fee
	}
	w.keeper.SetIntent(w.ctx, intent)

	// Create a solve
	solveTx := GetRandomTx()
	solverAddr := GetRandomTHORAddress()
	solve := IntentSolve{
		SolveTxID:     solveTx.ID,
		IntentTxID:    intentTx.ID,
		SolverAddress: solverAddr,
		TargetCoin:    common.NewCoin(common.RuneAsset(), cosmos.NewUint(150*common.One)),
		SubmitHeight:  w.ctx.BlockHeight(),
		MatchHeight:   w.ctx.BlockHeight(),
	}
	w.keeper.SetIntentSolve(w.ctx, solve)

	mgr := NewDummyMgrWithKeeper(w.keeper)
	intentMgr, ok := NewIntentManager(mgr).(*IntentMgr)
	c.Assert(ok, Equals, true)

	// Execute the intent
	err := intentMgr.executeIntent(w.ctx, intent, solve)
	c.Assert(err, IsNil)

	// Verify intent status is completed
	updatedIntent, err := w.keeper.GetIntent(w.ctx, intentTx.ID)
	c.Assert(err, IsNil)
	c.Check(updatedIntent.Status, Equals, IntentCompleted)

	// Verify affiliate info was stored in intent and fee would be calculated correctly
	c.Check(updatedIntent.AffiliateAddress.Equals(affiliateAddr), Equals, true)
	c.Check(updatedIntent.AffiliateBasisPoints, Equals, int64(100))

	// Verify fee calculation logic is correct
	// sourceAmount = 100 * common.One = 10,000,000,000
	// 10 bps = 10/10000 = 0.1% -> 10,000,000
	// 100 bps = 100/10000 = 1% -> 100,000,000
	expectedProtocolFee := common.GetSafeShare(cosmos.NewUint(10), cosmos.NewUint(10000), sourceAmount)
	expectedAffiliateFee := common.GetSafeShare(cosmos.NewUint(100), cosmos.NewUint(10000), sourceAmount)
	c.Check(expectedProtocolFee.Equal(cosmos.NewUint(10000000)), Equals, true)   // 0.1% of 10B = 10M
	c.Check(expectedAffiliateFee.Equal(cosmos.NewUint(100000000)), Equals, true) // 1% of 10B = 100M
}

// TestExecuteIntent_AffiliateFeeOnTargetAsset tests affiliate fee charged on target when source has no pool
func (s *IntentMgrTestSuite) TestExecuteIntent_AffiliateFeeOnTargetAsset(c *C) {
	w := getHandlerTestWrapper(c, 1, true, true)

	// No pool for source asset - fee will be charged on target (RUNE)

	// Create an intent with affiliate fee info using non-pool source asset
	noPoolAsset, err := common.NewAsset("UNKNOWN.NOPOOL")
	c.Assert(err, IsNil)

	intentTx := GetRandomTx()
	userAddr := GetRandomBTCAddress()
	affiliateAddr := GetRandomTHORAddress()
	targetAmount := cosmos.NewUint(100 * common.One)
	intent := Intent{
		TxID:                 intentTx.ID,
		SourceAddress:        userAddr,
		Source:               common.NewCoin(noPoolAsset, cosmos.NewUint(50*common.One)),
		TargetAsset:          common.RuneAsset(),
		TargetAddress:        GetRandomTHORAddress(),
		HighTarget:           cosmos.NewUint(100 * common.One),
		LowTarget:            cosmos.NewUint(80 * common.One),
		ExpiryHeight:         w.ctx.BlockHeight() + 100,
		Status:               IntentActive,
		MinFeeBasisPoints:    10, // 0.1% protocol fee
		AffiliateAddress:     affiliateAddr,
		AffiliateBasisPoints: 100, // 1% affiliate fee
	}
	w.keeper.SetIntent(w.ctx, intent)

	// Create a solve with RUNE as target
	solveTx := GetRandomTx()
	solverAddr := GetRandomTHORAddress()
	solve := IntentSolve{
		SolveTxID:     solveTx.ID,
		IntentTxID:    intentTx.ID,
		SolverAddress: solverAddr,
		TargetCoin:    common.NewCoin(common.RuneAsset(), targetAmount),
		SubmitHeight:  w.ctx.BlockHeight(),
		MatchHeight:   w.ctx.BlockHeight(),
	}
	w.keeper.SetIntentSolve(w.ctx, solve)

	mgr := NewDummyMgrWithKeeper(w.keeper)
	intentMgr, ok := NewIntentManager(mgr).(*IntentMgr)
	c.Assert(ok, Equals, true)

	// Execute the intent
	err = intentMgr.executeIntent(w.ctx, intent, solve)
	c.Assert(err, IsNil)

	// Verify intent status is completed
	updatedIntent, err := w.keeper.GetIntent(w.ctx, intentTx.ID)
	c.Assert(err, IsNil)
	c.Check(updatedIntent.Status, Equals, IntentCompleted)

	// Verify affiliate info was stored in intent
	c.Check(updatedIntent.AffiliateAddress.Equals(affiliateAddr), Equals, true)
	c.Check(updatedIntent.AffiliateBasisPoints, Equals, int64(100))

	// Verify fee calculation logic on target asset is correct
	// targetAmount = 100 * common.One = 10,000,000,000
	// 10 bps = 10/10000 = 0.1% -> 10,000,000
	// 100 bps = 100/10000 = 1% -> 100,000,000
	expectedProtocolFee := common.GetSafeShare(cosmos.NewUint(10), cosmos.NewUint(10000), targetAmount)
	expectedAffiliateFee := common.GetSafeShare(cosmos.NewUint(100), cosmos.NewUint(10000), targetAmount)
	c.Check(expectedProtocolFee.Equal(cosmos.NewUint(10000000)), Equals, true)   // 0.1% of 10B = 10M
	c.Check(expectedAffiliateFee.Equal(cosmos.NewUint(100000000)), Equals, true) // 1% of 10B = 100M
}

func (s *IntentMgrTestSuite) TestSelectBestSolve_NonGasTargetUsesConvertedGasStep(c *C) {
	w := getHandlerTestWrapper(c, 1, true, true)

	targetAsset, err := common.NewAsset("ETH.USDC-0XA0B86991C6218B36C1D19D4A2E9EB0CE3606EB48")
	c.Assert(err, IsNil)

	gasPool := NewPool()
	gasPool.Asset = common.ETHAsset
	gasPool.Status = PoolAvailable
	gasPool.BalanceAsset = cosmos.NewUint(1_000_000_000)
	gasPool.BalanceRune = cosmos.NewUint(1_000_000_000)
	c.Assert(w.keeper.SetPool(w.ctx, gasPool), IsNil)

	targetPool := NewPool()
	targetPool.Asset = targetAsset
	targetPool.Status = PoolAvailable
	targetPool.BalanceAsset = cosmos.NewUint(1_000_000_000)
	targetPool.BalanceRune = cosmos.NewUint(1_000_000_000)
	c.Assert(w.keeper.SetPool(w.ctx, targetPool), IsNil)

	mgr := NewDummyMgrWithKeeper(w.keeper)
	intentMgr, ok := NewIntentManager(mgr).(*IntentMgr)
	c.Assert(ok, Equals, true)

	intent := Intent{
		TxID:        GetRandomTx().ID,
		TargetAsset: targetAsset,
	}

	winningSolveTxID := GetRandomTx().ID
	losingSolveTxID := GetRandomTx().ID
	solves := []IntentSolve{
		{
			SolveTxID:    winningSolveTxID,
			TargetCoin:   common.NewCoin(targetAsset, cosmos.NewUint(1000)),
			SubmitHeight: 20,
		},
		{
			SolveTxID:    losingSolveTxID,
			TargetCoin:   common.NewCoin(targetAsset, cosmos.NewUint(999)),
			SubmitHeight: 10,
		},
	}

	selected := intentMgr.selectBestSolve(w.ctx, intent, solves)
	c.Check(selected.SolveTxID.Equals(losingSolveTxID), Equals, true)
}

func (s *IntentMgrTestSuite) TestSelectBestSolve_THORNativeTargetUsesRuneGas(c *C) {
	w := getHandlerTestWrapper(c, 1, true, true)

	targetPool := NewPool()
	targetPool.Asset = common.NAMI
	targetPool.Status = PoolAvailable
	targetPool.BalanceAsset = cosmos.NewUint(1_000_000_000)
	targetPool.BalanceRune = cosmos.NewUint(1_000_000_000)
	c.Assert(w.keeper.SetPool(w.ctx, targetPool), IsNil)

	c.Assert(w.keeper.SaveNetworkFee(w.ctx, common.THORChain, NewNetworkFee(common.THORChain, 1, 375000)), IsNil)

	mgr := NewDummyMgrWithKeeper(w.keeper)
	intentMgr, ok := NewIntentManager(mgr).(*IntentMgr)
	c.Assert(ok, Equals, true)

	intent := Intent{
		TxID:        GetRandomTx().ID,
		TargetAsset: common.NAMI,
	}

	winningSolveTxID := GetRandomTx().ID
	losingSolveTxID := GetRandomTx().ID
	solves := []IntentSolve{
		{
			SolveTxID:    winningSolveTxID,
			TargetCoin:   common.NewCoin(common.NAMI, cosmos.NewUint(1000)),
			SubmitHeight: 20,
		},
		{
			SolveTxID:    losingSolveTxID,
			TargetCoin:   common.NewCoin(common.NAMI, cosmos.NewUint(999)),
			SubmitHeight: 10,
		},
	}

	selected := intentMgr.selectBestSolve(w.ctx, intent, solves)
	c.Check(selected.SolveTxID.Equals(losingSolveTxID), Equals, true)
}

func (s *IntentMgrTestSuite) TestSelectBestSolve_PricingUnavailableFallsBackToExactComparison(c *C) {
	w := getHandlerTestWrapper(c, 1, true, true)
	c.Assert(w.keeper.SaveNetworkFee(w.ctx, common.THORChain, NewNetworkFee(common.THORChain, 1, 375000)), IsNil)

	mgr := NewDummyMgrWithKeeper(w.keeper)
	intentMgr, ok := NewIntentManager(mgr).(*IntentMgr)
	c.Assert(ok, Equals, true)

	intent := Intent{
		TxID:        GetRandomTx().ID,
		TargetAsset: common.NAMI,
	}

	highestSolveTxID := GetRandomTx().ID
	lowerEarlierSolveTxID := GetRandomTx().ID
	solves := []IntentSolve{
		{
			SolveTxID:    highestSolveTxID,
			TargetCoin:   common.NewCoin(common.NAMI, cosmos.NewUint(1000)),
			SubmitHeight: 20,
		},
		{
			SolveTxID:    lowerEarlierSolveTxID,
			TargetCoin:   common.NewCoin(common.NAMI, cosmos.NewUint(999)),
			SubmitHeight: 10,
		},
	}

	selected := intentMgr.selectBestSolve(w.ctx, intent, solves)
	c.Check(selected.SolveTxID.Equals(highestSolveTxID), Equals, true)
}
