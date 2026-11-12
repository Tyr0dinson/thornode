package thorchain

import (
	"github.com/cometbft/cometbft/crypto/secp256k1"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
	. "gopkg.in/check.v1"
)

func (s *DashAccountingSuite) TestInputCreditBlocksBothArrivalOrders(c *C) {
	for _, earlyFirst := range []bool{false, true} {
		ctx, mgr, _, tx := dashAccountingFixture(c)
		first := tx
		first.InstantSend = nil
		if earlyFirst {
			first.InstantSend = &common.InstantSend{}
		}
		suppressed, err := recordInstantSendInputs(ctx, mgr.Keeper(), first)
		c.Assert(err, IsNil)
		c.Assert(suppressed, Equals, false)
		second := tx
		second.Tx.ID = GetRandomTxHash()
		if earlyFirst {
			second.InstantSend = nil
			suppressed, err = recordInstantSendInputs(ctx, mgr.Keeper(), second)
			c.Assert(err, IsNil)
			c.Assert(suppressed, Equals, true)
		} else {
			_, allowed, readErr := prepareInstantSendCredit(ctx, mgr, second, ObservedTxVoter{}, 3, 4)
			c.Assert(readErr, IsNil)
			c.Assert(allowed, Equals, false)
		}
	}
}

func (s *DashAccountingSuite) TestDashInputCreditRollsBackWithVaultFailure(c *C) {
	for _, legacy := range []bool{false, true} {
		ctx, mgr, vault, tx := dashAccountingFixture(c)
		tx.InstantSend = nil
		nodes := make([]NodeAccount, 4)
		keys := make([]secp256k1.PrivKey, 4)
		for i := range nodes {
			nodes[i], keys[i] = nodeWithPrivKey(c, mgr.Keeper(), ctx)
		}
		mgr.K = errataFailVaultKeeper{mgr.Keeper()}
		var err error
		if legacy {
			handler := NewObservedTxInHandler(mgr)
			for i := 0; i < 3; i++ {
				cache, commit := ctx.CacheContext()
				_, err = handler.Run(cache, NewMsgObservedTxIn(common.ObservedTxs{tx}, nodes[i].NodeAddress))
				if i < 2 {
					c.Assert(err, IsNil)
					commit()
				}
			}
		} else {
			quorum := &common.QuorumTx{ObsTx: tx, Inbound: true}
			for _, key := range keys[:3] {
				quorum.Attestations = append(quorum.Attestations, attestObservedTx(c, key, tx, true))
			}
			cache, _ := ctx.CacheContext()
			_, err = NewObservedTxQuorumHandler(mgr).Run(cache, types.NewMsgObservedTxQuorum(quorum, nodes[0].NodeAddress))
		}
		c.Assert(err, ErrorMatches, "(?s).*injected vault persistence failure.*")
		credit, readErr := mgr.Keeper().GetInstantSendInput(ctx, tx.SpentOutputRefs[0])
		c.Assert(readErr, IsNil)
		c.Assert(credit.Id.IsEmpty(), Equals, true)
		actual, readErr := mgr.Keeper().GetVault(ctx, vault.PubKey)
		c.Assert(readErr, IsNil)
		c.Assert(actual.Coins, DeepEquals, vault.Coins)
	}
}

func (s *DashAccountingSuite) TestInputExpiryAndAnchorBoundaries(c *C) {
	for _, anchor := range []int64{84, 96, 100} {
		ctx, mgr, _, tx := dashAccountingFixture(c)
		c.Assert(mgr.Keeper().SetLastChainHeight(ctx, common.DASHChain, 120), IsNil)
		tx.BlockHeight, tx.FinaliseHeight = anchor, anchor
		_, allowed, err := prepareInstantSendCredit(ctx, mgr, tx, ObservedTxVoter{}, 3, 4)
		c.Assert(err, IsNil)
		c.Assert(allowed, Equals, anchor == 96)
	}
	ctx, mgr, vault, tx := dashAccountingFixture(c)
	_, voter := creditTestDeposit(c, ctx, mgr, vault, tx)
	c.Assert(mgr.Keeper().SetLastChainHeight(ctx, common.DASHChain, 125), IsNil)
	pruneInstantSendCredits(ctx, mgr.Keeper())
	credit, err := mgr.Keeper().GetInstantSendInput(ctx, tx.SpentOutputRefs[0])
	c.Assert(err, IsNil)
	c.Assert(credit.Id, Equals, tx.Tx.ID) // Pending credits never expire.
	tx.InstantSend = nil
	tx.BlockHeight, tx.FinaliseHeight = 101, 101
	c.Assert(settleInstantSend(ctx, mgr.Keeper(), tx, voter, []cosmos.AccAddress{GetRandomBech32Addr(), GetRandomBech32Addr(), GetRandomBech32Addr()}, 4), IsNil)
	credit, err = mgr.Keeper().GetInstantSendInput(ctx, tx.SpentOutputRefs[0])
	c.Assert(err, IsNil)
	c.Assert(credit.Height, Equals, int64(125)) // Delayed settlement starts a fresh retention window.
	c.Assert(mgr.Keeper().SetLastChainHeight(ctx, common.DASHChain, 149), IsNil)
	pruneInstantSendCredits(ctx, mgr.Keeper())
	credit, err = mgr.Keeper().GetInstantSendInput(ctx, tx.SpentOutputRefs[0])
	c.Assert(err, IsNil)
	c.Assert(credit.Id, Equals, tx.Tx.ID)
	c.Assert(mgr.Keeper().SetLastChainHeight(ctx, common.DASHChain, 150), IsNil)
	pruneInstantSendCredits(ctx, mgr.Keeper())
	credit, err = mgr.Keeper().GetInstantSendInput(ctx, tx.SpentOutputRefs[0])
	c.Assert(err, IsNil)
	c.Assert(credit.Id.IsEmpty(), Equals, true)
	tx.InstantSend = &common.InstantSend{}
	tx.Tx.ID = GetRandomTxHash()
	_, allowed, err := prepareInstantSendCredit(ctx, mgr, tx, ObservedTxVoter{}, 3, 4)
	c.Assert(err, IsNil)
	c.Assert(allowed, Equals, false) // Pruning does not admit an old conflicting certificate.
	status, err := mgr.Keeper().GetInstantSendStatus(ctx, common.DASHChain)
	c.Assert(err, IsNil)
	c.Assert(status.MinHeight, Equals, int64(126))
	status.MinHeight--
	c.Assert(mgr.Keeper().SetInstantSendStatus(ctx, status), ErrorMatches, "InstantSend minimum height cannot decrease")
}

