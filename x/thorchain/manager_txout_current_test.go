package thorchain

import (
	"gopkg.in/check.v1"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	ttypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

type TxOutStoreSuite struct{}

var _ = Suite(&TxOutStoreSuite{})

type recordingEventMgr struct {
	*DummyEventMgr
	events []EmitEventItem
}

func newRecordingEventMgr() *recordingEventMgr {
	return &recordingEventMgr{DummyEventMgr: NewDummyEventMgr()}
}

func (m *recordingEventMgr) EmitEvent(ctx cosmos.Context, evt EmitEventItem) error {
	m.events = append(m.events, evt)
	return nil
}

func (m *recordingEventMgr) securityEvents() []*ttypes.EventSecurity {
	events := make([]*ttypes.EventSecurity, 0)
	for _, evt := range m.events {
		securityEvent, ok := evt.(*ttypes.EventSecurity)
		if ok {
			events = append(events, securityEvent)
		}
	}
	return events
}

func (s TxOutStoreSuite) TestTakeAffiliateFee(c *C) {
	w := getHandlerTestWrapper(c, 1, true, true)
	txOutStore := newTxOutStorage(w.keeper, w.mgr.GetConstants(), w.mgr.EventMgr(), w.mgr.GasMgr())

	// Test Case 1: Standard memo - should return error if parsing fails (e.g. invalid type) or handle correctly
	// Here we test the specific case where we pass the preferred asset swap memo, which usually fails parsing
	// but should be ignored by takeAffiliateFee

	toi := TxOutItem{
		Chain:     common.ETHChain,
		ToAddress: GetRandomETHAddress(),
		InHash:    GetRandomTxHash(),
		Coin:      common.NewCoin(common.ETHAsset, cosmos.NewUint(100*common.One)),
		Memo:      PreferredAssetSwapMemoPrefix + "-ETH.ETH", // "THOR-PREFERRED-ASSET-ETH.ETH"
	}

	// Mock the inbound transaction that this outbound is related to
	// The takeAffiliateFee method looks up the INBOUND transaction to get the memo
	voter := NewObservedTxVoter(toi.InHash, common.ObservedTxs{
		common.ObservedTx{
			Tx: common.Tx{
				ID:    toi.InHash,
				Chain: common.ETHChain,
				Coins: common.Coins{
					common.NewCoin(common.ETHAsset, cosmos.NewUint(100*common.One)),
				},
				Memo: toi.Memo, // The inbound memo is what matters
			},
		},
	})
	w.keeper.SetObservedTxInVoter(w.ctx, voter)

	// Execute takeAffiliateFee
	amount, err := txOutStore.takeAffiliateFee(w.ctx, w.mgr, toi)

	// Verify results
	c.Assert(err, IsNil, Commentf("Should not error on preferred asset swap memo"))
	c.Assert(amount.Equal(toi.Coin.Amount), Equals, true, Commentf("Amount should be unchanged"))
}

func (s TxOutStoreSuite) TestIsAffiliateFeeExempt(c *C) {
	w := getHandlerTestWrapper(c, 1, true, true)
	txOutStore := newTxOutStorage(w.keeper, w.mgr.GetConstants(), w.mgr.EventMgr(), w.mgr.GasMgr())

	testCases := []struct {
		name     string
		memo     string
		expected bool
	}{
		{
			name:     "Refund memo",
			memo:     "REFUND:TxHash",
			expected: true,
		},
		{
			name:     "Migrate memo",
			memo:     "MIGRATE:100",
			expected: true,
		},
		{
			name:     "Preferred Asset Swap memo",
			memo:     "THOR-PREFERRED-ASSET-ETH.ETH",
			expected: false,
		},
		{
			name:     "Preferred Asset Swap memo exact",
			memo:     constants.MemoPrefixPreferredAssetSwap,
			expected: false,
		},
		{
			name:     "Swap memo",
			memo:     "SWAP:ETH.ETH",
			expected: false,
		},
		{
			name:     "Empty memo",
			memo:     "",
			expected: false,
		},
		{
			name:     "Random memo",
			memo:     "HELLO",
			expected: false,
		},
	}

	for _, tc := range testCases {
		result := txOutStore.isAffiliateFeeExemptOutbound(tc.memo)
		c.Assert(result, Equals, tc.expected, Commentf("Failed case: %s", tc.name))
	}

	testCases = []struct {
		name     string
		memo     string
		expected bool
	}{
		{
			name:     "Refund memo",
			memo:     "REFUND:TxHash",
			expected: false,
		},
		{
			name:     "Migrate memo",
			memo:     "MIGRATE:100",
			expected: false,
		},
		{
			name:     "Preferred Asset Swap memo",
			memo:     "THOR-PREFERRED-ASSET-ETH.ETH",
			expected: true,
		},
		{
			name:     "Preferred Asset Swap memo exact",
			memo:     constants.MemoPrefixPreferredAssetSwap,
			expected: true,
		},
		{
			name:     "Swap memo",
			memo:     "SWAP:ETH.ETH",
			expected: false,
		},
		{
			name:     "Empty memo",
			memo:     "",
			expected: true,
		},
		{
			name:     "Random memo",
			memo:     "HELLO",
			expected: false,
		},
	}
	for _, tc := range testCases {
		result := txOutStore.isAffiliateFeeExemptInbound(tc.memo)
		c.Assert(result, Equals, tc.expected, Commentf("Failed case: %s", tc.name))
	}
}

func (s TxOutStoreSuite) TestAddGasFees(c *C) {
	ctx, mgr := setupManagerForTest(c)
	tx := GetRandomObservedTx()

	// Set vault to satisfy VaultExists check.
	vault := NewVault(ctx.BlockHeight(), ActiveVault, AsgardVault, tx.ObservedPubKey, common.Chains{common.ETHChain}.Strings(), []ChainContract{})
	c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)

	err := addGasFees(ctx, mgr, tx)
	c.Assert(err, IsNil)
	c.Assert(mgr.GasMgr().GetGas(), HasLen, 1)
}

func (s TxOutStoreSuite) TestEndBlock(c *C) {
	w := getHandlerTestWrapper(c, 1, true, true)
	txOutStore := newTxOutStorage(w.keeper, w.mgr.GetConstants(), w.mgr.EventMgr(), w.mgr.GasMgr())

	item := TxOutItem{
		Chain:     common.ETHChain,
		ToAddress: GetRandomETHAddress(),
		InHash:    GetRandomTxHash(),
		Coin:      common.NewCoin(common.ETHAsset, cosmos.NewUint(20*common.One)),
	}

	// Set up voter with the item as an action so EndBlock can update gas in both txOut and voter.
	voter := NewObservedTxVoter(item.InHash, common.ObservedTxs{
		common.ObservedTx{
			Tx:          GetRandomTx(),
			Status:      common.Status_incomplete,
			BlockHeight: 1,
			Signers:     []string{w.activeNodeAccount.NodeAddress.String()},
		},
	})
	voter.Actions = []TxOutItem{item}
	w.keeper.SetObservedTxInVoter(w.ctx, voter)

	err := txOutStore.UnSafeAddTxOutItem(w.ctx, w.mgr, item, w.ctx.BlockHeight())
	c.Assert(err, IsNil)

	c.Assert(txOutStore.EndBlock(w.ctx, w.mgr), IsNil)

	items, err := txOutStore.GetOutboundItems(w.ctx)
	c.Assert(err, IsNil)
	c.Assert(items, HasLen, 1)
	c.Check(items[0].GasRate, Equals, int64(562_500), Commentf("%d", items[0].GasRate)) // 562,500 gwei
	c.Assert(items[0].MaxGas, HasLen, 1)
	c.Check(items[0].MaxGas[0].Asset.Equals(common.ETHAsset), Equals, true)
	c.Check(items[0].MaxGas[0].Amount.Uint64(), Equals, uint64(56250), Commentf("%d", items[0].MaxGas[0].Amount.Uint64()))
}

func (s TxOutStoreSuite) TestNativeTxOutWithoutActiveVaults(c *C) {
	w := getHandlerTestWrapper(c, 1, true, false)
	txOutStore := newTxOutStorage(w.keeper, w.mgr.GetConstants(), w.mgr.EventMgr(), w.mgr.GasMgr())

	active, err := w.keeper.GetAsgardVaultsByStatus(w.ctx, ActiveVault)
	c.Assert(err, IsNil)
	c.Assert(active, HasLen, 0)

	toi := TxOutItem{
		Chain:     common.THORChain,
		ToAddress: GetRandomRUNEAddress(),
		InHash:    GetRandomTxHash(),
		Coin:      common.NewCoin(common.RuneAsset(), cosmos.NewUint(common.One)),
		Memo:      "RESERVE",
	}

	err = txOutStore.nativeTxOut(w.ctx, w.mgr, toi)
	c.Assert(err, NotNil)
	c.Check(err.Error(), Equals, "dev error: no pubkey for native txn")
}

func (s TxOutStoreSuite) TestAddOutTxItem(c *C) {
	w := getHandlerTestWrapper(c, 1, true, true)
	vault := GetRandomVault()
	vault.Coins = common.Coins{
		common.NewCoin(common.RuneAsset(), cosmos.NewUint(10000*common.One)),
		common.NewCoin(common.ETHAsset, cosmos.NewUint(10000*common.One)),
		common.NewCoin(common.BCHAsset, cosmos.NewUint(10000*common.One)),
		common.NewCoin(common.DOGEAsset, cosmos.NewUint(10000*common.One)),
	}
	c.Assert(w.keeper.SetVault(w.ctx, vault), IsNil)

	initialOutboundFeeWithheldRune, err := w.keeper.GetOutboundFeeWithheldRune(w.ctx, common.ETHAsset)
	c.Assert(err, IsNil)
	outboundFeeSpentRune, err := w.keeper.GetOutboundFeeSpentRune(w.ctx, common.ETHAsset)
	c.Assert(err, IsNil)
	c.Check(initialOutboundFeeWithheldRune.String(), Equals, w.keeper.GetSurplusForTargetMultiplier(w.ctx, cosmos.NewUint(10_000)).String())
	c.Check(outboundFeeSpentRune.String(), Equals, "0")

	acc1 := GetRandomValidatorNode(NodeActive)
	acc2 := GetRandomValidatorNode(NodeActive)
	acc3 := GetRandomValidatorNode(NodeActive)
	c.Assert(w.keeper.SetNodeAccount(w.ctx, acc1), IsNil)
	c.Assert(w.keeper.SetNodeAccount(w.ctx, acc2), IsNil)
	c.Assert(w.keeper.SetNodeAccount(w.ctx, acc3), IsNil)

	// Create voter
	inTxID := GetRandomTxHash()
	voter := NewObservedTxVoter(inTxID, common.ObservedTxs{
		common.ObservedTx{
			Tx:             GetRandomTx(),
			Status:         common.Status_incomplete,
			BlockHeight:    1,
			Signers:        []string{w.activeNodeAccount.NodeAddress.String(), acc1.NodeAddress.String(), acc2.NodeAddress.String()},
			KeysignMs:      0,
			FinaliseHeight: 1,
		},
	})
	w.keeper.SetObservedTxInVoter(w.ctx, voter)

	// Should get acc2. Acc3 hasn't signed and acc2 is the highest value
	item := TxOutItem{
		Chain:     common.DOGEChain,
		ToAddress: GetRandomDOGEAddress(),
		InHash:    inTxID,
		Coin:      common.NewCoin(common.DOGEAsset, cosmos.NewUint(20*common.One)),
	}
	txOutStore := newTxOutStorage(w.keeper, w.mgr.GetConstants(), w.mgr.EventMgr(), w.mgr.GasMgr())
	expectedOutboundFee, err := txOutStore.gasManager.GetAssetOutboundFee(w.ctx, item.Coin.Asset, false)
	c.Assert(err, IsNil)
	ok, err := txOutStore.TryAddTxOutItem(w.ctx, w.mgr, item, cosmos.ZeroUint())
	c.Assert(err, IsNil)
	c.Assert(ok, Equals, true)
	msgs, err := txOutStore.GetOutboundItems(w.ctx)
	c.Assert(err, IsNil)
	c.Assert(msgs, HasLen, 1)
	c.Assert(msgs[0].VaultPubKey.String(), Equals, vault.PubKey.String())
	c.Assert(msgs[0].Coin.Amount.Equal(item.Coin.Amount.Sub(expectedOutboundFee)), Equals, true, Commentf("%d", msgs[0].Coin.Amount.Uint64()))

	// Gas withheld should be updated
	outboundFeeWithheldRune, err := w.keeper.GetOutboundFeeWithheldRune(w.ctx, common.DOGEAsset)
	c.Assert(err, IsNil)
	outboundFeeSpentRune, err = w.keeper.GetOutboundFeeSpentRune(w.ctx, common.DOGEAsset)
	c.Assert(err, IsNil)

	c.Check(outboundFeeWithheldRune.Sub(initialOutboundFeeWithheldRune).String(), Equals, expectedOutboundFee.String())
	c.Check(outboundFeeSpentRune.String(), Equals, "0")

	// Should get acc1. Acc3 hasn't signed and acc1 now has the highest amount
	// of coin.
	item = TxOutItem{
		Chain:     common.DOGEChain,
		ToAddress: GetRandomDOGEAddress(),
		InHash:    inTxID,
		Coin:      common.NewCoin(common.DOGEAsset, cosmos.NewUint(20*common.One)),
	}
	txOutStore.ClearOutboundItems(w.ctx)
	success, err := txOutStore.TryAddTxOutItem(w.ctx, w.mgr, item, cosmos.ZeroUint())
	c.Assert(success, Equals, true)
	c.Assert(err, IsNil)
	msgs, err = txOutStore.GetOutboundItems(w.ctx)
	c.Assert(err, IsNil)
	c.Assert(msgs, HasLen, 1)
	c.Assert(msgs[0].VaultPubKey.String(), Equals, vault.PubKey.String())

	// Outbound fee withheld RUNE should be updated
	outboundFeeWithheldRune, err = w.keeper.GetOutboundFeeWithheldRune(w.ctx, common.DOGEAsset)
	c.Assert(err, IsNil)
	c.Assert(outboundFeeWithheldRune.Sub(initialOutboundFeeWithheldRune).String(), Equals, expectedOutboundFee.MulUint64(2).String())
	beforeLargeOutboundFeeWithheldRune := outboundFeeWithheldRune

	item = TxOutItem{
		Chain:     common.DOGEChain,
		ToAddress: GetRandomDOGEAddress(),
		InHash:    inTxID,
		Coin:      common.NewCoin(common.DOGEAsset, cosmos.NewUint(1000*common.One)),
	}
	txOutStore.ClearOutboundItems(w.ctx)
	expectedLargeOutboundRuneFee, err := txOutStore.gasManager.GetAssetOutboundFee(w.ctx, item.Coin.Asset, true)
	c.Assert(err, IsNil)
	success, err = txOutStore.TryAddTxOutItem(w.ctx, w.mgr, item, cosmos.ZeroUint())
	c.Assert(err, IsNil)
	c.Assert(success, Equals, true)
	for height := w.ctx.BlockHeight(); height <= w.ctx.BlockHeight()+100; height++ {
		msgs, err = txOutStore.GetOutboundItems(w.ctx.WithBlockHeight(height))
		c.Assert(err, IsNil)
		if len(msgs) > 0 {
			break
		}
	}
	c.Assert(msgs, HasLen, 1)
	c.Check(msgs[0].VaultPubKey.String(), Equals, vault.PubKey.String())

	// Outbound fee withheld RUNE should be updated
	outboundFeeWithheldRune, err = w.keeper.GetOutboundFeeWithheldRune(w.ctx, common.DOGEAsset)
	c.Assert(err, IsNil)
	c.Assert(outboundFeeWithheldRune.Sub(beforeLargeOutboundFeeWithheldRune).String(), Equals, expectedLargeOutboundRuneFee.String())

	networkFee := NewNetworkFee(common.BCHChain, 1, 10)
	c.Assert(w.keeper.SaveNetworkFee(w.ctx, common.BCHChain, networkFee), IsNil)

	item = TxOutItem{
		Chain:     common.BCHChain,
		ToAddress: "1EFJFJm7Y9mTVsCBXA9PKuRuzjgrdBe4rR",
		InHash:    inTxID,
		Coin:      common.NewCoin(common.BCHAsset, cosmos.NewUint(20*common.One)),
		MaxGas: common.Gas{
			common.NewCoin(common.BCHAsset, cosmos.NewUint(10000)),
		},
	}
	txOutStore.ClearOutboundItems(w.ctx)
	result, err := txOutStore.TryAddTxOutItem(w.ctx, w.mgr, item, cosmos.ZeroUint())
	c.Assert(err, IsNil)
	c.Assert(result, Equals, true)
	for height := w.ctx.BlockHeight(); height <= w.ctx.BlockHeight()+100; height++ {
		msgs, err = txOutStore.GetOutboundItems(w.ctx.WithBlockHeight(height))
		c.Assert(err, IsNil)
		if len(msgs) > 0 {
			break
		}
	}
	c.Assert(msgs, HasLen, 1)
	// this should be a mocknet address
	c.Assert(msgs[0].ToAddress.String(), Equals, "qzg5mkh7rkw3y8kw47l3rrnvhmenvctmd5yg6hxe64")

	// outbound originating from a pool should pay fee from asgard to reserve
	FundModule(c, w.ctx, w.keeper, AsgardName, 1000*common.One)
	testAndCheckModuleBalances(
		c, w.ctx, w.keeper,
		func() {
			item = TxOutItem{
				Chain:     common.THORChain,
				ToAddress: GetRandomRUNEAddress(),
				InHash:    inTxID,
				Coin:      common.NewCoin(common.RuneAsset(), cosmos.NewUint(1000*common.One)),
			}
			txOutStore.ClearOutboundItems(w.ctx)
			success, err = txOutStore.TryAddTxOutItem(w.ctx, w.mgr, item, cosmos.ZeroUint())
			c.Assert(err, IsNil)
			c.Assert(success, Equals, true)
			msgs, err = txOutStore.GetOutboundItems(w.ctx)
			c.Assert(err, IsNil)
			c.Assert(msgs, HasLen, 0)
		},
		ModuleBalances{
			Asgard:  -1000_00000000,
			Reserve: 0,
		},
	)

	// outbound originating from bond should pay fee from bond to reserve
	FundModule(c, w.ctx, w.keeper, BondName, 1000*common.One)
	testAndCheckModuleBalances(
		c, w.ctx, w.keeper,
		func() {
			item = TxOutItem{
				ModuleName: BondName,
				Chain:      common.THORChain,
				ToAddress:  GetRandomRUNEAddress(),
				InHash:     inTxID,
				Coin:       common.NewCoin(common.RuneAsset(), cosmos.NewUint(1000*common.One)),
			}
			txOutStore.ClearOutboundItems(w.ctx)
			success, err = txOutStore.TryAddTxOutItem(w.ctx, w.mgr, item, cosmos.ZeroUint())
			c.Assert(err, IsNil)
			c.Assert(success, Equals, true)
			msgs, err = txOutStore.GetOutboundItems(w.ctx)
			c.Assert(err, IsNil)
			c.Assert(msgs, HasLen, 0)
		},
		ModuleBalances{
			Bond:    -1000_00000000,
			Reserve: 0,
		},
	)

	// ensure that min out is respected
	success, err = txOutStore.TryAddTxOutItem(w.ctx, w.mgr, item, cosmos.NewUint(9999999999*common.One))
	c.Check(success, Equals, false)
	c.Check(err, NotNil)
}

