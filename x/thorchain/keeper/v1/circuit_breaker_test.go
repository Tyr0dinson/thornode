package keeperv1

import (
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

type CircuitBreakerSuite struct{}

var _ = Suite(&CircuitBreakerSuite{})

func (s *CircuitBreakerSuite) TestAsgardSolvencyCircuitBreaker(c *C) {
	ctx, k := setupKeeperForTest(c)

	// empty the starting balance of asgard
	runeBal := k.GetRuneBalanceOfModule(ctx, AsgardName)
	coins := common.NewCoins(common.NewCoin(common.RuneAsset(), runeBal))
	c.Assert(k.SendFromModuleToModule(ctx, AsgardName, ReserveName, coins), IsNil)

	pool := NewPool()
	pool.Asset = common.BTCAsset
	pool.BalanceRune = cosmos.NewUint(1000)
	pool.PendingInboundRune = cosmos.NewUint(100)
	c.Assert(k.SetPool(ctx, pool), IsNil)

	cb := AsgardSolvencyCircuitBreaker(k)

	// asgard has no rune, so it should be insolvent
	check := cb(ctx)
	c.Assert(check.Expected.GT(check.Actual), Equals, true)
	c.Assert(check.Asset, Equals, common.RuneAsset().Native())
	c.Assert(len(check.Msgs) > 0, Equals, true)

	// send the expected amount to asgard
	expCoins := common.NewCoins(
		common.NewCoin(common.RuneAsset(), cosmos.NewUint(1100)),
	)
	c.Assert(k.MintToModule(ctx, ModuleName, expCoins[0]), IsNil)
	c.Assert(k.SendFromModuleToModule(ctx, ModuleName, AsgardName, expCoins), IsNil)

	// now it should be solvent
	check = cb(ctx)
	c.Assert(check.Actual.GTE(check.Expected), Equals, true)
}

func (s *CircuitBreakerSuite) TestBondSolvencyCircuitBreaker(c *C) {
	ctx, k := setupKeeperForTest(c)

	node := GetRandomValidatorNode(NodeActive)
	node.Bond = cosmos.NewUint(1000)
	c.Assert(k.SetNodeAccount(ctx, node), IsNil)

	network := NewNetwork()
	network.BondRewardRune = cosmos.NewUint(2000)
	c.Assert(k.SetNetwork(ctx, network), IsNil)

	cb := BondSolvencyCircuitBreaker(k)

	// bond module has no rune, so it should be insolvent
	check := cb(ctx)
	c.Assert(check.Expected.GT(check.Actual), Equals, true)
	c.Assert(check.Asset, Equals, common.RuneAsset().Native())
	c.Assert(len(check.Msgs) > 0, Equals, true)

	// send the expected amount to bond module
	expRune := common.NewCoin(common.RuneAsset(), cosmos.NewUint(3000))
	c.Assert(k.MintToModule(ctx, ModuleName, expRune), IsNil)
	c.Assert(k.SendFromModuleToModule(ctx, ModuleName, BondName, common.NewCoins(expRune)), IsNil)

	// now it should be solvent
	check = cb(ctx)
	c.Assert(check.Actual.GTE(check.Expected), Equals, true)
}

func (s *CircuitBreakerSuite) TestRecordInsolvencySubBps(c *C) {
	result := common.CircuitBreakerCheck{}
	worstBps := cosmos.ZeroUint()

	// a sub-BPS shortfall must still populate the worst-insolvency fields —
	// with floor rounding it computed 0 BPS and the fields stayed empty, so
	// the runner had nothing to compare against tolerance
	recordInsolvency(&result, &worstBps, common.RuneAsset().Native(), cosmos.NewUint(19999), cosmos.NewUint(20000))

	c.Assert(worstBps.Equal(cosmos.NewUint(1)), Equals, true)
	c.Assert(result.Asset, Equals, common.RuneAsset().Native())
	c.Assert(result.Actual.Equal(cosmos.NewUint(19999)), Equals, true)
	c.Assert(result.Expected.Equal(cosmos.NewUint(20000)), Equals, true)
}

func (s *CircuitBreakerSuite) TestRecordInsolvencyMaxUint(c *C) {
	result := common.CircuitBreakerCheck{}
	worstBps := cosmos.ZeroUint()
	maxUint := cosmos.NewUintFromString("115792089237316195423570985008687907853269984665640564039457584007913129639935")

	// The intermediate shortfall * 10000 exceeds 256 bits. Selection of the
	// worst insolvency must still complete without panicking.
	recordInsolvency(&result, &worstBps, common.RuneAsset().Native(), cosmos.ZeroUint(), maxUint)

	c.Assert(worstBps.Equal(cosmos.NewUint(10000)), Equals, true)
	c.Assert(result.Actual.IsZero(), Equals, true)
	c.Assert(result.Expected.Equal(maxUint), Equals, true)
}

func (s *CircuitBreakerSuite) TestVaultDeficitCircuitBreakerNormalDeficit(c *C) {
	ctx, k := setupKeeperForTest(c)

	drained := NewVault(ctx.BlockHeight(), RetiringVault, AsgardVault, GetRandomPubKey(), common.Chains{common.ETHChain}.Strings(), []ChainContract{})
	drained.Coins = common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(100)))
	missed := drained.SubFundsWithDeficit(common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(250))))
	c.Assert(missed, HasLen, 1)
	c.Assert(missed[0].Amount.Equal(cosmos.NewUint(150)), Equals, true)
	c.Assert(drained.ValidateDeficitInvariant(), IsNil)
	c.Assert(k.SetVault(ctx, drained), IsNil)

	// The invariant is per vault: a different vault may legitimately hold the
	// same asset while this vault's observed debit remains unsettled.
	funded := NewVault(ctx.BlockHeight(), ActiveVault, AsgardVault, GetRandomPubKey(), common.Chains{common.ETHChain}.Strings(), []ChainContract{})
	funded.Coins = common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(900)))
	c.Assert(k.SetVault(ctx, funded), IsNil)

	check := VaultDeficitCircuitBreaker(k)(ctx)
	c.Assert(check.Broken, Equals, false)
	c.Assert(check.Actual.IsZero(), Equals, true)
	c.Assert(check.Expected.IsZero(), Equals, true)
	c.Assert(check.Asset, Equals, "")
	c.Assert(check.Msgs, HasLen, 0)
}

func (s *CircuitBreakerSuite) TestVaultDeficitCircuitBreakerBrokenInvariant(c *C) {
	ctx, k := setupKeeperForTest(c)

	vault := NewVault(ctx.BlockHeight(), ActiveVault, AsgardVault, GetRandomPubKey(), common.Chains{common.ETHChain}.Strings(), []ChainContract{})
	vault.Coins = common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(10)))
	vault.Deficit = common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(1)))
	c.Assert(k.SetVault(ctx, vault), IsNil)

	check := VaultDeficitCircuitBreaker(k)(ctx)
	c.Assert(check.Broken, Equals, true)
	c.Assert(check.Indeterminate, Equals, false)
	c.Assert(check.Actual.Equal(cosmos.NewUint(10)), Equals, true)
	c.Assert(check.Expected.Equal(cosmos.NewUint(11)), Equals, true)
	c.Assert(check.Asset, Equals, common.ETHAsset.String())
	c.Assert(check.Msgs, HasLen, 1)
	c.Assert(check.Msgs[0], Matches, "vault_deficit: .*has both coins and deficit.*")
}

