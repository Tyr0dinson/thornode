package thorchain

import (
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	. "gopkg.in/check.v1"
)

type HandlerOperatorRotateSuite struct{}

var _ = Suite(&HandlerOperatorRotateSuite{})

func (s *HandlerOperatorRotateSuite) SetUpSuite(c *C) {
	SetupConfigForTest()
}

// rotateMsg builds a zero-coin MsgOperatorRotate from signer to operator.
func rotateMsg(signer, operator cosmos.AccAddress) *MsgOperatorRotate {
	return NewMsgOperatorRotate(signer, operator, common.NewCoin(common.EmptyAsset, cosmos.ZeroUint()))
}

// setLivePendingKeygen schedules an unresolved Asgard keygen at the current
// height so hasPendingKeygen reports a live pending keygen.
func setLivePendingKeygen(c *C, ctx cosmos.Context, mgr *Mgrs) {
	members := common.PubKeys{GetRandomPubKey(), GetRandomPubKey(), GetRandomPubKey()}
	keygen, err := NewKeygen(ctx.BlockHeight(), members.Strings(), AsgardKeygen)
	c.Assert(err, IsNil)
	kb := NewKeygenBlock(ctx.BlockHeight())
	kb.Keygens = []Keygen{keygen}
	mgr.Keeper().SetKeygenBlock(ctx, kb)
}

func (s *HandlerOperatorRotateSuite) TestOperatorRotateHandler_InvalidMessage(c *C) {
	ctx, mgr := setupManagerForTest(c)
	h := NewOperatorRotateHandler(mgr)

	result, err := h.Run(ctx, &MsgMimir{})
	c.Assert(err, NotNil)
	c.Assert(result, IsNil)
	c.Assert(err, Equals, errInvalidMessage)
}

func (s *HandlerOperatorRotateSuite) TestOperatorRotateHandler_ValidateBasicFail(c *C) {
	ctx, mgr := setupManagerForTest(c)
	h := NewOperatorRotateHandler(mgr)

	// Empty signer should fail ValidateBasic
	msg := NewMsgOperatorRotate(cosmos.AccAddress{}, GetRandomBech32Addr(), common.NewCoin(common.EmptyAsset, cosmos.ZeroUint()))
	_, err := h.Run(ctx, msg)
	c.Assert(err, NotNil)
}

func (s *HandlerOperatorRotateSuite) TestOperatorRotateHandler_HaltOperatorRotate(c *C) {
	ctx, mgr := setupManagerForTest(c)
	h := NewOperatorRotateHandler(mgr)

	signer := GetRandomBech32Addr()
	operator := GetRandomBech32Addr()
	msg := NewMsgOperatorRotate(signer, operator, common.NewCoin(common.EmptyAsset, cosmos.ZeroUint()))

	// Set halt mimir
	mgr.Keeper().SetMimir(ctx, constants.HaltOperatorRotate.String(), 1)

	_, err := h.Run(ctx, msg)
	c.Assert(err, NotNil)
	c.Assert(err.Error(), Equals, "rotate is halted")
}

func (s *HandlerOperatorRotateSuite) TestOperatorRotateHandler_PastChurnCutoff(c *C) {
	ctx, mgr := setupManagerForTest(c)
	h := NewOperatorRotateHandler(mgr)

	signer := GetRandomBech32Addr()
	operator := GetRandomBech32Addr()
	msg := NewMsgOperatorRotate(signer, operator, common.NewCoin(common.EmptyAsset, cosmos.ZeroUint()))

	// Create an active vault with StatusSince = 1. Default ChurnInterval on mocknet is 60.
	// halfChurn = 30, rotateCutoffHeight = 1 + 30 = 31.
	// Default ctx block height is 18, so it should still be within the first half.
	// Set block height past the cutoff.
	vault := GetRandomVault()
	vault.Status = ActiveVault
	vault.StatusSince = 1
	c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)

	// Set ChurnInterval to 20 via mimir so halfChurn = 10, cutoff = 1 + 10 = 11.
	// Block height 18 > 11 (second half of the cycle) and churning is not halted,
	// so rotate should be refused by the near-churn guard.
	mgr.Keeper().SetMimir(ctx, constants.ChurnInterval.String(), 20)

	_, err := h.Run(ctx, msg)
	c.Assert(err, NotNil)
	c.Assert(err.Error(), Equals, "rotate not allowed in the second half of the churn cycle")
}