func (s TxOutStoreSuite) TestAddOutTxItem_OutboundHeightDoesNotGetOverride(c *C) {
	SetupConfigForTest()
	w := getHandlerTestWrapper(c, 1, true, true)
	vault := GetRandomVault()
	vault.Coins = common.Coins{
		common.NewCoin(common.RuneAsset(), cosmos.NewUint(10000*common.One)),
		common.NewCoin(common.DOGEAsset, cosmos.NewUint(10000*common.One)),
	}
	c.Assert(w.keeper.SetVault(w.ctx, vault), IsNil)

	acc1 := GetRandomValidatorNode(NodeActive)
	acc2 := GetRandomValidatorNode(NodeActive)
	acc3 := GetRandomValidatorNode(NodeActive)
	c.Assert(w.keeper.SetNodeAccount(w.ctx, acc1), IsNil)
	c.Assert(w.keeper.SetNodeAccount(w.ctx, acc2), IsNil)
	c.Assert(w.keeper.SetNodeAccount(w.ctx, acc3), IsNil)

	w.keeper.SetMimir(w.ctx, constants.MinTxOutVolumeThreshold.String(), 100000000000)
	w.keeper.SetMimir(w.ctx, constants.TxOutDelayRate.String(), 2500000000)
	w.keeper.SetMimir(w.ctx, constants.MaxTxOutOffset.String(), 720)
	// Create voter
	inTxID := GetRandomTxHash()
	voter := NewObservedTxVoter(inTxID, common.ObservedTxs{
		common.ObservedTx{
			Tx:             GetRandomTx(),
			Status:         common.Status_incomplete,
			BlockHeight:    1,
			Signers:        []string{w.activeNodeAccount.NodeAddress.String(), acc1.NodeAddress.String(), acc2.NodeAddress.String()},
			KeysignMs:      0,
			FinaliseHeight: 1,
		},
	})
	w.keeper.SetObservedTxInVoter(w.ctx, voter)

	// this should be sent via asgard
	item := TxOutItem{
		Chain:     common.DOGEChain,
		ToAddress: GetRandomDOGEAddress(),
		InHash:    inTxID,
		Coin:      common.NewCoin(common.DOGEAsset, cosmos.NewUint(80*common.One)),
	}
	txOutStore := newTxOutStorage(w.keeper, w.mgr.GetConstants(), w.mgr.EventMgr(), w.mgr.GasMgr())
	expectedOutboundFee, err := txOutStore.gasManager.GetAssetOutboundFee(w.ctx, item.Coin.Asset, false)
	c.Assert(err, IsNil)
	ok, err := txOutStore.TryAddTxOutItem(w.ctx, w.mgr, item, cosmos.ZeroUint())
	c.Assert(err, IsNil)
	c.Assert(ok, Equals, true)

	msgs, err := txOutStore.GetOutboundItems(w.ctx)
	c.Assert(err, IsNil)
	c.Assert(msgs, HasLen, 0)
	//  the outbound has been delayed
	newCtx := w.ctx.WithBlockHeight(4)
	msgs, err = txOutStore.GetOutboundItems(newCtx)
	c.Assert(err, IsNil)
	c.Assert(msgs, HasLen, 1)
	c.Assert(msgs[0].VaultPubKey.String(), Equals, vault.PubKey.String())
	c.Assert(msgs[0].Coin.Amount.Equal(item.Coin.Amount.Sub(expectedOutboundFee)), Equals, true, Commentf("%d", msgs[0].Coin.Amount.Uint64()))

	// make sure outbound_height has been set correctly
	afterVoter, err := w.keeper.GetObservedTxInVoter(w.ctx, inTxID)
	c.Assert(err, IsNil)
	c.Assert(afterVoter.OutboundHeight, Equals, int64(4))

	item.Chain = common.THORChain
	item.Coin = common.NewCoin(common.RuneNative, cosmos.NewUint(100*common.One))
	item.ToAddress = GetRandomTHORAddress()
	ok, err = txOutStore.TryAddTxOutItem(w.ctx, w.mgr, item, cosmos.ZeroUint())
	c.Assert(err, IsNil)
	c.Assert(ok, Equals, true)

	// make sure outbound_height has not been overwritten
	afterVoter1, err := w.keeper.GetObservedTxInVoter(w.ctx, inTxID)
	c.Assert(err, IsNil)
	c.Assert(afterVoter1.OutboundHeight, Equals, int64(4))
}

func (s TxOutStoreSuite) TestAddOutTxItemNotEnoughForFee(c *C) {
	w := getHandlerTestWrapper(c, 1, true, true)
	vault := GetRandomVault()
	vault.Coins = common.Coins{
		common.NewCoin(common.RuneAsset(), cosmos.NewUint(10000*common.One)),
		common.NewCoin(common.DOGEAsset, cosmos.NewUint(10000*common.One)),
	}
	c.Assert(w.keeper.SetVault(w.ctx, vault), IsNil)

	acc1 := GetRandomValidatorNode(NodeActive)
	acc2 := GetRandomValidatorNode(NodeActive)
	acc3 := GetRandomValidatorNode(NodeActive)
	c.Assert(w.keeper.SetNodeAccount(w.ctx, acc1), IsNil)
	c.Assert(w.keeper.SetNodeAccount(w.ctx, acc2), IsNil)
	c.Assert(w.keeper.SetNodeAccount(w.ctx, acc3), IsNil)

	// Create voter
	inTxID := GetRandomTxHash()
	voter := NewObservedTxVoter(inTxID, common.ObservedTxs{
		common.ObservedTx{
			Tx:             GetRandomTx(),
			Status:         common.Status_incomplete,
			BlockHeight:    1,
			Signers:        []string{w.activeNodeAccount.NodeAddress.String(), acc1.NodeAddress.String(), acc2.NodeAddress.String()},
			KeysignMs:      0,
			FinaliseHeight: 1,
		},
	})
	w.keeper.SetObservedTxInVoter(w.ctx, voter)

	item := TxOutItem{
		Chain:     common.DOGEChain,
		ToAddress: GetRandomDOGEAddress(),
		InHash:    inTxID,
		Coin:      common.NewCoin(common.DOGEAsset, cosmos.NewUint(300000)),
	}
	txOutStore := newTxOutStorage(w.keeper, w.mgr.GetConstants(), w.mgr.EventMgr(), w.mgr.GasMgr())
	ok, err := txOutStore.TryAddTxOutItem(w.ctx, w.mgr, item, cosmos.ZeroUint())
	c.Assert(err, NotNil)
	c.Assert(err, Equals, ErrNotEnoughToPayFee)
	c.Assert(ok, Equals, false)
	msgs, err := txOutStore.GetOutboundItems(w.ctx)
	c.Assert(err, IsNil)
	c.Assert(msgs, HasLen, 0)
}

func (s TxOutStoreSuite) TestAddOutTxItemWithoutBFT(c *C) {
	w := getHandlerTestWrapper(c, 1, true, true)
	vault := GetRandomVault()
	vault.Coins = common.Coins{
		common.NewCoin(common.ETHAsset, cosmos.NewUint(100*common.One)),
	}
	c.Assert(w.keeper.SetVault(w.ctx, vault), IsNil)

	inTxID := GetRandomTxHash()
	item := TxOutItem{
		Chain:     common.ETHChain,
		ToAddress: GetRandomETHAddress(),
		InHash:    inTxID,
		Coin:      common.NewCoin(common.ETHAsset, cosmos.NewUint(20*common.One)),
	}
	txOutStore := newTxOutStorage(w.keeper, w.mgr.GetConstants(), w.mgr.EventMgr(), w.mgr.GasMgr())
	success, err := txOutStore.TryAddTxOutItem(w.ctx, w.mgr, item, cosmos.ZeroUint())
	c.Assert(err, IsNil)
	c.Assert(success, Equals, true)
	msgs, err := txOutStore.GetOutboundItems(w.ctx)
	c.Assert(err, IsNil)
	c.Assert(msgs, HasLen, 1)
	// TODO: Eri: seems like before gas was subtracted, not sure why it would be
	c.Assert(msgs[0].Coin.Amount.Equal(cosmos.NewUint(20*common.One)), Equals, true, Commentf("%d", msgs[0].Coin.Amount.Uint64()))
}

func (s TxOutStoreSuite) TestCalcTxOutHeight(c *C) {
	ctx, keeper := setupKeeperForTest(c)

	pool := NewPool()
	pool.Asset = common.ETHAsset
	pool.BalanceRune = cosmos.NewUint(90527581399649)
	pool.BalanceAsset = cosmos.NewUint(1402011488988)
	c.Assert(keeper.SetPool(ctx, pool), IsNil)

	keeper.SetMimir(ctx, "MinTxOutVolumeThreshold", 2500000000)
	keeper.SetMimir(ctx, "TxOutDelayRate", 2500000000)
	keeper.SetMimir(ctx, "MaxTxOutOffset", 720)
	keeper.SetMimir(ctx, "TxOutDelayMax", 17280)
	// With the above values, a RUNE value of 18,000 would be delayed for the full MaxTxOutOffset.

	version := GetCurrentVersion()
	txout, err := GetTxOutStore(version, keeper, NewDummyEventMgr(), NewDummyGasManager())
	c.Assert(err, IsNil)

	toi := TxOutItem{
		Coin: common.NewCoin(common.ETHAsset, cosmos.NewUint(2*common.One)),
		Memo: "OUT:nomnomnom",
	}
	value := pool.AssetValueInRune(toi.Coin.Amount)
	c.Check(value.Uint64(), Equals, uint64(129_13957141), Commentf("%d", value.Uint64()))

	c.Check(ctx.BlockHeight(), Equals, int64(18), Commentf("%d", ctx.BlockHeight()))
	// Confirming that the current height is 18.

	targetBlock, _, err := txout.CalcTxOutHeight(ctx, keeper.GetVersion(), toi)
	c.Assert(err, IsNil)
	c.Check(targetBlock, Equals, int64(24))
	c.Assert(keeper.AppendTxOut(ctx, targetBlock, toi), IsNil)
	// (sumValue / minTxOutVolumeThreshold) * common.One = (129_13957141 / 25_00000000) * 1_00000000 = 5_00000000,
	// which reduces the 25_00000000 TxOutDelayRate to 20_00000000.
	// value / TxOutDelayRate is then 129 / 20 ~= 6, added to the starting height of 18 to get 24.

	targetBlock, _, err = txout.CalcTxOutHeight(ctx, keeper.GetVersion(), toi)
	c.Assert(err, IsNil)
	c.Check(targetBlock, Equals, int64(26))
	c.Assert(keeper.AppendTxOut(ctx, targetBlock, toi), IsNil)
	// (sumValue / minTxOutVolumeThreshold) * common.One = (258_27914282 / 25_00000000) * 1_00000000 = 10_00000000,
	// which reduces the 25_00000000 TxOutDelayRate to 15_00000000.
	// value / TxOutDelayRate is then 129 / 15 ~= 8, added to the starting height of 18 to get 26.

	normalGasTOI := toi
	normalGasTOI.MaxGas = common.Gas{toi.Coin}
	normalGasTOI.MaxGas[0].Amount = cosmos.NewUint(7500)
	normalGasValue := pool.AssetValueInRune(normalGasTOI.MaxGas[0].Amount)
	c.Check(normalGasValue.String(), Equals, "484273", Commentf("%s", normalGasValue.String()))
	targetBlock, _, err = txout.CalcTxOutHeight(ctx, keeper.GetVersion(), normalGasTOI)
	c.Assert(err, IsNil)
	c.Check(targetBlock, Equals, int64(31))

	millionGasTOI := normalGasTOI
	millionGasTOI.MaxGas = common.Gas{normalGasTOI.MaxGas[0]}
	millionGasTOI.MaxGas[0].Amount = millionGasTOI.MaxGas[0].Amount.MulUint64(1e6)
	millionGasValue := pool.AssetValueInRune(millionGasTOI.MaxGas[0].Amount)
	c.Check(millionGasValue.String(), Equals, "484273392786", Commentf("%s", millionGasValue.String()))
	targetBlock, _, err = txout.CalcTxOutHeight(ctx, keeper.GetVersion(), millionGasTOI)
	c.Assert(err, IsNil)
	c.Check(targetBlock, Equals, int64(738)) // This would have the maximum delay in contrast to with normal gas, due to its higher value.

	thousandSizeTOI := toi
	thousandSizeTOI.Coin.Amount = toi.Coin.Amount.MulUint64(1000)
	thousandSizeTOIValue := pool.AssetValueInRune(thousandSizeTOI.Coin.Amount)
	c.Check(thousandSizeTOIValue.Uint64(), Equals, uint64(129_139_57140964), Commentf("%d", thousandSizeTOIValue.Uint64()))

	targetBlock, _, err = txout.CalcTxOutHeight(ctx, keeper.GetVersion(), thousandSizeTOI)
	c.Assert(err, IsNil)
	c.Check(targetBlock, Equals, int64(738))
	c.Assert(keeper.AppendTxOut(ctx, targetBlock, thousandSizeTOI), IsNil)
	// (sumValue / minTxOutVolumeThreshold) * common.One = (129_397_85055246 / 25_00000000) * 1_00000000 = 5_175_00000000,
	// which reduces the 25_00000000 TxOutDelayRate to 1.
	// value / TxOutDelayRate is then 129_139_57140964 / 1, which is capped at MaxTxOutOffset (720).
	// 18 + 720 = 738

	// Now check the effect on TxOutDelayRate from the already-scheduled value.
	targetBlock, _, err = txout.CalcTxOutHeight(ctx, keeper.GetVersion(), toi)
	c.Assert(err, IsNil)
	c.Check(targetBlock, Equals, int64(739))
	// As above, sumValue reduces TxOutDelayRate to 1.
	// value / TxOutDelayRate is then 129_13957141 / 1, which is capped at MaxTxOutOffset (720).
	// 18 + 720 = 738, but since that block isn't empty (and the value sum would be greater than MinTxOutVolumeThreshold)
	// the outbound is scheduled for one block later, 739.
}

