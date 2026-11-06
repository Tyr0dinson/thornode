package thorchain

import (
	"errors"
	"fmt"

	dbm "github.com/cosmos/cosmos-db"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"
)

type HandlerAddLiquiditySuite struct{}

var _ = Suite(&HandlerAddLiquiditySuite{})

type MockAddLiquidityKeeper struct {
	keeper.KVStoreDummy
	currentPool       Pool
	activeNodeAccount NodeAccount
	failGetPool       bool
	lp                LiquidityProvider
	pol               ProtocolOwnedLiquidity
	polAddress        common.Address
}

func (m *MockAddLiquidityKeeper) PoolExist(_ cosmos.Context, asset common.Asset) bool {
	return m.currentPool.Asset.Equals(asset)
}

func (m *MockAddLiquidityKeeper) GetPools(_ cosmos.Context) (Pools, error) {
	return Pools{m.currentPool}, nil
}

func (m *MockAddLiquidityKeeper) GetPool(_ cosmos.Context, _ common.Asset) (Pool, error) {
	if m.failGetPool {
		return Pool{}, errors.New("fail to get pool")
	}
	return m.currentPool, nil
}

func (m *MockAddLiquidityKeeper) SetPool(_ cosmos.Context, pool Pool) error {
	m.currentPool = pool
	return nil
}

func (m *MockAddLiquidityKeeper) GetModuleAddress(mod string) (common.Address, error) {
	return m.polAddress, nil
}

func (m *MockAddLiquidityKeeper) GetPOL(_ cosmos.Context) (ProtocolOwnedLiquidity, error) {
	return m.pol, nil
}

func (m *MockAddLiquidityKeeper) SetPOL(_ cosmos.Context, pol ProtocolOwnedLiquidity) error {
	m.pol = pol
	return nil
}

func (m *MockAddLiquidityKeeper) ListValidatorsWithBond(_ cosmos.Context) (NodeAccounts, error) {
	return NodeAccounts{m.activeNodeAccount}, nil
}

func (m *MockAddLiquidityKeeper) ListActiveValidators(_ cosmos.Context) (NodeAccounts, error) {
	return NodeAccounts{m.activeNodeAccount}, nil
}

func (m *MockAddLiquidityKeeper) TotalActiveValidators(_ cosmos.Context) (int, error) {
	return 1, nil
}

func (m *MockAddLiquidityKeeper) GetNodeAccount(_ cosmos.Context, addr cosmos.AccAddress) (NodeAccount, error) {
	if m.activeNodeAccount.NodeAddress.Equals(addr) {
		return m.activeNodeAccount, nil
	}
	return NodeAccount{}, errors.New("not exist")
}

func (m *MockAddLiquidityKeeper) GetLiquidityProvider(_ cosmos.Context, asset common.Asset, addr common.Address) (LiquidityProvider, error) {
	return m.lp, nil
}

func (m *MockAddLiquidityKeeper) SetLiquidityProvider(ctx cosmos.Context, lp LiquidityProvider) {
	m.lp = lp
}

func (m *MockAddLiquidityKeeper) AddOwnership(ctx cosmos.Context, coin common.Coin, _ cosmos.AccAddress) error {
	m.lp.Units = m.lp.Units.Add(coin.Amount)
	return nil
}

func (m *MockAddLiquidityKeeper) GetAsgardVaults(_ cosmos.Context) (Vaults, error) {
	return Vaults{
		{
			Status: ActiveVault,
			Type:   AsgardVault,
			Coins: common.NewCoins(
				common.NewCoin(m.currentPool.Asset, m.currentPool.BalanceAsset),
			),
		},
	}, nil
}

func (m *MockAddLiquidityKeeper) GetAsgardVaultsByStatus(_ cosmos.Context, status VaultStatus) (Vaults, error) {
	// Return a vault that supports the current pool's chain with the active node as a member
	vault := GetRandomVault()
	vault.Status = status
	vault.Type = AsgardVault
	vault.Chains = []string{m.currentPool.Asset.GetChain().String()}
	vault.Membership = []string{m.activeNodeAccount.PubKeySet.Secp256k1.String()}
	vault.Coins = common.NewCoins(
		common.NewCoin(m.currentPool.Asset, m.currentPool.BalanceAsset),
	)

	if status == ActiveVault {
		return Vaults{vault}, nil
	}
	// Return empty for retiring vaults in these tests
	return Vaults{}, nil
}

func (s *HandlerAddLiquiditySuite) SetUpSuite(c *C) {
	SetupConfigForTest()
}

func (s *HandlerAddLiquiditySuite) TestAddLiquidityHandler(c *C) {
	var err error
	ctx, mgr := setupManagerForTest(c)
	nodeAccount := GetRandomValidatorNode(NodeActive)
	c.Assert(mgr.K.SetNodeAccount(ctx, nodeAccount), IsNil)

	vault := GetRandomVault()
	vault.Chains = append(vault.Chains, common.ETHChain.String())
	err = mgr.K.SetVault(ctx, vault)
	c.Assert(err, IsNil)

	FundModule(c, ctx, mgr.K, BondName, nodeAccount.Bond.Uint64())

	pool := NewPool()
	pool.Asset = common.ETHAsset
	pool.Status = PoolAvailable
	err = mgr.K.SetPool(ctx, pool)
	c.Assert(err, IsNil)

	runeAddr := GetRandomRUNEAddress()
	ethAddr := GetRandomETHAddress()

	addHandler := NewAddLiquidityHandler(mgr)
	secHandler := NewSecuredAssetDepositHandler(mgr)

	_, err = addHandler.Run(ctx, NewMsgAddLiquidity(
		GetRandomTx(),
		common.ETHAsset,
		cosmos.NewUint(100*common.One),
		cosmos.ZeroUint(),
		runeAddr,
		ethAddr,
		common.NoAddress,
		cosmos.ZeroUint(),
		nodeAccount.NodeAddress,
	))
	c.Assert(err, IsNil)

	midLiquidityPool, err := mgr.Keeper().GetPool(ctx, common.ETHAsset)
	c.Assert(err, IsNil)
	c.Assert(midLiquidityPool.PendingInboundRune.String(), Equals, "10000000000")

	// add ETH
	_, err = addHandler.Run(ctx, NewMsgAddLiquidity(
		GetRandomTx(),
		common.ETHAsset,
		cosmos.ZeroUint(),
		cosmos.NewUint(100*common.One),
		runeAddr,
		ethAddr,
		common.NoAddress,
		cosmos.ZeroUint(),
		nodeAccount.NodeAddress,
	))
	c.Assert(err, IsNil)

	postLiquidityPool, err := mgr.Keeper().GetPool(ctx, common.ETHAsset)
	c.Assert(err, IsNil)
	c.Assert(postLiquidityPool.BalanceAsset.String(), Equals, "10000000000")
	c.Assert(postLiquidityPool.BalanceRune.String(), Equals, "10000000000")
	c.Assert(postLiquidityPool.PendingInboundAsset.String(), Equals, "0")
	c.Assert(postLiquidityPool.PendingInboundRune.String(), Equals, "0")

	pol, err := mgr.Keeper().GetPOL(ctx)
	c.Assert(err, IsNil)
	c.Check(pol.RuneDeposited.Uint64(), Equals, uint64(0))

	// Secured asset deposit
	// NOTE: needs to be a different than the one that deposited L1 assets
	contractAddr := GetRandomRUNEAddress()
	accAddr, err := contractAddr.AccAddress()
	c.Assert(err, IsNil)

	_, err = secHandler.Run(ctx, NewMsgSecuredAssetDeposit(
		common.ETHAsset,
		cosmos.NewUint(common.One*100),
		accAddr,
		accAddr,
		GetRandomTx(),
	))
	c.Assert(err, IsNil)

	balance := mgr.SecuredAssetManager().BalanceOf(ctx, common.ETHAsset, accAddr)
	c.Assert(balance.String(), Equals, "10000000000")

	tx := GetRandomTx()
	tx.Coins = common.NewCoins(common.NewCoin(common.RuneAsset(), cosmos.NewUint(10000)))

	// add RUNE
	// For secured assets, signer must match runeAddr to prevent unauthorized withdrawals
	_, err = addHandler.Run(ctx, NewMsgAddLiquidity(
		tx,
		common.ETHAsset.GetSecuredAsset(),
		cosmos.NewUint(50*common.One),
		cosmos.ZeroUint(),
		contractAddr,
		contractAddr,
		common.NoAddress,
		cosmos.ZeroUint(),
		accAddr,
	))
	c.Assert(err, IsNil)

	pool, err = mgr.K.GetPool(ctx, common.ETHAsset)
	c.Assert(err, IsNil)

	c.Assert(pool.PendingInboundRune.String(), Equals, "5000000000")

	// add secured ETH (ETH-ETH)
	_, err = addHandler.Run(ctx, NewMsgAddLiquidity(
		tx,
		common.ETHAsset.GetSecuredAsset(),
		cosmos.ZeroUint(),
		cosmos.NewUint(50*common.One),
		contractAddr,
		contractAddr,
		common.NoAddress,
		cosmos.ZeroUint(),
		accAddr,
	))
	c.Assert(err, IsNil)

	balance = mgr.SecuredAssetManager().BalanceOf(ctx, common.ETHAsset, accAddr)
	c.Assert(balance.String(), Equals, "5000000000")

	pool, err = mgr.K.GetPool(ctx, common.ETHAsset)
	c.Assert(err, IsNil)
	c.Assert(pool.BalanceAsset.String(), Equals, "15000000000")
	c.Assert(pool.BalanceRune.String(), Equals, "15000000000")
	c.Assert(pool.PendingInboundAsset.String(), Equals, "0")
	c.Assert(pool.PendingInboundRune.String(), Equals, "0")

	// add secured ETH + RUNE
	_, err = addHandler.Run(ctx, NewMsgAddLiquidity(
		tx,
		common.ETHAsset.GetSecuredAsset(),
		cosmos.NewUint(10*common.One),
		cosmos.NewUint(10*common.One),
		contractAddr,
		contractAddr,
		common.NoAddress,
		cosmos.ZeroUint(),
		accAddr,
	))
	c.Assert(err, IsNil)

	balance = mgr.SecuredAssetManager().BalanceOf(ctx, common.ETHAsset, accAddr)
	c.Assert(balance.String(), Equals, "4000000000")

	pool, err = mgr.K.GetPool(ctx, common.ETHAsset)
	c.Assert(err, IsNil)
	c.Assert(pool.BalanceAsset.String(), Equals, "16000000000")
	c.Assert(pool.BalanceRune.String(), Equals, "16000000000")
	c.Assert(pool.PendingInboundAsset.String(), Equals, "0")
	c.Assert(pool.PendingInboundRune.String(), Equals, "0")

	// add nothing (AssetAmount is zero so authorization check is skipped)
	_, err = addHandler.Run(ctx, NewMsgAddLiquidity(
		tx,
		common.ETHAsset.GetSecuredAsset(),
		cosmos.ZeroUint(),
		cosmos.ZeroUint(),
		contractAddr,
		contractAddr,
		common.NoAddress,
		cosmos.ZeroUint(),
		accAddr,
	))
	c.Assert(err, IsNil)

	balance = mgr.SecuredAssetManager().BalanceOf(ctx, common.ETHAsset, accAddr)
	c.Assert(balance.String(), Equals, "4000000000")

	pool, err = mgr.K.GetPool(ctx, common.ETHAsset)
	c.Assert(err, IsNil)
	c.Assert(pool.BalanceAsset.String(), Equals, "16000000000")
	c.Assert(pool.BalanceRune.String(), Equals, "16000000000")
	c.Assert(pool.PendingInboundAsset.String(), Equals, "0")
	c.Assert(pool.PendingInboundRune.String(), Equals, "0")

	// provide wrong params

	// wrong asset address
	_, err = addHandler.Run(ctx, NewMsgAddLiquidity(
		tx,
		common.ETHAsset.GetSecuredAsset(),
		cosmos.NewUint(common.One),
		cosmos.ZeroUint(),
		contractAddr,
		GetRandomETHAddress(),
		common.NoAddress,
		cosmos.ZeroUint(),
		nodeAccount.NodeAddress,
	))
	c.Assert(err, NotNil)

	// no asset address
	_, err = addHandler.Run(ctx, NewMsgAddLiquidity(
		tx,
		common.ETHAsset.GetSecuredAsset(),
		cosmos.NewUint(common.One),
		cosmos.ZeroUint(),
		contractAddr,
		common.NoAddress,
		common.NoAddress,
		cosmos.ZeroUint(),
		nodeAccount.NodeAddress,
	))
	c.Assert(err, NotNil)
}