func (s *CircuitBreakerSuite) TestVaultDeficitCircuitBreakerCorruptVault(c *C) {
	ctx, k := setupKeeperForTest(c)

	c.Assert(k.storeService.OpenKVStore(ctx).Set(k.GetKey(prefixVault, "corrupt"), []byte("garbage")), IsNil)

	// unreadable records are indeterminate, not proven violations
	check := VaultDeficitCircuitBreaker(k)(ctx)
	c.Assert(check.Broken, Equals, false)
	c.Assert(check.Indeterminate, Equals, true)
	c.Assert(check.Msgs, HasLen, 1)
	c.Assert(check.Msgs[0], Matches, "fail to unmarshal vault:.*")
}

func (s *CircuitBreakerSuite) TestVaultDeficitCircuitBreakerOverflowingAmounts(c *C) {
	ctx, k := setupKeeperForTest(c)

	// a vault recording a maximum-Uint balance plus a deficit for the same
	// asset is corrupt twice over — reporting it must saturate the summed
	// Expected instead of panicking EndBlock
	maxUint := cosmos.NewUintFromString("115792089237316195423570985008687907853269984665640564039457584007913129639935")
	vault := NewVault(1, ActiveVault, AsgardVault, GetRandomPubKey(), common.Chains{common.ETHChain}.Strings(), nil)
	vault.Coins = common.Coins{common.NewCoin(common.ETHAsset, maxUint)}
	vault.Deficit = common.Coins{common.NewCoin(common.ETHAsset, cosmos.NewUint(1))}
	c.Assert(k.SetVault(ctx, vault), IsNil)

	check := VaultDeficitCircuitBreaker(k)(ctx)
	c.Assert(check.Broken, Equals, true)
	c.Assert(check.Indeterminate, Equals, false)
	c.Assert(check.Msgs, HasLen, 1)
	c.Assert(check.Asset, Equals, common.ETHAsset.String())
	c.Assert(check.Actual.Equal(maxUint), Equals, true)
	c.Assert(check.Expected.Equal(maxUint), Equals, true)
}

func (s *CircuitBreakerSuite) TestAsgardSolvencyWithSwapQueue(c *C) {
	ctx, k := setupKeeperForTest(c)

	// empty the starting balance of asgard
	runeBal := k.GetRuneBalanceOfModule(ctx, AsgardName)
	coins := common.NewCoins(common.NewCoin(common.RuneAsset(), runeBal))
	c.Assert(k.SendFromModuleToModule(ctx, AsgardName, ReserveName, coins), IsNil)

	pool := NewPool()
	pool.Asset = common.BTCAsset
	pool.BalanceRune = cosmos.NewUint(1000)
	c.Assert(k.SetPool(ctx, pool), IsNil)

	// queue a swap with native rune — should be included in expected balance
	swapMsg := MsgSwap{
		Tx:          GetRandomTx(),
		TargetAsset: common.BTCAsset,
	}
	swapMsg.Tx.Coins = common.NewCoins(common.NewCoin(common.RuneAsset(), cosmos.NewUint(500)))
	c.Assert(k.SetSwapQueueItem(ctx, swapMsg, 0), IsNil)

	cb := AsgardSolvencyCircuitBreaker(k)

	// asgard has no rune, pool expects 1000 + swap queue expects 500 = 1500
	check := cb(ctx)
	c.Assert(check.Expected.GT(check.Actual), Equals, true)
	c.Assert(check.Asset, Equals, common.RuneAsset().Native())

	// fund asgard with pool rune + swap queue rune
	fundCoin := common.NewCoin(common.RuneAsset(), cosmos.NewUint(1500))
	c.Assert(k.MintToModule(ctx, ModuleName, fundCoin), IsNil)
	c.Assert(k.SendFromModuleToModule(ctx, ModuleName, AsgardName, common.NewCoins(fundCoin)), IsNil)

	// now it should be solvent
	check = cb(ctx)
	c.Assert(check.Actual.GTE(check.Expected), Equals, true)
}

func (s *CircuitBreakerSuite) TestBondSolvencyCircuitBreakerOverSolvencyIgnored(c *C) {
	ctx, k := setupKeeperForTest(c)

	node := GetRandomValidatorNode(NodeActive)
	node.Bond = cosmos.NewUint(1000)
	c.Assert(k.SetNodeAccount(ctx, node), IsNil)

	// Bond module holds double the expected rune. This is overfunding, not
	// insolvency, so it is not a circuit-breaker finding.
	expRune := common.NewCoin(common.RuneAsset(), cosmos.NewUint(2000))
	c.Assert(k.MintToModule(ctx, ModuleName, expRune), IsNil)
	c.Assert(k.SendFromModuleToModule(ctx, ModuleName, BondName, common.NewCoins(expRune)), IsNil)

	check := BondSolvencyCircuitBreaker(k)(ctx)
	c.Assert(check.Broken, Equals, false)
	c.Assert(check.Msgs, HasLen, 0)
	c.Assert(check.Actual.IsZero(), Equals, true)
	c.Assert(check.Expected.IsZero(), Equals, true)
}

func (s *CircuitBreakerSuite) TestAsgardSolvencyCircuitBreakerMalformedSwap(c *C) {
	ctx, k := setupKeeperForTest(c)

	// empty the starting balance of asgard so the malformed swap is the
	// only finding
	runeBal := k.GetRuneBalanceOfModule(ctx, AsgardName)
	coins := common.NewCoins(common.NewCoin(common.RuneAsset(), runeBal))
	c.Assert(k.SendFromModuleToModule(ctx, AsgardName, ReserveName, coins), IsNil)

	// a queued swap with two coins makes expected balances unknowable
	swapMsg := MsgSwap{
		Tx:          GetRandomTx(),
		TargetAsset: common.BTCAsset,
	}
	swapMsg.Tx.Coins = common.Coins{
		common.NewCoin(common.RuneAsset(), cosmos.NewUint(500)),
		common.NewCoin(common.ETHAsset, cosmos.NewUint(1)),
	}
	c.Assert(k.SetSwapQueueItem(ctx, swapMsg, 0), IsNil)

	check := AsgardSolvencyCircuitBreaker(k)(ctx)
	c.Assert(check.Broken, Equals, true)
	c.Assert(check.Indeterminate, Equals, false)
	c.Assert(check.Msgs, HasLen, 1)
	c.Assert(check.Msgs[0], Matches, "wrong number of coins for swap:.*")
}

