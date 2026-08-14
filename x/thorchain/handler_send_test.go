package thorchain

import (
	"errors"
	"strings"

	banktypes "github.com/cosmos/cosmos-sdk/x/bank/types"
	"gitlab.com/thorchain/thornode/v3/common"
	cosmos "gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"

	. "gopkg.in/check.v1"
)

type HandlerSendSuiteV87 struct{}

var _ = Suite(&HandlerSendSuiteV87{})

type altNativeFeeCollectionFailKeeper struct {
	keeper.Keeper
}

// SendFromAccountToModule forces the native fee collector's bank transfer to
// fail so ante error propagation can be verified.
func (k *altNativeFeeCollectionFailKeeper) SendFromAccountToModule(_ cosmos.Context, _ cosmos.AccAddress, _ string, _ common.Coins) error {
	return errKaboom
}

func (s *HandlerSendSuiteV87) TestValidate(c *C) {
	ctx, k := setupKeeperForTest(c)

	addr1 := GetRandomBech32Addr()
	addr2 := GetRandomBech32Addr()
	moduleNameAccAddr := k.GetModuleAccAddress(ModuleName)

	msg := &MsgSend{
		FromAddress: addr1,
		ToAddress:   addr2,
		Amount:      cosmos.NewCoins(cosmos.NewCoin("dummy", cosmos.NewInt(12))),
	}
	handler := NewSendHandler(NewDummyMgrWithKeeper(k))
	err := handler.validate(ctx, msg)
	c.Assert(err, IsNil)

	msg.ToAddress = moduleNameAccAddr
	// "dummy" is not the denom of the asset it parses to (THOR.DUMMY, whose
	// denom is "thor.dummy"), so it cannot be converted to a deposit.
	err = handler.validate(ctx, msg)
	c.Assert(err, NotNil)

	msg.Amount = cosmos.NewCoins(cosmos.NewCoin("rune", cosmos.NewInt(12)))
	err = handler.validate(ctx, msg)
	c.Assert(err, IsNil, Commentf("sending to module address: %s indicates a memo will be attached for MsgDeposit", ModuleName))

	for _, moduleName := range []string{AsgardName, BondName, ReserveName} {
		msg.ToAddress = k.GetModuleAccAddress(moduleName)
		validateErr := handler.validate(ctx, msg)
		c.Assert(validateErr, NotNil, Commentf("cannot send to module: %s", moduleName))
	}

	// invalid msg
	msg = &MsgSend{}
	err = handler.validate(ctx, msg)
	c.Assert(err, NotNil)

	// Note that cosmos.NewCoins and cosmos.ParsedCoins both sanitise (drop) zero-amount coins.
	zeroCoin := cosmos.NewCoin("dummy", cosmos.ZeroInt())
	c.Assert(zeroCoin.String(), Equals, "0dummy")
	newCoins := cosmos.NewCoins(zeroCoin)
	c.Assert(newCoins.String(), Equals, "")
	parsedCoins, err := cosmos.ParseCoins("0dummy")
	c.Assert(err, IsNil)
	c.Assert(parsedCoins.String(), Equals, "")
	// (This is a valid ParseCoins format, as shown with a non-zero amount.)
	parsedCoins, err = cosmos.ParseCoins("1dummy")
	c.Assert(err, IsNil)
	c.Assert(parsedCoins.String(), Equals, "1dummy")

	zeroCoins := cosmos.Coins{zeroCoin}
	c.Assert(zeroCoins.String(), Equals, "0dummy")

	// Test zero amount rejection when not for MsgDeposit conversion.
	msg = &MsgSend{
		FromAddress: addr1,
		ToAddress:   addr2,
		Amount:      zeroCoins,
	}
	err = handler.validate(ctx, msg)
	c.Assert(err, NotNil)
	// Test zero amount validity when for MsgDeposit conversion (such as for MsgUnbond).
	msg = &MsgSend{
		FromAddress: addr1,
		ToAddress:   moduleNameAccAddr,
		Amount:      zeroCoins,
	}
	err = handler.validate(ctx, msg)
	c.Assert(err, IsNil)
}

