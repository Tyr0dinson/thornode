package thorchain

import (
	"errors"
	"fmt"
	"strings"

	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper/types"
	types2 "gitlab.com/thorchain/thornode/v3/x/thorchain/types"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
)

type SlashingSuite struct{}

var _ = Suite(&SlashingSuite{})

func (s *SlashingSuite) SetUpSuite(_ *C) {
	SetupConfigForTest()
}

func (s *SlashingSuite) TestNodeSignSlashErrors(c *C) {
	testCases := []struct {
		name        string
		condition   func(keeper *TestSlashingLackKeeper)
		shouldError bool
	}{
		{
			name: "fail to get tx out should return an error",
			condition: func(keeper *TestSlashingLackKeeper) {
				keeper.failGetTxOut = true
			},
			shouldError: true,
		},
		{
			name: "fail to get vault should return an error",
			condition: func(keeper *TestSlashingLackKeeper) {
				keeper.failGetVault = true
			},
			shouldError: false,
		},
		{
			name: "fail to get node account by pub key should return an error",
			condition: func(keeper *TestSlashingLackKeeper) {
				keeper.failGetNodeAccountByPubKey = true
			},
			shouldError: false,
		},
		{
			name: "fail to get asgard vault by status should return an error",
			condition: func(keeper *TestSlashingLackKeeper) {
				keeper.failGetAsgardByStatus = true
			},
			shouldError: true,
		},
		{
			name: "fail to get observed tx voter should return an error",
			condition: func(keeper *TestSlashingLackKeeper) {
				keeper.failGetObservedTxVoter = true
			},
			shouldError: true,
		},
		{
			name: "fail to set tx out should return an error",
			condition: func(keeper *TestSlashingLackKeeper) {
				keeper.failSetTxOut = true
			},
			shouldError: true,
		},
	}
	for _, item := range testCases {
		c.Logf("name:%s", item.name)
		ctx, _ := setupKeeperForTest(c)
		ctx = ctx.WithBlockHeight(201) // set blockheight
		ver := GetCurrentVersion()
		constAccessor := constants.GetConstantValues(ver)
		na := GetRandomValidatorNode(NodeActive)
		inTx := common.NewTx(
			GetRandomTxHash(),
			GetRandomETHAddress(),
			GetRandomETHAddress(),
			common.Coins{
				common.NewCoin(common.ETHAsset, cosmos.NewUint(320000000)),
				common.NewCoin(common.RuneAsset(), cosmos.NewUint(420000000)),
			},
			nil,
			"SWAP:ETH.ETH",
		)

		txOutItem := TxOutItem{
			Chain:       common.ETHChain,
			InHash:      inTx.ID,
			VaultPubKey: na.PubKeySet.Secp256k1,
			ToAddress:   GetRandomETHAddress(),
			Coin: common.NewCoin(
				common.ETHAsset, cosmos.NewUint(3980500*common.One),
			),
		}
		txOut := NewTxOut(3)
		txOut.TxArray = append(txOut.TxArray, txOutItem)

		vault := GetRandomVault()
		vault.Type = AsgardVault
		keeper := &TestSlashingLackKeeper{
			txOut:  txOut,
			na:     na,
			vaults: Vaults{vault},
			voter: ObservedTxVoter{
				Actions: []TxOutItem{txOutItem},
			},
			slashPts: make(map[string]int64),
		}
		signingTransactionPeriod := constAccessor.GetInt64Value(constants.SigningTransactionPeriod)
		ctx = ctx.WithBlockHeight(3 + signingTransactionPeriod)
		slasher := newSlasher(keeper, NewDummyEventMgr())
		item.condition(keeper)
		if item.shouldError {
			c.Assert(slasher.LackSigning(ctx, NewDummyMgrWithKeeper(keeper)), NotNil)
		} else {
			c.Assert(slasher.LackSigning(ctx, NewDummyMgrWithKeeper(keeper)), IsNil)
		}
	}
}

func (s *SlashingSuite) TestNotSigningSlash(c *C) {
	ctx, _ := setupKeeperForTest(c)
	ctx = ctx.WithBlockHeight(201) // set blockheight
	txOutStore := NewTxStoreDummy()
	ver := GetCurrentVersion()
	constAccessor := constants.GetConstantValues(ver)
	na := GetRandomValidatorNode(NodeActive)
	inTx := common.NewTx(
		GetRandomTxHash(),
		GetRandomETHAddress(),
		GetRandomETHAddress(),
		common.Coins{
			common.NewCoin(common.ETHAsset, cosmos.NewUint(320000000)),
			common.NewCoin(common.RuneAsset(), cosmos.NewUint(420000000)),
		},
		nil,
		"SWAP:ETH.ETH",
	)

	txOutItem := TxOutItem{
		Chain:       common.ETHChain,
		InHash:      inTx.ID,
		VaultPubKey: na.PubKeySet.Secp256k1,
		ToAddress:   GetRandomETHAddress(),
		Coin: common.NewCoin(
			common.ETHAsset, cosmos.NewUint(3980500*common.One),
		),
	}
	txOut := NewTxOut(3)
	txOut.TxArray = append(txOut.TxArray, txOutItem)

	vault := GetRandomVault()
	vault.Type = AsgardVault
	vault.Coins = common.Coins{
		common.NewCoin(common.ETHAsset, cosmos.NewUint(5000000*common.One)),
	}
	keeper := &TestSlashingLackKeeper{
		txOut:  txOut,
		na:     na,
		vaults: Vaults{vault},
		voter: ObservedTxVoter{
			Actions: []TxOutItem{txOutItem},
		},
		slashPts: make(map[string]int64),
	}
	signingTransactionPeriod := constAccessor.GetInt64Value(constants.SigningTransactionPeriod)
	ctx = ctx.WithBlockHeight(3 + signingTransactionPeriod)
	mgr := NewDummyMgrWithKeeper(keeper)
	mgr.txOutStore = txOutStore
	slasher := newSlasher(keeper, NewDummyEventMgr())
	c.Assert(slasher.LackSigning(ctx, mgr), IsNil)

	outItems, err := txOutStore.GetOutboundItems(ctx)
	c.Assert(err, IsNil)
	c.Assert(outItems, HasLen, 1)
	c.Assert(outItems[0].VaultPubKey.Equals(keeper.vaults[0].PubKey), Equals, true)
	c.Assert(outItems[0].Memo, Equals, "")
	c.Assert(keeper.voter.Actions, HasLen, 1)
	// ensure we've updated our action item
	c.Assert(keeper.voter.Actions[0].VaultPubKey.Equals(outItems[0].VaultPubKey), Equals, true)
	c.Assert(keeper.txOut.TxArray[0].OutHash.IsEmpty(), Equals, false)
}

func (s *SlashingSuite) TestNewSlasher(c *C) {
	nas := NodeAccounts{
		GetRandomValidatorNode(NodeActive),
		GetRandomValidatorNode(NodeActive),
	}
	keeper := &TestSlashObservingKeeper{
		nas:      nas,
		addrs:    []cosmos.AccAddress{nas[0].NodeAddress},
		slashPts: make(map[string]int64),
	}
	slasher := newSlasher(keeper, NewDummyEventMgr())
	c.Assert(slasher, NotNil)
}

func (s *SlashingSuite) TestHandleSuccessfulSign(c *C) {
	ctx, _ := setupKeeperForTest(c)
	constAccessor := constants.GetConstantValues(GetCurrentVersion())

	testCases := []struct {
		name                  string
		setupNodeAccount      func() (NodeAccount, error)
		validatorAddr         string
		expectedMissingBefore uint64
		expectedMissingAfter  uint64
		expectedErr           error
	}{
		{
			name: "normal node account with missing blocks",
			setupNodeAccount: func() (NodeAccount, error) {
				na := GetRandomValidatorNode(NodeActive)
				na.MissingBlocks = 10
				return na, nil
			},
			expectedMissingBefore: 10,
			expectedMissingAfter:  9,
			expectedErr:           nil,
		},
		{
			name: "node account with zero missing blocks",
			setupNodeAccount: func() (NodeAccount, error) {
				na := GetRandomValidatorNode(NodeActive)
				na.MissingBlocks = 0
				return na, nil
			},
			expectedMissingBefore: 0,
			expectedMissingAfter:  0,
			expectedErr:           nil,
		},
		{
			name: "node account with max missing blocks",
			setupNodeAccount: func() (NodeAccount, error) {
				na := GetRandomValidatorNode(NodeActive)
				na.MissingBlocks = 100
				return na, nil
			},
			expectedMissingBefore: 100,
			expectedMissingAfter:  99,
			expectedErr:           nil,
		},
	}

	for _, tc := range testCases {
		c.Logf("Test case: %s", tc.name)
		na, err := tc.setupNodeAccount()
		c.Assert(err, IsNil)

		keeper := &TestDoubleSlashKeeper{
			na:          na,
			network:     NewNetwork(),
			slashPoints: make(map[string]int64),
			constants:   make(map[string]int64),
		}
		slasher := newSlasher(keeper, NewDummyEventMgr())

		pk, err := cosmos.GetPubKeyFromBech32(cosmos.Bech32PubKeyTypeConsPub, na.ValidatorConsPubKey)
		c.Assert(err, IsNil)

		var pair nodeAddressValidatorAddressPair
		pair.nodeAddress = na.NodeAddress
		pair.validatorAddress = pk.Address()

		c.Assert(keeper.na.MissingBlocks, Equals, tc.expectedMissingBefore)
		err = slasher.HandleSuccessfulSign(ctx, pk.Address(), constAccessor, []nodeAddressValidatorAddressPair{pair})
		c.Assert(err, IsNil)
		c.Assert(keeper.na.MissingBlocks, Equals, tc.expectedMissingAfter)
	}
}

func (s *SlashingSuite) TestHandleSuccessfulSignErrors(c *C) {
	ctx, _ := setupKeeperForTest(c)
	constAccessor := constants.GetConstantValues(GetCurrentVersion())

	// Test case: validator address not found
	na := GetRandomValidatorNode(NodeActive)
	keeper := &TestDoubleSlashKeeper{
		na:          na,
		network:     NewNetwork(),
		slashPoints: make(map[string]int64),
		constants:   make(map[string]int64),
	}
	slasher := newSlasher(keeper, NewDummyEventMgr())

	randomAddr := GetRandomBech32Addr().String()
	err := slasher.HandleSuccessfulSign(ctx, []byte(randomAddr), constAccessor, []nodeAddressValidatorAddressPair{})
	c.Assert(err, NotNil)
	c.Assert(err.Error(), Matches, "could not find active node account with validator address: .*")
}