// While churning is halted, the second-half near-churn guard is skipped: an
// operator can rotate even past the cycle midpoint, because no churn or keygen
// can occur. This is the case the feature was requested for.
func (s *HandlerOperatorRotateSuite) TestOperatorRotateHandler_ChurningHaltedAllowsSecondHalf(c *C) {
	ctx, mgr := setupManagerForTest(c)
	h := NewOperatorRotateHandler(mgr)

	signer := GetRandomBech32Addr()
	operator := GetRandomBech32Addr()

	na := GetRandomValidatorNode(NodeActive)
	na.BondAddress = common.Address(signer.String())
	c.Assert(mgr.Keeper().SetNodeAccount(ctx, na), IsNil)

	vault := GetRandomVault()
	vault.Status = ActiveVault
	vault.StatusSince = 1
	c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)

	// Second half of the cycle (cutoff 1 + 10 = 11, height 18 > 11) ...
	mgr.Keeper().SetMimir(ctx, constants.ChurnInterval.String(), 20)
	// ... but churning is halted, so rotation is allowed.
	mgr.Keeper().SetMimir(ctx, "HaltChurning", 1)

	result, err := h.Run(ctx, rotateMsg(signer, operator))
	c.Assert(err, IsNil)
	c.Assert(result, NotNil)
	updated, err := mgr.Keeper().GetNodeAccount(ctx, na.NodeAddress)
	c.Assert(err, IsNil)
	c.Assert(updated.BondAddress.Equals(common.Address(operator.String())), Equals, true)
}

// While churning is halted, a leftover keygen artifact (an InitVault that will
// not be retired until the next keygen schedules) must NOT block rotation.
func (s *HandlerOperatorRotateSuite) TestOperatorRotateHandler_ChurningHaltedAllowsStaleInitVault(c *C) {
	ctx, mgr := setupManagerForTest(c)
	h := NewOperatorRotateHandler(mgr)

	signer := GetRandomBech32Addr()
	operator := GetRandomBech32Addr()

	na := GetRandomValidatorNode(NodeActive)
	na.BondAddress = common.Address(signer.String())
	c.Assert(mgr.Keeper().SetNodeAccount(ctx, na), IsNil)

	// A leftover InitVault that would otherwise block rotation ...
	initVault := GetRandomVault()
	initVault.Status = InitVault
	c.Assert(mgr.Keeper().SetVault(ctx, initVault), IsNil)
	// ... but churning is halted, so no keygen can fire and rotation is allowed.
	mgr.Keeper().SetMimir(ctx, "HaltChurning", 1)

	result, err := h.Run(ctx, rotateMsg(signer, operator))
	c.Assert(err, IsNil)
	c.Assert(result, NotNil)
	updated, err := mgr.Keeper().GetNodeAccount(ctx, na.NodeAddress)
	c.Assert(err, IsNil)
	c.Assert(updated.BondAddress.Equals(common.Address(operator.String())), Equals, true)
}

// Rotating to the current operator (OperatorAddress == Signer) is rejected with
// a clear message, not the misleading new-operator-active error.
func (s *HandlerOperatorRotateSuite) TestOperatorRotateHandler_SelfRotateRejected(c *C) {
	ctx, mgr := setupManagerForTest(c)
	h := NewOperatorRotateHandler(mgr)

	signer := GetRandomBech32Addr()
	na := GetRandomValidatorNode(NodeActive)
	na.BondAddress = common.Address(signer.String())
	c.Assert(mgr.Keeper().SetNodeAccount(ctx, na), IsNil)

	_, err := h.Run(ctx, rotateMsg(signer, signer))
	c.Assert(err, NotNil)
	c.Assert(err.Error(), Equals, "new operator is the current operator")
}

