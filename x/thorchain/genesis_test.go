package thorchain

import (
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/btcsuite/btcutil/bech32"
	"github.com/cosmos/cosmos-sdk/runtime"
	"gitlab.com/thorchain/thornode/v3/cmd"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/common/tcysmartcontract"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

type GenesisTestSuite struct{}

var _ = Suite(&GenesisTestSuite{})

var genesisFixtureBech32 = regexp.MustCompile(`thor[a-z]*1[0-9a-z]+`)

func normalizeGenesisFixtureBech32(input string) string {
	return genesisFixtureBech32.ReplaceAllStringFunc(input, func(raw string) string {
		_, data, err := bech32.DecodeNoLimit(raw)
		if err != nil {
			return raw
		}

		targetPrefix := cmd.Bech32PrefixAccAddr
		switch {
		case strings.HasPrefix(raw, "thorcpub1"):
			targetPrefix = cmd.Bech32PrefixConsPub
		case strings.HasPrefix(raw, "thorpub1"):
			targetPrefix = cmd.Bech32PrefixAccPub
		case strings.HasPrefix(raw, "thorc1"):
			targetPrefix = cmd.Bech32PrefixConsAddr
		}

		normalized, err := bech32.Encode(targetPrefix, data)
		if err != nil {
			return raw
		}
		return normalized
	})
}

func (GenesisTestSuite) TestGenesis(c *C) {
	SetupConfigForTest()
	genesisState := DefaultGenesisState()
	c.Assert(ValidateGenesis(genesisState), IsNil)
	ctx, mgr := setupManagerForTest(c)
	gs := ExportGenesis(ctx, mgr.Keeper())
	c.Assert(ValidateGenesis(gs), IsNil)
	content, err := os.ReadFile("../../test/fixtures/genesis/genesis.json")
	c.Assert(err, IsNil)
	c.Assert(content, NotNil)
	normalizedContent := normalizeGenesisFixtureBech32(string(content))
	ctx, mgr = setupManagerForTest(c)
	var state GenesisState
	c.Assert(ModuleCdc.UnmarshalJSON([]byte(normalizedContent), &state), IsNil)
	result := InitGenesis(ctx, mgr.Keeper(), state)
	c.Assert(result, NotNil)
	gs1 := ExportGenesis(ctx, mgr.Keeper())
	c.Assert(len(gs1.Pools) > 0, Equals, true)
}

func (GenesisTestSuite) TestGenesisPOLReserveDepositRoundTrip(c *C) {
	SetupConfigForTest()

	srcCtx, srcMgr := setupManagerForTest(c)
	usdc, err := common.NewAsset("ETH.USDC-0XA0B86991C6218B36C1D19D4A2E9EB0CE3606EB48")
	c.Assert(err, IsNil)

	btcDeposit := NewPOLReserveDeposit(common.BTCAsset)
	btcDeposit.RuneDeposited = cosmos.NewUint(12345 * common.One)
	c.Assert(srcMgr.Keeper().SetPOLReserveDeposit(srcCtx, btcDeposit), IsNil)

	usdcDeposit := NewPOLReserveDeposit(usdc)
	usdcDeposit.RuneDeposited = cosmos.NewUint(678 * common.One)
	c.Assert(srcMgr.Keeper().SetPOLReserveDeposit(srcCtx, usdcDeposit), IsNil)

	exported := ExportGenesis(srcCtx, srcMgr.Keeper())
	found := map[string]cosmos.Uint{}
	for _, prd := range exported.PolReserveDeposits {
		found[prd.Asset.String()] = prd.RuneDeposited
	}
	c.Check(found[common.BTCAsset.String()].String(), Equals, btcDeposit.RuneDeposited.String())
	c.Check(found[usdc.String()].String(), Equals, usdcDeposit.RuneDeposited.String())

	dstCtx, dstMgr := setupManagerForTest(c)
	InitGenesis(dstCtx, dstMgr.Keeper(), exported)

	gotBTC, err := dstMgr.Keeper().GetPOLReserveDeposit(dstCtx, common.BTCAsset)
	c.Assert(err, IsNil)
	c.Check(gotBTC.Asset.Equals(common.BTCAsset), Equals, true)
	c.Check(gotBTC.RuneDeposited.String(), Equals, btcDeposit.RuneDeposited.String())

	gotUSDC, err := dstMgr.Keeper().GetPOLReserveDeposit(dstCtx, usdc)
	c.Assert(err, IsNil)
	c.Check(gotUSDC.Asset.Equals(usdc), Equals, true)
	c.Check(gotUSDC.RuneDeposited.String(), Equals, usdcDeposit.RuneDeposited.String())
}

func (GenesisTestSuite) TestGenesisInactiveAsgardDeficitRoundTrip(c *C) {
	SetupConfigForTest()

	srcCtx, srcMgr := setupManagerForTest(c)
	deficitVault := GetRandomVault()
	deficitVault.Status = InactiveVault
	deficitVault.StatusSince = srcCtx.BlockHeight()
	deficitVault.Coins = common.Coins{}
	deficitVault.Deficit = common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(12345)))
	c.Assert(srcMgr.Keeper().SetVault(srcCtx, deficitVault), IsNil)
	c.Assert(srcMgr.Keeper().RemoveFromAsgardIndex(srcCtx, deficitVault.PubKey), IsNil)
	initDeficitVault := GetRandomVault()
	initDeficitVault.Status = InitVault
	initDeficitVault.StatusSince = srcCtx.BlockHeight()
	initDeficitVault.Coins = common.Coins{}
	initDeficitVault.Deficit = common.NewCoins(common.NewCoin(common.BTCAsset, cosmos.NewUint(67890)))
	c.Assert(srcMgr.Keeper().SetVault(srcCtx, initDeficitVault), IsNil)
	c.Assert(srcMgr.Keeper().RemoveFromAsgardIndex(srcCtx, initDeficitVault.PubKey), IsNil)

	abandonedVault := GetRandomVault()
	abandonedVault.Status = InactiveVault
	abandonedVault.Coins = common.Coins{}
	abandonedVault.Deficit = common.Coins{}
	c.Assert(srcMgr.Keeper().SetVault(srcCtx, abandonedVault), IsNil)
	c.Assert(srcMgr.Keeper().RemoveFromAsgardIndex(srcCtx, abandonedVault.PubKey), IsNil)

	exported := ExportGenesis(srcCtx, srcMgr.Keeper())
	findVault := func(vaults Vaults, pubKey common.PubKey) (Vault, bool) {
		for _, vault := range vaults {
			if vault.PubKey.Equals(pubKey) {
				return vault, true
			}
		}
		return Vault{}, false
	}

	exportedDeficitVault, found := findVault(exported.Vaults, deficitVault.PubKey)
	c.Assert(found, Equals, true)
	c.Check(exportedDeficitVault.GetDeficit(common.ETHAsset).Amount.Equal(cosmos.NewUint(12345)), Equals, true)
	exportedInitDeficitVault, found := findVault(exported.Vaults, initDeficitVault.PubKey)
	c.Assert(found, Equals, true)
	c.Check(exportedInitDeficitVault.GetDeficit(common.BTCAsset).Amount.Equal(cosmos.NewUint(67890)), Equals, true)
	_, found = findVault(exported.Vaults, abandonedVault.PubKey)
	c.Check(found, Equals, false)

	dstCtx, dstMgr := setupManagerForTest(c)
	InitGenesis(dstCtx, dstMgr.Keeper(), exported)

	restored, err := dstMgr.Keeper().GetVault(dstCtx, deficitVault.PubKey)
	c.Assert(err, IsNil)
	c.Check(restored.Status, Equals, InactiveVault)
	c.Check(restored.GetDeficit(common.ETHAsset).Amount.Equal(cosmos.NewUint(12345)), Equals, true)
	restoredInit, err := dstMgr.Keeper().GetVault(dstCtx, initDeficitVault.PubKey)
	c.Assert(err, IsNil)
	c.Check(restoredInit.Status, Equals, InitVault)
	c.Check(restoredInit.GetDeficit(common.BTCAsset).Amount.Equal(cosmos.NewUint(67890)), Equals, true)

	reExported := ExportGenesis(dstCtx, dstMgr.Keeper())
	reExportedDeficitVault, found := findVault(reExported.Vaults, deficitVault.PubKey)
	c.Assert(found, Equals, true)
	c.Check(reExportedDeficitVault.GetDeficit(common.ETHAsset).Amount.Equal(cosmos.NewUint(12345)), Equals, true)
	reExportedInitDeficitVault, found := findVault(reExported.Vaults, initDeficitVault.PubKey)
	c.Assert(found, Equals, true)
	c.Check(reExportedInitDeficitVault.GetDeficit(common.BTCAsset).Amount.Equal(cosmos.NewUint(67890)), Equals, true)
}

