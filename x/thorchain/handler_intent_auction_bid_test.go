package thorchain

import (
	"errors"

	se "github.com/cosmos/cosmos-sdk/types/errors"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"
)

type HandlerIntentAuctionBidSuite struct{}

var _ = Suite(&HandlerIntentAuctionBidSuite{})

type HandlerIntentAuctionBidTestHelper struct {
	keeper.Keeper
	failToGetAuction    bool
	failToGetBids       bool
	failToGetBidderBid  bool
	failToGetNetworkFee bool
}

func NewHandlerIntentAuctionBidTestHelper(k keeper.Keeper) *HandlerIntentAuctionBidTestHelper {
	return &HandlerIntentAuctionBidTestHelper{
		Keeper: k,
	}
}

func (h *HandlerIntentAuctionBidTestHelper) GetIntentAuction(ctx cosmos.Context, txID common.TxID) (IntentAuction, error) {
	if h.failToGetAuction {
		return IntentAuction{}, errKaboom
	}
	return h.Keeper.GetIntentAuction(ctx, txID)
}

func (h *HandlerIntentAuctionBidTestHelper) GetIntentAuctionBidsByIntentID(ctx cosmos.Context, txID common.TxID) ([]IntentAuctionBid, error) {
	if h.failToGetBids {
		return nil, errKaboom
	}
	return h.Keeper.GetIntentAuctionBidsByIntentID(ctx, txID)
}

func (h *HandlerIntentAuctionBidTestHelper) GetBidderBidForAuction(ctx cosmos.Context, intentTxID common.TxID, bidderAddr common.Address) (IntentAuctionBid, error) {
	if h.failToGetBidderBid {
		return IntentAuctionBid{}, errKaboom
	}
	return h.Keeper.GetBidderBidForAuction(ctx, intentTxID, bidderAddr)
}

func (h *HandlerIntentAuctionBidTestHelper) GetNetworkFee(ctx cosmos.Context, chain common.Chain) (NetworkFee, error) {
	if h.failToGetNetworkFee {
		return NetworkFee{}, errKaboom
	}
	return h.Keeper.GetNetworkFee(ctx, chain)
}

func (HandlerIntentAuctionBidSuite) TestIntentAuctionBid_HappyPath(c *C) {
	w := getHandlerTestWrapper(c, 1, true, true)

	// Enable intents feature
	w.keeper.SetMimir(w.ctx, constants.IntentsEnabled.String(), 1)
	w.keeper.SetMimir(w.ctx, constants.IntentAuctionMinBidMultiplier.String(), 3)
	w.keeper.SetMimir(w.ctx, constants.IntentAuctionOvertimeBlocks.String(), 100)

	// Create an intent
	intentTx := GetRandomTx()
	intent := Intent{
		TxID:          intentTx.ID,
		SourceAddress: GetRandomTHORAddress(),
		Source:        common.NewCoin(common.DOGEAsset, cosmos.NewUint(100*common.One)),
		TargetAsset:   common.BTCAsset,
		TargetAddress: GetRandomBTCAddress(),
		HighTarget:    cosmos.NewUint(15 * common.One),
		LowTarget:     cosmos.NewUint(10 * common.One),
		ExpiryHeight:  w.ctx.BlockHeight() + 2000,
		Status:        IntentAuctioning,
	}
	w.keeper.SetIntent(w.ctx, intent)

	// Create an auction for the intent
	auction := IntentAuction{
		IntentTxID:      intentTx.ID,
		AuctionAmount:   common.NewCoin(common.DOGEAsset, cosmos.NewUint(10*common.One)),
		RemainingAmount: common.NewCoin(common.DOGEAsset, cosmos.NewUint(90*common.One)),
		BidAsset:        common.DOGEAsset.GetChain().GetGasAsset(),
		MinBid:          cosmos.NewUint(1000),
		StartHeight:     w.ctx.BlockHeight(),
		ExpiryHeight:    w.ctx.BlockHeight() + 1000,
		Status:          IntentAuctionStatus_AuctionActive,
	}
	w.keeper.SetIntentAuction(w.ctx, auction)

	mgr := NewDummyMgrWithKeeper(w.keeper)
	handler := NewIntentAuctionBidHandler(mgr)

	// Place a valid bid
	bidTx := GetRandomTx()
	bidTx.Coins = common.Coins{common.NewCoin(auction.BidAsset, cosmos.NewUint(5000))}
	msg := NewMsgIntentAuctionBid(
		bidTx,
		intentTx.ID,
		w.activeNodeAccount.NodeAddress,
	)

	_, err := handler.Run(w.ctx, msg)
	c.Assert(err, IsNil)

	// Verify bid was created
	bid, err := w.keeper.GetIntentAuctionBid(w.ctx, bidTx.ID)
	c.Assert(err, IsNil)
	c.Check(bid.BidTxID.Equals(bidTx.ID), Equals, true)
	c.Check(bid.IntentTxID.Equals(intentTx.ID), Equals, true)
	c.Check(bid.BidAmount.Amount.Equal(cosmos.NewUint(5000)), Equals, true)
}

