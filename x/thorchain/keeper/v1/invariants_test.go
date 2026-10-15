package keeperv1

import (
	"fmt"
	"strings"

	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/common/tcysmartcontract"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

type InvariantsSuite struct{}

var _ = Suite(&InvariantsSuite{})

func (s *InvariantsSuite) TestAffiliateCollectorInvariantRejectsEmptyTHORName(c *C) {
	for _, amount := range []uint64{0, 100} {
		ctx, k := setupKeeperForTest(c)
		if amount > 0 {
			FundModule(c, ctx, k, AffiliateCollectorName, amount)
		}
		// Bypass the public setter to simulate invalid persisted state. Even a
		// fully funded unnamed collector must fail the invariant.
		k.setAffiliateCollector(ctx, k.GetKey(prefixAffiliateCollector, ""), AffiliateFeeCollector{RuneAmount: cosmos.NewUint(amount)})
		messages, broken := AffilliateCollectorInvariant(k)(ctx)
		c.Assert(broken, Equals, true)
		c.Assert(messages, DeepEquals, []string{"affiliate collector THORName cannot be empty"})
	}
}

func (s *InvariantsSuite) TestAsgardInvariant(c *C) {
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

	// derived asset pools are not included in expectations
	pool = NewPool()
	pool.Asset = common.BTCAsset.GetDerivedAsset()
	pool.BalanceRune = cosmos.NewUint(666)
	pool.PendingInboundRune = cosmos.NewUint(777)
	c.Assert(k.SetPool(ctx, pool), IsNil)

	// savers pools are not included in expectations
	pool = NewPool()
	pool.Asset = common.BTCAsset.GetSyntheticAsset()
	pool.BalanceRune = cosmos.NewUint(666)
	pool.PendingInboundRune = cosmos.NewUint(777)
	c.Assert(k.SetPool(ctx, pool), IsNil)

	swapMsg := MsgSwap{
		Tx: GetRandomTx(),
	}
	swapMsg.Tx.Coins = common.NewCoins(common.NewCoin(common.RuneAsset(), cosmos.NewUint(2000)))
	c.Assert(k.SetSwapQueueItem(ctx, swapMsg, 0), IsNil)

	// synth swaps are ignored
	swapMsg.Tx.Coins = common.NewCoins(common.NewCoin(common.BTCAsset.GetSyntheticAsset(), cosmos.NewUint(666)))
	c.Assert(k.SetSwapQueueItem(ctx, swapMsg, 1), IsNil)

	// layer1 swaps are ignored
	swapMsg.Tx.Coins = common.NewCoins(common.NewCoin(common.BTCAsset, cosmos.NewUint(777)))
	c.Assert(k.SetSwapQueueItem(ctx, swapMsg, 2), IsNil)

	invariant := AsgardInvariant(k)

	msg, broken := invariant(ctx)
	c.Assert(broken, Equals, true)
	c.Assert(len(msg), Equals, 2)
	c.Assert(msg[0], Equals, "insolvent: 666btc/btc")
	c.Assert(msg[1], Equals, "insolvent: 3100rune")

	// send the expected amount to asgard
	expCoins := common.NewCoins(
		common.NewCoin(common.BTCAsset.GetSyntheticAsset(), cosmos.NewUint(666)),
		common.NewCoin(common.RuneAsset(), cosmos.NewUint(3100)),
	)
	for _, coin := range expCoins {
		c.Assert(k.MintToModule(ctx, ModuleName, coin), IsNil)
	}
	c.Assert(k.SendFromModuleToModule(ctx, ModuleName, AsgardName, expCoins), IsNil)

	msg, broken = invariant(ctx)
	c.Assert(broken, Equals, false)
	c.Assert(msg, IsNil)

	// System liquidity fees from a speculative swap remain physically in
	// Asgard while their lifecycle journal, rather than the pool, owns them.
	// The invariant must recognize that escrow until finality or rollback.
	record := newKeeperMempoolSwap(0, GetRandomPubKey(), ctx.BlockHeight()+100)
	escrowedRune := record.PendingEffects.Pools[0].ReserveRune.Add(record.PendingEffects.Pools[0].TcyRune)
	c.Assert(k.SetMempoolSwap(ctx, record), IsNil)
	escrowedCoin := common.NewCoin(common.RuneAsset(), escrowedRune)
	c.Assert(k.SendFromModuleToModule(ctx, ReserveName, AsgardName, common.NewCoins(escrowedCoin)), IsNil)

	msg, broken = invariant(ctx)
	c.Assert(broken, Equals, false)
	c.Assert(msg, IsNil)

	c.Assert(k.DeleteMempoolSwap(ctx, record.TxId, record.Index), IsNil)
	msg, broken = invariant(ctx)
	c.Assert(broken, Equals, true)
	c.Assert(msg, DeepEquals, []string{fmt.Sprintf("oversolvent: %srune", escrowedRune)})
	c.Assert(k.SendFromModuleToModule(ctx, AsgardName, ReserveName, common.NewCoins(escrowedCoin)), IsNil)

	// Escrowed mempool secured targets stage the underlying L1 asset on THORChain.
	// That bookkeeping entry should not break native Asgard accounting.
	stagedEscrow := TxOutItem{
		Chain:     common.THORChain,
		InHash:    GetRandomTxHash(),
		ToAddress: GetRandomTHORAddress(),
		Coin:      common.NewCoin(common.ETHAsset, cosmos.NewUint(123)),
	}
	c.Assert(k.SetStagedOutbound(ctx, stagedEscrow, ctx.BlockHeight()+10), IsNil)

	msg, broken = invariant(ctx)
	c.Assert(broken, Equals, false)
	c.Assert(msg, IsNil)
	c.Assert(k.DeleteStagedOutbounds(ctx, stagedEscrow.InHash), IsNil)

	// send a little more to make asgard oversolvent
	extraCoins := common.NewCoins(common.NewCoin(common.RuneAsset(), cosmos.NewUint(1)))
	c.Assert(k.SendFromModuleToModule(ctx, ReserveName, AsgardName, extraCoins), IsNil)

	msg, broken = invariant(ctx)
	c.Assert(broken, Equals, true)
	c.Assert(len(msg), Equals, 1)
	c.Assert(msg[0], Equals, "oversolvent: 1rune")
}

func (s *InvariantsSuite) TestMempoolSwapsInvariant(c *C) {
	ctx, k := setupKeeperForTest(c)
	record := newKeeperMempoolSwap(0, GetRandomPubKey(), ctx.BlockHeight()+100)
	voter := NewObservedTxVoter(record.TxId, nil)
	voter.UnfinalizedHeight = ctx.BlockHeight()
	voter.MempoolSwapTOR = record.ReservedTor
	k.SetObservedTxInVoter(ctx, voter)
	c.Assert(k.SetMempoolSwap(ctx, record), IsNil)
	k.SetMempoolSwapUsageTOR(ctx, record.ReservedTor)

	invariant := MempoolSwapsInvariant(k)
	msg, broken := invariant(ctx)
	c.Assert(broken, Equals, false)
	c.Assert(msg, IsNil)

	k.SetMempoolSwapUsageTOR(ctx, record.ReservedTor.Add(cosmos.OneUint()))
	msg, broken = invariant(ctx)
	c.Assert(broken, Equals, true)
	c.Assert(msg, DeepEquals, []string{fmt.Sprintf(
		"mempool swap TOR usage %s does not match lifecycle total %s",
		record.ReservedTor.Add(cosmos.OneUint()), record.ReservedTor,
	)})
	k.SetMempoolSwapUsageTOR(ctx, record.ReservedTor)
	k.SetMempoolSwapCount(ctx, cosmos.NewUint(2))
	msg, broken = invariant(ctx)
	c.Assert(broken, Equals, true)
	c.Assert(msg, DeepEquals, []string{"mempool swap count 2 does not match lifecycle total 1"})
	k.SetMempoolSwapCount(ctx, cosmos.OneUint())

	voter.MempoolSwapTOR = record.ReservedTor.Add(cosmos.OneUint())
	k.SetObservedTxInVoter(ctx, voter)
	msg, broken = invariant(ctx)
	c.Assert(broken, Equals, true)
	c.Assert(msg, DeepEquals, []string{fmt.Sprintf(
		"mempool swap %s voter TOR %s does not match lifecycle TOR %s",
		record.Cursor(), voter.MempoolSwapTOR, record.ReservedTor,
	)})
	voter.MempoolSwapTOR = record.ReservedTor
	k.SetObservedTxInVoter(ctx, voter)

	// The derived overlay is consensus input for finalized pool views. Missing
	// and stale keys must both be visible instead of silently skewing rewards.
	k.del(ctx, k.mempoolSwapPoolBalanceOverlayKey(common.ETHAsset))
	msg, broken = invariant(ctx)
	c.Assert(broken, Equals, true)
	c.Assert(msg, DeepEquals, []string{"mempool swap pool overlay ETH.ETH does not match lifecycle aggregate"})
	c.Assert(k.updateMempoolSwapPoolBalanceOverlays(ctx, nil, &record), IsNil)

	stale := record
	stale.TxId = GetRandomTxHash()
	stale.LatestSwap.Tx.ID = stale.TxId
	stale.PendingEffects.Pools[0].Asset = common.BTCAsset
	c.Assert(k.updateMempoolSwapPoolBalanceOverlays(ctx, nil, &stale), IsNil)
	msg, broken = invariant(ctx)
	c.Assert(broken, Equals, true)
	c.Assert(msg, DeepEquals, []string{"stale or corrupt mempool swap pool overlay for BTC.BTC"})
	c.Assert(k.updateMempoolSwapPoolBalanceOverlays(ctx, &stale, nil), IsNil)
	c.Check(mempoolSwapPoolBalanceOverlayEqual(MempoolSwapPoolBalanceOverlay{}, newMempoolSwapPoolBalanceOverlay(common.BTCAsset)), Equals, false,
		Commentf("a malformed overlay with nil amounts must be reported, not panic the invariant"))

	// Native supply aggregates are independently derived consensus inputs.
	synthAsset := common.BTCAsset.GetSyntheticAsset()
	k.del(ctx, k.mempoolSwapNativeSupplyOverlayKey(synthAsset))
	msg, broken = invariant(ctx)
	c.Assert(broken, Equals, true)
	c.Assert(msg, DeepEquals, []string{fmt.Sprintf(
		"mempool swap native supply overlay %s does not match lifecycle aggregate",
		mempoolSwapNativeSupplyOverlayMapKey(synthAsset),
	)})
	c.Assert(k.updateMempoolSwapNativeSupplyOverlays(ctx, nil, &record), IsNil)

	staleSupply := record
	staleSupply.TxId = GetRandomTxHash()
	staleSupply.LatestSwap.Tx.ID = staleSupply.TxId
	staleSupply.PendingEffects.Supplies[0].Asset = common.ETHAsset.GetSyntheticAsset()
	c.Assert(k.updateMempoolSwapNativeSupplyOverlays(ctx, nil, &staleSupply), IsNil)
	msg, broken = invariant(ctx)
	c.Assert(broken, Equals, true)
	c.Assert(msg, DeepEquals, []string{fmt.Sprintf(
		"stale or corrupt mempool swap native supply overlay for %s",
		common.ETHAsset.GetSyntheticAsset(),
	)})
	c.Assert(k.updateMempoolSwapNativeSupplyOverlays(ctx, &staleSupply, nil), IsNil)
	c.Check(mempoolSwapNativeSupplyOverlayEqual(MempoolSwapNativeSupplyOverlay{}, newMempoolSwapNativeSupplyOverlay(synthAsset)), Equals, false,
		Commentf("a malformed native supply overlay with nil amounts must be reported, not panic the invariant"))

	k.del(ctx, k.mempoolSwapVaultIndexKey(record))
	msg, broken = invariant(ctx)
	c.Assert(broken, Equals, true)
	c.Assert(msg, DeepEquals, []string{fmt.Sprintf(
		"mempool swap %s is missing its vault index",
		record.Cursor(),
	)})
}

func setupValidMempoolSwapInvariant(c *C) (cosmos.Context, KVStore, MempoolSwap) {
	ctx, k := setupKeeperForTest(c)
	record := newKeeperMempoolSwap(0, GetRandomPubKey(), ctx.BlockHeight()+100)
	voter := NewObservedTxVoter(record.TxId, nil)
	voter.UnfinalizedHeight = ctx.BlockHeight()
	voter.MempoolSwapTOR = record.ReservedTor
	k.SetObservedTxInVoter(ctx, voter)
	c.Assert(k.SetMempoolSwap(ctx, record), IsNil)
	k.SetMempoolSwapUsageTOR(ctx, record.ReservedTor)
	return ctx, k, record
}

func (s *InvariantsSuite) TestMempoolSwapsInvariantChecksCanonicalPhysicalKey(c *C) {
	ctx, k, record := setupValidMempoolSwapInvariant(c)
	store := k.kvStore(ctx)
	canonicalKey := k.mempoolSwapKey(record.TxId, record.Index)
	bz := append([]byte(nil), store.Get(canonicalKey)...)
	store.Delete(canonicalKey)
	store.Set(k.GetKey(prefixMempoolSwap, "WRONG"), bz)

	msg, broken := MempoolSwapsInvariant(k)(ctx)
	c.Assert(broken, Equals, true)
	c.Assert(len(msg) > 0, Equals, true)
	c.Check(msg[0], Equals, fmt.Sprintf(
		"mempool swap %s is stored under a non-canonical key", record.Cursor(),
	))
}

func (s *InvariantsSuite) TestMempoolSwapsInvariantChecksOverlayPhysicalKeys(c *C) {
	ctx, k, record := setupValidMempoolSwapInvariant(c)
	store := k.kvStore(ctx)

	poolKey := k.mempoolSwapPoolBalanceOverlayKey(record.PendingEffects.Pools[0].Asset)
	poolBz := append([]byte(nil), store.Get(poolKey)...)
	store.Delete(poolKey)
	store.Set(k.GetKey(prefixMempoolSwapPoolOverlay, "WRONG"), poolBz)

	supplyKey := k.mempoolSwapNativeSupplyOverlayKey(record.PendingEffects.Supplies[0].Asset)
	supplyBz := append([]byte(nil), store.Get(supplyKey)...)
	store.Delete(supplyKey)
	store.Set(k.GetKey(prefixMempoolSwapSupplyOverlay, "WRONG"), supplyBz)

	msg, broken := MempoolSwapsInvariant(k)(ctx)
	c.Assert(broken, Equals, true)
	joined := strings.Join(msg, "\n")
	c.Check(strings.Contains(joined, "stale or corrupt mempool swap pool overlay for ETH.ETH"), Equals, true)
	c.Check(strings.Contains(joined, "stale or corrupt mempool swap native supply overlay for BTC/BTC"), Equals, true)
}

func (s *InvariantsSuite) TestMempoolSwapsInvariantRetainsFinalizedTombstoneOwners(c *C) {
	ctx, k, record := setupValidMempoolSwapInvariant(c)
	record.Status = types.MempoolSwapStatus_FINALIZED
	record.ReservedTor = cosmos.ZeroUint()
	record.UpdatedHeight++
	c.Assert(k.SetMempoolSwap(ctx, record), IsNil)
	k.SetMempoolSwapUsageTOR(ctx, cosmos.ZeroUint())
	voter, err := k.GetObservedTxInVoter(ctx, record.TxId)
	c.Assert(err, IsNil)
	voter.MempoolSwapTOR = cosmos.ZeroUint()
	voter.FinalisedHeight = record.UpdatedHeight
	k.SetObservedTxInVoter(ctx, voter)

	msg, broken := MempoolSwapsInvariant(k)(ctx)
	c.Check(broken, Equals, false, Commentf("unexpected invariant errors: %v", msg))
	owner, found, err := k.GetMempoolSwapPoolOwner(ctx, common.ETHAsset)
	c.Assert(err, IsNil)
	c.Assert(found, Equals, true)
	c.Check(owner.Cursor(), Equals, record.Cursor())
	poolOverlay, err := k.GetMempoolSwapPoolBalanceOverlay(ctx, common.ETHAsset)
	c.Assert(err, IsNil)
	c.Check(mempoolSwapPoolBalanceOverlayEmpty(poolOverlay), Equals, true)

	// A tombstone owner index is not stale merely because its active aggregate
	// is zero; removing it must be reported as lifecycle corruption.
	k.del(ctx, k.mempoolSwapPoolOwnerKey(common.ETHAsset))
	msg, broken = MempoolSwapsInvariant(k)(ctx)
	c.Assert(broken, Equals, true)
	c.Check(strings.Contains(strings.Join(msg, "\n"), fmt.Sprintf(
		"mempool swap %s is missing its pool owner index for ETH.ETH", record.Cursor(),
	)), Equals, true)
}

func (s *InvariantsSuite) TestMempoolSwapsInvariantReportsNilVoterTORWithoutPanic(c *C) {
	ctx, k, record := setupValidMempoolSwapInvariant(c)
	store := k.kvStore(ctx)
	txID := record.TxId.String()
	c.Assert(len(txID) < 128, Equals, true)

	// Encode tx_id (field 1) and unfinalized_height (field 11), deliberately
	// omitting the custom Uint field 12 so protobuf leaves it nil.
	voterBz := append([]byte{0x0a, byte(len(txID))}, []byte(txID)...)
	voterBz = append(voterBz, 0x58, 0x01)
	store.Set(k.GetKey(prefixObservedTxIn, txID), voterBz)

	var msg []string
	var broken bool
	c.Assert(func() {
		msg, broken = MempoolSwapsInvariant(k)(ctx)
	}, Not(Panics), nil)
	c.Assert(broken, Equals, true)
	c.Assert(msg, DeepEquals, []string{fmt.Sprintf(
		"mempool swap %s voter TOR <nil> does not match lifecycle TOR %s",
		record.Cursor(), record.ReservedTor,
	)})
}

// TestVaultDeficitInvariant verifies same-asset coins and deficit break the invariant.
func (s *InvariantsSuite) TestVaultDeficitInvariant(c *C) {
	ctx, k := setupKeeperForTest(c)

	vault := NewVault(ctx.BlockHeight(), ActiveVault, AsgardVault, GetRandomPubKey(), common.Chains{common.ETHChain}.Strings(), []ChainContract{})
	vault.Coins = common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(10)))
	vault.Deficit = common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(1)))
	c.Assert(k.SetVault(ctx, vault), IsNil)

	msg, broken := VaultDeficitInvariant(k)(ctx)
	c.Assert(broken, Equals, true)
	c.Assert(msg, HasLen, 1)
	c.Assert(msg[0], Equals, fmt.Sprintf("vault_deficit: vault %s has both coins and deficit for ETH.ETH: coins=10 deficit=1", vault.PubKey))
}