// A live pending keygen blocks rotation.
func (s *HandlerOperatorRotateSuite) TestOperatorRotateHandler_PendingKeygenBlocked(c *C) {
	ctx, mgr := setupManagerForTest(c)
	h := NewOperatorRotateHandler(mgr)

	signer := GetRandomBech32Addr()
	operator := GetRandomBech32Addr()

	na := GetRandomValidatorNode(NodeActive)
	na.BondAddress = common.Address(signer.String())
	c.Assert(mgr.Keeper().SetNodeAccount(ctx, na), IsNil)

	setLivePendingKeygen(c, ctx, mgr)

	_, err := h.Run(ctx, rotateMsg(signer, operator))
	c.Assert(err, NotNil)
	c.Assert(err.Error(), Equals, "rotate not allowed while a keygen is pending")
}

// A live pending keygen blocks rotation EVEN while churning is halted: a keygen
// scheduled before the halt can still finalize and place its vault.
func (s *HandlerOperatorRotateSuite) TestOperatorRotateHandler_HaltedPendingKeygenBlocked(c *C) {
	ctx, mgr := setupManagerForTest(c)
	h := NewOperatorRotateHandler(mgr)

	signer := GetRandomBech32Addr()
	operator := GetRandomBech32Addr()

	na := GetRandomValidatorNode(NodeActive)
	na.BondAddress = common.Address(signer.String())
	c.Assert(mgr.Keeper().SetNodeAccount(ctx, na), IsNil)

	setLivePendingKeygen(c, ctx, mgr)
	mgr.Keeper().SetMimir(ctx, "HaltChurning", 1)

	_, err := h.Run(ctx, rotateMsg(signer, operator))
	c.Assert(err, NotNil)
	c.Assert(err.Error(), Equals, "rotate not allowed while a keygen is pending")
}

// A vault migration in progress (a retiring vault with real funds) blocks rotation.
func (s *HandlerOperatorRotateSuite) TestOperatorRotateHandler_RetiringVaultWithFundsBlocked(c *C) {
	ctx, mgr := setupManagerForTest(c)
	h := NewOperatorRotateHandler(mgr)

	signer := GetRandomBech32Addr()
	operator := GetRandomBech32Addr()

	na := GetRandomValidatorNode(NodeActive)
	na.BondAddress = common.Address(signer.String())
	c.Assert(mgr.Keeper().SetNodeAccount(ctx, na), IsNil)

	retiring := GetRandomVault()
	retiring.Status = RetiringVault
	retiring.AddFunds(common.NewCoins(common.NewCoin(common.BTCAsset, cosmos.NewUint(100*common.One))))
	c.Assert(mgr.Keeper().SetVault(ctx, retiring), IsNil)

	_, err := h.Run(ctx, rotateMsg(signer, operator))
	c.Assert(err, NotNil)
	c.Assert(err.Error(), Equals, "rotate not allowed while a vault migration is in progress")
}

func (s *HandlerOperatorRotateSuite) TestOperatorRotateHandler_NoNodesFound(c *C) {
	ctx, mgr := setupManagerForTest(c)
	h := NewOperatorRotateHandler(mgr)

	signer := GetRandomBech32Addr()
	operator := GetRandomBech32Addr()
	msg := NewMsgOperatorRotate(signer, operator, common.NewCoin(common.EmptyAsset, cosmos.ZeroUint()))

	// No node accounts set, so no nodes will be found for signer
	_, err := h.Run(ctx, msg)
	c.Assert(err, NotNil)
	c.Assert(err.Error(), Matches, "no nodes found for operator.*")
}