func (HandlerIntentAuctionBidSuite) TestIntentAuctionBid_TopUp(c *C) {
	w := getHandlerTestWrapper(c, 1, true, true)

	// Enable intents feature
	w.keeper.SetMimir(w.ctx, constants.IntentsEnabled.String(), 1)
	w.keeper.SetMimir(w.ctx, constants.IntentAuctionMinBidMultiplier.String(), 3)
	w.keeper.SetMimir(w.ctx, constants.IntentAuctionOvertimeBlocks.String(), 100)

	// Create an intent
	intentTx := GetRandomTx()
	intent := Intent{
		TxID:          intentTx.ID,
		SourceAddress: GetRandomTHORAddress(),
		Source:        common.NewCoin(common.DOGEAsset, cosmos.NewUint(100*common.One)),
		TargetAsset:   common.BTCAsset,
		TargetAddress: GetRandomBTCAddress(),
		HighTarget:    cosmos.NewUint(15 * common.One),
		LowTarget:     cosmos.NewUint(10 * common.One),
		ExpiryHeight:  w.ctx.BlockHeight() + 2000,
		Status:        IntentAuctioning,
	}
	w.keeper.SetIntent(w.ctx, intent)

	// Create an auction
	auction := IntentAuction{
		IntentTxID:      intentTx.ID,
		AuctionAmount:   common.NewCoin(common.DOGEAsset, cosmos.NewUint(10*common.One)),
		RemainingAmount: common.NewCoin(common.DOGEAsset, cosmos.NewUint(90*common.One)),
		BidAsset:        common.DOGEAsset.GetChain().GetGasAsset(),
		MinBid:          cosmos.NewUint(1000),
		StartHeight:     w.ctx.BlockHeight(),
		ExpiryHeight:    w.ctx.BlockHeight() + 1000,
		Status:          IntentAuctionStatus_AuctionActive,
	}
	w.keeper.SetIntentAuction(w.ctx, auction)

	mgr := NewDummyMgrWithKeeper(w.keeper)
	handler := NewIntentAuctionBidHandler(mgr)

	// Place first bid
	bidTx1 := GetRandomTx()
	bidTx1.FromAddress = GetRandomDOGEAddress()
	bidTx1.Coins = common.Coins{common.NewCoin(auction.BidAsset, cosmos.NewUint(5000))}
	msg1 := NewMsgIntentAuctionBid(bidTx1, intentTx.ID, w.activeNodeAccount.NodeAddress)
	_, err := handler.Run(w.ctx, msg1)
	c.Assert(err, IsNil)

	// Verify first bid
	bid1, err := w.keeper.GetIntentAuctionBid(w.ctx, bidTx1.ID)
	c.Assert(err, IsNil)
	c.Check(bid1.BidAmount.Amount.Equal(cosmos.NewUint(5000)), Equals, true)

	// Top up from same bidder
	bidTx2 := GetRandomTx()
	bidTx2.FromAddress = bidTx1.FromAddress // Same bidder
	bidTx2.Coins = common.Coins{common.NewCoin(auction.BidAsset, cosmos.NewUint(3000))}
	msg2 := NewMsgIntentAuctionBid(bidTx2, intentTx.ID, w.activeNodeAccount.NodeAddress)
	_, err = handler.Run(w.ctx, msg2)
	c.Assert(err, IsNil)

	// Verify bid was topped up (original bid should now have total of 8000)
	bid1Updated, err := w.keeper.GetIntentAuctionBid(w.ctx, bidTx1.ID)
	c.Assert(err, IsNil)
	c.Check(bid1Updated.BidAmount.Amount.Equal(cosmos.NewUint(8000)), Equals, true, Commentf("Bid should be topped up"))
}

func (HandlerIntentAuctionBidSuite) TestIntentAuctionBid_OvertimeExtension(c *C) {
	w := getHandlerTestWrapper(c, 1, true, true)

	// Enable intents feature
	w.keeper.SetMimir(w.ctx, constants.IntentsEnabled.String(), 1)
	w.keeper.SetMimir(w.ctx, constants.IntentAuctionMinBidMultiplier.String(), 3)
	w.keeper.SetMimir(w.ctx, constants.IntentAuctionOvertimeBlocks.String(), 100)
	w.keeper.SetMimir(w.ctx, constants.IntentAuctionMaxOvertimeBlocks.String(), 500)
	w.keeper.SetMimir(w.ctx, constants.IntentAuctionDurationBlocks.String(), 200)

	currentHeight := w.ctx.BlockHeight()

	// Create an intent
	intentTx := GetRandomTx()
	intent := Intent{
		TxID:          intentTx.ID,
		SourceAddress: GetRandomTHORAddress(),
		Source:        common.NewCoin(common.DOGEAsset, cosmos.NewUint(100*common.One)),
		TargetAsset:   common.BTCAsset,
		TargetAddress: GetRandomBTCAddress(),
		HighTarget:    cosmos.NewUint(15 * common.One),
		LowTarget:     cosmos.NewUint(10 * common.One),
		ExpiryHeight:  currentHeight + 2000,
		Status:        IntentAuctioning,
	}
	w.keeper.SetIntent(w.ctx, intent)

	// Create an auction in overtime window. For overtime detection:
	// - ExpiryHeight must be >= overtimeBlocks (100) for the underflow guard
	// - currentHeight must be >= ExpiryHeight - overtimeBlocks (we're close to expiry)
	// - ExpiryHeight must be > currentHeight (auction hasn't expired yet)
	// Using currentHeight + 99 satisfies all conditions.
	auctionExpiry := currentHeight + 99
	auction := IntentAuction{
		IntentTxID:      intentTx.ID,
		AuctionAmount:   common.NewCoin(common.DOGEAsset, cosmos.NewUint(10*common.One)),
		RemainingAmount: common.NewCoin(common.DOGEAsset, cosmos.NewUint(90*common.One)),
		BidAsset:        common.DOGEAsset.GetChain().GetGasAsset(),
		MinBid:          cosmos.NewUint(1000),
		StartHeight:     currentHeight - 100,
		ExpiryHeight:    auctionExpiry,
		Status:          IntentAuctionStatus_AuctionActive,
	}
	w.keeper.SetIntentAuction(w.ctx, auction)

	mgr := NewDummyMgrWithKeeper(w.keeper)
	handler := NewIntentAuctionBidHandler(mgr)

	// Place first bid from bidder A (establishes baseline)
	bidTx1 := GetRandomTx()
	bidTx1.Coins = common.Coins{common.NewCoin(auction.BidAsset, cosmos.NewUint(5000))}
	msg1 := NewMsgIntentAuctionBid(bidTx1, intentTx.ID, w.activeNodeAccount.NodeAddress)
	_, err := handler.Run(w.ctx, msg1)
	c.Assert(err, IsNil)

	// Place higher bid from a DIFFERENT bidder to trigger overtime extension.
	// Gas fee for DOGE is 37500 (150 * 250), so bid must be >= 5000 + 37500 = 42500
	bidTx2 := GetRandomTx()
	bidTx2.FromAddress = GetRandomDOGEAddress()
	bidTx2.Coins = common.Coins{common.NewCoin(auction.BidAsset, cosmos.NewUint(100000))} // High enough to exceed overtime minimum
	msg2 := NewMsgIntentAuctionBid(bidTx2, intentTx.ID, w.activeNodeAccount.NodeAddress)
	_, err = handler.Run(w.ctx, msg2)
	c.Assert(err, IsNil)

	// Check auction expiry was extended
	updatedAuction, err := w.keeper.GetIntentAuction(w.ctx, intentTx.ID)
	c.Assert(err, IsNil)
	c.Check(updatedAuction.ExpiryHeight > auctionExpiry, Equals, true, Commentf("Auction should be extended in overtime"))
}