func (s TxOutStoreSuite) TestAddOutTxItem_MultipleOutboundWillNotBeScheduledAtTheSameBlockHeight(c *C) {
	SetupConfigForTest()
	w := getHandlerTestWrapper(c, 1, true, true)
	vault := GetRandomVault()
	vault.Coins = common.Coins{
		common.NewCoin(common.RuneAsset(), cosmos.NewUint(10000*common.One)),
		common.NewCoin(common.DOGEAsset, cosmos.NewUint(10000*common.One)),
	}
	c.Assert(w.keeper.SetVault(w.ctx, vault), IsNil)

	acc1 := GetRandomValidatorNode(NodeActive)
	acc2 := GetRandomValidatorNode(NodeActive)
	acc3 := GetRandomValidatorNode(NodeActive)
	c.Assert(w.keeper.SetNodeAccount(w.ctx, acc1), IsNil)
	c.Assert(w.keeper.SetNodeAccount(w.ctx, acc2), IsNil)
	c.Assert(w.keeper.SetNodeAccount(w.ctx, acc3), IsNil)

	w.keeper.SetMimir(w.ctx, constants.MinTxOutVolumeThreshold.String(), 100000000000)
	w.keeper.SetMimir(w.ctx, constants.TxOutDelayRate.String(), 2500000000)
	w.keeper.SetMimir(w.ctx, constants.MaxTxOutOffset.String(), 720)
	// Create voter
	inTxID := GetRandomTxHash()
	voter := NewObservedTxVoter(inTxID, common.ObservedTxs{
		common.ObservedTx{
			Tx:             GetRandomTx(),
			Status:         common.Status_incomplete,
			BlockHeight:    1,
			Signers:        []string{w.activeNodeAccount.NodeAddress.String(), acc1.NodeAddress.String(), acc2.NodeAddress.String()},
			KeysignMs:      0,
			FinaliseHeight: 1,
		},
	})
	w.keeper.SetObservedTxInVoter(w.ctx, voter)

	// this should be sent via asgard
	item := TxOutItem{
		Chain:     common.DOGEChain,
		ToAddress: GetRandomDOGEAddress(),
		InHash:    inTxID,
		Coin:      common.NewCoin(common.DOGEAsset, cosmos.NewUint(80*common.One)),
	}
	txOutStore := newTxOutStorage(w.keeper, w.mgr.GetConstants(), w.mgr.EventMgr(), w.mgr.GasMgr())
	ok, err := txOutStore.TryAddTxOutItem(w.ctx, w.mgr, item, cosmos.ZeroUint())
	c.Assert(err, IsNil)
	c.Assert(ok, Equals, true)

	msgs, err := txOutStore.GetOutboundItems(w.ctx)
	c.Assert(err, IsNil)
	c.Assert(msgs, HasLen, 0)
	//  the outbound has been delayed

	item1 := TxOutItem{
		Chain:     common.DOGEChain,
		ToAddress: GetRandomDOGEAddress(),
		InHash:    inTxID,
		Coin:      common.NewCoin(common.DOGEAsset, cosmos.NewUint(10*common.One)),
	}

	expectedImmediateFee, err := txOutStore.gasManager.GetAssetOutboundFee(w.ctx, item1.Coin.Asset, false)
	c.Assert(err, IsNil)
	ok, err = txOutStore.TryAddTxOutItem(w.ctx, w.mgr, item1, cosmos.ZeroUint())
	c.Assert(err, IsNil)
	c.Assert(ok, Equals, true)

	msgs, err = txOutStore.GetOutboundItems(w.ctx)
	c.Assert(err, IsNil)
	c.Assert(msgs, HasLen, 1)
	//  the smaller outbound hasn't been delayed
	c.Assert(msgs[0].VaultPubKey.String(), Equals, vault.PubKey.String())
	c.Assert(msgs[0].Coin.Amount.Equal(item1.Coin.Amount.Sub(expectedImmediateFee)), Equals, true, Commentf("%d", msgs[0].Coin.Amount.Uint64()))

	newCtx := w.ctx.WithBlockHeight(4)
	msgs, err = txOutStore.GetOutboundItems(newCtx)
	c.Assert(err, IsNil)
	c.Assert(msgs, HasLen, 1) // the delayed outbound's height has been reached
	c.Assert(msgs[0].VaultPubKey.String(), Equals, vault.PubKey.String())
	c.Assert(msgs[0].Coin.Amount.Equal(item.Coin.Amount.Sub(expectedImmediateFee)), Equals, true, Commentf("%d", msgs[0].Coin.Amount.Uint64()))

	// make sure outbound_height has been set correctly (to the furthest-future outbound height)
	afterVoter, err := w.keeper.GetObservedTxInVoter(w.ctx, inTxID)
	c.Assert(err, IsNil)
	c.Assert(afterVoter.OutboundHeight, Equals, int64(4))

	item.Chain = common.THORChain
	item.Coin = common.NewCoin(common.RuneNative, cosmos.NewUint(100*common.One))
	item.ToAddress = GetRandomTHORAddress()
	ok, err = txOutStore.TryAddTxOutItem(w.ctx, w.mgr, item, cosmos.ZeroUint())
	c.Assert(err, IsNil)
	c.Assert(ok, Equals, true)

	// make sure outbound_height has not been lowered
	afterVoter1, err := w.keeper.GetObservedTxInVoter(w.ctx, inTxID)
	c.Assert(err, IsNil)
	c.Assert(afterVoter1.OutboundHeight, Equals, int64(4))
}

func (s TxOutStoreSuite) TestAddOutTxItemInteractionWithPool(c *C) {
	w := getHandlerTestWrapper(c, 1, true, true)
	pool, err := w.keeper.GetPool(w.ctx, common.DOGEAsset)
	c.Assert(err, IsNil)
	// Set unequal values for the pool balances for this test.
	pool.BalanceAsset = cosmos.NewUint(50 * common.One)
	pool.BalanceRune = cosmos.NewUint(100 * common.One)
	pool.Asset = common.DOGEAsset
	err = w.keeper.SetPool(w.ctx, pool)
	c.Assert(err, IsNil)

	vault := GetRandomVault()
	vault.Coins = common.Coins{
		common.NewCoin(common.DOGEAsset, cosmos.NewUint(100*common.One)),
	}
	c.Assert(w.keeper.SetVault(w.ctx, vault), IsNil)

	inTxID := GetRandomTxHash()
	item := TxOutItem{
		Chain:     common.DOGEChain,
		ToAddress: GetRandomDOGEAddress(),
		InHash:    inTxID,
		Coin:      common.NewCoin(common.DOGEAsset, cosmos.NewUint(20*common.One)),
	}
	txOutStore := newTxOutStorage(w.keeper, w.mgr.GetConstants(), w.mgr.EventMgr(), w.mgr.GasMgr())
	expectedOutboundFee, err := txOutStore.gasManager.GetAssetOutboundFee(w.ctx, item.Coin.Asset, false)
	c.Assert(err, IsNil)
	success, err := txOutStore.TryAddTxOutItem(w.ctx, w.mgr, item, cosmos.ZeroUint())
	c.Assert(err, IsNil)
	c.Assert(success, Equals, true)
	var msgs []TxOutItem
	for height := w.ctx.BlockHeight(); height <= w.ctx.BlockHeight()+5; height++ {
		msgs, err = txOutStore.GetOutboundItems(w.ctx.WithBlockHeight(height))
		c.Assert(err, IsNil)
		if len(msgs) > 0 {
			break
		}
	}
	c.Assert(msgs, HasLen, 1)
	c.Assert(msgs[0].Coin.Amount.Equal(item.Coin.Amount.Sub(expectedOutboundFee)), Equals, true, Commentf("%d", msgs[0].Coin.Amount.Uint64()))
	pool, err = w.keeper.GetPool(w.ctx, common.DOGEAsset)
	c.Assert(err, IsNil)
	expectedPoolAsset := cosmos.NewUint(50 * common.One).Add(expectedOutboundFee)
	expectedPoolRune := common.SafeSub(
		cosmos.NewUint(100*common.One),
		common.GetSafeShare(expectedOutboundFee, expectedPoolAsset, cosmos.NewUint(100*common.One)),
	)
	c.Assert(pool.BalanceAsset.Equal(expectedPoolAsset), Equals, true, Commentf("%d", pool.BalanceAsset.Uint64()))
	c.Assert(pool.BalanceRune.Equal(expectedPoolRune), Equals, true, Commentf("%d", pool.BalanceRune.Uint64()))
}

func (s TxOutStoreSuite) TestAddOutTxItemSendingFromRetiredVault(c *C) {
	SetupConfigForTest()
	w := getHandlerTestWrapper(c, 1, true, true)
	activeVault1 := GetRandomVault()
	activeVault1.Type = AsgardVault
	activeVault1.Status = ActiveVault
	activeVault1.Coins = common.Coins{
		common.NewCoin(common.RuneAsset(), cosmos.NewUint(10000*common.One)),
		common.NewCoin(common.ETHAsset, cosmos.NewUint(100*common.One)),
	}
	c.Assert(w.keeper.SetVault(w.ctx, activeVault1), IsNil)

	activeVault2 := GetRandomVault()
	activeVault2.Type = AsgardVault
	activeVault2.Status = ActiveVault
	activeVault2.Coins = common.Coins{
		common.NewCoin(common.RuneAsset(), cosmos.NewUint(10000*common.One)),
		common.NewCoin(common.ETHAsset, cosmos.NewUint(100*common.One)),
	}
	c.Assert(w.keeper.SetVault(w.ctx, activeVault2), IsNil)

	retiringVault1 := GetRandomVault()
	retiringVault1.Type = AsgardVault
	retiringVault1.Status = RetiringVault
	retiringVault1.Coins = common.Coins{
		common.NewCoin(common.RuneAsset(), cosmos.NewUint(10000*common.One)),
		common.NewCoin(common.ETHAsset, cosmos.NewUint(1000*common.One)),
	}
	c.Assert(w.keeper.SetVault(w.ctx, retiringVault1), IsNil)
	acc1 := GetRandomValidatorNode(NodeActive)
	acc2 := GetRandomValidatorNode(NodeActive)
	acc3 := GetRandomValidatorNode(NodeActive)
	c.Assert(w.keeper.SetNodeAccount(w.ctx, acc1), IsNil)
	c.Assert(w.keeper.SetNodeAccount(w.ctx, acc2), IsNil)
	c.Assert(w.keeper.SetNodeAccount(w.ctx, acc3), IsNil)

	w.keeper.SetMimir(w.ctx, constants.MinTxOutVolumeThreshold.String(), 10000000000000)
	w.keeper.SetMimir(w.ctx, constants.TxOutDelayRate.String(), 250000000000)
	w.keeper.SetMimir(w.ctx, constants.MaxTxOutOffset.String(), 720)
	// Create voter
	inTxID := GetRandomTxHash()
	voter := NewObservedTxVoter(inTxID, common.ObservedTxs{
		common.ObservedTx{
			Tx:             GetRandomTx(),
			Status:         common.Status_incomplete,
			BlockHeight:    1,
			Signers:        []string{w.activeNodeAccount.NodeAddress.String(), acc1.NodeAddress.String(), acc2.NodeAddress.String()},
			KeysignMs:      0,
			FinaliseHeight: 1,
		},
	})
	w.keeper.SetObservedTxInVoter(w.ctx, voter)

	// this should be sent via asgard
	item := TxOutItem{
		Chain:     common.ETHChain,
		ToAddress: GetRandomETHAddress(),
		InHash:    inTxID,
		Coin:      common.NewCoin(common.ETHAsset, cosmos.NewUint(120*common.One)),
	}
	txOutStore := newTxOutStorage(w.keeper, w.mgr.GetConstants(), w.mgr.EventMgr(), w.mgr.GasMgr())
	ok, err := txOutStore.TryAddTxOutItem(w.ctx, w.mgr, item, cosmos.ZeroUint())
	c.Assert(err, IsNil)
	c.Assert(ok, Equals, true)

	msgs, err := txOutStore.GetOutboundItems(w.ctx)
	c.Assert(err, IsNil)
	c.Assert(msgs, HasLen, 1)
}