func (s *SlashingSuite) TestHandleMissingSign(c *C) {
	ctx, _ := setupKeeperForTest(c)
	constAccessor := constants.GetConstantValues(GetCurrentVersion())

	testCases := []struct {
		name                  string
		setupNodeAccount      func() (NodeAccount, error)
		maxTrack              int64
		missBlockSignSlashPts int64
		expectedMissingBefore uint64
		expectedMissingAfter  uint64
		expectedSlashPoints   int64
		expectedErr           error
	}{
		{
			name: "normal node account with no missing blocks",
			setupNodeAccount: func() (NodeAccount, error) {
				na := GetRandomValidatorNode(NodeActive)
				na.MissingBlocks = 0
				return na, nil
			},
			maxTrack:              10,
			missBlockSignSlashPts: 5,
			expectedMissingBefore: 0,
			expectedMissingAfter:  1,
			expectedSlashPoints:   5,
			expectedErr:           nil,
		},
		{
			name: "node account with some missing blocks",
			setupNodeAccount: func() (NodeAccount, error) {
				na := GetRandomValidatorNode(NodeActive)
				na.MissingBlocks = 5
				return na, nil
			},
			maxTrack:              10,
			missBlockSignSlashPts: 5,
			expectedMissingBefore: 5,
			expectedMissingAfter:  6,
			expectedSlashPoints:   5,
			expectedErr:           nil,
		},
		{
			name: "node account at max missing blocks",
			setupNodeAccount: func() (NodeAccount, error) {
				na := GetRandomValidatorNode(NodeActive)
				na.MissingBlocks = 10
				return na, nil
			},
			maxTrack:              10,
			missBlockSignSlashPts: 5,
			expectedMissingBefore: 10,
			expectedMissingAfter:  10, // Should not increase beyond maxTrack
			expectedSlashPoints:   5,
			expectedErr:           nil,
		},
		{
			name: "node account exceeding max missing blocks",
			setupNodeAccount: func() (NodeAccount, error) {
				na := GetRandomValidatorNode(NodeActive)
				na.MissingBlocks = 11 // This is already above maxTrack, but should be capped
				return na, nil
			},
			maxTrack:              10,
			missBlockSignSlashPts: 5,
			expectedMissingBefore: 11,
			expectedMissingAfter:  10, // Should be capped at maxTrack
			expectedSlashPoints:   5,
			expectedErr:           nil,
		},
		{
			name: "node account with low maxTrack value",
			setupNodeAccount: func() (NodeAccount, error) {
				na := GetRandomValidatorNode(NodeActive)
				na.MissingBlocks = 1
				return na, nil
			},
			maxTrack:              3,
			missBlockSignSlashPts: 5,
			expectedMissingBefore: 1,
			expectedMissingAfter:  2,
			expectedSlashPoints:   5,
			expectedErr:           nil,
		},
		{
			name: "node account with high slash points",
			setupNodeAccount: func() (NodeAccount, error) {
				na := GetRandomValidatorNode(NodeActive)
				na.MissingBlocks = 5
				return na, nil
			},
			maxTrack:              10,
			missBlockSignSlashPts: 20,
			expectedMissingBefore: 5,
			expectedMissingAfter:  6,
			expectedSlashPoints:   20,
			expectedErr:           nil,
		},
	}

	for _, tc := range testCases {
		c.Logf("Test case: %s", tc.name)
		na, err := tc.setupNodeAccount()
		c.Assert(err, IsNil)

		keeper := &TestDoubleSlashKeeper{
			na:          na,
			network:     NewNetwork(),
			slashPoints: make(map[string]int64),
			constants:   make(map[string]int64),
		}
		keeper.constants["MissBlockSignSlashPoints"] = tc.missBlockSignSlashPts
		keeper.constants["MaxTrackMissingBlock"] = tc.maxTrack
		slasher := newSlasher(keeper, NewDummyEventMgr())

		pk, err := cosmos.GetPubKeyFromBech32(cosmos.Bech32PubKeyTypeConsPub, na.ValidatorConsPubKey)
		c.Assert(err, IsNil)

		var pair nodeAddressValidatorAddressPair
		pair.nodeAddress = na.NodeAddress
		pair.validatorAddress = pk.Address()

		c.Assert(keeper.na.MissingBlocks, Equals, tc.expectedMissingBefore)
		err = slasher.HandleMissingSign(ctx, pk.Address(), constAccessor, []nodeAddressValidatorAddressPair{pair})
		c.Assert(err, IsNil)
		c.Assert(keeper.na.MissingBlocks, Equals, tc.expectedMissingAfter)
		c.Assert(keeper.slashPoints[na.NodeAddress.String()], Equals, tc.expectedSlashPoints)
	}
}

func (s *SlashingSuite) TestHandleMissingSignErrors(c *C) {
	ctx, _ := setupKeeperForTest(c)
	constAccessor := constants.GetConstantValues(GetCurrentVersion())

	// Test case: validator address not found
	na := GetRandomValidatorNode(NodeActive)
	keeper := &TestDoubleSlashKeeper{
		na:          na,
		network:     NewNetwork(),
		slashPoints: make(map[string]int64),
		constants:   make(map[string]int64),
	}
	slasher := newSlasher(keeper, NewDummyEventMgr())

	randomAddr := GetRandomBech32Addr().String()
	err := slasher.HandleMissingSign(ctx, []byte(randomAddr), constAccessor, []nodeAddressValidatorAddressPair{})
	c.Assert(err, NotNil)
	c.Assert(err.Error(), Matches, "could not find active node account with validator address: .*")
}

func (s *SlashingSuite) TestDoubleSign(c *C) {
	ctx, _ := setupKeeperForTest(c)
	constAccessor := constants.GetConstantValues(GetCurrentVersion())

	na := GetRandomValidatorNode(NodeActive)

	keeper := &TestDoubleSlashKeeper{
		na:          na,
		network:     NewNetwork(),
		slashPoints: make(map[string]int64),
		constants:   make(map[string]int64),
	}
	slasher := newSlasher(keeper, NewDummyEventMgr())

	pk, err := cosmos.GetPubKeyFromBech32(cosmos.Bech32PubKeyTypeConsPub, na.ValidatorConsPubKey)
	c.Assert(err, IsNil)

	var pair nodeAddressValidatorAddressPair
	pair.nodeAddress = na.NodeAddress
	pair.validatorAddress = pk.Address()

	c.Assert(keeper.slashPoints[na.NodeAddress.String()], Equals, int64(0))
	keeper.constants["DoubleBlockSignSlashPoints"] = 1
	err = slasher.HandleDoubleSign(ctx, pk.Address(), 0, constAccessor, []nodeAddressValidatorAddressPair{pair})
	c.Assert(err, IsNil)
	c.Assert(keeper.slashPoints[na.NodeAddress.String()], Equals, int64(1))
}

func (s *SlashingSuite) TestIncreaseDecreaseSlashPoints(c *C) {
	ctx, _ := setupKeeperForTest(c)

	na := GetRandomValidatorNode(NodeActive)
	na.Bond = cosmos.NewUint(100 * common.One)

	keeper := &TestDoubleSlashKeeper{
		na:          na,
		network:     NewNetwork(),
		slashPoints: make(map[string]int64),
	}
	slasher := newSlasher(keeper, NewDummyEventMgr())
	addr := GetRandomBech32Addr()
	slasher.IncSlashPoints(ctx, 1, addr)
	slasher.DecSlashPoints(ctx, 1, addr)
	c.Assert(keeper.slashPoints[addr.String()], Equals, int64(0))
}

func (s *SlashingSuite) TestSlashVault(c *C) {
	ctx, mgr := setupManagerForTest(c)
	slasher := newSlasher(mgr.Keeper(), mgr.EventMgr())
	// when coins are empty , it should return nil
	c.Assert(slasher.SlashVault(ctx, GetRandomPubKey(), common.NewCoins(), mgr), IsNil)

	// when vault is not available , it should return an error
	err := slasher.SlashVault(ctx, GetRandomPubKey(), common.NewCoins(common.NewCoin(common.BTCAsset, cosmos.NewUint(common.One))), mgr)
	c.Assert(err, NotNil)
	c.Assert(errors.Is(err, types.ErrVaultNotFound), Equals, true)

	// create a node
	node := GetRandomValidatorNode(NodeActive)
	c.Assert(mgr.Keeper().SetNodeAccount(ctx, node), IsNil)
	FundModule(c, ctx, mgr.Keeper(), BondName, node.Bond.Uint64())
	vault := GetRandomVault()
	vault.Type = AsgardVault
	vault.Status = types2.VaultStatus_ActiveVault
	vault.PubKey = node.PubKeySet.Secp256k1
	vault.Membership = []string{
		node.PubKeySet.Secp256k1.String(),
	}
	vault.Coins = common.NewCoins(
		common.NewCoin(common.BTCAsset, cosmos.NewUint(2*common.One)),
	)
	c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)

	pool := NewPool()
	pool.Asset = common.BTCAsset
	pool.BalanceRune = cosmos.NewUint(100 * common.One)
	pool.BalanceAsset = cosmos.NewUint(100 * common.One)
	pool.Status = PoolAvailable

	c.Assert(mgr.Keeper().SetPool(ctx, pool), IsNil)

	asgardBeforeSlash := mgr.Keeper().GetRuneBalanceOfModule(ctx, AsgardName)
	bondBeforeSlash := mgr.Keeper().GetRuneBalanceOfModule(ctx, BondName)
	reserveBeforeSlash := mgr.Keeper().GetRuneBalanceOfModule(ctx, ReserveName)
	poolBeforeSlash := pool.BalanceRune

	err = slasher.SlashVault(ctx, vault.PubKey, common.NewCoins(common.NewCoin(common.BTCAsset, cosmos.NewUint(common.One))), mgr)
	c.Assert(err, IsNil)
	nodeTemp, err := mgr.Keeper().GetNodeAccountByPubKey(ctx, vault.PubKey)
	c.Assert(err, IsNil)
	expectedBond := cosmos.NewUint(99850000000)
	c.Assert(nodeTemp.Bond.Equal(expectedBond), Equals, true, Commentf("%d", nodeTemp.Bond.Uint64()))

	asgardAfterSlash := mgr.Keeper().GetRuneBalanceOfModule(ctx, AsgardName)
	bondAfterSlash := mgr.Keeper().GetRuneBalanceOfModule(ctx, BondName)
	reserveAfterSlash := mgr.Keeper().GetRuneBalanceOfModule(ctx, ReserveName)

	pool, err = mgr.Keeper().GetPool(ctx, pool.Asset)
	c.Assert(err, IsNil)
	poolAfterSlash := pool.BalanceRune

	// ensure pool's change is in sync with asgard's change
	c.Assert(asgardAfterSlash.Sub(asgardBeforeSlash).Uint64(), Equals, poolAfterSlash.Sub(poolBeforeSlash).Uint64(), Commentf("%d", "pool/asgard rune mismatch"))

	c.Check(asgardAfterSlash.Sub(asgardBeforeSlash).Uint64(), Equals, uint64(100000000), Commentf("%d", asgardAfterSlash.Sub(asgardBeforeSlash).Uint64()))
	c.Check(asgardAfterSlash.Sub(asgardBeforeSlash).Uint64(), Equals, uint64(100000000), Commentf("%d", asgardAfterSlash.Sub(asgardBeforeSlash).Uint64()))
	c.Check(bondBeforeSlash.Sub(bondAfterSlash).Uint64(), Equals, uint64(150000000), Commentf("%d", bondBeforeSlash.Sub(bondAfterSlash).Uint64()))
	c.Check(reserveAfterSlash.Sub(reserveBeforeSlash).Uint64(), Equals, uint64(50000000), Commentf("%d", reserveAfterSlash.Sub(reserveBeforeSlash).Uint64()))

	// add one more node , slash asgard
	node1 := GetRandomValidatorNode(NodeActive)
	c.Assert(mgr.Keeper().SetNodeAccount(ctx, node1), IsNil)
	FundModule(c, ctx, mgr.Keeper(), BondName, node1.Bond.Uint64())

	vault1 := GetRandomVault()
	vault1.Type = AsgardVault
	vault1.Status = types2.VaultStatus_ActiveVault
	vault1.PubKey = GetRandomPubKey()
	vault1.Membership = []string{
		node.PubKeySet.Secp256k1.String(),
		node1.PubKeySet.Secp256k1.String(),
	}
	vault1.Coins = common.NewCoins(
		common.NewCoin(common.BTCAsset, cosmos.NewUint(2*common.One)),
	)
	c.Assert(mgr.Keeper().SetVault(ctx, vault1), IsNil)
	nodeBeforeSlash, err := mgr.Keeper().GetNodeAccount(ctx, node.NodeAddress)
	c.Assert(err, IsNil)
	nodeBondBeforeSlash := nodeBeforeSlash.Bond
	node1BondBeforeSlash := node1.Bond
	mgr.Keeper().SetMimir(ctx, "PauseOnSlashThreshold", 1)
	err = slasher.SlashVault(ctx, vault1.PubKey, common.NewCoins(common.NewCoin(common.BTCAsset, cosmos.NewUint(common.One))), mgr)
	c.Assert(err, IsNil)

	nodeAfterSlash, err := mgr.Keeper().GetNodeAccount(ctx, node.NodeAddress)
	c.Assert(err, IsNil)
	node1AfterSlash, err := mgr.Keeper().GetNodeAccount(ctx, node1.NodeAddress)
	c.Assert(err, IsNil)
	nodeBondAfterSlash := nodeAfterSlash.Bond
	node1BondAfterSlash := node1AfterSlash.Bond

	c.Check(nodeBondBeforeSlash.Sub(nodeBondAfterSlash).Uint64(), Equals, uint64(76457722), Commentf("%d", nodeBondBeforeSlash.Sub(nodeBondAfterSlash).Uint64()))
	c.Check(node1BondBeforeSlash.Sub(node1BondAfterSlash).Uint64(), Equals, uint64(76572581), Commentf("%d", node1BondBeforeSlash.Sub(node1BondAfterSlash).Uint64()))

	// Check that signing halt mimir was set
	val, err := mgr.Keeper().GetMimir(ctx, fmt.Sprintf(constants.MimirTemplateHaltSigning, common.BTCChain))
	c.Assert(err, IsNil)
	c.Assert(val, Equals, int64(18), Commentf("%d", val))

	// Check that trading halt mimir was set
	val2, err := mgr.Keeper().GetMimir(ctx, fmt.Sprintf(constants.MimirTemplateHaltTrading, common.BTCChain))
	c.Assert(err, IsNil)
	c.Assert(val2, Equals, int64(18), Commentf("%d", val2))
}