// TestGenesisStableReserveCreditRoundTrip seeds per-contributor stable reserve
// credits, exports them, and imports into a fresh keeper. Without genesis
// wiring the credits are lost on a state export/import and every prior
// contributor's STABLE- withdrawal would fail with "insufficient credit".
func (GenesisTestSuite) TestGenesisStableReserveCreditRoundTrip(c *C) {
	SetupConfigForTest()

	srcCtx, srcMgr := setupManagerForTest(c)
	ethAddr, err := common.NewAddress("0x3021c479f7f8c9f1d5c7d8523ba5e22c0bcb5430")
	c.Assert(err, IsNil)
	btcAddr, err := common.NewAddress("bc1qar0srrr7xfkvy5l643lydnw9re59gtzzwf5mdq")
	c.Assert(err, IsNil)
	// A case-sensitive base58check (TRON) address: uppercasing it — as the store
	// key does — corrupts the checksum, so it can only survive export/import if the
	// original case is preserved in the value.
	tronAddr, err := common.NewAddress("TWcDDx1Q6QEoBrJi9qehtZnD4vcXXuVLer")
	c.Assert(err, IsNil)

	c.Assert(srcMgr.Keeper().SetStableReserveCredit(srcCtx, common.ETHAsset, ethAddr, cosmos.NewUint(1234*common.One)), IsNil)
	c.Assert(srcMgr.Keeper().SetStableReserveCredit(srcCtx, common.BTCAsset, btcAddr, cosmos.NewUint(5678)), IsNil)
	c.Assert(srcMgr.Keeper().SetStableReserveCredit(srcCtx, common.TRXAsset, tronAddr, cosmos.NewUint(9012)), IsNil)

	exported := ExportGenesis(srcCtx, srcMgr.Keeper())
	c.Assert(len(exported.StableReserveCredits), Equals, 3)

	// Exercise the binary proto round-trip explicitly so the marshal/unmarshal
	// path for the new field is covered, not just the in-memory export.
	bz, err := exported.Marshal()
	c.Assert(err, IsNil)
	var decoded GenesisState
	c.Assert(decoded.Unmarshal(bz), IsNil)
	c.Assert(len(decoded.StableReserveCredits), Equals, 3)

	dstCtx, dstMgr := setupManagerForTest(c)
	InitGenesis(dstCtx, dstMgr.Keeper(), decoded)

	gotETH, err := dstMgr.Keeper().GetStableReserveCredit(dstCtx, common.ETHAsset, ethAddr)
	c.Assert(err, IsNil)
	c.Check(gotETH.String(), Equals, cosmos.NewUint(1234*common.One).String())

	gotBTC, err := dstMgr.Keeper().GetStableReserveCredit(dstCtx, common.BTCAsset, btcAddr)
	c.Assert(err, IsNil)
	c.Check(gotBTC.String(), Equals, cosmos.NewUint(5678).String())

	// The base58check address must survive with its original case intact so the
	// credit is still keyed to (and withdrawable by) the real contributor.
	c.Check(exportedAddress(exported, common.TRXAsset), Equals, "TWcDDx1Q6QEoBrJi9qehtZnD4vcXXuVLer")
	gotTRON, err := dstMgr.Keeper().GetStableReserveCredit(dstCtx, common.TRXAsset, tronAddr)
	c.Assert(err, IsNil)
	c.Check(gotTRON.String(), Equals, cosmos.NewUint(9012).String())
}