func (s TxOutStoreSuite) TestAddOutTxItem_SecurityVersusOutboundNumber(c *C) {
	// The historical context of this example:
	// TxIn hash:  179BF41ED245E74F2B0A4B9B970ED1F5D11335B192641AE7268F7AA3C1ADB724
	// finalised_height:  7243175
	// Network version:  1.95.0 (see _Example2 for a less extreme, more recent example)

	// For a less extreme, more recent example:
	// 268D0DF45CC6E99F56C3DF2EEF2737CD40B0127C06D2B11E5D256E7558387D5C
	// finalised_height:  7838089
	// Network version:  1.97

	// Within this example vault bonds are treated as zero, using only assets to represent security.

	SetupConfigForTest()
	w := getHandlerTestWrapper(c, 1, true, true)

	assetEthTwt, err := common.NewAsset("ETH.TWT-BC2")
	c.Assert(err, IsNil)

	// Prepare the relevant Asgard vault PubKeys.
	z2lfPubKey := GetRandomPubKey()
	qe5vPubKey := GetRandomPubKey()
	yxy5PubKey := GetRandomPubKey()

	// This vault represents vault of pubkey .z2lf .
	activeVault1 := GetRandomVault()
	activeVault1.PubKey = z2lfPubKey
	activeVault1.Type = AsgardVault
	activeVault1.Status = ActiveVault
	activeVault1.Coins = common.Coins{
		common.NewCoin(common.ETHAsset, cosmos.NewUint(459265206245)),
		common.NewCoin(assetEthTwt, cosmos.NewUint(102469368)),
		// For .z2lf and .qe5v, record BTC amount to represent them being less secure than .yxy5 .
		common.NewCoin(common.BTCAsset, cosmos.NewUint(19169688813)),
		// For .z2lf only, record ETH amount to represent it being less secure than .qe5v .
		common.NewCoin(common.ATOMAsset, cosmos.NewUint(184220933893)),
	}
	c.Assert(w.keeper.SetVault(w.ctx, activeVault1), IsNil)

	// This vault represents vault of pubkey .qe5v .
	activeVault2 := GetRandomVault()
	activeVault2.PubKey = qe5vPubKey
	activeVault2.Type = AsgardVault
	activeVault2.Status = ActiveVault
	activeVault2.Coins = common.Coins{
		common.NewCoin(common.ETHAsset, cosmos.NewUint(547549226806)),
		// Note that Asgard .z2lf and .qe5v have had their ETH.TWT balance pushed down to the same number.
		common.NewCoin(assetEthTwt, cosmos.NewUint(102469368)),
		// For .z2lf and .qe5v, record BTC amount to represent them being less secure than .yxy5 .
		common.NewCoin(common.BTCAsset, cosmos.NewUint(26440155891)),
		// Leaving out ETH amount to represent .qe5v having higher security than .z2lf .
	}
	c.Assert(w.keeper.SetVault(w.ctx, activeVault2), IsNil)

	// This vault represents vault of pubkey .yxy5 .
	activeVault3 := GetRandomVault()
	activeVault3.PubKey = yxy5PubKey
	activeVault3.Type = AsgardVault
	activeVault3.Status = ActiveVault
	activeVault3.Coins = common.Coins{
		common.NewCoin(common.ETHAsset, cosmos.NewUint(510688596460)),
		common.NewCoin(assetEthTwt, cosmos.NewUint(15859492234966)),
		// Leaving out BTC and ETH amount to represent .yxy5 having the highest security .
	}
	c.Assert(w.keeper.SetVault(w.ctx, activeVault3), IsNil)

	// Setting pools to be able to represent the Asset values.
	pool, err := w.keeper.GetPool(w.ctx, common.ETHAsset)
	c.Assert(err, IsNil)
	pool.BalanceAsset = cosmos.NewUint(1653258402395)
	pool.BalanceRune = cosmos.NewUint(248680012786574)
	pool.Asset = common.ETHAsset
	err = w.keeper.SetPool(w.ctx, pool)
	c.Assert(err, IsNil)
	///
	pool, err = w.keeper.GetPool(w.ctx, assetEthTwt)
	c.Assert(err, IsNil)
	pool.BalanceAsset = cosmos.NewUint(89359597473914)
	pool.BalanceRune = cosmos.NewUint(46962864904253)
	err = w.keeper.SetPool(w.ctx, pool)
	c.Assert(err, NotNil)
	pool.Asset = assetEthTwt
	err = w.keeper.SetPool(w.ctx, pool)
	c.Assert(err, IsNil)
	///
	pool, err = w.keeper.GetPool(w.ctx, common.BTCAsset)
	c.Assert(err, IsNil)
	pool.BalanceAsset = cosmos.NewUint(80362018825)
	pool.BalanceRune = cosmos.NewUint(837898672769246)
	err = w.keeper.SetPool(w.ctx, pool)
	c.Assert(err, NotNil)
	pool.Asset = common.BTCAsset
	err = w.keeper.SetPool(w.ctx, pool)
	c.Assert(err, IsNil)
	///
	pool, err = w.keeper.GetPool(w.ctx, common.ATOMAsset)
	c.Assert(err, IsNil)
	pool.BalanceAsset = cosmos.NewUint(694112527552)
	pool.BalanceRune = cosmos.NewUint(612691971161372)
	err = w.keeper.SetPool(w.ctx, pool)
	c.Assert(err, NotNil)
	pool.Asset = common.ATOMAsset
	err = w.keeper.SetPool(w.ctx, pool)
	c.Assert(err, IsNil)

	var vaultsSecurityCheck Vaults
	vaultsSecurityCheck = append(vaultsSecurityCheck, activeVault1)
	vaultsSecurityCheck = append(vaultsSecurityCheck, activeVault2)
	vaultsSecurityCheck = append(vaultsSecurityCheck, activeVault3)
	vaultsSecurityCheck = w.keeper.SortBySecurity(w.ctx, vaultsSecurityCheck, 300)
	// Confirm that the vaults from least to most secure are .z2lf, .qe5v, .yxy5 .
	c.Assert(vaultsSecurityCheck[0].PubKey, Equals, z2lfPubKey)
	c.Assert(vaultsSecurityCheck[1].PubKey, Equals, qe5vPubKey)
	c.Assert(vaultsSecurityCheck[2].PubKey, Equals, yxy5PubKey)

	acc1 := GetRandomValidatorNode(NodeActive)
	acc2 := GetRandomValidatorNode(NodeActive)
	acc3 := GetRandomValidatorNode(NodeActive)
	c.Assert(w.keeper.SetNodeAccount(w.ctx, acc1), IsNil)
	c.Assert(w.keeper.SetNodeAccount(w.ctx, acc2), IsNil)
	c.Assert(w.keeper.SetNodeAccount(w.ctx, acc3), IsNil)

	w.keeper.SetMimir(w.ctx, constants.MinTxOutVolumeThreshold.String(), 10000000000000)
	w.keeper.SetMimir(w.ctx, constants.TxOutDelayRate.String(), 250000000000)
	maxTxOutOffset := int64(720)
	w.keeper.SetMimir(w.ctx, constants.MaxTxOutOffset.String(), maxTxOutOffset)
	// Create voter
	inTxID := GetRandomTxHash()
	voter := NewObservedTxVoter(inTxID, common.ObservedTxs{
		common.ObservedTx{
			Tx:             GetRandomTx(),
			Status:         common.Status_incomplete,
			BlockHeight:    1,
			Signers:        []string{w.activeNodeAccount.NodeAddress.String(), acc1.NodeAddress.String(), acc2.NodeAddress.String()},
			KeysignMs:      0,
			FinaliseHeight: 1,
		},
	})
	w.keeper.SetObservedTxInVoter(w.ctx, voter)

	// this should be sent via asgard
	item := TxOutItem{
		Chain:     common.ETHChain,
		ToAddress: GetRandomETHAddress(),
		InHash:    inTxID,
		Coin:      common.NewCoin(assetEthTwt, cosmos.NewUint(39076+39076+94830689368)),
		// This Coin amount is an estimate, given slight changes to pool RUNE amount in a block.
	}

	txOutStore := newTxOutStorage(w.keeper, w.mgr.GetConstants(), w.mgr.EventMgr(), w.mgr.GasMgr())
	ok, err := txOutStore.TryAddTxOutItem(w.ctx, w.mgr, item, cosmos.ZeroUint())
	c.Assert(err, IsNil)
	c.Assert(ok, Equals, true)

	msgs, err := txOutStore.GetOutboundItems(w.ctx)
	c.Assert(err, IsNil)

	// Only one outbound is created from the TxOutItem.
	c.Assert(msgs, HasLen, 1)

	// The outbound is from the single vault able to fulfill it in only one outbound.
	c.Assert(msgs[0].VaultPubKey, Equals, yxy5PubKey)

	scheduledOutbounds := make([]TxOutItem, 0)
	for height := w.ctx.BlockHeight() + 1; height <= w.ctx.BlockHeight()+17280; height++ {
		txOut, err := w.mgr.Keeper().GetTxOut(w.ctx, height)
		c.Assert(err, IsNil)
		if height > w.ctx.BlockHeight()+maxTxOutOffset && len(txOut.TxArray) == 0 {
			// we've hit our max offset, and an empty block, we can assume the
			// rest will be empty as well
			break
		}
		scheduledOutbounds = append(scheduledOutbounds, txOut.TxArray...)
	}
	// There are no scheduled outbounds.
	c.Assert(scheduledOutbounds, HasLen, 0)
}

// TestPrepareTxOutItemExcludesCompromisedVaults verifies initial outbound
// scheduling filters flagged vaults before both Active and Retiring candidates
// are considered. The compromised vaults are funded enough to be selected if the
// filter is accidentally removed.
func (s TxOutStoreSuite) TestPrepareTxOutItemExcludesCompromisedVaults(c *C) {
	w := getHandlerTestWrapper(c, 1, true, true)

	activeCompromisedPubKey := GetRandomPubKey()
	retiringCompromisedPubKey := GetRandomPubKey()
	retiringSafePubKey := GetRandomPubKey()

	activeCompromisedVault := GetRandomVault()
	activeCompromisedVault.PubKey = activeCompromisedPubKey
	activeCompromisedVault.Type = AsgardVault
	activeCompromisedVault.Status = ActiveVault
	activeCompromisedVault.Coins = common.Coins{
		common.NewCoin(common.ETHAsset, cosmos.NewUint(10000*common.One)),
	}
	c.Assert(w.keeper.SetVault(w.ctx, activeCompromisedVault), IsNil)

	retiringCompromisedVault := GetRandomVault()
	retiringCompromisedVault.PubKey = retiringCompromisedPubKey
	retiringCompromisedVault.Type = AsgardVault
	retiringCompromisedVault.Status = RetiringVault
	retiringCompromisedVault.Coins = common.Coins{
		common.NewCoin(common.ETHAsset, cosmos.NewUint(10000*common.One)),
	}
	c.Assert(w.keeper.SetVault(w.ctx, retiringCompromisedVault), IsNil)

	retiringSafeVault := GetRandomVault()
	retiringSafeVault.PubKey = retiringSafePubKey
	retiringSafeVault.Type = AsgardVault
	retiringSafeVault.Status = RetiringVault
	retiringSafeVault.Coins = common.Coins{
		common.NewCoin(common.ETHAsset, cosmos.NewUint(5000*common.One)),
	}
	c.Assert(w.keeper.SetVault(w.ctx, retiringSafeVault), IsNil)

	pool, err := w.keeper.GetPool(w.ctx, common.ETHAsset)
	c.Assert(err, IsNil)
	pool.Asset = common.ETHAsset
	pool.BalanceAsset = cosmos.NewUint(50000 * common.One)
	pool.BalanceRune = cosmos.NewUint(50000 * common.One)
	pool.LPUnits = cosmos.NewUint(50000 * common.One)
	c.Assert(w.keeper.SetPool(w.ctx, pool), IsNil)

	w.keeper.SetMimir(w.ctx, compromisedVaultMimirKeyPrefix+activeCompromisedVault.PubKey.String(), 1)
	w.keeper.SetMimir(w.ctx, compromisedVaultMimirKeyPrefix+retiringCompromisedVault.PubKey.String(), 1)

	inTxID := GetRandomTxHash()
	voter := NewObservedTxVoter(inTxID, common.ObservedTxs{
		common.ObservedTx{
			Tx:             GetRandomTx(),
			Status:         common.Status_incomplete,
			BlockHeight:    1,
			Signers:        []string{w.activeNodeAccount.NodeAddress.String()},
			KeysignMs:      0,
			FinaliseHeight: 1,
		},
	})
	w.keeper.SetObservedTxInVoter(w.ctx, voter)

	txOutStore := newTxOutStorage(w.keeper, w.mgr.GetConstants(), w.mgr.EventMgr(), w.mgr.GasMgr())
	outputs, _, err := txOutStore.prepareTxOutItem(w.ctx, TxOutItem{
		Chain:     common.ETHChain,
		ToAddress: GetRandomETHAddress(),
		InHash:    inTxID,
		Coin:      common.NewCoin(common.ETHAsset, cosmos.NewUint(100*common.One)),
	})
	c.Assert(err, IsNil)
	c.Assert(outputs, HasLen, 1)
	c.Assert(outputs[0].VaultPubKey.Equals(activeCompromisedPubKey), Equals, false)
	c.Assert(outputs[0].VaultPubKey.Equals(retiringCompromisedPubKey), Equals, false)
	c.Assert(outputs[0].VaultPubKey.Equals(retiringSafePubKey), Equals, true)
}