func (s *HandlerAddLiquiditySuite) TestAddLiquidityHandler_NoPool_ShouldCreateNewPool(c *C) {
	activeNodeAccount := GetRandomValidatorNode(NodeActive)
	activeNodeAccount.Bond = cosmos.NewUint(1000000 * common.One)
	activeNodeAccount.SupportedChains = []string{common.ETHChain.String()}
	runeAddr := GetRandomRUNEAddress()
	ethAddr := GetRandomETHAddress()
	pool := NewPool()
	pool.Status = PoolAvailable
	k := &MockAddLiquidityKeeper{
		activeNodeAccount: activeNodeAccount,
		currentPool:       pool,
		lp: LiquidityProvider{
			Asset:             common.ETHAsset,
			RuneAddress:       runeAddr,
			AssetAddress:      ethAddr,
			Units:             cosmos.ZeroUint(),
			PendingRune:       cosmos.ZeroUint(),
			PendingAsset:      cosmos.ZeroUint(),
			RuneDepositValue:  cosmos.ZeroUint(),
			AssetDepositValue: cosmos.ZeroUint(),
		},
	}
	// happy path
	ctx, mgr := setupManagerForTest(c)
	mgr.K = k
	addHandler := NewAddLiquidityHandler(mgr)
	preLiquidityPool, err := k.GetPool(ctx, common.ETHAsset)
	c.Assert(err, IsNil)
	c.Assert(preLiquidityPool.IsEmpty(), Equals, true)
	addTxHash := GetRandomTxHash()
	tx := common.NewTx(
		addTxHash,
		runeAddr,
		GetRandomETHAddress(),
		common.Coins{common.NewCoin(common.ETHAsset, cosmos.NewUint(common.One*5))},
		common.Gas{
			common.NewCoin(common.ETHAsset, cosmos.NewUint(10000)),
		},
		"add:ETH",
	)
	mgr.constAccessor = constants.NewDummyConstants(map[constants.ConstantName]int64{
		constants.MaximumLiquidityRune: 600_000_00000000,
	}, map[constants.ConstantName]bool{
		constants.StrictBondLiquidityRatio: true,
	}, map[constants.ConstantName]string{})

	msg := NewMsgAddLiquidity(
		tx,
		common.ETHAsset,
		cosmos.NewUint(100*common.One),
		cosmos.NewUint(100*common.One),
		runeAddr,
		ethAddr,
		common.NoAddress, cosmos.ZeroUint(),
		activeNodeAccount.NodeAddress,
	)
	_, err = addHandler.Run(ctx, msg)
	c.Assert(err, IsNil)
	postLiquidityPool, err := k.GetPool(ctx, common.ETHAsset)
	c.Assert(err, IsNil)
	c.Assert(postLiquidityPool.BalanceAsset.String(), Equals, preLiquidityPool.BalanceAsset.Add(msg.AssetAmount).String())
	c.Assert(postLiquidityPool.BalanceRune.String(), Equals, preLiquidityPool.BalanceRune.Add(msg.RuneAmount).String())
}

func (s *HandlerAddLiquiditySuite) TestAddLiquidityHandler_NoPool_ShouldCreateNewPoolSecured(c *C) {
	var err error
	ctx, mgr := setupManagerForTest(c)
	nodeAccount := GetRandomValidatorNode(NodeActive)
	c.Assert(mgr.K.SetNodeAccount(ctx, nodeAccount), IsNil)

	vault := GetRandomVault()
	vault.Chains = append(vault.Chains, common.BTCChain.String())
	err = mgr.K.SetVault(ctx, vault)
	c.Assert(err, IsNil)

	FundModule(c, ctx, mgr.K, BondName, nodeAccount.Bond.Uint64())

	pool := NewPool()
	pool.Asset = common.BTCAsset
	pool.Status = PoolAvailable
	err = mgr.K.SetPool(ctx, pool)
	c.Assert(err, IsNil)

	runeAddr := GetRandomRUNEAddress()

	addHandler := NewAddLiquidityHandler(mgr)
	secHandler := NewSecuredAssetDepositHandler(mgr)

	preLiquidityPool, err := mgr.K.GetPool(ctx, common.BTCAsset)
	c.Assert(err, IsNil)
	c.Assert(preLiquidityPool.Asset, Equals, common.BTCAsset)
	c.Assert(preLiquidityPool.BalanceAsset.String(), Equals, "0")
	c.Assert(preLiquidityPool.BalanceRune.String(), Equals, "0")

	tx := GetRandomTx()
	tx.Coins = common.NewCoins(common.NewCoin(common.RuneAsset(), cosmos.NewUint(10000)))

	accAddr, err := runeAddr.AccAddress()
	c.Assert(err, IsNil)

	_, err = secHandler.Run(ctx, NewMsgSecuredAssetDeposit(
		common.BTCAsset,
		cosmos.NewUint(common.One*2),
		accAddr,
		accAddr,
		tx,
	))
	c.Assert(err, IsNil)

	mgr.constAccessor = constants.NewDummyConstants(map[constants.ConstantName]int64{
		constants.MaximumLiquidityRune: 600_000_00000000,
	}, map[constants.ConstantName]bool{
		constants.StrictBondLiquidityRatio: true,
	}, map[constants.ConstantName]string{})

	// For secured assets, signer must match runeAddr to prevent unauthorized withdrawals
	msg := NewMsgAddLiquidity(
		tx,
		common.BTCAsset.GetSecuredAsset(),
		cosmos.NewUint(1000*common.One),
		cosmos.NewUint(1*common.One),
		runeAddr,
		runeAddr,
		common.NoAddress,
		cosmos.ZeroUint(),
		accAddr,
	)

	_, err = addHandler.Run(ctx, msg)
	c.Assert(err, IsNil)
	postLiquidityPool, err := mgr.K.GetPool(ctx, common.BTCAsset)
	c.Assert(err, IsNil)
	c.Assert(postLiquidityPool.BalanceAsset.String(), Equals, preLiquidityPool.BalanceAsset.Add(msg.AssetAmount).String())
	c.Assert(postLiquidityPool.BalanceRune.String(), Equals, preLiquidityPool.BalanceRune.Add(msg.RuneAmount).String())

	balance := mgr.SecuredAssetManager().BalanceOf(ctx, common.BTCAsset, accAddr)
	c.Assert(balance.String(), Equals, "100000000")
}

func (s *HandlerAddLiquiditySuite) TestAddLiquidityHandlerValidation(c *C) {
	ctx, _ := setupKeeperForTest(c)
	activeNodeAccount := GetRandomValidatorNode(NodeActive)
	runeAddr := GetRandomRUNEAddress()
	ethAddr := GetRandomETHAddress()
	ethSynthAsset, _ := common.NewAsset("ETH/ETH")
	mainnetBTCAddr, err := common.NewAddress("bc1qy0tj9fh0u6fgz0mejjp6776z6kugych0zwrkwr")
	c.Assert(err, IsNil)
	tx := common.NewTx(
		GetRandomTxHash(),
		GetRandomRUNEAddress(),
		GetRandomRUNEAddress(),
		common.Coins{common.NewCoin(ethSynthAsset, cosmos.NewUint(common.One*5))},
		common.Gas{
			{Asset: common.RuneNative, Amount: cosmos.NewUint(1 * common.One)},
		},
		"add:ETH.ETH",
	)

	k := &MockAddLiquidityKeeper{
		activeNodeAccount: activeNodeAccount,
		currentPool: Pool{
			BalanceRune:  cosmos.ZeroUint(),
			BalanceAsset: cosmos.ZeroUint(),
			Asset:        common.ETHAsset,
			LPUnits:      cosmos.ZeroUint(),
			Status:       PoolAvailable,
		},
		lp: LiquidityProvider{
			Asset:             common.ETHAsset,
			RuneAddress:       runeAddr,
			AssetAddress:      ethAddr,
			Units:             cosmos.ZeroUint(),
			PendingRune:       cosmos.ZeroUint(),
			PendingAsset:      cosmos.ZeroUint(),
			RuneDepositValue:  cosmos.ZeroUint(),
			AssetDepositValue: cosmos.ZeroUint(),
		},
	}
	testCases := []struct {
		name           string
		msg            *MsgAddLiquidity
		expectedResult error
	}{
		{
			name:           "empty signer should fail",
			msg:            NewMsgAddLiquidity(GetRandomTx(), common.ETHAsset, cosmos.NewUint(common.One*5), cosmos.NewUint(common.One*5), GetRandomETHAddress(), GetRandomETHAddress(), common.NoAddress, cosmos.ZeroUint(), cosmos.AccAddress{}),
			expectedResult: errAddLiquidityFailValidation,
		},
		{
			name:           "empty asset should fail",
			msg:            NewMsgAddLiquidity(GetRandomTx(), common.Asset{}, cosmos.NewUint(common.One*5), cosmos.NewUint(common.One*5), GetRandomETHAddress(), GetRandomETHAddress(), common.NoAddress, cosmos.ZeroUint(), GetRandomValidatorNode(NodeActive).NodeAddress),
			expectedResult: errAddLiquidityFailValidation,
		},
		{
			name:           "synth asset from coins should fail",
			msg:            NewMsgAddLiquidity(tx, common.ETHAsset, cosmos.NewUint(common.One*5), cosmos.NewUint(common.One*5), GetRandomETHAddress(), GetRandomETHAddress(), common.NoAddress, cosmos.ZeroUint(), GetRandomValidatorNode(NodeActive).NodeAddress),
			expectedResult: errAddLiquidityFailValidation,
		},
		{
			name:           "empty addresses should fail",
			msg:            NewMsgAddLiquidity(GetRandomTx(), common.BTCAsset, cosmos.NewUint(common.One*5), cosmos.NewUint(common.One*5), common.NoAddress, common.NoAddress, common.NoAddress, cosmos.ZeroUint(), GetRandomValidatorNode(NodeActive).NodeAddress),
			expectedResult: errAddLiquidityFailValidation,
		},
		{
			name:           "total liquidity provider is more than total bond should fail",
			msg:            NewMsgAddLiquidity(GetRandomTx(), common.ETHAsset, cosmos.NewUint(common.One*5000), cosmos.NewUint(common.One*5000), GetRandomRUNEAddress(), GetRandomETHAddress(), common.NoAddress, cosmos.ZeroUint(), activeNodeAccount.NodeAddress),
			expectedResult: errAddLiquidityRUNEMoreThanBond,
		},
		{
			name:           "rune address with wrong chain should fail",
			msg:            NewMsgAddLiquidity(GetRandomTx(), common.ETHAsset, cosmos.NewUint(common.One*5), cosmos.NewUint(common.One*5), GetRandomETHAddress(), GetRandomRUNEAddress(), common.NoAddress, cosmos.ZeroUint(), GetRandomValidatorNode(NodeActive).NodeAddress),
			expectedResult: errAddLiquidityFailValidation,
		},
		{
			name:           "asset address with wrong network should fail",
			msg:            NewMsgAddLiquidity(GetRandomTx(), common.ETHAsset, cosmos.NewUint(common.One*5), cosmos.NewUint(common.One*5), GetRandomRUNEAddress(), mainnetBTCAddr, common.NoAddress, cosmos.ZeroUint(), GetRandomValidatorNode(NodeActive).NodeAddress),
			expectedResult: fmt.Errorf("address(%s) is not same network", mainnetBTCAddr),
		},
	}
	constAccessor := constants.NewDummyConstants(map[constants.ConstantName]int64{
		constants.MaximumLiquidityRune: 600_000_00000000,
	}, map[constants.ConstantName]bool{
		constants.StrictBondLiquidityRatio: true,
	}, map[constants.ConstantName]string{})

	for _, item := range testCases {
		mgr := NewDummyMgrWithKeeper(k)
		mgr.constAccessor = constAccessor
		addHandler := NewAddLiquidityHandler(mgr)
		_, err := addHandler.Run(ctx, item.msg)
		c.Assert(err.Error(), Equals, item.expectedResult.Error(), Commentf("name:%s, actual: %w, expected: %w", item.name, err, item.expectedResult))
	}
}

func (s *HandlerAddLiquiditySuite) TestHandlerAddLiquidityFailScenario(c *C) {
	activeNodeAccount := GetRandomValidatorNode(NodeActive)
	emptyPool := Pool{
		BalanceRune:  cosmos.ZeroUint(),
		BalanceAsset: cosmos.ZeroUint(),
		Asset:        common.ETHAsset,
		LPUnits:      cosmos.ZeroUint(),
		Status:       PoolAvailable,
	}

	testCases := []struct {
		name           string
		k              keeper.Keeper
		expectedResult error
	}{
		{
			name: "fail to get pool should fail add liquidity",
			k: &MockAddLiquidityKeeper{
				activeNodeAccount: activeNodeAccount,
				currentPool:       emptyPool,
				failGetPool:       true,
			},
			expectedResult: errInternal,
		},
		{
			name: "suspended pool should fail add liquidity",
			k: &MockAddLiquidityKeeper{
				activeNodeAccount: activeNodeAccount,
				currentPool: Pool{
					BalanceRune:  cosmos.ZeroUint(),
					BalanceAsset: cosmos.ZeroUint(),
					Asset:        common.ETHAsset,
					LPUnits:      cosmos.ZeroUint(),
					Status:       PoolSuspended,
				},
			},
			expectedResult: errInvalidPoolStatus,
		},
	}
	for _, tc := range testCases {
		runeAddr := GetRandomRUNEAddress()
		ethAddr := GetRandomETHAddress()
		addTxHash := GetRandomTxHash()
		tx := common.NewTx(
			addTxHash,
			runeAddr,
			GetRandomETHAddress(),
			common.Coins{common.NewCoin(common.ETHAsset, cosmos.NewUint(common.One*5))},
			common.Gas{
				common.NewCoin(common.ETHAsset, cosmos.NewUint(10000)),
			},
			"add:ETH",
		)
		msg := NewMsgAddLiquidity(
			tx,
			common.ETHAsset,
			cosmos.NewUint(100*common.One),
			cosmos.NewUint(100*common.One),
			runeAddr,
			ethAddr,
			common.NoAddress, cosmos.ZeroUint(),
			activeNodeAccount.NodeAddress,
		)
		ctx, mgr := setupManagerForTest(c)
		mgr.K = tc.k
		addHandler := NewAddLiquidityHandler(mgr)
		_, err := addHandler.Run(ctx, msg)
		c.Assert(errors.Is(err, tc.expectedResult), Equals, true, Commentf(tc.name))
	}
}