// Regression test for the validate()/handle() alignment: the first bid in the
// overtime window must not extend the auction.
func (HandlerIntentAuctionBidSuite) TestIntentAuctionBid_FirstBidInOvertimeDoesNotExtend(c *C) {
	w := getHandlerTestWrapper(c, 1, true, true)

	w.keeper.SetMimir(w.ctx, constants.IntentsEnabled.String(), 1)
	w.keeper.SetMimir(w.ctx, constants.IntentAuctionMinBidMultiplier.String(), 3)
	w.keeper.SetMimir(w.ctx, constants.IntentAuctionOvertimeBlocks.String(), 100)
	w.keeper.SetMimir(w.ctx, constants.IntentAuctionMaxOvertimeBlocks.String(), 500)
	w.keeper.SetMimir(w.ctx, constants.IntentAuctionDurationBlocks.String(), 200)

	currentHeight := w.ctx.BlockHeight()
	intentTx := GetRandomTx()
	intent := Intent{
		TxID:          intentTx.ID,
		SourceAddress: GetRandomTHORAddress(),
		Source:        common.NewCoin(common.DOGEAsset, cosmos.NewUint(100*common.One)),
		TargetAsset:   common.BTCAsset,
		TargetAddress: GetRandomBTCAddress(),
		HighTarget:    cosmos.NewUint(15 * common.One),
		LowTarget:     cosmos.NewUint(10 * common.One),
		ExpiryHeight:  currentHeight + 2000,
		Status:        IntentAuctioning,
	}
	w.keeper.SetIntent(w.ctx, intent)

	auctionExpiry := currentHeight + 99
	auction := IntentAuction{
		IntentTxID:      intentTx.ID,
		AuctionAmount:   common.NewCoin(common.DOGEAsset, cosmos.NewUint(10*common.One)),
		RemainingAmount: common.NewCoin(common.DOGEAsset, cosmos.NewUint(90*common.One)),
		BidAsset:        common.DOGEAsset.GetChain().GetGasAsset(),
		MinBid:          cosmos.NewUint(1000),
		StartHeight:     currentHeight - 100,
		ExpiryHeight:    auctionExpiry,
		Status:          IntentAuctionStatus_AuctionActive,
	}
	w.keeper.SetIntentAuction(w.ctx, auction)

	handler := NewIntentAuctionBidHandler(NewDummyMgrWithKeeper(w.keeper))

	bidTx := GetRandomTx()
	bidTx.Coins = common.Coins{common.NewCoin(auction.BidAsset, cosmos.NewUint(100000))}
	msg := NewMsgIntentAuctionBid(bidTx, intentTx.ID, w.activeNodeAccount.NodeAddress)

	_, err := handler.Run(w.ctx, msg)
	c.Assert(err, IsNil)

	updatedAuction, err := w.keeper.GetIntentAuction(w.ctx, intentTx.ID)
	c.Assert(err, IsNil)
	c.Check(updatedAuction.ExpiryHeight, Equals, auctionExpiry, Commentf("first bid should not extend auction in overtime"))
}

func (HandlerIntentAuctionBidSuite) TestIntentAuctionBid_ValidationErrors(c *C) {
	w := getHandlerTestWrapper(c, 1, true, true)

	// Enable intents feature
	w.keeper.SetMimir(w.ctx, constants.IntentsEnabled.String(), 1)
	w.keeper.SetMimir(w.ctx, constants.IntentAuctionMinBidMultiplier.String(), 3)

	// Create an intent
	intentTx := GetRandomTx()
	intent := Intent{
		TxID:          intentTx.ID,
		SourceAddress: GetRandomTHORAddress(),
		Source:        common.NewCoin(common.DOGEAsset, cosmos.NewUint(100*common.One)),
		TargetAsset:   common.BTCAsset,
		TargetAddress: GetRandomBTCAddress(),
		HighTarget:    cosmos.NewUint(15 * common.One),
		LowTarget:     cosmos.NewUint(10 * common.One),
		ExpiryHeight:  w.ctx.BlockHeight() + 2000,
		Status:        IntentAuctioning,
	}
	w.keeper.SetIntent(w.ctx, intent)

	// Create an auction
	auction := IntentAuction{
		IntentTxID:      intentTx.ID,
		AuctionAmount:   common.NewCoin(common.DOGEAsset, cosmos.NewUint(10*common.One)),
		RemainingAmount: common.NewCoin(common.DOGEAsset, cosmos.NewUint(90*common.One)),
		BidAsset:        common.DOGEAsset.GetChain().GetGasAsset(),
		MinBid:          cosmos.NewUint(1000),
		StartHeight:     w.ctx.BlockHeight(),
		ExpiryHeight:    w.ctx.BlockHeight() + 1000,
		Status:          IntentAuctionStatus_AuctionActive,
	}
	w.keeper.SetIntentAuction(w.ctx, auction)

	bidTx := GetRandomTx()
	bidTx.Coins = common.Coins{common.NewCoin(auction.BidAsset, cosmos.NewUint(5000))}

	testCases := []struct {
		name        string
		setupMsg    func() *MsgIntentAuctionBid
		expectedErr error
	}{
		{
			name: "feature disabled should fail",
			setupMsg: func() *MsgIntentAuctionBid {
				w.keeper.SetMimir(w.ctx, constants.IntentsEnabled.String(), 0)
				return NewMsgIntentAuctionBid(bidTx, intentTx.ID, w.activeNodeAccount.NodeAddress)
			},
			expectedErr: se.ErrUnauthorized,
		},
		{
			name: "non-existent auction should fail",
			setupMsg: func() *MsgIntentAuctionBid {
				w.keeper.SetMimir(w.ctx, constants.IntentsEnabled.String(), 1)
				fakeTxID := GetRandomTxHash()
				return NewMsgIntentAuctionBid(bidTx, fakeTxID, w.activeNodeAccount.NodeAddress)
			},
			expectedErr: se.ErrUnknownRequest,
		},
		{
			name: "inactive auction should fail",
			setupMsg: func() *MsgIntentAuctionBid {
				inactiveAuction := auction
				inactiveAuction.Status = IntentAuctionStatus_AuctionCompleted
				w.keeper.SetIntentAuction(w.ctx, inactiveAuction)
				return NewMsgIntentAuctionBid(bidTx, intentTx.ID, w.activeNodeAccount.NodeAddress)
			},
			expectedErr: se.ErrUnauthorized,
		},
		{
			name: "expired auction should fail",
			setupMsg: func() *MsgIntentAuctionBid {
				expiredAuction := auction
				expiredAuction.Status = IntentAuctionStatus_AuctionActive
				expiredAuction.ExpiryHeight = w.ctx.BlockHeight() - 1 // Expired
				w.keeper.SetIntentAuction(w.ctx, expiredAuction)
				return NewMsgIntentAuctionBid(bidTx, intentTx.ID, w.activeNodeAccount.NodeAddress)
			},
			expectedErr: se.ErrUnauthorized,
		},
		{
			name: "no coins should fail",
			setupMsg: func() *MsgIntentAuctionBid {
				// Reset to valid auction
				auction.ExpiryHeight = w.ctx.BlockHeight() + 1000
				w.keeper.SetIntentAuction(w.ctx, auction)

				noCoinsTx := GetRandomTx()
				noCoinsTx.Coins = common.Coins{}
				return NewMsgIntentAuctionBid(noCoinsTx, intentTx.ID, w.activeNodeAccount.NodeAddress)
			},
			expectedErr: se.ErrUnknownRequest, // ValidateBasic returns ErrUnknownRequest for coin validation
		},
		{
			name: "multiple coins should fail",
			setupMsg: func() *MsgIntentAuctionBid {
				multiTx := GetRandomTx()
				multiTx.Coins = common.Coins{
					common.NewCoin(auction.BidAsset, cosmos.NewUint(5000)),
					common.NewCoin(common.BTCAsset, cosmos.NewUint(1000)),
				}
				return NewMsgIntentAuctionBid(multiTx, intentTx.ID, w.activeNodeAccount.NodeAddress)
			},
			expectedErr: se.ErrUnknownRequest, // ValidateBasic returns ErrUnknownRequest for coin validation
		},
		{
			name: "wrong asset should fail",
			setupMsg: func() *MsgIntentAuctionBid {
				wrongAssetTx := GetRandomTx()
				wrongAssetTx.Coins = common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(5000))}
				return NewMsgIntentAuctionBid(wrongAssetTx, intentTx.ID, w.activeNodeAccount.NodeAddress)
			},
			expectedErr: se.ErrInvalidRequest,
		},
		{
			name: "bid below minimum should fail",
			setupMsg: func() *MsgIntentAuctionBid {
				lowBidTx := GetRandomTx()
				lowBidTx.Coins = common.Coins{common.NewCoin(auction.BidAsset, cosmos.NewUint(100))} // Below MinBid of 1000
				return NewMsgIntentAuctionBid(lowBidTx, intentTx.ID, w.activeNodeAccount.NodeAddress)
			},
			expectedErr: se.ErrInvalidRequest,
		},
	}

	for _, tc := range testCases {
		msg := tc.setupMsg()
		handler := NewIntentAuctionBidHandler(NewDummyMgrWithKeeper(w.keeper))
		_, err := handler.Run(w.ctx, msg)
		c.Check(errors.Is(err, tc.expectedErr), Equals, true, Commentf("name:%s, got err: %v", tc.name, err))
	}
}