func (s *HandlerOperatorRotateSuite) TestOperatorRotateHandler_ActiveNodeOldOperator(c *C) {
	ctx, mgr := setupManagerForTest(c)
	h := NewOperatorRotateHandler(mgr)

	// An operator with an Active node may now rotate (outside the churn window).
	na := GetRandomValidatorNode(NodeActive)
	signer := GetRandomBech32Addr()
	na.BondAddress = common.Address(signer.String())
	c.Assert(mgr.Keeper().SetNodeAccount(ctx, na), IsNil)

	operator := GetRandomBech32Addr()
	msg := NewMsgOperatorRotate(signer, operator, common.NewCoin(common.EmptyAsset, cosmos.ZeroUint()))

	result, err := h.Run(ctx, msg)
	c.Assert(err, IsNil)
	c.Assert(result, NotNil)

	// The Active node's operator address was rotated, status untouched.
	updated, err := mgr.Keeper().GetNodeAccount(ctx, na.NodeAddress)
	c.Assert(err, IsNil)
	c.Assert(updated.BondAddress.Equals(common.Address(operator.String())), Equals, true)
	c.Assert(updated.Status, Equals, NodeActive)
}

func (s *HandlerOperatorRotateSuite) TestOperatorRotateHandler_ActiveNodeNewOperator(c *C) {
	ctx, mgr := setupManagerForTest(c)
	h := NewOperatorRotateHandler(mgr)

	signer := GetRandomBech32Addr()
	operator := GetRandomBech32Addr()

	// Create a standby node with the signer as bond address
	standbyNode := GetRandomValidatorNode(NodeStandby)
	standbyNode.BondAddress = common.Address(signer.String())
	c.Assert(mgr.Keeper().SetNodeAccount(ctx, standbyNode), IsNil)

	// Create an active node with the NEW operator as bond address
	activeNode := GetRandomValidatorNode(NodeActive)
	activeNode.BondAddress = common.Address(operator.String())
	c.Assert(mgr.Keeper().SetNodeAccount(ctx, activeNode), IsNil)

	msg := NewMsgOperatorRotate(signer, operator, common.NewCoin(common.EmptyAsset, cosmos.ZeroUint()))

	_, err := h.Run(ctx, msg)
	c.Assert(err, NotNil)
	c.Assert(err.Error(), Matches, "cannot rotate to operator with active node.*")
}

func (s *HandlerOperatorRotateSuite) TestOperatorRotateHandler_DuplicateBondProvider(c *C) {
	ctx, mgr := setupManagerForTest(c)
	h := NewOperatorRotateHandler(mgr)

	signer := GetRandomBech32Addr()
	operator := GetRandomBech32Addr()

	// Create a standby node with the signer as bond address
	na := GetRandomValidatorNode(NodeStandby)
	na.BondAddress = common.Address(signer.String())
	c.Assert(mgr.Keeper().SetNodeAccount(ctx, na), IsNil)

	// Set up bond providers with the new operator already in the list
	bp := NewBondProviders(na.NodeAddress)
	bp.Providers = []BondProvider{
		NewBondProvider(signer),
		NewBondProvider(operator), // operator already exists
	}
	c.Assert(mgr.Keeper().SetBondProviders(ctx, bp), IsNil)

	msg := NewMsgOperatorRotate(signer, operator, common.NewCoin(common.EmptyAsset, cosmos.ZeroUint()))

	_, err := h.Run(ctx, msg)
	c.Assert(err, NotNil)
	c.Assert(err.Error(), Matches, "operator .* is already a bond provider for node.*")
}