func (s *CircuitBreakerSuite) TestAsgardSolvencyCircuitBreakerUnexpectedDenom(c *C) {
	ctx, k := setupKeeperForTest(c)

	// drain the default rune, then credit rune with no pool or queue backing
	runeBal := k.GetRuneBalanceOfModule(ctx, AsgardName)
	coins := common.NewCoins(common.NewCoin(common.RuneAsset(), runeBal))
	c.Assert(k.SendFromModuleToModule(ctx, AsgardName, ReserveName, coins), IsNil)

	coin := common.NewCoin(common.RuneAsset(), cosmos.NewUint(123))
	c.Assert(k.MintToModule(ctx, ModuleName, coin), IsNil)
	c.Assert(k.SendFromModuleToModule(ctx, ModuleName, AsgardName, common.NewCoins(coin)), IsNil)

	// A denom with zero expected backing is overfunding and is ignored by
	// solvency circuit-breaker routes.
	check := AsgardSolvencyCircuitBreaker(k)(ctx)
	c.Assert(check.Broken, Equals, false)
	c.Assert(check.Actual.IsZero(), Equals, true)
	c.Assert(check.Expected.IsZero(), Equals, true)
	c.Assert(check.Msgs, HasLen, 0)
}

func (s *CircuitBreakerSuite) TestAsgardSolvencyCircuitBreakerInconsistentLegacyStreaming(c *C) {
	ctx, k := setupKeeperForTest(c)

	// empty asgard so the inconsistent stream is the only finding
	runeBal := k.GetRuneBalanceOfModule(ctx, AsgardName)
	coins := common.NewCoins(common.NewCoin(common.RuneAsset(), runeBal))
	c.Assert(k.SendFromModuleToModule(ctx, AsgardName, ReserveName, coins), IsNil)

	tx := GetRandomTx()
	tx.Coins = common.NewCoins(common.NewCoin(common.RuneAsset(), cosmos.NewUint(500)))
	swap := MsgSwap{
		Tx:             tx,
		TargetAsset:    common.BTCAsset,
		StreamInterval: 1,
		Version:        types.SwapVersion_v1,
	}
	c.Assert(k.SetSwapQueueItem(ctx, swap, 0), IsNil)

	// stream record claims more swapped in than was ever deposited — the
	// old SafeSub clamp would silently understate expected balances
	stream := StreamingSwap{
		TxID:     tx.ID,
		Deposit:  cosmos.NewUint(500),
		Quantity: 10,
		Interval: 10,
		In:       cosmos.NewUint(600),
		Out:      cosmos.ZeroUint(),
	}
	k.SetStreamingSwap(ctx, stream)

	check := AsgardSolvencyCircuitBreaker(k)(ctx)
	c.Assert(check.Broken, Equals, true)
	c.Assert(check.Indeterminate, Equals, false)
	c.Assert(check.Msgs, HasLen, 1)
	c.Assert(check.Msgs[0], Matches, "inconsistent streaming state for swap .*")
}

func (s *CircuitBreakerSuite) TestAsgardSolvencyCircuitBreakerInconsistentSwapState(c *C) {
	ctx, k := setupKeeperForTest(c)

	// empty asgard so the inconsistent state is the only finding
	runeBal := k.GetRuneBalanceOfModule(ctx, AsgardName)
	coins := common.NewCoins(common.NewCoin(common.RuneAsset(), runeBal))
	c.Assert(k.SendFromModuleToModule(ctx, AsgardName, ReserveName, coins), IsNil)

	// advanced-queue swap whose embedded state was swapped in beyond the
	// queued coin
	tx := GetRandomTx()
	tx.Coins = common.NewCoins(common.NewCoin(common.RuneAsset(), cosmos.NewUint(500)))
	swap := MsgSwap{
		Tx:          tx,
		TargetAsset: common.BTCAsset,
		Version:     types.SwapVersion_v2,
		State: &types.SwapState{
			Deposit:   cosmos.NewUint(500),
			Withdrawn: cosmos.ZeroUint(),
			In:        cosmos.NewUint(600),
			Out:       cosmos.ZeroUint(),
		},
	}
	c.Assert(k.SetAdvSwapQueueItem(ctx, swap), IsNil)

	check := AsgardSolvencyCircuitBreaker(k)(ctx)
	c.Assert(check.Broken, Equals, true)
	c.Assert(check.Indeterminate, Equals, false)
	c.Assert(check.Msgs, HasLen, 1)
	c.Assert(check.Msgs[0], Matches, "inconsistent swap state for .*")
}

func (s *CircuitBreakerSuite) TestAsgardSolvencyCircuitBreakerHealthyV1MidStream(c *C) {
	ctx, k := setupKeeperForTest(c)

	// empty the starting balance of asgard
	runeBal := k.GetRuneBalanceOfModule(ctx, AsgardName)
	coins := common.NewCoins(common.NewCoin(common.RuneAsset(), runeBal))
	c.Assert(k.SendFromModuleToModule(ctx, AsgardName, ReserveName, coins), IsNil)

	// real V1 messages carry embedded state too (NewMsgSwap populates it),
	// but V1 progress lives only in the StreamingSwap record — a healthy
	// mid-stream swap must not be compared against the embedded deposit
	tx := GetRandomTx()
	tx.Coins = common.NewCoins(common.NewCoin(common.RuneAsset(), cosmos.NewUint(500)))
	swap := types.NewMsgSwap(tx, common.BTCAsset, GetRandomBTCAddress(), cosmos.ZeroUint(), common.NoAddress, cosmos.ZeroUint(), "", "", nil, types.SwapType_market, 10, 1, types.SwapVersion_v1, GetRandomBech32Addr())
	c.Assert(k.SetSwapQueueItem(ctx, *swap, 0), IsNil)

	stream := StreamingSwap{
		TxID:     tx.ID,
		Deposit:  cosmos.NewUint(500),
		Quantity: 10,
		Interval: 10,
		Count:    2,
		In:       cosmos.NewUint(100),
		Out:      cosmos.ZeroUint(),
	}
	k.SetStreamingSwap(ctx, stream)

	// asgard holds exactly the unswapped remainder — healthy
	fund := common.NewCoin(common.RuneAsset(), cosmos.NewUint(400))
	c.Assert(k.MintToModule(ctx, ModuleName, fund), IsNil)
	c.Assert(k.SendFromModuleToModule(ctx, ModuleName, AsgardName, common.NewCoins(fund)), IsNil)

	check := AsgardSolvencyCircuitBreaker(k)(ctx)
	c.Assert(check.Broken, Equals, false)
	c.Assert(check.Msgs, HasLen, 0)
	c.Assert(check.Expected.IsZero(), Equals, true)
}

