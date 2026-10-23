//go:build mocknet

package thorchain

import (
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/constants"
)

// The churn schedule is wall-clock time since the last churn: a BLOCKTIMEMS
// change mid-cycle neither fires the churn early nor skips it, with or without
// retries.
func (vts *ValidatorMgrTestSuite) TestChurnSurvivesBlockTimeChange(c *C) {
	churnedBy := func(blockMs, retryInterval int64) int64 {
		ctx, mgr := setupManagerForTest(c)
		k := mgr.Keeper()
		ctx = ctx.WithBlockHeight(50)

		k.SetMimir(ctx, constants.ChurnInterval.String(), 30)
		k.SetMimir(ctx, constants.ChurnRetryInterval.String(), retryInterval)
		k.SetMimir(ctx, constants.DesiredValidatorSet.String(), 4)
		k.SetMimir(ctx, constants.AsgardSize.String(), 10)

		membership := make(common.PubKeys, 0, 5)
		for i := 0; i < 5; i++ {
			na := GetRandomValidatorNode(NodeActive)
			c.Assert(k.SetNodeAccount(ctx, na), IsNil)
			membership = append(membership, na.PubKeySet.Secp256k1)
		}
		vault := NewVault(30, ActiveVault, AsgardVault, GetRandomPubKey(), common.Chains{common.THORChain, common.BTCChain}.Strings(), nil)
		vault.Membership = membership.Strings()
		c.Assert(k.SetVault(ctx, vault), IsNil)

		// the last churn was at 30; blocks 31-50 at 1000ms are 20 of the 30
		// reference blocks of the interval
		k.SetMimir(ctx, constants.BlockTimeMs.String(), blockMs)

		for h := int64(51); h <= 80; h++ {
			hctx := ctx.WithBlockHeight(h)
			k.UpdateBlockTime(hctx) // as module BeginBlock does
			c.Assert(k.GetConfigInt64(hctx, constants.ChurnInterval), Equals, 30*1000/blockMs)
			c.Assert(mgr.ValidatorMgr().BeginBlock(hctx, mgr, nil), IsNil)
			kb, err := k.GetKeygenBlock(hctx, h)
			c.Assert(err, IsNil)
			if len(kb.Keygens) > 0 {
				return h
			}
		}
		return 0
	}

	// the other 10 are 5 blocks at 2000ms, though the interval in blocks (15)
	// already lies behind
	c.Check(churnedBy(2000, 10), Equals, int64(55))
	c.Check(churnedBy(2000, 0), Equals, int64(55))
	// or 20 blocks at 500ms, though the interval in blocks is now 60
	c.Check(churnedBy(500, 10), Equals, int64(70))
}

// At a faster block time a keygen's age in reference blocks rounds down, so a
// keygen the result check still accepts can sit one block further back than a
// plain window of the validity reaches. hasPendingKeygen must still see it, or
// the churn would schedule its replacement while a result can still finalize.
func (vts *ValidatorMgrTestSuite) TestHasPendingKeygenAtFasterBlockTime(c *C) {
	ctx, k := setupKeeperForTest(c)
	// valid for 399 reference blocks, past the XMR keygen's 6-minute minimum
	// that an Asgard lookback also covers
	k.SetMimir(ctx, constants.ChurnRetryInterval.String(), 400)
	k.SetMimir(ctx, constants.BlockTimeMs.String(), 500)
	k.UpdateBlockTime(ctx.WithBlockHeight(1))
	mgr := NewDummyMgrWithKeeper(k)
	validatorMgr := newValidatorMgr(k, mgr.NetworkMgr(), mgr.TxOutStore(), mgr.EventMgr())

	const keygenHeight int64 = 20
	members := common.PubKeys{GetRandomPubKey(), GetRandomPubKey(), GetRandomPubKey()}
	keygen, err := NewKeygen(keygenHeight, members.Strings(), AsgardKeygen)
	c.Assert(err, IsNil)
	keygenBlock := NewKeygenBlock(keygenHeight)
	keygenBlock.Keygens = []Keygen{keygen}
	k.SetKeygenBlock(ctx, keygenBlock)

	// 799 blocks of 500ms: 399.5 reference blocks, 399 rounded down, still valid
	at := ctx.WithBlockHeight(keygenHeight + 799)
	c.Assert(k.RefBlocksSince(at, keygenHeight), Equals, int64(399))
	pending, err := validatorMgr.hasPendingKeygen(at, AsgardKeygen)
	c.Assert(err, IsNil)
	c.Check(pending, Equals, true)
	// 800 blocks: 400 reference blocks, expired
	pending, err = validatorMgr.hasPendingKeygen(ctx.WithBlockHeight(keygenHeight+800), AsgardKeygen)
	c.Assert(err, IsNil)
	c.Check(pending, Equals, false)
}