func (s *SlashingSuite) TestUpdatePoolFromSlash(c *C) {
	ctx, mgr := setupManagerForTest(c)
	slasher := newSlasher(mgr.Keeper(), mgr.EventMgr())

	pool := NewPool()
	pool.Asset = common.BTCAsset
	pool.BalanceRune = cosmos.NewUint(1000 * common.One)
	pool.BalanceAsset = cosmos.NewUint(1000 * common.One)
	pool.Status = PoolAvailable
	c.Assert(mgr.Keeper().SetPool(ctx, pool), IsNil)

	deductAsset := cosmos.NewUint(250 * common.One)
	creditRune := cosmos.NewUint(500 * common.One)
	stolenAsset := common.NewCoin(common.BTCAsset, deductAsset)
	slasher.updatePoolFromSlash(ctx, pool, stolenAsset, creditRune, mgr)

	pool, err := mgr.Keeper().GetPool(ctx, common.BTCAsset)
	c.Assert(err, IsNil)
	c.Assert(pool.BalanceAsset.Uint64(), Equals, uint64(750*common.One))
	c.Assert(pool.BalanceRune.Uint64(), Equals, uint64(1500*common.One))
}

func (s *SlashingSuite) TestNetworkShouldNotSlashMorethanVaultAmount(c *C) {
	ctx, mgr := setupManagerForTest(c)
	slasher := newSlasher(mgr.Keeper(), mgr.EventMgr())

	// create a node
	node := GetRandomValidatorNode(NodeActive)
	c.Assert(mgr.Keeper().SetNodeAccount(ctx, node), IsNil)
	FundModule(c, ctx, mgr.Keeper(), BondName, node.Bond.Uint64())
	vault := GetRandomVault()
	vault.Type = AsgardVault
	vault.Status = types2.VaultStatus_ActiveVault
	vault.PubKey = node.PubKeySet.Secp256k1
	vault.Membership = []string{
		node.PubKeySet.Secp256k1.String(),
	}
	vault.Coins = common.NewCoins(
		common.NewCoin(common.BTCAsset, cosmos.NewUint(common.One/2)),
	)
	c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)

	pool := NewPool()
	pool.Asset = common.BTCAsset
	pool.BalanceRune = cosmos.NewUint(100 * common.One)
	pool.BalanceAsset = cosmos.NewUint(100 * common.One)
	pool.Status = PoolAvailable

	c.Assert(mgr.Keeper().SetPool(ctx, pool), IsNil)

	asgardBeforeSlash := mgr.Keeper().GetRuneBalanceOfModule(ctx, AsgardName)
	bondBeforeSlash := mgr.Keeper().GetRuneBalanceOfModule(ctx, BondName)
	reserveBeforeSlash := mgr.Keeper().GetRuneBalanceOfModule(ctx, ReserveName)
	poolBeforeSlash := pool.BalanceRune

	// vault only has 0.5 BTC , however the outbound is 1 BTC , make sure we don't over slash the vault
	err := slasher.SlashVault(ctx, vault.PubKey, common.NewCoins(common.NewCoin(common.BTCAsset, cosmos.NewUint(common.One))), mgr)
	c.Assert(err, IsNil)
	nodeTemp, err := mgr.Keeper().GetNodeAccountByPubKey(ctx, vault.PubKey)
	c.Assert(err, IsNil)
	expectedBond := cosmos.NewUint(99925000000)
	c.Assert(nodeTemp.Bond.Equal(expectedBond), Equals, true, Commentf("%d", nodeTemp.Bond.Uint64()))

	asgardAfterSlash := mgr.Keeper().GetRuneBalanceOfModule(ctx, AsgardName)
	bondAfterSlash := mgr.Keeper().GetRuneBalanceOfModule(ctx, BondName)
	reserveAfterSlash := mgr.Keeper().GetRuneBalanceOfModule(ctx, ReserveName)

	pool, err = mgr.Keeper().GetPool(ctx, pool.Asset)
	c.Assert(err, IsNil)
	poolAfterSlash := pool.BalanceRune

	// ensure pool's change is in sync with asgard's change
	c.Assert(asgardAfterSlash.Sub(asgardBeforeSlash).Uint64(), Equals, poolAfterSlash.Sub(poolBeforeSlash).Uint64(), Commentf("%d", "pool/asgard rune mismatch"))

	c.Check(asgardAfterSlash.Sub(asgardBeforeSlash).Uint64(), Equals, uint64(50000000), Commentf("%d", asgardAfterSlash.Sub(asgardBeforeSlash).Uint64()))
	c.Check(bondBeforeSlash.Sub(bondAfterSlash).Uint64(), Equals, uint64(75000000), Commentf("%d", bondBeforeSlash.Sub(bondAfterSlash).Uint64()))
	c.Check(reserveAfterSlash.Sub(reserveBeforeSlash).Uint64(), Equals, uint64(25000000), Commentf("%d", reserveAfterSlash.Sub(reserveBeforeSlash).Uint64()))

	// add one more node , slash asgard
	node1 := GetRandomValidatorNode(NodeActive)
	c.Assert(mgr.Keeper().SetNodeAccount(ctx, node1), IsNil)
	FundModule(c, ctx, mgr.Keeper(), BondName, node1.Bond.Uint64())

	vault1 := GetRandomVault()
	vault1.Type = AsgardVault
	vault1.Status = types2.VaultStatus_ActiveVault
	vault1.PubKey = GetRandomPubKey()
	vault1.Membership = []string{
		node.PubKeySet.Secp256k1.String(),
		node1.PubKeySet.Secp256k1.String(),
	}
	vault1.Coins = common.NewCoins(
		common.NewCoin(common.BTCAsset, cosmos.NewUint(common.One/2)),
	)
	c.Assert(mgr.Keeper().SetVault(ctx, vault1), IsNil)
	nodeBeforeSlash, err := mgr.Keeper().GetNodeAccount(ctx, node.NodeAddress)
	c.Assert(err, IsNil)
	nodeBondBeforeSlash := nodeBeforeSlash.Bond
	node1BondBeforeSlash := node1.Bond
	mgr.Keeper().SetMimir(ctx, "PauseOnSlashThreshold", 1)
	err = slasher.SlashVault(ctx, vault1.PubKey, common.NewCoins(common.NewCoin(common.BTCAsset, cosmos.NewUint(common.One))), mgr)
	c.Assert(err, IsNil)

	nodeAfterSlash, err := mgr.Keeper().GetNodeAccount(ctx, node.NodeAddress)
	c.Assert(err, IsNil)
	node1AfterSlash, err := mgr.Keeper().GetNodeAccount(ctx, node1.NodeAddress)
	c.Assert(err, IsNil)
	nodeBondAfterSlash := nodeAfterSlash.Bond
	node1BondAfterSlash := node1AfterSlash.Bond

	c.Check(nodeBondBeforeSlash.Sub(nodeBondAfterSlash).Uint64(), Equals, uint64(37862676), Commentf("%d", nodeBondBeforeSlash.Sub(nodeBondAfterSlash).Uint64()))
	c.Check(node1BondBeforeSlash.Sub(node1BondAfterSlash).Uint64(), Equals, uint64(37891094), Commentf("%d", node1BondBeforeSlash.Sub(node1BondAfterSlash).Uint64()))

	// Check that signing halt mimir was set
	val, err := mgr.Keeper().GetMimir(ctx, fmt.Sprintf(constants.MimirTemplateHaltSigning, common.BTCChain))
	c.Assert(err, IsNil)
	c.Assert(val, Equals, int64(18), Commentf("%d", val))

	// Check that trading halt mimir was set
	val2, err := mgr.Keeper().GetMimir(ctx, fmt.Sprintf(constants.MimirTemplateHaltTrading, common.BTCChain))
	c.Assert(err, IsNil)
	c.Assert(val2, Equals, int64(18), Commentf("%d", val2))

	// Attempt to slash more than node has, pool should only be deducted what was successfully slashed
	pool.BalanceRune = cosmos.NewUint(4000 * common.One)
	pool.BalanceAsset = cosmos.NewUint(4000 * common.One)
	c.Assert(mgr.Keeper().SetPool(ctx, pool), IsNil)

	node2 := GetRandomValidatorNode(NodeActive)
	c.Assert(mgr.Keeper().SetNodeAccount(ctx, node2), IsNil)
	FundModule(c, ctx, mgr.Keeper(), BondName, node2.Bond.Uint64())

	vault = GetRandomVault()
	vault.Status = types2.VaultStatus_ActiveVault
	vault.PubKey = node.PubKeySet.Secp256k1
	vault.Membership = []string{
		node2.PubKeySet.Secp256k1.String(),
	}
	vault.Coins = common.NewCoins(
		common.NewCoin(common.BTCAsset, cosmos.NewUint(4000*common.One)),
	)
	c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)

	err = slasher.SlashVault(ctx, vault.PubKey, common.NewCoins(common.NewCoin(common.BTCAsset, cosmos.NewUint(2000*common.One))), mgr)
	c.Assert(err, IsNil)
	updatedPool, err := mgr.Keeper().GetPool(ctx, common.BTCAsset)
	c.Assert(err, IsNil)

	// Even though the total rune value to slash is 3000, the node only has 1000 RUNE bonded, so only slash and credit that much to the pool's rune side
	c.Assert(updatedPool.BalanceRune.Uint64(), Equals, cosmos.NewUint(5000*common.One).Uint64())
	// But deduct full stolen amount from asset side
	c.Assert(updatedPool.BalanceAsset.Uint64(), Equals, cosmos.NewUint(2000*common.One).Uint64())
}

func (s *SlashingSuite) TestNeedsNewVault(c *C) {
	ctx, mgr := setupManagerForTest(c)

	inhash := GetRandomTxHash()
	outhash := GetRandomTxHash()
	pk1 := GetRandomPubKey()
	pk2 := GetRandomPubKey()
	pk3 := GetRandomPubKey()
	sig1, _ := pk1.GetThorAddress()
	sig2, _ := pk2.GetThorAddress()
	sig3, _ := pk3.GetThorAddress()
	pk := GetRandomPubKey()
	tx := GetRandomTx()
	tx.ID = outhash
	obs := NewObservedTx(tx, 0, pk, 0)
	obs.ObservedPubKey = pk
	obs.Signers = []string{sig1.String(), sig2.String(), sig3.String()}

	vault := GetRandomVault()
	vault.Membership = []string{pk1.String(), pk2.String(), pk3.String()}

	slasher := newSlasher(mgr.Keeper(), mgr.EventMgr())

	c.Assert(len(tx.Coins), Equals, 1)
	toi := TxOutItem{
		InHash:      inhash,
		Coin:        tx.Coins[0],
		VaultPubKey: pk,
	}

	c.Check(slasher.needsNewVault(ctx, mgr, vault, 300, 1, toi), Equals, true)

	voter := NewObservedTxVoter(outhash, []common.ObservedTx{obs})
	mgr.Keeper().SetObservedTxOutVoter(ctx, voter)

	mgr.Keeper().SetObservedLink(ctx, inhash, outhash)

	c.Check(slasher.needsNewVault(ctx, mgr, vault, 300, 1, toi), Equals, false)
	ctx = ctx.WithBlockHeight(600)
	c.Check(slasher.needsNewVault(ctx, mgr, vault, 300, 1, toi), Equals, false)
	ctx = ctx.WithBlockHeight(900)
	c.Check(slasher.needsNewVault(ctx, mgr, vault, 300, 1, toi), Equals, false)

	// test that more than 2/3rd will always return false
	ctx = ctx.WithBlockHeight(999999999)
	c.Check(slasher.needsNewVault(ctx, mgr, vault, 300, 1, toi), Equals, false)
}