func (s *CircuitBreakerSuite) TestAsgardSolvencyCircuitBreakerZeroInDepositMismatchV1(c *C) {
	ctx, k := setupKeeperForTest(c)

	// empty asgard so the mismatch is the only finding
	runeBal := k.GetRuneBalanceOfModule(ctx, AsgardName)
	coins := common.NewCoins(common.NewCoin(common.RuneAsset(), runeBal))
	c.Assert(k.SendFromModuleToModule(ctx, AsgardName, ReserveName, coins), IsNil)

	// nothing swapped in yet, but the queued coin no longer matches the
	// stream record's deposit — a corrupted queue coin must not silently
	// understate expected liabilities
	tx := GetRandomTx()
	tx.Coins = common.NewCoins(common.NewCoin(common.RuneAsset(), cosmos.NewUint(400)))
	swap := MsgSwap{
		Tx:             tx,
		TargetAsset:    common.BTCAsset,
		StreamInterval: 1,
		Version:        types.SwapVersion_v1,
	}
	c.Assert(k.SetSwapQueueItem(ctx, swap, 0), IsNil)

	stream := StreamingSwap{
		TxID:     tx.ID,
		Deposit:  cosmos.NewUint(500),
		Quantity: 10,
		Interval: 10,
		In:       cosmos.ZeroUint(),
		Out:      cosmos.ZeroUint(),
	}
	k.SetStreamingSwap(ctx, stream)

	check := AsgardSolvencyCircuitBreaker(k)(ctx)
	c.Assert(check.Broken, Equals, true)
	c.Assert(check.Indeterminate, Equals, false)
	c.Assert(check.Msgs, HasLen, 1)
	c.Assert(check.Msgs[0], Matches, "inconsistent streaming state for swap .*")
}

func (s *CircuitBreakerSuite) TestAsgardSolvencyCircuitBreakerZeroInDepositMismatchV2(c *C) {
	ctx, k := setupKeeperForTest(c)

	// empty asgard so the mismatch is the only finding
	runeBal := k.GetRuneBalanceOfModule(ctx, AsgardName)
	coins := common.NewCoins(common.NewCoin(common.RuneAsset(), runeBal))
	c.Assert(k.SendFromModuleToModule(ctx, AsgardName, ReserveName, coins), IsNil)

	// streaming (quantity > 1) advanced-queue swap with nothing swapped in
	// but a queued coin below the recorded deposit
	tx := GetRandomTx()
	tx.Coins = common.NewCoins(common.NewCoin(common.RuneAsset(), cosmos.NewUint(400)))
	swap := MsgSwap{
		Tx:          tx,
		TargetAsset: common.BTCAsset,
		Version:     types.SwapVersion_v2,
		State: &types.SwapState{
			Quantity:  10,
			Deposit:   cosmos.NewUint(500),
			Withdrawn: cosmos.ZeroUint(),
			In:        cosmos.ZeroUint(),
			Out:       cosmos.ZeroUint(),
		},
	}
	c.Assert(k.SetAdvSwapQueueItem(ctx, swap), IsNil)

	check := AsgardSolvencyCircuitBreaker(k)(ctx)
	c.Assert(check.Broken, Equals, true)
	c.Assert(check.Indeterminate, Equals, false)
	c.Assert(check.Msgs, HasLen, 1)
	c.Assert(check.Msgs[0], Matches, "inconsistent swap state for .*")
}

func (s *CircuitBreakerSuite) TestAsgardSolvencyCircuitBreakerOldFormatV2RuneInput(c *C) {
	ctx, k := setupKeeperForTest(c)

	// empty the starting balance of asgard
	runeBal := k.GetRuneBalanceOfModule(ctx, AsgardName)
	coins := common.NewCoins(common.NewCoin(common.RuneAsset(), runeBal))
	c.Assert(k.SendFromModuleToModule(ctx, AsgardName, ReserveName, coins), IsNil)

	// old-format V2 streaming swap: the embedded state carries quantity but
	// no deposit, progress lives in the separate StreamingSwap record
	// (mirrors the fallback in calculateNetworkSolvency)
	tx := GetRandomTx()
	tx.Coins = common.NewCoins(common.NewCoin(common.RuneAsset(), cosmos.NewUint(500)))
	swap := MsgSwap{
		Tx:          tx,
		TargetAsset: common.BTCAsset,
		Version:     types.SwapVersion_v2,
		State: &types.SwapState{
			Quantity:  10,
			Deposit:   cosmos.ZeroUint(),
			Withdrawn: cosmos.ZeroUint(),
			In:        cosmos.ZeroUint(),
			Out:       cosmos.ZeroUint(),
		},
	}
	c.Assert(k.SetAdvSwapQueueItem(ctx, swap), IsNil)

	stream := StreamingSwap{
		TxID:     tx.ID,
		Deposit:  cosmos.NewUint(500),
		Quantity: 10,
		Interval: 10,
		Count:    2,
		In:       cosmos.NewUint(100),
		Out:      cosmos.ZeroUint(),
	}
	k.SetStreamingSwap(ctx, stream)

	// asgard holds exactly the unswapped remainder — healthy mid-stream
	// state must not read as an inconsistent stream
	fund := common.NewCoin(common.RuneAsset(), cosmos.NewUint(400))
	c.Assert(k.MintToModule(ctx, ModuleName, fund), IsNil)
	c.Assert(k.SendFromModuleToModule(ctx, ModuleName, AsgardName, common.NewCoins(fund)), IsNil)

	check := AsgardSolvencyCircuitBreaker(k)(ctx)
	c.Assert(check.Broken, Equals, false)
	c.Assert(check.Msgs, HasLen, 0)
	c.Assert(check.Expected.IsZero(), Equals, true)
}

func (s *CircuitBreakerSuite) TestAsgardSolvencyCircuitBreakerOldFormatV2ExternalInput(c *C) {
	ctx, k := setupKeeperForTest(c)

	// empty the starting balance of asgard
	runeBal := k.GetRuneBalanceOfModule(ctx, AsgardName)
	coins := common.NewCoins(common.NewCoin(common.RuneAsset(), runeBal))
	c.Assert(k.SendFromModuleToModule(ctx, AsgardName, ReserveName, coins), IsNil)

	// old-format V2 BTC->RUNE streaming swap: the produced RUNE exists only
	// in the separate record's Out — dropping it would understate expected
	// balances and mask insolvency
	tx := GetRandomTx()
	tx.Coins = common.NewCoins(common.NewCoin(common.BTCAsset, cosmos.NewUint(500)))
	swap := MsgSwap{
		Tx:          tx,
		TargetAsset: common.RuneAsset(),
		Version:     types.SwapVersion_v2,
		State: &types.SwapState{
			Quantity:  10,
			Deposit:   cosmos.ZeroUint(),
			Withdrawn: cosmos.ZeroUint(),
			In:        cosmos.ZeroUint(),
			Out:       cosmos.ZeroUint(),
		},
	}
	c.Assert(k.SetAdvSwapQueueItem(ctx, swap), IsNil)

	stream := StreamingSwap{
		TxID:     tx.ID,
		Deposit:  cosmos.NewUint(500),
		Quantity: 10,
		Interval: 10,
		Count:    2,
		In:       cosmos.NewUint(100),
		Out:      cosmos.NewUint(250),
	}
	k.SetStreamingSwap(ctx, stream)

	// asgard is missing the produced RUNE — insolvent by 250, not broken
	check := AsgardSolvencyCircuitBreaker(k)(ctx)
	c.Assert(check.Broken, Equals, false)
	c.Assert(check.Asset, Equals, common.RuneAsset().Native())
	c.Assert(check.Expected.Equal(cosmos.NewUint(250)), Equals, true)
	c.Assert(check.Actual.IsZero(), Equals, true)

	// funding the produced RUNE restores solvency
	fund := common.NewCoin(common.RuneAsset(), cosmos.NewUint(250))
	c.Assert(k.MintToModule(ctx, ModuleName, fund), IsNil)
	c.Assert(k.SendFromModuleToModule(ctx, ModuleName, AsgardName, common.NewCoins(fund)), IsNil)

	check = AsgardSolvencyCircuitBreaker(k)(ctx)
	c.Assert(check.Broken, Equals, false)
	c.Assert(check.Msgs, HasLen, 0)
	c.Assert(check.Expected.IsZero(), Equals, true)
}