// TestVaultDeficitInvariantAllowsDifferentAssets verifies different assets do not break the invariant.
func (s *InvariantsSuite) TestVaultDeficitInvariantAllowsDifferentAssets(c *C) {
	ctx, k := setupKeeperForTest(c)

	vault := NewVault(ctx.BlockHeight(), ActiveVault, AsgardVault, GetRandomPubKey(), common.Chains{common.ETHChain, common.BTCChain}.Strings(), []ChainContract{})
	vault.Coins = common.NewCoins(common.NewCoin(common.BTCAsset, cosmos.NewUint(10)))
	vault.Deficit = common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(1)))
	c.Assert(k.SetVault(ctx, vault), IsNil)

	msg, broken := VaultDeficitInvariant(k)(ctx)
	c.Assert(broken, Equals, false)
	c.Assert(msg, IsNil)
}

// TestVaultDeficitInvariantAggregatesDuplicates verifies duplicate entries are summed by asset.
func (s *InvariantsSuite) TestVaultDeficitInvariantAggregatesDuplicates(c *C) {
	ctx, k := setupKeeperForTest(c)

	vault := NewVault(ctx.BlockHeight(), ActiveVault, AsgardVault, GetRandomPubKey(), common.Chains{common.ETHChain}.Strings(), []ChainContract{})
	vault.Coins = common.Coins{
		common.NewCoin(common.ETHAsset, cosmos.NewUint(4)),
		common.NewCoin(common.ETHAsset, cosmos.NewUint(6)),
	}
	vault.Deficit = common.Coins{
		common.NewCoin(common.ETHAsset, cosmos.ZeroUint()),
		common.NewCoin(common.ETHAsset, cosmos.NewUint(1)),
	}
	c.Assert(k.SetVault(ctx, vault), IsNil)

	msg, broken := VaultDeficitInvariant(k)(ctx)
	c.Assert(broken, Equals, true)
	c.Assert(msg, HasLen, 1)
	c.Assert(msg[0], Equals, fmt.Sprintf("vault_deficit: vault %s has both coins and deficit for ETH.ETH: coins=10 deficit=1", vault.PubKey))
}