type AddLiquidityTestKeeper struct {
	keeper.KVStoreDummy
	store          map[string]interface{}
	liquidityUnits cosmos.Uint
}

// NewAddLiquidityTestKeeper
func NewAddLiquidityTestKeeper() *AddLiquidityTestKeeper {
	return &AddLiquidityTestKeeper{
		store:          make(map[string]interface{}),
		liquidityUnits: cosmos.ZeroUint(),
	}
}

func (p *AddLiquidityTestKeeper) PoolExist(ctx cosmos.Context, asset common.Asset) bool {
	_, ok := p.store[asset.String()]
	return ok
}

var notExistLiquidityProviderAsset, _ = common.NewAsset("ETH.NotExistLiquidityProviderAsset")

func (p *AddLiquidityTestKeeper) GetPool(ctx cosmos.Context, asset common.Asset) (Pool, error) {
	if p, ok := p.store[asset.String()]; ok {
		pool, poolOk := p.(Pool)
		if !poolOk {
			return pool, fmt.Errorf("dev error: failed to cast pool")
		}
		return pool, nil
	}
	return NewPool(), nil
}

func (p *AddLiquidityTestKeeper) GetPools(_ cosmos.Context) (Pools, error) {
	pools := make(Pools, 0)
	for _, value := range p.store {
		pool, ok := value.(Pool)
		if !ok {
			continue
		}
		pools = append(pools, pool)
	}
	return pools, nil
}

func (p *AddLiquidityTestKeeper) SetPool(ctx cosmos.Context, ps Pool) error {
	p.store[ps.Asset.String()] = ps
	return nil
}

func (p *AddLiquidityTestKeeper) GetModuleAddress(_ string) (common.Address, error) {
	return common.NoAddress, nil
}

func (p *AddLiquidityTestKeeper) GetPOL(_ cosmos.Context) (ProtocolOwnedLiquidity, error) {
	return NewProtocolOwnedLiquidity(), nil
}

func (p *AddLiquidityTestKeeper) SetPOL(_ cosmos.Context, pol ProtocolOwnedLiquidity) error {
	return nil
}

func (p *AddLiquidityTestKeeper) GetLiquidityProvider(ctx cosmos.Context, asset common.Asset, addr common.Address) (LiquidityProvider, error) {
	if notExistLiquidityProviderAsset.Equals(asset) {
		return LiquidityProvider{}, errors.New("simulate error for test")
	}
	lp := LiquidityProvider{
		Asset:             asset,
		RuneAddress:       addr,
		Units:             cosmos.ZeroUint(),
		PendingRune:       cosmos.ZeroUint(),
		PendingAsset:      cosmos.ZeroUint(),
		RuneDepositValue:  cosmos.ZeroUint(),
		AssetDepositValue: cosmos.ZeroUint(),
	}
	key := p.GetKey("lp/", lp.Key())
	if res, ok := p.store[string(key)]; ok {
		lpCast, okCast := res.(LiquidityProvider)
		if !okCast {
			return lpCast, fmt.Errorf("dev error: failed to cast liquidity provider")
		}
		return lpCast, nil
	}
	lp.Units = p.liquidityUnits
	return lp, nil
}

func (p *AddLiquidityTestKeeper) SetLiquidityProvider(ctx cosmos.Context, lp LiquidityProvider) {
	key := p.GetKey("lp/", lp.Key())
	p.store[string(key)] = lp
}

func (p *AddLiquidityTestKeeper) GetLiquidityProviderIterator(_ cosmos.Context, asset common.Asset) cosmos.Iterator {
	db := dbm.NewMemDB()
	for key, value := range p.store {
		lp, ok := value.(LiquidityProvider)
		if !ok || !lp.Asset.Equals(asset) {
			continue
		}
		if err := db.Set([]byte(key), p.Cdc().MustMarshal(&lp)); err != nil {
			panic(err)
		}
	}
	iter, err := db.Iterator(nil, nil)
	if err != nil {
		panic(err)
	}
	return iter
}

func (p *AddLiquidityTestKeeper) AddOwnership(ctx cosmos.Context, coin common.Coin, addr cosmos.AccAddress) error {
	p.liquidityUnits = p.liquidityUnits.Add(coin.Amount)
	return nil
}

func (s *HandlerAddLiquiditySuite) TestCalculateLPUnits(c *C) {
	inputs := []struct {
		name           string
		oldLPUnits     cosmos.Uint
		poolRune       cosmos.Uint
		poolAsset      cosmos.Uint
		addRune        cosmos.Uint
		addAsset       cosmos.Uint
		poolUnits      cosmos.Uint
		liquidityUnits cosmos.Uint
		expectedErr    error
	}{
		{
			name:           "first-add-zero-rune",
			oldLPUnits:     cosmos.ZeroUint(),
			poolRune:       cosmos.ZeroUint(),
			poolAsset:      cosmos.ZeroUint(),
			addRune:        cosmos.ZeroUint(),
			addAsset:       cosmos.NewUint(100 * common.One),
			poolUnits:      cosmos.ZeroUint(),
			liquidityUnits: cosmos.ZeroUint(),
			expectedErr:    errors.New("total RUNE in the pool is zero"),
		},
		{
			name:           "first-add-zero-asset",
			oldLPUnits:     cosmos.ZeroUint(),
			poolRune:       cosmos.ZeroUint(),
			poolAsset:      cosmos.ZeroUint(),
			addRune:        cosmos.NewUint(100 * common.One),
			addAsset:       cosmos.ZeroUint(),
			poolUnits:      cosmos.ZeroUint(),
			liquidityUnits: cosmos.ZeroUint(),
			expectedErr:    errors.New("total asset in the pool is zero"),
		},
		{
			name:           "first-add",
			oldLPUnits:     cosmos.ZeroUint(),
			poolRune:       cosmos.ZeroUint(),
			poolAsset:      cosmos.ZeroUint(),
			addRune:        cosmos.NewUint(100 * common.One),
			addAsset:       cosmos.NewUint(100 * common.One),
			poolUnits:      cosmos.NewUint(100 * common.One),
			liquidityUnits: cosmos.NewUint(100 * common.One),
			expectedErr:    nil,
		},
		{
			name:           "second-add",
			oldLPUnits:     cosmos.NewUint(789500 * common.One),
			poolRune:       cosmos.NewUint(500 * common.One),
			poolAsset:      cosmos.NewUint(500 * common.One),
			addRune:        cosmos.NewUint(345 * common.One),
			addAsset:       cosmos.NewUint(234 * common.One),
			poolUnits:      cosmos.NewUint(1240460 * common.One),
			liquidityUnits: cosmos.NewUint(450960 * common.One),
			expectedErr:    nil,
		},
		{
			name:           "asym-add",
			oldLPUnits:     cosmos.NewUint(300 * common.One),
			poolRune:       cosmos.NewUint(500 * common.One),
			poolAsset:      cosmos.NewUint(500 * common.One),
			addRune:        cosmos.NewUint(500 * common.One),
			addAsset:       cosmos.ZeroUint(),
			poolUnits:      cosmos.NewUint(400 * common.One),
			liquidityUnits: cosmos.NewUint(100 * common.One),
			expectedErr:    nil,
		},
	}

	for _, item := range inputs {
		c.Logf("Name: %s", item.name)
		poolUnits, liquidityUnits, err := calculatePoolUnits(item.oldLPUnits, item.poolRune, item.poolAsset, item.addRune, item.addAsset)
		if item.expectedErr == nil {
			c.Assert(err, IsNil)
		} else {
			c.Assert(err.Error(), Equals, item.expectedErr.Error())
		}

		c.Check(item.poolUnits.Uint64(), Equals, poolUnits.Uint64(), Commentf("%d / %d", item.poolUnits.Uint64(), poolUnits.Uint64()))
		c.Check(item.liquidityUnits.Uint64(), Equals, liquidityUnits.Uint64(), Commentf("%d / %d", item.liquidityUnits.Uint64(), liquidityUnits.Uint64()))
	}
}

func (s *HandlerAddLiquiditySuite) TestValidateAddLiquidityMessage(c *C) {
	ps := NewAddLiquidityTestKeeper()
	ctx, mgr := setupManagerForTest(c)
	mgr.K = ps
	txID := GetRandomTxHash()
	ethAddress := GetRandomETHAddress()
	assetAddress := GetRandomETHAddress()
	h := NewAddLiquidityHandler(mgr)
	c.Assert(h.validateAddLiquidityMessage(ctx, ps, common.Asset{}, txID, ethAddress, assetAddress), NotNil)
	c.Assert(h.validateAddLiquidityMessage(ctx, ps, common.ETHAsset, txID, ethAddress, assetAddress), NotNil)
	c.Assert(h.validateAddLiquidityMessage(ctx, ps, common.ETHAsset, txID, ethAddress, assetAddress), NotNil)
	c.Assert(h.validateAddLiquidityMessage(ctx, ps, common.ETHAsset, common.TxID(""), ethAddress, assetAddress), NotNil)
	c.Assert(h.validateAddLiquidityMessage(ctx, ps, common.ETHAsset, txID, common.NoAddress, common.NoAddress), NotNil)
	c.Assert(h.validateAddLiquidityMessage(ctx, ps, common.ETHAsset, txID, ethAddress, assetAddress), NotNil)
	c.Assert(h.validateAddLiquidityMessage(ctx, ps, common.ETHAsset, txID, common.NoAddress, assetAddress), NotNil)
	c.Assert(h.validateAddLiquidityMessage(ctx, ps, common.BTCAsset, txID, ethAddress, common.NoAddress), NotNil)
	c.Assert(ps.SetPool(ctx, Pool{
		BalanceRune:  cosmos.NewUint(100 * common.One),
		BalanceAsset: cosmos.NewUint(100 * common.One),
		Asset:        common.ETHAsset,
		LPUnits:      cosmos.NewUint(100 * common.One),
		Status:       PoolAvailable,
	}), IsNil)
	// happy path
	c.Assert(h.validateAddLiquidityMessage(ctx, ps, common.ETHAsset, txID, ethAddress, assetAddress), Equals, nil)
	// Don't accept THOR.ETH
	thorAsset := common.ETHAsset
	var err error // Previously undeclared, so declaration needed
	thorAsset.Chain, err = common.NewChain("THOR")
	c.Assert(err, IsNil)
	c.Assert(h.validateAddLiquidityMessage(ctx, ps, thorAsset, txID, ethAddress, assetAddress), NotNil)
	// Don't accept TEST.ETH
	testAsset := common.ETHAsset
	testAsset.Chain, err = common.NewChain("TEST")
	c.Assert(err, IsNil)
	c.Assert(h.validateAddLiquidityMessage(ctx, ps, testAsset, txID, ethAddress, assetAddress), NotNil)
}