func (HandlerIntentAuctionBidSuite) TestIntentAuctionBid_KeeperErrors(c *C) {
	w := getHandlerTestWrapper(c, 1, true, true)

	// Enable intents feature
	w.keeper.SetMimir(w.ctx, constants.IntentsEnabled.String(), 1)

	// Create an intent
	intentTx := GetRandomTx()
	intent := Intent{
		TxID:          intentTx.ID,
		SourceAddress: GetRandomTHORAddress(),
		Source:        common.NewCoin(common.DOGEAsset, cosmos.NewUint(100*common.One)),
		TargetAsset:   common.BTCAsset,
		TargetAddress: GetRandomBTCAddress(),
		HighTarget:    cosmos.NewUint(15 * common.One),
		LowTarget:     cosmos.NewUint(10 * common.One),
		ExpiryHeight:  w.ctx.BlockHeight() + 2000,
		Status:        IntentAuctioning,
	}
	w.keeper.SetIntent(w.ctx, intent)

	// Create an auction
	auction := IntentAuction{
		IntentTxID:      intentTx.ID,
		AuctionAmount:   common.NewCoin(common.DOGEAsset, cosmos.NewUint(10*common.One)),
		RemainingAmount: common.NewCoin(common.DOGEAsset, cosmos.NewUint(90*common.One)),
		BidAsset:        common.DOGEAsset.GetChain().GetGasAsset(),
		MinBid:          cosmos.NewUint(1000),
		StartHeight:     w.ctx.BlockHeight(),
		ExpiryHeight:    w.ctx.BlockHeight() + 1000,
		Status:          IntentAuctionStatus_AuctionActive,
	}
	w.keeper.SetIntentAuction(w.ctx, auction)

	bidTx := GetRandomTx()
	bidTx.Coins = common.Coins{common.NewCoin(auction.BidAsset, cosmos.NewUint(5000))}
	msg := NewMsgIntentAuctionBid(bidTx, intentTx.ID, w.activeNodeAccount.NodeAddress)

	// Test GetAuction failure
	testKeeper := NewHandlerIntentAuctionBidTestHelper(w.keeper)
	testKeeper.failToGetAuction = true
	handler := NewIntentAuctionBidHandler(NewDummyMgrWithKeeper(testKeeper))
	result, err := handler.Run(w.ctx, msg)
	c.Check(err, NotNil)
	c.Check(errors.Is(err, errInternal), Equals, true, Commentf("GetAuction failure should return internal error"))
	c.Check(result, IsNil)

	// Test GetBids failure (this tests Issue 6 fix)
	testKeeper2 := NewHandlerIntentAuctionBidTestHelper(w.keeper)
	testKeeper2.failToGetBids = true
	handler2 := NewIntentAuctionBidHandler(NewDummyMgrWithKeeper(testKeeper2))
	result2, err := handler2.Run(w.ctx, msg)
	c.Check(err, NotNil)
	c.Check(errors.Is(err, errInternal), Equals, true, Commentf("GetBids failure should now return internal error"))
	c.Check(result2, IsNil)
}

func (HandlerIntentAuctionBidSuite) TestIntentAuctionBid_InvalidMessage(c *C) {
	w := getHandlerTestWrapper(c, 1, true, true)

	mgr := NewDummyMgrWithKeeper(w.keeper)
	handler := NewIntentAuctionBidHandler(mgr)

	// Pass wrong message type
	msgBan := NewMsgBan(GetRandomBech32Addr(), w.activeNodeAccount.NodeAddress)
	result, err := handler.Run(w.ctx, msgBan)
	c.Check(err, NotNil)
	c.Check(errors.Is(err, errInvalidMessage), Equals, true)
	c.Check(result, IsNil)
}

