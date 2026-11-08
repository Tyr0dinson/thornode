package thorchain

import (
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
)

// Observation slash credit across finality tiers (#3422). Final and non-final
// observations of the same tx land in separate voter entries and keep separate
// consensus selection, but a node's observe point in either tier must still have
// a reachable refund, and a node that observed the tx in the final tier must not
// be called a non-observer at non-final consensus. Every test starts from a
// nonzero slash balance so an over-refund of unrelated points is visible.

const finalityCreditPre = int64(50)

// finalityCreditInboundTxs builds non-final and final observations of the same
// inbound on a fast-finality chain: the non-final one is reported three blocks
// before its finalise height, the final one at it.
func finalityCreditInboundTxs(vaultPubKey common.PubKey) (common.TxID, common.ObservedTx, common.ObservedTx) {
	txID := GetRandomTxHash()
	tx := common.Tx{
		ID:          txID,
		Chain:       common.ETHChain,
		FromAddress: GetRandomETHAddress(),
		ToAddress:   GetRandomETHAddress(),
		Coins:       common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(1*common.One))),
		Gas:         common.Gas{common.NewCoin(common.ETHAsset, cosmos.NewUint(1))},
		Memo:        "SWAP:ETH.ETH",
	}
	nonFinal := common.NewObservedTx(tx, 97, vaultPubKey, 100)
	final := common.NewObservedTx(tx, 100, vaultPubKey, 100)
	return txID, nonFinal, final
}

// finalityCreditOutboundTxs is the outbound equivalent, with identical gas so
// the observations differ only in finality.
func finalityCreditOutboundTxs(vaultPubKey common.PubKey) (common.TxID, common.ObservedTx, common.ObservedTx) {
	txID := GetRandomTxHash()
	final := observeSlashOutboundTx(txID, vaultPubKey, 6947, 100)
	nonFinal := final
	nonFinal.BlockHeight = 97
	return txID, nonFinal, final
}

func finalityCreditPoints(c *C, ctx cosmos.Context, mgr *Mgrs, node cosmos.AccAddress, want int64, msg string) {
	pts, err := mgr.Keeper().GetNodeAccountSlashPoints(ctx, node)
	c.Assert(err, IsNil)
	c.Assert(pts, Equals, want, Commentf("%s: got %d want %d", msg, pts, want))
}

func finalityCreditSetPre(ctx cosmos.Context, mgr *Mgrs, nas NodeAccounts) {
	for _, na := range nas {
		mgr.Keeper().SetNodeAccountSlashPoints(ctx, na.NodeAddress, finalityCreditPre)
	}
}

// TestObserveSlashFinalFirstNonFinalInbound: final consensus forms before a
// node's non-final observation lands (the 30 s sub-quorum gossip flush on a
// fast-finality chain). The already-credited node's late non-final observation
// must be refunded its observe point, a non-observer keeps only the lack penalty
// for missing the final tier, and an observation past the flex window keeps its
// charge.
func (s *HandlerObservedTxHelpersSuite) TestObserveSlashFinalFirstNonFinalInbound(c *C) {
	ctx, mgr := setupManagerForTest(c)
	ctx = ctx.WithBlockHeight(100)
	nas, vaultPubKey := observeSlashSetup(c, ctx, mgr)
	finalityCreditSetPre(ctx, mgr, nas)
	lack := mgr.GetConstants().GetInt64Value(constants.LackOfObservationPenalty)
	observe := mgr.GetConstants().GetInt64Value(constants.ObserveSlashPoints)

	txID, nonFinal, final := finalityCreditInboundTxs(vaultPubKey)
	voter := NewObservedTxVoter(txID, []common.ObservedTx{})
	for _, na := range nas[:3] {
		voter, _ = processTxInAttestation(ctx, mgr, voter, nas, final, na.NodeAddress, false)
	}
	c.Assert(voter.FinalisedHeight, Equals, int64(100))
	finalityCreditPoints(c, ctx, mgr, nas[0].NodeAddress, finalityCreditPre, "final observer after consensus")
	finalityCreditPoints(c, ctx, mgr, nas[3].NodeAddress, finalityCreditPre+lack, "non-observer at final consensus")

	ctx = ctx.WithBlockHeight(101)
	voter, _ = processTxInAttestation(ctx, mgr, voter, nas, nonFinal, nas[0].NodeAddress, false)
	finalityCreditPoints(c, ctx, mgr, nas[0].NodeAddress, finalityCreditPre, "credited node's late non-final observation")

	// Seeing the tx only before it was final is not a final observation, so the
	// lack penalty stays; the non-final observation's own point is refunded.
	voter, _ = processTxInAttestation(ctx, mgr, voter, nas, nonFinal, nas[3].NodeAddress, false)
	finalityCreditPoints(c, ctx, mgr, nas[3].NodeAddress, finalityCreditPre+lack, "non-final-only observer after final consensus")

	ctx = ctx.WithBlockHeight(111)
	_, _ = processTxInAttestation(ctx, mgr, voter, nas, nonFinal, nas[1].NodeAddress, false)
	finalityCreditPoints(c, ctx, mgr, nas[1].NodeAddress, finalityCreditPre+observe, "non-final observation past the flex window")
}