func (s *HandlerAddLiquiditySuite) TestAddLiquidityV1(c *C) {
	ps := NewAddLiquidityTestKeeper()
	ctx, _ := setupKeeperForTest(c)
	txID := GetRandomTxHash()

	runeAddress := GetRandomRUNEAddress()
	assetAddress := GetRandomETHAddress()
	btcAddress, err := common.NewAddress("bc1qwqdg6squsna38e46795at95yu9atm8azzmyvckulcc7kytlcckxswvvzej")
	c.Assert(err, IsNil)
	constAccessor := constants.GetConstantValues(GetCurrentVersion())
	h := NewAddLiquidityHandler(NewDummyMgrWithKeeper(ps))
	err = h.addLiquidity(ctx, common.Asset{}, cosmos.NewUint(100*common.One), cosmos.NewUint(100*common.One), runeAddress, assetAddress, txID, false, constAccessor)
	c.Assert(err, NotNil)
	c.Assert(ps.SetPool(ctx, Pool{
		BalanceRune:         cosmos.ZeroUint(),
		BalanceAsset:        cosmos.NewUint(100 * common.One),
		Asset:               common.ETHAsset,
		LPUnits:             cosmos.NewUint(100 * common.One),
		SynthUnits:          cosmos.ZeroUint(),
		PendingInboundAsset: cosmos.ZeroUint(),
		PendingInboundRune:  cosmos.ZeroUint(),
		Status:              PoolAvailable,
	}), IsNil)
	err = h.addLiquidity(ctx, common.ETHAsset, cosmos.NewUint(100*common.One), cosmos.NewUint(100*common.One), runeAddress, assetAddress, txID, false, constAccessor)
	c.Assert(err, IsNil)
	su, err := ps.GetLiquidityProvider(ctx, common.ETHAsset, runeAddress)
	c.Assert(err, IsNil)
	// c.Assert(su.Units.Equal(cosmos.NewUint(11250000000)), Equals, true, Commentf("%d", su.Units.Uint64()))

	c.Assert(ps.SetPool(ctx, Pool{
		BalanceRune:         cosmos.NewUint(100 * common.One),
		BalanceAsset:        cosmos.NewUint(100 * common.One),
		Asset:               notExistLiquidityProviderAsset,
		LPUnits:             cosmos.NewUint(100 * common.One),
		SynthUnits:          cosmos.ZeroUint(),
		PendingInboundAsset: cosmos.ZeroUint(),
		PendingInboundRune:  cosmos.ZeroUint(),
		Status:              PoolAvailable,
	}), IsNil)
	// add asymmetically
	err = h.addLiquidity(ctx, common.ETHAsset, cosmos.NewUint(100*common.One), cosmos.ZeroUint(), runeAddress, assetAddress, txID, false, constAccessor)
	c.Assert(err, IsNil)
	err = h.addLiquidity(ctx, common.ETHAsset, cosmos.ZeroUint(), cosmos.NewUint(100*common.One), runeAddress, assetAddress, txID, false, constAccessor)
	c.Assert(err, IsNil)

	err = h.addLiquidity(ctx, notExistLiquidityProviderAsset, cosmos.NewUint(100*common.One), cosmos.NewUint(100*common.One), runeAddress, assetAddress, txID, false, constAccessor)
	c.Assert(err, NotNil)
	c.Assert(ps.SetPool(ctx, Pool{
		BalanceRune:         cosmos.NewUint(100 * common.One),
		BalanceAsset:        cosmos.NewUint(100 * common.One),
		Asset:               common.ETHAsset,
		LPUnits:             cosmos.NewUint(100 * common.One),
		SynthUnits:          cosmos.ZeroUint(),
		PendingInboundAsset: cosmos.ZeroUint(),
		PendingInboundRune:  cosmos.ZeroUint(),
		Status:              PoolAvailable,
	}), IsNil)

	for i := 1; i <= 150; i++ {
		lp := LiquidityProvider{Units: cosmos.NewUint(common.One / 5000)}
		ps.SetLiquidityProvider(ctx, lp)
	}
	err = h.addLiquidity(ctx, common.ETHAsset, cosmos.NewUint(common.One), cosmos.NewUint(common.One), runeAddress, assetAddress, txID, false, constAccessor)
	c.Assert(err, IsNil)

	err = h.addLiquidity(ctx, common.ETHAsset, cosmos.NewUint(100*common.One), cosmos.NewUint(100*common.One), runeAddress, assetAddress, txID, false, constAccessor)
	c.Assert(err, IsNil)
	p, err := ps.GetPool(ctx, common.ETHAsset)
	c.Assert(err, IsNil)
	c.Check(p.LPUnits.Equal(cosmos.NewUint(201*common.One)), Equals, true, Commentf("%d", p.LPUnits.Uint64()))

	// Test atomic cross chain liquidity provision
	// create BTC pool
	c.Assert(ps.SetPool(ctx, Pool{
		BalanceRune:         cosmos.ZeroUint(),
		BalanceAsset:        cosmos.ZeroUint(),
		Asset:               common.BTCAsset,
		LPUnits:             cosmos.ZeroUint(),
		SynthUnits:          cosmos.ZeroUint(),
		PendingInboundAsset: cosmos.ZeroUint(),
		PendingInboundRune:  cosmos.ZeroUint(),
		Status:              PoolAvailable,
	}), IsNil)

	// add rune
	err = h.addLiquidity(ctx, common.BTCAsset, cosmos.NewUint(100*common.One), cosmos.ZeroUint(), runeAddress, btcAddress, txID, true, constAccessor)
	c.Assert(err, IsNil)
	_, err = ps.GetLiquidityProvider(ctx, common.BTCAsset, runeAddress)
	c.Assert(err, IsNil)
	// c.Check(su.Units.IsZero(), Equals, true)
	// add btc
	err = h.addLiquidity(ctx, common.BTCAsset, cosmos.ZeroUint(), cosmos.NewUint(100*common.One), runeAddress, btcAddress, txID, false, constAccessor)
	c.Assert(err, IsNil)
	su, err = ps.GetLiquidityProvider(ctx, common.BTCAsset, runeAddress)
	c.Assert(err, IsNil)
	c.Check(su.Units.IsZero(), Equals, false)
	p, err = ps.GetPool(ctx, common.BTCAsset)
	c.Assert(err, IsNil)
	c.Check(p.BalanceAsset.Equal(cosmos.NewUint(100*common.One)), Equals, true, Commentf("%d", p.BalanceAsset.Uint64()))
	c.Check(p.BalanceRune.Equal(cosmos.NewUint(100*common.One)), Equals, true, Commentf("%d", p.BalanceRune.Uint64()))
	c.Check(p.LPUnits.Equal(cosmos.NewUint(100*common.One)), Equals, true, Commentf("%d", p.LPUnits.Uint64()))
}

func (s *HandlerAddLiquiditySuite) TestAddLiquidityXMRUnknownSender(c *C) {
	constAccessor := constants.GetConstantValues(GetCurrentVersion())

	stageRuneFirst := func(runeAddress, assetAddress, unknownSender common.Address) {
		ps := NewAddLiquidityTestKeeper()
		ctx, _ := setupKeeperForTest(c)
		h := NewAddLiquidityHandler(NewDummyMgrWithKeeper(ps))

		c.Assert(ps.SetPool(ctx, Pool{
			BalanceRune:         cosmos.ZeroUint(),
			BalanceAsset:        cosmos.ZeroUint(),
			Asset:               common.XMRAsset,
			LPUnits:             cosmos.ZeroUint(),
			SynthUnits:          cosmos.ZeroUint(),
			PendingInboundAsset: cosmos.ZeroUint(),
			PendingInboundRune:  cosmos.ZeroUint(),
			Status:              PoolAvailable,
		}), IsNil)

		err := h.addLiquidity(ctx, common.XMRAsset, cosmos.NewUint(100*common.One), cosmos.ZeroUint(), runeAddress, assetAddress, GetRandomTxHash(), true, constAccessor)
		c.Assert(err, IsNil)

		err = h.addLiquidity(ctx, common.XMRAsset, cosmos.ZeroUint(), cosmos.NewUint(100*common.One), runeAddress, unknownSender, GetRandomTxHash(), false, constAccessor)
		c.Assert(err, IsNil)

		su, err := ps.GetLiquidityProvider(ctx, common.XMRAsset, runeAddress)
		c.Assert(err, IsNil)
		c.Check(su.AssetAddress.Equals(assetAddress), Equals, true)
		c.Check(su.Units.IsZero(), Equals, false)

		pool, err := ps.GetPool(ctx, common.XMRAsset)
		c.Assert(err, IsNil)
		c.Check(pool.PendingInboundRune.IsZero(), Equals, true)
		c.Check(pool.PendingInboundAsset.IsZero(), Equals, true)
		c.Check(pool.BalanceRune.Equal(cosmos.NewUint(100*common.One)), Equals, true)
		c.Check(pool.BalanceAsset.Equal(cosmos.NewUint(100*common.One)), Equals, true)
	}

	stageAssetFirst := func(runeAddress, assetAddress, unknownSender common.Address) {
		ps := NewAddLiquidityTestKeeper()
		ctx, _ := setupKeeperForTest(c)
		h := NewAddLiquidityHandler(NewDummyMgrWithKeeper(ps))

		c.Assert(ps.SetPool(ctx, Pool{
			BalanceRune:         cosmos.ZeroUint(),
			BalanceAsset:        cosmos.ZeroUint(),
			Asset:               common.XMRAsset,
			LPUnits:             cosmos.ZeroUint(),
			SynthUnits:          cosmos.ZeroUint(),
			PendingInboundAsset: cosmos.ZeroUint(),
			PendingInboundRune:  cosmos.ZeroUint(),
			Status:              PoolAvailable,
		}), IsNil)

		err := h.addLiquidity(ctx, common.XMRAsset, cosmos.ZeroUint(), cosmos.NewUint(100*common.One), runeAddress, unknownSender, GetRandomTxHash(), true, constAccessor)
		c.Assert(err, IsNil)

		err = h.addLiquidity(ctx, common.XMRAsset, cosmos.NewUint(100*common.One), cosmos.ZeroUint(), runeAddress, assetAddress, GetRandomTxHash(), false, constAccessor)
		c.Assert(err, IsNil)

		su, err := ps.GetLiquidityProvider(ctx, common.XMRAsset, runeAddress)
		c.Assert(err, IsNil)
		c.Check(su.AssetAddress.Equals(assetAddress), Equals, true)
		c.Check(su.Units.IsZero(), Equals, false)

		pool, err := ps.GetPool(ctx, common.XMRAsset)
		c.Assert(err, IsNil)
		c.Check(pool.PendingInboundRune.IsZero(), Equals, true)
		c.Check(pool.PendingInboundAsset.IsZero(), Equals, true)
		c.Check(pool.BalanceRune.Equal(cosmos.NewUint(100*common.One)), Equals, true)
		c.Check(pool.BalanceAsset.Equal(cosmos.NewUint(100*common.One)), Equals, true)
	}

	xmrAddr1, err := GetRandomEd25519PubKey().GetAddress(common.XMRChain)
	c.Assert(err, IsNil)
	xmrUnknown1, err := common.NewAddress(common.XMRUnknownSenderAddress(GetRandomTxHash().String(), xmrAddr1.String()))
	c.Assert(err, IsNil)
	stageRuneFirst(GetRandomRUNEAddress(), xmrAddr1, xmrUnknown1)

	xmrAddr2, err := GetRandomEd25519PubKey().GetAddress(common.XMRChain)
	c.Assert(err, IsNil)
	xmrUnknown2, err := common.NewAddress(common.XMRUnknownSenderAddress(GetRandomTxHash().String(), xmrAddr2.String()))
	c.Assert(err, IsNil)
	stageAssetFirst(GetRandomRUNEAddress(), xmrAddr2, xmrUnknown2)

	ps := NewAddLiquidityTestKeeper()
	ctx, _ := setupKeeperForTest(c)
	mgr := NewDummyMgrWithKeeper(ps)
	mgr.constAccessor = constants.NewDummyConstants(
		map[constants.ConstantName]int64{},
		map[constants.ConstantName]bool{constants.StrictBondLiquidityRatio: false},
		map[constants.ConstantName]string{},
	)
	h := NewAddLiquidityHandler(mgr)
	c.Assert(ps.SetPool(ctx, Pool{
		BalanceRune:         cosmos.ZeroUint(),
		BalanceAsset:        cosmos.ZeroUint(),
		Asset:               common.XMRAsset,
		LPUnits:             cosmos.ZeroUint(),
		SynthUnits:          cosmos.ZeroUint(),
		PendingInboundAsset: cosmos.ZeroUint(),
		PendingInboundRune:  cosmos.ZeroUint(),
		Status:              PoolAvailable,
	}), IsNil)

	xmrRuneAddr := GetRandomRUNEAddress()
	xmrAddr3, err := GetRandomEd25519PubKey().GetAddress(common.XMRChain)
	c.Assert(err, IsNil)
	xmrUnknown3, err := common.NewAddress(common.XMRUnknownSenderAddress(GetRandomTxHash().String(), xmrAddr3.String()))
	c.Assert(err, IsNil)
	tx := common.NewTx(
		GetRandomTxHash(),
		xmrUnknown3,
		xmrAddr3,
		common.Coins{common.NewCoin(common.XMRAsset, cosmos.NewUint(100*common.One))},
		common.Gas{common.NewCoin(common.XMRAsset, cosmos.NewUint(1))},
		fmt.Sprintf("add:%s:%s", common.XMRAsset, xmrRuneAddr),
	)
	msg := NewMsgAddLiquidity(
		tx,
		common.XMRAsset,
		cosmos.ZeroUint(),
		cosmos.NewUint(100*common.One),
		xmrRuneAddr,
		xmrUnknown3,
		common.NoAddress,
		cosmos.ZeroUint(),
		GetRandomBech32Addr(),
	)
	err = h.validate(ctx, *msg)
	c.Assert(err, IsNil)

	ps = NewAddLiquidityTestKeeper()
	ctx, _ = setupKeeperForTest(c)
	mgr = NewDummyMgrWithKeeper(ps)
	mgr.constAccessor = constants.NewDummyConstants(
		map[constants.ConstantName]int64{},
		map[constants.ConstantName]bool{constants.StrictBondLiquidityRatio: false},
		map[constants.ConstantName]string{},
	)
	h = NewAddLiquidityHandler(mgr)
	c.Assert(ps.SetPool(ctx, Pool{
		BalanceRune:         cosmos.ZeroUint(),
		BalanceAsset:        cosmos.ZeroUint(),
		Asset:               common.XMRAsset,
		LPUnits:             cosmos.ZeroUint(),
		SynthUnits:          cosmos.ZeroUint(),
		PendingInboundAsset: cosmos.ZeroUint(),
		PendingInboundRune:  cosmos.ZeroUint(),
		Status:              PoolAvailable,
	}), IsNil)

	runeTx := common.NewTx(
		GetRandomTxHash(),
		xmrRuneAddr,
		GetRandomTHORAddress(),
		common.Coins{common.NewCoin(common.RuneAsset(), cosmos.NewUint(100*common.One))},
		common.Gas{},
		fmt.Sprintf("add:%s:%s", common.XMRAsset, xmrAddr3),
	)
	runeMsg := NewMsgAddLiquidity(
		runeTx,
		common.XMRAsset,
		cosmos.NewUint(100*common.One),
		cosmos.ZeroUint(),
		xmrRuneAddr,
		xmrAddr3,
		common.NoAddress,
		cosmos.ZeroUint(),
		GetRandomBech32Addr(),
	)
	err = h.validate(ctx, *runeMsg)
	c.Assert(err, IsNil)
	err = h.handle(ctx, *runeMsg)
	c.Assert(err, IsNil)

	assetTx := common.NewTx(
		GetRandomTxHash(),
		xmrUnknown3,
		xmrAddr3,
		common.Coins{common.NewCoin(common.XMRAsset, cosmos.NewUint(100*common.One))},
		common.Gas{common.NewCoin(common.XMRAsset, cosmos.NewUint(1))},
		fmt.Sprintf("add:%s:%s", common.XMRAsset, xmrRuneAddr),
	)
	assetMsg := NewMsgAddLiquidity(
		assetTx,
		common.XMRAsset,
		cosmos.ZeroUint(),
		cosmos.NewUint(100*common.One),
		xmrRuneAddr,
		xmrUnknown3,
		common.NoAddress,
		cosmos.ZeroUint(),
		GetRandomBech32Addr(),
	)
	err = h.validate(ctx, *assetMsg)
	c.Assert(err, IsNil)
	err = h.handle(ctx, *assetMsg)
	c.Assert(err, IsNil)

	su, err := ps.GetLiquidityProvider(ctx, common.XMRAsset, xmrRuneAddr)
	c.Assert(err, IsNil)
	c.Check(su.AssetAddress.Equals(xmrAddr3), Equals, true)
	c.Check(su.Units.IsZero(), Equals, false)

	ps = NewAddLiquidityTestKeeper()
	ctx, _ = setupKeeperForTest(c)
	h = NewAddLiquidityHandler(NewDummyMgrWithKeeper(ps))
	c.Assert(ps.SetPool(ctx, Pool{
		BalanceRune:         cosmos.NewUint(100 * common.One),
		BalanceAsset:        cosmos.NewUint(100 * common.One),
		Asset:               common.XMRAsset,
		LPUnits:             cosmos.NewUint(100 * common.One),
		SynthUnits:          cosmos.ZeroUint(),
		PendingInboundAsset: cosmos.ZeroUint(),
		PendingInboundRune:  cosmos.ZeroUint(),
		Status:              PoolAvailable,
	}), IsNil)

	victimRuneAddr := GetRandomRUNEAddress()
	victimAssetAddr, err := GetRandomEd25519PubKey().GetAddress(common.XMRChain)
	c.Assert(err, IsNil)
	ps.SetLiquidityProvider(ctx, LiquidityProvider{
		Asset:             common.XMRAsset,
		RuneAddress:       victimRuneAddr,
		AssetAddress:      victimAssetAddr,
		Units:             cosmos.NewUint(100 * common.One),
		PendingRune:       cosmos.ZeroUint(),
		PendingAsset:      cosmos.ZeroUint(),
		RuneDepositValue:  cosmos.ZeroUint(),
		AssetDepositValue: cosmos.ZeroUint(),
		LastAddHeight:     ctx.BlockHeight(),
	})

	attackerRuneAddr := GetRandomRUNEAddress()
	err = h.addLiquidity(
		ctx,
		common.XMRAsset,
		cosmos.NewUint(10*common.One),
		cosmos.ZeroUint(),
		attackerRuneAddr,
		victimAssetAddr,
		GetRandomTxHash(),
		true,
		constAccessor,
	)
	c.Assert(err, Equals, errAddLiquidityMismatchAddr)

	victimLP, err := ps.GetLiquidityProvider(ctx, common.XMRAsset, victimRuneAddr)
	c.Assert(err, IsNil)
	c.Check(victimLP.RuneAddress.Equals(victimRuneAddr), Equals, true)
	c.Check(victimLP.AssetAddress.Equals(victimAssetAddr), Equals, true)
	c.Check(victimLP.Units.Equal(cosmos.NewUint(100*common.One)), Equals, true)
}