func (s *SlashingSuite) TestNeedsNewVaultEdDSA(c *C) {
	ctx, mgr := setupManagerForTest(c)

	inhash := GetRandomTxHash()
	outhash := GetRandomTxHash()
	pk1 := GetRandomPubKey()
	pk2 := GetRandomPubKey()
	pk3 := GetRandomPubKey()
	sig1, _ := pk1.GetThorAddress()
	sig2, _ := pk2.GetThorAddress()
	sig3, _ := pk3.GetThorAddress()

	// Use separate ECDSA and EdDSA keys for the vault, as would be the case for SOL.
	ecdsaPK := GetRandomPubKey()
	eddsaPK := GetRandomPubKey()

	tx := GetRandomTx()
	tx.ID = outhash
	obs := NewObservedTx(tx, 0, eddsaPK, 0)
	obs.ObservedPubKey = eddsaPK // observation uses the EdDSA key (as SOL bifrost would)
	obs.Signers = []string{sig1.String(), sig2.String(), sig3.String()}

	vault := GetRandomVault()
	vault.Membership = []string{pk1.String(), pk2.String(), pk3.String()}

	slasher := newSlasher(mgr.Keeper(), mgr.EventMgr())

	c.Assert(len(tx.Coins), Equals, 1)
	toi := TxOutItem{
		InHash:           inhash,
		Coin:             tx.Coins[0],
		VaultPubKey:      ecdsaPK, // ECDSA key on the toi
		VaultPubKeyEddsa: eddsaPK, // EdDSA key on the toi
	}

	// No observation yet, should need new vault.
	c.Check(slasher.needsNewVault(ctx, mgr, vault, 300, 1, toi), Equals, true)

	voter := NewObservedTxVoter(outhash, []common.ObservedTx{obs})
	mgr.Keeper().SetObservedTxOutVoter(ctx, voter)
	mgr.Keeper().SetObservedLink(ctx, inhash, outhash)

	// Observation exists with EdDSA key matching toi.VaultPubKeyEddsa,
	// even though it doesn't match toi.VaultPubKey (ECDSA). Should NOT need new vault.
	c.Check(slasher.needsNewVault(ctx, mgr, vault, 300, 1, toi), Equals, false)

	// Confirm it still returns false at later block heights.
	ctx = ctx.WithBlockHeight(999999999)
	c.Check(slasher.needsNewVault(ctx, mgr, vault, 300, 1, toi), Equals, false)
}

func (s *SlashingSuite) TestTreasuryRecoveryForMaxAttempts(c *C) {
	ctx, mgr := setupManagerForTest(c)

	// Setup a node and vault
	na := GetRandomValidatorNode(NodeActive)
	c.Assert(mgr.Keeper().SetNodeAccount(ctx, na), IsNil)

	vault := GetRandomVault()
	vault.Type = AsgardVault
	vault.Status = types2.VaultStatus_ActiveVault
	vault.PubKey = na.PubKeySet.Secp256k1
	vault.Membership = []string{na.PubKeySet.Secp256k1.String()}
	vault.Coins = common.NewCoins(
		common.NewCoin(common.BTCAsset, cosmos.NewUint(100*common.One)),
	)
	c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)

	// Setup pool
	pool := NewPool()
	pool.Asset = common.BTCAsset
	pool.BalanceRune = cosmos.NewUint(1000 * common.One)
	pool.BalanceAsset = cosmos.NewUint(1000 * common.One)
	pool.Status = PoolAvailable
	c.Assert(mgr.Keeper().SetPool(ctx, pool), IsNil)

	// Create tx out item
	inHash := GetRandomTxHash()
	txOutItem := TxOutItem{
		Chain:       common.BTCChain,
		InHash:      inHash,
		VaultPubKey: na.PubKeySet.Secp256k1,
		ToAddress:   GetRandomBTCAddress(),
		Coin:        common.NewCoin(common.BTCAsset, cosmos.NewUint(1*common.One)),
		Memo:        fmt.Sprintf("OUT:%s", inHash.String()),
	}

	// Parse the memo
	memo, err := ParseMemoWithTHORNames(ctx, mgr.Keeper(), txOutItem.Memo)
	c.Assert(err, IsNil)
	c.Assert(memo.IsOutbound(), Equals, true, Commentf("Memo should be outbound"))

	// Directly call sendFailedOutboundToTreasury
	slasher := newSlasher(mgr.Keeper(), mgr.EventMgr())
	err = slasher.sendFailedOutboundToTreasury(ctx, mgr, txOutItem, vault, memo)
	c.Assert(err, IsNil, Commentf("sendFailedOutboundToTreasury should not error"))

	// Verify a swap was queued (check both standard and advanced swap queues)
	hasSwap := false

	// Check standard swap queue
	swapQueueItems := mgr.Keeper().GetSwapQueueIterator(ctx)
	for ; swapQueueItems.Valid(); swapQueueItems.Next() {
		hasSwap = true
		break
	}
	swapQueueItems.Close()

	// If not in standard queue, check advanced swap queue
	if !hasSwap {
		advSwapQueueItems := mgr.Keeper().GetAdvSwapQueueItemIterator(ctx)
		for ; advSwapQueueItems.Valid(); advSwapQueueItems.Next() {
			hasSwap = true
			break
		}
		advSwapQueueItems.Close()
	}

	c.Assert(hasSwap, Equals, true, Commentf("Expected a swap to be queued for treasury recovery"))
}

func (s *SlashingSuite) TestTreasuryRecoveryWithDifferentMemoTypes(c *C) {
	ctx, mgr := setupManagerForTest(c)

	testCases := []struct {
		name          string
		memo          string
		shouldRecover bool
	}{
		{
			name:          "outbound memo should recover",
			memo:          fmt.Sprintf("OUT:%s", GetRandomTxHash()),
			shouldRecover: true,
		},
		{
			name:          "refund memo should recover",
			memo:          fmt.Sprintf("REFUND:%s", GetRandomTxHash()),
			shouldRecover: true,
		},
		{
			name:          "ragnarok memo should recover",
			memo:          fmt.Sprintf("RAGNAROK:%d", 12345),
			shouldRecover: true,
		},
		{
			name:          "migrate memo should not recover (internal)",
			memo:          fmt.Sprintf("MIGRATE:%d", 12345),
			shouldRecover: false,
		},
	}

	for _, tc := range testCases {
		c.Logf("Test case: %s", tc.name)

		// Setup vault
		na := GetRandomValidatorNode(NodeActive)
		c.Assert(mgr.Keeper().SetNodeAccount(ctx, na), IsNil)

		vault := GetRandomVault()
		vault.Type = AsgardVault
		vault.Status = types2.VaultStatus_ActiveVault
		vault.PubKey = na.PubKeySet.Secp256k1
		vault.Membership = []string{na.PubKeySet.Secp256k1.String()}
		vault.Coins = common.NewCoins(
			common.NewCoin(common.BTCAsset, cosmos.NewUint(100*common.One)),
		)
		c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)

		// Setup pool for non-RUNE assets
		pool := NewPool()
		pool.Asset = common.BTCAsset
		pool.BalanceRune = cosmos.NewUint(1000 * common.One)
		pool.BalanceAsset = cosmos.NewUint(1000 * common.One)
		pool.Status = PoolAvailable
		c.Assert(mgr.Keeper().SetPool(ctx, pool), IsNil)

		// Create tx out item
		inHash := GetRandomTxHash()
		txOutItem := TxOutItem{
			Chain:       common.BTCChain,
			InHash:      inHash,
			VaultPubKey: na.PubKeySet.Secp256k1,
			ToAddress:   GetRandomBTCAddress(),
			Coin:        common.NewCoin(common.BTCAsset, cosmos.NewUint(1*common.One)),
			Memo:        tc.memo,
		}

		// Parse memo
		parsedMemo, err := ParseMemoWithTHORNames(ctx, mgr.Keeper(), txOutItem.Memo)
		c.Assert(err, IsNil)

		// Check if memo matches expected outbound type
		c.Assert(parsedMemo.IsOutbound(), Equals, tc.shouldRecover, Commentf("Memo type mismatch for %s", tc.name))

		// If this should recover, test the sendFailedOutboundToTreasury function
		if tc.shouldRecover {
			slasher := newSlasher(mgr.Keeper(), mgr.EventMgr())
			err = slasher.sendFailedOutboundToTreasury(ctx, mgr, txOutItem, vault, parsedMemo)
			c.Assert(err, IsNil, Commentf("sendFailedOutboundToTreasury should not error for %s", tc.name))

			// Verify a swap was queued (check both standard and advanced swap queues)
			hasSwap := false

			// Check standard swap queue
			swapQueueItems := mgr.Keeper().GetSwapQueueIterator(ctx)
			for ; swapQueueItems.Valid(); swapQueueItems.Next() {
				hasSwap = true
				break
			}
			swapQueueItems.Close()

			// If not in standard queue, check advanced swap queue
			if !hasSwap {
				advSwapQueueItems := mgr.Keeper().GetAdvSwapQueueItemIterator(ctx)
				for ; advSwapQueueItems.Valid(); advSwapQueueItems.Next() {
					hasSwap = true
					break
				}
				advSwapQueueItems.Close()
			}

			c.Assert(hasSwap, Equals, true, Commentf("Expected swap to be queued for %s", tc.name))
		}
	}
}