func (s *CircuitBreakerSuite) TestAsgardSolvencyCircuitBreakerOldFormatV2ContradictoryEmbedded(c *C) {
	ctx, k := setupKeeperForTest(c)

	// empty the starting balance of asgard
	runeBal := k.GetRuneBalanceOfModule(ctx, AsgardName)
	coins := common.NewCoins(common.NewCoin(common.RuneAsset(), runeBal))
	c.Assert(k.SendFromModuleToModule(ctx, AsgardName, ReserveName, coins), IsNil)

	// a deposit-less embedded state with nonzero progress is contradictory:
	// it must not take the old-format fallback and read as a not-yet-started
	// stream, silently dropping the embedded Out from expected balances
	tx := GetRandomTx()
	tx.Coins = common.NewCoins(common.NewCoin(common.RuneAsset(), cosmos.NewUint(500)))
	swap := MsgSwap{
		Tx:          tx,
		TargetAsset: common.BTCAsset,
		Version:     types.SwapVersion_v2,
		State: &types.SwapState{
			Quantity:  10,
			Deposit:   cosmos.ZeroUint(),
			Withdrawn: cosmos.ZeroUint(),
			In:        cosmos.ZeroUint(),
			Out:       cosmos.NewUint(250),
		},
	}
	c.Assert(k.SetAdvSwapQueueItem(ctx, swap), IsNil)

	check := AsgardSolvencyCircuitBreaker(k)(ctx)
	c.Assert(check.Broken, Equals, true)
	c.Assert(check.Indeterminate, Equals, false)
	c.Assert(check.Msgs, HasLen, 1)
	c.Assert(check.Msgs[0], Matches, "inconsistent swap state for .*")
}

func (s *CircuitBreakerSuite) TestAsgardSolvencyCircuitBreakerOldFormatV2RecordMismatch(c *C) {
	ctx, k := setupKeeperForTest(c)

	// empty the starting balance of asgard
	runeBal := k.GetRuneBalanceOfModule(ctx, AsgardName)
	coins := common.NewCoins(common.NewCoin(common.RuneAsset(), runeBal))
	c.Assert(k.SendFromModuleToModule(ctx, AsgardName, ReserveName, coins), IsNil)

	// old-format V2 swap whose fallback record disagrees with the queued
	// coin — the consistency check must fire on the fallback path too
	tx := GetRandomTx()
	tx.Coins = common.NewCoins(common.NewCoin(common.RuneAsset(), cosmos.NewUint(500)))
	swap := MsgSwap{
		Tx:          tx,
		TargetAsset: common.BTCAsset,
		Version:     types.SwapVersion_v2,
		State: &types.SwapState{
			Quantity:  10,
			Deposit:   cosmos.ZeroUint(),
			Withdrawn: cosmos.ZeroUint(),
			In:        cosmos.ZeroUint(),
			Out:       cosmos.ZeroUint(),
		},
	}
	c.Assert(k.SetAdvSwapQueueItem(ctx, swap), IsNil)

	stream := StreamingSwap{
		TxID:     tx.ID,
		Deposit:  cosmos.NewUint(400),
		Quantity: 10,
		Interval: 10,
		In:       cosmos.ZeroUint(),
		Out:      cosmos.ZeroUint(),
	}
	k.SetStreamingSwap(ctx, stream)

	check := AsgardSolvencyCircuitBreaker(k)(ctx)
	c.Assert(check.Broken, Equals, true)
	c.Assert(check.Indeterminate, Equals, false)
	c.Assert(check.Msgs, HasLen, 1)
	c.Assert(check.Msgs[0], Matches, "inconsistent streaming state for swap .*")
}

func (s *CircuitBreakerSuite) TestAsgardSolvencyCircuitBreakerNonStreamingV2SharedTxID(c *C) {
	ctx, k := setupKeeperForTest(c)

	// empty the starting balance of asgard
	runeBal := k.GetRuneBalanceOfModule(ctx, AsgardName)
	coins := common.NewCoins(common.NewCoin(common.RuneAsset(), runeBal))
	c.Assert(k.SendFromModuleToModule(ctx, AsgardName, ReserveName, coins), IsNil)

	// V1 main streaming swap mid-stream with its record
	tx := GetRandomTx()
	tx.Coins = common.NewCoins(common.NewCoin(common.RuneAsset(), cosmos.NewUint(500)))
	main := MsgSwap{
		Tx:             tx,
		TargetAsset:    common.BTCAsset,
		StreamInterval: 1,
		Version:        types.SwapVersion_v1,
	}
	c.Assert(k.SetSwapQueueItem(ctx, main, 0), IsNil)

	stream := StreamingSwap{
		TxID:     tx.ID,
		Deposit:  cosmos.NewUint(500),
		Quantity: 10,
		Interval: 10,
		Count:    2,
		In:       cosmos.NewUint(100),
		Out:      cosmos.ZeroUint(),
	}
	k.SetStreamingSwap(ctx, stream)

	// a non-streaming V2 swap sharing the TxID with a zero deposit must not
	// borrow the main swap's streaming record via the old-format fallback
	affTx := tx
	affTx.Coins = common.NewCoins(common.NewCoin(common.RuneAsset(), cosmos.NewUint(50)))
	aff := MsgSwap{
		Tx:          affTx,
		TargetAsset: common.RuneAsset(),
		Version:     types.SwapVersion_v2,
		State: &types.SwapState{
			Quantity:  1,
			Deposit:   cosmos.ZeroUint(),
			Withdrawn: cosmos.ZeroUint(),
			In:        cosmos.ZeroUint(),
			Out:       cosmos.ZeroUint(),
		},
	}
	c.Assert(k.SetAdvSwapQueueItem(ctx, aff), IsNil)

	// main remainder (400) + full non-streaming swap coin (50)
	fund := common.NewCoin(common.RuneAsset(), cosmos.NewUint(450))
	c.Assert(k.MintToModule(ctx, ModuleName, fund), IsNil)
	c.Assert(k.SendFromModuleToModule(ctx, ModuleName, AsgardName, common.NewCoins(fund)), IsNil)

	check := AsgardSolvencyCircuitBreaker(k)(ctx)
	c.Assert(check.Broken, Equals, false)
	c.Assert(check.Msgs, HasLen, 0)
	c.Assert(check.Expected.IsZero(), Equals, true)
}