// exportedAddress returns the exported stable-reserve credit address for a given
// layer-1 asset, or "" if absent.
func exportedAddress(g GenesisState, asset common.Asset) string {
	for _, c := range g.StableReserveCredits {
		if a, err := common.NewAsset(c.Asset); err == nil && a.Equals(asset) {
			return c.Address
		}
	}
	return ""
}

func (GenesisTestSuite) TestGenesisStagedOutboundDoesNotInferMempoolReservation(c *C) {
	SetupConfigForTest()

	srcCtx, srcMgr := setupManagerForTest(c)
	srcCtx = srcCtx.WithBlockHeight(42)
	inHash := GetRandomTxHash()
	cloutSpent := cosmos.NewUint(17)
	item := TxOutItem{
		Chain:       common.THORChain,
		ToAddress:   GetRandomTHORAddress(),
		VaultPubKey: GetRandomPubKey(),
		Coin:        common.NewCoin(common.RuneAsset(), cosmos.NewUint(25*common.One)),
		Memo:        "OUT:" + inHash.String(),
		InHash:      inHash,
		CloutSpent:  &cloutSpent,
	}
	c.Assert(srcMgr.Keeper().SetStagedOutbound(srcCtx, item, 100), IsNil)

	reservedTOR := cosmos.NewUint(1234)
	voter := NewObservedTxVoter(inHash, ObservedTxs{})
	voter.Actions = []TxOutItem{item}
	voter.MempoolSwapTOR = reservedTOR
	srcMgr.Keeper().SetObservedTxInVoter(srcCtx, voter)
	srcMgr.Keeper().SetMempoolSwapUsageTOR(srcCtx, reservedTOR)

	exported := ExportGenesis(srcCtx, srcMgr.Keeper())
	c.Assert(exported.StagedOutbounds, HasLen, 1)
	c.Assert(exported.StagedOutbounds[0].Items, HasLen, 1)
	c.Check(exported.StagedOutbounds[0].Items[0].StagedHeight, Equals, int64(42))

	dstCtx, dstMgr := setupManagerForTest(c)
	dstCtx = dstCtx.WithBlockHeight(500)
	InitGenesis(dstCtx, dstMgr.Keeper(), exported)

	restored, err := dstMgr.Keeper().GetStagedOutbounds(dstCtx, inHash)
	c.Assert(err, IsNil)
	c.Assert(restored, HasLen, 1)
	c.Check(restored[0].StagedHeight, Equals, int64(42))
	c.Check(restored[0].ScheduledHeight, Equals, int64(100))
	c.Check(dstMgr.Keeper().GetMempoolSwapUsageTOR(dstCtx).IsZero(), Equals, true,
		Commentf("v3.21 derives reservations only from canonical lifecycle records"))
	c.Check(dstMgr.Keeper().GetMempoolSwapCount(dstCtx).IsZero(), Equals, true)

	runeValue, cloutValue, err := dstMgr.Keeper().GetStagedOutboundValue(dstCtx)
	c.Assert(err, IsNil)
	c.Check(runeValue.Equal(item.Coin.Amount), Equals, true)
	c.Check(cloutValue.Equal(cloutSpent), Equals, true)
}

func newGenesisMempoolSwap(vault common.PubKey) types.MempoolSwap {
	tx := GetRandomTx()
	msg := NewMsgSwap(
		tx,
		common.BTCAsset,
		GetRandomBTCAddress(),
		cosmos.ZeroUint(),
		common.NoAddress,
		cosmos.ZeroUint(),
		"", "", nil,
		types.SwapType_market,
		1, 0,
		types.SwapVersion_v2,
		GetRandomBech32Addr(),
	)
	msg.Index = 0
	return types.MempoolSwap{
		TxId:               tx.ID,
		Index:              msg.Index,
		VaultPubKey:        vault,
		ReservedTor:        cosmos.NewUint(1234),
		AdmittedHeight:     40,
		UpdatedHeight:      42,
		NextRecoveryHeight: 90,
		Status:             types.MempoolSwapStatus_EXECUTING,
		LatestSwap:         *msg,
		PendingEffects: types.MempoolSwapPendingEffects{
			Pools: []types.MempoolSwapPoolEffect{{
				Asset:                common.ETHAsset,
				LiquidityFeeRune:     cosmos.NewUint(10),
				SwapSlip:             cosmos.NewUint(11),
				VolumeRune:           cosmos.NewUint(12),
				VolumeAsset:          cosmos.NewUint(13),
				ReserveRune:          cosmos.NewUint(14),
				TcyRune:              cosmos.NewUint(15),
				BalanceRuneIncrease:  cosmos.NewUint(21),
				BalanceRuneDecrease:  cosmos.NewUint(22),
				BalanceAssetIncrease: cosmos.NewUint(23),
				BalanceAssetDecrease: cosmos.NewUint(24),
			}},
			Clout: []types.MempoolSwapCloutEffect{{
				Address: GetRandomTHORAddress(),
				Score:   cosmos.NewUint(16),
			}},
			Affiliates: []types.MempoolSwapAffiliateEffect{{
				Thorname: "affiliate",
				FeeRune:  cosmos.NewUint(17),
			}},
			DynamicFees: []types.MempoolSwapDynamicFeeEffect{{
				Thorname:   "affiliate",
				PairAssetA: common.ETHAsset,
				PairAssetB: common.BTCAsset,
				VolumeTor:  cosmos.NewUint(18),
				FeesTor:    cosmos.NewUint(19),
			}},
			Cleanups: []types.MempoolSwapCleanupEffect{{
				Item: TxOutItem{
					Chain:     common.ETHChain,
					InHash:    tx.ID,
					ToAddress: GetRandomETHAddress(),
					Coin:      common.NewCoin(common.ETHAsset, cosmos.NewUint(20)),
				},
				BurnReason: "failed_outbound",
				CleanupKey: "genesis-cleanup-test",
			}},
			Supplies: []types.MempoolSwapSupplyEffect{{
				Asset:          common.BTCAsset.GetSyntheticAsset(),
				SupplyIncrease: cosmos.NewUint(25),
				SupplyDecrease: cosmos.NewUint(26),
			}},
		},
	}
}

func rawGenesisMempoolSwapKey(record types.MempoolSwap) []byte {
	return []byte(fmt.Sprintf("mempool_swap//%s", canonicalGenesisMempoolSwapIdentity(record)))
}

