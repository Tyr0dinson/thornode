package thorchain

import (
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
)

// A lost Bifrost delivery acknowledgement can replay an identical final DASH
// attestation after commitment. The quorum path must neither process nor slash twice.
func (s *HandlerObservedTxHelpersSuite) TestDashDeliveryReplayAfterCommitDoesNotProcessOrSlash(c *C) {
	for _, inbound := range []bool{true, false} {
		ctx, mgr := setupManagerForTest(c)
		ctx = ctx.WithBlockHeight(1000) // well beyond the original observation window
		na := GetRandomValidatorNode(NodeActive)
		c.Assert(mgr.Keeper().SetNodeAccount(ctx, na), IsNil)
		mgr.Keeper().SetNodeAccountSlashPoints(ctx, na.NodeAddress, 42)
		observed := GetRandomObservedTx()
		observed.Tx.Chain = common.DASHChain
		observed.Tx.Coins = common.NewCoins(common.NewCoin(common.DASHAsset, cosmos.NewUint(common.One)))
		observed.Tx.Gas = common.Gas{common.NewCoin(common.DASHAsset, cosmos.NewUint(1000))}
		observed.BlockHeight = 100
		observed.FinaliseHeight = 100
		observed.Signers = nil
		stored := observed
		stored.Signers = []string{na.NodeAddress.String()}
		voter := NewObservedTxVoter(observed.Tx.ID, ObservedTxs{stored})
		voter.Tx = stored
		voter.Height = 100
		voter.FinalisedHeight = 100
		var quorum, added bool
		if inbound {
			voter, quorum, added = applyTxInAttestation(ctx, mgr, voter, NodeAccounts{na}, observed, na.NodeAddress, false)
		} else {
			voter, quorum, added = applyTxOutAttestation(ctx, mgr, voter, NodeAccounts{na}, observed, na.NodeAddress, false)
		}
		c.Assert(quorum, Equals, false)
		c.Assert(added, Equals, false)
		c.Assert(voter.Txs, HasLen, 1)
		c.Assert(voter.Txs[0].Signers, HasLen, 1)
		points, err := mgr.Keeper().GetNodeAccountSlashPoints(ctx, na.NodeAddress)
		c.Assert(err, IsNil)
		c.Assert(points, Equals, int64(42))
		called := false
		handler := func(cosmos.Context, cosmos.Msg) (*cosmos.Result, error) {
			called = true
			return &cosmos.Result{}, nil
		}
		if inbound {
			err = handleObservedTxInQuorum(ctx, mgr, na.NodeAddress, NodeAccounts{na}, handler, observed, voter, []cosmos.AccAddress{na.NodeAddress}, quorum)
		} else {
			err = handleObservedTxOutQuorum(ctx, mgr, na.NodeAddress, NodeAccounts{na}, handler, observed, voter, []cosmos.AccAddress{na.NodeAddress}, quorum)
		}
		c.Assert(err, IsNil)
		c.Assert(called, Equals, false)
	}
}

// InstantSend and ChainLock heights contribute to the same quorum, counting each signer once.
func (s *HandlerObservedTxHelpersSuite) TestDashDeliveryMixedHeightsReachOneQuorum(c *C) {
	for _, inbound := range []bool{true, false} {
		ctx, mgr := setupManagerForTest(c)
		ctx = ctx.WithBlockHeight(1000)
		nas := make(NodeAccounts, 4)
		for i := range nas {
			nas[i] = GetRandomValidatorNode(NodeActive)
			mgr.Keeper().SetNodeAccountSlashPoints(ctx, nas[i].NodeAddress, 42)
		}
		observed := GetRandomObservedTx()
		observed.Tx.Chain = common.DASHChain
		observed.Tx.Coins = common.NewCoins(common.NewCoin(common.DASHAsset, cosmos.NewUint(common.One)))
		observed.Tx.Gas = common.Gas{common.NewCoin(common.DASHAsset, cosmos.NewUint(1000))}
		voter := NewObservedTxVoter(observed.Tx.ID, nil)
		quorums := 0
		for i, vote := range []struct {
			signer int
			height int64
		}{{0, 100}, {1, 150}, {0, 150}, {2, 150}, {3, 100}} {
			observed.BlockHeight, observed.FinaliseHeight = vote.height, vote.height
			var quorum, added bool
			if inbound {
				voter, quorum, added = applyTxInAttestation(ctx, mgr, voter, nas, observed, nas[vote.signer].NodeAddress, false)
			} else {
				voter, quorum, added = applyTxOutAttestation(ctx, mgr, voter, nas, observed, nas[vote.signer].NodeAddress, false)
			}
			c.Assert(added, Equals, true)
			c.Assert(quorum, Equals, i == 3, Commentf("vote %d must count distinct signers", i))
			if quorum {
				quorums++
			}
		}
		c.Assert(quorums, Equals, 1)
		c.Assert(voter.HasFinalised(nas), Equals, true)
		for _, na := range nas {
			points, err := mgr.Keeper().GetNodeAccountSlashPoints(ctx, na.NodeAddress)
			c.Assert(err, IsNil)
			c.Assert(points, Equals, int64(42))
		}
	}
}