func (s *CircuitBreakerSuite) TestAsgardSolvencyCircuitBreakerExternalInputInconsistentV1(c *C) {
	ctx, k := setupKeeperForTest(c)

	// empty the starting balance of asgard
	runeBal := k.GetRuneBalanceOfModule(ctx, AsgardName)
	coins := common.NewCoins(common.NewCoin(common.RuneAsset(), runeBal))
	c.Assert(k.SendFromModuleToModule(ctx, AsgardName, ReserveName, coins), IsNil)

	// BTC->RUNE V1 streaming swap whose record claims more swapped in than
	// deposited — its Out must not be trusted to credit expected balances
	tx := GetRandomTx()
	tx.Coins = common.NewCoins(common.NewCoin(common.BTCAsset, cosmos.NewUint(500)))
	swap := MsgSwap{
		Tx:             tx,
		TargetAsset:    common.RuneAsset(),
		StreamInterval: 1,
		Version:        types.SwapVersion_v1,
	}
	c.Assert(k.SetSwapQueueItem(ctx, swap, 0), IsNil)

	stream := StreamingSwap{
		TxID:     tx.ID,
		Deposit:  cosmos.NewUint(500),
		Quantity: 10,
		Interval: 10,
		In:       cosmos.NewUint(600),
		Out:      cosmos.NewUint(250),
	}
	k.SetStreamingSwap(ctx, stream)

	check := AsgardSolvencyCircuitBreaker(k)(ctx)
	c.Assert(check.Broken, Equals, true)
	c.Assert(check.Indeterminate, Equals, false)
	c.Assert(check.Msgs, HasLen, 1)
	c.Assert(check.Msgs[0], Matches, "inconsistent streaming state for swap .*")
}

func (s *CircuitBreakerSuite) TestAsgardSolvencyCircuitBreakerExternalInputDepositMismatchV2(c *C) {
	ctx, k := setupKeeperForTest(c)

	// empty the starting balance of asgard
	runeBal := k.GetRuneBalanceOfModule(ctx, AsgardName)
	coins := common.NewCoins(common.NewCoin(common.RuneAsset(), runeBal))
	c.Assert(k.SendFromModuleToModule(ctx, AsgardName, ReserveName, coins), IsNil)

	// BTC->RUNE V2 streaming swap whose queued coin no longer matches the
	// embedded deposit — previously unvalidated because the input is not
	// native, while Out was still trusted below
	tx := GetRandomTx()
	tx.Coins = common.NewCoins(common.NewCoin(common.BTCAsset, cosmos.NewUint(500)))
	swap := MsgSwap{
		Tx:          tx,
		TargetAsset: common.RuneAsset(),
		Version:     types.SwapVersion_v2,
		State: &types.SwapState{
			Quantity:  10,
			Deposit:   cosmos.NewUint(400),
			Withdrawn: cosmos.ZeroUint(),
			In:        cosmos.ZeroUint(),
			Out:       cosmos.NewUint(250),
		},
	}
	c.Assert(k.SetAdvSwapQueueItem(ctx, swap), IsNil)

	check := AsgardSolvencyCircuitBreaker(k)(ctx)
	c.Assert(check.Broken, Equals, true)
	c.Assert(check.Indeterminate, Equals, false)
	c.Assert(check.Msgs, HasLen, 1)
	c.Assert(check.Msgs[0], Matches, "inconsistent swap state for .*")
}

func (s *CircuitBreakerSuite) TestAsgardSolvencyCircuitBreakerHealthyExternalInputMidStream(c *C) {
	ctx, k := setupKeeperForTest(c)

	// empty the starting balance of asgard
	runeBal := k.GetRuneBalanceOfModule(ctx, AsgardName)
	coins := common.NewCoins(common.NewCoin(common.RuneAsset(), runeBal))
	c.Assert(k.SendFromModuleToModule(ctx, AsgardName, ReserveName, coins), IsNil)

	// healthy BTC->RUNE V2 mid-stream swap: the hoisted validation must not
	// flag consistent external-input state, and the produced RUNE is owed
	tx := GetRandomTx()
	tx.Coins = common.NewCoins(common.NewCoin(common.BTCAsset, cosmos.NewUint(500)))
	swap := MsgSwap{
		Tx:          tx,
		TargetAsset: common.RuneAsset(),
		Version:     types.SwapVersion_v2,
		State: &types.SwapState{
			Quantity:  10,
			Deposit:   cosmos.NewUint(500),
			Withdrawn: cosmos.ZeroUint(),
			In:        cosmos.NewUint(100),
			Out:       cosmos.NewUint(250),
		},
	}
	c.Assert(k.SetAdvSwapQueueItem(ctx, swap), IsNil)

	// asgard holds exactly the produced RUNE — healthy
	fund := common.NewCoin(common.RuneAsset(), cosmos.NewUint(250))
	c.Assert(k.MintToModule(ctx, ModuleName, fund), IsNil)
	c.Assert(k.SendFromModuleToModule(ctx, ModuleName, AsgardName, common.NewCoins(fund)), IsNil)

	check := AsgardSolvencyCircuitBreaker(k)(ctx)
	c.Assert(check.Broken, Equals, false)
	c.Assert(check.Msgs, HasLen, 0)
	c.Assert(check.Expected.IsZero(), Equals, true)
}

func (s *CircuitBreakerSuite) TestAsgardSolvencyCircuitBreakerSecuredInputStreaming(c *C) {
	ctx, k := setupKeeperForTest(c)

	// empty the starting balance of asgard
	runeBal := k.GetRuneBalanceOfModule(ctx, AsgardName)
	coins := common.NewCoins(common.NewCoin(common.RuneAsset(), runeBal))
	c.Assert(k.SendFromModuleToModule(ctx, AsgardName, ReserveName, coins), IsNil)

	// secured-asset-input V1 streaming swap (the stable-reserve rescue swap
	// shape) — newly covered by the hoisted validation: healthy state must
	// pass, and an impossible record must fail closed
	tx := GetRandomTx()
	tx.Coins = common.NewCoins(common.NewCoin(common.BTCAsset.GetSecuredAsset(), cosmos.NewUint(500)))
	swap := MsgSwap{
		Tx:             tx,
		TargetAsset:    common.RuneAsset(),
		StreamInterval: 1,
		Version:        types.SwapVersion_v1,
	}
	c.Assert(k.SetSwapQueueItem(ctx, swap, 0), IsNil)

	stream := StreamingSwap{
		TxID:     tx.ID,
		Deposit:  cosmos.NewUint(500),
		Quantity: 10,
		Interval: 10,
		Count:    2,
		In:       cosmos.NewUint(100),
		Out:      cosmos.NewUint(250),
	}
	k.SetStreamingSwap(ctx, stream)

	// asgard holds exactly the produced RUNE — healthy
	fund := common.NewCoin(common.RuneAsset(), cosmos.NewUint(250))
	c.Assert(k.MintToModule(ctx, ModuleName, fund), IsNil)
	c.Assert(k.SendFromModuleToModule(ctx, ModuleName, AsgardName, common.NewCoins(fund)), IsNil)

	check := AsgardSolvencyCircuitBreaker(k)(ctx)
	c.Assert(check.Broken, Equals, false)
	c.Assert(check.Msgs, HasLen, 0)
	c.Assert(check.Expected.IsZero(), Equals, true)

	// an In beyond the deposit is impossible state and must fail closed
	stream.In = cosmos.NewUint(600)
	k.SetStreamingSwap(ctx, stream)

	check = AsgardSolvencyCircuitBreaker(k)(ctx)
	c.Assert(check.Broken, Equals, true)
	c.Assert(check.Indeterminate, Equals, false)
	c.Assert(check.Msgs, HasLen, 1)
	c.Assert(check.Msgs[0], Matches, "inconsistent streaming state for swap .*")
}

