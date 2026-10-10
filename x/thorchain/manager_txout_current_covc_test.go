package thorchain

import (
	"fmt"
	"strings"

	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
)

type CovCCTxOutSuite struct{}

var _ = Suite(&CovCCTxOutSuite{})

func covCCTxOutStore(w handlerTestWrapper, evt EventManager) *TxOutStorage {
	if evt == nil {
		evt = w.mgr.EventMgr()
	}
	return newTxOutStorage(w.keeper, w.mgr.GetConstants(), evt, w.mgr.GasMgr())
}

func (s *CovCCTxOutSuite) TestNoopAndNullAddressOutbounds(c *C) {
	w := getHandlerTestWrapper(c, 1, true, true)
	rec := newRecordingEventMgr()
	tos := covCCTxOutStore(w, rec)

	// noop destination: accepted but nothing scheduled
	toi := TxOutItem{
		Chain:     common.ETHChain,
		ToAddress: common.Address("noop"),
		InHash:    GetRandomTxHash(),
		Coin:      common.NewCoin(common.ETHAsset, cosmos.NewUint(common.One)),
	}
	c.Assert(toi.ToAddress.IsNoop(), Equals, true)
	ok, err := tos.TryAddTxOutItem(w.ctx, w.mgr, toi, cosmos.ZeroUint())
	c.Assert(err, IsNil)
	c.Assert(ok, Equals, true)
	c.Assert(tos.UnSafeAddTxOutItem(w.ctx, w.mgr, toi, w.ctx.BlockHeight()), IsNil)

	// EVM null address: dropped with security event
	toi.ToAddress = common.EVMNullAddress
	ok, err = tos.TryAddTxOutItem(w.ctx, w.mgr, toi, cosmos.ZeroUint())
	c.Assert(err, IsNil)
	c.Assert(ok, Equals, true)
	c.Assert(tos.UnSafeAddTxOutItem(w.ctx, w.mgr, toi, w.ctx.BlockHeight()), IsNil)

	sec := rec.securityEvents()
	c.Assert(sec, HasLen, 2)
	c.Check(sec[0].Msg, Equals, "evm outbound to null address")
	c.Check(sec[1].Msg, Equals, "evm outbound to null address in UnSafeAddTxOutItem")

	items, err := tos.GetOutboundItems(w.ctx)
	c.Assert(err, IsNil)
	c.Assert(items, HasLen, 0)
}

func (s *CovCCTxOutSuite) TestUnSafeAddBCHAndFilter(c *C) {
	w := getHandlerTestWrapper(c, 1, true, true)
	tos := covCCTxOutStore(w, nil)

	bchAddr := GetRandomBCHAddress()
	toi := TxOutItem{
		Chain:       common.BCHChain,
		ToAddress:   bchAddr,
		VaultPubKey: GetRandomPubKey(),
		InHash:      GetRandomTxHash(),
		Coin:        common.NewCoin(common.BCHAsset, cosmos.NewUint(common.One)),
		MaxGas:      common.Gas{common.NewCoin(common.BCHAsset, cosmos.NewUint(100))},
	}
	c.Assert(tos.UnSafeAddTxOutItem(w.ctx, w.mgr, toi, w.ctx.BlockHeight()), IsNil)

	// not a BCH address -> conversion error
	bad := toi
	bad.ToAddress = GetRandomETHAddress()
	err := tos.UnSafeAddTxOutItem(w.ctx, w.mgr, bad, w.ctx.BlockHeight())
	c.Assert(err, NotNil)
	c.Assert(err.Error(), Matches, ".*fail to convert BCH address.*")

	// a second item to a different address
	other := TxOutItem{
		Chain:       common.ETHChain,
		ToAddress:   GetRandomETHAddress(),
		VaultPubKey: GetRandomPubKey(),
		InHash:      GetRandomTxHash(),
		Coin:        common.NewCoin(common.ETHAsset, cosmos.NewUint(common.One)),
	}
	c.Assert(tos.UnSafeAddTxOutItem(w.ctx, w.mgr, other, w.ctx.BlockHeight()), IsNil)

	all, err := tos.GetOutboundItems(w.ctx)
	c.Assert(err, IsNil)
	c.Assert(all, HasLen, 2)

	filtered := tos.GetOutboundItemByToAddress(w.ctx, other.ToAddress)
	c.Assert(filtered, HasLen, 1)
	c.Assert(filtered[0].ToAddress.Equals(other.ToAddress), Equals, true)
	c.Assert(tos.GetOutboundItemByToAddress(w.ctx, GetRandomETHAddress()), HasLen, 0)

	// filtering an empty block returns empty
	c.Assert(tos.GetOutboundItemByToAddress(w.ctx.WithBlockHeight(9999), other.ToAddress), HasLen, 0)
}