// TestObserveSlashFinalFirstNonFinalOutbound is the outbound path of the same
// final-first ordering.
func (s *HandlerObservedTxHelpersSuite) TestObserveSlashFinalFirstNonFinalOutbound(c *C) {
	ctx, mgr := setupManagerForTest(c)
	ctx = ctx.WithBlockHeight(100)
	nas, vaultPubKey := observeSlashSetup(c, ctx, mgr)
	finalityCreditSetPre(ctx, mgr, nas)

	txID, nonFinal, final := finalityCreditOutboundTxs(vaultPubKey)
	voter := NewObservedTxVoter(txID, []common.ObservedTx{})
	for _, na := range nas[:3] {
		voter, _ = processTxOutAttestation(ctx, mgr, voter, nas, final, na.NodeAddress, false)
	}
	c.Assert(voter.FinalisedHeight, Equals, int64(100))

	ctx = ctx.WithBlockHeight(101)
	for _, na := range nas[:3] {
		voter, _ = processTxOutAttestation(ctx, mgr, voter, nas, nonFinal, na.NodeAddress, false)
		finalityCreditPoints(c, ctx, mgr, na.NodeAddress, finalityCreditPre, "credited node's late non-final outbound observation")
	}
}

// TestObserveSlashSubQuorumNonFinalBeforeFinal: non-final observations that
// never reach non-final consensus land BEFORE final consensus. Final consensus
// must refund each node's non-final observe point once, alongside the final
// tier's own credit, without touching the lack penalty for nodes that did not
// observe the final tier.
func (s *HandlerObservedTxHelpersSuite) TestObserveSlashSubQuorumNonFinalBeforeFinal(c *C) {
	ctx, mgr := setupManagerForTest(c)
	ctx = ctx.WithBlockHeight(100)
	nas, vaultPubKey := observeSlashSetup(c, ctx, mgr)
	finalityCreditSetPre(ctx, mgr, nas)
	lack := mgr.GetConstants().GetInt64Value(constants.LackOfObservationPenalty)

	txID, nonFinal, final := finalityCreditInboundTxs(vaultPubKey)
	voter := NewObservedTxVoter(txID, []common.ObservedTx{})
	// 2 of 4 is below supermajority, so non-final consensus never forms.
	voter, _ = processTxInAttestation(ctx, mgr, voter, nas, nonFinal, nas[0].NodeAddress, false)
	voter, _ = processTxInAttestation(ctx, mgr, voter, nas, nonFinal, nas[3].NodeAddress, false)
	c.Assert(voter.Height, Equals, int64(0))
	for _, na := range nas[:3] {
		voter, _ = processTxInAttestation(ctx, mgr, voter, nas, final, na.NodeAddress, false)
	}
	c.Assert(voter.FinalisedHeight, Equals, int64(100))

	finalityCreditPoints(c, ctx, mgr, nas[0].NodeAddress, finalityCreditPre, "node observed in both tiers")
	finalityCreditPoints(c, ctx, mgr, nas[1].NodeAddress, finalityCreditPre, "final-only observer")
	finalityCreditPoints(c, ctx, mgr, nas[3].NodeAddress, finalityCreditPre+lack, "non-final-only observer")

	// The non-final-only observer's late final observation clears its penalty
	// through the existing flex branch, with no extra refund.
	ctx = ctx.WithBlockHeight(101)
	_, _ = processTxInAttestation(ctx, mgr, voter, nas, final, nas[3].NodeAddress, false)
	finalityCreditPoints(c, ctx, mgr, nas[3].NodeAddress, finalityCreditPre, "non-final-only observer after its late final observation")
}

// finalityCreditInstantTxs builds the three observations of one LTC outbound:
// the signing committee's instant observation (ConfirmationRequired 0, so
// final), and the scanner's non-final and final observations.
func finalityCreditInstantTxs(vaultPubKey common.PubKey) (common.TxID, common.ObservedTx, common.ObservedTx, common.ObservedTx) {
	txID := GetRandomTxHash()
	tx := common.Tx{
		ID:          txID,
		Chain:       common.LTCChain,
		FromAddress: GetRandomLTCAddress(),
		ToAddress:   GetRandomLTCAddress(),
		Coins:       common.NewCoins(common.NewCoin(common.LTCAsset, cosmos.NewUint(1*common.One))),
		Gas:         common.Gas{common.NewCoin(common.LTCAsset, cosmos.NewUint(67540))},
		Memo:        "OUT:" + txID.String(),
	}
	instant := common.NewObservedTx(tx, 98, vaultPubKey, 98)
	scannerNonFinal := common.NewObservedTx(tx, 100, vaultPubKey, 106)
	scannerFinal := common.NewObservedTx(tx, 106, vaultPubKey, 106)
	return txID, instant, scannerNonFinal, scannerFinal
}

// instantFinalScenario runs a committee member's instant observation, then
// non-final scanner consensus among the other three nodes, and returns the
// voter with the member still missing its scanner observations.
func instantFinalScenario(c *C, ctx cosmos.Context, mgr *Mgrs, nas NodeAccounts, instant, scannerNonFinal common.ObservedTx, txID common.TxID) ObservedTxVoter {
	member := nas[3].NodeAddress
	observe := mgr.GetConstants().GetInt64Value(constants.ObserveSlashPoints)

	voter := NewObservedTxVoter(txID, []common.ObservedTx{})
	voter, _ = processTxOutAttestation(ctx, mgr, voter, nas, instant, member, false)
	for _, na := range nas[:3] {
		voter, _ = processTxOutAttestation(ctx, mgr, voter, nas, scannerNonFinal, na.NodeAddress, false)
	}
	c.Assert(voter.Height, Equals, int64(100))
	c.Assert(voter.FinalisedHeight, Equals, int64(0))
	// The instant observation's own point waits for final consensus; the member
	// must not also be charged the lack penalty at non-final consensus.
	finalityCreditPoints(c, ctx, mgr, member, finalityCreditPre+observe, "instant-final member at non-final consensus")
	return voter
}