// Fallback cannot execute another payment. A third height variant retains the
// accepted observation charge for vault transfers already seen by both mempool paths.
func (s *HandlerObservedTxHelpersSuite) TestDashDeliveryFallbackOverlapAccounting(c *C) {
	for _, inbound := range []bool{true, false} {
		for _, priorVariants := range []int{1, 2} {
			ctx, mgr := setupManagerForTest(c)
			ctx = ctx.WithBlockHeight(1000) // after the late-observation window
			na := GetRandomValidatorNode(NodeActive)
			c.Assert(mgr.Keeper().SetNodeAccount(ctx, na), IsNil)
			mgr.Keeper().SetNodeAccountSlashPoints(ctx, na.NodeAddress, 42)
			observed := GetRandomObservedTx()
			observed.Tx.Chain = common.DASHChain
			observed.Tx.Coins = common.NewCoins(common.NewCoin(common.DASHAsset, cosmos.NewUint(common.One)))
			observed.Tx.Gas = common.Gas{common.NewCoin(common.DASHAsset, cosmos.NewUint(1000))}
			observed.BlockHeight, observed.FinaliseHeight = 100, 100
			observed.Signers = []string{na.NodeAddress.String()}
			voter := NewObservedTxVoter(observed.Tx.ID, ObservedTxs{observed})
			voter.Tx, voter.Height, voter.FinalisedHeight = observed, 100, 100
			if priorVariants == 2 {
				ordinary := observed
				ordinary.BlockHeight, ordinary.FinaliseHeight = 123, 123
				voter.Txs = append(voter.Txs, ordinary)
			}
			observed.BlockHeight, observed.FinaliseHeight = 150, 150
			observed.Signers = nil
			var quorum, added bool
			if inbound {
				voter, quorum, added = applyTxInAttestation(ctx, mgr, voter, NodeAccounts{na}, observed, na.NodeAddress, false)
			} else {
				voter, quorum, added = applyTxOutAttestation(ctx, mgr, voter, NodeAccounts{na}, observed, na.NodeAddress, false)
			}
			c.Assert(quorum, Equals, false)
			c.Assert(added, Equals, true)
			wantPoints := int64(42)
			if priorVariants == 2 {
				wantPoints += mgr.GetConstants().GetInt64Value(constants.ObserveSlashPoints)
			}
			points, err := mgr.Keeper().GetNodeAccountSlashPoints(ctx, na.NodeAddress)
			c.Assert(err, IsNil)
			c.Assert(points, Equals, wantPoints)
			called := false
			handler := func(cosmos.Context, cosmos.Msg) (*cosmos.Result, error) {
				called = true
				return &cosmos.Result{}, nil
			}
			if inbound {
				err = handleObservedTxInQuorum(ctx, mgr, na.NodeAddress, NodeAccounts{na}, handler, observed, voter, []cosmos.AccAddress{na.NodeAddress}, quorum)
			} else {
				err = handleObservedTxOutQuorum(ctx, mgr, na.NodeAddress, NodeAccounts{na}, handler, observed, voter, []cosmos.AccAddress{na.NodeAddress}, quorum)
			}
			c.Assert(err, IsNil)
			c.Assert(called, Equals, false)
		}
	}
}