func (s TxOutStoreSuite) TestAddOutTxItem_VaultStatusVersusOutboundNumber(c *C) {
	// Within this example vault bonds are treated as zero, using only assets to represent security.

	SetupConfigForTest()
	w := getHandlerTestWrapper(c, 1, true, true)

	// Prepare the relevant Asgard vault PubKeys.
	activeVaultPubKey := GetRandomPubKey()
	retiringVault1PubKey := GetRandomPubKey()
	retiringVault2PubKey := GetRandomPubKey()

	activeVault := GetRandomVault()
	activeVault.PubKey = activeVaultPubKey
	activeVault.Type = AsgardVault
	activeVault.Status = ActiveVault
	activeVault.Coins = common.Coins{
		common.NewCoin(common.ETHAsset, cosmos.NewUint(60*common.One)),
		// In this example only one Active vault has received the asset (e.g. only one migrate round),
		// and does not have enough to satisfy a 100 * common.One outbound.
	}
	c.Assert(w.keeper.SetVault(w.ctx, activeVault), IsNil)

	retiringVault1 := GetRandomVault()
	retiringVault1.PubKey = retiringVault1PubKey
	retiringVault1.Type = AsgardVault
	retiringVault1.Status = RetiringVault
	retiringVault1.Coins = common.Coins{
		common.NewCoin(common.ETHAsset, cosmos.NewUint(80*common.One)),
		// Having the most assets, this vault is the least secure (most preferred for outbounds).
	}
	c.Assert(w.keeper.SetVault(w.ctx, retiringVault1), IsNil)

	retiringVault2 := GetRandomVault()
	retiringVault2.PubKey = retiringVault2PubKey
	retiringVault2.Type = AsgardVault
	retiringVault2.Status = RetiringVault
	retiringVault2.Coins = common.Coins{
		common.NewCoin(common.ETHAsset, cosmos.NewUint(70*common.One)),
	}
	c.Assert(w.keeper.SetVault(w.ctx, retiringVault2), IsNil)

	// Setting a pool to be able to represent the Asset values.
	pool, err := w.keeper.GetPool(w.ctx, common.ETHAsset)
	c.Assert(err, IsNil)
	pool.BalanceAsset = cosmos.NewUint(500 * common.One)
	pool.BalanceRune = cosmos.NewUint(75_000 * common.One)
	pool.Asset = common.ETHAsset
	err = w.keeper.SetPool(w.ctx, pool)
	c.Assert(err, IsNil)

	var vaultsSecurityCheck Vaults
	vaultsSecurityCheck = append(vaultsSecurityCheck, activeVault)
	vaultsSecurityCheck = append(vaultsSecurityCheck, retiringVault1)
	vaultsSecurityCheck = append(vaultsSecurityCheck, retiringVault2)
	vaultsSecurityCheck = w.keeper.SortBySecurity(w.ctx, vaultsSecurityCheck, 300)
	// Confirm that these vaults from least to most secure are retiringVault1, retiringVault2, activeVault .
	// Keep in mind that all else being equal, choosing outbounds from less secure vaults is preferred.
	c.Assert(vaultsSecurityCheck[0].PubKey, Equals, retiringVault1PubKey)
	c.Assert(vaultsSecurityCheck[1].PubKey, Equals, retiringVault2PubKey)
	c.Assert(vaultsSecurityCheck[2].PubKey, Equals, activeVaultPubKey)

	acc1 := GetRandomValidatorNode(NodeActive)
	acc2 := GetRandomValidatorNode(NodeActive)
	acc3 := GetRandomValidatorNode(NodeActive)
	c.Assert(w.keeper.SetNodeAccount(w.ctx, acc1), IsNil)
	c.Assert(w.keeper.SetNodeAccount(w.ctx, acc2), IsNil)
	c.Assert(w.keeper.SetNodeAccount(w.ctx, acc3), IsNil)

	w.keeper.SetMimir(w.ctx, constants.MinTxOutVolumeThreshold.String(), 10000000000000)
	w.keeper.SetMimir(w.ctx, constants.TxOutDelayRate.String(), 250000000000)
	maxTxOutOffset := int64(720)
	w.keeper.SetMimir(w.ctx, constants.MaxTxOutOffset.String(), maxTxOutOffset)
	// Create voter
	inTxID := GetRandomTxHash()
	voter := NewObservedTxVoter(inTxID, common.ObservedTxs{
		common.ObservedTx{
			Tx:             GetRandomTx(),
			Status:         common.Status_incomplete,
			BlockHeight:    1,
			Signers:        []string{w.activeNodeAccount.NodeAddress.String(), acc1.NodeAddress.String(), acc2.NodeAddress.String()},
			KeysignMs:      0,
			FinaliseHeight: 1,
		},
	})
	w.keeper.SetObservedTxInVoter(w.ctx, voter)

	// this should be sent via asgard
	item := TxOutItem{
		Chain:     common.ETHChain,
		ToAddress: GetRandomETHAddress(),
		InHash:    inTxID,
		Coin:      common.NewCoin(common.ETHAsset, cosmos.NewUint(100*common.One)),
		// Cannot be fulfilled by any single vault
	}

	txOutStore := newTxOutStorage(w.keeper, w.mgr.GetConstants(), w.mgr.EventMgr(), w.mgr.GasMgr())

	ok, err := txOutStore.TryAddTxOutItem(w.ctx, w.mgr, item, cosmos.ZeroUint())
	c.Assert(err, IsNil)
	c.Assert(ok, Equals, true)

	msgs, err := txOutStore.GetOutboundItems(w.ctx)
	c.Assert(err, IsNil)

	// The outbounds are not yet in the outbound queue, but should now be scheduled.
	c.Assert(msgs, HasLen, 0)

	scheduledOutbounds := make([]TxOutItem, 0)
	for height := w.ctx.BlockHeight() + 1; height <= w.ctx.BlockHeight()+17280; height++ {
		txOut, err := w.mgr.Keeper().GetTxOut(w.ctx, height)
		c.Assert(err, IsNil)
		if height > w.ctx.BlockHeight()+maxTxOutOffset && len(txOut.TxArray) == 0 {
			// we've hit our max offset, and an empty block, we can assume the
			// rest will be empty as well
			break
		}
		scheduledOutbounds = append(scheduledOutbounds, txOut.TxArray...)
	}
	// Two scheduled outbounds are created, because the prepareTxOutItem logic prefers 2 outbounds with zero remaining
	// to one outbound with non-zero remaining (and an "insufficient funds for outbound request" error).
	c.Assert(scheduledOutbounds, HasLen, 2)

	// As Active vaults are preferred to Retiring vaults (less migration keysign burden),
	// the two outbounds are from the Active vault and the less secure Retiring vault.
	c.Assert(scheduledOutbounds[0].VaultPubKey, Equals, activeVaultPubKey)
	c.Assert(scheduledOutbounds[1].VaultPubKey, Equals, retiringVault1PubKey)
}

func (s *TxOutStoreSuite) TestSplitCloutEqualDistribution(c *check.C) {
	ctx, _ := setupManagerForTest(c)
	tos := TxOutStorage{}

	// Define test cases
	swapperCloutLimit := cosmos.NewUint(100)
	clout1 := cosmos.NewUint(30)
	clout2 := cosmos.NewUint(30)
	runeValue := cosmos.NewUint(60)

	// Call function under test
	amountFromClout1, amountFromClout2, remainingRune := tos.splitClout(ctx, swapperCloutLimit, clout1, clout2, runeValue)

	// Assert that the clouts were split equally and no rune value remains
	c.Check(amountFromClout1.Equal(cosmos.NewUint(30)), check.Equals, true)
	c.Check(amountFromClout2.Equal(cosmos.NewUint(30)), check.Equals, true)
	c.Check(remainingRune.IsZero(), check.Equals, true)
}

func (s *TxOutStoreSuite) TestSplitCloutOneSideExceeds(c *check.C) {
	ctx, _ := setupManagerForTest(c)
	tos := TxOutStorage{}

	// Define test cases
	swapperCloutLimit := cosmos.NewUint(100)
	clout1 := cosmos.NewUint(70)
	clout2 := cosmos.NewUint(20)
	runeValue := cosmos.NewUint(60)

	// Call function under test
	amountFromClout1, amountFromClout2, remainingRune := tos.splitClout(ctx, swapperCloutLimit, clout1, clout2, runeValue)

	// Assert that clout2 took all it could, and clout1 took the remainder up to the limit
	c.Check(amountFromClout1.Equal(cosmos.NewUint(46)), check.Equals, true, Commentf("%d", amountFromClout1.Uint64()))
	c.Check(amountFromClout2.Equal(cosmos.NewUint(14)), check.Equals, true, Commentf("%d", amountFromClout2.Uint64()))
	c.Check(remainingRune.Equal(cosmos.NewUint(0)), check.Equals, true)
}

func (s *TxOutStoreSuite) TestSplitCloutUnevenDistribution(c *check.C) {
	ctx, _ := setupManagerForTest(c)
	tos := TxOutStorage{}

	// Define test cases
	swapperCloutLimit := cosmos.NewUint(100)
	clout1 := cosmos.NewUint(60)
	clout2 := cosmos.NewUint(25)
	runeValue := cosmos.NewUint(50)

	// Call function under test
	amountFromClout1, amountFromClout2, remainingRune := tos.splitClout(ctx, swapperCloutLimit, clout1, clout2, runeValue)

	// Assert that the clouts were split according to their capacity
	c.Check(amountFromClout1.Equal(cosmos.NewUint(35)), check.Equals, true, Commentf("%d", amountFromClout1.Uint64()))
	c.Check(amountFromClout2.Equal(cosmos.NewUint(15)), check.Equals, true, Commentf("%d", amountFromClout2.Uint64()))
	c.Check(remainingRune.Equal(cosmos.NewUint(0)), check.Equals, true)
}

func (s *TxOutStoreSuite) TestSplitCloutOverLimit(c *check.C) {
	ctx, _ := setupManagerForTest(c)
	tos := TxOutStorage{}

	// Define test cases
	swapperCloutLimit := cosmos.NewUint(100)
	clout1 := cosmos.NewUint(80)
	clout2 := cosmos.NewUint(80)
	runeValue := cosmos.NewUint(100)

	// Call function under test
	amountFromClout1, amountFromClout2, remainingRune := tos.splitClout(ctx, swapperCloutLimit, clout1, clout2, runeValue)

	// Assert that the clouts were split up to the limit
	c.Check(amountFromClout1.Equal(cosmos.NewUint(50)), check.Equals, true)
	c.Check(amountFromClout2.Equal(cosmos.NewUint(50)), check.Equals, true)
	c.Check(remainingRune.Equal(cosmos.NewUint(0)), check.Equals, true)
}

func (s *TxOutStoreSuite) TestSplitCloutWithZeroRuneValue(c *check.C) {
	ctx, _ := setupManagerForTest(c)
	tos := TxOutStorage{}

	// Define test cases
	swapperCloutLimit := cosmos.NewUint(100)
	clout1 := cosmos.NewUint(40)
	clout2 := cosmos.NewUint(40)
	runeValue := cosmos.NewUint(0)

	// Call function under test
	amountFromClout1, amountFromClout2, remainingRune := tos.splitClout(ctx, swapperCloutLimit, clout1, clout2, runeValue)

	// Assert that no clout was taken from either and rune value remains zero
	c.Check(amountFromClout1.IsZero(), check.Equals, true)
	c.Check(amountFromClout2.IsZero(), check.Equals, true)
	c.Check(remainingRune.IsZero(), check.Equals, true)
}

func (s *TxOutStoreSuite) TestSplitCloutCloutsAtLimit(c *check.C) {
	ctx, _ := setupManagerForTest(c)
	tos := TxOutStorage{}
	swapperCloutLimit := cosmos.NewUint(100)
	clout1 := cosmos.NewUint(50)
	clout2 := cosmos.NewUint(50)
	runeValue := cosmos.NewUint(30)

	// Call function under test
	amountFromClout1, amountFromClout2, remainingRune := tos.splitClout(ctx, swapperCloutLimit, clout1, clout2, runeValue)

	// Assert that the clouts were split equally and no rune value remains
	c.Check(amountFromClout1.Equal(cosmos.NewUint(15)), check.Equals, true)
	c.Check(amountFromClout2.Equal(cosmos.NewUint(15)), check.Equals, true)
	c.Check(remainingRune.Equal(cosmos.NewUint(0)), check.Equals, true)
}

func (s *TxOutStoreSuite) TestSplitCloutRuneValueExceedingClouts(c *check.C) {
	ctx, _ := setupManagerForTest(c)
	tos := TxOutStorage{}
	swapperCloutLimit := cosmos.NewUint(100)
	clout1 := cosmos.NewUint(40)
	clout2 := cosmos.NewUint(40)
	runeValue := cosmos.NewUint(120)

	// Call function under test
	amountFromClout1, amountFromClout2, remainingRune := tos.splitClout(ctx, swapperCloutLimit, clout1, clout2, runeValue)

	// Assert that the clouts took the maximum they could, and the remaining rune is correct
	c.Check(amountFromClout1.Equal(cosmos.NewUint(40)), check.Equals, true)
	c.Check(amountFromClout2.Equal(cosmos.NewUint(40)), check.Equals, true)
	c.Check(remainingRune.Equal(cosmos.NewUint(40)), check.Equals, true)
}

func (s TxOutStoreSuite) TestSelectFallbackVault(c *C) {
	ctx, mgr := setupManagerForTest(c)

	// Create three vaults with different ETH balances
	vault1 := GetRandomVault()
	vault1.Type = AsgardVault
	vault1.Status = ActiveVault
	vault1.Coins = common.Coins{
		common.NewCoin(common.ETHAsset, cosmos.NewUint(100*common.One)), // Highest ETH balance
	}
	c.Assert(mgr.Keeper().SetVault(ctx, vault1), IsNil)

	vault2 := GetRandomVault()
	vault2.Type = AsgardVault
	vault2.Status = ActiveVault
	vault2.Coins = common.Coins{
		common.NewCoin(common.ETHAsset, cosmos.NewUint(50*common.One)),
	}
	c.Assert(mgr.Keeper().SetVault(ctx, vault2), IsNil)

	vault3 := GetRandomVault()
	vault3.Type = AsgardVault
	vault3.Status = ActiveVault
	vault3.Coins = common.Coins{
		common.NewCoin(common.ETHAsset, cosmos.NewUint(75*common.One)),
	}
	c.Assert(mgr.Keeper().SetVault(ctx, vault3), IsNil)

	txOutStore := newTxOutStorage(mgr.Keeper(), mgr.GetConstants(), mgr.EventMgr(), mgr.GasMgr())

	// Create a TxOutItem requesting more ETH than any single vault has
	toi := TxOutItem{
		Chain:     common.ETHChain,
		ToAddress: GetRandomETHAddress(),
		InHash:    GetRandomTxHash(),
		Coin:      common.NewCoin(common.ETHAsset, cosmos.NewUint(200*common.One)), // More than any vault has
	}

	maxGasCoin := common.NewCoin(common.ETHAsset, cosmos.NewUint(1*common.One))
	vaults := Vaults{vault1, vault2, vault3}

	// Test: selectFallbackVault should pick the vault with the highest balance
	fallbackVault := txOutStore.selectFallbackVault(ctx, toi, maxGasCoin, vaults)
	c.Assert(fallbackVault.PubKey.Equals(vault1.PubKey), Equals, true, Commentf("Expected vault1 (highest balance) to be selected"))
}

func (s TxOutStoreSuite) TestSelectFallbackVaultSkipsFrozenChain(c *C) {
	ctx, mgr := setupManagerForTest(c)

	// Create a vault with highest balance but frozen for ETH chain
	vault1 := GetRandomVault()
	vault1.Type = AsgardVault
	vault1.Status = ActiveVault
	vault1.Frozen = []string{common.ETHChain.String()}
	vault1.Coins = common.Coins{
		common.NewCoin(common.ETHAsset, cosmos.NewUint(100*common.One)),
	}
	c.Assert(mgr.Keeper().SetVault(ctx, vault1), IsNil)

	// Create a vault with lower balance but not frozen
	vault2 := GetRandomVault()
	vault2.Type = AsgardVault
	vault2.Status = ActiveVault
	vault2.Coins = common.Coins{
		common.NewCoin(common.ETHAsset, cosmos.NewUint(50*common.One)),
	}
	c.Assert(mgr.Keeper().SetVault(ctx, vault2), IsNil)

	txOutStore := newTxOutStorage(mgr.Keeper(), mgr.GetConstants(), mgr.EventMgr(), mgr.GasMgr())

	toi := TxOutItem{
		Chain:     common.ETHChain,
		ToAddress: GetRandomETHAddress(),
		InHash:    GetRandomTxHash(),
		Coin:      common.NewCoin(common.ETHAsset, cosmos.NewUint(200*common.One)),
	}

	maxGasCoin := common.NewCoin(common.ETHAsset, cosmos.NewUint(1*common.One))
	vaults := Vaults{vault1, vault2}

	// Test: selectFallbackVault should skip the frozen vault and pick vault2
	fallbackVault := txOutStore.selectFallbackVault(ctx, toi, maxGasCoin, vaults)
	c.Assert(fallbackVault.PubKey.Equals(vault2.PubKey), Equals, true, Commentf("Expected vault2 (not frozen) to be selected"))
}