func (s *CircuitBreakerSuite) TestAsgardSolvencyCircuitBreakerCorruptSwapRecord(c *C) {
	ctx, k := setupKeeperForTest(c)

	// empty asgard so the corrupt record is the only finding
	runeBal := k.GetRuneBalanceOfModule(ctx, AsgardName)
	coins := common.NewCoins(common.NewCoin(common.RuneAsset(), runeBal))
	c.Assert(k.SendFromModuleToModule(ctx, AsgardName, ReserveName, coins), IsNil)

	c.Assert(k.storeService.OpenKVStore(ctx).Set(k.GetKey(prefixSwapQueueItem, "corrupt"), []byte("garbage")), IsNil)

	// unreadable records are indeterminate, not proven violations
	check := AsgardSolvencyCircuitBreaker(k)(ctx)
	c.Assert(check.Broken, Equals, false)
	c.Assert(check.Indeterminate, Equals, true)
	c.Assert(check.Msgs, HasLen, 1)
	c.Assert(check.Msgs[0], Matches, "fail to unmarshal swap record:.*")
}

func (s *CircuitBreakerSuite) TestBondSolvencyCircuitBreakerCorruptNodeAccount(c *C) {
	ctx, k := setupKeeperForTest(c)

	c.Assert(k.storeService.OpenKVStore(ctx).Set(k.GetKey(prefixNodeAccount, "corrupt"), []byte("garbage")), IsNil)

	// unreadable records are indeterminate, not proven violations
	check := BondSolvencyCircuitBreaker(k)(ctx)
	c.Assert(check.Broken, Equals, false)
	c.Assert(check.Indeterminate, Equals, true)
	c.Assert(check.Msgs, HasLen, 1)
	c.Assert(check.Msgs[0], Matches, "fail to unmarshal node account:.*")
}

func (s *CircuitBreakerSuite) TestAsgardSolvencyCircuitBreakerCorruptPool(c *C) {
	ctx, k := setupKeeperForTest(c)

	c.Assert(k.storeService.OpenKVStore(ctx).Set(k.GetKey(prefixPool, "corrupt"), []byte("garbage")), IsNil)

	// an unreadable pool aborts the whole route as indeterminate — no
	// invariant is proven violated
	check := AsgardSolvencyCircuitBreaker(k)(ctx)
	c.Assert(check.Broken, Equals, false)
	c.Assert(check.Indeterminate, Equals, true)
	c.Assert(check.Msgs, HasLen, 1)
	c.Assert(check.Msgs[0], Matches, "fail to get pools:.*")
}

func (s *CircuitBreakerSuite) TestAsgardSolvencyCircuitBreakerCorruptStreamingRecord(c *C) {
	ctx, k := setupKeeperForTest(c)

	// empty the starting balance of asgard
	runeBal := k.GetRuneBalanceOfModule(ctx, AsgardName)
	coins := common.NewCoins(common.NewCoin(common.RuneAsset(), runeBal))
	c.Assert(k.SendFromModuleToModule(ctx, AsgardName, ReserveName, coins), IsNil)

	// V1 streaming swap whose separate record bytes are unreadable
	tx := GetRandomTx()
	tx.Coins = common.NewCoins(common.NewCoin(common.RuneAsset(), cosmos.NewUint(500)))
	swap := MsgSwap{
		Tx:             tx,
		TargetAsset:    common.BTCAsset,
		StreamInterval: 1,
		Version:        types.SwapVersion_v1,
	}
	c.Assert(k.SetSwapQueueItem(ctx, swap, 0), IsNil)
	c.Assert(k.storeService.OpenKVStore(ctx).Set(k.GetKey(prefixStreamingSwap, tx.ID.String()), []byte("garbage")), IsNil)

	check := AsgardSolvencyCircuitBreaker(k)(ctx)
	c.Assert(check.Broken, Equals, false)
	c.Assert(check.Indeterminate, Equals, true)
	c.Assert(check.Msgs, HasLen, 1)
	c.Assert(check.Msgs[0], Matches, "fail to get streaming swap .*")
}

func (s *CircuitBreakerSuite) TestBondSolvencyCircuitBreakerCorruptNetwork(c *C) {
	ctx, k := setupKeeperForTest(c)

	c.Assert(k.storeService.OpenKVStore(ctx).Set(k.GetKey(prefixNetwork, ""), []byte("garbage")), IsNil)

	// an unreadable network singleton aborts the route as indeterminate
	check := BondSolvencyCircuitBreaker(k)(ctx)
	c.Assert(check.Broken, Equals, false)
	c.Assert(check.Indeterminate, Equals, true)
	c.Assert(check.Msgs, HasLen, 1)
	c.Assert(check.Msgs[0], Matches, "fail to get network:.*")
}

func (s *CircuitBreakerSuite) TestAsgardSolvencyCircuitBreakerOverflowingState(c *C) {
	ctx, k := setupKeeperForTest(c)

	// empty the starting balance of asgard
	runeBal := k.GetRuneBalanceOfModule(ctx, AsgardName)
	coins := common.NewCoins(common.NewCoin(common.RuneAsset(), runeBal))
	c.Assert(k.SendFromModuleToModule(ctx, AsgardName, ReserveName, coins), IsNil)

	// pools and a queued swap whose rune totals overflow 256 bits in
	// aggregate — reporting must saturate, not panic EndBlock
	maxUint := cosmos.NewUintFromString("115792089237316195423570985008687907853269984665640564039457584007913129639935")
	pool := NewPool()
	pool.Asset = common.BTCAsset
	pool.BalanceRune = maxUint
	pool.PendingInboundRune = maxUint
	c.Assert(k.SetPool(ctx, pool), IsNil)
	pool2 := NewPool()
	pool2.Asset = common.ETHAsset
	pool2.BalanceRune = maxUint
	c.Assert(k.SetPool(ctx, pool2), IsNil)

	tx := GetRandomTx()
	tx.Coins = common.NewCoins(common.NewCoin(common.RuneAsset(), maxUint))
	swap := MsgSwap{
		Tx:          tx,
		TargetAsset: common.BTCAsset,
	}
	c.Assert(k.SetSwapQueueItem(ctx, swap, 0), IsNil)

	check := AsgardSolvencyCircuitBreaker(k)(ctx)
	c.Assert(check.Broken, Equals, false)
	c.Assert(check.Indeterminate, Equals, false)
	c.Assert(check.Asset, Equals, common.RuneAsset().Native())
	c.Assert(check.Expected.Equal(maxUint), Equals, true)
	c.Assert(check.Actual.IsZero(), Equals, true)
}