func (HandlerAddLiquiditySuite) TestRuneOnlyFairMergeProvidedLiquidity(c *C) {
	ctx, k := setupKeeperForTest(c)
	txID := GetRandomTxHash()

	c.Assert(k.SetPool(ctx, Pool{
		BalanceRune:  cosmos.NewUint(100 * common.One),
		BalanceAsset: cosmos.NewUint(100 * common.One),
		Asset:        common.BTCAsset,
		LPUnits:      cosmos.NewUint(100 * common.One),
		SynthUnits:   cosmos.ZeroUint(),
		Status:       PoolAvailable,
	}), IsNil)

	runeAddr := GetRandomRUNEAddress()
	constAccessor := constants.GetConstantValues(GetCurrentVersion())
	h := NewAddLiquidityHandler(NewDummyMgrWithKeeper(k))
	err := h.addLiquidity(ctx, common.BTCAsset, cosmos.NewUint(100*common.One), cosmos.ZeroUint(), runeAddr, common.NoAddress, txID, false, constAccessor)
	c.Assert(err, IsNil)

	su, err := k.GetLiquidityProvider(ctx, common.BTCAsset, runeAddr)
	c.Assert(err, IsNil)
	c.Assert(su.Units.Uint64(), Equals, uint64(3333333333), Commentf("%d", su.Units.Uint64()))

	pool, err := k.GetPool(ctx, common.BTCAsset)
	c.Assert(err, IsNil)
	c.Assert(pool.LPUnits.Uint64(), Equals, uint64(13333333333), Commentf("%d", pool.LPUnits.Uint64()))
}

func (HandlerAddLiquiditySuite) TestAssetOnlyFairMergeProvidedLiquidity(c *C) {
	ctx, k := setupKeeperForTest(c)
	txID := GetRandomTxHash()

	c.Assert(k.SetPool(ctx, Pool{
		BalanceRune:  cosmos.NewUint(100 * common.One),
		BalanceAsset: cosmos.NewUint(100 * common.One),
		Asset:        common.BTCAsset,
		LPUnits:      cosmos.NewUint(100 * common.One),
		SynthUnits:   cosmos.ZeroUint(),
		Status:       PoolAvailable,
	}), IsNil)

	assetAddr := GetRandomBTCAddress()
	constAccessor := constants.GetConstantValues(GetCurrentVersion())
	h := NewAddLiquidityHandler(NewDummyMgrWithKeeper(k))
	err := h.addLiquidity(ctx, common.BTCAsset, cosmos.ZeroUint(), cosmos.NewUint(100*common.One), common.NoAddress, assetAddr, txID, false, constAccessor)
	c.Assert(err, IsNil)

	su, err := k.GetLiquidityProvider(ctx, common.BTCAsset, assetAddr)
	c.Assert(err, IsNil)
	c.Assert(su.Units.Uint64(), Equals, uint64(3333333333), Commentf("%d", su.Units.Uint64()))

	pool, err := k.GetPool(ctx, common.BTCAsset)
	c.Assert(err, IsNil)
	c.Assert(pool.LPUnits.Uint64(), Equals, uint64(13333333333), Commentf("%d", pool.LPUnits.Uint64()))
}

func (HandlerAddLiquiditySuite) TestSynthValidate(c *C) {
	ctx, mgr := setupManagerForTest(c)

	asset := common.BTCAsset.GetSyntheticAsset()

	c.Assert(mgr.Keeper().SetPool(ctx, Pool{
		BalanceRune:  cosmos.NewUint(100 * common.One),
		BalanceAsset: cosmos.NewUint(10 * common.One),
		Asset:        asset,
		LPUnits:      cosmos.ZeroUint(),
		SynthUnits:   cosmos.ZeroUint(),
		Status:       PoolAvailable,
	}), IsNil)

	handler := NewAddLiquidityHandler(mgr)

	addr := GetRandomBTCAddress()
	signer := GetRandomBech32Addr()
	addTxHash := GetRandomTxHash()

	tx := common.NewTx(
		addTxHash,
		addr,
		addr,
		common.Coins{common.NewCoin(asset, cosmos.NewUint(1000*common.One))},
		common.Gas{
			common.NewCoin(common.ETHAsset, cosmos.NewUint(10000)),
		},
		fmt.Sprintf("add:%s", asset.String()),
	)

	// don't allow add liquidity when the gas asset pool doesn't exist
	msg := NewMsgAddLiquidity(tx, asset, cosmos.ZeroUint(), cosmos.NewUint(1000*common.One), common.NoAddress, addr, common.NoAddress, cosmos.ZeroUint(), signer)
	err := handler.validate(ctx, *msg)
	c.Assert(err, NotNil)

	// Set gas pool's Asset to represent existence for IsEmpty
	gasPool := NewPool()
	gasPool.Asset = common.BTCAsset
	c.Assert(mgr.Keeper().SetPool(ctx, gasPool), IsNil)

	// happy path
	err = handler.validate(ctx, *msg)
	c.Assert(err, IsNil)

	// don't accept THOR/BTC
	thorAsset := asset
	thorAsset.Chain, err = common.NewChain("THOR")
	c.Assert(err, IsNil)
	msg.Asset = thorAsset
	err = handler.validate(ctx, *msg)
	c.Assert(err, NotNil)
	// don't accept TEST/BTC
	testAsset := asset
	testAsset.Chain, err = common.NewChain("TEST")
	c.Assert(err, IsNil)
	msg.Asset = testAsset
	err = handler.validate(ctx, *msg)
	c.Assert(err, NotNil)

	// don't allow non-gas assets
	busd, err := common.NewAsset("ETH.BUSD-BD1")
	c.Assert(err, IsNil)
	msg = NewMsgAddLiquidity(tx, busd.GetSyntheticAsset(), cosmos.ZeroUint(), cosmos.NewUint(1000*common.One), addr, common.NoAddress, common.NoAddress, cosmos.ZeroUint(), signer)
	err = handler.validate(ctx, *msg)
	c.Assert(err, NotNil)

	// address mismatch
	msg = NewMsgAddLiquidity(tx, asset, cosmos.ZeroUint(), cosmos.NewUint(1000*common.One), addr, common.NoAddress, common.NoAddress, cosmos.ZeroUint(), signer)
	err = handler.validate(ctx, *msg)
	c.Assert(err, NotNil)
	msg = NewMsgAddLiquidity(tx, asset, cosmos.ZeroUint(), cosmos.NewUint(1000*common.One), common.NoAddress, common.NoAddress, common.NoAddress, cosmos.ZeroUint(), signer)
	err = handler.validate(ctx, *msg)
	c.Assert(err, NotNil)

	// don't allow rune
	msg = NewMsgAddLiquidity(tx, asset, cosmos.NewUint(1000*common.One), cosmos.ZeroUint(), common.NoAddress, addr, common.NoAddress, cosmos.ZeroUint(), signer)
	err = handler.validate(ctx, *msg)
	c.Assert(err, NotNil)
	msg = NewMsgAddLiquidity(tx, asset, cosmos.NewUint(1000*common.One), cosmos.NewUint(1000*common.One), common.NoAddress, addr, common.NoAddress, cosmos.ZeroUint(), signer)
	err = handler.validate(ctx, *msg)
	c.Assert(err, NotNil)
}