func (s TxOutStoreSuite) TestSelectFallbackVaultSkipsInsufficientGas(c *C) {
	ctx, mgr := setupManagerForTest(c)

	// Request a non-gas asset (use a token on ETH)
	ethToken, err := common.NewAsset("ETH.USDC-0xA0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48")
	c.Assert(err, IsNil)

	// Create a vault with highest USDC balance but insufficient gas (no ETH)
	vault1 := GetRandomVault()
	vault1.Type = AsgardVault
	vault1.Status = ActiveVault
	vault1.Coins = common.Coins{
		common.NewCoin(ethToken, cosmos.NewUint(100*common.One)), // High USDC but no ETH for gas
	}
	c.Assert(mgr.Keeper().SetVault(ctx, vault1), IsNil)

	// Create a vault with lower USDC balance but has ETH for gas
	vault2 := GetRandomVault()
	vault2.Type = AsgardVault
	vault2.Status = ActiveVault
	vault2.Coins = common.Coins{
		common.NewCoin(ethToken, cosmos.NewUint(50*common.One)),        // Lower USDC
		common.NewCoin(common.ETHAsset, cosmos.NewUint(50*common.One)), // Has ETH for gas
	}
	c.Assert(mgr.Keeper().SetVault(ctx, vault2), IsNil)

	txOutStore := newTxOutStorage(mgr.Keeper(), mgr.GetConstants(), mgr.EventMgr(), mgr.GasMgr())

	toi := TxOutItem{
		Chain:     common.ETHChain,
		ToAddress: GetRandomETHAddress(),
		InHash:    GetRandomTxHash(),
		Coin:      common.NewCoin(ethToken, cosmos.NewUint(200*common.One)),
	}

	// Require significant gas (in ETH, the gas asset)
	maxGasCoin := common.NewCoin(common.ETHAsset, cosmos.NewUint(10*common.One))
	vaults := Vaults{vault1, vault2}

	// Test: selectFallbackVault should skip vault1 (no ETH for gas) and pick vault2
	fallbackVault := txOutStore.selectFallbackVault(ctx, toi, maxGasCoin, vaults)
	c.Assert(fallbackVault.PubKey.Equals(vault2.PubKey), Equals, true, Commentf("Expected vault2 (has gas) to be selected"))
}

func (s TxOutStoreSuite) TestSelectFallbackVaultReturnsEmptyWhenNoValidVault(c *C) {
	ctx, mgr := setupManagerForTest(c)

	// Create a vault frozen for ETH
	vault1 := GetRandomVault()
	vault1.Type = AsgardVault
	vault1.Status = ActiveVault
	vault1.Frozen = []string{common.ETHChain.String()}
	vault1.Coins = common.Coins{
		common.NewCoin(common.ETHAsset, cosmos.NewUint(100*common.One)),
	}
	c.Assert(mgr.Keeper().SetVault(ctx, vault1), IsNil)

	// Create a vault without the required asset
	vault2 := GetRandomVault()
	vault2.Type = AsgardVault
	vault2.Status = ActiveVault
	vault2.Coins = common.Coins{
		common.NewCoin(common.BTCAsset, cosmos.NewUint(50*common.One)), // Wrong asset
	}
	c.Assert(mgr.Keeper().SetVault(ctx, vault2), IsNil)

	txOutStore := newTxOutStorage(mgr.Keeper(), mgr.GetConstants(), mgr.EventMgr(), mgr.GasMgr())

	toi := TxOutItem{
		Chain:     common.ETHChain,
		ToAddress: GetRandomETHAddress(),
		InHash:    GetRandomTxHash(),
		Coin:      common.NewCoin(common.ETHAsset, cosmos.NewUint(200*common.One)),
	}

	maxGasCoin := common.NewCoin(common.ETHAsset, cosmos.NewUint(1*common.One))
	vaults := Vaults{vault1, vault2}

	// Test: selectFallbackVault should return empty vault when no valid vault
	fallbackVault := txOutStore.selectFallbackVault(ctx, toi, maxGasCoin, vaults)
	c.Assert(fallbackVault.PubKey.IsEmpty(), Equals, true, Commentf("Expected empty vault when no valid fallback"))
}

func (s TxOutStoreSuite) TestDiscoverOutboundsFallbackVault(c *C) {
	// Test that DiscoverOutbounds uses fallback vault when no vault has enough available balance.
	// This simulates a scenario where pending outbounds have reduced the effective available balance.
	SetupConfigForTest()
	w := getHandlerTestWrapper(c, 1, true, true)

	// Create a vault with some ETH
	vault := GetRandomVault()
	vault.Type = AsgardVault
	vault.Status = ActiveVault
	vault.Coins = common.Coins{
		common.NewCoin(common.ETHAsset, cosmos.NewUint(50*common.One)),
	}
	c.Assert(w.keeper.SetVault(w.ctx, vault), IsNil)

	acc1 := GetRandomValidatorNode(NodeActive)
	acc2 := GetRandomValidatorNode(NodeActive)
	acc3 := GetRandomValidatorNode(NodeActive)
	c.Assert(w.keeper.SetNodeAccount(w.ctx, acc1), IsNil)
	c.Assert(w.keeper.SetNodeAccount(w.ctx, acc2), IsNil)
	c.Assert(w.keeper.SetNodeAccount(w.ctx, acc3), IsNil)

	inTxID := GetRandomTxHash()
	voter := NewObservedTxVoter(inTxID, common.ObservedTxs{
		common.ObservedTx{
			Tx:             GetRandomTx(),
			Status:         common.Status_incomplete,
			BlockHeight:    1,
			Signers:        []string{w.activeNodeAccount.NodeAddress.String(), acc1.NodeAddress.String(), acc2.NodeAddress.String()},
			KeysignMs:      0,
			FinaliseHeight: 1,
		},
	})
	w.keeper.SetObservedTxInVoter(w.ctx, voter)

	// Test the selectFallbackVault function directly since the TryAddTxOutItem flow
	// is more complex to test (requires setting up proper pending outbound tracking).
	// The unit tests above verify selectFallbackVault works correctly,
	// and this test verifies that DiscoverOutbounds calls it when no outputs are created.

	eventMgr := newRecordingEventMgr()
	txOutStore := newTxOutStorage(w.keeper, w.mgr.GetConstants(), eventMgr, w.mgr.GasMgr())

	// Create a TxOutItem for testing
	toi := TxOutItem{
		Chain:     common.ETHChain,
		ToAddress: GetRandomETHAddress(),
		InHash:    inTxID,
		Coin:      common.NewCoin(common.ETHAsset, cosmos.NewUint(100*common.One)), // More than vault has
	}

	// Create vaults with zero available balance (simulating pending outbound deduction)
	vaultWithZeroAvailable := vault
	vaultWithZeroAvailable.Coins = common.Coins{
		common.NewCoin(common.ETHAsset, cosmos.ZeroUint()), // Zero after pending deduction
	}
	vaults := Vaults{vaultWithZeroAvailable}

	maxGasCoin := common.NewCoin(common.ETHAsset, cosmos.NewUint(common.One))

	// Call DiscoverOutbounds with a vault that has zero available balance
	outputs, remaining := txOutStore.DiscoverOutbounds(w.ctx, cosmos.NewUint(1000), maxGasCoin, toi, vaults)

	// Should have created one output using the fallback vault (which gets original balance from keeper)
	c.Assert(outputs, HasLen, 1, Commentf("Expected 1 output from fallback vault, got %d", len(outputs)))
	c.Assert(outputs[0].VaultPubKey.Equals(vault.PubKey), Equals, true, Commentf("Expected output to use original vault"))
	// The remaining should be zero because the entire amount was assigned to the fallback vault
	c.Assert(remaining.IsZero(), Equals, true, Commentf("Expected zero remaining, got %s", remaining.String()))
	securityEvents := eventMgr.securityEvents()
	c.Assert(securityEvents, HasLen, 1)
	c.Assert(securityEvents[0].Msg, Equals, "outbound scheduled with insufficient available vault balance")
}

func (s TxOutStoreSuite) TestDiscoverOutboundsRemainderAddedToSmallestOutput(c *C) {
	// Test that when partial outputs exist but there's a remainder, the remainder
	// is added to the smallest output to trigger the recovery flow.
	ctx, mgr := setupManagerForTest(c)

	// Create two vaults with different ETH balances that together can't cover the requested amount
	vault1 := GetRandomVault()
	vault1.Type = AsgardVault
	vault1.Status = ActiveVault
	vault1.Coins = common.Coins{
		common.NewCoin(common.ETHAsset, cosmos.NewUint(30*common.One)), // Smaller balance
	}
	c.Assert(mgr.Keeper().SetVault(ctx, vault1), IsNil)

	vault2 := GetRandomVault()
	vault2.Type = AsgardVault
	vault2.Status = ActiveVault
	vault2.Coins = common.Coins{
		common.NewCoin(common.ETHAsset, cosmos.NewUint(50*common.One)), // Larger balance
	}
	c.Assert(mgr.Keeper().SetVault(ctx, vault2), IsNil)

	eventMgr := newRecordingEventMgr()
	txOutStore := newTxOutStorage(mgr.Keeper(), mgr.GetConstants(), eventMgr, mgr.GasMgr())

	// Request more ETH than both vaults combined have
	// Total available: 30 + 50 = 80 ETH, requesting 100 ETH
	toi := TxOutItem{
		Chain:     common.ETHChain,
		ToAddress: GetRandomETHAddress(),
		InHash:    GetRandomTxHash(),
		Coin:      common.NewCoin(common.ETHAsset, cosmos.NewUint(100*common.One)),
	}

	maxGasCoin := common.NewCoin(common.ETHAsset, cosmos.NewUint(1*common.One))
	transactionFee := cosmos.NewUint(1000)

	// The vaults are sorted by vaults-necessary (smallest number first means larger vaults first)
	// vault2 (50 ETH) should be used first, then vault1 (30 ETH)
	// After both: 50 + ~29 = ~79 ETH covered (minus fees), ~21 ETH remainder
	vaults := Vaults{vault1, vault2}

	outputs, remaining := txOutStore.DiscoverOutbounds(ctx, transactionFee, maxGasCoin, toi, vaults)

	// Should have 2 outputs (both vaults used)
	c.Assert(outputs, HasLen, 2, Commentf("Expected 2 outputs, got %d", len(outputs)))

	// Find which output is from vault1 (smaller balance, should get remainder added)
	var vault1Output, vault2Output *TxOutItem
	for i := range outputs {
		if outputs[i].VaultPubKey.Equals(vault1.PubKey) {
			vault1Output = &outputs[i]
		} else if outputs[i].VaultPubKey.Equals(vault2.PubKey) {
			vault2Output = &outputs[i]
		}
	}
	c.Assert(vault1Output, NotNil, Commentf("Expected output from vault1"))
	c.Assert(vault2Output, NotNil, Commentf("Expected output from vault2"))

	// vault2 output should be its available balance (50 ETH minus gas reservation)
	expectedVault2Amount := cosmos.NewUint(49 * common.One) // 50 - 1 for max gas
	c.Assert(vault2Output.Coin.Amount.Equal(expectedVault2Amount), Equals, true,
		Commentf("Expected vault2 amount %s, got %s", expectedVault2Amount.String(), vault2Output.Coin.Amount.String()))

	// vault1 output should have the remainder added to it
	// Original: 100 - 49 = 51 ETH needed
	// vault1 available: 29 ETH (30 - 1 for max gas)
	// So vault1 takes 29, leaving 22 ETH remainder
	// That 22 ETH remainder should be added to vault1's output (the smaller one)
	// So vault1 output should be 29 + 22 = 51 ETH
	expectedVault1Amount := cosmos.NewUint(51 * common.One)
	c.Assert(vault1Output.Coin.Amount.Equal(expectedVault1Amount), Equals, true,
		Commentf("Expected vault1 amount %s (with remainder), got %s", expectedVault1Amount.String(), vault1Output.Coin.Amount.String()))

	// Remaining should be zero since we added the remainder to the smallest output
	c.Assert(remaining.IsZero(), Equals, true, Commentf("Expected zero remaining, got %s", remaining.String()))
	securityEvents := eventMgr.securityEvents()
	c.Assert(securityEvents, HasLen, 1)
	c.Assert(securityEvents[0].Msg, Equals, "outbound scheduled with insufficient available vault balance")
}

func (s TxOutStoreSuite) TestDiscoverOutboundsRagnarokGasAssetDoesNotReserveGasTwice(c *C) {
	ctx, mgr := setupManagerForTest(c)

	vault := GetRandomVault()
	vault.Type = AsgardVault
	vault.Status = ActiveVault
	vault.Coins = common.Coins{
		common.NewCoin(common.ETHAsset, cosmos.NewUint(100*common.One)),
	}
	c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)

	eventMgr := newRecordingEventMgr()
	txOutStore := newTxOutStorage(mgr.Keeper(), mgr.GetConstants(), eventMgr, mgr.GasMgr())
	toi := TxOutItem{
		Chain:     common.ETHChain,
		ToAddress: GetRandomETHAddress(),
		InHash:    common.BlankTxID,
		Coin:      common.NewCoin(common.ETHAsset, cosmos.NewUint(100*common.One)),
		Memo:      NewRagnarokMemo(ctx.BlockHeight()).String(),
	}

	outputs, remaining := txOutStore.DiscoverOutbounds(
		ctx,
		cosmos.NewUint(1000),
		common.NewCoin(common.ETHAsset, cosmos.NewUint(common.One)),
		toi,
		Vaults{vault},
	)

	c.Assert(outputs, HasLen, 1)
	c.Assert(outputs[0].Coin.Amount.Equal(cosmos.NewUint(100*common.One)), Equals, true)
	c.Assert(remaining.IsZero(), Equals, true)
	c.Assert(eventMgr.securityEvents(), HasLen, 0)
}

func (s TxOutStoreSuite) TestDiscoverOutboundsRagnarokFallbackDoesNotEmitSecurity(c *C) {
	ctx, mgr := setupManagerForTest(c)

	vault := GetRandomVault()
	vault.Type = AsgardVault
	vault.Status = RetiringVault
	vault.Coins = common.Coins{
		common.NewCoin(common.TRXAsset, cosmos.NewUint(224139306140)),
	}
	c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)

	pendingDeductedVault := vault
	pendingDeductedVault.Coins = common.Coins{
		common.NewCoin(common.TRXAsset, cosmos.ZeroUint()),
	}

	eventMgr := newRecordingEventMgr()
	txOutStore := newTxOutStorage(mgr.Keeper(), mgr.GetConstants(), eventMgr, mgr.GasMgr())
	toi := TxOutItem{
		Chain:     common.TRONChain,
		ToAddress: common.Address("TJSU21dCbB6uJXSANMydvRvDzr3kuuuKxS"),
		InHash:    common.BlankTxID,
		Coin:      common.NewCoin(common.TRXAsset, cosmos.NewUint(224139306140)),
		Memo:      NewRagnarokMemo(ctx.BlockHeight()).String(),
	}

	outputs, remaining := txOutStore.DiscoverOutbounds(
		ctx,
		cosmos.NewUint(1000),
		common.NewCoin(common.TRXAsset, cosmos.NewUint(2800000)),
		toi,
		Vaults{pendingDeductedVault},
	)

	c.Assert(outputs, HasLen, 1)
	c.Assert(outputs[0].VaultPubKey.Equals(vault.PubKey), Equals, true)
	c.Assert(outputs[0].Coin.Amount.Equal(toi.Coin.Amount), Equals, true)
	c.Assert(remaining.IsZero(), Equals, true)
	c.Assert(eventMgr.securityEvents(), HasLen, 0)
}