func rawGenesisObservedTxInKey(txID common.TxID) []byte {
	return []byte(fmt.Sprintf("observed_tx_in//%s", canonicalGenesisTxID(txID)))
}

func (GenesisTestSuite) TestGenesisMempoolSwapLifecycleRoundTrip(c *C) {
	SetupConfigForTest()

	srcCtx, srcMgr := setupManagerForTest(c)
	srcCtx = srcCtx.WithBlockHeight(42)
	vault := GetRandomVault()
	c.Assert(srcMgr.Keeper().SetVault(srcCtx, vault), IsNil)
	record := newGenesisMempoolSwap(vault.PubKey)
	c.Assert(srcMgr.Keeper().SetMempoolSwap(srcCtx, record), IsNil)

	// The lifecycle record is discoverable even after queue/staged ownership is
	// gone. Its voter must still be included by ExportGenesis.
	voter := NewObservedTxVoter(record.TxId, ObservedTxs{})
	voter.MempoolSwapTOR = record.ReservedTor
	srcMgr.Keeper().SetObservedTxInVoter(srcCtx, voter)
	srcMgr.Keeper().SetMempoolSwapUsageTOR(srcCtx, cosmos.NewUint(1))

	exported := ExportGenesis(srcCtx, srcMgr.Keeper())
	c.Assert(exported.MempoolSwaps, HasLen, 1)
	c.Check(exported.MempoolSwaps[0].Cursor(), Equals, record.Cursor())
	foundVoter := false
	for _, exportedVoter := range exported.ObservedTxInVoters {
		if exportedVoter.TxID.Equals(record.TxId) {
			foundVoter = true
			break
		}
	}
	c.Check(foundVoter, Equals, true)
	c.Assert(ValidateGenesis(exported), IsNil)

	// Exercise the generated binary field as well as in-memory export/import.
	bz, err := exported.Marshal()
	c.Assert(err, IsNil)
	var decoded GenesisState
	c.Assert(decoded.Unmarshal(bz), IsNil)
	c.Assert(decoded.MempoolSwaps, HasLen, 1)

	dstCtx, dstMgr := setupManagerForTest(c)
	InitGenesis(dstCtx, dstMgr.Keeper(), decoded)
	restored, found, err := dstMgr.Keeper().GetMempoolSwap(dstCtx, record.TxId, record.Index)
	c.Assert(err, IsNil)
	c.Assert(found, Equals, true)
	c.Check(restored.Status, Equals, types.MempoolSwapStatus_EXECUTING)
	c.Check(restored.PendingEffects.Affiliates[0].FeeRune.Equal(cosmos.NewUint(17)), Equals, true)
	c.Check(restored.PendingEffects.DynamicFees[0].VolumeTor.Equal(cosmos.NewUint(18)), Equals, true)
	c.Check(restored.PendingEffects.Cleanups[0].CleanupKey, Equals, "genesis-cleanup-test")
	c.Check(restored.PendingEffects.Supplies[0].SupplyIncrease.Equal(cosmos.NewUint(25)), Equals, true)
	c.Check(dstMgr.Keeper().GetMempoolSwapUsageTOR(dstCtx).Equal(record.ReservedTor), Equals, true,
		Commentf("usage must be derived from authoritative lifecycle records, not voter markers"))
	c.Check(dstMgr.Keeper().GetMempoolSwapCount(dstCtx).Equal(cosmos.OneUint()), Equals, true)
	restoredVoter, err := dstMgr.Keeper().GetObservedTxInVoter(dstCtx, record.TxId)
	c.Assert(err, IsNil)
	c.Check(restoredVoter.MempoolSwapTOR.Equal(record.ReservedTor), Equals, true)
	overlay, err := dstMgr.Keeper().GetMempoolSwapPoolBalanceOverlay(dstCtx, common.ETHAsset)
	c.Assert(err, IsNil)
	c.Check(overlay.BalanceRuneIncrease.Equal(cosmos.NewUint(21)), Equals, true)
	c.Check(overlay.BalanceRuneDecrease.Equal(cosmos.NewUint(22)), Equals, true)
	c.Check(overlay.BalanceAssetIncrease.Equal(cosmos.NewUint(23)), Equals, true)
	c.Check(overlay.BalanceAssetDecrease.Equal(cosmos.NewUint(24)), Equals, true)
	supplyOverlay, err := dstMgr.Keeper().GetMempoolSwapNativeSupplyOverlay(dstCtx, common.BTCAsset.GetSyntheticAsset())
	c.Assert(err, IsNil)
	c.Check(supplyOverlay.SupplyIncrease.Equal(cosmos.NewUint(25)), Equals, true)
	c.Check(supplyOverlay.SupplyDecrease.Equal(cosmos.NewUint(26)), Equals, true)

	byVault, err := dstMgr.Keeper().GetMempoolSwapsByVault(dstCtx, vault.PubKey, 10)
	c.Assert(err, IsNil)
	c.Assert(byVault, HasLen, 1)
	due, err := dstMgr.Keeper().GetMempoolSwapsDue(dstCtx, record.NextRecoveryHeight, 10)
	c.Assert(err, IsNil)
	c.Assert(due, HasLen, 1)

	reExported := ExportGenesis(dstCtx, dstMgr.Keeper())
	c.Assert(reExported.MempoolSwaps, HasLen, 1)
	c.Check(reExported.MempoolSwaps[0].Cursor(), Equals, record.Cursor())
}

func (GenesisTestSuite) TestGenesisRejectsDuplicateMempoolSwapLifecycle(c *C) {
	state := DefaultGenesisState()
	record := newGenesisMempoolSwap(GetRandomPubKey())
	duplicate := record
	duplicate.TxId = common.TxID(strings.ToLower(record.TxId.String()))
	duplicate.LatestSwap.Tx.ID = duplicate.TxId
	state.MempoolSwaps = []types.MempoolSwap{record, duplicate}
	err := ValidateGenesis(state)
	c.Assert(err, NotNil)
	c.Check(err.Error(), Matches, "duplicate mempool swap identity:.*")
}