// TestObserveSlashInstantFinalMemberNoScannerNonFinal: the member never files
// the scanner's non-final observation (the bug's +2 path). After final
// consensus it must be back at its starting balance.
func (s *HandlerObservedTxHelpersSuite) TestObserveSlashInstantFinalMemberNoScannerNonFinal(c *C) {
	ctx, mgr := setupManagerForTest(c)
	ctx = ctx.WithBlockHeight(100)
	nas, vaultPubKey := observeSlashSetup(c, ctx, mgr)
	finalityCreditSetPre(ctx, mgr, nas)
	member := nas[3].NodeAddress

	txID, instant, scannerNonFinal, scannerFinal := finalityCreditInstantTxs(vaultPubKey)
	voter := instantFinalScenario(c, ctx, mgr, nas, instant, scannerNonFinal, txID)

	ctx = ctx.WithBlockHeight(105)
	for _, na := range nas[:3] {
		voter, _ = processTxOutAttestation(ctx, mgr, voter, nas, scannerFinal, na.NodeAddress, false)
	}
	c.Assert(voter.FinalisedHeight, Equals, int64(105))
	c.Assert(voter.ExcusedObservers, HasLen, 0)
	finalityCreditPoints(c, ctx, mgr, member, finalityCreditPre, "instant-final member after final consensus")

	ctx = ctx.WithBlockHeight(106)
	_, _ = processTxOutAttestation(ctx, mgr, voter, nas, scannerFinal, member, false)
	finalityCreditPoints(c, ctx, mgr, member, finalityCreditPre, "instant-final member's redundant scanner final observation")
	for _, na := range nas[:3] {
		finalityCreditPoints(c, ctx, mgr, na.NodeAddress, finalityCreditPre, "scanner observer")
	}
}

// TestObserveSlashInstantFinalMemberLateScannerNonFinal: the excused member's
// own scanner non-final observation arrives inside the flex window. It must be
// refunded only its observe point: refunding the lack penalty it was never
// charged would remove unrelated slash points.
func (s *HandlerObservedTxHelpersSuite) TestObserveSlashInstantFinalMemberLateScannerNonFinal(c *C) {
	ctx, mgr := setupManagerForTest(c)
	ctx = ctx.WithBlockHeight(100)
	nas, vaultPubKey := observeSlashSetup(c, ctx, mgr)
	finalityCreditSetPre(ctx, mgr, nas)
	member := nas[3].NodeAddress
	observe := mgr.GetConstants().GetInt64Value(constants.ObserveSlashPoints)

	txID, instant, scannerNonFinal, scannerFinal := finalityCreditInstantTxs(vaultPubKey)
	_ = instantFinalScenario(c, ctx, mgr, nas, instant, scannerNonFinal, txID)

	// The excused set must survive the store round trip.
	voter, err := mgr.Keeper().GetObservedTxOutVoter(ctx, txID)
	c.Assert(err, IsNil)
	c.Assert(voter.ExcusedObservers, DeepEquals, []string{member.String()})

	ctx = ctx.WithBlockHeight(101)
	voter, _ = processTxOutAttestation(ctx, mgr, voter, nas, scannerNonFinal, member, false)
	finalityCreditPoints(c, ctx, mgr, member, finalityCreditPre+observe, "excused member's late scanner non-final observation")

	// A repeat cannot collect the refund again.
	voter, _ = processTxOutAttestation(ctx, mgr, voter, nas, scannerNonFinal, member, false)
	finalityCreditPoints(c, ctx, mgr, member, finalityCreditPre+observe, "duplicate scanner non-final observation")

	ctx = ctx.WithBlockHeight(105)
	for _, na := range nas[:3] {
		voter, _ = processTxOutAttestation(ctx, mgr, voter, nas, scannerFinal, na.NodeAddress, false)
	}
	finalityCreditPoints(c, ctx, mgr, member, finalityCreditPre, "excused member after final consensus")
}

// TestObserveSlashFinalBeforeNonFinalConsensusInbound is the inbound path of
// the excused-member sequence: on a fast-finality chain one node files its
// final observation before the other three reach non-final consensus.
func (s *HandlerObservedTxHelpersSuite) TestObserveSlashFinalBeforeNonFinalConsensusInbound(c *C) {
	ctx, mgr := setupManagerForTest(c)
	ctx = ctx.WithBlockHeight(100)
	nas, vaultPubKey := observeSlashSetup(c, ctx, mgr)
	finalityCreditSetPre(ctx, mgr, nas)
	node := nas[3].NodeAddress
	observe := mgr.GetConstants().GetInt64Value(constants.ObserveSlashPoints)

	txID, nonFinal, final := finalityCreditInboundTxs(vaultPubKey)
	voter := NewObservedTxVoter(txID, []common.ObservedTx{})
	voter, _ = processTxInAttestation(ctx, mgr, voter, nas, final, node, false)
	for _, na := range nas[:3] {
		voter, _ = processTxInAttestation(ctx, mgr, voter, nas, nonFinal, na.NodeAddress, false)
	}
	c.Assert(voter.Height, Equals, int64(100))
	c.Assert(voter.FinalisedHeight, Equals, int64(0))
	finalityCreditPoints(c, ctx, mgr, node, finalityCreditPre+observe, "final observer at non-final consensus")

	// The excused set must survive the store round trip.
	voter, err := mgr.Keeper().GetObservedTxInVoter(ctx, txID)
	c.Assert(err, IsNil)
	c.Assert(voter.ExcusedObservers, DeepEquals, []string{node.String()})

	ctx = ctx.WithBlockHeight(101)
	voter, _ = processTxInAttestation(ctx, mgr, voter, nas, nonFinal, node, false)
	finalityCreditPoints(c, ctx, mgr, node, finalityCreditPre+observe, "excused node's late non-final observation")

	// A repeat cannot collect the refund again.
	voter, _ = processTxInAttestation(ctx, mgr, voter, nas, nonFinal, node, false)
	finalityCreditPoints(c, ctx, mgr, node, finalityCreditPre+observe, "duplicate non-final observation")

	ctx = ctx.WithBlockHeight(105)
	for _, na := range nas[:3] {
		voter, _ = processTxInAttestation(ctx, mgr, voter, nas, final, na.NodeAddress, false)
	}
	c.Assert(voter.FinalisedHeight, Equals, int64(105))
	finalityCreditPoints(c, ctx, mgr, node, finalityCreditPre, "excused node after final consensus")
}

