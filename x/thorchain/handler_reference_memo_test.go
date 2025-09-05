package thorchain

import (
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"
	"gopkg.in/check.v1"
	. "gopkg.in/check.v1"
)

type HandlerReferenceMemoSuite struct{}

var _ = Suite(&HandlerReferenceMemoSuite{})

type keeperReferenceMemoTest struct {
	keeper.Keeper
}

func NewKeeperReferenceMemoTest(k keeper.Keeper) *keeperReferenceMemoTest {
	return &keeperReferenceMemoTest{
		Keeper: k,
	}
}

func (k *keeperReferenceMemoTest) GetReferenceMemo(ctx cosmos.Context, asset common.Asset, ref string) (ReferenceMemo, error) {
	// simulate all references are taken
	return NewReferenceMemo(asset, "my memo", "refff", 5), nil
}

func (s *HandlerReferenceMemoSuite) TestValidate(c *C) {
	ctx, mgr := setupManagerForTest(c)
	handler := NewReferenceMemoHandler(mgr)
	signer := GetRandomBech32Addr()
	asset := common.BTCAsset

	// Create BTC pool with proper decimals
	pool := NewPool()
	pool.Asset = common.BTCAsset
	pool.BalanceAsset = cosmos.NewUint(100 * common.One)
	pool.BalanceRune = cosmos.NewUint(100 * common.One)
	pool.Decimals = 8
	c.Assert(mgr.Keeper().SetPool(ctx, pool), IsNil)

	// Enable memoless transactions
	mgr.Keeper().SetMimir(ctx, constants.MemolessTxnTTL.String(), 100)

	msg := NewMsgReferenceMemo(asset, "+:BTC.BTC", signer)
	c.Assert(handler.validate(ctx, *msg), IsNil)

	// failure cases
	msg.Memo = "invalid memo"
	c.Assert(handler.validate(ctx, *msg), NotNil)
}

func (s *HandlerReferenceMemoSuite) TestValidateWithCost(c *C) {
	ctx, mgr := setupManagerForTest(c)
	handler := NewReferenceMemoHandler(mgr)
	signer := GetRandomBech32Addr()
	asset := common.BTCAsset

	// Create BTC pool with proper decimals
	pool := NewPool()
	pool.Asset = common.BTCAsset
	pool.BalanceAsset = cosmos.NewUint(100 * common.One)
	pool.BalanceRune = cosmos.NewUint(100 * common.One)
	pool.Decimals = 8
	c.Assert(mgr.Keeper().SetPool(ctx, pool), IsNil)

	// Enable memoless transactions
	mgr.Keeper().SetMimir(ctx, constants.MemolessTxnTTL.String(), 100)

	// Set a cost for memoless transactions
	mgr.Keeper().SetMimir(ctx, constants.MemolessTxnCost.String(), 1000_00000000) // 1000 RUNE

	msg := NewMsgReferenceMemo(asset, "+:BTC.BTC", signer)

	// Should fail validation due to insufficient balance
	err := handler.validate(ctx, *msg)
	c.Assert(err, NotNil)
	c.Check(err.Error(), check.Matches, ".*insufficient RUNE balance.*")

	// Add sufficient balance to signer
	err = mgr.Keeper().MintAndSendToAccount(ctx, signer, common.NewCoin(common.RuneNative, cosmos.NewUint(1000_00000000)))
	c.Assert(err, IsNil)

	// Should now pass validation
	c.Assert(handler.validate(ctx, *msg), IsNil)
}