func (s *CovCCTxOutSuite) TestEndBlockSkipsPastAndFailedGas(c *C) {
	w := getHandlerTestWrapper(c, 10, true, true)
	tos := covCCTxOutStore(w, nil)

	// voter with outbound height in the past -> MaxGas not refreshed
	pastItem := TxOutItem{
		Chain:       common.ETHChain,
		ToAddress:   GetRandomETHAddress(),
		VaultPubKey: GetRandomPubKey(),
		InHash:      GetRandomTxHash(),
		Coin:        common.NewCoin(common.ETHAsset, cosmos.NewUint(common.One)),
		MaxGas:      common.Gas{common.NewCoin(common.ETHAsset, cosmos.NewUint(7))},
	}
	pastVoter := NewObservedTxVoter(pastItem.InHash, nil)
	pastVoter.OutboundHeight = 5
	w.keeper.SetObservedTxInVoter(w.ctx, pastVoter)
	c.Assert(tos.UnSafeAddTxOutItem(w.ctx, w.mgr, pastItem, w.ctx.BlockHeight()), IsNil)

	// two items on a chain with no network fee -> gas details lookup fails once,
	// second item skipped via the failure cache
	for i := 0; i < 2; i++ {
		item := TxOutItem{
			Chain:       common.AVAXChain,
			ToAddress:   GetRandomETHAddress(),
			VaultPubKey: GetRandomPubKey(),
			InHash:      GetRandomTxHash(),
			Coin:        common.NewCoin(common.AVAXAsset, cosmos.NewUint(common.One)),
			MaxGas:      common.Gas{common.NewCoin(common.AVAXAsset, cosmos.NewUint(3))},
		}
		c.Assert(tos.UnSafeAddTxOutItem(w.ctx, w.mgr, item, w.ctx.BlockHeight()), IsNil)
	}

	c.Assert(tos.EndBlock(w.ctx, w.mgr), IsNil)

	items, err := tos.GetOutboundItems(w.ctx)
	c.Assert(err, IsNil)
	c.Assert(items, HasLen, 3)
	for _, item := range items {
		switch item.Chain {
		case common.ETHChain:
			c.Check(item.MaxGas[0].Amount.Uint64(), Equals, uint64(7))
			c.Check(item.GasRate, Equals, int64(0))
		case common.AVAXChain:
			c.Check(item.MaxGas[0].Amount.Uint64(), Equals, uint64(3))
			c.Check(item.GasRate, Equals, int64(0))
		default:
			c.Fatalf("unexpected chain %s", item.Chain)
		}
	}
}

func (s *CovCCTxOutSuite) TestEndBlockKeepsInactiveVaultRefundWithinGasBudget(c *C) {
	w := getHandlerTestWrapper(c, 649, true, true)
	tos := covCCTxOutStore(w, nil)
	pubkey := GetRandomPubKey()
	vault := NewVault(w.ctx.BlockHeight(), InactiveVault, AsgardVault, pubkey, common.Chains{common.BTCChain}.Strings(), nil)
	c.Assert(w.keeper.SetVault(w.ctx, vault), IsNil)

	inHash := GetRandomTxHash()
	item := TxOutItem{
		Chain:          common.BTCChain,
		ToAddress:      GetRandomBTCAddress(),
		VaultPubKey:    pubkey,
		InHash:         inHash,
		Coin:           common.NewCoin(common.BTCAsset, cosmos.NewUint(2_560_083)),
		MaxGas:         common.Gas{common.NewCoin(common.BTCAsset, cosmos.NewUint(49_000))},
		MaxGasDeducted: true,
	}
	voter := NewObservedTxVoter(inHash, nil)
	voter.Actions = []TxOutItem{item}
	w.keeper.SetObservedTxInVoter(w.ctx, voter)
	c.Assert(w.keeper.SetTxOut(w.ctx, &TxOut{Height: w.ctx.BlockHeight(), TxArray: []TxOutItem{item}}), IsNil)

	check := func(feeRate, wantGas, wantAmount uint64) {
		c.Assert(w.keeper.SaveNetworkFee(w.ctx, common.BTCChain, NewNetworkFee(common.BTCChain, 1000, feeRate)), IsNil)
		c.Assert(tos.EndBlock(w.ctx, w.mgr), IsNil)
		items, err := tos.GetOutboundItems(w.ctx)
		c.Assert(err, IsNil)
		c.Assert(items, HasLen, 1)
		updated, err := w.keeper.GetObservedTxInVoter(w.ctx, inHash)
		c.Assert(err, IsNil)
		c.Assert(updated.Actions, HasLen, 1)
		for _, out := range []TxOutItem{items[0], updated.Actions[0]} {
			c.Check(out.MaxGas.ToCoins().GetCoin(common.BTCAsset).Amount.Uint64(), Equals, wantGas)
			c.Check(out.Coin.Amount.Uint64(), Equals, wantAmount)
			c.Check(out.Coin.Amount.Add(out.MaxGas.ToCoins().GetCoin(common.BTCAsset).Amount).Uint64(), Equals, uint64(2_609_083))
			c.Check(out.MaxGasDeducted, Equals, true)
		}
		c.Check(items[0].GasRate, Equals, updated.Actions[0].GasRate)
	}

	check(56, 84_000, 2_525_083)
	check(20, 30_000, 2_579_083)
}