// TestDashOutboundScannerObservationCredit verifies the two scanner variants
// remain free of an extra observe point, even beyond the late-observation window.
func (s *HandlerObservedTxHelpersSuite) TestDashOutboundScannerObservationCredit(c *C) {
	for _, migration := range []bool{false, true} {
		ctx, mgr := setupManagerForTest(c)
		ctx = ctx.WithBlockHeight(100)
		nas := make(NodeAccounts, 4)
		for i := range nas {
			nas[i] = GetRandomValidatorNode(NodeActive)
			mgr.Keeper().SetNodeAccountSlashPoints(ctx, nas[i].NodeAddress, 50)
		}
		pubkey := GetRandomPubKey()
		from, err := pubkey.GetAddress(common.DASHChain)
		c.Assert(err, IsNil)
		to, err := GetRandomPubKey().GetAddress(common.DASHChain)
		c.Assert(err, IsNil)
		txID := GetRandomTxHash()
		memo := "OUT:" + txID.String()
		finalHeight := int64(150) // ChainLock inclusion
		if migration {
			memo = "MIGRATE:90"
			finalHeight = 110 // InstantSend lock cycle
		}
		mempool := common.NewObservedTx(common.Tx{
			ID: txID, Chain: common.DASHChain, FromAddress: from, ToAddress: to,
			Coins: common.NewCoins(common.NewCoin(common.DASHAsset, cosmos.NewUint(common.One))),
			Gas:   common.Gas{common.NewCoin(common.DASHAsset, cosmos.NewUint(2000))}, Memo: memo,
		}, 123, pubkey, 123)
		final := mempool
		final.BlockHeight, final.FinaliseHeight = finalHeight, finalHeight
		c.Assert(mempool.Valid(), IsNil)
		c.Assert(final.Valid(), IsNil)
		voter := NewObservedTxVoter(txID, nil)
		submit := func(tx common.ObservedTx, signer cosmos.AccAddress) {
			_, _ = processTxOutAttestation(ctx, mgr, voter, nas, tx, signer, false)
			voter, err = mgr.Keeper().GetObservedTxOutVoter(ctx, txID)
			c.Assert(err, IsNil) // reload between arrivals, as separate blocks do
		}
		for _, na := range nas {
			submit(mempool, na.NodeAddress)
		}
		c.Assert(voter.FinalisedHeight, Equals, int64(100))
		ctx = ctx.WithBlockHeight(1000)
		for _, na := range nas {
			submit(final, na.NodeAddress)
			var points int64
			points, err = mgr.Keeper().GetNodeAccountSlashPoints(ctx, na.NodeAddress)
			c.Assert(err, IsNil)
			c.Assert(points, Equals, int64(50))
		}
		// A third distinct variant still costs a point. The signer change avoids
		// creating it; the consensus spam protection itself remains unchanged.
		third := final
		third.BlockHeight, third.FinaliseHeight = 160, 160
		submit(third, nas[0].NodeAddress)
		points, err := mgr.Keeper().GetNodeAccountSlashPoints(ctx, nas[0].NodeAddress)
		c.Assert(err, IsNil)
		c.Assert(points, Equals, 50+mgr.GetConstants().GetInt64Value(constants.ObserveSlashPoints))
	}
}

// TestDashTransactionWindowGuardWaitsForLateObservers checks the guard against
// the signers present at the DASH deadline, rather than the shorter global one.
func (s *ObservationTallySuite) TestDashTransactionWindowGuardWaitsForLateObservers(c *C) {
	for _, outbound := range []bool{false, true} {
		for _, catchUp := range []bool{false, true} {
			ctx, mgr, nas := setupObservationTallyTest(c)
			k := mgr.Keeper()
			k.SetMimir(ctx, constants.ObservationDelayFlexibility.String(), 2)
			k.SetMimir(ctx, "ObservationDelayFlexibilityDASH", 5)
			k.SetMimir(ctx, constants.MissingObservationGuardBps.String(), 1000)
			tx := observeSlashOutboundTx(GetRandomTxHash(), GetRandomPubKey(), 1000, 100)
			tx.Tx.Chain = common.DASHChain
			tx.Tx.Coins = common.NewCoins(common.NewCoin(common.DASHAsset, cosmos.NewUint(common.One)))
			tx.Tx.Gas = common.Gas{common.NewCoin(common.DASHAsset, cosmos.NewUint(1000))}
			process := processTxInAttestation
			if outbound {
				process = processTxOutAttestation
			} else {
				tx.Tx.Memo = "NOOP"
			}
			voter := NewObservedTxVoter(tx.Tx.ID, nil)
			for _, na := range nas[:14] {
				voter, _ = process(ctx, mgr, voter, nas, tx, na.NodeAddress, false)
			}
			consensusHeight := ctx.BlockHeight()
			c.Assert(voter.FinalisedHeight, Equals, consensusHeight)
			for elapsed := int64(2); elapsed <= 5; elapsed++ {
				ctx = ctx.WithBlockHeight(consensusHeight + elapsed)
				if catchUp && elapsed == 5 {
					// ChainLock observations arrive on the last allowed block.
					tx.BlockHeight, tx.FinaliseHeight = 101, 101
					for _, na := range nas[14:18] {
						voter, _ = process(ctx, mgr, voter, nas, tx, na.NodeAddress, false)
					}
				}
				c.Assert(processObservationTallies(ctx, mgr), IsNil)
				guardSince, err := k.GetObservationGuardSince(ctx, common.DASHChain)
				c.Assert(err, IsNil)
				switch {
				case elapsed < 5:
					checkTally(c, ctx, mgr, nas[19], common.DASHChain, 0, 0)
					c.Assert(guardSince, Equals, int64(0))
				case catchUp:
					// Two misses out of twenty are within the 10% guard threshold.
					checkTally(c, ctx, mgr, nas[19], common.DASHChain, 1, 1)
					checkTally(c, ctx, mgr, nas[17], common.DASHChain, 0, 1)
					c.Assert(guardSince, Equals, int64(0))
				default:
					// Six misses remain a shared event and must not score anyone.
					checkTally(c, ctx, mgr, nas[19], common.DASHChain, 0, 0)
					checkTally(c, ctx, mgr, nas[0], common.DASHChain, 0, 0)
					c.Assert(guardSince, Equals, consensusHeight)
				}
			}
		}
	}
}
