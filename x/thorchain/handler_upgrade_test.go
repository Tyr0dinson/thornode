package thorchain

import (
	"bytes"
	"errors"
	"fmt"
	math "math"
	"math/big"
	"strconv"

	"github.com/cosmos/cosmos-sdk/codec"
	se "github.com/cosmos/cosmos-sdk/types/errors"
	upgradetypes "github.com/cosmos/cosmos-sdk/x/upgrade/types"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/common/cosmos"
	"gitlab.com/thorchain/thornode/x/thorchain/keeper"
	keeperv1 "gitlab.com/thorchain/thornode/x/thorchain/keeper/v1"
	"gitlab.com/thorchain/thornode/x/thorchain/types"
)

type HandlerUpgradeSuite struct{}

type TestUpgradeKeeper struct {
	keeper.KVStoreDummy
	activeAccounts   []NodeAccount
	failNodeAccount  NodeAccount
	emptyNodeAccount NodeAccount
	vaultNodeAccount NodeAccount
	proposedUpgrade  *types.Upgrade
	votes            map[string]bool

	scheduled bool
}

func (k *TestUpgradeKeeper) GetNodeAccount(_ cosmos.Context, addr cosmos.AccAddress) (NodeAccount, error) {
	if k.failNodeAccount.NodeAddress.Equals(addr) {
		return NodeAccount{}, errKaboom
	}
	if k.emptyNodeAccount.NodeAddress.Equals(addr) {
		return NodeAccount{}, nil
	}
	if k.vaultNodeAccount.NodeAddress.Equals(addr) {
		return NodeAccount{Type: NodeTypeVault}, nil
	}

	for _, na := range k.activeAccounts {
		if na.NodeAddress.Equals(addr) {
			return na, nil
		}
	}

	return NodeAccount{}, errKaboom
}

func (k *TestUpgradeKeeper) ProposeUpgrade(_ cosmos.Context, name string, upgrade types.Upgrade) error {
	k.proposedUpgrade = &upgrade
	return nil
}

func (k *TestUpgradeKeeper) GetProposedUpgrade(_ cosmos.Context, name string) (types.Upgrade, error) {
	if k.proposedUpgrade == nil {
		return types.Upgrade{}, fmt.Errorf("proposed upgrade not found for %s", name)
	}
	return *k.proposedUpgrade, nil
}

func (k *TestUpgradeKeeper) ApproveUpgrade(_ cosmos.Context, addr cosmos.AccAddress, name string) error {
	k.votes[addr.String()] = true
	return nil
}

func (k *TestUpgradeKeeper) RejectUpgrade(_ cosmos.Context, addr cosmos.AccAddress, name string) error {
	k.votes[addr.String()] = false
	return nil
}

func (k *TestUpgradeKeeper) GetNodeAccountIterator(_ cosmos.Context) cosmos.Iterator {
	nas := make([]NodeAccount, 0, len(k.activeAccounts)+2)
	nas = append(nas, k.activeAccounts...)
	nas = append(nas, k.vaultNodeAccount, k.emptyNodeAccount)
	return newMockNodeAccountIterator(k.Cdc(), nas)
}

func (k *TestUpgradeKeeper) GetUpgradeVoteIterator(_ cosmos.Context, name string) cosmos.Iterator {
	votes := make([]mockVote, 0, len(k.votes))
	for addr, approve := range k.votes {
		acc, err := cosmos.AccAddressFromBech32(addr)
		if err != nil {
			panic(err)
		}
		votes = append(votes, mockVote{acc: acc, approve: approve})
	}
	return newMockUpgradeVoteIterator(votes)
}

func (k *TestUpgradeKeeper) ScheduleUpgrade(_ cosmos.Context, plan upgradetypes.Plan) error {
	k.scheduled = true
	return nil
}

func (k *TestUpgradeKeeper) ClearUpgradePlan(_ cosmos.Context) {
	k.scheduled = false
}

func (k *TestUpgradeKeeper) GetUpgradePlan(_ cosmos.Context) (upgradetypes.Plan, bool) {
	if k.scheduled {
		return upgradetypes.Plan{Name: "berserk"}, true
	}
	return upgradetypes.Plan{}, false
}