func (s *InvariantsSuite) TestBondInvariant(c *C) {
	ctx, k := setupKeeperForTest(c)

	node := GetRandomValidatorNode(NodeActive)
	node.Bond = cosmos.NewUint(1000)
	c.Assert(k.SetNodeAccount(ctx, node), IsNil)

	node = GetRandomValidatorNode(NodeActive)
	node.Bond = cosmos.NewUint(100)
	c.Assert(k.SetNodeAccount(ctx, node), IsNil)

	network := NewNetwork()
	network.BondRewardRune = cosmos.NewUint(2000)
	c.Assert(k.SetNetwork(ctx, network), IsNil)

	invariant := BondInvariant(k)

	msg, broken := invariant(ctx)
	c.Assert(broken, Equals, true)
	c.Assert(len(msg), Equals, 1)
	c.Assert(msg[0], Equals, "insolvent: 3100rune")

	expRune := common.NewCoin(common.RuneAsset(), cosmos.NewUint(3100))
	c.Assert(k.MintToModule(ctx, ModuleName, expRune), IsNil)
	c.Assert(k.SendFromModuleToModule(ctx, ModuleName, BondName, common.NewCoins(expRune)), IsNil)

	msg, broken = invariant(ctx)
	c.Assert(broken, Equals, false)
	c.Assert(msg, IsNil)

	// send more to make bond oversolvent
	c.Assert(k.MintToModule(ctx, ModuleName, expRune), IsNil)
	c.Assert(k.SendFromModuleToModule(ctx, ModuleName, BondName, common.NewCoins(expRune)), IsNil)

	msg, broken = invariant(ctx)
	c.Assert(broken, Equals, true)
	c.Assert(len(msg), Equals, 1)
	c.Assert(msg[0], Equals, "oversolvent: 3100rune")
}