func (s *CovCCTxOutSuite) TestEndBlockKeepsRefundAboveDustAfterGasIncrease(c *C) {
	w := getHandlerTestWrapper(c, 649, true, true)
	tos := covCCTxOutStore(w, nil)
	inHash := GetRandomTxHash()
	item := TxOutItem{
		Chain:          common.BTCChain,
		ToAddress:      GetRandomBTCAddress(),
		VaultPubKey:    GetRandomPubKey(),
		InHash:         inHash,
		Coin:           common.NewCoin(common.BTCAsset, cosmos.NewUint(6_500)),
		MaxGas:         common.Gas{common.NewCoin(common.BTCAsset, cosmos.NewUint(3_000))},
		MaxGasDeducted: true,
	}
	voter := NewObservedTxVoter(inHash, nil)
	voter.Actions = []TxOutItem{item}
	w.keeper.SetObservedTxInVoter(w.ctx, voter)
	c.Assert(w.keeper.SetTxOut(w.ctx, &TxOut{Height: w.ctx.BlockHeight(), TxArray: []TxOutItem{item}}), IsNil)

	// 6,500 + 3,000 - 9,000 = 500 sats, below BTC's dust threshold.
	c.Assert(w.keeper.SaveNetworkFee(w.ctx, common.BTCChain, NewNetworkFee(common.BTCChain, 1000, 6)), IsNil)
	c.Assert(tos.EndBlock(w.ctx, w.mgr), IsNil)
	items, err := tos.GetOutboundItems(w.ctx)
	c.Assert(err, IsNil)
	updated, err := w.keeper.GetObservedTxInVoter(w.ctx, inHash)
	c.Assert(err, IsNil)
	c.Assert(items, HasLen, 1)
	c.Assert(updated.Actions, HasLen, 1)
	for _, out := range []TxOutItem{items[0], updated.Actions[0]} {
		c.Check(out.MaxGas.ToCoins().GetCoin(common.BTCAsset).Amount.Uint64(), Equals, uint64(3_000))
		c.Check(out.Coin.Amount.Uint64(), Equals, uint64(6_500))
	}
}