// UpgradeApprovedByMajority returns true and no error if the upgrade is approved by 2/3 of Validators.
// it additionally returns the current approval percentage and the bond needed to reach quorum, if not approved.
// TODO: this is currently copy/paste of the KVStore implementation.
func (k *TestUpgradeKeeper) UpgradeApprovedByMajority(ctx cosmos.Context, name string) (bool, string, uint64, error) {
	iterA := k.GetNodeAccountIterator(ctx)
	defer iterA.Close()

	active := make(map[string]bool)

	for ; iterA.Valid(); iterA.Next() {
		var na NodeAccount
		if err := k.Cdc().Unmarshal(iterA.Value(), &na); err != nil {
			return false, "", 0, fmt.Errorf("failed to unmarshal: %w", err)
		}

		if na.IsEmpty() || na.Status != NodeActive || na.Type != NodeTypeValidator {
			continue
		}

		active[na.NodeAddress.String()] = true
	}

	iterV := k.GetUpgradeVoteIterator(ctx, name)
	defer iterV.Close()

	var approvingVals int64

	for ; iterV.Valid(); iterV.Next() {
		key, vote := iterV.Key(), iterV.Value()
		if !bytes.Equal(vote, []byte{0x1}) {
			continue
		}

		prefix := []byte(keeperv1.VotePrefix(name))
		addr := cosmos.AccAddress(bytes.TrimPrefix(key, prefix))

		_, ok := active[addr.String()]
		if !ok {
			// this could happen if a validator votes and then becomes inactive
			continue
		}

		approvingVals++
	}

	valThreshold := big.NewRat(int64(len(active))*2, 3)

	approval := big.NewRat(int64(approvingVals), int64(len(active)))
	approvalFlt, _ := approval.Float64()
	approvalStr := strconv.FormatFloat(approvalFlt, 'f', -1, 64)

	t, _ := valThreshold.Float64()

	if float64(approvingVals) >= t {
		return true, approvalStr, 0, nil
	}

	neededForQuorum := new(big.Rat).Sub(valThreshold, big.NewRat(int64(approvingVals), 1))
	nfq, _ := neededForQuorum.Float64()
	valsToQuorum := math.Ceil(nfq)

	return false, approvalStr, uint64(valsToQuorum), nil
}

var _ = Suite(&HandlerUpgradeSuite{})

func (s *HandlerUpgradeSuite) TestUpgrade(c *C) {
	ctx, _ := setupKeeperForTest(c)

	const (
		upgradeName = "berserk"
		upgradeInfo = "scheduled upgrade"
	)

	upgradeHeight := ctx.BlockHeight() + 100

	keeper := &TestUpgradeKeeper{
		failNodeAccount:  GetRandomValidatorNode(NodeActive),
		emptyNodeAccount: GetRandomValidatorNode(NodeStandby),
		vaultNodeAccount: GetRandomVaultNode(NodeActive),
		votes:            make(map[string]bool),
	}

	// add some active accounts
	for i := 0; i < 10; i++ {
		keeper.activeAccounts = append(keeper.activeAccounts, GetRandomValidatorNode(NodeActive))
	}

	mgr := NewDummyMgrWithKeeper(keeper)

	handler := NewProposeUpgradeHandler(mgr)

	// invalid height
	msg := NewMsgProposeUpgrade(upgradeName, ctx.BlockHeight(), upgradeInfo, keeper.activeAccounts[0].NodeAddress)
	result, err := handler.Run(ctx, msg)
	c.Assert(result, IsNil)
	c.Assert(err, NotNil)

	// invalid msg
	msg = &MsgProposeUpgrade{}
	result, err = handler.Run(ctx, msg)
	c.Assert(result, IsNil)
	c.Assert(err, NotNil)

	// fail to get node account should fail
	msg1 := NewMsgProposeUpgrade(upgradeName, upgradeHeight, upgradeInfo, keeper.failNodeAccount.NodeAddress)
	result, err = handler.Run(ctx, msg1)
	c.Assert(err, NotNil)
	c.Assert(result, IsNil)

	// node account empty should fail
	msg2 := NewMsgProposeUpgrade(upgradeName, upgradeHeight, upgradeInfo, keeper.emptyNodeAccount.NodeAddress)
	result, err = handler.Run(ctx, msg2)
	c.Assert(err, NotNil)
	c.Assert(result, IsNil)
	c.Assert(errors.Is(err, se.ErrUnauthorized), Equals, true)

	// vault node should fail
	msg3 := NewMsgProposeUpgrade(upgradeName, upgradeHeight, upgradeInfo, keeper.vaultNodeAccount.NodeAddress)
	result, err = handler.Run(ctx, msg3)
	c.Assert(err, NotNil)
	c.Assert(result, IsNil)
	c.Assert(errors.Is(err, se.ErrUnauthorized), Equals, true)

	// happy path to get the upgrade proposed
	msg4 := NewMsgProposeUpgrade(upgradeName, upgradeHeight, upgradeInfo, keeper.activeAccounts[0].NodeAddress)
	result, err = handler.Run(ctx, msg4)
	c.Assert(err, IsNil)
	c.Assert(result, NotNil)
	c.Assert(keeper.scheduled, Equals, false)

	approveHandler := NewApproveUpgradeHandler(mgr)

	// vote for upgrade by 1 less than 2/3 of active accounts
	for i, na := range keeper.activeAccounts {
		if i == 0 {
			// skip the proposer because they already approved by proposing
			continue
		}
		if i == 6 {
			// break after 6 approve votes so that we are 1 less than 2/3
			break
		}
		msg := NewMsgApproveUpgrade(upgradeName, na.NodeAddress)
		result, err = approveHandler.Run(ctx, msg)
		c.Assert(err, IsNil)
		c.Assert(result, NotNil)
	}

	// upgrade should still not be scheduled
	c.Assert(keeper.scheduled, Equals, false)

	// vote for upgrade by one more active account
	msg5 := NewMsgApproveUpgrade(upgradeName, keeper.activeAccounts[8].NodeAddress)
	result, err = approveHandler.Run(ctx, msg5)
	c.Assert(err, IsNil)
	c.Assert(result, NotNil)

	// upgrade should now be scheduled
	c.Assert(keeper.scheduled, Equals, true)

	rejectHandler := NewRejectUpgradeHandler(mgr)

	// reject upgrade by one of the active accounts to drop below 2/3
	msg6 := NewMsgRejectUpgrade(upgradeName, keeper.activeAccounts[4].NodeAddress)
	result, err = rejectHandler.Run(ctx, msg6)
	c.Assert(err, IsNil)
	c.Assert(result, NotNil)

	// upgrade should now be cleared
	c.Assert(keeper.scheduled, Equals, false)
}

