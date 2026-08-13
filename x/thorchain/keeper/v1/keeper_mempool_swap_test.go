package keeperv1

import (
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

type KeeperMempoolSwapSuite struct{}

var _ = Suite(&KeeperMempoolSwapSuite{})

func newKeeperMempoolSwap(index uint32, vault common.PubKey, recoveryHeight int64) MempoolSwap {
	tx := GetRandomTx()
	msg := types.NewMsgSwap(
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
	msg.Index = index

	return MempoolSwap{
		TxId:               tx.ID,
		Index:              index,
		VaultPubKey:        vault,
		ReservedTor:        cosmos.NewUint(1234),
		AdmittedHeight:     18,
		UpdatedHeight:      18,
		NextRecoveryHeight: recoveryHeight,
		Status:             types.MempoolSwapStatus_RESERVED,
		LatestSwap:         *msg,
		PendingEffects: types.MempoolSwapPendingEffects{
			Pools: []types.MempoolSwapPoolEffect{{
				Asset:                common.ETHAsset,
				LiquidityFeeRune:     cosmos.NewUint(11),
				SwapSlip:             cosmos.NewUint(12),
				VolumeRune:           cosmos.NewUint(13),
				VolumeAsset:          cosmos.NewUint(14),
				ReserveRune:          cosmos.NewUint(15),
				TcyRune:              cosmos.NewUint(16),
				BalanceRuneIncrease:  cosmos.NewUint(21),
				BalanceRuneDecrease:  cosmos.NewUint(22),
				BalanceAssetIncrease: cosmos.NewUint(23),
				BalanceAssetDecrease: cosmos.NewUint(24),
			}},
			Clout: []types.MempoolSwapCloutEffect{{
				Address: GetRandomTHORAddress(),
				Score:   cosmos.NewUint(17),
			}},
			Affiliates: []types.MempoolSwapAffiliateEffect{{
				Thorname: "affiliate",
				FeeRune:  cosmos.NewUint(18),
			}},
			DynamicFees: []types.MempoolSwapDynamicFeeEffect{{
				Thorname:   "affiliate",
				PairAssetA: common.ETHAsset,
				PairAssetB: common.BTCAsset,
				VolumeTor:  cosmos.NewUint(19),
				FeesTor:    cosmos.NewUint(20),
			}},
			Cleanups: []types.MempoolSwapCleanupEffect{{
				Item: types.TxOutItem{
					Chain:     common.ETHChain,
					InHash:    tx.ID,
					ToAddress: GetRandomETHAddress(),
					Coin:      common.NewCoin(common.ETHAsset, cosmos.NewUint(21)),
				},
				BurnReason: "failed_outbound",
				CleanupKey: "mempool-cleanup-test",
			}},
			Supplies: []types.MempoolSwapSupplyEffect{{
				Asset:          common.BTCAsset.GetSyntheticAsset(),
				SupplyIncrease: cosmos.NewUint(25),
				SupplyDecrease: cosmos.NewUint(26),
			}},
		},
	}
}

func (s *KeeperMempoolSwapSuite) TestLifecycleCRUDAndIndexes(c *C) {
	ctx, k := setupKeeperForTest(c)
	vaultA := GetRandomPubKey()
	vaultB := GetRandomPubKey()
	record := newKeeperMempoolSwap(0, vaultA, 30)

	// Lifecycle CRUD does not implicitly alter the compatibility aggregate.
	k.SetMempoolSwapUsageTOR(ctx, cosmos.NewUint(9999))
	c.Assert(k.SetMempoolSwap(ctx, record), IsNil)
	c.Check(k.GetMempoolSwapUsageTOR(ctx).Equal(cosmos.NewUint(9999)), Equals, true)
	c.Check(k.GetMempoolSwapCount(ctx).Equal(cosmos.OneUint()), Equals, true)
	c.Check(k.HasMempoolSwap(ctx, record.TxId, record.Index), Equals, true)

	got, found, err := k.GetMempoolSwap(ctx, record.TxId, record.Index)
	c.Assert(err, IsNil)
	c.Assert(found, Equals, true)
	c.Check(got.Status, Equals, types.MempoolSwapStatus_RESERVED)
	c.Check(got.PendingEffects.Pools[0].ReserveRune.Equal(cosmos.NewUint(15)), Equals, true)
	c.Check(got.PendingEffects.DynamicFees[0].FeesTor.Equal(cosmos.NewUint(20)), Equals, true)
	c.Check(got.PendingEffects.Cleanups[0].CleanupKey, Equals, "mempool-cleanup-test")
	c.Check(got.PendingEffects.Supplies[0].SupplyIncrease.Equal(cosmos.NewUint(25)), Equals, true)

	byVault, err := k.GetMempoolSwapsByVault(ctx, vaultA, 10)
	c.Assert(err, IsNil)
	c.Assert(byVault, HasLen, 1)
	c.Check(byVault[0].TxId.Equals(record.TxId), Equals, true)
	due, err := k.GetMempoolSwapsDue(ctx, 29, 10)
	c.Assert(err, IsNil)
	c.Check(due, HasLen, 0)
	due, err = k.GetMempoolSwapsDue(ctx, 30, 10)
	c.Assert(err, IsNil)
	c.Assert(due, HasLen, 1)

	// Updating the canonical record atomically moves both secondary markers.
	record.VaultPubKey = vaultB
	record.NextRecoveryHeight = 50
	record.UpdatedHeight = 19
	record.Status = types.MempoolSwapStatus_EXECUTING
	c.Assert(k.SetMempoolSwap(ctx, record), IsNil)
	c.Check(k.GetMempoolSwapCount(ctx).Equal(cosmos.OneUint()), Equals, true)
	byVault, err = k.GetMempoolSwapsByVault(ctx, vaultA, 10)
	c.Assert(err, IsNil)
	c.Check(byVault, HasLen, 0)
	byVault, err = k.GetMempoolSwapsByVault(ctx, vaultB, 10)
	c.Assert(err, IsNil)
	c.Assert(byVault, HasLen, 1)
	due, err = k.GetMempoolSwapsDue(ctx, 30, 10)
	c.Assert(err, IsNil)
	c.Check(due, HasLen, 0)
	due, err = k.GetMempoolSwapsDue(ctx, 50, 10)
	c.Assert(err, IsNil)
	c.Assert(due, HasLen, 1)

	// A rejected update leaves the prior canonical value and indexes intact.
	invalid := record
	invalid.VaultPubKey = common.EmptyPubKey
	c.Assert(k.SetMempoolSwap(ctx, invalid), NotNil)
	byVault, err = k.GetMempoolSwapsByVault(ctx, vaultB, 10)
	c.Assert(err, IsNil)
	c.Assert(byVault, HasLen, 1)

	c.Assert(k.DeleteMempoolSwap(ctx, record.TxId, record.Index), IsNil)
	c.Check(k.GetMempoolSwapCount(ctx).IsZero(), Equals, true)
	c.Check(k.HasMempoolSwap(ctx, record.TxId, record.Index), Equals, false)
	missing, found, err := k.GetMempoolSwap(ctx, record.TxId, record.Index)
	c.Assert(err, IsNil)
	c.Check(found, Equals, false)
	c.Check(missing.TxId.Equals(record.TxId), Equals, true)
	c.Check(missing.Index, Equals, record.Index)
	byVault, err = k.GetMempoolSwapsByVault(ctx, vaultB, 10)
	c.Assert(err, IsNil)
	c.Check(byVault, HasLen, 0)
	due, err = k.GetMempoolSwapsDue(ctx, 50, 10)
	c.Assert(err, IsNil)
	c.Check(due, HasLen, 0)
	c.Check(k.GetMempoolSwapUsageTOR(ctx).Equal(cosmos.NewUint(9999)), Equals, true)

	// Genesis restoration rebuilds indexes/count but leaves TOR usage alone.
	c.Assert(k.RestoreMempoolSwap(ctx, record), IsNil)
	c.Check(k.GetMempoolSwapCount(ctx).Equal(cosmos.OneUint()), Equals, true)
	byVault, err = k.GetMempoolSwapsByVault(ctx, vaultB, 10)
	c.Assert(err, IsNil)
	c.Assert(byVault, HasLen, 1)
	c.Check(k.GetMempoolSwapUsageTOR(ctx).Equal(cosmos.NewUint(9999)), Equals, true)
}

func (s *KeeperMempoolSwapSuite) TestPoolBalanceOverlayTracksLifecycleTransitions(c *C) {
	ctx, k := setupKeeperForTest(c)
	recordA := newKeeperMempoolSwap(0, GetRandomPubKey(), 30)
	recordA.PendingEffects.Supplies = nil

	c.Assert(k.SetMempoolSwap(ctx, recordA), IsNil)
	overlay, err := k.GetMempoolSwapPoolBalanceOverlay(ctx, common.ETHAsset)
	c.Assert(err, IsNil)
	c.Check(overlay.BalanceRuneIncrease.Equal(cosmos.NewUint(21)), Equals, true)
	c.Check(overlay.BalanceRuneDecrease.Equal(cosmos.NewUint(22)), Equals, true)
	c.Check(overlay.BalanceAssetIncrease.Equal(cosmos.NewUint(23)), Equals, true)
	c.Check(overlay.BalanceAssetDecrease.Equal(cosmos.NewUint(24)), Equals, true)
	owner, found, err := k.GetMempoolSwapPoolOwner(ctx, common.ETHAsset)
	c.Assert(err, IsNil)
	c.Check(found, Equals, true)
	c.Check(owner.Cursor(), Equals, recordA.Cursor())

	// Finality makes the already-applied balances authoritative and clears the
	// active aggregate, but the tombstone retains ownership through its deep-
	// reorg window.
	recordA.Status = types.MempoolSwapStatus_FINALIZED
	recordA.ReservedTor = cosmos.ZeroUint()
	recordA.UpdatedHeight++
	c.Assert(k.SetMempoolSwap(ctx, recordA), IsNil)
	overlay, err = k.GetMempoolSwapPoolBalanceOverlay(ctx, common.ETHAsset)
	c.Assert(err, IsNil)
	c.Check(mempoolSwapPoolBalanceOverlayEmpty(overlay), Equals, true)
	owner, found, err = k.GetMempoolSwapPoolOwner(ctx, common.ETHAsset)
	c.Assert(err, IsNil)
	c.Check(found, Equals, true)
	c.Check(owner.Cursor(), Equals, recordA.Cursor())

	// A deep-reorg rollback marker restores the zero-TOR tombstone's overlay so
	// end-block readers exclude it again while rollback is pending.
	recordA.Status = types.MempoolSwapStatus_ROLLBACK_PENDING
	recordA.UpdatedHeight++
	c.Assert(k.SetMempoolSwap(ctx, recordA), IsNil)
	overlay, err = k.GetMempoolSwapPoolBalanceOverlay(ctx, common.ETHAsset)
	c.Assert(err, IsNil)
	c.Check(overlay.BalanceRuneIncrease.Equal(cosmos.NewUint(21)), Equals, true)
	c.Check(overlay.BalanceRuneDecrease.Equal(cosmos.NewUint(22)), Equals, true)
	owner, found, err = k.GetMempoolSwapPoolOwner(ctx, common.ETHAsset)
	c.Assert(err, IsNil)
	c.Check(found, Equals, true)
	c.Check(owner.Cursor(), Equals, recordA.Cursor())

	c.Assert(k.DeleteMempoolSwap(ctx, recordA.TxId, recordA.Index), IsNil)
	overlay, err = k.GetMempoolSwapPoolBalanceOverlay(ctx, common.ETHAsset)
	c.Assert(err, IsNil)
	c.Check(mempoolSwapPoolBalanceOverlayEmpty(overlay), Equals, true)
	_, found, err = k.GetMempoolSwapPoolOwner(ctx, common.ETHAsset)
	c.Assert(err, IsNil)
	c.Check(found, Equals, false)
}

func (s *KeeperMempoolSwapSuite) TestOverlayAggregationUsesCanonicalMapKeys(c *C) {
	record := newKeeperMempoolSwap(0, GetRandomPubKey(), 30)
	poolOverlays := mempoolSwapRecordPoolBalanceOverlays(&record)
	c.Assert(poolOverlays, HasLen, 1)
	_, hasPool := poolOverlays["ETH.ETH"]
	c.Check(hasPool, Equals, true)

	supplyOverlays := mempoolSwapRecordNativeSupplyOverlays(&record)
	c.Assert(supplyOverlays, HasLen, 1)
	_, hasSupply := supplyOverlays["BTC/BTC"]
	c.Check(hasSupply, Equals, true)
}

func (s *KeeperMempoolSwapSuite) TestNativeSupplyOverlayTracksLifecycleTransitions(c *C) {
	ctx, k := setupKeeperForTest(c)
	recordA := newKeeperMempoolSwap(0, GetRandomPubKey(), 30)
	recordA.PendingEffects.Pools = nil

	c.Assert(k.SetMempoolSwap(ctx, recordA), IsNil)
	overlay, err := k.GetMempoolSwapNativeSupplyOverlay(ctx, common.BTCAsset.GetSyntheticAsset())
	c.Assert(err, IsNil)
	c.Check(overlay.SupplyIncrease.Equal(cosmos.NewUint(25)), Equals, true)
	c.Check(overlay.SupplyDecrease.Equal(cosmos.NewUint(26)), Equals, true)
	owner, found, err := k.GetMempoolSwapPoolOwner(ctx, common.BTCAsset)
	c.Assert(err, IsNil)
	c.Check(found, Equals, true)
	c.Check(owner.Cursor(), Equals, recordA.Cursor())

	// Finality makes the already-applied bank supply authoritative, while the
	// zero-overlay tombstone remains the pool owner through its reorg window.
	recordA.Status = types.MempoolSwapStatus_FINALIZED
	recordA.ReservedTor = cosmos.ZeroUint()
	recordA.UpdatedHeight++
	c.Assert(k.SetMempoolSwap(ctx, recordA), IsNil)
	overlay, err = k.GetMempoolSwapNativeSupplyOverlay(ctx, common.BTCAsset.GetSyntheticAsset())
	c.Assert(err, IsNil)
	c.Check(mempoolSwapNativeSupplyOverlayEmpty(overlay), Equals, true)
	owner, found, err = k.GetMempoolSwapPoolOwner(ctx, common.BTCAsset)
	c.Assert(err, IsNil)
	c.Check(found, Equals, true)
	c.Check(owner.Cursor(), Equals, recordA.Cursor())

	// A deep reorg restores the tombstone's contribution while rollback waits.
	recordA.Status = types.MempoolSwapStatus_ROLLBACK_PENDING
	recordA.UpdatedHeight++
	c.Assert(k.SetMempoolSwap(ctx, recordA), IsNil)
	overlay, err = k.GetMempoolSwapNativeSupplyOverlay(ctx, common.BTCAsset.GetSyntheticAsset())
	c.Assert(err, IsNil)
	c.Check(overlay.SupplyIncrease.Equal(cosmos.NewUint(25)), Equals, true)
	c.Check(overlay.SupplyDecrease.Equal(cosmos.NewUint(26)), Equals, true)
	owner, found, err = k.GetMempoolSwapPoolOwner(ctx, common.BTCAsset)
	c.Assert(err, IsNil)
	c.Check(found, Equals, true)
	c.Check(owner.Cursor(), Equals, recordA.Cursor())

	c.Assert(k.DeleteMempoolSwap(ctx, recordA.TxId, recordA.Index), IsNil)
	overlay, err = k.GetMempoolSwapNativeSupplyOverlay(ctx, common.BTCAsset.GetSyntheticAsset())
	c.Assert(err, IsNil)
	c.Check(mempoolSwapNativeSupplyOverlayEmpty(overlay), Equals, true)
	_, found, err = k.GetMempoolSwapPoolOwner(ctx, common.BTCAsset)
	c.Assert(err, IsNil)
	c.Check(found, Equals, false)
}

func (s *KeeperMempoolSwapSuite) TestPoolOwnerIndexRejectsBalanceSupplyConflictAtomically(c *C) {
	ctx, k := setupKeeperForTest(c)
	balanceOwner := newKeeperMempoolSwap(0, GetRandomPubKey(), 30)
	balanceOwner.PendingEffects.Supplies = nil

	supplyOwner := newKeeperMempoolSwap(0, GetRandomPubKey(), 31)
	supplyOwner.PendingEffects.Pools = nil
	supplyOwner.PendingEffects.Supplies[0].Asset = common.ETHAsset.GetSyntheticAsset()

	c.Assert(k.SetMempoolSwap(ctx, balanceOwner), IsNil)
	err := k.SetMempoolSwap(ctx, supplyOwner)
	c.Assert(err, NotNil)
	c.Check(k.HasMempoolSwap(ctx, supplyOwner.TxId, supplyOwner.Index), Equals, false)
	c.Check(k.GetMempoolSwapCount(ctx).Equal(cosmos.OneUint()), Equals, true)

	owner, found, err := k.GetMempoolSwapPoolOwner(ctx, common.ETHAsset)
	c.Assert(err, IsNil)
	c.Check(found, Equals, true)
	c.Check(owner.Cursor(), Equals, balanceOwner.Cursor())
	poolOverlay, err := k.GetMempoolSwapPoolBalanceOverlay(ctx, common.ETHAsset)
	c.Assert(err, IsNil)
	c.Check(poolOverlay.BalanceRuneIncrease.Equal(cosmos.NewUint(21)), Equals, true)
	supplyOverlay, err := k.GetMempoolSwapNativeSupplyOverlay(ctx, common.ETHAsset.GetSyntheticAsset())
	c.Assert(err, IsNil)
	c.Check(mempoolSwapNativeSupplyOverlayEmpty(supplyOverlay), Equals, true)
}

func (s *KeeperMempoolSwapSuite) TestFinalizedTombstoneOwnsPoolUntilTerminalDelete(c *C) {
	ctx, k := setupKeeperForTest(c)
	tombstone := newKeeperMempoolSwap(0, GetRandomPubKey(), 30)
	tombstone.PendingEffects.Supplies = nil
	c.Assert(k.SetMempoolSwap(ctx, tombstone), IsNil)
	tombstone.Status = types.MempoolSwapStatus_FINALIZED
	tombstone.ReservedTor = cosmos.ZeroUint()
	tombstone.UpdatedHeight++
	c.Assert(k.SetMempoolSwap(ctx, tombstone), IsNil)

	successor := newKeeperMempoolSwap(0, GetRandomPubKey(), 31)
	successor.PendingEffects.Supplies = nil
	err := k.SetMempoolSwap(ctx, successor)
	c.Assert(err, ErrorMatches, "mempool swap pool ETH.ETH already has a different lifecycle owner")
	c.Check(k.HasMempoolSwap(ctx, successor.TxId, successor.Index), Equals, false)

	c.Assert(k.DeleteMempoolSwap(ctx, tombstone.TxId, tombstone.Index), IsNil)
	c.Assert(k.SetMempoolSwap(ctx, successor), IsNil)
	owner, found, err := k.GetMempoolSwapPoolOwner(ctx, common.ETHAsset)
	c.Assert(err, IsNil)
	c.Assert(found, Equals, true)
	c.Check(owner.Cursor(), Equals, successor.Cursor())
}

func (s *KeeperMempoolSwapSuite) TestRuneMintCapProtectsSpeculativeBurnHeadroom(c *C) {
	ctx, k := setupKeeperForTest(c)
	bankRuneSupply := func() cosmos.Uint {
		return cosmos.NewUint(k.coinKeeper.GetSupply(ctx, common.RuneAsset().Native()).Amount.Uint64())
	}
	maxSupply := bankRuneSupply()

	// Start at the existing test supply cap, with enough Reserve balance to
	// distinguish borrowing from a real bank mint.
	c.Assert(k.SendFromModuleToModule(ctx, AsgardName, ReserveName, common.NewCoins(
		common.NewCoin(common.RuneAsset(), cosmos.NewUint(100)),
	)), IsNil)

	// Model a speculative derived swap which has burned 100 RUNE and then
	// durably claimed that exact supply decrease in its lifecycle record.
	burn := common.NewCoin(common.RuneAsset(), cosmos.NewUint(100))
	c.Assert(k.SendFromModuleToModule(ctx, AsgardName, ModuleName, common.NewCoins(burn)), IsNil)
	c.Assert(k.BurnFromModule(ctx, ModuleName, burn), IsNil)
	record := newKeeperMempoolSwap(0, GetRandomPubKey(), 30)
	record.PendingEffects.Supplies = []types.MempoolSwapSupplyEffect{{
		Asset:          common.RuneAsset(),
		SupplyIncrease: cosmos.ZeroUint(),
		SupplyDecrease: burn.Amount,
	}}
	c.Assert(k.SetMempoolSwap(ctx, record), IsNil)
	k.SetMimir(ctx, constants.MaxRuneSupply.String(), maxSupply.BigInt().Int64())

	// Physical supply is cap-100, but the reversible burn must not expose 100 of
	// mint headroom. A new request therefore borrows from Reserve.
	c.Check(bankRuneSupply().Equal(maxSupply.Sub(cosmos.NewUint(100))), Equals, true)
	c.Assert(k.MintToModule(ctx, ModuleName, common.NewCoin(common.RuneAsset(), cosmos.NewUint(50))), IsNil)
	c.Check(bankRuneSupply().Equal(maxSupply.Sub(cosmos.NewUint(100))), Equals, true)
	c.Check(k.GetRuneBalanceOfModule(ctx, ReserveName).Equal(cosmos.NewUint(50)), Equals, true)

	// Rollback preparation removes the decrease overlay before replaying the
	// exact inverse mint. The 100 RUNE is minted back without touching Reserve.
	record.Status = types.MempoolSwapStatus_ROLLBACK_PENDING
	record.ReservedTor = cosmos.ZeroUint()
	record.PendingEffects = types.MempoolSwapPendingEffects{}
	record.UpdatedHeight++
	c.Assert(k.SetMempoolSwap(ctx, record), IsNil)
	reserveBefore := k.GetRuneBalanceOfModule(ctx, ReserveName)
	c.Assert(k.MintToModule(ctx, ModuleName, burn), IsNil)
	c.Check(bankRuneSupply().Equal(maxSupply), Equals, true)
	c.Check(k.GetRuneBalanceOfModule(ctx, ReserveName).Equal(reserveBefore), Equals, true)
}

func (s *KeeperMempoolSwapSuite) TestTimeoutOrderingAndBounds(c *C) {
	ctx, k := setupKeeperForTest(c)
	vault := GetRandomPubKey()
	late := newKeeperMempoolSwap(0, vault, 100)
	early := newKeeperMempoolSwap(0, vault, 9)
	late.PendingEffects = types.MempoolSwapPendingEffects{}
	early.PendingEffects = types.MempoolSwapPendingEffects{}
	c.Assert(k.SetMempoolSwap(ctx, late), IsNil)
	c.Assert(k.SetMempoolSwap(ctx, early), IsNil)

	due, err := k.GetMempoolSwapsDue(ctx, 99, 10)
	c.Assert(err, IsNil)
	c.Assert(due, HasLen, 1)
	c.Check(due[0].TxId.Equals(early.TxId), Equals, true)
	due, err = k.GetMempoolSwapsDue(ctx, 100, 1)
	c.Assert(err, IsNil)
	c.Assert(due, HasLen, 1)
	c.Check(due[0].TxId.Equals(early.TxId), Equals, true)
}

func (s *KeeperMempoolSwapSuite) TestVaultRecoveryCursor(c *C) {
	ctx, k := setupKeeperForTest(c)
	vault := GetRandomPubKey()
	recordA := newKeeperMempoolSwap(0, vault, 30)
	recordB := newKeeperMempoolSwap(0, vault, 30)
	recordA.PendingEffects = types.MempoolSwapPendingEffects{}
	recordB.PendingEffects = types.MempoolSwapPendingEffects{}
	if recordB.Cursor() < recordA.Cursor() {
		recordA, recordB = recordB, recordA
	}
	c.Assert(k.SetMempoolSwap(ctx, recordA), IsNil)
	c.Assert(k.SetMempoolSwap(ctx, recordB), IsNil)

	first, err := k.GetMempoolSwapsByVaultAfter(ctx, vault, "", 1)
	c.Assert(err, IsNil)
	c.Assert(first, HasLen, 1)
	c.Check(first[0].Cursor(), Equals, recordA.Cursor())
	after, err := k.GetMempoolSwapsByVaultAfter(ctx, vault, recordA.Cursor(), 10)
	c.Assert(err, IsNil)
	c.Assert(after, HasLen, 1)
	c.Check(after[0].Cursor(), Equals, recordB.Cursor())

	k.SetMempoolSwapVaultRecovery(ctx, vault, recordA.Cursor())
	cursor, err := k.GetMempoolSwapVaultRecovery(ctx, vault)
	c.Assert(err, IsNil)
	c.Check(cursor, Equals, recordA.Cursor())
	vaults, err := k.GetMempoolSwapVaultRecoveries(ctx, 1)
	c.Assert(err, IsNil)
	c.Assert(vaults, HasLen, 1)
	c.Check(vaults[0].Equals(vault), Equals, true)

	k.DeleteMempoolSwapVaultRecovery(ctx, vault)
	vaults, err = k.GetMempoolSwapVaultRecoveries(ctx, 10)
	c.Assert(err, IsNil)
	c.Check(vaults, HasLen, 0)
}

func (s *KeeperMempoolSwapSuite) TestLifecycleStatusAndIndexValidation(c *C) {
	ctx, k := setupKeeperForTest(c)
	vault := GetRandomPubKey()

	noSource := newKeeperMempoolSwap(0, vault, 30)
	noSource.LatestSwap.Tx.Coins = nil
	c.Assert(k.SetMempoolSwap(ctx, noSource), ErrorMatches, "mempool swap latest swap must contain exactly one source coin")

	multipleSources := newKeeperMempoolSwap(0, vault, 30)
	multipleSources.LatestSwap.Tx.Coins = append(
		multipleSources.LatestSwap.Tx.Coins,
		common.NewCoin(common.ETHAsset, cosmos.OneUint()),
	)
	c.Assert(k.SetMempoolSwap(ctx, multipleSources), ErrorMatches, "mempool swap latest swap must contain exactly one source coin")

	nativeSource := newKeeperMempoolSwap(0, vault, 30)
	nativeSource.LatestSwap.Tx.Coins[0] = common.NewCoin(common.RuneAsset(), cosmos.OneUint())
	c.Assert(k.SetMempoolSwap(ctx, nativeSource), ErrorMatches, "mempool swap latest swap source asset must be an external L1 asset")

	indexed := newKeeperMempoolSwap(0, vault, 30)
	indexed.Index = 1
	indexed.LatestSwap.Index = 1
	c.Assert(k.SetMempoolSwap(ctx, indexed), ErrorMatches, "mempool swap index must be zero")

	finalized := newKeeperMempoolSwap(0, vault, 30)
	finalized.Status = types.MempoolSwapStatus_FINALIZED
	c.Assert(k.SetMempoolSwap(ctx, finalized), ErrorMatches, "finalized mempool swap tombstone cannot reserve TOR")
	finalized.ReservedTor = cosmos.ZeroUint()
	c.Assert(k.SetMempoolSwap(ctx, finalized), IsNil)
	c.Check(k.GetMempoolSwapCount(ctx).Equal(cosmos.OneUint()), Equals, true)

	rollbackPending := newKeeperMempoolSwap(0, vault, 30)
	rollbackPending.Status = types.MempoolSwapStatus_ROLLBACK_PENDING
	rollbackPending.ReservedTor = cosmos.ZeroUint()
	rollbackPending.PendingEffects = types.MempoolSwapPendingEffects{}
	c.Assert(k.SetMempoolSwap(ctx, rollbackPending), IsNil)
	c.Check(k.GetMempoolSwapCount(ctx).Equal(cosmos.NewUint(2)), Equals, true)
}

func (s *KeeperMempoolSwapSuite) TestDeleteCountUnderflowIsAtomic(c *C) {
	ctx, k := setupKeeperForTest(c)
	record := newKeeperMempoolSwap(0, GetRandomPubKey(), 30)
	c.Assert(k.SetMempoolSwap(ctx, record), IsNil)
	k.SetMempoolSwapCount(ctx, cosmos.ZeroUint())

	c.Assert(k.DeleteMempoolSwap(ctx, record.TxId, record.Index), ErrorMatches, "mempool swap count underflow deleting .*")
	c.Check(k.HasMempoolSwap(ctx, record.TxId, record.Index), Equals, true)
	byVault, err := k.GetMempoolSwapsByVault(ctx, record.VaultPubKey, 10)
	c.Assert(err, IsNil)
	c.Check(byVault, HasLen, 1)
	overlay, err := k.GetMempoolSwapPoolBalanceOverlay(ctx, common.ETHAsset)
	c.Assert(err, IsNil)
	c.Check(overlay.BalanceRuneIncrease.Equal(record.PendingEffects.Pools[0].BalanceRuneIncrease), Equals, true,
		Commentf("failed deletion must preserve the derived pool overlay"))
}