func (s *HandlerSendSuiteV87) TestValidateMultiple(c *C) {
	// Verify that multiple coins can be sent, except to module address
	ctx, k := setupKeeperForTest(c)

	// cosmos bank sends are gated in the handler and default to off, and this
	// case is about the coins rather than the gate
	k.SetMimir(ctx, "BankSendEnabled", 1)

	addr1 := GetRandomBech32Addr()
	addr2 := GetRandomBech32Addr()
	handler := NewSendHandler(NewDummyMgrWithKeeper(k))
	moduleAddress := k.GetModuleAccAddress(ModuleName)

	msg := &MsgSend{
		FromAddress: addr1,
		ToAddress:   addr2,
		Amount: cosmos.NewCoins(
			cosmos.NewCoin("foo", cosmos.NewInt(10)),
			cosmos.NewCoin("bar", cosmos.NewInt(20)),
		),
	}
	err := handler.validate(ctx, msg)
	c.Assert(err, IsNil)

	bank := &banktypes.MsgSend{
		FromAddress: addr1.String(),
		ToAddress:   addr2.String(),
		Amount: cosmos.NewCoins(
			cosmos.NewCoin("foo", cosmos.NewInt(10)),
			cosmos.NewCoin("bar", cosmos.NewInt(20)),
		),
	}
	err = handler.validate(ctx, bank)
	c.Assert(err, IsNil)

	msg = &MsgSend{
		FromAddress: addr1,
		ToAddress:   moduleAddress,
		Amount: cosmos.NewCoins(
			cosmos.NewCoin("foo", cosmos.NewInt(10)),
			cosmos.NewCoin("bar", cosmos.NewInt(20)),
		),
	}
	err = handler.validate(ctx, msg)
	c.Assert(err, NotNil)

	bank = &banktypes.MsgSend{
		FromAddress: addr1.String(),
		ToAddress:   moduleAddress.String(),
		Amount: cosmos.NewCoins(
			cosmos.NewCoin("foo", cosmos.NewInt(10)),
			cosmos.NewCoin("bar", cosmos.NewInt(20)),
		),
	}
	err = handler.validate(ctx, bank)
	c.Assert(err, NotNil)
}

func (s *HandlerSendSuiteV87) TestHandle(c *C) {
	ctx, k := setupKeeperForTest(c)

	addr1 := GetRandomBech32Addr()
	addr2 := GetRandomBech32Addr()

	FundAccount(c, ctx, k, addr1, 200*common.One)

	coin, err := common.NewCoin(common.RuneNative, cosmos.NewUint(12*common.One)).Native()
	c.Assert(err, IsNil)
	msg := &MsgSend{
		FromAddress: addr1,
		ToAddress:   addr2,
		Amount:      cosmos.NewCoins(coin),
	}

	handler := NewSendHandler(NewDummyMgrWithKeeper(k))
	_, err = handler.handle(ctx, msg)
	c.Assert(err, IsNil)

	// invalid msg should result in a error
	result, err := handler.Run(ctx, NewMsgNetworkFee(ctx.BlockHeight(), common.ETHChain, 1, 10000, GetRandomBech32Addr()))
	c.Assert(err, NotNil)
	c.Assert(result, IsNil)
	// insufficient funds
	coin, err = common.NewCoin(common.RuneNative, cosmos.NewUint(3000*common.One)).Native()
	c.Assert(err, IsNil)
	msg = &MsgSend{
		FromAddress: addr1,
		ToAddress:   addr2,
		Amount:      cosmos.NewCoins(coin),
	}
	_, err = handler.handle(ctx, msg)
	c.Assert(err, NotNil)
}

func (s *HandlerSendSuiteV87) TestDepositConversionRequiresCanonicalDenom(c *C) {
	ctx, _ := setupKeeperForTest(c)

	fromAddr := GetRandomBech32Addr()

	// "x/ruji" is the denom of the asset it parses to, so it converts
	_, err := getDeposit(ctx, fromAddr, cosmos.Coins{cosmos.NewCoin("x/ruji", cosmos.NewInt(12))})
	c.Assert(err, IsNil)

	// "x/rune" parses to THOR.RUNE, whose denom is "rune" - converting it would
	// debit native RUNE for a send of a different denom
	_, err = getDeposit(ctx, fromAddr, cosmos.Coins{cosmos.NewCoin("x/rune", cosmos.NewInt(12))})
	c.Assert(err, NotNil)
}