func (s *InvariantsSuite) TestTHORChainInvariant(c *C) {
	ctx, k := setupKeeperForTest(c)

	invariant := THORChainInvariant(k)

	// should pass since it has no coins
	msg, broken := invariant(ctx)
	c.Assert(broken, Equals, false)
	c.Assert(msg, IsNil)

	// send some coins to make it oversolvent
	coins := common.NewCoins(common.NewCoin(common.RuneAsset(), cosmos.NewUint(1)))
	c.Assert(k.SendFromModuleToModule(ctx, AsgardName, ModuleName, coins), IsNil)

	msg, broken = invariant(ctx)
	c.Assert(broken, Equals, true)
	c.Assert(len(msg), Equals, 1)
	c.Assert(msg[0], Equals, "oversolvent: 1rune")
}

func (s *InvariantsSuite) TestTCYClaimInvariant(c *C) {
	ctx, k := setupKeeperForTest(c)

	invariant := TCYClaimInvariant(k)

	// empty state: no module balance, no claimers -> not broken
	msg, broken := invariant(ctx)
	c.Assert(broken, Equals, false)
	c.Assert(msg, IsNil)

	// fund the claim module with TCY but no claimer records ->
	// fully protocol-owned, not broken (surplus is expected, not flagged)
	balance := cosmos.NewUint(1000)
	tcyCoin := common.NewCoin(common.TCY, balance)
	c.Assert(k.MintToModule(ctx, ModuleName, tcyCoin), IsNil)
	c.Assert(k.SendFromModuleToModule(ctx, ModuleName, TCYClaimingName, common.NewCoins(tcyCoin)), IsNil)

	msg, broken = invariant(ctx)
	c.Assert(broken, Equals, false)
	c.Assert(msg, IsNil)

	// add claimer records summing to less than the module balance ->
	// the difference is protocol-owned, still not broken
	c.Assert(k.SetTCYClaimer(ctx, types.NewTCYClaimer(GetRandomETHAddress(), common.ETHAsset, cosmos.NewUint(400))), IsNil)
	c.Assert(k.SetTCYClaimer(ctx, types.NewTCYClaimer(GetRandomETHAddress(), common.ETHAsset, cosmos.NewUint(500))), IsNil)

	msg, broken = invariant(ctx)
	c.Assert(broken, Equals, false)
	c.Assert(msg, IsNil)

	// equality (sum == balance) -> still not broken
	c.Assert(k.SetTCYClaimer(ctx, types.NewTCYClaimer(GetRandomETHAddress(), common.ETHAsset, cosmos.NewUint(100))), IsNil)

	msg, broken = invariant(ctx)
	c.Assert(broken, Equals, false)
	c.Assert(msg, IsNil)

	// claimer sum exceeds module balance by 50 -> insolvent
	c.Assert(k.SetTCYClaimer(ctx, types.NewTCYClaimer(GetRandomBTCAddress(), common.BTCAsset, cosmos.NewUint(50))), IsNil)

	msg, broken = invariant(ctx)
	c.Assert(broken, Equals, true)
	c.Assert(len(msg), Equals, 1)
	c.Assert(msg[0], Equals, "insolvent: 50tcy")
}

