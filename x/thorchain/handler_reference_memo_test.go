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

func (k *keeperReferenceMemoTest) GetReferenceMemo(ctx cosmos.Context, chain common.Chain, ref string) (ReferenceMemo, error) {
	// simulate all references are taken
	return NewReferenceMemo(chain, "my memo", "refff", 5), nil
}

func (s *HandlerReferenceMemoSuite) TestValidate(c *C) {
	ctx, mgr := setupManagerForTest(c)
	handler := NewReferenceMemoHandler(mgr)
	signer := GetRandomBech32Addr()
	chain := common.BTCChain

	// Enable memoless transactions
	mgr.Keeper().SetMimir(ctx, constants.MemolessTxnTTL.String(), 100)

	var blankHash common.TxID
	msg := NewMsgReferenceMemo(blankHash, chain, "+:BTC.BTC", signer)
	c.Assert(handler.validate(ctx, *msg), IsNil)

	// failure cases
	msg.Memo = "invalid memo"
	c.Assert(handler.validate(ctx, *msg), NotNil)
}

func (s *HandlerReferenceMemoSuite) TestValidateWithCost(c *C) {
	ctx, mgr := setupManagerForTest(c)
	handler := NewReferenceMemoHandler(mgr)
	signer := GetRandomBech32Addr()
	chain := common.BTCChain

	// Enable memoless transactions
	mgr.Keeper().SetMimir(ctx, constants.MemolessTxnTTL.String(), 100)

	// Set a cost for memoless transactions
	mgr.Keeper().SetMimir(ctx, constants.MemolessTxnCost.String(), 1000_00000000) // 1000 RUNE

	var blankHash common.TxID
	msg := NewMsgReferenceMemo(blankHash, chain, "+:BTC.BTC", signer)

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
	chain := common.BTCChain

	// Enable memoless transactions
	mgr.Keeper().SetMimir(ctx, constants.MemolessTxnTTL.String(), 100)

	// happy path
	var blankHash common.TxID
	msg := NewMsgReferenceMemo(blankHash, chain, "+:BTC.BTC", signer)
	_, err := handler.handle(ctx, *msg)
	c.Assert(err, IsNil)

	c.Check(mgr.Keeper().ReferenceMemoExists(ctx, chain, "20000"), NotNil)
}

func (s *HandlerReferenceMemoSuite) TestHandlerWithCost(c *C) {
	ctx, mgr := setupManagerForTest(c)
	handler := NewReferenceMemoHandler(mgr)
	signer := GetRandomBech32Addr()
	chain := common.BTCChain

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

	var blankHash common.TxID
	msg := NewMsgReferenceMemo(blankHash, chain, "+:BTC.BTC", signer)
	_, err = handler.handle(ctx, *msg)
	c.Assert(err, IsNil)

	// Check that RUNE was transferred to reserve
	finalReserveBalance := mgr.Keeper().GetRuneBalanceOfModule(ctx, ReserveName)
	expectedIncrease := cosmos.NewUint(uint64(cost))
	actualIncrease := finalReserveBalance.Sub(initialReserveBalance)
	c.Check(actualIncrease.Equal(expectedIncrease), check.Equals, true, check.Commentf("Expected increase: %s, Actual increase: %s", expectedIncrease, actualIncrease))

	// Check that reference memo was created
	c.Check(mgr.Keeper().ReferenceMemoExists(ctx, chain, "20000"), NotNil)
}

func (s *HandlerReferenceMemoSuite) TestNextReference(c *C) {
	ctx, k := setupKeeperForTest(c)
	keeper := NewKeeperReferenceMemoTest(k)
	mgr := NewDummyMgrWithKeeper(keeper)
	handler := NewReferenceMemoHandler(mgr)
	chain := common.BTCChain

	// Test case for no references are available and ensure we can't get into
	// an infinite loop
	ref := handler.nextRefence(ctx, chain, "00005", 1, 10, 100)
	c.Check(ref, Equals, "")

	// Test case for a position in the middle of the range
	c.Check(handler.nextRefence(ctx, chain, "3", 1, 10, 3), check.Equals, "00004")

	// Test case for a position at the end of the range
	c.Check(handler.nextRefence(ctx, chain, "10", 1, 10, 3), check.Equals, "00001")

	// Test case for a position outside the range (high)
	c.Check(handler.nextRefence(ctx, chain, "12", 1, 10, 3), check.Equals, "00001")

	// Test case for a position outside the range (low)
	c.Check(handler.nextRefence(ctx, chain, "5", 20, 30, 3), check.Equals, "00020")
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