func (s *HandlerSendSuiteV87) TestDepositConversionRequiresDirectSigner(c *C) {
	ctx, _ := setupKeeperForTest(c)

	fromAddr := GetRandomBech32Addr()
	coins := cosmos.NewCoins(cosmos.NewCoin("rune", cosmos.NewInt(12)))
	memoCtx := ctx.WithValue(ContextKeyTxMemo, "SWAP:BTC.BTC")

	// a directly signed send is converted, inheriting the memo of the tx it signed
	signedCtx := memoCtx.WithValue(ContextKeyTxSigners, map[string]struct{}{
		string(fromAddr): {},
	})
	msgDeposit, err := getDeposit(signedCtx, fromAddr, coins)
	c.Assert(err, IsNil)
	c.Assert(msgDeposit.Memo, Equals, "SWAP:BTC.BTC")
	c.Assert(msgDeposit.Signer.Equals(fromAddr), Equals, true)

	// a send dispatched on behalf of fromAddr must not execute a memo fromAddr
	// never signed
	dispatchedCtx := memoCtx.WithValue(ContextKeyTxSigners, map[string]struct{}{
		string(GetRandomBech32Addr()): {},
	})
	_, err = getDeposit(dispatchedCtx, fromAddr, coins)
	c.Assert(err, NotNil)
}

func (s *HandlerSendSuiteV87) TestSendRequiresDirectSignerOrContract(c *C) {
	ctx, k := setupKeeperForTest(c)

	fromAddr := GetRandomBech32Addr()
	toAddr := GetRandomBech32Addr()
	handler := NewSendHandler(NewDummyMgrWithKeeper(k))

	msg := &MsgSend{
		FromAddress: fromAddr,
		ToAddress:   toAddr,
		Amount:      cosmos.NewCoins(cosmos.NewCoin("rune", cosmos.NewInt(12))),
	}

	// signed by its sender
	signedCtx := ctx.WithValue(ContextKeyTxSigners, map[string]struct{}{
		string(fromAddr): {},
	})
	c.Assert(handler.validate(signedCtx, msg), IsNil)

	// dispatched by neither the sender nor a contract
	dispatchedCtx := ctx.WithValue(ContextKeyTxSigners, map[string]struct{}{
		string(GetRandomBech32Addr()): {},
	})
	c.Assert(handler.validate(dispatchedCtx, msg), NotNil)
}

func (s *HandlerSendSuiteV87) TestBankSendEnabledGateInHandler(c *C) {
	ctx, k := setupKeeperForTest(c)

	addr1 := GetRandomBech32Addr()
	addr2 := GetRandomBech32Addr()
	FundAccount(c, ctx, k, addr1, 200*common.One)

	handler := NewBankSendHandler(NewSendHandler(NewDummyMgrWithKeeper(k)))
	msg := &banktypes.MsgSend{
		FromAddress: addr1.String(),
		ToAddress:   addr2.String(),
		Amount:      cosmos.NewCoins(cosmos.NewCoin("rune", cosmos.NewInt(12))),
	}

	// the gate is enforced by the msg server itself, not only by the ante
	// handler, so that no dispatch path can bypass it
	k.SetMimir(ctx, "BankSendEnabled", 0)
	_, err := handler.Send(ctx, msg)
	c.Assert(err, ErrorMatches, ".*bank sends are disabled.*")

	k.SetMimir(ctx, "BankSendEnabled", 1)
	_, err = handler.Send(ctx, msg)
	c.Assert(err, IsNil)
}

// helpers
func fundAccountWith(c *C, ctx cosmos.Context, k keeper.Keeper, addr cosmos.AccAddress, asset common.Asset, amt cosmos.Uint) {
	coin := common.NewCoin(asset, amt)
	c.Assert(k.MintToModule(ctx, ModuleName, coin), IsNil)
	c.Assert(k.SendFromModuleToAccount(ctx, ModuleName, addr, common.NewCoins(coin)), IsNil)
}

func isWithinTolerance(a, b cosmos.Uint, bps uint64) bool {
	if a.Equal(b) {
		return true
	}
	base := a
	if b.GT(base) {
		base = b
	}
	tol := base.Mul(cosmos.NewUint(bps)).Quo(cosmos.NewUint(10000))
	var diff cosmos.Uint
	if a.GT(b) {
		diff = a.Sub(b)
	} else {
		diff = b.Sub(a)
	}
	return diff.LTE(tol)
}