// TestObserveSlashInstantFinalMemberScannerNonFinalAfterFinal: the excused
// member's scanner non-final observation lands only after final consensus.
func (s *HandlerObservedTxHelpersSuite) TestObserveSlashInstantFinalMemberScannerNonFinalAfterFinal(c *C) {
	ctx, mgr := setupManagerForTest(c)
	ctx = ctx.WithBlockHeight(100)
	nas, vaultPubKey := observeSlashSetup(c, ctx, mgr)
	finalityCreditSetPre(ctx, mgr, nas)
	member := nas[3].NodeAddress

	txID, instant, scannerNonFinal, scannerFinal := finalityCreditInstantTxs(vaultPubKey)
	voter := instantFinalScenario(c, ctx, mgr, nas, instant, scannerNonFinal, txID)

	ctx = ctx.WithBlockHeight(105)
	for _, na := range nas[:3] {
		voter, _ = processTxOutAttestation(ctx, mgr, voter, nas, scannerFinal, na.NodeAddress, false)
	}
	ctx = ctx.WithBlockHeight(106)
	_, _ = processTxOutAttestation(ctx, mgr, voter, nas, scannerNonFinal, member, false)
	finalityCreditPoints(c, ctx, mgr, member, finalityCreditPre, "excused member's scanner non-final observation after final consensus")
}

// TestObserveSlashFinalityCreditNormalSequenceOutbound: the ordinary non-final
// then final sequence on the outbound path. Each tier credits its own
// observation once; non-final consensus having formed, final consensus must
// not credit the non-final tier a second time.
func (s *HandlerObservedTxHelpersSuite) TestObserveSlashFinalityCreditNormalSequenceOutbound(c *C) {
	ctx, mgr := setupManagerForTest(c)
	ctx = ctx.WithBlockHeight(100)
	nas, vaultPubKey := observeSlashSetup(c, ctx, mgr)
	finalityCreditSetPre(ctx, mgr, nas)
	lack := mgr.GetConstants().GetInt64Value(constants.LackOfObservationPenalty)

	txID, _, scannerNonFinal, scannerFinal := finalityCreditInstantTxs(vaultPubKey)
	voter := NewObservedTxVoter(txID, []common.ObservedTx{})
	for _, na := range nas[:3] {
		voter, _ = processTxOutAttestation(ctx, mgr, voter, nas, scannerNonFinal, na.NodeAddress, false)
	}
	c.Assert(voter.Height, Equals, int64(100))
	c.Assert(voter.ExcusedObservers, HasLen, 0)
	ctx = ctx.WithBlockHeight(105)
	for _, na := range nas[:3] {
		voter, _ = processTxOutAttestation(ctx, mgr, voter, nas, scannerFinal, na.NodeAddress, false)
	}
	c.Assert(voter.FinalisedHeight, Equals, int64(105))

	for _, na := range nas[:3] {
		finalityCreditPoints(c, ctx, mgr, na.NodeAddress, finalityCreditPre, "node observed in both tiers")
	}
	finalityCreditPoints(c, ctx, mgr, nas[3].NodeAddress, finalityCreditPre+2*lack, "non-observer at both consensuses")
}

// TestObserveSlashFinalityCreditVariantSpamCapped: after final consensus, a
// node's first non-final observation is refunded, its second through the
// existing one-variant allowance, and any further variant keeps its point.
func (s *HandlerObservedTxHelpersSuite) TestObserveSlashFinalityCreditVariantSpamCapped(c *C) {
	ctx, mgr := setupManagerForTest(c)
	ctx = ctx.WithBlockHeight(100)
	nas, vaultPubKey := observeSlashSetup(c, ctx, mgr)
	finalityCreditSetPre(ctx, mgr, nas)
	observe := mgr.GetConstants().GetInt64Value(constants.ObserveSlashPoints)
	node := nas[0].NodeAddress

	txID, nonFinal, final := finalityCreditInboundTxs(vaultPubKey)
	voter := NewObservedTxVoter(txID, []common.ObservedTx{})
	for _, na := range nas[:3] {
		voter, _ = processTxInAttestation(ctx, mgr, voter, nas, final, na.NodeAddress, false)
	}

	ctx = ctx.WithBlockHeight(101)
	variants := []int64{97, 98, 99}
	want := []int64{finalityCreditPre, finalityCreditPre, finalityCreditPre + observe}
	for i, height := range variants {
		variant := nonFinal
		variant.BlockHeight = height
		voter, _ = processTxInAttestation(ctx, mgr, voter, nas, variant, node, false)
		finalityCreditPoints(c, ctx, mgr, node, want[i], "non-final variant after final consensus")
	}
}