func (s *CircuitBreakerSuite) TestBondSolvencyCircuitBreakerOverflowingBonds(c *C) {
	ctx, k := setupKeeperForTest(c)

	// two bonds plus rewards whose sum exceeds 256 bits — reporting must
	// saturate, not panic EndBlock
	maxUint := cosmos.NewUintFromString("115792089237316195423570985008687907853269984665640564039457584007913129639935")
	node := GetRandomValidatorNode(NodeActive)
	node.Bond = maxUint
	c.Assert(k.SetNodeAccount(ctx, node), IsNil)
	node2 := GetRandomValidatorNode(NodeActive)
	node2.Bond = maxUint
	c.Assert(k.SetNodeAccount(ctx, node2), IsNil)

	network := NewNetwork()
	network.BondRewardRune = maxUint
	c.Assert(k.SetNetwork(ctx, network), IsNil)

	check := BondSolvencyCircuitBreaker(k)(ctx)
	c.Assert(check.Broken, Equals, false)
	c.Assert(check.Indeterminate, Equals, false)
	c.Assert(check.Asset, Equals, common.RuneAsset().Native())
	c.Assert(check.Expected.Equal(maxUint), Equals, true)
}

func (s *CircuitBreakerSuite) TestMalformedSwapPurgeSignal(c *C) {
	ctx, k := setupKeeperForTest(c)

	// nothing recorded: consume returns zero
	count, lastHeight, err := k.ConsumeMalformedSwapPurge(ctx)
	c.Assert(err, IsNil)
	c.Assert(count, Equals, int64(0))
	c.Assert(lastHeight, Equals, int64(0))

	// purges accumulate until consumed, then reset; the height tracks the
	// most recent deletion
	k.AddMalformedSwapPurge(ctx.WithBlockHeight(40), 2)
	k.AddMalformedSwapPurge(ctx.WithBlockHeight(50), 3)
	k.AddMalformedSwapPurge(ctx.WithBlockHeight(60), 0)
	count, lastHeight, err = k.ConsumeMalformedSwapPurge(ctx)
	c.Assert(err, IsNil)
	c.Assert(count, Equals, int64(5))
	c.Assert(lastHeight, Equals, int64(50))
	count, _, err = k.ConsumeMalformedSwapPurge(ctx)
	c.Assert(err, IsNil)
	c.Assert(count, Equals, int64(0))
}

func (s *CircuitBreakerSuite) TestMalformedSwapPurgeSignalCorruptCounter(c *C) {
	ctx, k := setupKeeperForTest(c)

	// an unreadable counter must be reported, not silently cleared
	c.Assert(k.storeService.OpenKVStore(ctx).Set(k.GetKey(prefixMalformedSwapPurge, "count"), []byte("garbage")), IsNil)

	_, _, err := k.ConsumeMalformedSwapPurge(ctx)
	c.Assert(err, NotNil)

	// the record is preserved: a second consume still errors
	_, _, err = k.ConsumeMalformedSwapPurge(ctx)
	c.Assert(err, NotNil)

	// and the route reports it as indeterminate
	runeBal := k.GetRuneBalanceOfModule(ctx, AsgardName)
	coins := common.NewCoins(common.NewCoin(common.RuneAsset(), runeBal))
	c.Assert(k.SendFromModuleToModule(ctx, AsgardName, ReserveName, coins), IsNil)

	check := AsgardSolvencyCircuitBreaker(k)(ctx)
	c.Assert(check.Broken, Equals, false)
	c.Assert(check.Indeterminate, Equals, true)
	c.Assert(check.Msgs, HasLen, 1)
	c.Assert(check.Msgs[0], Matches, "fail to read malformed swap purge counter:.*")
}

func (s *CircuitBreakerSuite) TestMalformedSwapPurgeSignalCorruptHeight(c *C) {
	ctx, k := setupKeeperForTest(c)

	k.AddMalformedSwapPurge(ctx.WithBlockHeight(70), 4)
	c.Assert(k.storeService.OpenKVStore(ctx).Set(k.GetKey(prefixMalformedSwapPurge, "height"), []byte("garbage")), IsNil)

	// the height read fails after the count read succeeded — nothing is
	// cleared
	_, _, err := k.ConsumeMalformedSwapPurge(ctx)
	c.Assert(err, NotNil)

	// repairing the height key shows the count survived the failed consume
	k.setInt64(ctx, k.GetKey(prefixMalformedSwapPurge, "height"), 70)
	count, lastHeight, err := k.ConsumeMalformedSwapPurge(ctx)
	c.Assert(err, IsNil)
	c.Assert(count, Equals, int64(4))
	c.Assert(lastHeight, Equals, int64(70))
}

func (s *CircuitBreakerSuite) TestMalformedSwapPurgeSignalSelfHeals(c *C) {
	ctx, k := setupKeeperForTest(c)

	c.Assert(k.storeService.OpenKVStore(ctx).Set(k.GetKey(prefixMalformedSwapPurge, "count"), []byte("garbage")), IsNil)
	_, _, err := k.ConsumeMalformedSwapPurge(ctx)
	c.Assert(err, NotNil)

	// a later purge overwrites the corrupt counter and the signal clears
	k.AddMalformedSwapPurge(ctx.WithBlockHeight(80), 2)
	count, lastHeight, err := k.ConsumeMalformedSwapPurge(ctx)
	c.Assert(err, IsNil)
	c.Assert(count, Equals, int64(2))
	c.Assert(lastHeight, Equals, int64(80))
}

func (s *CircuitBreakerSuite) TestAsgardSolvencyCircuitBreakerConsumesPurgeSignal(c *C) {
	ctx, k := setupKeeperForTest(c)

	// empty asgard so the purge signal is the only finding
	runeBal := k.GetRuneBalanceOfModule(ctx, AsgardName)
	coins := common.NewCoins(common.NewCoin(common.RuneAsset(), runeBal))
	c.Assert(k.SendFromModuleToModule(ctx, AsgardName, ReserveName, coins), IsNil)

	k.AddMalformedSwapPurge(ctx.WithBlockHeight(42), 3)

	check := AsgardSolvencyCircuitBreaker(k)(ctx)
	c.Assert(check.Broken, Equals, false)
	c.Assert(check.Indeterminate, Equals, true)
	c.Assert(check.Msgs, HasLen, 1)
	c.Assert(check.Msgs[0], Equals, "3 malformed swap queue records deleted, most recently at height 42")

	// consumed: the next check is clean
	check = AsgardSolvencyCircuitBreaker(k)(ctx)
	c.Assert(check.Indeterminate, Equals, false)
	c.Assert(check.Msgs, HasLen, 0)
}

func (s *CircuitBreakerSuite) TestCircuitBreakerRoutes(c *C) {
	_, k := setupKeeperForTest(c)

	routes := k.CircuitBreakerRoutes()
	c.Assert(len(routes), Equals, 3)
	c.Assert(routes[0].Route, Equals, "AsgardSolvency")
	c.Assert(routes[1].Route, Equals, "BondSolvency")
	c.Assert(routes[2].Route, Equals, "VaultDeficit")
}