func (s *DashAccountingSuite) TestInstantSendEnableRequiresSupermajority(c *C) {
	ctx, mgr, _, _ := dashAccountingFixture(c)
	mgr.Keeper().SetMimir(ctx, "DashInstantSendEnabled", 0)
	mgr.Keeper().SetMimir(ctx, constants.OperationalVotesMin.String(), 3)
	nodes := make([]NodeAccount, 10)
	for i := range nodes {
		nodes[i], _ = nodeWithPrivKey(c, mgr.Keeper(), ctx)
		nodes[i].Bond = cosmos.ZeroUint()
		c.Assert(mgr.Keeper().SetNodeAccount(ctx, nodes[i]), IsNil)
	}
	handler := NewMimirHandler(mgr)
	for i := 0; i < 7; i++ {
		_, err := handler.Run(ctx, NewMsgMimir("DashInstantSendEnabled", 1, nodes[i].NodeAddress))
		c.Assert(err, IsNil)
		value, readErr := mgr.Keeper().GetMimir(ctx, "DashInstantSendEnabled")
		c.Assert(readErr, IsNil)
		if i < 6 {
			c.Assert(value, Equals, int64(0))
		} else {
			c.Assert(value, Equals, int64(1))
		}
	}
	for i := 0; i < 3; i++ {
		_, err := handler.Run(ctx, NewMsgMimir("HaltDASHTrading", ctx.BlockHeight(), nodes[i].NodeAddress))
		c.Assert(err, IsNil)
	}
	enabled, err := instantSendEnabled(ctx, mgr.Keeper(), common.DASHChain)
	c.Assert(err, IsNil)
	c.Assert(enabled, Equals, false)
}

func (s *DashAccountingSuite) TestNormalInputExpirySurvivesGenesis(c *C) {
	ctx, mgr, _, tx := dashAccountingFixture(c)
	tx.InstantSend = nil
	tx.BlockHeight, tx.FinaliseHeight = 50, 50
	suppressed, err := recordInstantSendInputs(ctx, mgr.Keeper(), tx)
	c.Assert(err, IsNil)
	c.Assert(suppressed, Equals, false)
	c.Assert(mgr.Keeper().SetLastChainHeight(ctx, common.DASHChain, 124), IsNil)
	pruneInstantSendCredits(ctx, mgr.Keeper())
	data := ExportGenesis(ctx, mgr.Keeper())
	c.Assert(validateInstantSendGenesis(data), IsNil)
	next, nextMgr := setupManagerForTest(c)
	initGenesis(next, nextMgr.Keeper(), data)
	credit, err := nextMgr.Keeper().GetInstantSendInput(next, tx.SpentOutputRefs[0])
	c.Assert(err, IsNil)
	c.Assert(credit.Height, Equals, int64(100)) // Observation at height 50 arrived at agreed height 100.
	status, err := nextMgr.Keeper().GetInstantSendStatus(next, common.DASHChain)
	c.Assert(err, IsNil)
	c.Assert(status.MinHeight, Equals, int64(100))
	c.Assert(nextMgr.Keeper().SetLastChainHeight(next, common.DASHChain, 125), IsNil)
	pruneInstantSendCredits(next, nextMgr.Keeper())
	credit, err = nextMgr.Keeper().GetInstantSendInput(next, tx.SpentOutputRefs[0])
	c.Assert(err, IsNil)
	c.Assert(credit.Id.IsEmpty(), Equals, true)
}
