//go:build mainnet

package thorchain

import (
	sdk "github.com/cosmos/cosmos-sdk/types"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

type Migrate21to22XRPSuite struct{}

var _ = Suite(&Migrate21to22XRPSuite{})

// xrpDropMigrationFrom is the module version slot this migration runs in.
const xrpDropMigrationFrom = uint64(21)

// xrpDropSlotRun returns the registered run func for the 21to22 slot and how
// many slots carry that number (which must be exactly one).
func xrpDropSlotRun(mgr *Mgrs) (func(sdk.Context) error, int) {
	var run func(sdk.Context) error
	count := 0
	for _, mig := range NewMigrator(mgr).luviMigrations() {
		if mig.from == xrpDropMigrationFrom {
			run = mig.run
			count++
		}
	}
	return run, count
}

// seedSecuredXRP gives seeder a secured XRP balance of amt (1e8 units) so the
// secured pool exists with a non-zero depth and share supply.
func seedSecuredXRP(c *C, ctx cosmos.Context, mgr *Mgrs, seeder cosmos.AccAddress, amt uint64) {
	_, err := mgr.SecuredAssetManager().Deposit(
		ctx, common.XRPAsset, cosmos.NewUint(amt), seeder,
		common.Address("rSeederXRPaddr"), GetRandomTxHash(),
	)
	c.Assert(err, IsNil)
}

// securedXRPBalance returns owner's secured XRP balance in 1e8 units.
func securedXRPBalance(ctx cosmos.Context, mgr *Mgrs, owner cosmos.AccAddress) uint64 {
	return mgr.SecuredAssetManager().BalanceOf(ctx, common.XRPAsset, owner).Uint64()
}

// xrpOutboundAmount is the scheduled outbound coin (the re-credited 150 XRP
// minus the ~0.241174 XRP outbound fee); distinct from the re-credit amount.
const xrpOutboundAmount = uint64(14_975_882_600)

// stuckXRPItem builds the undeliverable outbound TxOut item.
func stuckXRPItem(vaultKey common.PubKey) TxOutItem {
	return TxOutItem{
		Chain:       common.XRPChain,
		ToAddress:   common.Address(xrpStuckToAddress),
		VaultPubKey: vaultKey,
		Coin:        common.NewCoin(common.XRPAsset, cosmos.NewUint(xrpOutboundAmount)),
		Memo:        "OUT:" + xrpStuckInHash,
		InHash:      common.TxID(xrpStuckInHash),
	}
}

// The migration neutralises the live stuck XRP outbound (OutHash set to
// BlankTxID so bifrost never signs it and the slasher never reschedules it) and
// restores the owner's secured XRP by exactly the withdrawn amount, run only
// through its registered slot. Already-done siblings and unrelated items are
// untouched.
func (s *Migrate21to22XRPSuite) TestDropAndRecredit(c *C) {
	ctx, mgr := setupManagerForTest(c)
	k := mgr.Keeper()

	owner, err := cosmos.AccAddressFromBech32(xrpRecreditOwner)
	c.Assert(err, IsNil)

	// Seed an unrelated secured XRP holder so the pool has depth and supply.
	const seedAmt = uint64(1_000_000_000_000) // 10,000 XRP
	seedSecuredXRP(c, ctx, mgr, GetRandomBech32Addr(), seedAmt)

	asset := common.XRPAsset.GetSecuredAsset()
	poolBefore, err := k.GetSecuredAsset(ctx, asset)
	c.Assert(err, IsNil)
	c.Assert(securedXRPBalance(ctx, mgr, owner), Equals, uint64(0))

	// Place the live stuck item, plus an already-signed sibling of the same
	// inbound and an unrelated live item, at the current height.
	height := ctx.BlockHeight()
	vaultKey := GetRandomPubKey()
	txs := NewTxOut(height)

	live := stuckXRPItem(vaultKey)
	done := stuckXRPItem(vaultKey)
	done.OutHash = common.TxID("1111111111111111111111111111111111111111111111111111111111111111")
	decoy := stuckXRPItem(vaultKey)
	decoy.InHash = common.TxID("AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA")
	// Same inbound hash but a different destination must not be neutralised.
	wrongDest := stuckXRPItem(vaultKey)
	wrongDest.ToAddress = common.Address("rOtherXRPdestination9999999999999")
	txs.TxArray = append(txs.TxArray, live, done, decoy, wrongDest)
	c.Assert(k.SetTxOut(ctx, txs), IsNil)

	run, count := xrpDropSlotRun(mgr)
	c.Assert(count, Equals, 1)
	c.Assert(run, NotNil)
	c.Assert(run(ctx), IsNil)

	// The live item is neutralised; the done sibling and the decoy are untouched.
	after, err := k.GetTxOut(ctx, height)
	c.Assert(err, IsNil)
	c.Assert(after.TxArray, HasLen, 4)
	c.Check(after.TxArray[0].OutHash.Equals(common.BlankTxID), Equals, true, Commentf("live item not neutralised"))
	c.Check(after.TxArray[1].OutHash.String(), Equals, done.OutHash.String(), Commentf("done sibling changed"))
	c.Check(after.TxArray[2].OutHash.IsEmpty(), Equals, true, Commentf("decoy neutralised"))
	c.Check(after.TxArray[3].OutHash.IsEmpty(), Equals, true, Commentf("wrong-destination item neutralised"))

	// The owner's secured XRP is restored by exactly the withdrawn amount
	// (1:1 shares at the seeded depth), and the depth grows by the same amount.
	c.Check(securedXRPBalance(ctx, mgr, owner), Equals, xrpRecreditAmount)
	poolAfter, err := k.GetSecuredAsset(ctx, asset)
	c.Assert(err, IsNil)
	c.Check(poolAfter.Depth.Sub(poolBefore.Depth).Uint64(), Equals, xrpRecreditAmount)
}

// All-or-none: if no live stuck item is found in the scan window, the migration
// re-credits nothing and moves no secured depth.
func (s *Migrate21to22XRPSuite) TestNoLiveItemAborts(c *C) {
	ctx, mgr := setupManagerForTest(c)
	k := mgr.Keeper()

	owner, err := cosmos.AccAddressFromBech32(xrpRecreditOwner)
	c.Assert(err, IsNil)
	seedSecuredXRP(c, ctx, mgr, GetRandomBech32Addr(), uint64(1_000_000_000_000))

	asset := common.XRPAsset.GetSecuredAsset()
	poolBefore, err := k.GetSecuredAsset(ctx, asset)
	c.Assert(err, IsNil)

	run, _ := xrpDropSlotRun(mgr)
	c.Assert(run(ctx), IsNil)

	c.Check(securedXRPBalance(ctx, mgr, owner), Equals, uint64(0), Commentf("owner credited without a drop"))
	poolAfter, err := k.GetSecuredAsset(ctx, asset)
	c.Assert(err, IsNil)
	c.Check(poolAfter.Depth.Equal(poolBefore.Depth), Equals, true, Commentf("depth moved without a drop"))
}

// recreditSecuredXRP errors and mints nothing on an unparsable owner.
func (s *Migrate21to22XRPSuite) TestRecreditErrorsOnBadOwner(c *C) {
	ctx, mgr := setupManagerForTest(c)
	k := mgr.Keeper()
	seedSecuredXRP(c, ctx, mgr, GetRandomBech32Addr(), uint64(1_000_000_000_000))

	asset := common.XRPAsset.GetSecuredAsset()
	poolBefore, err := k.GetSecuredAsset(ctx, asset)
	c.Assert(err, IsNil)

	_, err = recreditSecuredXRP(ctx, k, "thor1notavalidaddress", xrpRecreditAmount)
	c.Assert(err, NotNil)

	poolAfter, err := k.GetSecuredAsset(ctx, asset)
	c.Assert(err, IsNil)
	c.Check(poolAfter.Depth.Equal(poolBefore.Depth), Equals, true)
}
