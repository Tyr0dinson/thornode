package thorchain

import (
	. "gopkg.in/check.v1"
)

type HandlerRagnarokCovCESuite struct{}

var _ = Suite(&HandlerRagnarokCovCESuite{})

func (s *HandlerRagnarokCovCESuite) TestRagnarokCovCE(c *C) {
	ctx, mgr := setupManagerForTest(c)
	ctx = ctx.WithBlockHeight(10)
	fx := newCovCEOutboundFixture(c, NewRagnarokMemo(1).String())
	c.Assert(mgr.Keeper().SetTxOut(ctx, fx.txOut), IsNil)
	mgr.Keeper().SetRagnarokPending(ctx, 2)
	k := mgr.Keeper()

	slasher := &covCEFailSlasher{Slasher: mgr.slasher}
	mgr.slasher = slasher

	// keeper failures
	mgr.K = newCovCEFailKeeper(k, "GetTxOut")
	_, err := NewRagnarokHandler(mgr).Run(ctx, NewMsgRagnarok(fx.observed, 1, GetRandomBech32Addr()))
	c.Check(err, ErrorMatches, "(?s)unable to get txOut record.*kaboom.*")
	mgr.K = newCovCEFailKeeper(k, "SetTxOut")
	_, err = NewRagnarokHandler(mgr).Run(ctx, NewMsgRagnarok(fx.observed, 1, GetRandomBech32Addr()))
	c.Check(err, ErrorMatches, "(?s)fail to save tx out.*kaboom.*")
	mgr.K = k

	// gas override match, pending count decremented, no slash
	mgr.K = newCovCEFailKeeper(k, "SetLastSignedHeight")
	res, err := NewRagnarokHandler(mgr).Run(ctx, NewMsgRagnarok(fx.observed, 1, GetRandomBech32Addr()))
	mgr.K = k
	c.Assert(err, IsNil)
	c.Assert(res, NotNil)
	c.Check(slasher.calls, Equals, 0)
	pending, err := k.GetRagnarokPending(ctx)
	c.Assert(err, IsNil)
	c.Check(pending, Equals, int64(1))
	stored, err := k.GetTxOut(ctx, 1)
	c.Assert(err, IsNil)
	c.Check(stored.TxArray[3].OutHash.Equals(fx.observed.Tx.ID), Equals, true)

	// a second observation finds no unsigned item: slash, failure surfaced
	other := fx.observed
	other.Tx.ID = GetRandomTxHash()
	slasher.fail = true
	_, err = NewRagnarokHandler(mgr).Run(ctx, NewMsgRagnarok(other, 1, GetRandomBech32Addr()))
	c.Check(err, ErrorMatches, "(?s)fail to slash account.*kaboom.*")
	c.Check(slasher.calls, Equals, 1)
}
