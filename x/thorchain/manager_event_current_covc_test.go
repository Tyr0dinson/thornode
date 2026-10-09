package thorchain

import (
	"errors"

	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

type EventMgrCovCBSuite struct{}

var _ = Suite(&EventMgrCovCBSuite{})

// covCBFailingEvent is an EmitEventItem whose Events() always fails.
type covCBFailingEvent struct{}

func (covCBFailingEvent) Events() (cosmos.Events, error) {
	return nil, errors.New("covCB events failure")
}

func (s *EventMgrCovCBSuite) TestEmitEventErrorCovCB(c *C) {
	ctx, _ := setupKeeperForTest(c)
	eventMgr := newEventMgr()
	before := len(ctx.EventManager().Events())
	err := eventMgr.EmitEvent(ctx, covCBFailingEvent{})
	c.Assert(err, NotNil)
	c.Assert(err.Error(), Matches, "fail to get events: covCB events failure")
	c.Assert(len(ctx.EventManager().Events()), Equals, before)
}

func (s *EventMgrCovCBSuite) TestEmitSwapEventWithOutTxsCovCB(c *C) {
	ctx, _ := setupKeeperForTest(c)
	ctx = ctx.WithEventManager(cosmos.NewEventManager())
	eventMgr := newEventMgr()

	inTx := GetRandomTx()
	swap := NewEventSwap(common.BTCAsset, cosmos.ZeroUint(), cosmos.ZeroUint(), cosmos.ZeroUint(), cosmos.ZeroUint(),
		inTx, common.NewCoin(common.RuneAsset(), cosmos.NewUint(100)), cosmos.ZeroUint())
	outTx := GetRandomTx()
	swap.OutTxs = outTx

	c.Assert(eventMgr.EmitSwapEvent(ctx, swap), IsNil)
	events := ctx.EventManager().Events()
	c.Assert(len(events), Equals, 2)
	c.Assert(events[0].Type, Equals, "outbound")
	c.Assert(events[1].Type, Equals, "swap")
	found := false
	for _, attr := range events[0].Attributes {
		if attr.Key == "in_tx_id" {
			c.Assert(attr.Value, Equals, inTx.ID.String())
			found = true
		}
	}
	c.Assert(found, Equals, true)

	// without OutTxs only the swap event is emitted
	ctx = ctx.WithEventManager(cosmos.NewEventManager())
	swap.OutTxs = common.Tx{}
	c.Assert(eventMgr.EmitSwapEvent(ctx, swap), IsNil)
	events = ctx.EventManager().Events()
	c.Assert(len(events), Equals, 1)
	c.Assert(events[0].Type, Equals, "swap")
}