func (s *CovCCTxOutSuite) TestEndBlockPreservesNormalOutboundAfterVaultRetires(c *C) {
	w := getHandlerTestWrapper(c, 649, true, true)
	tos := covCCTxOutStore(w, nil)
	pubkey := GetRandomPubKey()
	vault := NewVault(w.ctx.BlockHeight(), RetiringVault, AsgardVault, pubkey, common.Chains{common.BTCChain}.Strings(), nil)
	c.Assert(w.keeper.SetVault(w.ctx, vault), IsNil)

	inHash := GetRandomTxHash()
	item := TxOutItem{
		Chain:       common.BTCChain,
		ToAddress:   GetRandomBTCAddress(),
		VaultPubKey: pubkey,
		InHash:      inHash,
		Coin:        common.NewCoin(common.BTCAsset, cosmos.NewUint(2_560_083)),
		MaxGas:      common.Gas{common.NewCoin(common.BTCAsset, cosmos.NewUint(49_000))},
	}
	voter := NewObservedTxVoter(inHash, nil)
	voter.Actions = []TxOutItem{item}
	w.keeper.SetObservedTxInVoter(w.ctx, voter)
	c.Assert(w.keeper.SetTxOut(w.ctx, &TxOut{Height: w.ctx.BlockHeight(), TxArray: []TxOutItem{item}}), IsNil)

	// The delayed outbound was charged its normal outbound fee when scheduled.
	// Its selected retiring vault drains before the signing block.
	vault.Status = InactiveVault
	c.Assert(w.keeper.SetVault(w.ctx, vault), IsNil)
	for _, test := range []struct{ feeRate, gas uint64 }{{56, 84_000}, {20, 30_000}} {
		c.Assert(w.keeper.SaveNetworkFee(w.ctx, common.BTCChain, NewNetworkFee(common.BTCChain, 1000, test.feeRate)), IsNil)
		c.Assert(tos.EndBlock(w.ctx, w.mgr), IsNil)
		items, err := tos.GetOutboundItems(w.ctx)
		c.Assert(err, IsNil)
		updated, err := w.keeper.GetObservedTxInVoter(w.ctx, inHash)
		c.Assert(err, IsNil)
		for _, out := range []TxOutItem{items[0], updated.Actions[0]} {
			c.Check(out.MaxGas.ToCoins().GetCoin(common.BTCAsset).Amount.Uint64(), Equals, test.gas)
			c.Check(out.Coin.Amount.Uint64(), Equals, uint64(2_560_083))
			c.Check(out.MaxGasDeducted, Equals, false)
		}
	}
}

func (s *CovCCTxOutSuite) TestTakeAffiliateFeeBranches(c *C) {
	w := getHandlerTestWrapper(c, 1, true, true)
	tos := covCCTxOutStore(w, nil)

	setInbound := func(memo string) common.TxID {
		id := GetRandomTxHash()
		tx := GetRandomTx()
		tx.ID = id
		tx.Memo = memo
		voter := NewObservedTxVoter(id, common.ObservedTxs{common.ObservedTx{Tx: tx}})
		voter.Tx = voter.Txs[0]
		w.keeper.SetObservedTxInVoter(w.ctx, voter)
		return id
	}

	amt := cosmos.NewUint(100 * common.One)

	// refund outbounds are exempt
	toi := TxOutItem{
		Chain:     common.THORChain,
		ToAddress: GetRandomRUNEAddress(),
		InHash:    GetRandomTxHash(),
		Coin:      common.NewCoin(common.RuneAsset(), amt),
		Memo:      "REFUND:" + GetRandomTxHash().String(),
	}
	out, err := tos.takeAffiliateFee(w.ctx, w.mgr, toi)
	c.Assert(err, IsNil)
	c.Assert(out.Equal(amt), Equals, true)

	// unparsable inbound memo -> error and unchanged amount
	toi.Memo = ""
	toi.InHash = setInbound("NOTAMEMO:foo")
	out, err = tos.takeAffiliateFee(w.ctx, w.mgr, toi)
	c.Assert(err, NotNil)
	c.Assert(out.Equal(amt), Equals, true)

	// rune outbound for a non-rune target (the affiliate fee outbound itself)
	toi.InHash = setInbound(fmt.Sprintf("=:ETH.ETH:%s::%s:100", GetRandomETHAddress(), GetRandomRUNEAddress()))
	out, err = tos.takeAffiliateFee(w.ctx, w.mgr, toi)
	c.Assert(err, IsNil)
	c.Assert(out.Equal(amt), Equals, true)

	// swap to RUNE with a 1% affiliate -> 1% skimmed and paid to affiliate
	aff := GetRandomRUNEAddress()
	toi.InHash = setInbound(fmt.Sprintf("=:THOR.RUNE:%s::%s:100", GetRandomRUNEAddress(), aff))
	out, err = tos.takeAffiliateFee(w.ctx, w.mgr, toi)
	c.Assert(err, IsNil)
	c.Assert(out.Uint64(), Equals, uint64(99*common.One))
	affAcc, err := aff.AccAddress()
	c.Assert(err, IsNil)
	c.Assert(w.keeper.GetBalance(w.ctx, affAcc).AmountOf(common.RuneNative.Native()).Uint64(), Equals, uint64(common.One))

	// no active validators -> error
	na := w.activeNodeAccount
	na.Status = NodeStandby
	c.Assert(w.keeper.SetNodeAccount(w.ctx, na), IsNil)
	toi.InHash = setInbound(fmt.Sprintf("=:THOR.RUNE:%s::%s:100", GetRandomRUNEAddress(), aff))
	out, err = tos.takeAffiliateFee(w.ctx, w.mgr, toi)
	c.Assert(err, NotNil)
	c.Assert(err.Error(), Matches, ".*no active node accounts.*")
	c.Assert(out.Equal(amt), Equals, true)
}

