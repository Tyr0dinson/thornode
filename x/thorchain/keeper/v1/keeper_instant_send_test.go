package keeperv1

import (
	"bytes"
	"strings"

	"cosmossdk.io/log"
	"cosmossdk.io/store"
	storemetrics "cosmossdk.io/store/metrics"
	dbm "github.com/cosmos/cosmos-db"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	ttypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

type InstantSendKeeperSuite struct{}

var _ = Suite(&InstantSendKeeperSuite{})

func (s *InstantSendKeeperSuite) TestPersistence(c *C) {
	ctx, k := setupKeeperForTest(c)
	db := dbm.NewMemDB()
	key := cosmos.NewKVStoreKey(StoreKey)
	ms := store.NewCommitMultiStore(db, log.NewNopLogger(), storemetrics.NewNoOpMetrics())
	ms.MountStoreWithDB(key, cosmos.StoreTypeIAVL, nil)
	c.Assert(ms.LoadLatestVersion(), IsNil)
	ctx = ctx.WithMultiStore(ms)
	k.storeKey = key
	pubKey := ttypes.GetRandomPubKey()
	state := common.InstantSendState{
		Chain: common.DASHChain, PubKey: pubKey,
		Pending: []common.PendingInbound{{Id: common.TxID(strings.Repeat("A", 64)), Amount: 10, Height: 1}},
	}
	c.Assert(k.SetInstantSend(ctx, state), IsNil)
	ms.Commit()
	loaded := store.NewCommitMultiStore(db, log.NewNopLogger(), storemetrics.NewNoOpMetrics())
	loaded.MountStoreWithDB(key, cosmos.StoreTypeIAVL, nil)
	c.Assert(loaded.LoadLatestVersion(), IsNil)
	ctx = ctx.WithMultiStore(loaded)
	fresh := NewKVStore(k.cdc, k.storeKey, k.coinKeeper, k.accountKeeper, k.upgradeKeeper, k.version)
	actual, err := fresh.GetInstantSend(ctx, common.DASHChain, pubKey)
	c.Assert(err, IsNil)
	c.Assert(actual, DeepEquals, state)
	list, err := fresh.ListInstantSend(ctx, common.DASHChain)
	c.Assert(err, IsNil)
	c.Assert(list, HasLen, 1)
	actual.Pending[0].Amount = 0
	c.Assert(fresh.SetInstantSend(ctx, actual), NotNil)
	actual, err = fresh.GetInstantSend(ctx, common.DASHChain, pubKey)
	c.Assert(err, IsNil)
	c.Assert(actual.Pending[0].Amount, Equals, uint64(10))
}

func (s *InstantSendKeeperSuite) TestUnsupportedChainAndDeletion(c *C) {
	ctx, k := setupKeeperForTest(c)
	key := ttypes.GetRandomPubKey()
	state := common.InstantSendState{Chain: common.DASHChain, PubKey: key, Pending: []common.PendingInbound{{Id: ttypes.GetRandomTxHash(), Amount: 10, Height: 1}}}
	c.Assert(k.SetInstantSend(ctx, state), IsNil)
	other := state.Copy()
	other.Chain = common.Chain("BASH")
	c.Assert(k.SetInstantSend(ctx, other), ErrorMatches, "unsupported InstantSend chain")
	state.Pending = nil
	c.Assert(k.SetInstantSend(ctx, state), IsNil)
	loaded, err := k.GetInstantSend(ctx, state.Chain, key)
	c.Assert(err, IsNil)
	c.Assert(loaded, DeepEquals, state)
	all, err := k.ListInstantSend(ctx, common.EmptyChain)
	c.Assert(err, IsNil)
	c.Assert(all, HasLen, 0)
}

func (s *InstantSendKeeperSuite) TestUnchangedStatusDoesNotWrite(c *C) {
	ctx, k := setupKeeperForTest(c)
	var trace bytes.Buffer
	ctx.MultiStore().SetTracer(&trace)
	defer ctx.MultiStore().SetTracer(nil)
	status := common.InstantSendStatus{Chain: common.DASHChain}
	c.Assert(k.SetInstantSendStatus(ctx, status), IsNil)
	c.Assert(strings.Contains(trace.String(), `"operation":"write"`), Equals, false)
	values, err := k.ListInstantSendStatus(ctx)
	c.Assert(err, IsNil)
	c.Assert(values, HasLen, 0)
	status.MinHeight = 10
	trace.Reset()
	c.Assert(k.SetInstantSendStatus(ctx, status), IsNil)
	c.Assert(strings.Contains(trace.String(), `"operation":"write"`), Equals, true)
	trace.Reset()
	c.Assert(k.SetInstantSendStatus(ctx, status), IsNil)
	c.Assert(strings.Contains(trace.String(), `"operation":"write"`), Equals, false)
}

func (s *InstantSendKeeperSuite) TestInputPruningKeepsWholeCredits(c *C) {
	ctx, k := setupKeeperForTest(c)
	credits := make([]common.InstantSendCredit, 2)
	for i := range credits {
		credits[i] = common.InstantSendCredit{Id: ttypes.GetRandomTxHash(), Height: int64(100 + i)}
		hash := ttypes.GetRandomTxHash().String()
		for n := 0; n < common.InstantSendMaxInputs; n++ {
			credits[i].Inputs = append(credits[i].Inputs, common.OutputRef{TxHash: hash, OutputIndex: uint32(n)})
		}
		c.Assert(k.SetInstantSendCredit(ctx, credits[i]), IsNil)
	}
	c.Assert(k.PruneInstantSendCredits(ctx, 100, 5000), IsNil)
	all, err := k.ListInstantSendCredit(ctx)
	c.Assert(err, IsNil)
	c.Assert(all, HasLen, 2)
	c.Assert(k.PruneInstantSendCredits(ctx, 102, 5000), IsNil)
	all, err = k.ListInstantSendCredit(ctx)
	c.Assert(err, IsNil)
	c.Assert(all, HasLen, 1)
	c.Assert(all[0].Id, Equals, credits[1].Id)
	for i, credit := range credits {
		for _, input := range credit.Inputs {
			owner, readErr := k.GetInstantSendInput(ctx, input)
			c.Assert(readErr, IsNil)
			c.Assert(owner.Id.IsEmpty(), Equals, i == 0)
		}
	}
	c.Assert(k.PruneInstantSendCredits(ctx, 102, 5000), IsNil)
	all, err = k.ListInstantSendCredit(ctx)
	c.Assert(err, IsNil)
	c.Assert(all, HasLen, 0)
}

func (s *InstantSendKeeperSuite) TestInputPruningRetainsRecordsOnError(c *C) {
	ctx, k := setupKeeperForTest(c)
	credit := common.InstantSendCredit{
		Id: ttypes.GetRandomTxHash(), Height: 100,
		Inputs: []common.OutputRef{{TxHash: ttypes.GetRandomTxHash().String()}},
	}
	c.Assert(k.SetInstantSendCredit(ctx, credit), IsNil)
	missing := common.InstantSendCredit{Id: ttypes.GetRandomTxHash(), Height: 101}
	key := k.GetKey(prefixInstantSendExpiry, instantSendExpiryKey(missing))
	k.kvStore(ctx).Set(key, []byte(missing.Id))
	c.Assert(k.PruneInstantSendCredits(ctx, 102, 5000), ErrorMatches, "invalid InstantSend expiry record")
	owner, err := k.GetInstantSendInput(ctx, credit.Inputs[0])
	c.Assert(err, IsNil)
	c.Assert(owner.Id, Equals, credit.Id) // No partial deletion before a later read failure.
	k.kvStore(ctx).Delete(key)
	c.Assert(k.PruneInstantSendCredits(ctx, 102, 5000), IsNil)
	owner, err = k.GetInstantSendInput(ctx, credit.Inputs[0])
	c.Assert(err, IsNil)
	c.Assert(owner.Id.IsEmpty(), Equals, true)
}