func (s *HandlerReferenceMemoSuite) TestHandler(c *C) {
	ctx, mgr := setupManagerForTest(c)
	handler := NewReferenceMemoHandler(mgr)
	signer := GetRandomBech32Addr()
	asset := common.BTCAsset

	// Create BTC pool with proper decimals
	pool := NewPool()
	pool.Asset = common.BTCAsset
	pool.BalanceAsset = cosmos.NewUint(100 * common.One)
	pool.BalanceRune = cosmos.NewUint(100 * common.One)
	pool.Decimals = 8
	c.Assert(mgr.Keeper().SetPool(ctx, pool), IsNil)

	// Enable memoless transactions
	mgr.Keeper().SetMimir(ctx, constants.MemolessTxnTTL.String(), 100)

	// happy path
	msg := NewMsgReferenceMemo(asset, "+:BTC.BTC", signer)
	_, err := handler.handle(ctx, *msg)
	c.Assert(err, IsNil)

	c.Check(mgr.Keeper().ReferenceMemoExists(ctx, asset, "20000"), NotNil)
}

func (s *HandlerReferenceMemoSuite) TestHandlerWithCost(c *C) {
	ctx, mgr := setupManagerForTest(c)
	handler := NewReferenceMemoHandler(mgr)
	signer := GetRandomBech32Addr()
	asset := common.BTCAsset

	// Create BTC pool with proper decimals
	pool := NewPool()
	pool.Asset = common.BTCAsset
	pool.BalanceAsset = cosmos.NewUint(100 * common.One)
	pool.BalanceRune = cosmos.NewUint(100 * common.One)
	pool.Decimals = 8
	c.Assert(mgr.Keeper().SetPool(ctx, pool), IsNil)

	// Enable memoless transactions
	mgr.Keeper().SetMimir(ctx, constants.MemolessTxnTTL.String(), 100)

	// Set a cost for memoless transactions
	cost := int64(500_00000000) // 500 RUNE
	mgr.Keeper().SetMimir(ctx, constants.MemolessTxnCost.String(), cost)

	// Add sufficient balance to signer
	err := mgr.Keeper().MintAndSendToAccount(ctx, signer, common.NewCoin(common.RuneNative, cosmos.NewUint(uint64(cost))))
	c.Assert(err, IsNil)

	// Get initial reserve balance
	initialReserveBalance := mgr.Keeper().GetRuneBalanceOfModule(ctx, ReserveName)

	msg := NewMsgReferenceMemo(asset, "+:BTC.BTC", signer)
	_, err = handler.handle(ctx, *msg)
	c.Assert(err, IsNil)

	// Check that RUNE was transferred to reserve
	finalReserveBalance := mgr.Keeper().GetRuneBalanceOfModule(ctx, ReserveName)
	expectedIncrease := cosmos.NewUint(uint64(cost))
	actualIncrease := finalReserveBalance.Sub(initialReserveBalance)
	c.Check(actualIncrease.Equal(expectedIncrease), check.Equals, true, check.Commentf("Expected increase: %s, Actual increase: %s", expectedIncrease, actualIncrease))

	// Check that reference memo was created
	c.Check(mgr.Keeper().ReferenceMemoExists(ctx, asset, "20000"), NotNil)
}

func (s *HandlerReferenceMemoSuite) TestNextReference(c *C) {
	ctx, mgr := setupManagerForTest(c)
	handler := NewReferenceMemoHandler(mgr)
	asset := common.BTCAsset

	// Test case for a position in the middle of the range
	c.Check(handler.nextReferenceWithInterval(ctx, asset, "3", 1, 99_999, 1, 3), check.Equals, "00004")

	// Test case for a position at the end of the range
	c.Check(handler.nextReferenceWithInterval(ctx, asset, "99999", 1, 99_999, 1, 3), check.Equals, "00001")

	// Test case for a position outside the range (high)
	c.Check(handler.nextReferenceWithInterval(ctx, asset, "120000", 1, 99_999, 1, 3), check.Equals, "00001")

	// Test case for a position outside the range (low)
	c.Check(handler.nextReferenceWithInterval(ctx, asset, "5", 20, 99_999, 1, 3), check.Equals, "00020")
}