func (s *SlashingSuite) TestReverseSwapForFailedSwapOutbound(c *C) {
	ctx, mgr := setupManagerForTest(c)

	// Setup a node and vault
	na := GetRandomValidatorNode(NodeActive)
	c.Assert(mgr.Keeper().SetNodeAccount(ctx, na), IsNil)

	vault := GetRandomVault()
	vault.Type = AsgardVault
	vault.Status = types2.VaultStatus_ActiveVault
	vault.PubKey = na.PubKeySet.Secp256k1
	vault.Membership = []string{na.PubKeySet.Secp256k1.String()}
	vault.Coins = common.NewCoins(
		common.NewCoin(common.BTCAsset, cosmos.NewUint(100*common.One)),
		common.NewCoin(common.ETHAsset, cosmos.NewUint(100*common.One)),
	)
	c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)

	// Setup pools for both assets
	btcPool := NewPool()
	btcPool.Asset = common.BTCAsset
	btcPool.BalanceRune = cosmos.NewUint(1000 * common.One)
	btcPool.BalanceAsset = cosmos.NewUint(1000 * common.One)
	btcPool.Status = PoolAvailable
	c.Assert(mgr.Keeper().SetPool(ctx, btcPool), IsNil)

	ethPool := NewPool()
	ethPool.Asset = common.ETHAsset
	ethPool.BalanceRune = cosmos.NewUint(1000 * common.One)
	ethPool.BalanceAsset = cosmos.NewUint(1000 * common.One)
	ethPool.Status = PoolAvailable
	c.Assert(mgr.Keeper().SetPool(ctx, ethPool), IsNil)

	// Create original swap transaction: ETH -> BTC
	inHash := GetRandomTxHash()
	ethAddr := GetRandomETHAddress()
	btcAddr := GetRandomBTCAddress()
	swapMemo := fmt.Sprintf("=:BTC.BTC:%s", btcAddr.String())

	originalTx := common.Tx{
		ID:          inHash,
		Chain:       common.ETHChain,
		FromAddress: ethAddr,
		ToAddress:   GetRandomETHAddress(), // vault address
		Coins:       common.Coins{common.NewCoin(common.ETHAsset, cosmos.NewUint(10*common.One))},
		Memo:        swapMemo,
		Gas:         common.Gas{common.NewCoin(common.ETHAsset, cosmos.NewUint(1000000))},
	}

	observedTx := ObservedTx{
		Tx:             originalTx,
		Status:         common.Status_done,
		OutHashes:      nil,
		BlockHeight:    ctx.BlockHeight(),
		Signers:        []string{na.NodeAddress.String()},
		ObservedPubKey: vault.PubKey,
		FinaliseHeight: ctx.BlockHeight(),
	}

	// Save the observed tx voter
	voter := NewObservedTxVoter(inHash, []ObservedTx{observedTx})
	voter.FinalisedHeight = ctx.BlockHeight()
	voter.Tx = observedTx
	mgr.Keeper().SetObservedTxInVoter(ctx, voter)

	// Create the failed outbound (BTC output that couldn't be sent)
	txOutItem := TxOutItem{
		Chain:       common.BTCChain,
		InHash:      inHash,
		VaultPubKey: na.PubKeySet.Secp256k1,
		ToAddress:   btcAddr,
		Coin:        common.NewCoin(common.BTCAsset, cosmos.NewUint(1*common.One)),
		Memo:        fmt.Sprintf("OUT:%s", inHash.String()),
	}

	// Parse the outbound memo
	memo, err := ParseMemoWithTHORNames(ctx, mgr.Keeper(), txOutItem.Memo)
	c.Assert(err, IsNil)
	c.Assert(memo.IsOutbound(), Equals, true)

	// Call sendFailedOutboundToTreasury - it should detect this is a swap and do a reverse swap
	slasher := newSlasher(mgr.Keeper(), mgr.EventMgr())
	err = slasher.sendFailedOutboundToTreasury(ctx, mgr, txOutItem, vault, memo)
	c.Assert(err, IsNil, Commentf("sendFailedOutboundToTreasury should not error"))

	// Verify a swap was queued for reverse swap (BTC -> ETH)
	hasSwap := false
	var queuedSwap MsgSwap

	// Check standard swap queue
	swapQueueItems := mgr.Keeper().GetSwapQueueIterator(ctx)
	for ; swapQueueItems.Valid(); swapQueueItems.Next() {
		var msg MsgSwap
		if err := mgr.Keeper().Cdc().Unmarshal(swapQueueItems.Value(), &msg); err == nil {
			hasSwap = true
			queuedSwap = msg
			break
		}
	}
	swapQueueItems.Close()

	// If not in standard queue, check advanced swap queue
	if !hasSwap {
		advSwapQueueItems := mgr.Keeper().GetAdvSwapQueueItemIterator(ctx)
		for ; advSwapQueueItems.Valid(); advSwapQueueItems.Next() {
			var msg MsgSwap
			if err := mgr.Keeper().Cdc().Unmarshal(advSwapQueueItems.Value(), &msg); err == nil {
				hasSwap = true
				queuedSwap = msg
				break
			}
		}
		advSwapQueueItems.Close()
	}

	c.Assert(hasSwap, Equals, true, Commentf("Expected reverse swap to be queued"))

	// Verify the swap is reversing correctly: BTC -> ETH back to original sender
	c.Assert(queuedSwap.Tx.Coins[0].Asset.Equals(common.BTCAsset), Equals, true,
		Commentf("Swap should be from BTC (failed asset)"))
	c.Assert(queuedSwap.TargetAsset.Equals(common.ETHAsset), Equals, true,
		Commentf("Swap should be to ETH (original asset)"))
	c.Assert(queuedSwap.Destination.Equals(ethAddr), Equals, true,
		Commentf("Swap should send back to original sender"))
	c.Assert(queuedSwap.TradeTarget.IsZero(), Equals, true,
		Commentf("Reverse swap should have no slip limit"))
}

func (s *SlashingSuite) TestReverseSwapWithCustomRefundAddress(c *C) {
	ctx, mgr := setupManagerForTest(c)

	// Setup a node and vault
	na := GetRandomValidatorNode(NodeActive)
	c.Assert(mgr.Keeper().SetNodeAccount(ctx, na), IsNil)

	vault := GetRandomVault()
	vault.Type = AsgardVault
	vault.Status = types2.VaultStatus_ActiveVault
	vault.PubKey = na.PubKeySet.Secp256k1
	vault.Membership = []string{na.PubKeySet.Secp256k1.String()}
	vault.Coins = common.NewCoins(
		common.NewCoin(common.BTCAsset, cosmos.NewUint(100*common.One)),
		common.NewCoin(common.ETHAsset, cosmos.NewUint(100*common.One)),
	)
	c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)

	// Setup pools
	btcPool := NewPool()
	btcPool.Asset = common.BTCAsset
	btcPool.BalanceRune = cosmos.NewUint(1000 * common.One)
	btcPool.BalanceAsset = cosmos.NewUint(1000 * common.One)
	btcPool.Status = PoolAvailable
	c.Assert(mgr.Keeper().SetPool(ctx, btcPool), IsNil)

	ethPool := NewPool()
	ethPool.Asset = common.ETHAsset
	ethPool.BalanceRune = cosmos.NewUint(1000 * common.One)
	ethPool.BalanceAsset = cosmos.NewUint(1000 * common.One)
	ethPool.Status = PoolAvailable
	c.Assert(mgr.Keeper().SetPool(ctx, ethPool), IsNil)

	// Create original swap transaction with custom refund address: ETH -> BTC
	inHash := GetRandomTxHash()
	ethAddr := GetRandomETHAddress()
	customRefundAddr := GetRandomETHAddress()
	btcAddr := GetRandomBTCAddress()
	// Format: =:ASSET:DESTINATION/REFUND:LIMIT
	swapMemo := fmt.Sprintf("=:BTC.BTC:%s/%s", btcAddr.String(), customRefundAddr.String())

	originalTx := common.Tx{
		ID:          inHash,
		Chain:       common.ETHChain,
		FromAddress: ethAddr,
		ToAddress:   GetRandomETHAddress(),
		Coins:       common.Coins{common.NewCoin(common.ETHAsset, cosmos.NewUint(10*common.One))},
		Memo:        swapMemo,
		Gas:         common.Gas{common.NewCoin(common.ETHAsset, cosmos.NewUint(1000000))},
	}

	observedTx := ObservedTx{
		Tx:             originalTx,
		Status:         common.Status_done,
		OutHashes:      nil,
		BlockHeight:    ctx.BlockHeight(),
		Signers:        []string{na.NodeAddress.String()},
		ObservedPubKey: vault.PubKey,
		FinaliseHeight: ctx.BlockHeight(),
	}

	voter := NewObservedTxVoter(inHash, []ObservedTx{observedTx})
	voter.FinalisedHeight = ctx.BlockHeight()
	voter.Tx = observedTx
	mgr.Keeper().SetObservedTxInVoter(ctx, voter)

	// Create the failed outbound
	txOutItem := TxOutItem{
		Chain:       common.BTCChain,
		InHash:      inHash,
		VaultPubKey: na.PubKeySet.Secp256k1,
		ToAddress:   btcAddr,
		Coin:        common.NewCoin(common.BTCAsset, cosmos.NewUint(1*common.One)),
		Memo:        fmt.Sprintf("OUT:%s", inHash.String()),
	}

	memo, err := ParseMemoWithTHORNames(ctx, mgr.Keeper(), txOutItem.Memo)
	c.Assert(err, IsNil)

	// Call sendFailedOutboundToTreasury
	slasher := newSlasher(mgr.Keeper(), mgr.EventMgr())
	err = slasher.sendFailedOutboundToTreasury(ctx, mgr, txOutItem, vault, memo)
	c.Assert(err, IsNil)

	// Verify the reverse swap uses the custom refund address
	hasSwap := false
	var queuedSwap MsgSwap

	swapQueueItems := mgr.Keeper().GetSwapQueueIterator(ctx)
	for ; swapQueueItems.Valid(); swapQueueItems.Next() {
		var msg MsgSwap
		if err := mgr.Keeper().Cdc().Unmarshal(swapQueueItems.Value(), &msg); err == nil {
			hasSwap = true
			queuedSwap = msg
			break
		}
	}
	swapQueueItems.Close()

	if !hasSwap {
		advSwapQueueItems := mgr.Keeper().GetAdvSwapQueueItemIterator(ctx)
		for ; advSwapQueueItems.Valid(); advSwapQueueItems.Next() {
			var msg MsgSwap
			if err := mgr.Keeper().Cdc().Unmarshal(advSwapQueueItems.Value(), &msg); err == nil {
				hasSwap = true
				queuedSwap = msg
				break
			}
		}
		advSwapQueueItems.Close()
	}

	c.Assert(hasSwap, Equals, true, Commentf("Expected reverse swap to be queued"))
	c.Assert(queuedSwap.Destination.Equals(customRefundAddr), Equals, true,
		Commentf("Reverse swap should use custom refund address"))
}

func (s *SlashingSuite) TestReverseSwapFallbackToTreasury(c *C) {
	ctx, mgr := setupManagerForTest(c)

	// Setup a node and vault
	na := GetRandomValidatorNode(NodeActive)
	c.Assert(mgr.Keeper().SetNodeAccount(ctx, na), IsNil)

	vault := GetRandomVault()
	vault.Type = AsgardVault
	vault.Status = types2.VaultStatus_ActiveVault
	vault.PubKey = na.PubKeySet.Secp256k1
	vault.Membership = []string{na.PubKeySet.Secp256k1.String()}
	vault.Coins = common.NewCoins(
		common.NewCoin(common.BTCAsset, cosmos.NewUint(100*common.One)),
	)
	c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)

	// Setup only BTC pool (no ETH pool for reverse swap)
	btcPool := NewPool()
	btcPool.Asset = common.BTCAsset
	btcPool.BalanceRune = cosmos.NewUint(1000 * common.One)
	btcPool.BalanceAsset = cosmos.NewUint(1000 * common.One)
	btcPool.Status = PoolAvailable
	c.Assert(mgr.Keeper().SetPool(ctx, btcPool), IsNil)

	// Create failed outbound WITHOUT an original observed tx
	// This simulates a case where we can't retrieve the original transaction
	inHash := GetRandomTxHash()
	txOutItem := TxOutItem{
		Chain:       common.BTCChain,
		InHash:      inHash,
		VaultPubKey: na.PubKeySet.Secp256k1,
		ToAddress:   GetRandomBTCAddress(),
		Coin:        common.NewCoin(common.BTCAsset, cosmos.NewUint(1*common.One)),
		Memo:        fmt.Sprintf("OUT:%s", inHash.String()),
	}

	memo, err := ParseMemoWithTHORNames(ctx, mgr.Keeper(), txOutItem.Memo)
	c.Assert(err, IsNil)

	// Call sendFailedOutboundToTreasury - should fall back to treasury since no original tx
	slasher := newSlasher(mgr.Keeper(), mgr.EventMgr())
	err = slasher.sendFailedOutboundToTreasury(ctx, mgr, txOutItem, vault, memo)
	c.Assert(err, IsNil)

	// Verify a treasury recovery swap was queued (BTC -> RUNE to treasury)
	hasSwap := false
	var queuedSwap MsgSwap

	swapQueueItems := mgr.Keeper().GetSwapQueueIterator(ctx)
	for ; swapQueueItems.Valid(); swapQueueItems.Next() {
		var msg MsgSwap
		if err := mgr.Keeper().Cdc().Unmarshal(swapQueueItems.Value(), &msg); err == nil {
			hasSwap = true
			queuedSwap = msg
			break
		}
	}
	swapQueueItems.Close()

	if !hasSwap {
		advSwapQueueItems := mgr.Keeper().GetAdvSwapQueueItemIterator(ctx)
		for ; advSwapQueueItems.Valid(); advSwapQueueItems.Next() {
			var msg MsgSwap
			if err := mgr.Keeper().Cdc().Unmarshal(advSwapQueueItems.Value(), &msg); err == nil {
				hasSwap = true
				queuedSwap = msg
				break
			}
		}
		advSwapQueueItems.Close()
	}

	c.Assert(hasSwap, Equals, true, Commentf("Expected treasury recovery swap to be queued"))
	c.Assert(queuedSwap.TargetAsset.Equals(common.RuneAsset()), Equals, true,
		Commentf("Treasury recovery should swap to RUNE"))
	c.Assert(queuedSwap.Destination.Equals(common.TreasuryAddress), Equals, true,
		Commentf("Treasury recovery should send to treasury address"))
}