func (s *CovCCTxOutSuite) TestCalcClout(c *C) {
	w := getHandlerTestWrapper(c, 100, true, true)
	tos := covCCTxOutStore(w, nil)

	fromAddr := GetRandomETHAddress()
	toAddr := GetRandomETHAddress()
	inHash := GetRandomTxHash()
	tx := GetRandomTx()
	tx.ID = inHash
	tx.FromAddress = fromAddr
	voter := NewObservedTxVoter(inHash, common.ObservedTxs{common.ObservedTx{Tx: tx}})
	voter.Tx = voter.Txs[0]
	w.keeper.SetObservedTxInVoter(w.ctx, voter)

	cloutIn := NewSwapperClout(fromAddr)
	cloutIn.Score = cosmos.NewUint(100 * common.One)
	c.Assert(w.keeper.SetSwapperClout(w.ctx, cloutIn), IsNil)
	cloutOut := NewSwapperClout(toAddr)
	cloutOut.Score = cosmos.NewUint(100 * common.One)
	c.Assert(w.keeper.SetSwapperClout(w.ctx, cloutOut), IsNil)

	w.keeper.SetMimir(w.ctx, constants.CloutLimit.String(), 1000*common.One)

	toi := TxOutItem{
		Chain:     common.ETHChain,
		ToAddress: toAddr,
		InHash:    inHash,
		Coin:      common.NewCoin(common.ETHAsset, cosmos.NewUint(common.One)),
	}

	// aggregator outbounds get no clout
	agg := toi
	agg.Aggregator = "0x1234"
	val, applied := tos.calcClout(w.ctx, cosmos.NewUint(50*common.One), agg)
	c.Assert(val.Uint64(), Equals, uint64(50*common.One))
	c.Assert(applied.IsZero(), Equals, true)

	// clout covers the whole value split across in/out
	val, applied = tos.calcClout(w.ctx, cosmos.NewUint(50*common.One), toi)
	c.Assert(val.IsZero(), Equals, true)
	c.Assert(applied.Uint64(), Equals, uint64(50*common.One))

	gotIn, err := w.keeper.GetSwapperClout(w.ctx, fromAddr)
	c.Assert(err, IsNil)
	gotOut, err := w.keeper.GetSwapperClout(w.ctx, toAddr)
	c.Assert(err, IsNil)
	c.Assert(gotIn.Spent.Add(gotOut.Spent).Uint64(), Equals, uint64(50*common.One))
	c.Assert(gotIn.LastSpentHeight, Equals, int64(100))
	c.Assert(gotOut.LastSpentHeight, Equals, int64(100))

	// same in/out address: both shares accrue to the single clout record
	selfHash := GetRandomTxHash()
	tx.ID = selfHash
	tx.FromAddress = toAddr
	voter = NewObservedTxVoter(selfHash, common.ObservedTxs{common.ObservedTx{Tx: tx}})
	voter.Tx = voter.Txs[0]
	w.keeper.SetObservedTxInVoter(w.ctx, voter)
	before, err := w.keeper.GetSwapperClout(w.ctx, toAddr)
	c.Assert(err, IsNil)
	self := toi
	self.InHash = selfHash
	val, applied = tos.calcClout(w.ctx, cosmos.NewUint(10*common.One), self)
	c.Assert(val.IsZero(), Equals, true)
	c.Assert(applied.Uint64(), Equals, uint64(10*common.One))
	after, err := w.keeper.GetSwapperClout(w.ctx, toAddr)
	c.Assert(err, IsNil)
	c.Assert(after.Spent.Sub(before.Spent).Uint64(), Equals, uint64(10*common.One))
}

