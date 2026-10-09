package keeperv1

import (
	"errors"

	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	kvTypes "gitlab.com/thorchain/thornode/v3/x/thorchain/keeper/types"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

type KeeperVaultNodeCovSuite struct{}

var _ = Suite(&KeeperVaultNodeCovSuite{})

func (s *KeeperVaultNodeCovSuite) TestVaultSecurityOrdering(c *C) {
	ctx, k := setupKeeperForTest(c)
	ctx = ctx.WithBlockHeight(20)

	c.Check(k.GetMostSecure(ctx, nil, 10).PubKey.IsEmpty(), Equals, true)
	c.Check(k.GetLeastSecure(ctx, nil, 10).PubKey.IsEmpty(), Equals, true)

	pool := NewPool()
	pool.Asset = common.BTCAsset
	pool.Status = types.PoolStatus_Available
	pool.BalanceRune = cosmos.NewUint(100 * common.One)
	pool.BalanceAsset = cosmos.NewUint(100 * common.One)
	c.Assert(k.SetPool(ctx, pool), IsNil)

	// secure vault: one active member with a 1000 RUNE bond and a 10 BTC (= 10 RUNE) balance
	na := GetRandomValidatorNode(NodeActive)
	na.Bond = cosmos.NewUint(1000 * common.One)
	c.Assert(k.SetNodeAccount(ctx, na), IsNil)
	secure := types.GetRandomVault()
	secure.Membership = []string{na.PubKeySet.Secp256k1.String()}
	secure.Coins = common.NewCoins(
		common.NewCoin(common.BTCAsset, cosmos.NewUint(10*common.One)),
		common.NewCoin(common.RuneAsset(), cosmos.NewUint(5*common.One)), // rune is ignored
	)

	// insecure vault: a standby member's bond does not count, 50 BTC balance,
	// a member pubkey that cannot be converted to an address is skipped
	standby := GetRandomValidatorNode(NodeStandby)
	c.Assert(k.SetNodeAccount(ctx, standby), IsNil)
	insecure := types.GetRandomVault()
	insecure.Membership = []string{standby.PubKeySet.Secp256k1.String(), "bogus"}
	insecure.Coins = common.NewCoins(common.NewCoin(common.BTCAsset, cosmos.NewUint(50*common.One)))

	// recent outbounds from/to the vaults adjust their values
	btcAddr, err := insecure.PubKey.GetAddress(common.BTCChain)
	c.Assert(err, IsNil)
	thorAddr, err := insecure.PubKey.GetAddress(common.THORChain)
	c.Assert(err, IsNil)
	txOut := NewTxOut(19)
	txOut.TxArray = append(txOut.TxArray,
		TxOutItem{Chain: common.BTCChain, VaultPubKey: insecure.PubKey, ToAddress: GetRandomBTCAddress(), Coin: common.NewCoin(common.BTCAsset, cosmos.NewUint(1*common.One))},
		TxOutItem{Chain: common.THORChain, VaultPubKey: insecure.PubKey, ToAddress: GetRandomRUNEAddress(), Coin: common.NewCoin(common.RuneAsset(), cosmos.NewUint(1*common.One))},
		TxOutItem{Chain: common.BTCChain, VaultPubKey: GetRandomPubKey(), ToAddress: btcAddr, Coin: common.NewCoin(common.BTCAsset, cosmos.NewUint(2*common.One))},
		TxOutItem{Chain: common.BTCChain, VaultPubKey: GetRandomPubKey(), ToAddress: thorAddr, Coin: common.NewCoin(common.RuneAsset(), cosmos.NewUint(1))},
		TxOutItem{Chain: common.BTCChain, VaultPubKey: insecure.PubKey, ToAddress: btcAddr, OutHash: GetRandomTxHash(), Coin: common.NewCoin(common.BTCAsset, cosmos.NewUint(common.One))},
	)
	c.Assert(k.SetTxOut(ctx, txOut), IsNil)

	vaults := Vaults{secure, insecure}
	c.Check(k.GetMostSecure(ctx, vaults, 5).PubKey, Equals, secure.PubKey)
	c.Check(k.GetLeastSecure(ctx, vaults, 5).PubKey, Equals, insecure.PubKey)
	// a single vault is returned unchanged
	c.Check(k.SortBySecurity(ctx, Vaults{insecure}, 5), HasLen, 1)

	sec := k.getSortedVaultSecurity(ctx, vaults, 5)
	c.Assert(sec, HasLen, 2)
	c.Check(sec[0].TotalBond.IsZero(), Equals, true)
	// 50 BTC - 1 BTC sent - 1 RUNE sent + 2 BTC received + 1e-8 RUNE received
	c.Check(sec[0].TotalValue.Uint64(), Equals, uint64(50*common.One)-common.One-common.One+2*common.One+1)
	c.Check(sec[0].Diff.IsNegative(), Equals, true)
	c.Check(sec[1].TotalBond.Uint64(), Equals, uint64(1000*common.One))
	c.Check(sec[1].TotalValue.Uint64(), Equals, uint64(10*common.One))
}

func (s *KeeperVaultNodeCovSuite) TestVaultIndexes(c *C) {
	ctx, k := setupKeeperForTest(c)

	v := types.GetRandomVault()
	v.PubKeyEddsa = types.GetRandomEd25519PubKey()
	v.PubKeyFrost = GetRandomPubKey()
	c.Assert(k.SetVault(ctx, v), IsNil)

	// the vault is reachable via any of its keys
	for _, pk := range []common.PubKey{v.PubKey, v.PubKeyEddsa, v.PubKeyFrost} {
		c.Check(k.VaultExists(ctx, pk), Equals, true)
		got, err := k.GetVault(ctx, pk)
		c.Assert(err, IsNil)
		c.Check(got.PubKey, Equals, v.PubKey)
	}
	unknown := GetRandomPubKey()
	c.Check(k.VaultExists(ctx, unknown), Equals, false)
	_, err := k.GetVault(ctx, unknown)
	c.Check(errors.Is(err, kvTypes.ErrVaultNotFound), Equals, true)

	// corrupt eddsa / frost index entries
	badEddsa := GetRandomPubKey()
	corruptKeyCov(ctx, k, k.GetKey(prefixVaultAsgardEDDSAIndex, badEddsa.String()))
	c.Check(k.VaultExists(ctx, badEddsa), Equals, false)
	_, err = k.GetVault(ctx, badEddsa)
	c.Check(errors.Is(err, kvTypes.ErrVaultNotFound), Equals, true)
	badFrost := GetRandomPubKey()
	corruptKeyCov(ctx, k, k.GetKey(prefixVaultAsgardFrostIndex, badFrost.String()))
	c.Check(k.VaultExists(ctx, badFrost), Equals, false)
	_, err = k.GetVault(ctx, badFrost)
	c.Check(errors.Is(err, kvTypes.ErrVaultNotFound), Equals, true)

	// HasValidVaultPools
	ok, err := k.HasValidVaultPools(ctx)
	c.Assert(err, IsNil)
	c.Check(ok, Equals, false)
	v.Coins = common.NewCoins(common.NewCoin(common.BTCAsset, cosmos.NewUint(1)))
	c.Assert(k.SetVault(ctx, v), IsNil)
	ok, err = k.HasValidVaultPools(ctx)
	c.Assert(err, IsNil)
	c.Check(ok, Equals, true)

	// DeleteVault: funds block deletion, unknown is a no-op
	c.Check(k.DeleteVault(ctx, v.PubKey), ErrorMatches, "unable to delete vault.*")
	c.Assert(k.DeleteVault(ctx, GetRandomPubKey()), IsNil)
	v.Coins = nil
	c.Assert(k.SetVault(ctx, v), IsNil)
	c.Assert(k.DeleteVault(ctx, v.PubKey), IsNil)
	asgards, err := k.GetAsgardVaults(ctx)
	c.Assert(err, IsNil)
	c.Check(asgards, HasLen, 0)

	// a corrupt vault record fails to delete and breaks HasValidVaultPools
	bad := GetRandomPubKey()
	corruptKeyCov(ctx, k, k.GetKey(prefixVault, bad.String()))
	err = k.DeleteVault(ctx, bad)
	c.Check(err, NotNil)
	c.Check(errors.Is(err, kvTypes.ErrVaultNotFound), Equals, false)
	_, err = k.HasValidVaultPools(ctx)
	c.Check(err, NotNil)
}

func (s *KeeperVaultNodeCovSuite) TestAsgardIndexCorrupt(c *C) {
	ctx, k := setupKeeperForTest(c)

	// an asgard index referencing a missing vault surfaces a not found error
	k.setStrings(ctx, k.GetKey(prefixVaultAsgardIndex, ""), []string{GetRandomPubKey().String()})
	_, err := k.GetAsgardVaults(ctx)
	c.Check(errors.Is(err, kvTypes.ErrVaultNotFound), Equals, true)

	// an invalid pubkey in the index breaks every index operation
	k.setStrings(ctx, k.GetKey(prefixVaultAsgardIndex, ""), []string{"notapubkey"})
	_, err = k.GetAsgardVaults(ctx)
	c.Check(err, NotNil)
	_, err = k.GetAsgardVaultsByStatus(ctx, ActiveVault)
	c.Check(err, NotNil)
	c.Check(k.SetVault(ctx, types.GetRandomVault()), NotNil)
	c.Check(k.RemoveFromAsgardIndex(ctx, GetRandomPubKey()), NotNil)

	corruptKeyCov(ctx, k, k.GetKey(prefixVaultAsgardIndex, ""))
	_, err = k.GetAsgardVaults(ctx)
	c.Check(err, NotNil)
}

func (s *KeeperVaultNodeCovSuite) TestNodeAccountMisc(c *C) {
	ctx, k := setupKeeperForTest(c)

	// lowest active version
	c.Check(k.GetLowestActiveVersion(ctx).Equals(constants.SWVersion), Equals, true)
	n1 := GetRandomValidatorNode(NodeActive)
	n1.Version = "1.2.3"
	n2 := GetRandomValidatorNode(NodeActive)
	n2.Version = "1.1.0"
	c.Assert(k.SetNodeAccount(ctx, n1), IsNil)
	c.Assert(k.SetNodeAccount(ctx, n2), IsNil)
	c.Check(k.GetLowestActiveVersion(ctx).String(), Equals, "1.1.0")

	// lookup by pubkey
	na, err := k.GetNodeAccountByPubKey(ctx, n1.PubKeySet.Secp256k1)
	c.Assert(err, IsNil)
	c.Check(na.NodeAddress.Equals(n1.NodeAddress), Equals, true)
	_, err = k.GetNodeAccountByPubKey(ctx, common.PubKey("bogus"))
	c.Check(err, NotNil)

	// empty accounts are not stored
	c.Assert(k.SetNodeAccount(ctx, NodeAccount{}), IsNil)

	// first activation sets the active height and clears slash points
	n3 := GetRandomValidatorNode(NodeActive)
	n3.ActiveBlockHeight = 0
	k.SetNodeAccountSlashPoints(ctx, n3.NodeAddress, 9)
	c.Assert(k.SetNodeAccount(ctx.WithBlockHeight(33), n3), IsNil)
	got, err := k.GetNodeAccount(ctx, n3.NodeAddress)
	c.Assert(err, IsNil)
	c.Check(got.ActiveBlockHeight, Equals, int64(33))
	pts, err := k.GetNodeAccountSlashPoints(ctx, n3.NodeAddress)
	c.Assert(err, IsNil)
	c.Check(pts, Equals, int64(0))

	// slash points never go negative
	k.SetNodeAccountSlashPoints(ctx, n3.NodeAddress, -5)
	pts, err = k.GetNodeAccountSlashPoints(ctx, n3.NodeAddress)
	c.Assert(err, IsNil)
	c.Check(pts, Equals, int64(0))
	c.Assert(k.IncNodeAccountSlashPoints(ctx, n3.NodeAddress, 4), IsNil)
	c.Assert(k.DecNodeAccountSlashPoints(ctx, n3.NodeAddress, 10), IsNil)
	pts, err = k.GetNodeAccountSlashPoints(ctx, n3.NodeAddress)
	c.Assert(err, IsNil)
	c.Check(pts, Equals, int64(0))

	// corrupt slash points
	bad := GetRandomBech32Addr()
	corruptKeyCov(ctx, k, k.GetKey(prefixNodeSlashPoints, bad.String()))
	c.Check(k.IncNodeAccountSlashPoints(ctx, bad, 1), NotNil)
	c.Check(k.DecNodeAccountSlashPoints(ctx, bad, 1), NotNil)

	// jail release
	c.Assert(k.SetNodeAccountJail(ctx, n1.NodeAddress, 100, "bad"), IsNil)
	c.Assert(k.ReleaseNodeAccountFromJail(ctx.WithBlockHeight(50), n1.NodeAddress), IsNil)
	jail, err := k.GetNodeAccountJail(ctx, n1.NodeAddress)
	c.Assert(err, IsNil)
	c.Check(jail.ReleaseHeight, Equals, int64(50))
	c.Check(jail.Reason, Equals, "")
	corruptKeyCov(ctx, k, k.GetKey(prefixNodeJail, bad.String()))
	c.Check(k.ReleaseNodeAccountFromJail(ctx, bad), NotNil)
	c.Check(k.SetNodeAccountJail(ctx, bad, 1, "x"), NotNil)
}

func (s *KeeperVaultNodeCovSuite) TestDeductNativeTxFeeFromBond(c *C) {
	ctx, k := setupKeeperForTest(c)
	fee := k.GetNativeTxFee(ctx)
	c.Assert(fee.IsZero(), Equals, false)

	na := GetRandomValidatorNode(NodeActive)
	na.Bond = fee.MulUint64(3)
	c.Assert(k.SetNodeAccount(ctx, na), IsNil)

	// bond module has no funds yet
	c.Check(k.DeductNativeTxFeeFromBond(ctx, na.NodeAddress), NotNil)

	FundModule(c, ctx, k, BondName, fee.Uint64()*10)
	na.Bond = fee.MulUint64(3)
	c.Assert(k.SetNodeAccount(ctx, na), IsNil)
	reserve := k.GetRuneBalanceOfModule(ctx, ReserveName)
	c.Assert(k.DeductNativeTxFeeFromBond(ctx, na.NodeAddress), IsNil)
	got, err := k.GetNodeAccount(ctx, na.NodeAddress)
	c.Assert(err, IsNil)
	c.Check(got.Bond.Equal(fee.MulUint64(2)), Equals, true)
	c.Check(k.GetRuneBalanceOfModule(ctx, ReserveName).Equal(reserve.Add(fee)), Equals, true)

	// not enough bond
	poor := GetRandomValidatorNode(NodeActive)
	poor.Bond = cosmos.ZeroUint()
	c.Assert(k.SetNodeAccount(ctx, poor), IsNil)
	c.Check(k.DeductNativeTxFeeFromBond(ctx, poor.NodeAddress), ErrorMatches, "not enound bond for fee")

	// corrupt node account
	bad := GetRandomBech32Addr()
	corruptKeyCov(ctx, k, k.GetKey(prefixNodeAccount, bad.String()))
	c.Check(k.DeductNativeTxFeeFromBond(ctx, bad), NotNil)

	// zero fee is a no-op
	k.SetMimir(ctx, constants.NativeTransactionFee.String(), 0)
	c.Assert(k.DeductNativeTxFeeFromBond(ctx, poor.NodeAddress), IsNil)
}

func (s *KeeperVaultNodeCovSuite) TestRemoveLowBondValidatorAccounts(c *C) {
	ctx, k := setupKeeperForTest(c)

	// zero bond standby validator is removed
	zero := GetRandomValidatorNode(NodeStandby)
	zero.Bond = cosmos.ZeroUint()
	c.Assert(k.SetNodeAccount(ctx, zero), IsNil)

	// small bond without providers is returned to the bond address
	small := GetRandomValidatorNode(NodeStandby)
	small.Bond = cosmos.NewUint(100)
	c.Assert(k.SetNodeAccount(ctx, small), IsNil)

	// small bond with a provider is returned to the provider
	withBP := GetRandomValidatorNode(NodeStandby)
	withBP.Bond = cosmos.NewUint(200)
	c.Assert(k.SetNodeAccount(ctx, withBP), IsNil)
	provider := GetRandomBech32Addr()
	bps := NewBondProviders(withBP.NodeAddress)
	bp := NewBondProvider(provider)
	bp.Bond = cosmos.NewUint(200)
	bps.Providers = append(bps.Providers, bp)
	c.Assert(k.SetBondProviders(ctx, bps), IsNil)

	// active and well bonded nodes are kept
	active := GetRandomValidatorNode(NodeActive)
	c.Assert(k.SetNodeAccount(ctx, active), IsNil)

	FundModule(c, ctx, k, BondName, 1000)
	c.Assert(k.RemoveLowBondValidatorAccounts(ctx), IsNil)

	for _, removed := range []NodeAccount{zero, small, withBP} {
		got, err := k.GetNodeAccount(ctx, removed.NodeAddress)
		c.Assert(err, IsNil)
		c.Check(got.Status, Equals, types.NodeStatus_Unknown)
	}
	got, err := k.GetNodeAccount(ctx, active.NodeAddress)
	c.Assert(err, IsNil)
	c.Check(got.Status, Equals, NodeActive)

	smallTo, err := small.BondAddress.AccAddress()
	c.Assert(err, IsNil)
	c.Check(k.GetBalanceOf(ctx, smallTo, common.RuneNative).Amount.Int64(), Equals, int64(100))
	c.Check(k.GetBalanceOf(ctx, provider, common.RuneNative).Amount.Int64(), Equals, int64(200))
	c.Check(k.has(ctx, k.GetKey(prefixBondProviders, withBP.NodeAddress.String())), Equals, false)
}

func (s *KeeperVaultNodeCovSuite) TestRemoveLowBondValidatorAccountsSendFailures(c *C) {
	ctx, k := setupKeeperForTest(c)

	// bond module is empty: sends fail, the account without providers is
	// kept untouched and the one with providers keeps its bond
	small := GetRandomValidatorNode(NodeStandby)
	small.Bond = cosmos.NewUint(100)
	c.Assert(k.SetNodeAccount(ctx, small), IsNil)
	withBP := GetRandomValidatorNode(NodeStandby)
	withBP.Bond = cosmos.NewUint(200)
	c.Assert(k.SetNodeAccount(ctx, withBP), IsNil)
	bps := NewBondProviders(withBP.NodeAddress)
	bp := NewBondProvider(GetRandomBech32Addr())
	bp.Bond = cosmos.NewUint(200)
	bps.Providers = append(bps.Providers, bp)
	c.Assert(k.SetBondProviders(ctx, bps), IsNil)

	c.Assert(k.RemoveLowBondValidatorAccounts(ctx), IsNil)
	got, err := k.GetNodeAccount(ctx, small.NodeAddress)
	c.Assert(err, IsNil)
	c.Check(got.Bond.Uint64(), Equals, uint64(100))
	got, err = k.GetNodeAccount(ctx, withBP.NodeAddress)
	c.Assert(err, IsNil)
	c.Check(got.Bond.Uint64(), Equals, uint64(200))
	c.Check(k.has(ctx, k.GetKey(prefixBondProviders, withBP.NodeAddress.String())), Equals, true)

	// an invalid bond address is a db error
	badAddr := GetRandomValidatorNode(NodeStandby)
	badAddr.Bond = cosmos.NewUint(5)
	badAddr.BondAddress = common.Address("bogus")
	c.Assert(k.SetNodeAccount(ctx, badAddr), IsNil)
	c.Check(k.RemoveLowBondValidatorAccounts(ctx), NotNil)
}

func (s *KeeperVaultNodeCovSuite) TestRemoveLowBondCorruptAccounts(c *C) {
	ctx, k := setupKeeperForTest(c)
	corruptKeyCov(ctx, k, k.GetKey(prefixNodeAccount, GetRandomBech32Addr().String()))
	c.Check(k.RemoveLowBondValidatorAccounts(ctx), NotNil)
	_, err := k.ListActiveValidators(ctx)
	c.Check(err, NotNil)
	c.Check(k.GetMinJoinVersion(ctx).String(), Equals, "0.0.0")
	c.Check(k.GetLowestActiveVersion(ctx).Equals(constants.SWVersion), Equals, true)
	_, err = UpgradeApprovedByMajority(ctx, k, "1.2.3")
	c.Check(err, NotNil)
	c.Check(UpdateActiveValidatorVersions(ctx, k, "1.2.3"), NotNil)
}

func (s *KeeperVaultNodeCovSuite) TestUpgradeVotesAndVersions(c *C) {
	ctx, k := setupKeeperForTest(c)
	addr1, addr2 := GetRandomBech32Addr(), GetRandomBech32Addr()

	_, err := k.GetUpgradeVote(ctx, addr1, "1.2.3")
	c.Check(err, ErrorMatches, "no vote found.*")
	k.ApproveUpgrade(ctx, addr1, "1.2.3")
	k.RejectUpgrade(ctx, addr2, "1.2.3")
	vote, err := k.GetUpgradeVote(ctx, addr1, "1.2.3")
	c.Assert(err, IsNil)
	c.Check(vote, Equals, true)
	vote, err = k.GetUpgradeVote(ctx, addr2, "1.2.3")
	c.Assert(err, IsNil)
	c.Check(vote, Equals, false)

	// update all active validators to a version
	n1 := GetRandomValidatorNode(NodeActive)
	n2 := GetRandomValidatorNode(NodeActive)
	standby := GetRandomValidatorNode(NodeStandby)
	standby.Version = "0.1.0"
	for _, na := range []NodeAccount{n1, n2, standby} {
		c.Assert(k.SetNodeAccount(ctx, na), IsNil)
	}
	c.Assert(UpdateActiveValidatorVersions(ctx, k, "7.8.9"), IsNil)
	for _, na := range []NodeAccount{n1, n2} {
		updated, getErr := k.GetNodeAccount(ctx, na.NodeAddress)
		c.Assert(getErr, IsNil)
		c.Check(updated.Version, Equals, "7.8.9")
	}
	got, err := k.GetNodeAccount(ctx, standby.NodeAddress)
	c.Assert(err, IsNil)
	c.Check(got.Version, Equals, "0.1.0")
	ver, height := k.GetMinJoinLast(ctx)
	c.Check(ver.String(), Equals, "7.8.9")
	c.Check(height, Equals, ctx.BlockHeight())
	events := 0
	for _, e := range ctx.EventManager().Events() {
		if e.Type == "set_version" {
			events++
		}
	}
	c.Check(events, Equals, 2)

	// corrupt upgrade proposals fail removal
	corruptKeyCov(ctx, k, []byte(string(prefixUpgradeProposals)+"bad"))
	c.Check(k.RemoveExpiredUpgradeProposals(ctx), NotNil)
	_, err = k.GetProposedUpgrade(ctx, "bad")
	c.Check(err, NotNil)
}