func (s *SlashingSuite) TestLackSigningWithMempoolSwaps(c *C) {
	ctx, mgr := setupManagerForTest(c)
	mgr.Keeper().SetMimir(ctx, constants.EnableAdvSwapQueue.String(), int64(AdvSwapQueueModeEnabled))
	mgr.Keeper().SetMimir(ctx, constants.MempoolSwapCapTOR.String(), 100000000000000)

	// Setup constants
	signingTransPeriod := int64(300)
	maxOutboundAttempts := int64(3)
	mgr.Keeper().SetMimir(ctx, constants.SigningTransactionPeriod.String(), signingTransPeriod)
	mgr.Keeper().SetMimir(ctx, constants.MaxOutboundAttempts.String(), maxOutboundAttempts)

	// Setup vault
	vault := GetRandomVault()
	vault.Type = AsgardVault
	vault.Status = ActiveVault
	vault.Coins = common.Coins{
		common.NewCoin(common.BTCAsset, cosmos.NewUint(100*common.One)),
		common.NewCoin(common.ETHAsset, cosmos.NewUint(100*common.One)),
	}
	c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)

	// Setup pools
	btcPool := NewPool()
	btcPool.Asset = common.BTCAsset
	btcPool.BalanceRune = cosmos.NewUint(1000 * common.One)
	btcPool.BalanceAsset = cosmos.NewUint(1000 * common.One)
	btcPool.Status = PoolAvailable
	c.Assert(mgr.Keeper().SetPool(ctx, btcPool), IsNil)

	ethPool := NewPool()
	ethPool.Asset = common.ETHAsset
	ethPool.BalanceRune = cosmos.NewUint(1000 * common.One)
	ethPool.BalanceAsset = cosmos.NewUint(1000 * common.One)
	ethPool.Status = PoolAvailable
	c.Assert(mgr.Keeper().SetPool(ctx, ethPool), IsNil)

	// Test 1: Mempool swap that never finalized should be rolled back after MaxOutboundAttempts
	inTxID := GetRandomTxHash()

	// Create mempool voter (unfinalized)
	voter := NewObservedTxVoter(inTxID, ObservedTxs{
		ObservedTx{
			Tx: common.Tx{
				ID:    inTxID,
				Chain: common.BTCChain,
				Coins: common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(1*common.One))},
			},
			Status:         common.Status_done,
			BlockHeight:    ctx.BlockHeight(),
			Signers:        []string{},
			FinaliseHeight: 0, // Not finalized
		},
	})
	voter.Tx = voter.Txs[0] // Set consolidated tx for mempool rollback
	voter.UnfinalizedHeight = ctx.BlockHeight()
	voter.FinalisedHeight = 0
	voter.MempoolSwapTOR = cosmos.NewUint(50 * common.One)
	mgr.Keeper().SetObservedTxInVoter(ctx, voter)
	mgr.Keeper().SetMempoolSwapUsageTOR(ctx, voter.MempoolSwapTOR)

	// Create unsigned outbound (mempool swap output).
	// OutHash must be empty (not BlankTxID) for LackSigning to process it,
	// and the outbound must be at height = lackSigningHeight - signingTransPeriod
	// since LackSigning checks TxOut at (currentBlockHeight - signingTransPeriod).
	voterHeight := ctx.BlockHeight()
	lackSigningHeight := voterHeight + (maxOutboundAttempts * signingTransPeriod) + 10
	outboundHeight := lackSigningHeight - signingTransPeriod

	txOutItem := TxOutItem{
		Chain:       common.ETHChain,
		InHash:      inTxID,
		VaultPubKey: vault.PubKey,
		ToAddress:   GetRandomETHAddress(),
		Coin:        common.NewCoin(common.ETHAsset, cosmos.NewUint(5*common.One)),
		Memo:        "OUT:" + inTxID.String(),
	}

	txOut := NewTxOut(outboundHeight)
	txOut.TxArray = append(txOut.TxArray, txOutItem)
	c.Assert(mgr.Keeper().SetTxOut(ctx, txOut), IsNil)

	ctx = ctx.WithBlockHeight(lackSigningHeight)

	// Run LackSigning
	slasher := newSlasher(mgr.Keeper(), mgr.EventMgr())
	err := slasher.LackSigning(ctx, mgr)
	c.Assert(err, IsNil)

	// Verify the mempool swap was rolled back
	updatedTxOut, err := mgr.Keeper().GetTxOut(ctx, outboundHeight)
	c.Assert(err, IsNil)
	c.Check(updatedTxOut.TxArray[0].OutHash, Equals, common.BlankTxID,
		Commentf("Outbound should be marked as handled after rolling back the mempool swap"))
	updatedVoter, err := mgr.Keeper().GetObservedTxInVoter(ctx, inTxID)
	c.Assert(err, IsNil)
	c.Check(updatedVoter.Reverted, Equals, false, Commentf("timeout rollback should leave voter.Reverted false"))
	c.Check(updatedVoter.Actions, IsNil, Commentf("timeout rollback should clear actions"))

	// Test 2: Finalized inbound (traditional flow) should NOT be rolled back
	finalizedTxID := GetRandomTxHash()
	test2VoterHeight := ctx.BlockHeight()
	test2LackSigningHeight := test2VoterHeight + (maxOutboundAttempts * signingTransPeriod) + 10
	finalizedOutHeight := test2LackSigningHeight - signingTransPeriod

	finalizedVoter := NewObservedTxVoter(finalizedTxID, ObservedTxs{
		ObservedTx{
			Tx: common.Tx{
				ID:    finalizedTxID,
				Chain: common.BTCChain,
				Coins: common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(1*common.One))},
			},
			Status:         common.Status_done,
			BlockHeight:    ctx.BlockHeight(),
			Signers:        []string{},
			FinaliseHeight: ctx.BlockHeight(),
		},
	})
	finalizedVoter.FinalisedHeight = ctx.BlockHeight()
	finalizedVoter.UnfinalizedHeight = 0
	mgr.Keeper().SetObservedTxInVoter(ctx, finalizedVoter)

	finalizedTxOutItem := TxOutItem{
		Chain:       common.ETHChain,
		InHash:      finalizedTxID,
		VaultPubKey: vault.PubKey,
		ToAddress:   GetRandomETHAddress(),
		Coin:        common.NewCoin(common.ETHAsset, cosmos.NewUint(5*common.One)),
		Memo:        "OUT:" + finalizedTxID.String(),
	}

	finalizedTxOut := NewTxOut(finalizedOutHeight)
	finalizedTxOut.TxArray = append(finalizedTxOut.TxArray, finalizedTxOutItem)
	c.Assert(mgr.Keeper().SetTxOut(ctx, finalizedTxOut), IsNil)

	ctx = ctx.WithBlockHeight(test2LackSigningHeight)

	err = slasher.LackSigning(ctx, mgr)
	c.Assert(err, IsNil)

	// Verify finalized outbound is NOT rolled back (goes to treasury recovery instead)
	updatedFinalizedTxOut, err := mgr.Keeper().GetTxOut(ctx, finalizedOutHeight)
	c.Assert(err, IsNil)
	c.Check(updatedFinalizedTxOut.TxArray[0].OutHash, Equals, common.BlankTxID,
		Commentf("finalized outbound should be handled by treasury recovery, not mempool rollback"))
	updatedFinalizedVoter, err := mgr.Keeper().GetObservedTxInVoter(ctx, finalizedTxID)
	c.Assert(err, IsNil)
	c.Check(updatedFinalizedVoter.Reverted, Equals, false,
		Commentf("finalized inbound should not set voter.Reverted"))
}

func (s *SlashingSuite) TestLackSigningRetriesFinalizedStagedMempoolSwap(c *C) {
	ctx, mgr := setupManagerForTest(c)

	signingTransPeriod := int64(300)
	maxOutboundAttempts := int64(3)
	mgr.Keeper().SetMimir(ctx, constants.SigningTransactionPeriod.String(), signingTransPeriod)
	mgr.Keeper().SetMimir(ctx, constants.MaxOutboundAttempts.String(), maxOutboundAttempts)

	vault := GetRandomVault()
	vault.Type = AsgardVault
	vault.Status = ActiveVault
	c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)

	inTxID := GetRandomTxHash()
	recipient := GetRandomTHORAddress()
	stagedAmount := cosmos.NewUint(7 * common.One)
	reservedTOR := cosmos.NewUint(123 * common.One)

	// Build a finalized voter that still has staged native outbounds waiting to
	// be finalized. This simulates a mempool swap that succeeded earlier but
	// hit a finalize error and needs a later retry.
	voter := NewObservedTxVoter(inTxID, ObservedTxs{
		ObservedTx{
			Tx: common.Tx{
				ID:    inTxID,
				Chain: common.BTCChain,
				Coins: common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(common.One))},
			},
			Status:         common.Status_done,
			BlockHeight:    ctx.BlockHeight(),
			Signers:        []string{},
			FinaliseHeight: ctx.BlockHeight(),
		},
	})
	voter.Tx = voter.Txs[0]
	voter.UnfinalizedHeight = ctx.BlockHeight() - 1
	voter.FinalisedHeight = ctx.BlockHeight()
	voter.MempoolSwapTOR = reservedTOR

	nativeOutbound := TxOutItem{
		Chain:       common.THORChain,
		InHash:      inTxID,
		ToAddress:   recipient,
		VaultPubKey: vault.PubKey,
		Coin:        common.NewCoin(common.RuneAsset(), stagedAmount),
		Memo:        "OUT:" + inTxID.String(),
	}
	voter.Actions = []TxOutItem{nativeOutbound}

	mgr.Keeper().SetObservedTxInVoter(ctx, voter)
	mgr.Keeper().SetMempoolSwapUsageTOR(ctx, reservedTOR)
	c.Assert(mgr.Keeper().SetStagedOutbound(ctx, nativeOutbound, ctx.BlockHeight()+10), IsNil)

	timeoutHeight := ctx.BlockHeight() + (signingTransPeriod * maxOutboundAttempts) + 10
	ctx = ctx.WithBlockHeight(timeoutHeight)

	slasher := newSlasher(mgr.Keeper(), mgr.EventMgr())
	c.Assert(slasher.LackSigning(ctx, mgr), IsNil)

	// The timeout worker should retry finalization, not roll back finalized
	// mempool swaps.
	stagedItems, err := mgr.Keeper().GetStagedOutbounds(ctx, inTxID)
	c.Assert(err, IsNil)
	c.Check(stagedItems, HasLen, 0, Commentf("finalized staged outbounds should be consumed by finalize retry"))

	updatedVoter, err := mgr.Keeper().GetObservedTxInVoter(ctx, inTxID)
	c.Assert(err, IsNil)
	c.Check(updatedVoter.HasReservedMempoolSwapTOR(), Equals, false, Commentf("finalize retry should release the reserved mempool TOR value"))
	c.Check(updatedVoter.Reverted, Equals, false, Commentf("finalize retry should not mark the voter as reverted"))
	c.Check(mgr.Keeper().GetMempoolSwapUsageTOR(ctx).IsZero(), Equals, true, Commentf("finalize retry should release global mempool swap usage"))

	recipientAcc, err := recipient.AccAddress()
	c.Assert(err, IsNil)
	recipientBalance := mgr.Keeper().GetBalance(ctx, recipientAcc).AmountOf(common.RuneNative.Native())
	c.Check(recipientBalance.Equal(cosmos.NewIntFromBigInt(stagedAmount.BigInt())), Equals, true, Commentf("native finalize retry should pay the staged outbound"))
}