func (HandlerIntentAuctionBidSuite) TestIntentAuctionBid_BidMustExceedHighest(c *C) {
	w := getHandlerTestWrapper(c, 1, true, true)

	// Enable intents feature
	w.keeper.SetMimir(w.ctx, constants.IntentsEnabled.String(), 1)
	w.keeper.SetMimir(w.ctx, constants.IntentAuctionMinBidMultiplier.String(), 3)
	w.keeper.SetMimir(w.ctx, constants.IntentAuctionOvertimeBlocks.String(), 100)

	// Create an intent
	intentTx := GetRandomTx()
	intent := Intent{
		TxID:          intentTx.ID,
		SourceAddress: GetRandomTHORAddress(),
		Source:        common.NewCoin(common.DOGEAsset, cosmos.NewUint(100*common.One)),
		TargetAsset:   common.BTCAsset,
		TargetAddress: GetRandomBTCAddress(),
		HighTarget:    cosmos.NewUint(15 * common.One),
		LowTarget:     cosmos.NewUint(10 * common.One),
		ExpiryHeight:  w.ctx.BlockHeight() + 2000,
		Status:        IntentAuctioning,
	}
	w.keeper.SetIntent(w.ctx, intent)

	// Create an auction
	auction := IntentAuction{
		IntentTxID:      intentTx.ID,
		AuctionAmount:   common.NewCoin(common.DOGEAsset, cosmos.NewUint(10*common.One)),
		RemainingAmount: common.NewCoin(common.DOGEAsset, cosmos.NewUint(90*common.One)),
		BidAsset:        common.DOGEAsset.GetChain().GetGasAsset(),
		MinBid:          cosmos.NewUint(1000),
		StartHeight:     w.ctx.BlockHeight(),
		ExpiryHeight:    w.ctx.BlockHeight() + 1000,
		Status:          IntentAuctionStatus_AuctionActive,
	}
	w.keeper.SetIntentAuction(w.ctx, auction)

	mgr := NewDummyMgrWithKeeper(w.keeper)
	handler := NewIntentAuctionBidHandler(mgr)

	// Place first bid
	bidTx1 := GetRandomTx()
	bidTx1.Coins = common.Coins{common.NewCoin(auction.BidAsset, cosmos.NewUint(5000))}
	msg1 := NewMsgIntentAuctionBid(bidTx1, intentTx.ID, w.activeNodeAccount.NodeAddress)
	_, err := handler.Run(w.ctx, msg1)
	c.Assert(err, IsNil)

	// Try to place lower or equal bid (should fail)
	bidTx2 := GetRandomTx()
	bidTx2.Coins = common.Coins{common.NewCoin(auction.BidAsset, cosmos.NewUint(4000))} // Lower than current highest
	msg2 := NewMsgIntentAuctionBid(bidTx2, intentTx.ID, w.activeNodeAccount.NodeAddress)
	_, err = handler.Run(w.ctx, msg2)
	c.Check(errors.Is(err, se.ErrInvalidRequest), Equals, true, Commentf("Bid lower than highest should fail"))

	// Place higher bid (should succeed)
	bidTx3 := GetRandomTx()
	bidTx3.Coins = common.Coins{common.NewCoin(auction.BidAsset, cosmos.NewUint(6000))} // Higher than current highest
	msg3 := NewMsgIntentAuctionBid(bidTx3, intentTx.ID, w.activeNodeAccount.NodeAddress)
	_, err = handler.Run(w.ctx, msg3)
	c.Assert(err, IsNil)
}

func (HandlerIntentAuctionBidSuite) TestIntentAuctionBid_GetBidderBidFailure(c *C) {
	w := getHandlerTestWrapper(c, 1, true, true)

	// Enable intents feature
	w.keeper.SetMimir(w.ctx, constants.IntentsEnabled.String(), 1)

	// Create an intent
	intentTx := GetRandomTx()
	intent := Intent{
		TxID:          intentTx.ID,
		SourceAddress: GetRandomTHORAddress(),
		Source:        common.NewCoin(common.DOGEAsset, cosmos.NewUint(100*common.One)),
		TargetAsset:   common.BTCAsset,
		TargetAddress: GetRandomBTCAddress(),
		HighTarget:    cosmos.NewUint(15 * common.One),
		LowTarget:     cosmos.NewUint(10 * common.One),
		ExpiryHeight:  w.ctx.BlockHeight() + 2000,
		Status:        IntentAuctioning,
	}
	w.keeper.SetIntent(w.ctx, intent)

	// Create an auction
	auction := IntentAuction{
		IntentTxID:      intentTx.ID,
		AuctionAmount:   common.NewCoin(common.DOGEAsset, cosmos.NewUint(10*common.One)),
		RemainingAmount: common.NewCoin(common.DOGEAsset, cosmos.NewUint(90*common.One)),
		BidAsset:        common.DOGEAsset.GetChain().GetGasAsset(),
		MinBid:          cosmos.NewUint(1000),
		StartHeight:     w.ctx.BlockHeight(),
		ExpiryHeight:    w.ctx.BlockHeight() + 1000,
		Status:          IntentAuctionStatus_AuctionActive,
	}
	w.keeper.SetIntentAuction(w.ctx, auction)

	bidTx := GetRandomTx()
	bidTx.Coins = common.Coins{common.NewCoin(auction.BidAsset, cosmos.NewUint(5000))}
	msg := NewMsgIntentAuctionBid(bidTx, intentTx.ID, w.activeNodeAccount.NodeAddress)

	// Test GetBidderBidForAuction failure in validate()
	testKeeper := NewHandlerIntentAuctionBidTestHelper(w.keeper)
	testKeeper.failToGetBidderBid = true
	handler := NewIntentAuctionBidHandler(NewDummyMgrWithKeeper(testKeeper))
	result, err := handler.Run(w.ctx, msg)
	c.Check(err, NotNil)
	c.Check(errors.Is(err, errInternal), Equals, true, Commentf("GetBidderBidForAuction failure should return internal error"))
	c.Check(result, IsNil)
}