func (s *HandlerReferenceMemoSuite) TestLeadingZeros(c *check.C) {
	// Test case where string length is less than the given length
	// The function should pad the string with leading zeros.
	c.Assert(leadingZeros(5, "123"), check.Equals, "00123")

	// Test case where string length is greater than the given length
	// The function should truncate the string to the given length.
	c.Assert(leadingZeros(2, "12345"), check.Equals, "12")

	// Test case where string length is equal to the given length
	// The function should return the string as it is.
	c.Assert(leadingZeros(5, "12345"), check.Equals, "12345")

	// Test case where string is empty
	// The function should return a string with the given length filled with zeros.
	c.Assert(leadingZeros(5, ""), check.Equals, "00000")

	// Test case where length is zero
	// The function should return an empty string regardless of the input string.
	c.Assert(leadingZeros(0, "12345"), check.Equals, "")
}

func (s *HandlerReferenceMemoSuite) TestCalculateAssetBasedInterval(c *check.C) {
	ctx, mgr := setupManagerForTest(c)
	handler := NewReferenceMemoHandler(mgr)

	// Create pools with proper decimals for testing
	// BTC pool with 8 decimals
	btcPool := NewPool()
	btcPool.Asset = common.BTCAsset
	btcPool.BalanceAsset = cosmos.NewUint(100 * common.One)
	btcPool.BalanceRune = cosmos.NewUint(100 * common.One)
	btcPool.Decimals = 8
	c.Assert(mgr.Keeper().SetPool(ctx, btcPool), IsNil)

	// ETH pool with 8 decimals
	ethPool := NewPool()
	ethPool.Asset = common.ETHAsset
	ethPool.BalanceAsset = cosmos.NewUint(100 * common.One)
	ethPool.BalanceRune = cosmos.NewUint(100 * common.One)
	ethPool.Decimals = 8
	c.Assert(mgr.Keeper().SetPool(ctx, ethPool), IsNil)

	// XRP pool with 6 decimals
	xrpPool := NewPool()
	xrpPool.Asset = common.XRPAsset
	xrpPool.BalanceAsset = cosmos.NewUint(100 * common.One)
	xrpPool.BalanceRune = cosmos.NewUint(100 * common.One)
	xrpPool.Decimals = 6
	c.Assert(mgr.Keeper().SetPool(ctx, xrpPool), IsNil)

	// ATOM pool with 6 decimals
	atomPool := NewPool()
	atomPool.Asset = common.ATOMAsset
	atomPool.BalanceAsset = cosmos.NewUint(100 * common.One)
	atomPool.BalanceRune = cosmos.NewUint(100 * common.One)
	atomPool.Decimals = 6
	c.Assert(mgr.Keeper().SetPool(ctx, atomPool), IsNil)

	// Test 8-decimal assets (BTC, ETH) - should have interval of 1
	btcAsset := common.BTCAsset
	interval := handler.calculateAssetBasedInterval(ctx, btcAsset)
	c.Assert(interval, check.Equals, int64(1), check.Commentf("BTC should have interval 1 for 8 decimals"))

	ethAsset := common.ETHAsset
	interval = handler.calculateAssetBasedInterval(ctx, ethAsset)
	c.Assert(interval, check.Equals, int64(1), check.Commentf("ETH should have interval 1 for 8 decimals"))

	// Test 6-decimal assets (XRP, GAIA) - should have interval of 100
	xrpAsset := common.XRPAsset
	interval = handler.calculateAssetBasedInterval(ctx, xrpAsset)
	c.Assert(interval, check.Equals, int64(100), check.Commentf("XRP should have interval 100 for 6 decimals"))

	gaiaAtom := common.ATOMAsset // GAIA/ATOM uses 6 decimals
	interval = handler.calculateAssetBasedInterval(ctx, gaiaAtom)
	c.Assert(interval, check.Equals, int64(100), check.Commentf("ATOM should have interval 100 for 6 decimals"))

	// Test empty asset - should fallback to interval of 1
	emptyAsset := common.EmptyAsset
	interval = handler.calculateAssetBasedInterval(ctx, emptyAsset)
	c.Assert(interval, check.Equals, int64(0), check.Commentf("Empty asset should fallback to interval 0"))
}