// TestObserveSlashFinalityCreditSkipsXMR: XMR keeps strictly per-tier
// accounting, since its legacy and pre-mining credit matching assumes both
// observations share a finality. A non-final XMR observation after final
// consensus keeps its observe point, as before.
func (s *HandlerObservedTxHelpersSuite) TestObserveSlashFinalityCreditSkipsXMR(c *C) {
	ctx, mgr := setupManagerForTest(c)
	ctx = ctx.WithBlockHeight(100)
	nas, _, mined := setupXMRObservationCredit(c, ctx, mgr, true)
	observe := mgr.GetConstants().GetInt64Value(constants.ObserveSlashPoints)

	voter := NewObservedTxVoter(mined.Tx.ID, []common.ObservedTx{})
	for _, na := range nas[:3] {
		voter, _ = submitXMRObservationCredit(c, ctx, mgr, voter, nas, mined, na.NodeAddress, false)
	}
	c.Assert(voter.FinalisedHeight, Equals, int64(100))

	nonFinal := mined
	nonFinal.BlockHeight = nonFinal.XmrMinedHeight
	c.Assert(nonFinal.IsFinal(), Equals, false)
	ctx = ctx.WithBlockHeight(101)
	_, _ = submitXMRObservationCredit(c, ctx, mgr, voter, nas, nonFinal, nas[0].NodeAddress, false)
	finalityCreditPoints(c, ctx, mgr, nas[0].NodeAddress, finalityCreditPre+observe, "XMR non-final observation after final consensus")
}

// finalityCreditMemolessSetup prepares a memoless ETH inbound whose amount
// encodes a stored reference memo. After consensus, handleObservedTxInQuorum
// rewrites the stored consensus tx's memo: to r:<ref> at non-final consensus,
// then to the resolved memo at final consensus.
func finalityCreditMemolessSetup(c *C) (cosmos.Context, *Mgrs, NodeAccounts, cosmos.Handler, common.ObservedTx, common.ObservedTx, string) {
	ctx, mgr := setupManagerForTest(c)
	ctx = ctx.WithBlockHeight(100)
	nas, vaultPubKey := observeSlashSetup(c, ctx, mgr)
	for _, na := range nas {
		c.Assert(mgr.Keeper().SetNodeAccount(ctx, na), IsNil)
	}
	finalityCreditSetPre(ctx, mgr, nas)

	pool := NewPool()
	pool.Asset = common.ETHAsset
	pool.BalanceAsset = cosmos.NewUint(10_000 * common.One)
	pool.BalanceRune = cosmos.NewUint(10_000 * common.One)
	pool.Decimals = 8
	pool.Status = PoolAvailable
	c.Assert(mgr.Keeper().SetPool(ctx, pool), IsNil)
	mgr.Keeper().SetMimir(ctx, constants.MemolessTxnTTL.String(), 1000)

	_, nonFinal, final := finalityCreditInboundTxs(vaultPubKey)
	coins := common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(123456789)))
	nonFinal.Tx.Coins, final.Tx.Coins = coins, coins
	nonFinal.Tx.Memo, final.Tx.Memo = "", ""
	ref, err := generateReferenceMemoID(ctx, mgr, common.ETHAsset, final)
	c.Assert(err, IsNil)
	target := "SWAP:ETH.ETH:" + GetRandomETHAddress().String()
	mgr.Keeper().SetReferenceMemo(ctx, NewReferenceMemo(common.ETHAsset, target, ref, 1))
	return ctx, mgr, nas, NewInternalHandler(mgr), nonFinal, final, target
}

// finalityCreditObserveIn submits one inbound observation the way
// ObservedTxInHandler does: load the stored voter, attest, run the quorum
// handling, then return the voter as stored.
func finalityCreditObserveIn(c *C, ctx cosmos.Context, mgr *Mgrs, nas NodeAccounts, handler cosmos.Handler, tx common.ObservedTx, signer cosmos.AccAddress) ObservedTxVoter {
	voter, err := ensureVaultAndGetTxInVoter(ctx, tx.ObservedPubKey, tx.Tx.ID, mgr.Keeper())
	c.Assert(err, IsNil)
	voter, isQuorum := processTxInAttestation(ctx, mgr, voter, nas, tx, signer, true)
	c.Assert(handleObservedTxInQuorum(ctx, mgr, signer, nas, handler, tx, voter, voter.Tx.GetSigners(), isQuorum), IsNil)
	voter, err = mgr.Keeper().GetObservedTxInVoter(ctx, tx.Tx.ID)
	c.Assert(err, IsNil)
	return voter
}

// TestObserveSlashLateCreditMemolessInbound: after final consensus the stored
// consensus tx of a memoless inbound carries the resolved reference memo, so a
// late observation of the same tx, still memoless, no longer equals voter.Tx.
// Late credit must still reach it: the first non-final observation after final
// consensus, and a late final observation.
func (s *HandlerObservedTxHelpersSuite) TestObserveSlashLateCreditMemolessInbound(c *C) {
	ctx, mgr, nas, handler, nonFinal, final, target := finalityCreditMemolessSetup(c)
	lack := mgr.GetConstants().GetInt64Value(constants.LackOfObservationPenalty)

	var voter ObservedTxVoter
	for _, na := range nas[:3] {
		voter = finalityCreditObserveIn(c, ctx, mgr, nas, handler, final, na.NodeAddress)
	}
	c.Assert(voter.FinalisedHeight, Equals, int64(100))
	c.Assert(voter.Tx.Tx.Memo, Equals, target, Commentf("the stored consensus memo should be the resolved reference memo"))
	finalityCreditPoints(c, ctx, mgr, nas[3].NodeAddress, finalityCreditPre+lack, "non-observer at final consensus")

	ctx = ctx.WithBlockHeight(101)
	finalityCreditObserveIn(c, ctx, mgr, nas, handler, nonFinal, nas[0].NodeAddress)
	finalityCreditPoints(c, ctx, mgr, nas[0].NodeAddress, finalityCreditPre, "credited node's late memoless non-final observation")

	finalityCreditObserveIn(c, ctx, mgr, nas, handler, final, nas[3].NodeAddress)
	finalityCreditPoints(c, ctx, mgr, nas[3].NodeAddress, finalityCreditPre, "late memoless final observation")
}