func (s *HandlerSendSuiteV87) TestAltNativeTxFeeSend(c *C) {
	// message factories to test both thor MsgSend and bank MsgSend
	factories := []struct {
		name string
		make func(from, to cosmos.AccAddress, amt cosmos.Coins) cosmos.Msg
		set  func(msg cosmos.Msg, amt cosmos.Coins) cosmos.Msg
	}{
		{
			name: "thorchain.MsgSend",
			make: func(from, to cosmos.AccAddress, amt cosmos.Coins) cosmos.Msg {
				return &types.MsgSend{FromAddress: from, ToAddress: to, Amount: amt}
			},
			set: func(msg cosmos.Msg, amt cosmos.Coins) cosmos.Msg {
				m, ok := msg.(*types.MsgSend)
				c.Assert(ok, Equals, true)
				m.Amount = amt
				return m
			},
		},
		{
			name: "banktypes.MsgSend",
			make: func(from, to cosmos.AccAddress, amt cosmos.Coins) cosmos.Msg {
				return &banktypes.MsgSend{FromAddress: from.String(), ToAddress: to.String(), Amount: amt}
			},
			set: func(msg cosmos.Msg, amt cosmos.Coins) cosmos.Msg {
				m, ok := msg.(*banktypes.MsgSend)
				c.Assert(ok, Equals, true)
				m.Amount = amt
				return m
			},
		},
	}

	for _, f := range factories {
		c.Log("case:", f.name)

		ctx, k := setupKeeperForTest(c)
		k.SetMimir(ctx, constants.BankSendEnabled.String(), 1)
		k.SetMimir(ctx, constants.AltNativeTxFeeEnabled.String(), 1)
		k.SetMimir(ctx, constants.AltNativeTxFeeAddedBps.String(), 5000)
		altNativeTxFeeAddedBps := uint64(max(0, k.GetConfigInt64(ctx, constants.AltNativeTxFeeAddedBps)))
		altNativeTxFeeAddedBps = min(altNativeTxFeeAddedBps, 90000) // sanity cap: max 10x of the normal native tx fee

		signer := GetRandomBech32Addr()
		toAcc := GetRandomBech32Addr()
		mgr := NewDummyMgrWithKeeper(k)
		ad := AnteDecorator{keeper: k}
		handler := NewSendHandler(mgr)
		mgr.networkMgr = newNetworkMgr(k, mgr.TxOutStore(), mgr.EventMgr())

		feeAsset := common.NAMI
		fundAccountWith(c, ctx, k, signer, feeAsset, cosmos.NewUint(100*common.One))
		getAccBal := func(acc cosmos.AccAddress, asset common.Asset) cosmos.Uint {
			return cosmos.NewUintFromBigInt(k.GetBalance(ctx, acc).AmountOf(asset.Native()).BigInt())
		}

		// build initial message with 15 NAMI
		sentAmt := cosmos.NewUint(15 * common.One)

		tryMsg := func(m cosmos.Msg) cosmos.Uint {
			// balances before
			fromBefore := getAccBal(signer, feeAsset)
			toBefore := getAccBal(toAcc, feeAsset)
			poolBefore, err := k.GetPool(ctx, feeAsset)
			c.Assert(err, IsNil)

			// process
			_, err = ad.anteHandleMessage(ctx, mgr.GetVersion(), m)
			c.Assert(err, IsNil)
			_, err = handler.Run(ctx, m)
			c.Assert(err, IsNil)

			// balances after
			fromAfter := getAccBal(signer, feeAsset)
			toAfter := getAccBal(toAcc, feeAsset)

			// receiver gets exactly sent amount in NAMI
			c.Assert(toAfter.String(), Equals, toBefore.Add(sentAmt).String())
			// sender balance decreased by sent amount + alt-native fee
			c.Assert(fromAfter.LT(fromBefore.Sub(sentAmt)), Equals, true)

			// the alt-native fee valued in RUNE is ~= NativeTxFee within one NAMI unit
			maxDiff := cosmos.NewUintFromBigInt(poolBefore.BalanceRune.Quo(poolBefore.BalanceAsset).BigInt())
			feeInNami := fromBefore.Sub(sentAmt).Sub(fromAfter)
			swapper, err := GetSwapper(k.GetVersion())
			c.Assert(err, IsNil)
			feeInRune := swapper.CalcAssetEmission(poolBefore.BalanceAsset, feeInNami, poolBefore.BalanceRune)
			nativeFee := k.GetNativeTxFee(ctx)
			nativeFee = nativeFee.MulUint64(altNativeTxFeeAddedBps + 10000).QuoUint64(10000)

			inTol := nativeFee.Sub(maxDiff).LTE(feeInRune) && feeInRune.LTE(nativeFee.Add(maxDiff))
			c.Assert(inTol, Equals, true)
			return feeInNami
		}

		// pool: NAMI:RUNE = 1:200 (5,000:1,000,000)
		pool := NewPool()
		pool.Asset = feeAsset
		pool.BalanceAsset = cosmos.NewUint(5_000 * common.One)
		pool.BalanceRune = cosmos.NewUint(1_000_000 * common.One)
		pool.Status = PoolAvailable
		c.Assert(k.SetPool(ctx, pool), IsNil)

		nativeCoins, err := common.Coins{common.NewCoin(feeAsset, sentAmt)}.Native()
		c.Assert(err, IsNil)
		msg := f.make(signer, toAcc, nativeCoins)

		prevFeeInNami := tryMsg(msg)

		// Change pool ratio to 1:100 and verify fee ~ doubles
		pool.BalanceAsset = cosmos.NewUint(10_000 * common.One)
		pool.BalanceRune = cosmos.NewUint(1_000_000 * common.One)
		c.Assert(k.SetPool(ctx, pool), IsNil)

		curFeeInNami := tryMsg(msg)

		c.Assert(isWithinTolerance(curFeeInNami, prevFeeInNami.MulUint64(2), 10), Equals, true) // within 10 bps tolerance

		// Disabled when pool unavailable
		pool.Status = PoolStaged
		c.Assert(k.SetPool(ctx, pool), IsNil)
		_, err = ad.anteHandleMessage(ctx, mgr.GetVersion(), msg)
		c.Assert(err, NotNil)
		c.Assert(strings.Contains(err.Error(), "fail to fetch native coin to pay tx fee: fail to find coins to pay tx fee"), Equals, true)

		// Insufficient alt-native fee should be rejected in ante, like RUNE fees.
		pool.Status = PoolAvailable
		c.Assert(k.SetPool(ctx, pool), IsNil)
		nativeFeeInNami, err := calculateAltNativeTxFee(ctx, k, feeAsset)
		c.Assert(err, IsNil)
		signer = GetRandomBech32Addr()
		fundAccountWith(c, ctx, mgr.Keeper(), signer, feeAsset, nativeFeeInNami.MulUint64(3).QuoUint64(2)) // 1.5x fee
		smallSend, err := common.Coins{common.NewCoin(feeAsset, cosmos.OneUint())}.Native()
		c.Assert(err, IsNil)
		toAcc = GetRandomBech32Addr()
		msg = f.set(f.make(signer, toAcc, smallSend), smallSend)

		_, err = ad.anteHandleMessage(ctx, mgr.GetVersion(), msg)
		c.Assert(err, IsNil)
		_, err = handler.Run(ctx, msg)
		c.Assert(err, IsNil)
		fromAfter := getAccBal(signer, feeAsset)
		toAfter := getAccBal(toAcc, feeAsset)
		c.Assert(!fromAfter.IsZero() && fromAfter.LT(nativeFeeInNami), Equals, true)
		c.Assert(toAfter.String(), Equals, "1")

		// Next run should fail in ante and leave balances unchanged.
		fromBefore := fromAfter
		toBefore := toAfter
		_, err = ad.anteHandleMessage(ctx, mgr.GetVersion(), msg)
		c.Assert(err, NotNil)
		c.Assert(strings.Contains(err.Error(), "insufficient funds"), Equals, true)
		fromAfter = getAccBal(signer, feeAsset)
		toAfter = getAccBal(toAcc, feeAsset)
		c.Assert(fromAfter.String(), Equals, fromBefore.String())
		c.Assert(toAfter.String(), Equals, toBefore.String())

		// Repeating the tx should keep failing in ante without draining funds.
		_, err = ad.anteHandleMessage(ctx, mgr.GetVersion(), msg)
		c.Assert(err, NotNil)
		c.Assert(strings.Contains(err.Error(), "insufficient funds"), Equals, true)
		c.Assert(getAccBal(signer, feeAsset).String(), Equals, fromBefore.String())
	}
}

