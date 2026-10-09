package thorchain

import (
	abci "github.com/cometbft/cometbft/abci/types"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	"github.com/cosmos/cosmos-sdk/baseapp"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
)

type SlasherCovCBSuite struct{}

var _ = Suite(&SlasherCovCBSuite{})

func (s *SlasherCovCBSuite) SetUpSuite(_ *C) {
	SetupConfigForTest()
}

func covCBValAddr(c *C, na NodeAccount) []byte {
	pk, err := cosmos.GetPubKeyFromBech32(cosmos.Bech32PubKeyTypeConsPub, na.ValidatorConsPubKey)
	c.Assert(err, IsNil)
	return pk.Address()
}

func (s *SlasherCovCBSuite) TestBeginBlockCovCB(c *C) {
	ctx, k := setupKeeperForTest(c)
	ctx = ctx.WithBlockHeight(100)
	constAccessor := constants.GetConstantValues(GetCurrentVersion())

	// node that double signs and misses the last block
	na1 := GetRandomValidatorNode(NodeActive)
	na1.MissingBlocks = 3
	c.Assert(k.SetNodeAccount(ctx, na1), IsNil)
	// node that signs the last block
	na2 := GetRandomValidatorNode(NodeActive)
	na2.MissingBlocks = 2
	c.Assert(k.SetNodeAccount(ctx, na2), IsNil)
	// active node with an undecodable consensus pubkey is skipped
	na3 := GetRandomValidatorNode(NodeActive)
	na3.ValidatorConsPubKey = "bogus"
	c.Assert(k.SetNodeAccount(ctx, na3), IsNil)

	addr1 := covCBValAddr(c, na1)
	addr2 := covCBValAddr(c, na2)
	unknown := GetRandomValidatorNode(NodeActive)
	unknownAddr := covCBValAddr(c, unknown)

	misbehavior := []abci.Misbehavior{
		{
			Type:      abci.MisbehaviorType_DUPLICATE_VOTE,
			Validator: abci.Validator{Address: addr1, Power: 100},
			Height:    ctx.BlockHeight() - 1,
		},
		{
			// unknown evidence types are ignored
			Type:      abci.MisbehaviorType_LIGHT_CLIENT_ATTACK,
			Validator: abci.Validator{Address: addr2, Power: 100},
			Height:    ctx.BlockHeight() - 1,
		},
		{
			// double sign by an unknown validator only logs an error
			Type:      abci.MisbehaviorType_DUPLICATE_VOTE,
			Validator: abci.Validator{Address: unknownAddr, Power: 100},
			Height:    ctx.BlockHeight() - 1,
		},
	}
	lastCommit := abci.CommitInfo{
		Votes: []abci.VoteInfo{
			{Validator: abci.Validator{Address: addr1, Power: 100}, BlockIdFlag: cmtproto.BlockIDFlagAbsent},
			{Validator: abci.Validator{Address: addr2, Power: 100}, BlockIdFlag: cmtproto.BlockIDFlagCommit},
			{Validator: abci.Validator{Address: unknownAddr, Power: 100}, BlockIdFlag: cmtproto.BlockIDFlagCommit},
			{Validator: abci.Validator{Address: unknownAddr, Power: 100}, BlockIdFlag: cmtproto.BlockIDFlagAbsent},
		},
	}
	ctx = ctx.WithCometInfo(baseapp.NewBlockInfo(misbehavior, nil, nil, lastCommit))

	slasher := newSlasher(k, NewDummyEventMgr())
	slasher.BeginBlock(ctx, constAccessor)

	doubleSignPts := k.GetConfigInt64(ctx, constants.DoubleBlockSignSlashPoints)
	missPts := k.GetConfigInt64(ctx, constants.MissBlockSignSlashPoints)

	pts1, err := k.GetNodeAccountSlashPoints(ctx, na1.NodeAddress)
	c.Assert(err, IsNil)
	c.Assert(pts1, Equals, doubleSignPts+missPts)
	got1, err := k.GetNodeAccount(ctx, na1.NodeAddress)
	c.Assert(err, IsNil)
	c.Assert(got1.MissingBlocks, Equals, uint64(4))

	pts2, err := k.GetNodeAccountSlashPoints(ctx, na2.NodeAddress)
	c.Assert(err, IsNil)
	c.Assert(pts2, Equals, int64(0))
	got2, err := k.GetNodeAccount(ctx, na2.NodeAddress)
	c.Assert(err, IsNil)
	c.Assert(got2.MissingBlocks, Equals, uint64(1))

	pts3, err := k.GetNodeAccountSlashPoints(ctx, na3.NodeAddress)
	c.Assert(err, IsNil)
	c.Assert(pts3, Equals, int64(0))
}

func (s *SlasherCovCBSuite) TestHandleDoubleSignBranchesCovCB(c *C) {
	ctx, k := setupKeeperForTest(c)
	ctx = ctx.WithBlockHeight(1000)
	constAccessor := constants.GetConstantValues(GetCurrentVersion())
	slasher := newSlasher(k, NewDummyEventMgr())
	doubleSignPts := k.GetConfigInt64(ctx, constants.DoubleBlockSignSlashPoints)

	// a churned-out (standby) validator with bond is found through the
	// fallback search over all bonded validators
	na := GetRandomValidatorNode(NodeStandby)
	na.Bond = cosmos.NewUint(100 * common.One)
	c.Assert(k.SetNodeAccount(ctx, na), IsNil)
	addr := covCBValAddr(c, na)

	// too old: nothing slashed
	maxAge := constAccessor.GetInt64Value(constants.DoubleSignMaxAge)
	c.Assert(slasher.HandleDoubleSign(ctx, addr, ctx.BlockHeight()-maxAge-1, constAccessor, nil), IsNil)
	pts, err := k.GetNodeAccountSlashPoints(ctx, na.NodeAddress)
	c.Assert(err, IsNil)
	c.Assert(pts, Equals, int64(0))

	// recent: fallback lookup slashes the standby validator
	c.Assert(slasher.HandleDoubleSign(ctx, addr, ctx.BlockHeight()-1, constAccessor, nil), IsNil)
	pts, err = k.GetNodeAccountSlashPoints(ctx, na.NodeAddress)
	c.Assert(err, IsNil)
	c.Assert(pts, Equals, doubleSignPts)

	// a bonded validator with a bad consensus key is skipped in the fallback,
	// and an unknown address errors out
	bad := GetRandomValidatorNode(NodeStandby)
	bad.Bond = cosmos.NewUint(common.One)
	bad.ValidatorConsPubKey = "bogus"
	c.Assert(k.SetNodeAccount(ctx, bad), IsNil)
	unknown := GetRandomValidatorNode(NodeActive)
	err = slasher.HandleDoubleSign(ctx, covCBValAddr(c, unknown), ctx.BlockHeight(), constAccessor, nil)
	c.Assert(err, NotNil)
	c.Assert(err.Error(), Matches, "could not find node account with validator address.*")
}