func (HandlerAddLiquiditySuite) TestAddSynthNoLPs(c *C) {
	// there is an odd case where its possible in a synth vault to have a
	// balance asset of non-zero BUT have no LPs yet. Testing this edge case.
	ctx, k := setupKeeperForTest(c)
	txID := GetRandomTxHash()

	asset := common.BTCAsset.GetSyntheticAsset()

	pool := NewPool()
	pool.Asset = asset
	pool.Status = PoolAvailable
	pool.BalanceRune = cosmos.NewUint(0)
	pool.BalanceAsset = cosmos.NewUint(10 * common.One)
	c.Assert(k.SetPool(ctx, pool), IsNil)

	coin := common.NewCoin(asset, pool.BalanceAsset)
	c.Assert(k.MintToModule(ctx, ModuleName, coin), IsNil)
	c.Assert(k.SendFromModuleToModule(ctx, ModuleName, AsgardName, common.NewCoins(coin)), IsNil)

	addr := GetRandomBTCAddress()
	constAccessor := constants.GetConstantValues(GetCurrentVersion())
	h := NewAddLiquidityHandler(NewDummyMgrWithKeeper(k))
	addCoin := common.NewCoin(asset, cosmos.NewUint(10*common.One))
	c.Assert(k.MintToModule(ctx, ModuleName, addCoin), IsNil)
	c.Assert(k.SendFromModuleToModule(ctx, ModuleName, AsgardName, common.NewCoins(addCoin)), IsNil)
	err := h.addLiquidity(ctx, asset, cosmos.ZeroUint(), addCoin.Amount, common.NoAddress, addr, txID, false, constAccessor)
	c.Assert(err, IsNil)

	su, err := k.GetLiquidityProvider(ctx, asset, addr)
	c.Assert(err, IsNil)
	c.Check(su.Units.Uint64(), Equals, uint64(10*common.One), Commentf("%d", su.Units.Uint64()))

	pool, err = k.GetPool(ctx, asset)
	c.Assert(err, IsNil)
	c.Check(pool.BalanceRune.Uint64(), Equals, uint64(0), Commentf("%d", pool.BalanceRune.Uint64()))
	c.Check(pool.BalanceAsset.Uint64(), Equals, uint64(20*common.One), Commentf("%d", pool.BalanceAsset.Uint64()))
	c.Check(pool.LPUnits.Uint64(), Equals, uint64(10*common.One), Commentf("%d", pool.LPUnits.Uint64()))
}

func (HandlerAddLiquiditySuite) TestAddSynth(c *C) {
	ctx, k := setupKeeperForTest(c)
	txID := GetRandomTxHash()

	asset := common.BTCAsset.GetSyntheticAsset()

	pool := NewPool()
	pool.Asset = asset
	pool.Status = PoolAvailable
	pool.BalanceRune = cosmos.NewUint(0)
	pool.BalanceAsset = cosmos.NewUint(100 * common.One)
	pool.LPUnits = cosmos.NewUint(100)
	c.Assert(k.SetPool(ctx, pool), IsNil)

	coin := common.NewCoin(asset, pool.BalanceAsset)
	c.Assert(k.MintToModule(ctx, ModuleName, coin), IsNil)
	c.Assert(k.SendFromModuleToModule(ctx, ModuleName, AsgardName, common.NewCoins(coin)), IsNil)

	addr := GetRandomBTCAddress()
	constAccessor := constants.GetConstantValues(GetCurrentVersion())
	h := NewAddLiquidityHandler(NewDummyMgrWithKeeper(k))
	addCoin := common.NewCoin(asset, cosmos.NewUint(100*common.One))
	c.Assert(k.MintToModule(ctx, ModuleName, addCoin), IsNil)
	c.Assert(k.SendFromModuleToModule(ctx, ModuleName, AsgardName, common.NewCoins(addCoin)), IsNil)
	err := h.addLiquidity(ctx, asset, cosmos.ZeroUint(), addCoin.Amount, common.NoAddress, addr, txID, false, constAccessor)
	c.Assert(err, IsNil)

	su, err := k.GetLiquidityProvider(ctx, asset, addr)
	c.Assert(err, IsNil)
	c.Check(su.Units.Uint64(), Equals, uint64(100), Commentf("%d", su.Units.Uint64()))

	pool, err = k.GetPool(ctx, asset)
	c.Assert(err, IsNil)
	c.Check(pool.BalanceRune.Uint64(), Equals, uint64(0), Commentf("%d", pool.BalanceRune.Uint64()))
	c.Check(pool.BalanceAsset.Uint64(), Equals, uint64(200*common.One), Commentf("%d", pool.BalanceAsset.Uint64()))
	c.Check(pool.LPUnits.Uint64(), Equals, uint64(200), Commentf("%d", pool.LPUnits.Uint64()))
}

func (s *HandlerAddLiquiditySuite) TestAddLiquidityPOL(c *C) {
	var err error
	ctx, mgr := setupManagerForTest(c)
	activeNodeAccount := GetRandomValidatorNode(NodeActive)
	polAddr := GetRandomRUNEAddress()
	pool := NewPool()
	pool.Asset = common.ETHAsset
	pool.Status = PoolAvailable
	pool.BalanceAsset = cosmos.NewUint(1)
	pool.BalanceRune = cosmos.NewUint(1)
	k := &MockAddLiquidityKeeper{
		activeNodeAccount: activeNodeAccount,
		currentPool:       pool,
		lp: LiquidityProvider{
			Asset:             common.ETHAsset,
			RuneAddress:       polAddr,
			AssetAddress:      common.NoAddress,
			Units:             cosmos.ZeroUint(),
			PendingRune:       cosmos.ZeroUint(),
			PendingAsset:      cosmos.ZeroUint(),
			RuneDepositValue:  cosmos.ZeroUint(),
			AssetDepositValue: cosmos.ZeroUint(),
		},
		pol:        NewProtocolOwnedLiquidity(),
		polAddress: polAddr,
	}
	mgr.K = k
	addHandler := NewAddLiquidityHandler(mgr)
	addTxHash := GetRandomTxHash()
	tx := common.NewTx(
		addTxHash,
		polAddr,
		polAddr,
		common.Coins{common.NewCoin(common.RuneAsset(), cosmos.NewUint(common.One*100))},
		common.Gas{},
		"add:ETH",
	)
	msg := NewMsgAddLiquidity(
		tx,
		common.ETHAsset,
		cosmos.NewUint(100*common.One),
		cosmos.ZeroUint(),
		polAddr,
		GetRandomETHAddress(),
		common.NoAddress, cosmos.ZeroUint(),
		activeNodeAccount.NodeAddress,
	)

	_, err = addHandler.Run(ctx, msg)
	c.Assert(err, NotNil, Commentf("pol add with asset addr should fail"))

	// happy path
	msg.AssetAddress = common.NoAddress
	_, err = addHandler.Run(ctx, msg)
	c.Assert(err, IsNil)

	postLiquidityPool, err := mgr.Keeper().GetPool(ctx, common.ETHAsset)
	c.Assert(err, IsNil)
	c.Assert(postLiquidityPool.BalanceAsset.String(), Equals, "1")
	c.Assert(postLiquidityPool.BalanceRune.String(), Equals, "10000000001")
	c.Assert(postLiquidityPool.PendingInboundAsset.String(), Equals, "0")
	c.Assert(postLiquidityPool.PendingInboundRune.String(), Equals, "0")

	pol, err := mgr.Keeper().GetPOL(ctx)
	c.Assert(err, IsNil)
	c.Check(pol.RuneDeposited.Uint64(), Equals, uint64(10000000000))
}

// TestChainLiquidityRunePerValidatorLimit tests that the ChainLiquidityRunePerValidator
// limit applies to all pools on a chain combined, not per individual pool
func (s *HandlerAddLiquiditySuite) TestChainLiquidityRunePerValidatorLimit(c *C) {
	ctx, mgr := setupManagerForTest(c)

	// Set ChainLiquidityRunePerValidator to 100k RUNE
	mgr.Keeper().SetMimir(ctx, constants.EnableNodeChainConsensus.String(), 1)
	mgr.Keeper().SetMimir(ctx, constants.ChainLiquidityRunePerValidator.String(), 100_000_00000000)

	// Create a test chain (BTC) that will NOT have super majority support
	// We'll create 4 validators total, but only 2 will be in BTC vaults
	// Super majority requires > 2/3, so 2/4 = 50% is not enough
	validators := make(NodeAccounts, 4)
	for i := 0; i < 4; i++ {
		validators[i] = GetRandomValidatorNode(NodeActive)
		validators[i].Bond = cosmos.NewUint(1_000_000 * common.One) // 1M RUNE bond each to pass TVL cap checks
		validators[i].ActiveBlockHeight = 10
		c.Assert(mgr.Keeper().SetNodeAccount(ctx, validators[i]), IsNil)
	}

	// Create first BTC pool (BTC.BTC gas asset)
	btcPool := NewPool()
	btcPool.Asset = common.BTCAsset
	btcPool.Status = PoolAvailable
	btcPool.BalanceRune = cosmos.NewUint(150_000_00000000) // 150k RUNE
	btcPool.BalanceAsset = cosmos.NewUint(150_000_00000000)
	c.Assert(mgr.Keeper().SetPool(ctx, btcPool), IsNil)

	// Create second BTC pool (BTC.TOKEN - a hypothetical BTC token)
	// Give it a 1:1 RUNE ratio for easy calculation
	btcTokenAsset, err := common.NewAsset("BTC.TOKEN")
	c.Assert(err, IsNil)
	btcTokenPool := NewPool()
	btcTokenPool.Asset = btcTokenAsset
	btcTokenPool.Status = PoolAvailable
	btcTokenPool.BalanceRune = cosmos.NewUint(1_00000000)  // 1 RUNE
	btcTokenPool.BalanceAsset = cosmos.NewUint(1_00000000) // 1 TOKEN (1:1 ratio)
	c.Assert(mgr.Keeper().SetPool(ctx, btcTokenPool), IsNil)

	// Set up vault for BTC with assets matching the pool balances
	// Vault has membership of validators 0 and 1 (2 out of 4 = 50%, no super majority)
	vault := GetRandomVault()
	vault.Chains = []string{common.BTCChain.String()}
	vault.Status = ActiveVault
	vault.Membership = []string{
		validators[0].PubKeySet.Secp256k1.String(),
		validators[1].PubKeySet.Secp256k1.String(),
	}
	vault.Coins = common.NewCoins(
		common.NewCoin(common.BTCAsset, cosmos.NewUint(150_000_00000000)), // 150k BTC in vault = 150k RUNE value (1:1 ratio)
		common.NewCoin(btcTokenAsset, cosmos.NewUint(1_00000000)),         // 1 TOKEN in vault = 1 RUNE value (to match pool)
	)
	c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)

	// With 2 validators in BTC vault and a limit of 100k per validator,
	// the total chain limit should be 200k RUNE

	// Fund the bond module for validator bonds
	FundModule(c, ctx, mgr.Keeper(), BondName, 4_000_000*common.One) // 4M RUNE for 4 validators

	addHandler := NewAddLiquidityHandler(mgr)

	// Test 1: Try to add 50k RUNE to BTC.TOKEN pool
	// Current vault RUNE value: 150k BTC + 1 TOKEN = 150,001 RUNE
	// This should FAIL because total chain RUNE would be 150,001 + 50k = 200,001, exceeding the 200k limit
	runeAddr := GetRandomRUNEAddress()
	btcAddr := GetRandomBTCAddress()

	tx1 := GetRandomTx()
	tx1.Coins = common.NewCoins(common.NewCoin(common.RuneAsset(), cosmos.NewUint(50_000_00000000)))
	tx1.Chain = common.THORChain

	msg := NewMsgAddLiquidity(
		tx1,
		btcTokenAsset,
		cosmos.NewUint(50_000_00000000), // 50k RUNE
		cosmos.ZeroUint(),
		runeAddr,
		btcAddr,
		common.NoAddress,
		cosmos.ZeroUint(),
		validators[0].NodeAddress,
	)

	_, err = addHandler.Run(ctx, msg)
	c.Assert(err, NotNil)
	c.Check(err.Error(), Matches, ".*exceeds total RUNE limit.*")

	// Test 2: Try to add 49,999 RUNE to BTC.TOKEN pool
	// This should SUCCEED because total chain RUNE would be 150,001 + 49,999 = 200,000, exactly at the limit
	tx2 := GetRandomTx()
	tx2.Coins = common.NewCoins(common.NewCoin(common.RuneAsset(), cosmos.NewUint(49_999_00000000)))
	tx2.Chain = common.THORChain
	msg.Tx = tx2
	msg.RuneAmount = cosmos.NewUint(49_999_00000000)

	_, err = addHandler.Run(ctx, msg)
	c.Assert(err, IsNil)

	// Verify the pool was updated (RUNE goes to pending inbound)
	updatedBtcTokenPool, err := mgr.Keeper().GetPool(ctx, btcTokenAsset)
	c.Assert(err, IsNil)
	c.Check(updatedBtcTokenPool.PendingInboundRune.String(), Equals, "4999900000000")

	// Test 3: Update vault to include the pending RUNE (simulating it being processed)
	// then try to add even 1 more RUNE - should FAIL because we're at the limit
	vault.Coins = common.NewCoins(
		common.NewCoin(common.BTCAsset, cosmos.NewUint(150_000_00000000)),
		common.NewCoin(btcTokenAsset, cosmos.NewUint(49_999_00000000+1_00000000)), // Add the 49,999 from test 2 + original 1
	)
	c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)

	tx3 := GetRandomTx()
	tx3.Coins = common.NewCoins(common.NewCoin(common.RuneAsset(), cosmos.NewUint(1)))
	tx3.Chain = common.THORChain
	msg.Tx = tx3
	msg.RuneAmount = cosmos.NewUint(1)

	_, err = addHandler.Run(ctx, msg)
	c.Assert(err, NotNil)
	c.Check(err.Error(), Matches, ".*exceeds total RUNE limit.*")

	// Test 4: Verify that a chain with super majority support (like ETH) is NOT subject to the limit
	// Create ETH vault with all 4 validators as members (4/4 = 100%, has super majority)
	ethPool := NewPool()
	ethPool.Asset = common.ETHAsset
	ethPool.Status = PoolAvailable
	ethPool.BalanceRune = cosmos.NewUint(500_000_00000000) // 500k RUNE (way above the per-validator limit)
	ethPool.BalanceAsset = cosmos.NewUint(500_000_00000000)
	c.Assert(mgr.Keeper().SetPool(ctx, ethPool), IsNil)

	ethVault := GetRandomVault()
	ethVault.Chains = []string{common.ETHChain.String()}
	ethVault.Status = ActiveVault
	ethVault.Membership = []string{
		validators[0].PubKeySet.Secp256k1.String(),
		validators[1].PubKeySet.Secp256k1.String(),
		validators[2].PubKeySet.Secp256k1.String(),
		validators[3].PubKeySet.Secp256k1.String(),
	}
	ethVault.Coins = common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(500_000_00000000)))
	c.Assert(mgr.Keeper().SetVault(ctx, ethVault), IsNil)

	ethAddr := GetRandomETHAddress()
	tx4 := GetRandomTx()
	tx4.Coins = common.NewCoins(common.NewCoin(common.RuneAsset(), cosmos.NewUint(1_000_000_00000000)))
	tx4.Chain = common.THORChain

	ethMsg := NewMsgAddLiquidity(
		tx4,
		common.ETHAsset,
		cosmos.NewUint(1_000_000_00000000), // 1M RUNE - way above the 400k limit if it applied
		cosmos.ZeroUint(),
		runeAddr,
		ethAddr,
		common.NoAddress,
		cosmos.ZeroUint(),
		validators[0].NodeAddress,
	)

	// This should succeed because ETH has super majority support
	_, err = addHandler.Run(ctx, ethMsg)
	c.Assert(err, IsNil)
}