// TestAltNativeTxFeeFallsBackToFundedAsset verifies a multi-asset send skips a
// priceable but underfunded first asset and charges the next funded candidate.
func (s *HandlerSendSuiteV87) TestAltNativeTxFeeFallsBackToFundedAsset(c *C) {
	ctx, k := setupKeeperForTest(c)
	k.SetMimir(ctx, constants.AltNativeTxFeeEnabled.String(), 1)
	signer := GetRandomBech32Addr()

	seedPool := func(asset common.Asset) {
		pool := NewPool()
		pool.Asset = asset
		pool.Status = PoolAvailable
		pool.BalanceAsset = cosmos.NewUint(1_000_000 * common.One)
		pool.BalanceRune = cosmos.NewUint(200_000_000 * common.One)
		c.Assert(k.SetPool(ctx, pool), IsNil)
	}
	seedPool(common.NAMI)
	seedPool(common.RUJI)

	namiFee, err := calculateAltNativeTxFee(ctx, k, common.NAMI)
	c.Assert(err, IsNil)
	rujiFee, err := calculateAltNativeTxFee(ctx, k, common.RUJI)
	c.Assert(err, IsNil)
	c.Assert(namiFee.GT(cosmos.OneUint()), Equals, true)
	fundAccountWith(c, ctx, k, signer, common.NAMI, namiFee.Sub(cosmos.OneUint()))
	fundAccountWith(c, ctx, k, signer, common.RUJI, rujiFee.Add(cosmos.NewUint(10)))

	coins, err := common.NewCoins(
		common.NewCoin(common.NAMI, cosmos.OneUint()),
		common.NewCoin(common.RUJI, cosmos.OneUint()),
	).Native()
	c.Assert(err, IsNil)
	msg := &MsgSend{FromAddress: signer, ToAddress: GetRandomBech32Addr(), Amount: coins}
	namiBefore := getAltNativeTxFeeBalance(ctx, k, signer, common.NAMI)
	rujiBefore := getAltNativeTxFeeBalance(ctx, k, signer, common.RUJI)

	c.Assert(deductNativeTxFeeFromAccount(ctx, k, signer, msg), IsNil)

	c.Assert(getAltNativeTxFeeBalance(ctx, k, signer, common.NAMI).Equal(namiBefore), Equals, true)
	c.Assert(getAltNativeTxFeeBalance(ctx, k, signer, common.RUJI).Equal(rujiBefore.Sub(rujiFee)), Equals, true)
	_, namiEscrowExists := k.GetAltNativeFeeEscrow(ctx, common.NAMI)
	c.Assert(namiEscrowExists, Equals, false)
	rujiEscrow, rujiEscrowExists := k.GetAltNativeFeeEscrow(ctx, common.RUJI)
	c.Assert(rujiEscrowExists, Equals, true)
	c.Assert(rujiEscrow.Balance.Amount.Equal(rujiFee), Equals, true)
}