// Enforces a per-auction bidder cap while still allowing existing bidders to
// top up after the cap is reached.
func (HandlerIntentAuctionBidSuite) TestIntentAuctionBid_MaxBidsPerAuction(c *C) {
	w := getHandlerTestWrapper(c, 1, true, true)

	// Enable intents feature with a low bid cap.
	w.keeper.SetMimir(w.ctx, constants.IntentsEnabled.String(), 1)
	w.keeper.SetMimir(w.ctx, constants.IntentAuctionMinBidMultiplier.String(), 3)
	w.keeper.SetMimir(w.ctx, constants.IntentMaxBidsPerAuction.String(), 2)

	intentTx := GetRandomTx()
	intent := Intent{
		TxID:          intentTx.ID,
		SourceAddress: GetRandomTHORAddress(),
		Source:        common.NewCoin(common.DOGEAsset, cosmos.NewUint(100*common.One)),
		TargetAsset:   common.BTCAsset,
		TargetAddress: GetRandomBTCAddress(),
		HighTarget:    cosmos.NewUint(15 * common.One),
		LowTarget:     cosmos.NewUint(10 * common.One),
		ExpiryHeight:  w.ctx.BlockHeight() + 2000,
		Status:        IntentAuctioning,
	}
	w.keeper.SetIntent(w.ctx, intent)

	auction := IntentAuction{
		IntentTxID:      intentTx.ID,
		AuctionAmount:   common.NewCoin(common.DOGEAsset, cosmos.NewUint(10*common.One)),
		RemainingAmount: common.NewCoin(common.DOGEAsset, cosmos.NewUint(90*common.One)),
		BidAsset:        common.DOGEAsset.GetChain().GetGasAsset(),
		MinBid:          cosmos.NewUint(1000),
		StartHeight:     w.ctx.BlockHeight(),
		ExpiryHeight:    w.ctx.BlockHeight() + 1000,
		Status:          IntentAuctionStatus_AuctionActive,
	}
	w.keeper.SetIntentAuction(w.ctx, auction)

	handler := NewIntentAuctionBidHandler(NewDummyMgrWithKeeper(w.keeper))

	bidder1 := GetRandomDOGEAddress()
	bidder2 := GetRandomDOGEAddress()

	// First two distinct bidders should succeed.
	for i, bidder := range []common.Address{bidder1, bidder2} {
		bidTx := GetRandomTx()
		bidTx.FromAddress = bidder
		bidTx.Coins = common.Coins{common.NewCoin(auction.BidAsset, cosmos.NewUint(uint64(5000+i*1000)))}
		msg := NewMsgIntentAuctionBid(bidTx, intentTx.ID, w.activeNodeAccount.NodeAddress)
		_, err := handler.Run(w.ctx, msg)
		c.Assert(err, IsNil, Commentf("bid %d should succeed", i+1))
	}

	// A third distinct bidder should be rejected once the cap is reached.
	bidTx3 := GetRandomTx()
	bidTx3.FromAddress = GetRandomDOGEAddress()
	bidTx3.Coins = common.Coins{common.NewCoin(auction.BidAsset, cosmos.NewUint(7000))}
	msg3 := NewMsgIntentAuctionBid(bidTx3, intentTx.ID, w.activeNodeAccount.NodeAddress)
	_, err := handler.Run(w.ctx, msg3)
	c.Assert(err, NotNil)
	c.Check(err.Error(), Matches, ".*maximum number of bids.*")

	// Top-ups by an existing bidder should still be allowed after the cap is reached.
	topUpTx := GetRandomTx()
	topUpTx.FromAddress = bidder1
	topUpTx.Coins = common.Coins{common.NewCoin(auction.BidAsset, cosmos.NewUint(2000))}
	topUpMsg := NewMsgIntentAuctionBid(topUpTx, intentTx.ID, w.activeNodeAccount.NodeAddress)
	_, err = handler.Run(w.ctx, topUpMsg)
	c.Assert(err, IsNil)
}

func (HandlerIntentAuctionBidSuite) TestIntentAuctionBid_OvertimeBidRejection(c *C) {
	w := getHandlerTestWrapper(c, 1, true, true)

	// Enable intents feature
	w.keeper.SetMimir(w.ctx, constants.IntentsEnabled.String(), 1)
	w.keeper.SetMimir(w.ctx, constants.IntentAuctionMinBidMultiplier.String(), 3)
	w.keeper.SetMimir(w.ctx, constants.IntentAuctionOvertimeBlocks.String(), 100)

	currentHeight := w.ctx.BlockHeight()

	// Create an intent
	intentTx := GetRandomTx()
	intent := Intent{
		TxID:          intentTx.ID,
		SourceAddress: GetRandomTHORAddress(),
		Source:        common.NewCoin(common.DOGEAsset, cosmos.NewUint(100*common.One)),
		TargetAsset:   common.BTCAsset,
		TargetAddress: GetRandomBTCAddress(),
		HighTarget:    cosmos.NewUint(15 * common.One),
		LowTarget:     cosmos.NewUint(10 * common.One),
		ExpiryHeight:  currentHeight + 2000,
		Status:        IntentAuctioning,
	}
	w.keeper.SetIntent(w.ctx, intent)

	// Create an auction in overtime window. Using currentHeight + 99 ensures:
	// - ExpiryHeight >= overtimeBlocks (100) for the underflow guard
	// - currentHeight >= ExpiryHeight - overtimeBlocks (in overtime window)
	// - ExpiryHeight > currentHeight (not expired)
	auctionExpiry := currentHeight + 99
	auction := IntentAuction{
		IntentTxID:      intentTx.ID,
		AuctionAmount:   common.NewCoin(common.DOGEAsset, cosmos.NewUint(10*common.One)),
		RemainingAmount: common.NewCoin(common.DOGEAsset, cosmos.NewUint(90*common.One)),
		BidAsset:        common.DOGEAsset.GetChain().GetGasAsset(),
		MinBid:          cosmos.NewUint(1000),
		StartHeight:     currentHeight - 100,
		ExpiryHeight:    auctionExpiry,
		Status:          IntentAuctionStatus_AuctionActive,
	}
	w.keeper.SetIntentAuction(w.ctx, auction)

	mgr := NewDummyMgrWithKeeper(w.keeper)
	handler := NewIntentAuctionBidHandler(mgr)

	// Place first bid from bidder A (establishes baseline of 5000)
	bidTx1 := GetRandomTx()
	bidTx1.Coins = common.Coins{common.NewCoin(auction.BidAsset, cosmos.NewUint(5000))}
	msg1 := NewMsgIntentAuctionBid(bidTx1, intentTx.ID, w.activeNodeAccount.NodeAddress)
	_, err := handler.Run(w.ctx, msg1)
	c.Assert(err, IsNil)

	// Try to place a bid from a DIFFERENT bidder that exceeds highest but is
	// less than highest + gas fee.
	// Gas fee for DOGE is 37500 (150 * 250), so minimum overtime bid should be 5000 + 37500 = 42500
	// A bid of 6000 exceeds 5000 but is less than 42500
	bidTx2 := GetRandomTx()
	bidTx2.FromAddress = GetRandomDOGEAddress()
	bidTx2.Coins = common.Coins{common.NewCoin(auction.BidAsset, cosmos.NewUint(6000))}
	msg2 := NewMsgIntentAuctionBid(bidTx2, intentTx.ID, w.activeNodeAccount.NodeAddress)
	_, err = handler.Run(w.ctx, msg2)
	c.Check(errors.Is(err, se.ErrInvalidRequest), Equals, true, Commentf("Bid exceeding highest but below overtime minimum should fail"))
}