func (s TxOutStoreSuite) TestDiscoverOutboundsRagnarokXRPLeavesReserve(c *C) {
	ctx, mgr := setupManagerForTest(c)

	vault := GetRandomVault()
	vault.Type = AsgardVault
	vault.Status = RetiringVault
	vault.Coins = common.Coins{
		common.NewCoin(common.XRPAsset, cosmos.NewUint(250660398000)),
	}
	c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)

	eventMgr := newRecordingEventMgr()
	txOutStore := newTxOutStorage(mgr.Keeper(), mgr.GetConstants(), eventMgr, mgr.GasMgr())
	toi := TxOutItem{
		Chain:     common.XRPChain,
		ToAddress: common.Address("rpfiEuZjKgjqchVeq5qBEUH9zWeo2xoN9p"),
		InHash:    common.BlankTxID,
		Coin:      common.NewCoin(common.XRPAsset, cosmos.NewUint(250660397985)),
		Memo:      NewRagnarokMemo(ctx.BlockHeight()).String(),
	}

	outputs, remaining := txOutStore.DiscoverOutbounds(
		ctx,
		cosmos.NewUint(1000),
		common.NewCoin(common.XRPAsset, cosmos.NewUint(15)),
		toi,
		Vaults{vault},
	)

	c.Assert(outputs, HasLen, 1)
	c.Assert(outputs[0].Coin.Amount.Equal(cosmos.NewUint(250560398000)), Equals, true)
	c.Assert(remaining.IsZero(), Equals, true)
	c.Assert(eventMgr.securityEvents(), HasLen, 0)
}

// TestDiscoverOutboundsRagnarokSOLLeavesRentReserve covers the SOL Ragnarok
// failure seen in simulation: txout discovery scheduled a near-full SOL sweep,
// the transaction signed, and Solana rejected every broadcast because the
// source account would fall below rent-exempt balance. The fix must cap only the
// sweep amount and then treat the left-behind rent reserve as an intentional
// Ragnarok residual rather than a security/recovery deficit.
func (s TxOutStoreSuite) TestDiscoverOutboundsRagnarokSOLLeavesRentReserve(c *C) {
	ctx, mgr := setupManagerForTest(c)

	vault := GetRandomVault()
	vault.Type = AsgardVault
	vault.Status = RetiringVault
	vault.Coins = common.Coins{
		common.NewCoin(common.SOLAsset, cosmos.NewUint(2524534019)),
	}
	c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)

	eventMgr := newRecordingEventMgr()
	txOutStore := newTxOutStorage(mgr.Keeper(), mgr.GetConstants(), eventMgr, mgr.GasMgr())

	// This request mirrors the stuck RAGNAROK:2520 item from the default
	// simulation run: it leaves only gas-sized dust unless txout discovery first
	// subtracts the same SOL account reserve used by migration.
	toi := TxOutItem{
		Chain:     common.SOLChain,
		ToAddress: common.Address("AfzXzUhKw57x9Pub9Dj4CxYovYCktSrXY7mcr1XEcftY"),
		InHash:    common.BlankTxID,
		Coin:      common.NewCoin(common.SOLAsset, cosmos.NewUint(2524532519)),
		Memo:      NewRagnarokMemo(ctx.BlockHeight()).String(),
	}

	outputs, remaining := txOutStore.DiscoverOutbounds(
		ctx,
		cosmos.NewUint(1000),
		common.NewCoin(common.SOLAsset, cosmos.NewUint(750)),
		toi,
		Vaults{vault},
	)

	// The scheduled outbound is capped to the vault balance minus the chain dust
	// threshold, matching migration's SOL account-reserve convention. The
	// leftover Ragnarok residual is intentionally zeroed instead of being routed
	// into fallback/security handling.
	c.Assert(outputs, HasLen, 1)
	c.Assert(outputs[0].Coin.Amount.Equal(cosmos.NewUint(2524434019)), Equals, true)
	c.Assert(remaining.IsZero(), Equals, true)
	c.Assert(eventMgr.securityEvents(), HasLen, 0)
}

// tokenGasReserveVaults returns a vault holding ETH and USDT and a vault holding
// only ethInGasOnly ETH, both saved to the keeper.
func tokenGasReserveVaults(c *C, ctx cosmos.Context, mgr Manager, usdt common.Asset, ethInGasOnly uint64) (Vault, Vault) {
	withTokens := GetRandomVault()
	withTokens.Type = AsgardVault
	withTokens.Status = ActiveVault
	withTokens.Coins = common.Coins{
		common.NewCoin(common.ETHAsset, cosmos.NewUint(100*common.One)),
		common.NewCoin(usdt, cosmos.NewUint(1000*common.One)),
	}
	c.Assert(mgr.Keeper().SetVault(ctx, withTokens), IsNil)

	gasOnly := GetRandomVault()
	gasOnly.Type = AsgardVault
	gasOnly.Status = ActiveVault
	gasOnly.Coins = common.Coins{
		common.NewCoin(common.ETHAsset, cosmos.NewUint(ethInGasOnly*common.One)),
	}
	c.Assert(mgr.Keeper().SetVault(ctx, gasOnly), IsNil)
	return withTokens, gasOnly
}

// TestDiscoverOutboundsTokenGasReserve proves that with VaultTokenGasReserve on,
// a gas-asset outbound leaves reserve x MaxGas per token in a vault that holds
// tokens, spilling the rest onto other vaults, and that it changes nothing when
// off, for token outbounds, or for Ragnarok.
func (s TxOutStoreSuite) TestDiscoverOutboundsTokenGasReserve(c *C) {
	usdt, err := common.NewAsset("ETH.USDT-0XDAC17F958D2EE523A2206206994597C13D831EC7")
	c.Assert(err, IsNil)
	maxGas := common.NewCoin(common.ETHAsset, cosmos.NewUint(common.One))
	fee := cosmos.NewUint(1000)
	ethOut := func(amount uint64, memo string) TxOutItem {
		return TxOutItem{
			Chain:     common.ETHChain,
			ToAddress: GetRandomETHAddress(),
			InHash:    GetRandomTxHash(),
			Coin:      common.NewCoin(common.ETHAsset, cosmos.NewUint(amount*common.One)),
			Memo:      memo,
		}
	}

	// Off: the token vault pays 95 of its 100 ETH alone, as before.
	ctx, mgr := setupManagerForTest(c)
	withTokens, gasOnly := tokenGasReserveVaults(c, ctx, mgr, usdt, 50)
	store := newTxOutStorage(mgr.Keeper(), mgr.GetConstants(), newRecordingEventMgr(), mgr.GasMgr())
	outputs, remaining := store.DiscoverOutbounds(ctx, fee, maxGas, ethOut(95, ""), Vaults{withTokens, gasOnly})
	c.Assert(remaining.IsZero(), Equals, true)
	c.Assert(outputs, HasLen, 1)
	c.Check(outputs[0].VaultPubKey.Equals(withTokens.PubKey), Equals, true)
	c.Check(outputs[0].Coin.Amount.Equal(cosmos.NewUint(95*common.One)), Equals, true)

	// On (10): the token vault keeps 1 MaxGas for this outbound plus 10 for its
	// USDT, so it pays 89 and the gas-only vault pays the other 6.
	ctx, mgr = setupManagerForTest(c)
	mgr.Keeper().SetMimir(ctx, constants.VaultTokenGasReserve.String(), 10)
	withTokens, gasOnly = tokenGasReserveVaults(c, ctx, mgr, usdt, 50)
	eventMgr := newRecordingEventMgr()
	store = newTxOutStorage(mgr.Keeper(), mgr.GetConstants(), eventMgr, mgr.GasMgr())
	outputs, remaining = store.DiscoverOutbounds(ctx, fee, maxGas, ethOut(95, ""), Vaults{withTokens, gasOnly})
	c.Assert(remaining.IsZero(), Equals, true)
	c.Assert(outputs, HasLen, 2)
	c.Check(outputs[0].VaultPubKey.Equals(withTokens.PubKey), Equals, true)
	c.Check(outputs[0].Coin.Amount.Equal(cosmos.NewUint(89*common.One)), Equals, true)
	c.Check(outputs[1].VaultPubKey.Equals(gasOnly.PubKey), Equals, true)
	c.Check(outputs[1].Coin.Amount.Equal(cosmos.NewUint(6*common.One)), Equals, true)
	c.Check(eventMgr.securityEvents(), HasLen, 0)

	// On, but the vaults cannot pay 95 with the reserve held back (89 + 2):
	// the reserve is dropped and the token vault pays it all, as before.
	ctx, mgr = setupManagerForTest(c)
	mgr.Keeper().SetMimir(ctx, constants.VaultTokenGasReserve.String(), 10)
	withTokens, gasOnly = tokenGasReserveVaults(c, ctx, mgr, usdt, 3)
	eventMgr = newRecordingEventMgr()
	store = newTxOutStorage(mgr.Keeper(), mgr.GetConstants(), eventMgr, mgr.GasMgr())
	outputs, remaining = store.DiscoverOutbounds(ctx, fee, maxGas, ethOut(95, ""), Vaults{withTokens, gasOnly})
	c.Assert(remaining.IsZero(), Equals, true)
	c.Assert(outputs, HasLen, 1)
	c.Check(outputs[0].VaultPubKey.Equals(withTokens.PubKey), Equals, true)
	c.Check(outputs[0].Coin.Amount.Equal(cosmos.NewUint(95*common.One)), Equals, true)
	c.Check(eventMgr.securityEvents(), HasLen, 0)

	// On, but the reserve split would leave the gas-only vault a tail smaller
	// than the fee plus dust, which prepareTxOutItem drops after the inbound was
	// debited: the reserve is dropped and the token vault pays it all.
	ctx, mgr = setupManagerForTest(c)
	mgr.Keeper().SetMimir(ctx, constants.VaultTokenGasReserve.String(), 10)
	withTokens, gasOnly = tokenGasReserveVaults(c, ctx, mgr, usdt, 50)
	store = newTxOutStorage(mgr.Keeper(), mgr.GetConstants(), newRecordingEventMgr(), mgr.GasMgr())
	tail := ethOut(89, "")
	tail.Coin.Amount = tail.Coin.Amount.Add(fee.QuoUint64(2))
	outputs, _ = allocateOutbounds(fee, maxGas, tail, Vaults{withTokens, gasOnly}, false, 10)
	c.Assert(outputs, HasLen, 2)
	c.Check(hasUnpayableFragment(outputs, fee, common.ETHChain.DustThreshold()), Equals, true)
	outputs, remaining = store.DiscoverOutbounds(ctx, fee, maxGas, tail, Vaults{withTokens, gasOnly})
	c.Assert(remaining.IsZero(), Equals, true)
	c.Assert(outputs, HasLen, 1)
	c.Check(outputs[0].VaultPubKey.Equals(withTokens.PubKey), Equals, true)
	c.Check(outputs[0].Coin.Amount.Equal(tail.Coin.Amount), Equals, true)

	// A tail that clears the fee plus dust keeps the reserve split.
	tail = ethOut(89, "")
	tail.Coin.Amount = tail.Coin.Amount.Add(fee.Add(common.ETHChain.DustThreshold()))
	outputs, remaining = store.DiscoverOutbounds(ctx, fee, maxGas, tail, Vaults{withTokens, gasOnly})
	c.Assert(remaining.IsZero(), Equals, true)
	c.Assert(outputs, HasLen, 2)
	c.Check(outputs[1].VaultPubKey.Equals(gasOnly.PubKey), Equals, true)
	c.Check(outputs[1].Coin.Amount.Equal(fee.Add(common.ETHChain.DustThreshold())), Equals, true)

	// On: a token outbound holds nothing back.
	ctx, mgr = setupManagerForTest(c)
	mgr.Keeper().SetMimir(ctx, constants.VaultTokenGasReserve.String(), 10)
	withTokens, _ = tokenGasReserveVaults(c, ctx, mgr, usdt, 50)
	store = newTxOutStorage(mgr.Keeper(), mgr.GetConstants(), newRecordingEventMgr(), mgr.GasMgr())
	tokenOut := ethOut(0, "")
	tokenOut.Coin = common.NewCoin(usdt, cosmos.NewUint(1000*common.One))
	outputs, remaining = store.DiscoverOutbounds(ctx, fee, maxGas, tokenOut, Vaults{withTokens})
	c.Assert(remaining.IsZero(), Equals, true)
	c.Assert(outputs, HasLen, 1)
	c.Check(outputs[0].Coin.Amount.Equal(cosmos.NewUint(1000*common.One)), Equals, true)

	// On: Ragnarok sweeps the vault's gas asset without a reserve.
	ctx, mgr = setupManagerForTest(c)
	mgr.Keeper().SetMimir(ctx, constants.VaultTokenGasReserve.String(), 10)
	withTokens, _ = tokenGasReserveVaults(c, ctx, mgr, usdt, 50)
	store = newTxOutStorage(mgr.Keeper(), mgr.GetConstants(), newRecordingEventMgr(), mgr.GasMgr())
	outputs, remaining = store.DiscoverOutbounds(ctx, fee, maxGas, ethOut(100, NewRagnarokMemo(ctx.BlockHeight()).String()), Vaults{withTokens})
	c.Assert(remaining.IsZero(), Equals, true)
	c.Assert(outputs, HasLen, 1)
	c.Check(outputs[0].Coin.Amount.Equal(cosmos.NewUint(100*common.One)), Equals, true)
}

func (s TxOutStoreSuite) TestNonGasTokenCount(c *C) {
	usdt, err := common.NewAsset("ETH.USDT-0XDAC17F958D2EE523A2206206994597C13D831EC7")
	c.Assert(err, IsNil)
	usdc, err := common.NewAsset("ETH.USDC-0XA0B86991C6218B36C1D19D4A2E9EB0CE3606EB48")
	c.Assert(err, IsNil)
	vault := GetRandomVault()
	vault.Coins = common.Coins{
		common.NewCoin(common.ETHAsset, cosmos.NewUint(common.One)),
		common.NewCoin(usdt, cosmos.NewUint(common.One)),
		common.NewCoin(usdc, cosmos.ZeroUint()),
		common.NewCoin(common.BTCAsset, cosmos.NewUint(common.One)),
	}
	c.Check(nonGasTokenCount(vault, common.ETHChain), Equals, uint64(1))
	c.Check(nonGasTokenCount(vault, common.BTCChain), Equals, uint64(0))
	c.Check(nonGasTokenCount(vault, common.TRONChain), Equals, uint64(0))
}