// TestObserveSlashExcusedMemolessInbound: at non-final consensus the stored
// consensus tx of a memoless inbound gets its reference memo (r:<ref>). The
// excused node's late non-final observation, still memoless, must still be
// refunded its observe point.
func (s *HandlerObservedTxHelpersSuite) TestObserveSlashExcusedMemolessInbound(c *C) {
	ctx, mgr, nas, handler, nonFinal, final, _ := finalityCreditMemolessSetup(c)
	observe := mgr.GetConstants().GetInt64Value(constants.ObserveSlashPoints)
	node := nas[3].NodeAddress

	finalityCreditObserveIn(c, ctx, mgr, nas, handler, final, node)
	var voter ObservedTxVoter
	for _, na := range nas[:3] {
		voter = finalityCreditObserveIn(c, ctx, mgr, nas, handler, nonFinal, na.NodeAddress)
	}
	c.Assert(voter.Height, Equals, int64(100))
	c.Assert(voter.FinalisedHeight, Equals, int64(0))
	c.Assert(voter.Tx.Tx.Memo, Not(Equals), "", Commentf("the stored consensus memo should be the reference memo"))
	c.Assert(voter.ExcusedObservers, DeepEquals, []string{node.String()})
	finalityCreditPoints(c, ctx, mgr, node, finalityCreditPre+observe, "excused node at non-final consensus")

	ctx = ctx.WithBlockHeight(101)
	finalityCreditObserveIn(c, ctx, mgr, nas, handler, nonFinal, node)
	finalityCreditPoints(c, ctx, mgr, node, finalityCreditPre+observe, "excused node's late memoless non-final observation")
}

// finalityCreditObserveOut submits one outbound observation the way
// ObservedTxOutHandler does: load the stored voter, attest, run the quorum
// handling, then return the voter as stored. The outbound handler is a no-op:
// only the memo recovery and the slash accounting are under test.
func finalityCreditObserveOut(c *C, ctx cosmos.Context, mgr *Mgrs, nas NodeAccounts, tx common.ObservedTx, signer cosmos.AccAddress) ObservedTxVoter {
	signers := []cosmos.AccAddress{signer}
	voter, err := ensureVaultAndGetTxOutVoter(ctx, mgr.Keeper(), tx.ObservedPubKey, tx.Tx.ID, signers, tx.KeysignMs)
	c.Assert(err, IsNil)
	voter, isQuorum := processTxOutAttestation(ctx, mgr, voter, nas, tx, signer, true)
	noop := func(cosmos.Context, cosmos.Msg) (*cosmos.Result, error) {
		return &cosmos.Result{}, nil
	}
	c.Assert(handleObservedTxOutQuorum(ctx, mgr, signer, nas, noop, tx, voter, signers, isQuorum), IsNil)
	voter, err = mgr.Keeper().GetObservedTxOutVoter(ctx, tx.Tx.ID)
	c.Assert(err, IsNil)
	return voter
}

// TestObserveSlashLateCreditMemolessOutbound: at final consensus the stored
// consensus tx of a memoless outbound gets the memo of its scheduled
// TxOutItem, so a late observation of the same tx, still memoless, no longer
// equals voter.Tx. Late credit must still reach it: the first non-final
// observation after final consensus, and a late final observation.
func (s *HandlerObservedTxHelpersSuite) TestObserveSlashLateCreditMemolessOutbound(c *C) {
	ctx, mgr := setupManagerForTest(c)
	ctx = ctx.WithBlockHeight(100)
	nas, vaultPubKey := observeSlashSetup(c, ctx, mgr)
	finalityCreditSetPre(ctx, mgr, nas)
	lack := mgr.GetConstants().GetInt64Value(constants.LackOfObservationPenalty)

	_, nonFinal, final := finalityCreditOutboundTxs(vaultPubKey)
	nonFinal.Tx.Memo, final.Tx.Memo = "", ""
	inHash := GetRandomTxHash()
	txOut := NewTxOut(100)
	txOut.TxArray = append(txOut.TxArray, TxOutItem{
		Chain:        common.ETHChain,
		ToAddress:    final.Tx.ToAddress,
		VaultPubKey:  vaultPubKey,
		Coin:         final.Tx.Coins[0],
		OriginalMemo: "OUT:" + inHash.String(),
		InHash:       inHash,
	})
	c.Assert(mgr.Keeper().SetTxOut(ctx, txOut), IsNil)

	var voter ObservedTxVoter
	for _, na := range nas[:3] {
		voter = finalityCreditObserveOut(c, ctx, mgr, nas, final, na.NodeAddress)
	}
	c.Assert(voter.FinalisedHeight, Equals, int64(100))
	c.Assert(voter.Tx.Tx.Memo, Equals, "OUT:"+inHash.String(), Commentf("the stored consensus memo should be the scheduled outbound's memo"))
	finalityCreditPoints(c, ctx, mgr, nas[3].NodeAddress, finalityCreditPre+lack, "non-observer at final consensus")

	ctx = ctx.WithBlockHeight(101)
	finalityCreditObserveOut(c, ctx, mgr, nas, nonFinal, nas[0].NodeAddress)
	finalityCreditPoints(c, ctx, mgr, nas[0].NodeAddress, finalityCreditPre, "credited node's late memoless non-final outbound observation")

	finalityCreditObserveOut(c, ctx, mgr, nas, final, nas[3].NodeAddress)
	finalityCreditPoints(c, ctx, mgr, nas[3].NodeAddress, finalityCreditPre, "late memoless final outbound observation")
}