// TestChainLiquidityRunePerValidatorVaultBased tests that the ChainLiquidityRunePerValidator
// limit correctly calculates total chain liquidity from vault coins, including secured assets,
// trade assets, and assets from both active and retiring vaults
func (s *HandlerAddLiquiditySuite) TestChainLiquidityRunePerValidatorVaultBased(c *C) {
	ctx, mgr := setupManagerForTest(c)

	// Set ChainLiquidityRunePerValidator to 100k RUNE
	mgr.Keeper().SetMimir(ctx, constants.EnableNodeChainConsensus.String(), 1)
	mgr.Keeper().SetMimir(ctx, constants.ChainLiquidityRunePerValidator.String(), 100_000_00000000)

	// Create 4 validators
	validators := make(NodeAccounts, 4)
	for i := 0; i < 4; i++ {
		validators[i] = GetRandomValidatorNode(NodeActive)
		validators[i].Bond = cosmos.NewUint(1_000_000 * common.One)
		validators[i].ActiveBlockHeight = 10
		c.Assert(mgr.Keeper().SetNodeAccount(ctx, validators[i]), IsNil)
	}

	// With 2 validators supporting BTC (via vault membership), the total chain limit is 200k RUNE

	// Create BTC pool (gas asset)
	btcPool := NewPool()
	btcPool.Asset = common.BTCAsset
	btcPool.Status = PoolAvailable
	btcPool.BalanceRune = cosmos.NewUint(100_000_00000000) // 100k RUNE
	btcPool.BalanceAsset = cosmos.NewUint(10_00000000)     // 10 BTC at 10k RUNE/BTC
	c.Assert(mgr.Keeper().SetPool(ctx, btcPool), IsNil)

	// Create BTC.USDT pool (secured asset / token on BTC chain)
	btcUSDTAsset, err := common.NewAsset("BTC.USDT")
	c.Assert(err, IsNil)
	btcUSDTPool := NewPool()
	btcUSDTPool.Asset = btcUSDTAsset
	btcUSDTPool.Status = PoolAvailable
	btcUSDTPool.BalanceRune = cosmos.NewUint(50_000_00000000) // 50k RUNE
	btcUSDTPool.BalanceAsset = cosmos.NewUint(50_000_000000)  // 50k USDT at 1 RUNE/USDT
	c.Assert(mgr.Keeper().SetPool(ctx, btcUSDTPool), IsNil)

	// Create ETH pool (to test cross-chain filtering)
	ethPool := NewPool()
	ethPool.Asset = common.ETHAsset
	ethPool.Status = PoolAvailable
	ethPool.BalanceRune = cosmos.NewUint(100_000_00000000)
	ethPool.BalanceAsset = cosmos.NewUint(100_00000000)
	c.Assert(mgr.Keeper().SetPool(ctx, ethPool), IsNil)

	// Create active vault with BTC, BTC.USDT, and ETH
	// Only validators 0 and 1 are members of this vault for BTC support
	activeVault := GetRandomVault()
	activeVault.Status = ActiveVault
	activeVault.Chains = []string{common.BTCChain.String(), common.ETHChain.String()}
	activeVault.Membership = []string{
		validators[0].PubKeySet.Secp256k1.String(),
		validators[1].PubKeySet.Secp256k1.String(),
	}
	activeVault.Coins = common.NewCoins(
		common.NewCoin(common.BTCAsset, cosmos.NewUint(5_00000000)),  // 5 BTC = 50k RUNE
		common.NewCoin(btcUSDTAsset, cosmos.NewUint(30_000_000000)),  // 30k USDT = 30k RUNE
		common.NewCoin(common.ETHAsset, cosmos.NewUint(50_00000000)), // 50 ETH = 50k RUNE (should NOT count toward BTC limit)
	)
	c.Assert(mgr.Keeper().SetVault(ctx, activeVault), IsNil)

	// Create retiring vault with more BTC assets
	// Same validators 0 and 1 are members
	retiringVault := GetRandomVault()
	retiringVault.Status = RetiringVault
	retiringVault.Chains = []string{common.BTCChain.String()}
	retiringVault.Membership = []string{
		validators[0].PubKeySet.Secp256k1.String(),
		validators[1].PubKeySet.Secp256k1.String(),
	}
	retiringVault.Coins = common.NewCoins(
		common.NewCoin(common.BTCAsset, cosmos.NewUint(5_00000000)), // 5 BTC = 50k RUNE
		common.NewCoin(btcUSDTAsset, cosmos.NewUint(20_000_000000)), // 20k USDT = 20k RUNE
	)
	c.Assert(mgr.Keeper().SetVault(ctx, retiringVault), IsNil)

	// Total BTC chain RUNE in vaults:
	// Active vault: 5 BTC (50k RUNE) + 30k USDT (30k RUNE) = 80k RUNE
	// Retiring vault: 5 BTC (50k RUNE) + 20k USDT (20k RUNE) = 70k RUNE
	// Total: 150k RUNE
	// Limit: 200k RUNE
	// Available: 50k RUNE

	// Fund the bond module
	FundModule(c, ctx, mgr.Keeper(), BondName, 4_000_000*common.One)

	addHandler := NewAddLiquidityHandler(mgr)

	// Create another BTC token pool to add liquidity to
	btcTokenAsset, err := common.NewAsset("BTC.TOKEN")
	c.Assert(err, IsNil)
	btcTokenPool := NewPool()
	btcTokenPool.Asset = btcTokenAsset
	btcTokenPool.Status = PoolAvailable
	btcTokenPool.BalanceRune = cosmos.ZeroUint()
	btcTokenPool.BalanceAsset = cosmos.ZeroUint()
	c.Assert(mgr.Keeper().SetPool(ctx, btcTokenPool), IsNil)

	runeAddr := GetRandomRUNEAddress()
	btcAddr := GetRandomBTCAddress()

	// Test 1: Try to add 51k RUNE to BTC.TOKEN pool
	// This should FAIL because 150k + 51k = 201k > 200k limit
	tx1 := GetRandomTx()
	tx1.Coins = common.NewCoins(common.NewCoin(common.RuneAsset(), cosmos.NewUint(51_000_00000000)))
	tx1.Chain = common.THORChain

	msg := NewMsgAddLiquidity(
		tx1,
		btcTokenAsset,
		cosmos.NewUint(51_000_00000000),
		cosmos.ZeroUint(),
		runeAddr,
		btcAddr,
		common.NoAddress,
		cosmos.ZeroUint(),
		validators[0].NodeAddress,
	)

	_, err = addHandler.Run(ctx, msg)
	c.Assert(err, NotNil)
	c.Check(err.Error(), Matches, ".*exceeds total RUNE limit.*")

	// Test 2: Try to add 50k RUNE to BTC.TOKEN pool
	// This should SUCCEED because 150k + 50k = 200k = limit
	tx2 := GetRandomTx()
	tx2.Coins = common.NewCoins(common.NewCoin(common.RuneAsset(), cosmos.NewUint(50_000_00000000)))
	tx2.Chain = common.THORChain
	msg.Tx = tx2
	msg.RuneAmount = cosmos.NewUint(50_000_00000000)

	_, err = addHandler.Run(ctx, msg)
	c.Assert(err, IsNil)

	// Test 3: Verify ETH assets in the vault were NOT counted toward BTC limit
	// by checking that we can add more to ETH even though the same vault has BTC assets
	// Create a separate ETH vault with all 4 validators as members (super majority)
	ethOnlyVault := GetRandomVault()
	ethOnlyVault.Status = ActiveVault
	ethOnlyVault.Chains = []string{common.ETHChain.String()}
	ethOnlyVault.Membership = []string{
		validators[0].PubKeySet.Secp256k1.String(),
		validators[1].PubKeySet.Secp256k1.String(),
		validators[2].PubKeySet.Secp256k1.String(),
		validators[3].PubKeySet.Secp256k1.String(),
	}
	c.Assert(mgr.Keeper().SetVault(ctx, ethOnlyVault), IsNil)

	ethAddr := GetRandomETHAddress()
	tx3 := GetRandomTx()
	tx3.Coins = common.NewCoins(common.NewCoin(common.RuneAsset(), cosmos.NewUint(1_000_000_00000000)))
	tx3.Chain = common.THORChain

	ethMsg := NewMsgAddLiquidity(
		tx3,
		common.ETHAsset,
		cosmos.NewUint(1_000_000_00000000),
		cosmos.ZeroUint(),
		runeAddr,
		ethAddr,
		common.NoAddress,
		cosmos.ZeroUint(),
		validators[0].NodeAddress,
	)

	// Should succeed because ETH has super majority (all 4 validators)
	_, err = addHandler.Run(ctx, ethMsg)
	c.Assert(err, IsNil)
}