func (HandlerIntentAuctionBidSuite) TestIntentAuctionBid_BoundaryConditions(c *C) {
	w := getHandlerTestWrapper(c, 1, true, true)

	// Enable intents feature
	w.keeper.SetMimir(w.ctx, constants.IntentsEnabled.String(), 1)
	w.keeper.SetMimir(w.ctx, constants.IntentAuctionMinBidMultiplier.String(), 3)
	w.keeper.SetMimir(w.ctx, constants.IntentAuctionOvertimeBlocks.String(), 100)

	// Create an intent
	intentTx := GetRandomTx()
	intent := Intent{
		TxID:          intentTx.ID,
		SourceAddress: GetRandomTHORAddress(),
		Source:        common.NewCoin(common.DOGEAsset, cosmos.NewUint(100*common.One)),
		TargetAsset:   common.BTCAsset,
		TargetAddress: GetRandomBTCAddress(),
		HighTarget:    cosmos.NewUint(15 * common.One),
		LowTarget:     cosmos.NewUint(10 * common.One),
		ExpiryHeight:  w.ctx.BlockHeight() + 2000,
		Status:        IntentAuctioning,
	}
	w.keeper.SetIntent(w.ctx, intent)

	// Create an auction with MinBid of 1000
	auction := IntentAuction{
		IntentTxID:      intentTx.ID,
		AuctionAmount:   common.NewCoin(common.DOGEAsset, cosmos.NewUint(10*common.One)),
		RemainingAmount: common.NewCoin(common.DOGEAsset, cosmos.NewUint(90*common.One)),
		BidAsset:        common.DOGEAsset.GetChain().GetGasAsset(),
		MinBid:          cosmos.NewUint(1000),
		StartHeight:     w.ctx.BlockHeight(),
		ExpiryHeight:    w.ctx.BlockHeight() + 1000,
		Status:          IntentAuctionStatus_AuctionActive,
	}
	w.keeper.SetIntentAuction(w.ctx, auction)

	mgr := NewDummyMgrWithKeeper(w.keeper)
	handler := NewIntentAuctionBidHandler(mgr)

	// Test 1: First bid exactly at minimum should succeed
	bidTx1 := GetRandomTx()
	bidTx1.Coins = common.Coins{common.NewCoin(auction.BidAsset, cosmos.NewUint(1000))} // Exactly MinBid
	msg1 := NewMsgIntentAuctionBid(bidTx1, intentTx.ID, w.activeNodeAccount.NodeAddress)
	_, err := handler.Run(w.ctx, msg1)
	c.Assert(err, IsNil, Commentf("First bid exactly at minimum should succeed"))

	// Test 2: Bid exactly equal to highest should fail
	bidTx2 := GetRandomTx()
	bidTx2.Coins = common.Coins{common.NewCoin(auction.BidAsset, cosmos.NewUint(1000))} // Equal to current highest
	msg2 := NewMsgIntentAuctionBid(bidTx2, intentTx.ID, w.activeNodeAccount.NodeAddress)
	_, err = handler.Run(w.ctx, msg2)
	c.Check(errors.Is(err, se.ErrInvalidRequest), Equals, true, Commentf("Bid equal to highest should fail"))

	// Test 3: Bid one unit above highest should succeed
	bidTx3 := GetRandomTx()
	bidTx3.Coins = common.Coins{common.NewCoin(auction.BidAsset, cosmos.NewUint(1001))} // One above current highest
	msg3 := NewMsgIntentAuctionBid(bidTx3, intentTx.ID, w.activeNodeAccount.NodeAddress)
	_, err = handler.Run(w.ctx, msg3)
	c.Assert(err, IsNil, Commentf("Bid one unit above highest should succeed"))
}

func (HandlerIntentAuctionBidSuite) TestIntentAuctionBid_ZeroAmount(c *C) {
	w := getHandlerTestWrapper(c, 1, true, true)

	// Enable intents feature
	w.keeper.SetMimir(w.ctx, constants.IntentsEnabled.String(), 1)

	// Create an intent
	intentTx := GetRandomTx()
	intent := Intent{
		TxID:          intentTx.ID,
		SourceAddress: GetRandomTHORAddress(),
		Source:        common.NewCoin(common.DOGEAsset, cosmos.NewUint(100*common.One)),
		TargetAsset:   common.BTCAsset,
		TargetAddress: GetRandomBTCAddress(),
		HighTarget:    cosmos.NewUint(15 * common.One),
		LowTarget:     cosmos.NewUint(10 * common.One),
		ExpiryHeight:  w.ctx.BlockHeight() + 2000,
		Status:        IntentAuctioning,
	}
	w.keeper.SetIntent(w.ctx, intent)

	// Create an auction
	auction := IntentAuction{
		IntentTxID:      intentTx.ID,
		AuctionAmount:   common.NewCoin(common.DOGEAsset, cosmos.NewUint(10*common.One)),
		RemainingAmount: common.NewCoin(common.DOGEAsset, cosmos.NewUint(90*common.One)),
		BidAsset:        common.DOGEAsset.GetChain().GetGasAsset(),
		MinBid:          cosmos.NewUint(1000),
		StartHeight:     w.ctx.BlockHeight(),
		ExpiryHeight:    w.ctx.BlockHeight() + 1000,
		Status:          IntentAuctionStatus_AuctionActive,
	}
	w.keeper.SetIntentAuction(w.ctx, auction)

	mgr := NewDummyMgrWithKeeper(w.keeper)
	handler := NewIntentAuctionBidHandler(mgr)

	// Place a bid with zero amount
	bidTx := GetRandomTx()
	bidTx.Coins = common.Coins{common.NewCoin(auction.BidAsset, cosmos.ZeroUint())}
	msg := NewMsgIntentAuctionBid(bidTx, intentTx.ID, w.activeNodeAccount.NodeAddress)
	_, err := handler.Run(w.ctx, msg)
	c.Assert(err, NotNil)
	c.Check(err.Error(), Matches, ".*amount cannot be zero.*")
}