func (s *CovCCTxOutSuite) TestNativeTxOutDestinations(c *C) {
	w := getHandlerTestWrapper(c, 1, true, false)
	vault := GetRandomVault()
	vault.Chains = append(vault.Chains, common.THORChain.String())
	c.Assert(w.keeper.SetVault(w.ctx, vault), IsNil)
	tos := covCCTxOutStore(w, nil)

	mkToi := func(to common.Address, coin common.Coin, module string) TxOutItem {
		in := GetRandomTxHash()
		return TxOutItem{
			Chain:      common.THORChain,
			ToAddress:  to,
			InHash:     in,
			Coin:       coin,
			ModuleName: module,
			Memo:       NewOutboundMemo(in).String(),
		}
	}

	// minted from the thorchain module to a user
	user := GetRandomRUNEAddress()
	userAcc, err := user.AccAddress()
	c.Assert(err, IsNil)
	c.Assert(tos.nativeTxOut(w.ctx, w.mgr, mkToi(user, common.NewCoin(common.RuneAsset(), cosmos.NewUint(common.One)), ModuleName)), IsNil)
	c.Assert(w.keeper.GetBalance(w.ctx, userAcc).AmountOf(common.RuneNative.Native()).Uint64(), Equals, uint64(common.One))

	// module destinations, sourced from asgard
	for _, name := range []string{ReserveName, AffiliateCollectorName, TCYClaimingName} {
		addr, addrErr := w.keeper.GetModuleAddress(name)
		c.Assert(addrErr, IsNil)
		before := w.keeper.GetRuneBalanceOfModule(w.ctx, name)
		c.Assert(tos.nativeTxOut(w.ctx, w.mgr, mkToi(addr, common.NewCoin(common.RuneAsset(), cosmos.NewUint(2*common.One)), AsgardName)), IsNil)
		after := w.keeper.GetRuneBalanceOfModule(w.ctx, name)
		c.Assert(after.Sub(before).Uint64(), Equals, uint64(2*common.One), Commentf("module %s", name))
	}

	// trade asset outbound credits the trade account
	tradeAsset := common.ETHAsset.GetTradeAsset()
	c.Assert(tos.nativeTxOut(w.ctx, w.mgr, mkToi(user, common.NewCoin(tradeAsset, cosmos.NewUint(3*common.One)), AsgardName)), IsNil)
	tr, err := w.keeper.GetTradeAccount(w.ctx, userAcc, tradeAsset)
	c.Assert(err, IsNil)
	c.Assert(tr.Units.Uint64(), Equals, uint64(3*common.One))

	// invalid destination address
	err = tos.nativeTxOut(w.ctx, w.mgr, mkToi(GetRandomETHAddress(), common.NewCoin(common.RuneAsset(), cosmos.NewUint(1)), AsgardName))
	c.Assert(err, NotNil)

	// insufficient module funds
	err = tos.nativeTxOut(w.ctx, w.mgr, mkToi(user, common.NewCoin(common.RuneAsset(), cosmos.NewUint(1)), TCYStakeName))
	c.Assert(err, NotNil)
}

func covCCSetInboundMemo(w handlerTestWrapper, memo string) common.TxID {
	id := GetRandomTxHash()
	tx := GetRandomTx()
	tx.ID = id
	tx.Memo = memo
	voter := NewObservedTxVoter(id, common.ObservedTxs{common.ObservedTx{Tx: tx}})
	voter.Tx = voter.Txs[0]
	w.keeper.SetObservedTxInVoter(w.ctx, voter)
	return id
}

func (s *CovCCTxOutSuite) TestZeroRuneOutboundDropped(c *C) {
	w := getHandlerTestWrapper(c, 1, true, false)
	tos := covCCTxOutStore(w, nil)

	mk := func(in common.TxID) TxOutItem {
		return TxOutItem{
			Chain:     common.THORChain,
			ToAddress: GetRandomRUNEAddress(),
			InHash:    in,
			// native RUNE has no outbound fee, so a zero amount stays empty
			Coin: common.NewCoin(common.RuneAsset(), cosmos.ZeroUint()),
		}
	}

	// unknown inbound (empty voter) -> dropped, nothing to send
	ok, err := tos.TryAddTxOutItem(w.ctx, w.mgr, mk(GetRandomTxHash()), cosmos.ZeroUint())
	c.Assert(ok, Equals, false)
	c.Assert(err, Equals, ErrNotEnoughToPayFee)

	// inbound with unparsable memo -> dropped
	ok, err = tos.TryAddTxOutItem(w.ctx, w.mgr, mk(covCCSetInboundMemo(w, "NOTAMEMO")), cosmos.ZeroUint())
	c.Assert(ok, Equals, false)
	c.Assert(err, Equals, ErrNotEnoughToPayFee)

	// inbound non-withdraw memo -> dropped
	ok, err = tos.TryAddTxOutItem(w.ctx, w.mgr, mk(covCCSetInboundMemo(w, "=:THOR.RUNE:"+GetRandomRUNEAddress().String())), cosmos.ZeroUint())
	c.Assert(ok, Equals, false)
	c.Assert(err, Equals, ErrNotEnoughToPayFee)

	// inbound withdraw -> error so the withdraw handler can restore units
	ok, err = tos.TryAddTxOutItem(w.ctx, w.mgr, mk(covCCSetInboundMemo(w, "-:DOGE.DOGE:10000")), cosmos.ZeroUint())
	c.Assert(ok, Equals, false)
	c.Assert(err, ErrorMatches, ".*tx out item has zero coin")
}