func (s *HandlerAddLiquiditySuite) TestCountActiveValidatorsForChain(c *C) {
	ctx, mgr := setupManagerForTest(c)

	// Create 6 validators
	validators := make(NodeAccounts, 6)
	for i := 0; i < 6; i++ {
		validators[i] = GetRandomValidatorNode(NodeActive)
		validators[i].Bond = cosmos.NewUint(1_000_000 * common.One)
		validators[i].ActiveBlockHeight = 10
		c.Assert(mgr.Keeper().SetNodeAccount(ctx, validators[i]), IsNil)
	}

	// Create vaults with different chain support and memberships
	// Vault 1: BTC vault with validators 0, 2, 5
	btcVault := GetRandomVault()
	btcVault.Status = ActiveVault
	btcVault.Chains = []string{common.BTCChain.String()}
	btcVault.Membership = []string{
		validators[0].PubKeySet.Secp256k1.String(),
		validators[2].PubKeySet.Secp256k1.String(),
		validators[5].PubKeySet.Secp256k1.String(),
	}
	c.Assert(mgr.Keeper().SetVault(ctx, btcVault), IsNil)

	// Vault 2: ETH vault with validators 1, 2, 5
	ethVault := GetRandomVault()
	ethVault.Status = ActiveVault
	ethVault.Chains = []string{common.ETHChain.String()}
	ethVault.Membership = []string{
		validators[1].PubKeySet.Secp256k1.String(),
		validators[2].PubKeySet.Secp256k1.String(),
		validators[5].PubKeySet.Secp256k1.String(),
	}
	c.Assert(mgr.Keeper().SetVault(ctx, ethVault), IsNil)

	// Vault 3: GAIA vault with validators 3, 5
	gaiaVault := GetRandomVault()
	gaiaVault.Status = ActiveVault
	gaiaVault.Chains = []string{common.GAIAChain.String()}
	gaiaVault.Membership = []string{
		validators[3].PubKeySet.Secp256k1.String(),
		validators[5].PubKeySet.Secp256k1.String(),
	}
	c.Assert(mgr.Keeper().SetVault(ctx, gaiaVault), IsNil)

	// Vault 4: Multi-chain vault (BTC+ETH) with validator 4
	multiVault := GetRandomVault()
	multiVault.Status = ActiveVault
	multiVault.Chains = []string{common.BTCChain.String(), common.ETHChain.String()}
	multiVault.Membership = []string{
		validators[4].PubKeySet.Secp256k1.String(),
	}
	c.Assert(mgr.Keeper().SetVault(ctx, multiVault), IsNil)

	// Test 1: Count BTC validators (should be 4: validators 0, 2, 4, 5)
	btcCount, err := countActiveValidatorsForChain(ctx, mgr, common.BTCChain)
	c.Assert(err, IsNil)
	c.Check(btcCount, Equals, 4, Commentf("Expected 4 BTC validators, got %d", btcCount))

	// Test 2: Count ETH validators (should be 4: validators 1, 2, 4, 5)
	ethCount, err := countActiveValidatorsForChain(ctx, mgr, common.ETHChain)
	c.Assert(err, IsNil)
	c.Check(ethCount, Equals, 4, Commentf("Expected 4 ETH validators, got %d", ethCount))

	// Test 3: Count GAIA validators (should be 2: validators 3, 5)
	gaiaCount, err := countActiveValidatorsForChain(ctx, mgr, common.GAIAChain)
	c.Assert(err, IsNil)
	c.Check(gaiaCount, Equals, 2, Commentf("Expected 2 GAIA validators, got %d", gaiaCount))

	// Test 4: Count LTC validators (should be 0: no vaults support it)
	ltcCount, err := countActiveValidatorsForChain(ctx, mgr, common.LTCChain)
	c.Assert(err, IsNil)
	c.Check(ltcCount, Equals, 0, Commentf("Expected 0 LTC validators, got %d", ltcCount))

	// Test 5: Verify case insensitivity
	btcCount2, err := countActiveValidatorsForChain(ctx, mgr, common.BTCChain)
	c.Assert(err, IsNil)
	c.Check(btcCount2, Equals, btcCount, Commentf("Case sensitivity check failed"))

	// Test 6: Set all validators to standby, verify count becomes 0
	for i := 0; i < 6; i++ {
		validators[i].Status = NodeStandby
		c.Assert(mgr.Keeper().SetNodeAccount(ctx, validators[i]), IsNil)
	}

	btcCountStandby, err := countActiveValidatorsForChain(ctx, mgr, common.BTCChain)
	c.Assert(err, IsNil)
	c.Check(btcCountStandby, Equals, 0, Commentf("Expected 0 active BTC validators when all are standby"))

	// Test 7: Set validators back to active and verify counts are restored
	for i := 0; i < 6; i++ {
		validators[i].Status = NodeActive
		c.Assert(mgr.Keeper().SetNodeAccount(ctx, validators[i]), IsNil)
	}

	btcCountRestored, err := countActiveValidatorsForChain(ctx, mgr, common.BTCChain)
	c.Assert(err, IsNil)
	c.Check(btcCountRestored, Equals, 4, Commentf("Expected 4 BTC validators after restoring active status"))

	// Test 8: Retiring vault members should also count
	retiringVault := GetRandomVault()
	retiringVault.Status = RetiringVault
	retiringVault.Chains = []string{common.LTCChain.String()}
	retiringVault.Membership = []string{
		validators[0].PubKeySet.Secp256k1.String(),
		validators[1].PubKeySet.Secp256k1.String(),
	}
	c.Assert(mgr.Keeper().SetVault(ctx, retiringVault), IsNil)

	ltcCountWithRetiring, err := countActiveValidatorsForChain(ctx, mgr, common.LTCChain)
	c.Assert(err, IsNil)
	c.Check(ltcCountWithRetiring, Equals, 2, Commentf("Expected 2 LTC validators from retiring vault"))
}

func (s *HandlerAddLiquiditySuite) TestChainLiquidityValidationEdgeCases(c *C) {
	ctx, mgr := setupManagerForTest(c)

	// Set ChainLiquidityRunePerValidator to 100k RUNE
	mgr.Keeper().SetMimir(ctx, constants.EnableNodeChainConsensus.String(), 1)
	mgr.Keeper().SetMimir(ctx, constants.ChainLiquidityRunePerValidator.String(), 100_000_00000000)

	// Create 4 validators, only 2 support BTC (no super majority)
	validators := make(NodeAccounts, 4)
	for i := 0; i < 4; i++ {
		validators[i] = GetRandomValidatorNode(NodeActive)
		validators[i].Bond = cosmos.NewUint(1_000_000 * common.One)
		validators[i].ActiveBlockHeight = 10

		if i < 2 {
			validators[i].SupportedChains = []string{common.BTCChain.String()}
		} else {
			validators[i].SupportedChains = []string{common.ETHChain.String()}
		}

		c.Assert(mgr.Keeper().SetNodeAccount(ctx, validators[i]), IsNil)
	}

	// Fund the bond module
	FundModule(c, ctx, mgr.Keeper(), BondName, 4_000_000*common.One)

	addHandler := NewAddLiquidityHandler(mgr)

	// Test 1: Synthetic assets should skip the chain liquidity check
	btcSynthAsset := common.BTCAsset.GetSyntheticAsset()
	btcPool := NewPool()
	btcPool.Asset = common.BTCAsset
	btcPool.Status = PoolAvailable
	btcPool.BalanceRune = cosmos.NewUint(1_00000000)
	btcPool.BalanceAsset = cosmos.NewUint(1_00000000)
	c.Assert(mgr.Keeper().SetPool(ctx, btcPool), IsNil)

	// Create vault with BTC
	vault := GetRandomVault()
	vault.Chains = []string{common.BTCChain.String()}
	vault.Status = ActiveVault
	vault.Coins = common.NewCoins(common.NewCoin(common.BTCAsset, cosmos.NewUint(1_00000000)))
	c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)

	runeAddr := GetRandomRUNEAddress()
	btcAddr := GetRandomBTCAddress()
	tx1 := GetRandomTx()
	tx1.Coins = common.NewCoins(common.NewCoin(btcSynthAsset, cosmos.NewUint(1_000_000_00000000)))
	tx1.Chain = common.BTCChain

	// Synthetic asset add should not be subject to chain RUNE limit
	// (it would fail if the limit was applied since we're trying to add 1M RUNE worth)
	synthMsg := NewMsgAddLiquidity(
		tx1,
		btcSynthAsset,
		cosmos.ZeroUint(), // Synth deposits don't add RUNE
		cosmos.NewUint(1_000_000_00000000),
		runeAddr,
		btcAddr,
		common.NoAddress,
		cosmos.ZeroUint(),
		validators[0].NodeAddress,
	)

	_, err := addHandler.Run(ctx, synthMsg)
	// Note: This may fail for other reasons (like rune amount being zero for synths),
	// but should NOT fail due to chain RUNE limit
	if err != nil {
		c.Check(err.Error(), Not(Matches), ".*exceeds total RUNE limit.*",
			Commentf("Synthetic asset should not trigger chain RUNE limit"))
	}

	// Test 2: When ChainLiquidityRunePerValidator is 0, the check should be disabled
	mgr.Keeper().SetMimir(ctx, constants.ChainLiquidityRunePerValidator.String(), 0)

	btcTokenAsset, err := common.NewAsset("BTC.TOKEN")
	c.Assert(err, IsNil)
	btcTokenPool := NewPool()
	btcTokenPool.Asset = btcTokenAsset
	btcTokenPool.Status = PoolAvailable
	btcTokenPool.BalanceRune = cosmos.NewUint(1_00000000)
	btcTokenPool.BalanceAsset = cosmos.NewUint(1_00000000)
	c.Assert(mgr.Keeper().SetPool(ctx, btcTokenPool), IsNil)

	tx2 := GetRandomTx()
	tx2.Coins = common.NewCoins(common.NewCoin(common.RuneAsset(), cosmos.NewUint(1_000_000_00000000)))
	tx2.Chain = common.THORChain

	// Should succeed even with huge RUNE amount since limit is disabled (0)
	msg := NewMsgAddLiquidity(
		tx2,
		btcTokenAsset,
		cosmos.NewUint(1_000_000_00000000), // 1M RUNE
		cosmos.ZeroUint(),
		runeAddr,
		btcAddr,
		common.NoAddress,
		cosmos.ZeroUint(),
		validators[0].NodeAddress,
	)

	_, err = addHandler.Run(ctx, msg)
	c.Assert(err, IsNil, Commentf("Should succeed when ChainLiquidityRunePerValidator is 0"))
}

func (s *HandlerAddLiquiditySuite) TestChainLiquidityWithNoValidators(c *C) {
	ctx, mgr := setupManagerForTest(c)

	// Set ChainLiquidityRunePerValidator to 100k RUNE
	mgr.Keeper().SetMimir(ctx, constants.EnableNodeChainConsensus.String(), 1)
	mgr.Keeper().SetMimir(ctx, constants.ChainLiquidityRunePerValidator.String(), 100_000_00000000)

	// Create 4 validators, NONE support BTC
	validators := make(NodeAccounts, 4)
	for i := 0; i < 4; i++ {
		validators[i] = GetRandomValidatorNode(NodeActive)
		validators[i].Bond = cosmos.NewUint(1_000_000 * common.One)
		validators[i].ActiveBlockHeight = 10
		validators[i].SupportedChains = []string{common.ETHChain.String()}
		c.Assert(mgr.Keeper().SetNodeAccount(ctx, validators[i]), IsNil)
	}

	// Fund the bond module
	FundModule(c, ctx, mgr.Keeper(), BondName, 4_000_000*common.One)

	// Create BTC pool
	btcPool := NewPool()
	btcPool.Asset = common.BTCAsset
	btcPool.Status = PoolAvailable
	btcPool.BalanceRune = cosmos.NewUint(1_00000000)
	btcPool.BalanceAsset = cosmos.NewUint(1_00000000)
	c.Assert(mgr.Keeper().SetPool(ctx, btcPool), IsNil)

	// Create vault
	vault := GetRandomVault()
	vault.Chains = []string{common.BTCChain.String()}
	vault.Status = ActiveVault
	vault.Coins = common.NewCoins(common.NewCoin(common.BTCAsset, cosmos.NewUint(1_00000000)))
	c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)

	addHandler := NewAddLiquidityHandler(mgr)

	runeAddr := GetRandomRUNEAddress()
	btcAddr := GetRandomBTCAddress()
	tx := GetRandomTx()
	tx.Coins = common.NewCoins(common.NewCoin(common.RuneAsset(), cosmos.NewUint(1)))
	tx.Chain = common.THORChain

	// Try to add even 1 RUNE to BTC pool
	// With 0 validators supporting BTC, the limit check should be skipped (bootstrapping phase)
	// So this should succeed
	msg := NewMsgAddLiquidity(
		tx,
		common.BTCAsset,
		cosmos.NewUint(1),
		cosmos.ZeroUint(),
		runeAddr,
		btcAddr,
		common.NoAddress,
		cosmos.ZeroUint(),
		validators[0].NodeAddress,
	)

	_, err := addHandler.Run(ctx, msg)
	c.Assert(err, IsNil, Commentf("Should succeed when no validators support the chain (bootstrapping)"))
}

// The observed inbound is credited to its vault before the deposit handler
// runs, so the cap must not count the deposit's asset a second time.
func (s *HandlerAddLiquiditySuite) TestChainLiquidityCapCountsCreditedInboundOnce(c *C) {
	ctx, mgr := setupManagerForTest(c)
	mgr.Keeper().SetMimir(ctx, constants.EnableNodeChainConsensus.String(), 1)
	// 1 of 4 validators supports BTC: limit 100 RUNE.
	mgr.Keeper().SetMimir(ctx, constants.ChainLiquidityRunePerValidator.String(), 100*common.One)
	validators := make(NodeAccounts, 4)
	for i := range validators {
		validators[i] = GetRandomValidatorNode(NodeActive)
		c.Assert(mgr.Keeper().SetNodeAccount(ctx, validators[i]), IsNil)
	}
	pool := NewPool()
	pool.Asset = common.BTCAsset
	pool.Status = PoolAvailable
	pool.BalanceRune = cosmos.NewUint(1000 * common.One)
	pool.BalanceAsset = cosmos.NewUint(1000 * common.One)
	c.Assert(mgr.Keeper().SetPool(ctx, pool), IsNil)

	// 80 RUNE of existing custody plus a 15 RUNE deposit already credited.
	vault := GetRandomVault()
	vault.Chains = []string{common.BTCChain.String()}
	vault.Status = ActiveVault
	vault.Membership = []string{validators[0].PubKeySet.Secp256k1.String()}
	vault.Coins = common.NewCoins(common.NewCoin(common.BTCAsset, cosmos.NewUint(95*common.One)))
	c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)

	c.Assert(validateChainLiquidityCap(ctx, mgr, common.BTCChain, cosmos.ZeroUint()), IsNil)
	// RUNE paired into the pool is not held by any vault and still counts.
	c.Assert(validateChainLiquidityCap(ctx, mgr, common.BTCChain, cosmos.NewUint(5*common.One)), IsNil)
	c.Assert(validateChainLiquidityCap(ctx, mgr, common.BTCChain, cosmos.NewUint(6*common.One)), NotNil)
}