func (GenesisTestSuite) TestGenesisRejectsMultipleMempoolSwapPoolOwners(c *C) {
	recordA := newGenesisMempoolSwap(GetRandomPubKey())
	recordB := newGenesisMempoolSwap(GetRandomPubKey())
	voterA := NewObservedTxVoter(recordA.TxId, nil)
	voterA.UnfinalizedHeight = recordA.AdmittedHeight
	voterA.MempoolSwapTOR = recordA.ReservedTor
	voterB := NewObservedTxVoter(recordB.TxId, nil)
	voterB.UnfinalizedHeight = recordB.AdmittedHeight
	voterB.MempoolSwapTOR = recordB.ReservedTor

	state := DefaultGenesisState()
	state.MempoolSwaps = []types.MempoolSwap{recordA, recordB}
	state.ObservedTxInVoters = ObservedTxVoters{voterA, voterB}
	err := ValidateGenesis(state)
	c.Assert(err, NotNil)
	c.Check(err.Error(), Matches, "mempool swap pool .* has multiple lifecycle owners:.*")
}

func (GenesisTestSuite) TestGenesisRejectsFinalizedTombstonePoolOwnerConflict(c *C) {
	tombstone := newGenesisMempoolSwap(GetRandomPubKey())
	tombstone.Status = types.MempoolSwapStatus_FINALIZED
	tombstone.ReservedTor = cosmos.ZeroUint()
	active := newGenesisMempoolSwap(GetRandomPubKey())

	tombstoneVoter := NewObservedTxVoter(tombstone.TxId, nil)
	tombstoneVoter.FinalisedHeight = tombstone.UpdatedHeight
	tombstoneVoter.MempoolSwapTOR = cosmos.ZeroUint()
	activeVoter := NewObservedTxVoter(active.TxId, nil)
	activeVoter.UnfinalizedHeight = active.AdmittedHeight
	activeVoter.MempoolSwapTOR = active.ReservedTor

	state := DefaultGenesisState()
	state.MempoolSwaps = []types.MempoolSwap{tombstone, active}
	state.ObservedTxInVoters = ObservedTxVoters{tombstoneVoter, activeVoter}
	err := ValidateGenesis(state)
	c.Assert(err, NotNil)
	c.Check(err.Error(), Matches, "mempool swap pool .* has multiple lifecycle owners:.*")
}

func (GenesisTestSuite) TestGenesisRestoresFinalizedTombstonePoolOwner(c *C) {
	tombstone := newGenesisMempoolSwap(GetRandomPubKey())
	tombstone.Status = types.MempoolSwapStatus_FINALIZED
	tombstone.ReservedTor = cosmos.ZeroUint()
	voter := NewObservedTxVoter(tombstone.TxId, nil)
	voter.FinalisedHeight = tombstone.UpdatedHeight
	voter.MempoolSwapTOR = cosmos.ZeroUint()

	state := DefaultGenesisState()
	state.MempoolSwaps = []types.MempoolSwap{tombstone}
	state.ObservedTxInVoters = ObservedTxVoters{voter}
	c.Assert(ValidateGenesis(state), IsNil)

	ctx, mgr := setupManagerForTest(c)
	InitGenesis(ctx, mgr.Keeper(), state)
	poolOverlay, err := mgr.Keeper().GetMempoolSwapPoolBalanceOverlay(ctx, common.ETHAsset)
	c.Assert(err, IsNil)
	c.Check(mempoolSwapPoolBalanceOverlayIsZero(poolOverlay), Equals, true)
	supplyOverlay, err := mgr.Keeper().GetMempoolSwapNativeSupplyOverlay(ctx, common.BTCAsset.GetSyntheticAsset())
	c.Assert(err, IsNil)
	c.Check(mempoolSwapNativeSupplyOverlayIsZero(supplyOverlay), Equals, true)

	owner, found, err := mgr.Keeper().GetMempoolSwapPoolOwner(ctx, common.ETHAsset)
	c.Assert(err, IsNil)
	c.Assert(found, Equals, true)
	c.Check(owner.Cursor(), Equals, tombstone.Cursor())
	owner, found, err = mgr.Keeper().GetMempoolSwapPoolOwner(ctx, common.BTCAsset)
	c.Assert(err, IsNil)
	c.Assert(found, Equals, true)
	c.Check(owner.Cursor(), Equals, tombstone.Cursor())
}

func (GenesisTestSuite) TestGenesisValidatesMempoolSwapVoterMirror(c *C) {
	record := newGenesisMempoolSwap(GetRandomPubKey())
	state := DefaultGenesisState()
	state.MempoolSwaps = []types.MempoolSwap{record}

	err := ValidateGenesis(state)
	c.Assert(err, NotNil)
	c.Check(err.Error(), Matches, ".*missing its inbound voter")

	voter := NewObservedTxVoter(record.TxId, nil)
	voter.UnfinalizedHeight = record.AdmittedHeight
	state.ObservedTxInVoters = ObservedTxVoters{voter}
	err = ValidateGenesis(state)
	c.Assert(err, NotNil)
	c.Check(err.Error(), Matches, ".*voter TOR is nil")

	voter.MempoolSwapTOR = record.ReservedTor.Add(cosmos.OneUint())
	state.ObservedTxInVoters = ObservedTxVoters{voter}
	err = ValidateGenesis(state)
	c.Assert(err, NotNil)
	c.Check(err.Error(), Matches, ".*voter TOR .* does not match lifecycle TOR.*")

	voter.MempoolSwapTOR = record.ReservedTor
	state.ObservedTxInVoters = ObservedTxVoters{voter}
	c.Check(ValidateGenesis(state), IsNil)
}

func (GenesisTestSuite) TestInitGenesisNormalizesMempoolSwapVoterMirror(c *C) {
	record := newGenesisMempoolSwap(GetRandomPubKey())
	voter := NewObservedTxVoter(record.TxId, nil)
	voter.UnfinalizedHeight = record.AdmittedHeight
	voter.MempoolSwapTOR = record.ReservedTor.Add(cosmos.OneUint())

	state := DefaultGenesisState()
	state.MempoolSwaps = []types.MempoolSwap{record}
	state.ObservedTxInVoters = ObservedTxVoters{voter}
	c.Assert(ValidateGenesis(state), NotNil,
		Commentf("validation should reject the bad compatibility mirror"))

	ctx, mgr := setupManagerForTest(c)
	InitGenesis(ctx, mgr.Keeper(), state)
	restored, err := mgr.Keeper().GetObservedTxInVoter(ctx, record.TxId)
	c.Assert(err, IsNil)
	c.Check(restored.MempoolSwapTOR.Equal(record.ReservedTor), Equals, true,
		Commentf("direct InitGenesis callers must still restore the canonical lifecycle value"))
}