func (HandlerIntentAuctionBidSuite) TestIntentAuctionBid_NetworkFeeFailure(c *C) {
	w := getHandlerTestWrapper(c, 1, true, true)

	// Enable intents feature
	w.keeper.SetMimir(w.ctx, constants.IntentsEnabled.String(), 1)
	w.keeper.SetMimir(w.ctx, constants.IntentAuctionMinBidMultiplier.String(), 3)
	w.keeper.SetMimir(w.ctx, constants.IntentAuctionOvertimeBlocks.String(), 100)

	currentHeight := w.ctx.BlockHeight()

	// Create an intent
	intentTx := GetRandomTx()
	intent := Intent{
		TxID:          intentTx.ID,
		SourceAddress: GetRandomTHORAddress(),
		Source:        common.NewCoin(common.DOGEAsset, cosmos.NewUint(100*common.One)),
		TargetAsset:   common.BTCAsset,
		TargetAddress: GetRandomBTCAddress(),
		HighTarget:    cosmos.NewUint(15 * common.One),
		LowTarget:     cosmos.NewUint(10 * common.One),
		ExpiryHeight:  currentHeight + 2000,
		Status:        IntentAuctioning,
	}
	w.keeper.SetIntent(w.ctx, intent)

	// Create an auction in overtime window (currentHeight + 99).
	auctionExpiry := currentHeight + 99
	auction := IntentAuction{
		IntentTxID:      intentTx.ID,
		AuctionAmount:   common.NewCoin(common.DOGEAsset, cosmos.NewUint(10*common.One)),
		RemainingAmount: common.NewCoin(common.DOGEAsset, cosmos.NewUint(90*common.One)),
		BidAsset:        common.DOGEAsset.GetChain().GetGasAsset(),
		MinBid:          cosmos.NewUint(1000),
		StartHeight:     currentHeight - 100,
		ExpiryHeight:    auctionExpiry,
		Status:          IntentAuctionStatus_AuctionActive,
	}
	w.keeper.SetIntentAuction(w.ctx, auction)

	// Place first bid from bidder A to establish a highest bid
	mgr := NewDummyMgrWithKeeper(w.keeper)
	handler := NewIntentAuctionBidHandler(mgr)
	bidTx1 := GetRandomTx()
	bidTx1.Coins = common.Coins{common.NewCoin(auction.BidAsset, cosmos.NewUint(5000))}
	msg1 := NewMsgIntentAuctionBid(bidTx1, intentTx.ID, w.activeNodeAccount.NodeAddress)
	_, err := handler.Run(w.ctx, msg1)
	c.Assert(err, IsNil)

	// Now try to place a second bid from a different bidder with network fee
	// lookup failing. This tests that calculateGasFee errors are properly propagated.
	testKeeper := NewHandlerIntentAuctionBidTestHelper(w.keeper)
	testKeeper.failToGetNetworkFee = true
	handler2 := NewIntentAuctionBidHandler(NewDummyMgrWithKeeper(testKeeper))

	bidTx2 := GetRandomTx()
	bidTx2.FromAddress = GetRandomDOGEAddress()
	bidTx2.Coins = common.Coins{common.NewCoin(auction.BidAsset, cosmos.NewUint(100000))}
	msg2 := NewMsgIntentAuctionBid(bidTx2, intentTx.ID, w.activeNodeAccount.NodeAddress)
	result, err := handler2.Run(w.ctx, msg2)
	c.Check(err, NotNil)
	c.Check(errors.Is(err, errInternal), Equals, true, Commentf("Network fee failure should return internal error"))
	c.Check(result, IsNil)
}

func (HandlerIntentAuctionBidSuite) TestCalculateSolveBeatIncrement_GasAssetTarget(c *C) {
	w := getHandlerTestWrapper(c, 1, true, true)
	c.Assert(w.keeper.SaveNetworkFee(w.ctx, common.ETHChain, NewNetworkFee(common.ETHChain, 1, 375000)), IsNil)

	increment, incrementAsset, err := calculateSolveBeatIncrement(w.ctx, NewDummyMgrWithKeeper(w.keeper), common.ETHAsset)
	c.Assert(err, IsNil)
	c.Assert(increment.Equal(cosmos.NewUint(375000)), Equals, true)
	c.Assert(incrementAsset.Equals(common.ETHAsset), Equals, true)
}

func (HandlerIntentAuctionBidSuite) TestCalculateSolveBeatIncrement_RuneTarget(c *C) {
	w := getHandlerTestWrapper(c, 1, true, true)
	c.Assert(w.keeper.SaveNetworkFee(w.ctx, common.THORChain, NewNetworkFee(common.THORChain, 1, 123)), IsNil)

	increment, incrementAsset, err := calculateSolveBeatIncrement(w.ctx, NewDummyMgrWithKeeper(w.keeper), common.RuneNative)
	c.Assert(err, IsNil)
	c.Assert(increment.Equal(cosmos.NewUint(123)), Equals, true)
	c.Assert(incrementAsset.Equals(common.RuneNative), Equals, true)
}

func (HandlerIntentAuctionBidSuite) TestCalculateSolveBeatIncrement_ExternalTokenTargetUsesPoolConversion(c *C) {
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

	c.Assert(w.keeper.SaveNetworkFee(w.ctx, common.ETHChain, NewNetworkFee(common.ETHChain, 1, 375000)), IsNil)

	increment, incrementAsset, err := calculateSolveBeatIncrement(w.ctx, NewDummyMgrWithKeeper(w.keeper), targetAsset)
	c.Assert(err, IsNil)
	c.Assert(increment.Equal(cosmos.NewUint(375000)), Equals, true)
	c.Assert(incrementAsset.Equals(targetAsset), Equals, true)
}

func (HandlerIntentAuctionBidSuite) TestCalculateSolveBeatIncrement_THORNativeTargetUsesRuneGas(c *C) {
	w := getHandlerTestWrapper(c, 1, true, true)
	targetAsset := common.BTCAsset.GetSecuredAsset()

	targetPool := NewPool()
	targetPool.Asset = common.BTCAsset
	targetPool.Status = PoolAvailable
	targetPool.BalanceAsset = cosmos.NewUint(1_000_000_000)
	targetPool.BalanceRune = cosmos.NewUint(1_000_000_000)
	c.Assert(w.keeper.SetPool(w.ctx, targetPool), IsNil)

	c.Assert(w.keeper.SaveNetworkFee(w.ctx, common.THORChain, NewNetworkFee(common.THORChain, 1, 375000)), IsNil)

	increment, incrementAsset, err := calculateSolveBeatIncrement(w.ctx, NewDummyMgrWithKeeper(w.keeper), targetAsset)
	c.Assert(err, IsNil)
	c.Assert(increment.Equal(cosmos.NewUint(375000)), Equals, true)
	c.Assert(incrementAsset.Equals(targetAsset), Equals, true)
}

func (HandlerIntentAuctionBidSuite) TestCalculateSolveEqualityTolerance_PricingUnavailableFallsBackToExactComparison(c *C) {
	w := getHandlerTestWrapper(c, 1, true, true)
	c.Assert(w.keeper.SaveNetworkFee(w.ctx, common.THORChain, NewNetworkFee(common.THORChain, 1, 375000)), IsNil)
	targetAsset := common.BTCAsset.GetSecuredAsset()

	increment, incrementAsset, err := calculateSolveBeatIncrement(w.ctx, NewDummyMgrWithKeeper(w.keeper), targetAsset)
	c.Assert(err, IsNil)
	c.Assert(increment.Equal(cosmos.OneUint()), Equals, true)
	c.Assert(incrementAsset.Equals(targetAsset), Equals, true)

	tolerance, err := calculateSolveEqualityTolerance(w.ctx, NewDummyMgrWithKeeper(w.keeper), targetAsset)
	c.Assert(err, IsNil)
	c.Assert(tolerance.IsZero(), Equals, true)
}