// finalityCreditAttest is processTxInAttestation or processTxOutAttestation.
type finalityCreditAttest func(cosmos.Context, Manager, ObservedTxVoter, NodeAccounts, common.ObservedTx, cosmos.AccAddress, bool) (ObservedTxVoter, bool)

// finalAfterNonFinalScenario: a node misses non-final consensus, then observes
// the tx as final within its flex window, before final consensus. Its lack
// penalty is refunded and it is excused, so a second final variant collects
// nothing more, its own late non-final observation gets back only its observe
// point, and final consensus settles its final observe point.
func finalAfterNonFinalScenario(c *C, ctx cosmos.Context, mgr *Mgrs, nas NodeAccounts, attest finalityCreditAttest, txID common.TxID, nonFinal, final common.ObservedTx) {
	lack := mgr.GetConstants().GetInt64Value(constants.LackOfObservationPenalty)
	observe := mgr.GetConstants().GetInt64Value(constants.ObserveSlashPoints)
	node := nas[3].NodeAddress

	voter := NewObservedTxVoter(txID, []common.ObservedTx{})
	for _, na := range nas[:3] {
		voter, _ = attest(ctx, mgr, voter, nas, nonFinal, na.NodeAddress, false)
	}
	c.Assert(voter.Height, Equals, int64(100))
	finalityCreditPoints(c, ctx, mgr, node, finalityCreditPre+lack, "non-observer at non-final consensus")

	ctx = ctx.WithBlockHeight(102)
	voter, _ = attest(ctx, mgr, voter, nas, final, node, false)
	c.Assert(voter.FinalisedHeight, Equals, int64(0))
	c.Assert(voter.ExcusedObservers, DeepEquals, []string{node.String()})
	finalityCreditPoints(c, ctx, mgr, node, finalityCreditPre+observe, "final observation within the non-final flex window")

	// A gas variant is a redundant re-observation and cannot collect the
	// refund again.
	variant := final
	variant.Tx.Gas = common.Gas{common.NewCoin(final.Tx.Gas[0].Asset, final.Tx.Gas[0].Amount.Add(cosmos.NewUint(1)))}
	voter, _ = attest(ctx, mgr, voter, nas, variant, node, false)
	finalityCreditPoints(c, ctx, mgr, node, finalityCreditPre+observe, "second final variant")

	ctx = ctx.WithBlockHeight(103)
	voter, _ = attest(ctx, mgr, voter, nas, nonFinal, node, false)
	finalityCreditPoints(c, ctx, mgr, node, finalityCreditPre+observe, "the node's own late non-final observation")

	ctx = ctx.WithBlockHeight(105)
	for _, na := range nas[:3] {
		voter, _ = attest(ctx, mgr, voter, nas, final, na.NodeAddress, false)
	}
	c.Assert(voter.FinalisedHeight, Equals, int64(105))
	c.Assert(voter.ExcusedObservers, HasLen, 0)
	finalityCreditPoints(c, ctx, mgr, node, finalityCreditPre, "after final consensus")
}

// TestObserveSlashFinalAfterNonFinalConsensusInbound: a node whose only
// observation is final and lands just after non-final consensus, e.g. from a
// scanner a few blocks behind, is not left with the non-final lack penalty.
func (s *HandlerObservedTxHelpersSuite) TestObserveSlashFinalAfterNonFinalConsensusInbound(c *C) {
	ctx, mgr := setupManagerForTest(c)
	ctx = ctx.WithBlockHeight(100)
	nas, vaultPubKey := observeSlashSetup(c, ctx, mgr)
	finalityCreditSetPre(ctx, mgr, nas)

	txID, nonFinal, final := finalityCreditInboundTxs(vaultPubKey)
	finalAfterNonFinalScenario(c, ctx, mgr, nas, processTxInAttestation, txID, nonFinal, final)
}

// TestObserveSlashFinalAfterNonFinalConsensusOutbound is the outbound path of
// the same ordering.
func (s *HandlerObservedTxHelpersSuite) TestObserveSlashFinalAfterNonFinalConsensusOutbound(c *C) {
	ctx, mgr := setupManagerForTest(c)
	ctx = ctx.WithBlockHeight(100)
	nas, vaultPubKey := observeSlashSetup(c, ctx, mgr)
	finalityCreditSetPre(ctx, mgr, nas)

	txID, nonFinal, final := finalityCreditOutboundTxs(vaultPubKey)
	finalAfterNonFinalScenario(c, ctx, mgr, nas, processTxOutAttestation, txID, nonFinal, final)
}