func (GenesisTestSuite) TestExportGenesisFailsClosedOnInvalidMempoolSwapLifecycle(c *C) {
	ctx, mgr := setupManagerForTest(c)
	store := runtime.KVStoreAdapter(serviceThorchain.OpenKVStore(ctx))
	record := newGenesisMempoolSwap(GetRandomPubKey())
	key := rawGenesisMempoolSwapKey(record)

	invalid := record
	invalid.Status = types.MempoolSwapStatus_UNKNOWN
	bz, err := mgr.Keeper().Cdc().Marshal(&invalid)
	c.Assert(err, IsNil)
	store.Set(key, bz)
	c.Assert(func() { ExportGenesis(ctx, mgr.Keeper()) }, PanicMatches,
		"invalid mempool swap lifecycle record:.*")

	store.Set(key, []byte{0xff})
	c.Assert(func() { ExportGenesis(ctx, mgr.Keeper()) }, PanicMatches,
		"fail to unmarshal mempool swap lifecycle record:.*")
}

func (GenesisTestSuite) TestExportGenesisFailsClosedOnNonCanonicalMempoolSwapKey(c *C) {
	ctx, mgr := setupManagerForTest(c)
	store := runtime.KVStoreAdapter(serviceThorchain.OpenKVStore(ctx))
	record := newGenesisMempoolSwap(GetRandomPubKey())
	bz, err := mgr.Keeper().Cdc().Marshal(&record)
	c.Assert(err, IsNil)
	store.Set([]byte("mempool_swap//WRONG"), bz)

	c.Assert(func() { ExportGenesis(ctx, mgr.Keeper()) }, PanicMatches,
		"mempool swap lifecycle record .* is stored under a non-canonical physical key")
}

func (GenesisTestSuite) TestExportGenesisFailsClosedOnSwappedMempoolSwapKeys(c *C) {
	ctx, mgr := setupManagerForTest(c)
	store := runtime.KVStoreAdapter(serviceThorchain.OpenKVStore(ctx))
	recordA := newGenesisMempoolSwap(GetRandomPubKey())
	recordB := newGenesisMempoolSwap(GetRandomPubKey())
	recordB.TxId = GetRandomTxHash()
	recordB.LatestSwap.Tx.ID = recordB.TxId

	bzA, err := mgr.Keeper().Cdc().Marshal(&recordA)
	c.Assert(err, IsNil)
	bzB, err := mgr.Keeper().Cdc().Marshal(&recordB)
	c.Assert(err, IsNil)
	store.Set(mgr.Keeper().GetMempoolSwapCanonicalKey(recordB.TxId, recordB.Index), bzA)
	store.Set(mgr.Keeper().GetMempoolSwapCanonicalKey(recordA.TxId, recordA.Index), bzB)

	c.Assert(func() { ExportGenesis(ctx, mgr.Keeper()) }, PanicMatches,
		"mempool swap lifecycle record .* is stored under a non-canonical physical key")
}

func (GenesisTestSuite) TestExportGenesisFailsClosedOnDuplicatePhysicalMempoolSwapIdentity(c *C) {
	ctx, mgr := setupManagerForTest(c)
	store := runtime.KVStoreAdapter(serviceThorchain.OpenKVStore(ctx))
	record := newGenesisMempoolSwap(GetRandomPubKey())
	c.Assert(mgr.Keeper().SetMempoolSwap(ctx, record), IsNil)
	bz, err := mgr.Keeper().Cdc().Marshal(&record)
	c.Assert(err, IsNil)
	store.Set([]byte("mempool_swap//WRONG"), bz)

	c.Assert(func() { ExportGenesis(ctx, mgr.Keeper()) }, PanicMatches,
		"duplicate mempool swap lifecycle identity in store:.*")
}

func (GenesisTestSuite) TestExportGenesisFailsClosedOnMissingOrCorruptLifecycleVoter(c *C) {
	missingCtx, missingMgr := setupManagerForTest(c)
	missingRecord := newGenesisMempoolSwap(GetRandomPubKey())
	c.Assert(missingMgr.Keeper().SetMempoolSwap(missingCtx, missingRecord), IsNil)
	c.Assert(func() { ExportGenesis(missingCtx, missingMgr.Keeper()) }, PanicMatches,
		"mempool swap lifecycle record .* is missing its inbound voter")

	corruptCtx, corruptMgr := setupManagerForTest(c)
	corruptRecord := newGenesisMempoolSwap(GetRandomPubKey())
	c.Assert(corruptMgr.Keeper().SetMempoolSwap(corruptCtx, corruptRecord), IsNil)
	corruptStore := runtime.KVStoreAdapter(serviceThorchain.OpenKVStore(corruptCtx))
	corruptStore.Set(rawGenesisObservedTxInKey(corruptRecord.TxId), []byte{0xff})
	c.Assert(func() { ExportGenesis(corruptCtx, corruptMgr.Keeper()) }, PanicMatches,
		"fail to get voter for mempool swap lifecycle record .*:.*")

	mismatchCtx, mismatchMgr := setupManagerForTest(c)
	mismatchRecord := newGenesisMempoolSwap(GetRandomPubKey())
	c.Assert(mismatchMgr.Keeper().SetMempoolSwap(mismatchCtx, mismatchRecord), IsNil)
	mismatchVoter := NewObservedTxVoter(mismatchRecord.TxId, nil)
	mismatchVoter.UnfinalizedHeight = mismatchRecord.AdmittedHeight
	mismatchVoter.MempoolSwapTOR = mismatchRecord.ReservedTor.Add(cosmos.OneUint())
	mismatchMgr.Keeper().SetObservedTxInVoter(mismatchCtx, mismatchVoter)
	c.Assert(func() { ExportGenesis(mismatchCtx, mismatchMgr.Keeper()) }, PanicMatches,
		"mempool swap lifecycle record .* voter TOR .* does not match reserved TOR.*")
}