// TestAltNativeTxFeePreservesCollectionError verifies operational collection
// failures are returned to callers instead of being mislabeled insufficient funds.
func (s *HandlerSendSuiteV87) TestAltNativeTxFeePreservesCollectionError(c *C) {
	ctx, baseKeeper := setupKeeperForTest(c)
	baseKeeper.SetMimir(ctx, constants.AltNativeTxFeeEnabled.String(), 1)
	asset := common.NAMI
	pool := NewPool()
	pool.Asset = asset
	pool.Status = PoolAvailable
	pool.BalanceAsset = cosmos.NewUint(1_000_000 * common.One)
	pool.BalanceRune = cosmos.NewUint(200_000_000 * common.One)
	c.Assert(baseKeeper.SetPool(ctx, pool), IsNil)

	signer := GetRandomBech32Addr()
	fee, err := calculateAltNativeTxFee(ctx, baseKeeper, asset)
	c.Assert(err, IsNil)
	fundAccountWith(c, ctx, baseKeeper, signer, asset, fee.Add(cosmos.OneUint()))
	balanceBefore := getAltNativeTxFeeBalance(ctx, baseKeeper, signer, asset)
	coins, err := common.NewCoins(common.NewCoin(asset, cosmos.OneUint())).Native()
	c.Assert(err, IsNil)
	msg := &MsgSend{FromAddress: signer, ToAddress: GetRandomBech32Addr(), Amount: coins}
	failingKeeper := &altNativeFeeCollectionFailKeeper{Keeper: baseKeeper}

	err = deductNativeTxFeeFromAccount(ctx, failingKeeper, signer, msg)
	c.Assert(err, NotNil)
	c.Assert(errors.Is(err, errKaboom), Equals, true)
	c.Assert(strings.Contains(err.Error(), "kaboom"), Equals, true)
	c.Assert(strings.Contains(err.Error(), "insufficient funds"), Equals, false)
	c.Assert(getAltNativeTxFeeBalance(ctx, baseKeeper, signer, asset).Equal(balanceBefore), Equals, true)
	_, exists := baseKeeper.GetAltNativeFeeEscrow(ctx, asset)
	c.Assert(exists, Equals, false)
}