// TestLackSigningForceRollbackStuckFinalizedMempoolSwap verifies that
// LackSigning force-rolls back a finalized active-vault mempool swap whose
// staged outbounds have been stuck beyond 3x the normal timeout. This
// prevents permanent TOR capacity reservation from deterministic finalize
// failures.
func (s *SlashingSuite) TestLackSigningForceRollbackStuckFinalizedMempoolSwap(c *C) {
	ctx, mgr := setupManagerForTest(c)

	signingTransPeriod := int64(300)
	maxOutboundAttempts := int64(3)
	mgr.Keeper().SetMimir(ctx, constants.SigningTransactionPeriod.String(), signingTransPeriod)
	mgr.Keeper().SetMimir(ctx, constants.MaxOutboundAttempts.String(), maxOutboundAttempts)

	vault := GetRandomVault()
	vault.Type = AsgardVault
	vault.Status = ActiveVault
	vault.Coins = common.Coins{
		common.NewCoin(common.BTCAsset, cosmos.NewUint(100*common.One)),
	}
	c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)

	btcPool := NewPool()
	btcPool.Asset = common.BTCAsset
	btcPool.BalanceRune = cosmos.NewUint(1000 * common.One)
	btcPool.BalanceAsset = cosmos.NewUint(1000 * common.One)
	btcPool.Status = PoolAvailable
	c.Assert(mgr.Keeper().SetPool(ctx, btcPool), IsNil)

	inTxID := GetRandomTxHash()
	reservedTOR := cosmos.NewUint(500 * common.One)

	voter := NewObservedTxVoter(inTxID, ObservedTxs{
		ObservedTx{
			Tx: common.Tx{
				ID:    inTxID,
				Chain: common.BTCChain,
				Coins: common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(common.One))},
			},
			Status:         common.Status_done,
			BlockHeight:    ctx.BlockHeight(),
			Signers:        []string{},
			FinaliseHeight: ctx.BlockHeight(),
			ObservedPubKey: vault.PubKey,
		},
	})
	voter.Tx = voter.Txs[0]
	voter.UnfinalizedHeight = ctx.BlockHeight() - 1
	voter.FinalisedHeight = ctx.BlockHeight()
	voter.MempoolSwapTOR = reservedTOR

	stagedOutbound := TxOutItem{
		Chain:       common.THORChain,
		InHash:      inTxID,
		ToAddress:   GetRandomTHORAddress(),
		VaultPubKey: vault.PubKey,
		Coin:        common.NewCoin(common.RuneAsset(), cosmos.NewUint(7*common.One)),
		Memo:        "OUT:" + inTxID.String(),
	}
	voter.Actions = []TxOutItem{stagedOutbound}

	mgr.Keeper().SetObservedTxInVoter(ctx, voter)
	mgr.Keeper().SetMempoolSwapUsageTOR(ctx, reservedTOR)
	c.Assert(mgr.Keeper().SetStagedOutbound(ctx, stagedOutbound, ctx.BlockHeight()+10), IsNil)

	// Advance to 3x the normal timeout (force-rollback threshold)
	timeoutAge := signingTransPeriod * maxOutboundAttempts
	forceRollbackHeight := ctx.BlockHeight() + (3 * timeoutAge) + 10
	ctx = ctx.WithBlockHeight(forceRollbackHeight)

	slasher := newSlasher(mgr.Keeper(), mgr.EventMgr())
	c.Assert(slasher.LackSigning(ctx, mgr), IsNil)

	stagedItems, err := mgr.Keeper().GetStagedOutbounds(ctx, inTxID)
	c.Assert(err, IsNil)
	c.Check(stagedItems, HasLen, 0, Commentf("staged outbounds should be cleared by force rollback"))

	updatedVoter, err := mgr.Keeper().GetObservedTxInVoter(ctx, inTxID)
	c.Assert(err, IsNil)
	c.Check(updatedVoter.HasReservedMempoolSwapTOR(), Equals, false, Commentf("force rollback should release TOR reservation"))
	c.Check(mgr.Keeper().GetMempoolSwapUsageTOR(ctx).IsZero(), Equals, true, Commentf("force rollback should release global mempool swap usage"))

	// The active-vault force-rollback path does not schedule a refund outbound,
	// unlike the inactive-vault sibling path. It must surface that no-refund
	// risk via an explicit security event so monitoring is alerted that user
	// funds are stranded in the active vault and need operator intervention.
	// The generic "mempool swap rolled back" event (emitted on every rollback
	// inside rollbackMempoolSwapAtomic) is not sufficient — a dedicated
	// no-refund message is required for incident response.
	foundForceEvent := false
	for _, evt := range ctx.EventManager().Events() {
		if evt.Type != types2.SecurityEventType {
			continue
		}
		for _, attr := range evt.Attributes {
			if attr.Key == "msg" && strings.Contains(attr.Value, "force-rolled back without refund") {
				foundForceEvent = true
				c.Check(attr.Value, Matches, ".*user inbound funds stranded.*",
					Commentf("force-rollback security event must mention stranded user funds for operator clarity"))
			}
		}
	}
	c.Check(foundForceEvent, Equals, true,
		Commentf("force-rollback path must emit an explicit no-refund security event so operators can intervene"))
}

// TestLackSigningRetriesInactiveVaultFinalizedMempoolSwapRollback verifies
// that LackSigning acts as the periodic reconciler for finalized staged
// mempool swaps whose inbound was observed on an inactive vault. Those swaps
// must be rolled back and refunded on timeout instead of retried through
// staged-outbound finalization.
func (s *SlashingSuite) TestLackSigningRetriesInactiveVaultFinalizedMempoolSwapRollback(c *C) {
	ctx, mgr := setupManagerForTest(c)

	signingTransPeriod := int64(300)
	maxOutboundAttempts := int64(3)
	mgr.Keeper().SetMimir(ctx, constants.SigningTransactionPeriod.String(), signingTransPeriod)
	mgr.Keeper().SetMimir(ctx, constants.MaxOutboundAttempts.String(), maxOutboundAttempts)

	// Mirror the finalized inactive-vault mempool setup from the quorum-path
	// test, but leave the staged outbound in place so the timeout worker has to
	// reconcile it later.
	sourcePool := NewPool()
	sourcePool.Asset = common.BTCAsset
	sourcePool.BalanceRune = cosmos.NewUint(1000 * common.One)
	sourcePool.BalanceAsset = cosmos.NewUint(1000 * common.One)
	sourcePool.Status = PoolAvailable
	c.Assert(mgr.Keeper().SetPool(ctx, sourcePool), IsNil)

	targetPool := NewPool()
	targetPool.Asset = common.ETHAsset
	targetPool.BalanceRune = cosmos.NewUint(1000 * common.One)
	targetPool.BalanceAsset = cosmos.NewUint(1000 * common.One)
	targetPool.Status = PoolAvailable
	c.Assert(mgr.Keeper().SetPool(ctx, targetPool), IsNil)

	activeVault := NewVault(ctx.BlockHeight(), ActiveVault, AsgardVault, GetRandomPubKey(), common.Chains{common.BTCChain, common.ETHChain, common.THORChain}.Strings(), nil)
	activeVault.Coins = common.Coins{
		common.NewCoin(common.BTCAsset, cosmos.NewUint(100*common.One)),
		common.NewCoin(common.ETHAsset, cosmos.NewUint(100*common.One)),
	}
	c.Assert(mgr.Keeper().SetVault(ctx, activeVault), IsNil)

	inactiveVault := NewVault(ctx.BlockHeight(), InactiveVault, AsgardVault, GetRandomPubKey(), common.Chains{common.BTCChain, common.ETHChain}.Strings(), nil)
	inactiveVault.Coins = common.Coins{
		common.NewCoin(common.BTCAsset, cosmos.NewUint(10*common.One)),
	}
	c.Assert(mgr.Keeper().SetVault(ctx, inactiveVault), IsNil)

	toAddr, err := inactiveVault.PubKey.GetAddress(common.BTCChain)
	c.Assert(err, IsNil)

	inTxID := GetRandomTxHash()
	inboundTx := common.NewTx(
		inTxID,
		GetRandomBTCAddress(),
		toAddr,
		common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(1*common.One))},
		common.Gas{common.NewCoin(common.BTCAsset, cosmos.OneUint())},
		"SWAP:ETH.ETH:"+GetRandomETHAddress().String(),
	)
	finalizedTx := NewObservedTx(inboundTx, 12, inactiveVault.PubKey, 12)

	voter := NewObservedTxVoter(inTxID, ObservedTxs{finalizedTx})
	voter.Tx = finalizedTx
	voter.Height = ctx.BlockHeight()
	voter.FinalisedHeight = ctx.BlockHeight()
	voter.MempoolSwapTOR = cosmos.NewUint(50 * common.One)

	stagedOutbound := TxOutItem{
		Chain:       common.ETHChain,
		InHash:      inTxID,
		VaultPubKey: activeVault.PubKey,
		ToAddress:   GetRandomETHAddress(),
		Coin:        common.NewCoin(common.ETHAsset, cosmos.NewUint(3*common.One)),
		Memo:        "OUT:" + inTxID.String(),
	}
	voter.Actions = []TxOutItem{stagedOutbound}

	// Seed the orphaned finalized state: the inbound is already finalized, a
	// staged outbound still exists, and mempool capacity is still reserved. The
	// timeout worker should unwind this back to a refund instead of retrying
	// staged finalization.
	mgr.Keeper().SetObservedTxInVoter(ctx, voter)
	mgr.Keeper().SetMempoolSwapUsageTOR(ctx, voter.MempoolSwapTOR)
	c.Assert(mgr.Keeper().SetStagedOutbound(ctx, stagedOutbound, ctx.BlockHeight()+10), IsNil)

	timeoutHeight := ctx.BlockHeight() + (signingTransPeriod * maxOutboundAttempts) + 10
	ctx = ctx.WithBlockHeight(timeoutHeight)

	slasher := newSlasher(mgr.Keeper(), mgr.EventMgr())
	c.Assert(slasher.LackSigning(ctx, mgr), IsNil)

	// The timeout path should clear the speculative staged state and replace it
	// with a normal refund outbound/action.
	stagedItems, err := mgr.Keeper().GetStagedOutbounds(ctx, inTxID)
	c.Assert(err, IsNil)
	c.Check(stagedItems, HasLen, 0, Commentf("inactive-vault timeout recovery should clear staged outbounds"))

	updatedVoter, err := mgr.Keeper().GetObservedTxInVoter(ctx, inTxID)
	c.Assert(err, IsNil)
	c.Check(updatedVoter.Reverted, Equals, false, Commentf("inactive-vault timeout recovery should normalize the voter"))
	c.Check(updatedVoter.HasReservedMempoolSwapTOR(), Equals, false, Commentf("inactive-vault timeout recovery should clear the voter mempool TOR value"))
	c.Check(mgr.Keeper().GetMempoolSwapUsageTOR(ctx).IsZero(), Equals, true, Commentf("inactive-vault timeout recovery should release mempool swap capacity"))

	_, err = mgr.Keeper().GetSwapQueueItem(ctx, CreateMempoolRollbackTxID(inTxID, 0), 0)
	c.Assert(err, NotNil, Commentf("inactive-vault timeout recovery should settle the reverse swap inline"))

	outItems, err := mgr.TxOutStore().GetOutboundItems(ctx)
	c.Assert(err, IsNil)
	c.Assert(outItems, HasLen, 1, Commentf("inactive-vault timeout recovery should schedule exactly one refund outbound"))
	c.Assert(updatedVoter.Actions, HasLen, 1, Commentf("inactive-vault timeout recovery should replace staged actions with the scheduled refund action"))
	c.Check(outItems[0].Coin.Asset.Equals(common.BTCAsset), Equals, true)
	c.Check(outItems[0].Coin.Amount.GT(cosmos.ZeroUint()), Equals, true)
	c.Check(outItems[0].Coin.Amount.LTE(cosmos.NewUint(1*common.One)), Equals, true)
	c.Check(outItems[0].ToAddress.Equals(inboundTx.FromAddress), Equals, true)
	c.Check(outItems[0].GetMemo(), Equals, NewRefundMemo(inTxID).String())
	c.Check(updatedVoter.Actions[0].GetMemo(), Equals, NewRefundMemo(inTxID).String())
}