type mockNodeAccountIterator struct {
	cdc          codec.BinaryCodec
	nodeAccounts []NodeAccount
	i            int
}

func newMockNodeAccountIterator(cdc codec.BinaryCodec, nodeAccounts []NodeAccount) *mockNodeAccountIterator {
	return &mockNodeAccountIterator{cdc: cdc, nodeAccounts: nodeAccounts}
}

func (it *mockNodeAccountIterator) Domain() (start []byte, end []byte) { return nil, nil }
func (it *mockNodeAccountIterator) Valid() bool                        { return it.i < len(it.nodeAccounts) }
func (it *mockNodeAccountIterator) Next()                              { it.i++ }
func (it *mockNodeAccountIterator) Key() (key []byte)                  { return nil }
func (it *mockNodeAccountIterator) Value() (value []byte) {
	bz, err := it.cdc.Marshal(&it.nodeAccounts[it.i])
	if err != nil {
		panic(fmt.Errorf("failed to marshal: %w", err))
	}
	return bz
}
func (it *mockNodeAccountIterator) Error() error { return nil }
func (it *mockNodeAccountIterator) Close() error { return nil }

type mockUpgradeVoteIterator struct {
	upgradeVotes []mockVote
	i            int
}

type mockVote struct {
	acc     cosmos.AccAddress
	approve bool
}

func newMockUpgradeVoteIterator(upgradeVotes []mockVote) *mockUpgradeVoteIterator {
	return &mockUpgradeVoteIterator{upgradeVotes: upgradeVotes}
}

func (it *mockUpgradeVoteIterator) Domain() (start []byte, end []byte) { return nil, nil }
func (it *mockUpgradeVoteIterator) Valid() bool                        { return it.i < len(it.upgradeVotes) }
func (it *mockUpgradeVoteIterator) Next()                              { it.i++ }
func (it *mockUpgradeVoteIterator) Key() (key []byte) {
	return it.upgradeVotes[it.i].acc.Bytes()
}

func (it *mockUpgradeVoteIterator) Value() (value []byte) {
	if it.upgradeVotes[it.i].approve {
		return []byte{0x01}
	}
	return []byte{0x00}
}

func (it *mockUpgradeVoteIterator) Error() error { return nil }
func (it *mockUpgradeVoteIterator) Close() error { return nil }