// TestGenesisDynamicFeeRoundtrip seeds a DynamicFeeRecord, accumulator, and
// last-observed-epoch-blocks value into one keeper, exports the state,
// imports into a fresh keeper, and re-exports. It then asserts the two
// exports are equivalent. Without wiring in ExportGenesis / initGenesis
// the re-export would silently drop the dynamic-fee state — leaving the
// controller blind to its prior direction across chain restarts and
// losing in-flight pre-seal volume.
func (GenesisTestSuite) TestGenesisDynamicFeeRoundtrip(c *C) {
	SetupConfigForTest()
	ctx, mgr := setupManagerForTest(c)
	k := mgr.Keeper()

	rec := types.DynamicFeeRecord{
		Thorname:        "bentley",
		PairAssetA:      "BTC.BTC",
		PairAssetB:      "THOR.RUNE",
		DynamicBps:      7,
		LastActiveEpoch: 42,
		History: []types.DynamicFeeEpochRecord{
			{Epoch: 41, VolumeTor: cosmos.NewUint(100), FeesTor: cosmos.NewUint(10), BpsAtClose: 6},
			{Epoch: 42, VolumeTor: cosmos.NewUint(200), FeesTor: cosmos.NewUint(20), BpsAtClose: 7},
		},
	}
	c.Assert(k.SetDynamicFeeRecord(ctx, rec), IsNil)

	acc := types.DynamicFeeAccumulator{
		Thorname:   "bentley",
		PairAssetA: "BTC.BTC",
		PairAssetB: "THOR.RUNE",
		VolumeTor:  cosmos.NewUint(333),
		FeesTor:    cosmos.NewUint(33),
	}
	c.Assert(k.SetDynamicFeeAccumulator(ctx, acc), IsNil)
	k.SetLastObservedDynamicFeeEpochBlocks(ctx, 1337)

	exported := ExportGenesis(ctx, k)
	c.Check(len(exported.DynamicFeeRecords), Equals, 1)
	c.Check(len(exported.DynamicFeeAccumulators), Equals, 1)
	c.Check(exported.LastObservedDynamicFeeEpochBlocks, Equals, int64(1337))
	c.Check(exported.DynamicFeeRecords[0].DynamicBps, Equals, uint64(7))
	c.Check(len(exported.DynamicFeeRecords[0].History), Equals, 2)

	// Fresh keeper → initGenesis from exported → re-export → deep compare.
	ctx2, mgr2 := setupManagerForTest(c)
	k2 := mgr2.Keeper()
	InitGenesis(ctx2, k2, exported)

	reimported, err := k2.GetDynamicFeeRecord(ctx2, "bentley", "BTC.BTC", "THOR.RUNE")
	c.Assert(err, IsNil)
	c.Check(reimported.DynamicBps, Equals, uint64(7))
	c.Check(reimported.LastActiveEpoch, Equals, uint64(42))
	c.Check(len(reimported.History), Equals, 2)
	c.Check(reimported.History[1].BpsAtClose, Equals, uint64(7))

	reimportedAcc, err := k2.GetDynamicFeeAccumulator(ctx2, "bentley", "BTC.BTC", "THOR.RUNE")
	c.Assert(err, IsNil)
	c.Check(reimportedAcc.VolumeTor.Equal(cosmos.NewUint(333)), Equals, true)
	c.Check(reimportedAcc.FeesTor.Equal(cosmos.NewUint(33)), Equals, true)

	observed, err := k2.GetLastObservedDynamicFeeEpochBlocks(ctx2)
	c.Assert(err, IsNil)
	c.Check(observed, Equals, int64(1337))

	reExported := ExportGenesis(ctx2, k2)
	c.Check(len(reExported.DynamicFeeRecords), Equals, 1)
	c.Check(len(reExported.DynamicFeeAccumulators), Equals, 1)
	c.Check(reExported.LastObservedDynamicFeeEpochBlocks, Equals, int64(1337))
}

func (GenesisTestSuite) TestGenesisReferenceMemoWindowRoundTrip(c *C) {
	SetupConfigForTest()

	srcCtx, srcMgr := setupManagerForTest(c)
	srcCtx = srcCtx.WithBlockHeight(1000)
	ttl := srcMgr.Keeper().GetConfigInt64(srcCtx, constants.MemolessTxnTTL)

	for i := 1; i <= 3; i++ {
		srcMgr.Keeper().SetReferenceMemo(srcCtx, types.ReferenceMemo{
			Asset:     common.BTCAsset,
			Reference: fmt.Sprintf("0000%d", i),
			Memo:      "+:BTC.BTC",
			Height:    srcCtx.BlockHeight(),
		})
	}
	srcMgr.Keeper().SetReferenceMemo(srcCtx, types.ReferenceMemo{
		Asset:     common.ETHAsset,
		Reference: "00001",
		Memo:      "+:ETH.ETH",
		Height:    srcCtx.BlockHeight(),
	})

	exported := ExportGenesis(srcCtx, srcMgr.Keeper())

	dstCtx, dstMgr := setupManagerForTest(c)
	dstCtx = dstCtx.WithBlockHeight(srcCtx.BlockHeight())
	InitGenesis(dstCtx, dstMgr.Keeper(), exported)

	// the window is not a genesis field, it is rebuilt from the imported memos so
	// the congestion price does not reset to "empty ring" across a fork
	c.Check(memolessLiveRefs(dstCtx, dstMgr.Keeper(), common.BTCAsset, ttl), Equals, int64(3))
	c.Check(memolessLiveRefs(dstCtx, dstMgr.Keeper(), common.ETHAsset, ttl), Equals, int64(1))

	// and it decays away as the imported memos expire
	c.Check(memolessLiveRefs(dstCtx.WithBlockHeight(dstCtx.BlockHeight()+ttl), dstMgr.Keeper(), common.BTCAsset, ttl), Equals, int64(0))
}