func (s *HandlerOperatorRotateSuite) TestOperatorRotateHandler_NoBondProviderMatch(c *C) {
	ctx, mgr := setupManagerForTest(c)
	h := NewOperatorRotateHandler(mgr)

	signer := GetRandomBech32Addr()
	operator := GetRandomBech32Addr()

	// Create a standby node with the signer as bond address
	na := GetRandomValidatorNode(NodeStandby)
	na.BondAddress = common.Address(signer.String())
	c.Assert(mgr.Keeper().SetNodeAccount(ctx, na), IsNil)

	// Set up bond providers with a different address (not the signer)
	bp := NewBondProviders(na.NodeAddress)
	otherAddr := GetRandomBech32Addr()
	bp.Providers = []BondProvider{
		NewBondProvider(otherAddr),
	}
	c.Assert(mgr.Keeper().SetBondProviders(ctx, bp), IsNil)

	msg := NewMsgOperatorRotate(signer, operator, common.NewCoin(common.EmptyAsset, cosmos.ZeroUint()))

	_, err := h.Run(ctx, msg)
	c.Assert(err, NotNil)
	c.Assert(err.Error(), Matches, "no bond provider matches current operator.*")
}

func (s *HandlerOperatorRotateSuite) TestOperatorRotateHandler_HappyPath(c *C) {
	ctx, mgr := setupManagerForTest(c)
	h := NewOperatorRotateHandler(mgr)

	signer := GetRandomBech32Addr()
	operator := GetRandomBech32Addr()

	// Create a standby node with the signer as bond address
	na := GetRandomValidatorNode(NodeStandby)
	na.BondAddress = common.Address(signer.String())
	c.Assert(mgr.Keeper().SetNodeAccount(ctx, na), IsNil)

	msg := NewMsgOperatorRotate(signer, operator, common.NewCoin(common.EmptyAsset, cosmos.ZeroUint()))

	result, err := h.Run(ctx, msg)
	c.Assert(err, IsNil)
	c.Assert(result, NotNil)

	// Verify node account was updated with new bond address
	updatedNA, err := mgr.Keeper().GetNodeAccount(ctx, na.NodeAddress)
	c.Assert(err, IsNil)
	c.Assert(updatedNA.BondAddress.Equals(common.Address(operator.String())), Equals, true,
		Commentf("expected bond address %s, got %s", operator.String(), updatedNA.BondAddress))

	// Verify bond providers were updated
	bp, err := mgr.Keeper().GetBondProviders(ctx, na.NodeAddress)
	c.Assert(err, IsNil)
	c.Assert(len(bp.Providers), Equals, 1)
	c.Assert(bp.Providers[0].BondAddress.Equals(operator), Equals, true)
}

func (s *HandlerOperatorRotateSuite) TestOperatorRotateHandler_HappyPathMultipleNodes(c *C) {
	ctx, mgr := setupManagerForTest(c)
	h := NewOperatorRotateHandler(mgr)

	signer := GetRandomBech32Addr()
	operator := GetRandomBech32Addr()

	// Create two standby nodes with the same signer as bond address
	na1 := GetRandomValidatorNode(NodeStandby)
	na1.BondAddress = common.Address(signer.String())
	c.Assert(mgr.Keeper().SetNodeAccount(ctx, na1), IsNil)

	na2 := GetRandomValidatorNode(NodeStandby)
	na2.BondAddress = common.Address(signer.String())
	c.Assert(mgr.Keeper().SetNodeAccount(ctx, na2), IsNil)

	msg := NewMsgOperatorRotate(signer, operator, common.NewCoin(common.EmptyAsset, cosmos.ZeroUint()))

	result, err := h.Run(ctx, msg)
	c.Assert(err, IsNil)
	c.Assert(result, NotNil)

	// Verify both nodes were rotated
	updatedNA1, err := mgr.Keeper().GetNodeAccount(ctx, na1.NodeAddress)
	c.Assert(err, IsNil)
	c.Assert(updatedNA1.BondAddress.Equals(common.Address(operator.String())), Equals, true)

	updatedNA2, err := mgr.Keeper().GetNodeAccount(ctx, na2.NodeAddress)
	c.Assert(err, IsNil)
	c.Assert(updatedNA2.BondAddress.Equals(common.Address(operator.String())), Equals, true)
}