func (s *CovCCTxOutSuite) TestZeroTokenOutboundDropped(c *C) {
	w := getHandlerTestWrapper(c, 1, true, false)
	tos := covCCTxOutStore(w, nil)

	token, err := common.NewAsset("ETH.TKN-0X17AB05351FC94A1A67BF3F56DDBB941AE6C63E25")
	c.Assert(err, IsNil)
	pool := NewPool()
	pool.Asset = token
	pool.Status = PoolAvailable
	pool.BalanceRune = cosmos.NewUint(100 * common.One)
	pool.BalanceAsset = cosmos.NewUint(100 * common.One)
	pool.LPUnits = cosmos.NewUint(100 * common.One)
	c.Assert(w.keeper.SetPool(w.ctx, pool), IsNil)

	vault := GetRandomVault()
	c.Assert(w.keeper.SetVault(w.ctx, vault), IsNil)

	mk := func(in common.TxID) TxOutItem {
		return TxOutItem{
			Chain:       common.ETHChain,
			ToAddress:   GetRandomETHAddress(),
			VaultPubKey: vault.PubKey,
			InHash:      in,
			Coin:        common.NewCoin(token, cosmos.ZeroUint()),
		}
	}

	for _, memo := range []string{"", "NOTAMEMO", "=:ETH.ETH:" + GetRandomETHAddress().String()} {
		in := GetRandomTxHash()
		if memo != "" {
			in = covCCSetInboundMemo(w, memo)
		}
		ok, addErr := tos.TryAddTxOutItem(w.ctx, w.mgr, mk(in), cosmos.ZeroUint())
		c.Assert(ok, Equals, false, Commentf("memo %q", memo))
		c.Assert(addErr, Equals, ErrNotEnoughToPayFee, Commentf("memo %q", memo))
	}

	ok, err := tos.TryAddTxOutItem(w.ctx, w.mgr, mk(covCCSetInboundMemo(w, "-:ETH.TKN-0X17AB05351FC94A1A67BF3F56DDBB941AE6C63E25:10000")), cosmos.ZeroUint())
	c.Assert(ok, Equals, false)
	c.Assert(err, ErrorMatches, ".*tx out item has zero coin")

	// pool unchanged: dropped outputs must not touch balances
	after, err := w.keeper.GetPool(w.ctx, token)
	c.Assert(err, IsNil)
	c.Assert(after.BalanceRune.Equal(pool.BalanceRune), Equals, true)
	c.Assert(after.BalanceAsset.Equal(pool.BalanceAsset), Equals, true)
}

func (s *CovCCTxOutSuite) TestMemoArbitraryDataTruncated(c *C) {
	w := getHandlerTestWrapper(c, 1, true, true)
	tos := covCCTxOutStore(w, nil)
	vault := GetRandomVault()
	vault.Coins = common.Coins{common.NewCoin(common.DOGEAsset, cosmos.NewUint(10000*common.One))}
	c.Assert(w.keeper.SetVault(w.ctx, vault), IsNil)

	in := covCCSetInboundMemo(w, "=:DOGE.DOGE:"+GetRandomDOGEAddress().String()+"|"+strings.Repeat("x", 200))
	toi := TxOutItem{
		Chain:       common.DOGEChain,
		ToAddress:   GetRandomDOGEAddress(),
		VaultPubKey: vault.PubKey,
		InHash:      in,
		Coin:        common.NewCoin(common.DOGEAsset, cosmos.NewUint(10*common.One)),
	}
	ok, err := tos.TryAddTxOutItem(w.ctx, w.mgr, toi, cosmos.ZeroUint())
	c.Assert(err, IsNil)
	c.Assert(ok, Equals, true)

	voter, err := w.keeper.GetObservedTxInVoter(w.ctx, in)
	c.Assert(err, IsNil)
	c.Assert(voter.Actions, HasLen, 1)
	memo := voter.Actions[0].Memo
	c.Assert(len(memo), Equals, common.DOGEChain.MaxMemoLength())
	c.Assert(strings.HasPrefix(memo, "OUT:"+in.String()+"|xxx"), Equals, true)
}