func (s *InvariantsSuite) TestTCYStakeInvariant(c *C) {
	ctx, k := setupKeeperForTest(c)

	invariant := TCYStakeInvariant(k)

	// empty state: no module balance, no stakers -> not broken
	msg, broken := invariant(ctx)
	c.Assert(broken, Equals, false)
	c.Assert(msg, IsNil)

	// stake module TCY exactly backing the staker records -> not broken
	balance := cosmos.NewUint(1000)
	tcyCoin := common.NewCoin(common.TCY, balance)
	c.Assert(k.MintToModule(ctx, ModuleName, tcyCoin), IsNil)
	c.Assert(k.SendFromModuleToModule(ctx, ModuleName, TCYStakeName, common.NewCoins(tcyCoin)), IsNil)
	c.Assert(k.SetTCYStaker(ctx, types.NewTCYStaker(GetRandomRUNEAddress(), cosmos.NewUint(600))), IsNil)
	c.Assert(k.SetTCYStaker(ctx, types.NewTCYStaker(GetRandomRUNEAddress(), cosmos.NewUint(400))), IsNil)

	msg, broken = invariant(ctx)
	c.Assert(broken, Equals, false)
	c.Assert(msg, IsNil)

	// TCY in the module with no record to redeem it - the #2870 fingerprint,
	// where the funds moved but the credit was dropped
	stranded := common.NewCoin(common.TCY, cosmos.NewUint(250))
	c.Assert(k.MintToModule(ctx, ModuleName, stranded), IsNil)
	c.Assert(k.SendFromModuleToModule(ctx, ModuleName, TCYStakeName, common.NewCoins(stranded)), IsNil)

	msg, broken = invariant(ctx)
	c.Assert(broken, Equals, true)
	c.Assert(len(msg), Equals, 1)
	c.Assert(msg[0], Equals, "oversolvent: 250tcy")

	// records the module cannot pay out
	c.Assert(k.SetTCYStaker(ctx, types.NewTCYStaker(GetRandomRUNEAddress(), cosmos.NewUint(500))), IsNil)

	msg, broken = invariant(ctx)
	c.Assert(broken, Equals, true)
	c.Assert(len(msg), Equals, 1)
	c.Assert(msg[0], Equals, "insolvent: 250tcy")
}