// TCY smart contract addresses are surfaced by ListTCYStakers as synthetic stakers whose
// amount is the contract's own TCY balance. That balance is exported by the bank module,
// so exporting it again as a staker record would double count it and, if the smart
// contract address list ever changed, import as real TCYStake module backed stake.
func (GenesisTestSuite) TestGenesisExportSkipsTCYSmartContractStakers(c *C) {
	SetupConfigForTest()
	ctx, mgr := setupManagerForTest(c)
	k := mgr.Keeper()

	scAddr, _ := tcySmartContractStaker(c, ctx, k, cosmos.NewUint(1234))

	staker := GetRandomRUNEAddress()
	c.Assert(k.SetTCYStaker(ctx, types.NewTCYStaker(staker, cosmos.NewUint(500))), IsNil)

	// the synthetic staker is visible through the keeper
	stakers, err := k.ListTCYStakers(ctx)
	c.Assert(err, IsNil)
	foundSC := false
	for _, s := range stakers {
		if s.Address.Equals(scAddr) {
			foundSC = true
		}
	}
	c.Assert(foundSC, Equals, true)

	// ...but not through the genesis export
	exported := ExportGenesis(ctx, k)
	foundStaker := false
	for _, s := range exported.TcyStakers {
		c.Assert(tcysmartcontract.IsTCYSmartContractAddress(s.Address), Equals, false)
		if s.Address.Equals(staker) {
			foundStaker = true
			c.Assert(s.Amount.Equal(cosmos.NewUint(500)), Equals, true)
		}
	}
	c.Assert(foundStaker, Equals, true)
}

// TestGenesisSolVaultNonceRoundTrip: a vault's committed durable nonce survives
// export/import (case-sensitive base58 values intact), so the vault is not treated as
// legacy afterwards; invalid records fail genesis validation.
func (GenesisTestSuite) TestGenesisSolVaultNonceRoundTrip(c *C) {
	SetupConfigForTest()

	srcCtx, srcMgr := setupManagerForTest(c)
	vaultPK := GetRandomPubKey()
	record := SolVaultNonce{
		VaultPubKey:   vaultPK,
		NonceAccount:  "9rAtxuhtKn8qagc3UtZFyhLrw5zkh6etv43TibaXuSKo",
		Nonce:         "C8976AEb5Q4QFVZQQZ7rNY3u2j4n83PPHNagHeJLeCkH",
		Slot:          1000,
		UpdatedHeight: 42,
		SetByTx:       GetRandomTxHash(),
	}
	srcMgr.Keeper().SetSolVaultNonce(srcCtx, record)

	exported := ExportGenesis(srcCtx, srcMgr.Keeper())
	c.Assert(exported.SolVaultNonces, HasLen, 1)
	c.Assert(ValidateGenesis(exported), IsNil)
	bz, err := exported.Marshal()
	c.Assert(err, IsNil)
	var decoded GenesisState
	c.Assert(decoded.Unmarshal(bz), IsNil)

	dstCtx, dstMgr := setupManagerForTest(c)
	InitGenesis(dstCtx, dstMgr.Keeper(), decoded)
	got, found, err := dstMgr.Keeper().GetSolVaultNonce(dstCtx, vaultPK)
	c.Assert(err, IsNil)
	c.Assert(found, Equals, true)
	c.Check(got.NonceAccount, Equals, record.NonceAccount)
	c.Check(got.Nonce, Equals, record.Nonce)
	c.Check(got.Slot, Equals, record.Slot)
	c.Check(got.UpdatedHeight, Equals, record.UpdatedHeight)
	c.Check(got.SetByTx.Equals(record.SetByTx), Equals, true)

	bad := decoded
	bad.SolVaultNonces = []SolVaultNonce{{VaultPubKey: vaultPK, NonceAccount: record.NonceAccount, Slot: 1}}
	c.Assert(ValidateGenesis(bad), NotNil)
}

func (GenesisTestSuite) TestExportSwapperCloutBase58(c *C) {
	ctx, mgr := setupManagerForTest(c)
	transparent, _, _, _ := zecTxOutAddresses()
	clout := NewSwapperClout(transparent)
	clout.Score = cosmos.NewUint(1000)
	c.Assert(mgr.Keeper().SetSwapperClout(ctx, clout), IsNil)

	// Store keys are uppercased, so the export must not rebuild base58 addresses from them.
	gs := ExportGenesis(ctx, mgr.Keeper())
	c.Assert(gs.SwapperClout, HasLen, 1)
	c.Check(gs.SwapperClout[0].Address, Equals, transparent)
	c.Check(gs.SwapperClout[0].Score.String(), Equals, "1000")
}

func (GenesisTestSuite) TestGenesisDeferredErrataPoolCorrectionRoundTrip(c *C) {
	SetupConfigForTest()

	srcCtx, srcMgr := setupManagerForTest(c)
	tx := GetRandomTx()
	tx.Chain = common.BTCChain
	tx.Coins = common.NewCoins(common.NewCoin(common.BTCAsset, cosmos.NewUint(common.One)))
	observed := NewObservedTx(tx, srcCtx.BlockHeight(), GetRandomPubKey(), srcCtx.BlockHeight())
	voter := NewObservedTxVoter(tx.ID, ObservedTxs{observed})
	voter.Tx = observed
	voter.FinalisedHeight = srcCtx.BlockHeight()
	voter.SetReverted()
	voter.ErrataPoolCorrection = TxSwap.String()
	srcMgr.Keeper().SetObservedTxInVoter(srcCtx, voter)
	srcMgr.Keeper().SetErrataPoolCorrection(srcCtx, common.BTCAsset, tx.ID)

	exported := ExportGenesis(srcCtx, srcMgr.Keeper())
	found := false
	for _, exportedVoter := range exported.ObservedTxInVoters {
		if exportedVoter.TxID.Equals(tx.ID) {
			found = true
			c.Check(exportedVoter.ErrataPoolCorrection, Equals, TxSwap.String())
		}
	}
	c.Assert(found, Equals, true)

	dstCtx, dstMgr := setupManagerForTest(c)
	InitGenesis(dstCtx, dstMgr.Keeper(), exported)
	pending, err := dstMgr.Keeper().GetErrataPoolCorrections(dstCtx, common.BTCAsset)
	c.Assert(err, IsNil)
	c.Check(pending, DeepEquals, []common.TxID{tx.ID})
}
