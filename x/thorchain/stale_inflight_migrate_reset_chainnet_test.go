//go:build chainnet
// +build chainnet

package thorchain

import (
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

type StaleInFlightMigrateResetSuite struct{}

var _ = Suite(&StaleInFlightMigrateResetSuite{})

func setTestInFlightMigrate(c *C, ctx cosmos.Context, mgr *Mgrs, txIDString, sourcePubKeyString string) {
	txID, err := common.NewTxID(txIDString)
	c.Assert(err, IsNil)
	sourcePubKey, err := common.NewPubKey(sourcePubKeyString)
	c.Assert(err, IsNil)

	tx := common.Tx{
		ID:    txID,
		Chain: common.XMRChain,
		Coins: common.Coins{
			common.NewCoin(common.XMRAsset, cosmos.NewUint(common.One)),
		},
		Memo: "migrate:15333",
	}
	observed := common.ObservedTx{
		Tx:             tx,
		BlockHeight:    ctx.BlockHeight(),
		FinaliseHeight: ctx.BlockHeight(),
		ObservedPubKey: sourcePubKey,
	}
	voter := NewObservedTxVoter(txID, common.ObservedTxs{observed})
	voter.Tx = observed
	voter.Height = ctx.BlockHeight()
	mgr.Keeper().SetObservedTxOutVoter(ctx, voter)
}

func getTestInFlightMigrates(c *C, ctx cosmos.Context, mgr *Mgrs) map[string]string {
	entries := make(map[string]string)
	iter := mgr.Keeper().GetInFlightMigrateIterator(ctx)
	c.Assert(iter, NotNil)
	defer iter.Close()
	for ; iter.Valid(); iter.Next() {
		var source types.ProtoString
		c.Assert(mgr.Keeper().Cdc().Unmarshal(iter.Value(), &source), IsNil)
		entries[inFlightMigrateTxIDFromKey(string(iter.Key()))] = source.GetValue()
	}
	return entries
}

func (s *StaleInFlightMigrateResetSuite) TestChainnetStaleInFlightMigrateReset(c *C) {
	ctx, mgr := setupManagerForTest(c)

	for _, target := range chainnetStaleInFlightMigrateResetTargets {
		setTestInFlightMigrate(c, ctx, mgr, target.txID, target.sourceVault)
	}

	keepTxID := GetRandomTxHash().String()
	keepSource := GetRandomPubKey().String()
	setTestInFlightMigrate(c, ctx, mgr, keepTxID, keepSource)

	entries := getTestInFlightMigrates(c, ctx, mgr)
	c.Assert(entries, HasLen, len(chainnetStaleInFlightMigrateResetTargets)+1)

	cleared, skipped, missing, err := clearChainnetStaleInFlightMigrates(ctx, mgr)
	c.Assert(err, IsNil)
	c.Assert(cleared, Equals, len(chainnetStaleInFlightMigrateResetTargets))
	c.Assert(skipped, Equals, 0)
	c.Assert(missing, Equals, 0)

	entries = getTestInFlightMigrates(c, ctx, mgr)
	c.Assert(entries, HasLen, 1)
	c.Assert(entries[keepTxID], Equals, keepSource)

	err = runChainnetStaleInFlightMigrateResetOnce(ctx, mgr)
	c.Assert(err, IsNil)
	doneAt, err := mgr.Keeper().GetMimir(ctx, chainnetStaleInFlightMigrateResetMimirKey)
	c.Assert(err, IsNil)
	c.Assert(doneAt, Equals, ctx.BlockHeight())

	err = runChainnetStaleInFlightMigrateResetOnce(ctx, mgr)
	c.Assert(err, IsNil)
	entries = getTestInFlightMigrates(c, ctx, mgr)
	c.Assert(entries, HasLen, 1)
	c.Assert(entries[keepTxID], Equals, keepSource)
}

func (s *StaleInFlightMigrateResetSuite) TestChainnetStaleInFlightMigrateResetSkipsSourceMismatch(c *C) {
	ctx, mgr := setupManagerForTest(c)

	target := chainnetStaleInFlightMigrateResetTargets[0]
	otherSource := GetRandomPubKey().String()
	setTestInFlightMigrate(c, ctx, mgr, target.txID, otherSource)

	cleared, skipped, missing, err := clearChainnetStaleInFlightMigrates(ctx, mgr)
	c.Assert(err, IsNil)
	c.Assert(cleared, Equals, 0)
	c.Assert(skipped, Equals, 1)
	c.Assert(missing, Equals, len(chainnetStaleInFlightMigrateResetTargets)-1)

	entries := getTestInFlightMigrates(c, ctx, mgr)
	c.Assert(entries, HasLen, 1)
	c.Assert(entries[target.txID], Equals, otherSource)
}