// A TCY smart contract address holds its TCY in its own account, never in the stake
// module, so it must not count towards the backing the module has to provide.
func (s *InvariantsSuite) TestTCYStakeInvariantIgnoresSmartContractStakers(c *C) {
	ctx, k := setupKeeperForTest(c)

	addresses, err := tcysmartcontract.GetTCYSmartContractAddresses()
	c.Assert(err, IsNil)
	c.Assert(len(addresses) > 0, Equals, true)
	scAcc, err := addresses[0].AccAddress()
	c.Assert(err, IsNil)

	coin := common.NewCoin(common.TCY, cosmos.NewUint(777))
	c.Assert(k.MintToModule(ctx, ModuleName, coin), IsNil)
	c.Assert(k.SendFromModuleToAccount(ctx, ModuleName, scAcc, common.NewCoins(coin)), IsNil)

	// ListTCYStakers surfaces it as a staker...
	stakers, err := k.ListTCYStakers(ctx)
	c.Assert(err, IsNil)
	c.Assert(len(stakers) > 0, Equals, true)

	// ...but the empty stake module is still consistent
	msg, broken := TCYStakeInvariant(k)(ctx)
	c.Assert(broken, Equals, false)
	c.Assert(msg, IsNil)
}

func (s *InvariantsSuite) TestStreamingSwapsInvariant(c *C) {
	ctx, k := setupKeeperForTest(c)

	// Happy path: V1 streaming swap exists and matches stream record
	tx := GetRandomTx()
	tx.Coins = common.NewCoins(common.NewCoin(common.RuneAsset(), cosmos.NewUint(1000)))
	swap := MsgSwap{
		Tx:             tx,
		TargetAsset:    common.BTCAsset,
		StreamInterval: 1,                    // makes it a legacy streaming swap
		Version:        types.SwapVersion_v1, // V1 required for IsLegacyStreaming()
	}
	c.Assert(k.SetSwapQueueItem(ctx, swap, 0), IsNil)

	stream := StreamingSwap{
		TxID:     tx.ID,
		Deposit:  cosmos.NewUint(1000),
		Count:    0,
		Quantity: 10,
		Interval: 10, // Required: must be >= 1 for Valid() to pass
		In:       cosmos.ZeroUint(),
	}
	k.SetStreamingSwap(ctx, stream)

	invariant := StreamingSwapsInvariant(k)
	msg, broken := invariant(ctx)
	c.Assert(broken, Equals, false)
	c.Assert(msg, IsNil)

	// Broken path: Stream exists but swap missing
	k.RemoveSwapQueueItem(ctx, tx.ID, 0)
	msg, broken = invariant(ctx)
	c.Assert(broken, Equals, true)
	c.Assert(len(msg), Equals, 1)
	c.Assert(msg[0], Equals, fmt.Sprintf("swap not found for stream: %s", tx.ID))

	// Broken path: Swap mismatch deposit
	c.Assert(k.SetSwapQueueItem(ctx, swap, 0), IsNil)
	stream.Deposit = cosmos.NewUint(999)
	k.SetStreamingSwap(ctx, stream)
	msg, broken = invariant(ctx)
	c.Assert(broken, Equals, true)
	c.Assert(len(msg), Equals, 1)
	c.Assert(msg[0], Equals, fmt.Sprintf("%s: swap.coin 1000 != stream.deposit 999", tx.ID))

	// Fix the state to be valid again.
	c.Assert(k.SetSwapQueueItem(ctx, swap, 0), IsNil) // Ensure swap exists
	stream.Deposit = cosmos.NewUint(1000)             // Fix deposit
	k.SetStreamingSwap(ctx, stream)                   // Save fixed stream
	// Verify it's fixed
	msg, broken = invariant(ctx)
	c.Assert(broken, Equals, false)
	c.Assert(msg, IsNil)

	// V2 Market Streaming Swap (Happy Path)
	v2Tx := GetRandomTx()
	v2Tx.Coins = common.NewCoins(common.NewCoin(common.RuneAsset(), cosmos.NewUint(1000)))
	v2Swap := MsgSwap{
		Tx:          v2Tx,
		TargetAsset: common.BTCAsset,
		TradeTarget: cosmos.ZeroUint(),
		SwapType:    types.SwapType_market,
		State: &types.SwapState{
			Quantity: 5,
			Count:    2,
			Deposit:  cosmos.NewUint(1000),
			In:       cosmos.ZeroUint(),
		},
	}
	c.Assert(k.SetAdvSwapQueueItem(ctx, v2Swap), IsNil)

	msg, broken = invariant(ctx)
	c.Assert(broken, Equals, false)
	c.Assert(msg, IsNil)

	// Limit Swap test
	limitTx := GetRandomTx()
	limitTx.Coins = common.NewCoins(common.NewCoin(common.RuneAsset(), cosmos.NewUint(1000)))
	limitSwap := MsgSwap{
		Tx:          limitTx,
		TargetAsset: common.BTCAsset,
		TradeTarget: cosmos.ZeroUint(), // required for getRatio
		SwapType:    types.SwapType_limit,
		State: &types.SwapState{
			Quantity:    2,
			Count:       4, // 4 attempts total
			Deposit:     cosmos.NewUint(1000),
			In:          cosmos.ZeroUint(),
			FailedSwaps: []uint64{1, 2}, // 2 failed attempts
		},
	}
	// SuccessCount = 4 - 2 = 2. Quantity = 2. 2 <= 2. Valid.

	// Add to advanced swap queue (V2)
	c.Assert(k.SetAdvSwapQueueItem(ctx, limitSwap), IsNil)

	msg, broken = invariant(ctx)
	c.Assert(broken, Equals, false)
	c.Assert(msg, IsNil)

	// Broken path: Limit swap success count exceeds quantity
	limitSwap.State.Count = 5                    // 5 attempts total
	limitSwap.State.FailedSwaps = []uint64{1, 2} // 2 failed -> 3 successes > 2 Quantity
	c.Assert(k.SetAdvSwapQueueItem(ctx, limitSwap), IsNil)

	msg, broken = invariant(ctx)
	c.Assert(broken, Equals, true)
	c.Assert(len(msg), Equals, 1)
	c.Assert(msg[0], Equals, fmt.Sprintf("%s: state.success_count 3 > state.quantity 2", limitTx.ID))
}