// TestLackSigningRetriesInactiveVaultFinalizedQueuedMempoolSwapRollback verifies
// that LackSigning also recovers finalized inactive-vault mempool swaps that
// are stranded only in queue state, without any staged outbound left to drive
// the existing timeout retry path.
func (s *SlashingSuite) TestLackSigningRetriesInactiveVaultFinalizedQueuedMempoolSwapRollback(c *C) {
	ctx, mgr := setupManagerForTest(c)

	signingTransPeriod := int64(300)
	maxOutboundAttempts := int64(3)
	mgr.Keeper().SetMimir(ctx, constants.SigningTransactionPeriod.String(), signingTransPeriod)
	mgr.Keeper().SetMimir(ctx, constants.MaxOutboundAttempts.String(), maxOutboundAttempts)

	sourcePool := NewPool()
	sourcePool.Asset = common.BTCAsset
	sourcePool.BalanceRune = cosmos.NewUint(1000 * common.One)
	sourcePool.BalanceAsset = cosmos.NewUint(1000 * common.One)
	sourcePool.Status = PoolAvailable
	c.Assert(mgr.Keeper().SetPool(ctx, sourcePool), IsNil)

	targetPool := NewPool()
	targetPool.Asset = common.ETHAsset
	targetPool.BalanceRune = cosmos.NewUint(1000 * common.One)
	targetPool.BalanceAsset = cosmos.NewUint(1000 * common.One)
	targetPool.Status = PoolAvailable
	c.Assert(mgr.Keeper().SetPool(ctx, targetPool), IsNil)

	activeVault := NewVault(ctx.BlockHeight(), ActiveVault, AsgardVault, GetRandomPubKey(), common.Chains{common.BTCChain, common.ETHChain, common.THORChain}.Strings(), nil)
	activeVault.Coins = common.Coins{
		common.NewCoin(common.BTCAsset, cosmos.NewUint(100*common.One)),
		common.NewCoin(common.ETHAsset, cosmos.NewUint(100*common.One)),
	}
	c.Assert(mgr.Keeper().SetVault(ctx, activeVault), IsNil)

	inactiveVault := NewVault(ctx.BlockHeight(), InactiveVault, AsgardVault, GetRandomPubKey(), common.Chains{common.BTCChain, common.ETHChain}.Strings(), nil)
	inactiveVault.Coins = common.Coins{
		common.NewCoin(common.BTCAsset, cosmos.NewUint(10*common.One)),
	}
	c.Assert(mgr.Keeper().SetVault(ctx, inactiveVault), IsNil)

	na := GetRandomValidatorNode(NodeActive)
	c.Assert(mgr.Keeper().SetNodeAccount(ctx, na), IsNil)

	toAddr, err := inactiveVault.PubKey.GetAddress(common.BTCChain)
	c.Assert(err, IsNil)

	inTxID := GetRandomTxHash()
	inboundTx := common.NewTx(
		inTxID,
		GetRandomBTCAddress(),
		toAddr,
		common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(1*common.One))},
		common.Gas{common.NewCoin(common.BTCAsset, cosmos.OneUint())},
		"SWAP:ETH.ETH:"+GetRandomETHAddress().String(),
	)
	finalizedTx := NewObservedTx(inboundTx, 12, inactiveVault.PubKey, 12)

	voter := NewObservedTxVoter(inTxID, ObservedTxs{finalizedTx})
	voter.Tx = finalizedTx
	voter.Height = ctx.BlockHeight()
	voter.UnfinalizedHeight = ctx.BlockHeight()
	voter.FinalisedHeight = ctx.BlockHeight()
	voter.MempoolSwapTOR = cosmos.NewUint(50 * common.One)

	// Build the queued-only orphaned state from the review note: the inbound
	// already finalized on an inactive vault, part of the swap progressed into
	// advanced-queue state, but no staged outbound remains to drive the
	// existing timeout recovery path.
	queuedSwap := NewMsgSwap(
		inboundTx,
		common.ETHAsset,
		GetRandomETHAddress(),
		cosmos.ZeroUint(),
		common.NoAddress,
		cosmos.ZeroUint(),
		"",
		"",
		nil,
		MarketSwap,
		1,
		0,
		types2.SwapVersion_v2,
		na.NodeAddress,
	)
	queuedSwap.State.Out = cosmos.NewUint(3 * common.One)
	queuedSwap.State.In = queuedSwap.Tx.Coins[0].Amount

	mgr.Keeper().SetObservedTxInVoter(ctx, voter)
	mgr.Keeper().SetMempoolSwapUsageTOR(ctx, voter.MempoolSwapTOR)
	c.Assert(mgr.Keeper().SetAdvSwapQueueItem(ctx, *queuedSwap), IsNil)
	c.Check(mgr.Keeper().HasStagedOutbound(ctx, inTxID), Equals, false, Commentf("queued recovery fixture should not rely on staged outbounds"))

	timeoutHeight := ctx.BlockHeight() + (signingTransPeriod * maxOutboundAttempts) + 10
	if rem := timeoutHeight % signingTransPeriod; rem != 0 {
		timeoutHeight += signingTransPeriod - rem
	}
	ctx = ctx.WithBlockHeight(timeoutHeight)

	slasher := newSlasher(mgr.Keeper(), mgr.EventMgr())
	c.Assert(slasher.LackSigning(ctx, mgr), IsNil)

	// The timeout reconciler should treat this the same as any other inactive-
	// vault mempool cleanup: remove speculative queue state, release reserved
	// capacity, and replace the temporary action with a real refund outbound.
	_, err = mgr.Keeper().GetAdvSwapQueueItem(ctx, inTxID, 0)
	c.Assert(err, NotNil, Commentf("queued inactive-vault timeout recovery should remove the pending advanced-queue swap"))

	updatedVoter, err := mgr.Keeper().GetObservedTxInVoter(ctx, inTxID)
	c.Assert(err, IsNil)
	c.Check(updatedVoter.Reverted, Equals, false, Commentf("queued inactive-vault timeout recovery should normalize the voter"))
	c.Check(updatedVoter.HasReservedMempoolSwapTOR(), Equals, false, Commentf("queued inactive-vault timeout recovery should clear the voter mempool TOR value"))
	c.Check(mgr.Keeper().GetMempoolSwapUsageTOR(ctx).IsZero(), Equals, true, Commentf("queued inactive-vault timeout recovery should release mempool swap capacity"))

	outItems, err := mgr.TxOutStore().GetOutboundItems(ctx)
	c.Assert(err, IsNil)
	c.Assert(outItems, HasLen, 1, Commentf("queued inactive-vault timeout recovery should schedule exactly one refund outbound"))
	c.Assert(updatedVoter.Actions, HasLen, 1, Commentf("queued inactive-vault timeout recovery should replace queued speculative state with the scheduled refund action"))
	c.Check(outItems[0].Coin.Asset.Equals(common.BTCAsset), Equals, true)
	c.Check(outItems[0].Coin.Amount.GT(cosmos.ZeroUint()), Equals, true)
	c.Check(outItems[0].Coin.Amount.LTE(cosmos.NewUint(1*common.One)), Equals, true)
	c.Check(outItems[0].ToAddress.Equals(inboundTx.FromAddress), Equals, true)
	c.Check(outItems[0].GetMemo(), Equals, NewRefundMemo(inTxID).String())
	c.Check(updatedVoter.Actions[0].GetMemo(), Equals, NewRefundMemo(inTxID).String())
}

// TestCollectTimedOutInactiveVaultMempoolQueueHashesDedupesSameInbound verifies
// the queued-timeout collector only returns one retry hash even if multiple
// advanced-queue rows share the same inbound txid.
func (s *SlashingSuite) TestCollectTimedOutInactiveVaultMempoolQueueHashesDedupesSameInbound(c *C) {
	ctx, mgr := setupManagerForTest(c)

	timeoutAge := int64(50)

	inactiveVault := NewVault(ctx.BlockHeight(), InactiveVault, AsgardVault, GetRandomPubKey(), common.Chains{common.BTCChain, common.ETHChain}.Strings(), nil)
	inactiveVault.Coins = common.Coins{
		common.NewCoin(common.BTCAsset, cosmos.NewUint(10*common.One)),
	}
	c.Assert(mgr.Keeper().SetVault(ctx, inactiveVault), IsNil)

	na := GetRandomValidatorNode(NodeActive)
	c.Assert(mgr.Keeper().SetNodeAccount(ctx, na), IsNil)

	toAddr, err := inactiveVault.PubKey.GetAddress(common.BTCChain)
	c.Assert(err, IsNil)

	inTxID := GetRandomTxHash()
	inboundTx := common.NewTx(
		inTxID,
		GetRandomBTCAddress(),
		toAddr,
		common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(1*common.One))},
		common.Gas{common.NewCoin(common.BTCAsset, cosmos.OneUint())},
		"SWAP:ETH.ETH:"+GetRandomETHAddress().String(),
	)
	finalizedTx := NewObservedTx(inboundTx, 12, inactiveVault.PubKey, 12)

	voter := NewObservedTxVoter(inTxID, ObservedTxs{finalizedTx})
	voter.Tx = finalizedTx
	voter.Height = ctx.BlockHeight()
	voter.UnfinalizedHeight = ctx.BlockHeight()
	voter.FinalisedHeight = ctx.BlockHeight()
	voter.MempoolSwapTOR = cosmos.NewUint(50 * common.One)
	mgr.Keeper().SetObservedTxInVoter(ctx, voter)

	queuedSwap0 := NewMsgSwap(
		inboundTx,
		common.ETHAsset,
		GetRandomETHAddress(),
		cosmos.ZeroUint(),
		common.NoAddress,
		cosmos.ZeroUint(),
		"",
		"",
		nil,
		MarketSwap,
		1,
		0,
		types2.SwapVersion_v2,
		na.NodeAddress,
	)
	queuedSwap0.State.Out = cosmos.NewUint(3 * common.One)
	queuedSwap0.State.In = queuedSwap0.Tx.Coins[0].Amount
	c.Assert(mgr.Keeper().SetAdvSwapQueueItem(ctx, *queuedSwap0), IsNil)

	queuedSwap1 := NewMsgSwap(
		inboundTx,
		common.ETHAsset,
		GetRandomETHAddress(),
		cosmos.ZeroUint(),
		common.NoAddress,
		cosmos.ZeroUint(),
		"",
		"",
		nil,
		MarketSwap,
		1,
		0,
		types2.SwapVersion_v2,
		na.NodeAddress,
	)
	queuedSwap1.Index = 1
	queuedSwap1.State.Out = cosmos.NewUint(1 * common.One)
	queuedSwap1.State.In = queuedSwap1.Tx.Coins[0].Amount
	c.Assert(mgr.Keeper().SetAdvSwapQueueItem(ctx, *queuedSwap1), IsNil)

	ctx = ctx.WithBlockHeight(voter.FinalisedHeight + timeoutAge + 1)

	slasher := newSlasher(mgr.Keeper(), mgr.EventMgr())
	hashes := slasher.collectTimedOutInactiveVaultMempoolQueueHashes(ctx, mgr.Keeper().GetAdvSwapQueueItemIterator(ctx), timeoutAge)

	c.Assert(hashes, HasLen, 1)
	c.Check(hashes[0].Equals(inTxID), Equals, true)
}