// TestPrepareTxOutItemCountsPendingGasOutbounds proves that with
// VaultTokenGasReserve on, a token outbound's gas check subtracts the gas asset
// already committed to queued gas-asset outbounds from the same vault, so it
// is not assigned to a vault that will have no gas left to send it.
func (s TxOutStoreSuite) TestPrepareTxOutItemCountsPendingGasOutbounds(c *C) {
	usdt, err := common.NewAsset("ETH.USDT-0XDAC17F958D2EE523A2206206994597C13D831EC7")
	c.Assert(err, IsNil)

	// prepare schedules a USDT outbound with memo for amount and returns its outputs.
	prepare := func(reserve int64, memo string, amount uint64) (Vault, Vault, []TxOutItem) {
		w := getHandlerTestWrapper(c, 1, true, true)
		if reserve > 0 {
			w.keeper.SetMimir(w.ctx, constants.VaultTokenGasReserve.String(), reserve)
		}
		for _, asset := range []common.Asset{common.ETHAsset, usdt} {
			pool := NewPool()
			pool.Asset = asset
			pool.Status = PoolAvailable
			pool.BalanceAsset = cosmos.NewUint(100000 * common.One)
			pool.BalanceRune = cosmos.NewUint(100000 * common.One)
			pool.LPUnits = cosmos.NewUint(100000 * common.One)
			c.Assert(w.keeper.SetPool(w.ctx, pool), IsNil)
		}

		// drained has more USDT, so it is tried first, but all its ETH is
		// committed to a queued ETH outbound.
		drained := GetRandomVault()
		drained.Type = AsgardVault
		drained.Status = ActiveVault
		drained.Coins = common.Coins{
			common.NewCoin(common.ETHAsset, cosmos.NewUint(10*common.One)),
			common.NewCoin(usdt, cosmos.NewUint(1000*common.One)),
		}
		c.Assert(w.keeper.SetVault(w.ctx, drained), IsNil)
		funded := GetRandomVault()
		funded.Type = AsgardVault
		funded.Status = ActiveVault
		funded.Coins = common.Coins{
			common.NewCoin(common.ETHAsset, cosmos.NewUint(10*common.One)),
			common.NewCoin(usdt, cosmos.NewUint(600*common.One)),
		}
		c.Assert(w.keeper.SetVault(w.ctx, funded), IsNil)
		// Any active vault left by the wrapper must not take the outbound.
		vaults, err := w.keeper.GetAsgardVaultsByStatus(w.ctx, ActiveVault)
		c.Assert(err, IsNil)
		for _, v := range vaults {
			if !v.PubKey.Equals(drained.PubKey) && !v.PubKey.Equals(funded.PubKey) {
				v.Status = InactiveVault
				c.Assert(w.keeper.SetVault(w.ctx, v), IsNil)
			}
		}

		c.Assert(w.keeper.AppendTxOut(w.ctx, w.ctx.BlockHeight(), TxOutItem{
			Chain:       common.ETHChain,
			ToAddress:   GetRandomETHAddress(),
			VaultPubKey: drained.PubKey,
			InHash:      GetRandomTxHash(),
			Coin:        common.NewCoin(common.ETHAsset, cosmos.NewUint(10*common.One)),
			MaxGas:      common.Gas{common.NewCoin(common.ETHAsset, cosmos.NewUint(common.One))},
		}), IsNil)

		inTxID := GetRandomTxHash()
		w.keeper.SetObservedTxInVoter(w.ctx, NewObservedTxVoter(inTxID, common.ObservedTxs{
			common.ObservedTx{
				Tx:             GetRandomTx(),
				Status:         common.Status_incomplete,
				BlockHeight:    1,
				Signers:        []string{w.activeNodeAccount.NodeAddress.String()},
				FinaliseHeight: 1,
			},
		}))
		store := newTxOutStorage(w.keeper, w.mgr.GetConstants(), w.mgr.EventMgr(), w.mgr.GasMgr())
		outputs, _, err := store.prepareTxOutItem(w.ctx, TxOutItem{
			Chain:     common.ETHChain,
			ToAddress: GetRandomETHAddress(),
			InHash:    inTxID,
			Coin:      common.NewCoin(usdt, cosmos.NewUint(amount*common.One)),
			Memo:      memo,
		})
		c.Assert(err, IsNil)
		return drained, funded, outputs
	}

	// run returns which vault the USDT outbound was assigned to.
	run := func(reserve int64) string {
		drained, funded, outputs := prepare(reserve, "", 600)
		c.Assert(outputs, HasLen, 1)
		switch {
		case outputs[0].VaultPubKey.Equals(drained.PubKey):
			return "drained"
		case outputs[0].VaultPubKey.Equals(funded.PubKey):
			return "funded"
		}
		return outputs[0].VaultPubKey.String()
	}

	// Off: the queued ETH outbound is ignored and the drained vault is chosen.
	c.Check(run(0), Equals, "drained")
	// On: the drained vault has no ETH left after its queue, so the funded vault pays.
	c.Check(run(1), Equals, "funded")

	// On, Ragnarok: selection ignores the Mimir, so the drained vault still
	// pays its USDT. Skipping it would leave a residual that Ragnarok zeroes
	// after the pool was already debited.
	for _, reserve := range []int64{0, 1} {
		ragnarok := NewRagnarokMemo(1).String()
		drained, funded, outputs := prepare(reserve, ragnarok, 1200)
		c.Assert(outputs, HasLen, 2)
		c.Check(outputs[0].VaultPubKey.Equals(drained.PubKey), Equals, true)
		c.Check(outputs[0].Coin.Amount.Equal(cosmos.NewUint(1000*common.One)), Equals, true)
		c.Check(outputs[1].VaultPubKey.Equals(funded.PubKey), Equals, true)
		c.Check(outputs[1].Coin.Amount.Equal(cosmos.NewUint(200*common.One)), Equals, true)
	}
}

// TestDiscoverOutboundsTokenGasReserveFallbackFoldsFragment proves that when
// the reserve-free fallback itself splits off a fragment too small to survive
// the outbound fee, the fragment is not left for prepareTxOutItem to drop: it
// is folded into the largest output, which survives the fee.
func (s TxOutStoreSuite) TestDiscoverOutboundsTokenGasReserveFallbackFoldsFragment(c *C) {
	usdt, err := common.NewAsset("ETH.USDT-0XDAC17F958D2EE523A2206206994597C13D831EC7")
	c.Assert(err, IsNil)
	maxGas := common.NewCoin(common.ETHAsset, cosmos.NewUint(common.One))
	fee := cosmos.NewUint(1000)
	// The token vault can give 99 ETH without the reserve and 89 with it; the
	// gas-only vault 2, so the reserve pass falls short and the fallback runs.
	// Asking for 99 + fee/2 leaves the gas-only vault a fee/2 fragment.
	amount := cosmos.NewUint(99 * common.One).Add(fee.QuoUint64(2))
	ethOut := TxOutItem{
		Chain:     common.ETHChain,
		ToAddress: GetRandomETHAddress(),
		InHash:    GetRandomTxHash(),
		Coin:      common.NewCoin(common.ETHAsset, amount),
	}

	// The fallback allocation on its own splits off the fragment.
	ctx, mgr := setupManagerForTest(c)
	withTokens, gasOnly := tokenGasReserveVaults(c, ctx, mgr, usdt, 3)
	outputs, left := allocateOutbounds(fee, maxGas, ethOut, Vaults{withTokens, gasOnly}, false, 0)
	c.Assert(left.Coin.Amount.IsZero(), Equals, true)
	c.Assert(outputs, HasLen, 2)
	c.Check(outputs[1].Coin.Amount.Equal(fee.QuoUint64(2)), Equals, true)

	// Off: selection is unchanged from before the Mimir existed.
	store := newTxOutStorage(mgr.Keeper(), mgr.GetConstants(), newRecordingEventMgr(), mgr.GasMgr())
	outputs, remaining := store.DiscoverOutbounds(ctx, fee, maxGas, ethOut, Vaults{withTokens, gasOnly})
	c.Assert(remaining.IsZero(), Equals, true)
	c.Assert(outputs, HasLen, 2)

	// On: the fragment is folded into the token vault's output, so the whole
	// amount stays in one output that survives the fee. The vault can pay it
	// (the fee comes out of what it sends), so no security event is raised.
	ctx, mgr = setupManagerForTest(c)
	mgr.Keeper().SetMimir(ctx, constants.VaultTokenGasReserve.String(), 10)
	withTokens, gasOnly = tokenGasReserveVaults(c, ctx, mgr, usdt, 3)
	eventMgr := newRecordingEventMgr()
	store = newTxOutStorage(mgr.Keeper(), mgr.GetConstants(), eventMgr, mgr.GasMgr())
	outputs, remaining = store.DiscoverOutbounds(ctx, fee, maxGas, ethOut, Vaults{withTokens, gasOnly})
	c.Assert(remaining.IsZero(), Equals, true)
	c.Assert(outputs, HasLen, 1)
	c.Check(outputs[0].VaultPubKey.Equals(withTokens.PubKey), Equals, true)
	c.Check(outputs[0].Coin.Amount.Equal(amount), Equals, true)
	c.Check(eventMgr.securityEvents(), HasLen, 0)
}

func (s TxOutStoreSuite) TestFoldUnpayableFragments(c *C) {
	fee := cosmos.NewUint(1000)
	dust := common.ETHChain.DustThreshold()
	out := func(amount cosmos.Uint) TxOutItem {
		return TxOutItem{Coin: common.NewCoin(common.ETHAsset, amount)}
	}
	big := out(cosmos.NewUint(common.One))
	payable := out(fee.Add(dust))
	tiny := out(fee)

	// A single output is never folded, however small.
	outputs, remaining := foldUnpayableFragments([]TxOutItem{tiny}, cosmos.ZeroUint(), fee, dust)
	c.Check(outputs, HasLen, 1)
	c.Check(remaining.IsZero(), Equals, true)

	// Payable outputs are kept as they are.
	outputs, remaining = foldUnpayableFragments([]TxOutItem{big, payable}, cosmos.NewUint(7), fee, dust)
	c.Check(outputs, HasLen, 2)
	c.Check(remaining.Equal(cosmos.NewUint(7)), Equals, true)

	// Fragments are folded into the largest output, wherever it sits.
	outputs, remaining = foldUnpayableFragments([]TxOutItem{payable, tiny, big, tiny}, cosmos.ZeroUint(), fee, dust)
	c.Assert(outputs, HasLen, 2)
	c.Check(outputs[0].Coin.Amount.Equal(payable.Coin.Amount), Equals, true)
	c.Check(outputs[1].Coin.Amount.Equal(big.Coin.Amount.Add(fee.MulUint64(2))), Equals, true)
	c.Check(remaining.IsZero(), Equals, true)
	// The input is not modified.
	c.Check(big.Coin.Amount.Equal(cosmos.NewUint(common.One)), Equals, true)

	// All fragments: nothing is kept and everything goes back to remaining.
	outputs, remaining = foldUnpayableFragments([]TxOutItem{tiny, tiny}, cosmos.ZeroUint(), fee, dust)
	c.Check(outputs, HasLen, 0)
	c.Check(remaining.Equal(fee.MulUint64(2)), Equals, true)
}

// TestPrepareTxOutItemReserveFallbackKeepsFragment is the end-to-end case: with
// VaultTokenGasReserve on, gas committed to a queued token outbound lowers the
// token vault's ETH, so the reserve-free fallback for an ETH outbound spills a
// sub-fee fragment onto a second vault. That fragment must not be dropped after
// the inbound was debited: every unit of the outbound is either sent or taken
// as the fee.
func (s TxOutStoreSuite) TestPrepareTxOutItemReserveFallbackKeepsFragment(c *C) {
	usdt, err := common.NewAsset("ETH.USDT-0XDAC17F958D2EE523A2206206994597C13D831EC7")
	c.Assert(err, IsNil)

	run := func(reserve int64) (Vault, cosmos.Uint, cosmos.Uint, []TxOutItem) {
		w := getHandlerTestWrapper(c, 1, true, true)
		if reserve > 0 {
			w.keeper.SetMimir(w.ctx, constants.VaultTokenGasReserve.String(), reserve)
		}
		for _, asset := range []common.Asset{common.ETHAsset, usdt} {
			pool := NewPool()
			pool.Asset = asset
			pool.Status = PoolAvailable
			pool.BalanceAsset = cosmos.NewUint(100000 * common.One)
			pool.BalanceRune = cosmos.NewUint(100000 * common.One)
			pool.LPUnits = cosmos.NewUint(100000 * common.One)
			c.Assert(w.keeper.SetPool(w.ctx, pool), IsNil)
		}
		maxGas, _, err := w.mgr.GasMgr().GetGasDetails(w.ctx, common.ETHChain)
		c.Assert(err, IsNil)
		fee, err := w.mgr.GasMgr().GetAssetOutboundFee(w.ctx, common.ETHAsset, false)
		c.Assert(err, IsNil)
		c.Assert(fee.QuoUint64(2).LT(maxGas.Amount), Equals, true)

		withTokens := GetRandomVault()
		withTokens.Type = AsgardVault
		withTokens.Status = ActiveVault
		withTokens.Coins = common.Coins{
			common.NewCoin(common.ETHAsset, cosmos.NewUint(15*common.One)),
			common.NewCoin(usdt, cosmos.NewUint(1000*common.One)),
		}
		c.Assert(w.keeper.SetVault(w.ctx, withTokens), IsNil)
		// The gas-only vault can pay a fragment, but not the reserve shortfall.
		gasOnly := GetRandomVault()
		gasOnly.Type = AsgardVault
		gasOnly.Status = ActiveVault
		gasOnly.Coins = common.Coins{
			common.NewCoin(common.ETHAsset, maxGas.Amount.MulUint64(5)),
		}
		c.Assert(w.keeper.SetVault(w.ctx, gasOnly), IsNil)
		vaults, err := w.keeper.GetAsgardVaultsByStatus(w.ctx, ActiveVault)
		c.Assert(err, IsNil)
		for _, v := range vaults {
			if !v.PubKey.Equals(withTokens.PubKey) && !v.PubKey.Equals(gasOnly.PubKey) {
				v.Status = InactiveVault
				c.Assert(w.keeper.SetVault(w.ctx, v), IsNil)
			}
		}

		// A queued USDT outbound from the token vault commits MaxGas of its ETH.
		c.Assert(w.keeper.AppendTxOut(w.ctx, w.ctx.BlockHeight(), TxOutItem{
			Chain:       common.ETHChain,
			ToAddress:   GetRandomETHAddress(),
			VaultPubKey: withTokens.PubKey,
			InHash:      GetRandomTxHash(),
			Coin:        common.NewCoin(usdt, cosmos.NewUint(100*common.One)),
			MaxGas:      common.Gas{maxGas},
		}), IsNil)

		// Without the reserve the token vault can give 15 - 2 x MaxGas; ask for
		// half a fee more than that.
		amount := common.SafeSub(cosmos.NewUint(15*common.One), maxGas.Amount.MulUint64(2)).Add(fee.QuoUint64(2))
		inTxID := GetRandomTxHash()
		w.keeper.SetObservedTxInVoter(w.ctx, NewObservedTxVoter(inTxID, common.ObservedTxs{
			common.ObservedTx{
				Tx:             GetRandomTx(),
				Status:         common.Status_incomplete,
				BlockHeight:    1,
				Signers:        []string{w.activeNodeAccount.NodeAddress.String()},
				FinaliseHeight: 1,
			},
		}))
		store := newTxOutStorage(w.keeper, w.mgr.GetConstants(), w.mgr.EventMgr(), w.mgr.GasMgr())
		outputs, _, err := store.prepareTxOutItem(w.ctx, TxOutItem{
			Chain:     common.ETHChain,
			ToAddress: GetRandomETHAddress(),
			InHash:    inTxID,
			Coin:      common.NewCoin(common.ETHAsset, amount),
			Memo:      NewOutboundMemo(inTxID).String(),
		})
		c.Assert(err, IsNil)
		return withTokens, amount, fee, outputs
	}

	for _, reserve := range []int64{0, 10} {
		withTokens, amount, fee, outputs := run(reserve)
		c.Assert(outputs, HasLen, 1, Commentf("reserve %d", reserve))
		c.Check(outputs[0].VaultPubKey.Equals(withTokens.PubKey), Equals, true, Commentf("reserve %d", reserve))
		c.Check(outputs[0].Coin.Amount.Add(fee).Equal(amount), Equals, true,
			Commentf("reserve %d: sent %s + fee %s != %s", reserve, outputs[0].Coin.Amount, fee, amount))
	}
}