// TestObserveSlashFinalAfterNonFinalConsensusEdges: a final observation past
// the non-final flex window keeps the lack penalty, one that itself forms final
// consensus is refunded it, and a late non-final observation is left to the
// existing late branch.
func (s *HandlerObservedTxHelpersSuite) TestObserveSlashFinalAfterNonFinalConsensusEdges(c *C) {
	ctx, mgr := setupManagerForTest(c)
	ctx = ctx.WithBlockHeight(100)
	nas, vaultPubKey := observeSlashSetup(c, ctx, mgr)
	finalityCreditSetPre(ctx, mgr, nas)
	lack := mgr.GetConstants().GetInt64Value(constants.LackOfObservationPenalty)
	node := nas[3].NodeAddress

	txID, nonFinal, final := finalityCreditInboundTxs(vaultPubKey)
	voter := NewObservedTxVoter(txID, []common.ObservedTx{})
	for _, na := range nas[:3] {
		voter, _ = processTxInAttestation(ctx, mgr, voter, nas, nonFinal, na.NodeAddress, false)
	}
	ctx = ctx.WithBlockHeight(111)
	voter, _ = processTxInAttestation(ctx, mgr, voter, nas, final, node, false)
	for _, na := range nas[:2] {
		voter, _ = processTxInAttestation(ctx, mgr, voter, nas, final, na.NodeAddress, false)
	}
	c.Assert(voter.FinalisedHeight, Equals, int64(111))
	finalityCreditPoints(c, ctx, mgr, node, finalityCreditPre+lack, "final observation past the non-final flex window")

	ctx = ctx.WithBlockHeight(200)
	finalityCreditSetPre(ctx, mgr, nas)
	txID, nonFinal, final = finalityCreditInboundTxs(vaultPubKey)
	voter = NewObservedTxVoter(txID, []common.ObservedTx{})
	for _, na := range nas[:3] {
		voter, _ = processTxInAttestation(ctx, mgr, voter, nas, nonFinal, na.NodeAddress, false)
	}
	ctx = ctx.WithBlockHeight(202)
	for _, na := range nas[:2] {
		voter, _ = processTxInAttestation(ctx, mgr, voter, nas, final, na.NodeAddress, false)
	}
	c.Assert(voter.FinalisedHeight, Equals, int64(0))
	voter, _ = processTxInAttestation(ctx, mgr, voter, nas, final, node, false)
	c.Assert(voter.FinalisedHeight, Equals, int64(202))
	finalityCreditPoints(c, ctx, mgr, node, finalityCreditPre, "final observation that forms final consensus")

	ctx = ctx.WithBlockHeight(300)
	finalityCreditSetPre(ctx, mgr, nas)
	txID, nonFinal, _ = finalityCreditInboundTxs(vaultPubKey)
	voter = NewObservedTxVoter(txID, []common.ObservedTx{})
	for _, na := range nas[:3] {
		voter, _ = processTxInAttestation(ctx, mgr, voter, nas, nonFinal, na.NodeAddress, false)
	}
	ctx = ctx.WithBlockHeight(301)
	voter, _ = processTxInAttestation(ctx, mgr, voter, nas, nonFinal, node, false)
	c.Assert(voter.ExcusedObservers, HasLen, 0)
	finalityCreditPoints(c, ctx, mgr, node, finalityCreditPre, "late non-final observation within the flex window")
}

// TestObserveSlashFinalAfterNonFinalConsensusMemolessInbound: after non-final
// consensus the stored consensus tx of a memoless inbound carries r:<ref>, so
// a node's memoless final observation within the flex window must be matched
// against the consensus entry as observed.
func (s *HandlerObservedTxHelpersSuite) TestObserveSlashFinalAfterNonFinalConsensusMemolessInbound(c *C) {
	ctx, mgr, nas, handler, nonFinal, final, _ := finalityCreditMemolessSetup(c)
	observe := mgr.GetConstants().GetInt64Value(constants.ObserveSlashPoints)
	node := nas[3].NodeAddress

	var voter ObservedTxVoter
	for _, na := range nas[:3] {
		voter = finalityCreditObserveIn(c, ctx, mgr, nas, handler, nonFinal, na.NodeAddress)
	}
	c.Assert(voter.Height, Equals, int64(100))
	c.Assert(voter.Tx.Tx.Memo, Not(Equals), "", Commentf("the stored consensus memo should be the reference memo"))

	ctx = ctx.WithBlockHeight(101)
	voter = finalityCreditObserveIn(c, ctx, mgr, nas, handler, final, node)
	c.Assert(voter.FinalisedHeight, Equals, int64(0))
	finalityCreditPoints(c, ctx, mgr, node, finalityCreditPre+observe, "memoless final observation within the non-final flex window")
}

// TestObserveSlashFinalAfterNonFinalConsensusSkipsXMR: XMR keeps per-tier
// accounting, so a final observation after XMR non-final consensus does not
// refund the non-final lack penalty.
func (s *HandlerObservedTxHelpersSuite) TestObserveSlashFinalAfterNonFinalConsensusSkipsXMR(c *C) {
	ctx, mgr := setupManagerForTest(c)
	ctx = ctx.WithBlockHeight(100)
	nas, _, nonFinal := setupXMRObservationCredit(c, ctx, mgr, false)
	lack := mgr.GetConstants().GetInt64Value(constants.LackOfObservationPenalty)
	observe := mgr.GetConstants().GetInt64Value(constants.ObserveSlashPoints)
	final := nonFinal
	final.BlockHeight = final.FinaliseHeight
	c.Assert(final.IsFinal(), Equals, true)

	voter := NewObservedTxVoter(nonFinal.Tx.ID, []common.ObservedTx{})
	for _, na := range nas[:3] {
		voter, _ = submitXMRObservationCredit(c, ctx, mgr, voter, nas, nonFinal, na.NodeAddress, false)
	}
	c.Assert(voter.Height, Equals, int64(100))
	c.Assert(voter.FinalisedHeight, Equals, int64(0))

	ctx = ctx.WithBlockHeight(101)
	_, _ = submitXMRObservationCredit(c, ctx, mgr, voter, nas, final, nas[3].NodeAddress, false)
	finalityCreditPoints(c, ctx, mgr, nas[3].NodeAddress, finalityCreditPre+observe+lack, "XMR final observation after non-final consensus")
}