func (s *CovCCTxOutSuite) TestPrepareFailsWithoutGasDetails(c *C) {
	w := getHandlerTestWrapper(c, 1, true, false)
	tos := covCCTxOutStore(w, nil)
	// AVAX has no network fee configured in the test setup
	toi := TxOutItem{
		Chain:       common.AVAXChain,
		ToAddress:   GetRandomETHAddress(),
		VaultPubKey: GetRandomPubKey(),
		InHash:      GetRandomTxHash(),
		Coin:        common.NewCoin(common.AVAXAsset, cosmos.NewUint(common.One)),
	}
	ok, err := tos.TryAddTxOutItem(w.ctx, w.mgr, toi, cosmos.ZeroUint())
	c.Assert(ok, Equals, false)
	c.Assert(err, ErrorMatches, "failed to prepare tx out item: fail to get .*")
	items, err := tos.GetOutboundItems(w.ctx)
	c.Assert(err, IsNil)
	c.Assert(items, HasLen, 0)
}

func (s *CovCCTxOutSuite) TestNativeTxOutSecuredAsset(c *C) {
	w := getHandlerTestWrapper(c, 1, true, false)
	vault := GetRandomVault()
	vault.Chains = append(vault.Chains, common.THORChain.String())
	c.Assert(w.keeper.SetVault(w.ctx, vault), IsNil)
	tos := covCCTxOutStore(w, nil)

	user := GetRandomRUNEAddress()
	userAcc, err := user.AccAddress()
	c.Assert(err, IsNil)
	secured := common.ETHAsset.GetSecuredAsset()
	in := GetRandomTxHash()
	toi := TxOutItem{
		Chain:      common.THORChain,
		ToAddress:  user,
		InHash:     in,
		Coin:       common.NewCoin(secured, cosmos.NewUint(4*common.One)),
		ModuleName: AsgardName,
		Memo:       NewOutboundMemo(in).String(),
	}
	c.Assert(tos.nativeTxOut(w.ctx, w.mgr, toi), IsNil)
	bal := w.keeper.GetBalance(w.ctx, userAcc).AmountOf(secured.Native())
	c.Assert(bal.Uint64(), Equals, uint64(4*common.One))
}

func (s *CovCCTxOutSuite) TestDiscoverOutboundsSkipsUnusableVaults(c *C) {
	w := getHandlerTestWrapper(c, 1, true, false)
	tos := covCCTxOutStore(w, newRecordingEventMgr())

	mkVault := func(amount uint64) Vault {
		v := GetRandomVault()
		v.Coins = common.Coins{common.NewCoin(common.DOGEAsset, cosmos.NewUint(amount))}
		return v
	}
	frozen := mkVault(1_000_000)
	frozen.Frozen = []string{common.DOGEChain.String()}
	empty := GetRandomVault()
	self := mkVault(1_000_000) // destination is this vault's own address
	tooSmallForFee := mkVault(10)
	tooSmallForGas := mkVault(50)
	onlyGas := mkVault(100)
	partial := mkVault(200)

	selfAddr, err := self.GetAddress(common.DOGEChain)
	c.Assert(err, IsNil)
	toi := TxOutItem{
		Chain:     common.DOGEChain,
		ToAddress: selfAddr,
		InHash:    GetRandomTxHash(),
		Coin:      common.NewCoin(common.DOGEAsset, cosmos.NewUint(1000)),
	}
	maxGas := common.NewCoin(common.DOGEAsset, cosmos.NewUint(100))
	outputs, remaining := tos.DiscoverOutbounds(w.ctx, cosmos.NewUint(10), maxGas, toi,
		Vaults{frozen, empty, self, tooSmallForFee, tooSmallForGas, onlyGas, partial})

	c.Assert(remaining.IsZero(), Equals, true)
	c.Assert(outputs, HasLen, 1)
	c.Assert(outputs[0].VaultPubKey.Equals(partial.PubKey), Equals, true)
	// 100 spendable from the partial vault + the unallocated 900 remainder
	c.Assert(outputs[0].Coin.Amount.Uint64(), Equals, uint64(1000))
}