func (s *HandlerOperatorRotateSuite) TestOperatorRotateHandler_WithExistingBondProviders(c *C) {
	ctx, mgr := setupManagerForTest(c)
	h := NewOperatorRotateHandler(mgr)

	signer := GetRandomBech32Addr()
	operator := GetRandomBech32Addr()

	// Create a standby node with the signer as bond address
	na := GetRandomValidatorNode(NodeStandby)
	na.BondAddress = common.Address(signer.String())
	c.Assert(mgr.Keeper().SetNodeAccount(ctx, na), IsNil)

	// Set up bond providers with the signer as the operator
	bp := NewBondProviders(na.NodeAddress)
	extraProvider := GetRandomBech32Addr()
	bp.Providers = []BondProvider{
		NewBondProvider(signer),
		NewBondProvider(extraProvider),
	}
	c.Assert(mgr.Keeper().SetBondProviders(ctx, bp), IsNil)

	msg := NewMsgOperatorRotate(signer, operator, common.NewCoin(common.EmptyAsset, cosmos.ZeroUint()))

	result, err := h.Run(ctx, msg)
	c.Assert(err, IsNil)
	c.Assert(result, NotNil)

	// Verify bond providers: signer should be replaced by operator, extraProvider stays
	updatedBP, err := mgr.Keeper().GetBondProviders(ctx, na.NodeAddress)
	c.Assert(err, IsNil)
	c.Assert(len(updatedBP.Providers), Equals, 2)

	// Find the rotated provider
	foundOperator := false
	foundExtra := false
	for _, p := range updatedBP.Providers {
		if p.BondAddress.Equals(operator) {
			foundOperator = true
		}
		if p.BondAddress.Equals(extraProvider) {
			foundExtra = true
		}
	}
	c.Assert(foundOperator, Equals, true, Commentf("operator should be in bond providers"))
	c.Assert(foundExtra, Equals, true, Commentf("extra provider should remain"))
}

func (s *HandlerOperatorRotateSuite) TestOperatorRotateHandler_WithinChurnWindow(c *C) {
	ctx, mgr := setupManagerForTest(c)
	h := NewOperatorRotateHandler(mgr)

	signer := GetRandomBech32Addr()
	operator := GetRandomBech32Addr()

	// Create a standby node with the signer as bond address
	na := GetRandomValidatorNode(NodeStandby)
	na.BondAddress = common.Address(signer.String())
	c.Assert(mgr.Keeper().SetNodeAccount(ctx, na), IsNil)

	// Create active vault to set lastChurnHeight
	vault := GetRandomVault()
	vault.Status = ActiveVault
	vault.StatusSince = 10
	c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)

	// Default mocknet ChurnInterval is 60. halfChurn = 30, cutoff = 10 + 30 = 40.
	// Block height 18 < 40, so should succeed.
	msg := NewMsgOperatorRotate(signer, operator, common.NewCoin(common.EmptyAsset, cosmos.ZeroUint()))

	result, err := h.Run(ctx, msg)
	c.Assert(err, IsNil)
	c.Assert(result, NotNil)
}

func (s *HandlerOperatorRotateSuite) TestOperatorRotateHandler_OperatorAddressEmpty(c *C) {
	ctx, mgr := setupManagerForTest(c)
	h := NewOperatorRotateHandler(mgr)

	// Empty operator address should fail ValidateBasic
	msg := NewMsgOperatorRotate(GetRandomBech32Addr(), cosmos.AccAddress{}, common.NewCoin(common.EmptyAsset, cosmos.ZeroUint()))
	_, err := h.Run(ctx, msg)
	c.Assert(err, NotNil)
}

func (s *HandlerOperatorRotateSuite) TestOperatorRotateHandler_NonZeroCoin(c *C) {
	ctx, mgr := setupManagerForTest(c)
	h := NewOperatorRotateHandler(mgr)

	// Non-zero coin should fail ValidateBasic
	msg := NewMsgOperatorRotate(GetRandomBech32Addr(), GetRandomBech32Addr(), common.NewCoin(common.RuneNative, cosmos.NewUint(100)))
	_, err := h.Run(ctx, msg)
	c.Assert(err, NotNil)
}
