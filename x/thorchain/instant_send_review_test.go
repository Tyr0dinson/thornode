package thorchain

import (
	"encoding/json"
	"errors"
	"slices"

	"github.com/cometbft/cometbft/crypto/secp256k1"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

func (s *DashAccountingSuite) TestOrdinaryVoteCannotPoisonEarlyCredit(c *C) {
	for _, legacy := range []bool{false, true} {
		ctx, mgr, vault, tx := dashAccountingFixture(c)
		nodes := make(NodeAccounts, 4)
		keys := make([]secp256k1.PrivKey, 4)
		for i := range nodes {
			nodes[i], keys[i] = nodeWithPrivKey(c, mgr.Keeper(), ctx)
		}
		pool := NewPool()
		pool.Asset, pool.Status = common.DASHAsset, PoolAvailable
		pool.BalanceAsset, pool.BalanceRune = cosmos.NewUint(1000*common.One), cosmos.NewUint(1000*common.One)
		c.Assert(mgr.Keeper().SetPool(ctx, pool), IsNil)
		send := func(observation common.ObservedTx, signers []secp256k1.PrivKey) {
			q := &common.QuorumTx{ObsTx: observation, Inbound: true}
			for _, key := range signers {
				q.Attestations = append(q.Attestations, attestObservedTx(c, key, observation, true))
			}
			_, err := NewObservedTxQuorumHandler(mgr).Run(ctx, types.NewMsgObservedTxQuorum(q, nodes[0].NodeAddress))
			c.Assert(err, IsNil)
		}
		ordinary := tx
		ordinary.InstantSend = nil
		if legacy {
			_, err := NewObservedTxInHandler(mgr).Run(ctx, NewMsgObservedTxIn(common.ObservedTxs{ordinary}, nodes[0].NodeAddress))
			c.Assert(err, IsNil)
		} else {
			send(ordinary, keys[:1])
		}
		before, err := mgr.Keeper().GetObservedTxInVoter(ctx, tx.Tx.ID)
		c.Assert(err, IsNil)
		c.Assert(before.Txs, HasLen, 1)
		points := make([]int64, len(nodes))
		for i, node := range nodes {
			points[i], err = mgr.Keeper().GetNodeAccountSlashPoints(ctx, node.NodeAddress)
			c.Assert(err, IsNil)
		}
		send(tx, keys[1:])
		after, err := mgr.Keeper().GetObservedTxInVoter(ctx, tx.Tx.ID)
		c.Assert(err, IsNil)
		c.Assert(after, DeepEquals, before)
		for i, node := range nodes {
			point, readErr := mgr.Keeper().GetNodeAccountSlashPoints(ctx, node.NodeAddress)
			c.Assert(readErr, IsNil)
			c.Assert(point, Equals, points[i])
		}
		actual, err := mgr.Keeper().GetVault(ctx, vault.PubKey)
		c.Assert(err, IsNil)
		c.Assert(actual.Coins, DeepEquals, vault.Coins)
		state, err := mgr.Keeper().GetInstantSend(ctx, common.DASHChain, vault.PubKey)
		c.Assert(err, IsNil)
		c.Assert(state.Pending, HasLen, 0)
		credit, err := mgr.Keeper().GetInstantSendInput(ctx, tx.SpentOutputRefs[0])
		c.Assert(err, IsNil)
		c.Assert(credit.Id.IsEmpty(), Equals, true)
		c.Assert(mgr.Keeper().HasSwapQueueItem(ctx, tx.Tx.ID, 0) || mgr.Keeper().HasAdvSwapQueueItem(ctx, tx.Tx.ID, 0), Equals, false)
		checkInstantSend(ctx, mgr)
		assertInstantSendHalt(c, ctx, mgr, 0)

		ordinary.BlockHeight, ordinary.FinaliseHeight = 101, 101
		send(ordinary, keys[1:])
		send(ordinary, keys[1:])
		actual, err = mgr.Keeper().GetVault(ctx, vault.PubKey)
		c.Assert(err, IsNil)
		c.Assert(actual.GetCoin(common.DASHAsset).Amount.Uint64(), Equals, uint64(101*common.One))
		c.Assert(actual.InboundTxCount, Equals, vault.InboundTxCount+1)
		state, err = mgr.Keeper().GetInstantSend(ctx, common.DASHChain, vault.PubKey)
		c.Assert(err, IsNil)
		c.Assert(state.Pending, HasLen, 0)
		c.Assert(mgr.Keeper().HasSwapQueueItem(ctx, tx.Tx.ID, 0) || mgr.Keeper().HasAdvSwapQueueItem(ctx, tx.Tx.ID, 0), Equals, true)
	}
}

func (s *DashAccountingSuite) TestInstantSendFixedCapacityAndSettlement(c *C) {
	ctx, mgr, vault, tx := dashAccountingFixture(c)
	mgr.Keeper().SetMimir(ctx, "DashInstantSendMaxAmount", 1000*common.One)
	// A previously stored count setting no longer disables admission.
	mgr.Keeper().SetMimir(ctx, "DashInstantSendMaxCount", 0)
	node, key := nodeWithPrivKey(c, mgr.Keeper(), ctx)
	pool := NewPool()
	pool.Asset, pool.Status = common.DASHAsset, PoolAvailable
	pool.BalanceAsset, pool.BalanceRune = cosmos.NewUint(1000*common.One), cosmos.NewUint(1000*common.One)
	c.Assert(mgr.Keeper().SetPool(ctx, pool), IsNil)
	send := func(observation common.ObservedTx) {
		q := &common.QuorumTx{ObsTx: observation, Inbound: true, Attestations: []*common.Attestation{attestObservedTx(c, key, observation, true)}}
		_, err := NewObservedTxQuorumHandler(mgr).Run(ctx, types.NewMsgObservedTxQuorum(q, node.NodeAddress))
		c.Assert(err, IsNil)
	}
	for i := 0; i < common.InstantSendMaxPending; i++ {
		early := tx
		if i > 0 {
			early.Tx.ID = GetRandomTxHash()
			early.SpentOutputRefs = []common.OutputRef{{TxHash: GetRandomTxHash().String()}}
		}
		send(early)
		state, err := mgr.Keeper().GetInstantSend(ctx, common.DASHChain, vault.PubKey)
		c.Assert(err, IsNil)
		c.Assert(state.Pending, HasLen, i+1)
	}
	full, err := mgr.Keeper().GetInstantSend(ctx, common.DASHChain, vault.PubKey)
	c.Assert(err, IsNil)
	before, err := mgr.Keeper().GetVault(ctx, vault.PubKey)
	c.Assert(err, IsNil)
	c.Assert(before.GetCoin(common.DASHAsset).Amount.Uint64(), Equals, uint64((100+common.InstantSendMaxPending)*common.One))
	next := tx
	next.Tx.ID = GetRandomTxHash()
	next.SpentOutputRefs = []common.OutputRef{{TxHash: GetRandomTxHash().String()}}
	send(next)
	after, err := mgr.Keeper().GetVault(ctx, vault.PubKey)
	c.Assert(err, IsNil)
	c.Assert(after, DeepEquals, before)
	state, err := mgr.Keeper().GetInstantSend(ctx, common.DASHChain, vault.PubKey)
	c.Assert(err, IsNil)
	c.Assert(state, DeepEquals, full)
	c.Assert(mgr.Keeper().HasSwapQueueItem(ctx, next.Tx.ID, 0) || mgr.Keeper().HasAdvSwapQueueItem(ctx, next.Tx.ID, 0), Equals, false)
	credit, err := mgr.Keeper().GetInstantSendInput(ctx, next.SpentOutputRefs[0])
	c.Assert(err, IsNil)
	c.Assert(credit.Id.IsEmpty(), Equals, true)
	voter, err := mgr.Keeper().GetObservedTxInVoter(ctx, next.Tx.ID)
	c.Assert(err, IsNil)
	c.Assert(voter.UpdatedVault, Equals, false)

	settled := tx
	settled.InstantSend = nil
	settled.BlockHeight, settled.FinaliseHeight = 101, 101
	send(settled)
	state, err = mgr.Keeper().GetInstantSend(ctx, common.DASHChain, vault.PubKey)
	c.Assert(err, IsNil)
	c.Assert(state.Pending, HasLen, common.InstantSendMaxPending-1)
	after, err = mgr.Keeper().GetVault(ctx, vault.PubKey)
	c.Assert(err, IsNil)
	c.Assert(after.Coins, DeepEquals, before.Coins)
	send(next)
	state, err = mgr.Keeper().GetInstantSend(ctx, common.DASHChain, vault.PubKey)
	c.Assert(err, IsNil)
	c.Assert(state.Pending, HasLen, common.InstantSendMaxPending)
	c.Assert(state.PendingAmount(), DeepEquals, full.PendingAmount())
	after, err = mgr.Keeper().GetVault(ctx, vault.PubKey)
	c.Assert(err, IsNil)
	c.Assert(after.GetCoin(common.DASHAsset).Amount.Uint64(), Equals, before.GetCoin(common.DASHAsset).Amount.Uint64()+common.One)
	c.Assert(mgr.Keeper().HasSwapQueueItem(ctx, next.Tx.ID, 0) || mgr.Keeper().HasAdvSwapQueueItem(ctx, next.Tx.ID, 0), Equals, true)
}

func (s *DashAccountingSuite) TestTimeoutUsesOriginalCreditAndExposesOverdueFunds(c *C) {
	ctx, mgr, vault, tx := dashAccountingFixture(c)
	vault, voter := creditTestDeposit(c, ctx, mgr, vault, tx)
	// Outbound scheduling updates this field; the deposit's deadline must not move.
	voter.FinalisedHeight = ctx.BlockHeight() + 250
	mgr.Keeper().SetObservedTxInVoter(ctx, voter)
	before := ctx.WithBlockHeight(ctx.BlockHeight() + 299)
	checkInstantSend(before, mgr)
	assertInstantSendHalt(c, before, mgr, 0)
	expected, err := solvencyVault(before, mgr.Keeper(), vault, common.DASHChain)
	c.Assert(err, IsNil)
	c.Assert(expected.GetCoin(common.DASHAsset).Amount.Uint64(), Equals, uint64(100*common.One))
	deadline := before.WithBlockHeight(before.BlockHeight() + 1)
	checkInstantSend(deadline, mgr)
	assertInstantSendHalt(c, deadline, mgr, deadline.BlockHeight())
	expected, err = solvencyVault(deadline, mgr.Keeper(), vault, common.DASHChain)
	c.Assert(err, IsNil)
	c.Assert(expected.Coins, DeepEquals, vault.Coins)
	state, err := mgr.Keeper().GetInstantSend(deadline, common.DASHChain, vault.PubKey)
	c.Assert(err, IsNil)
	c.Assert(state.Pending[0].Height, Equals, ctx.BlockHeight())
	c.Assert(state.PendingAmount().Uint64(), Equals, uint64(common.One))
}

func (s *DashAccountingSuite) TestEmptyInstantSendDoesNotChangeGenesis(c *C) {
	ctx, mgr := setupManagerForTest(c)
	checkInstantSend(ctx.WithBlockHeight(100), mgr)
	statuses, err := mgr.Keeper().ListInstantSendStatus(ctx)
	c.Assert(err, IsNil)
	c.Assert(statuses, HasLen, 0)
	data, err := json.Marshal(ExportGenesis(ctx, mgr.Keeper()))
	c.Assert(err, IsNil)
	var fields map[string]json.RawMessage
	c.Assert(json.Unmarshal(data, &fields), IsNil)
	for _, key := range []string{"instant_send", "instant_send_status", "instant_send_credits"} {
		_, found := fields[key]
		c.Assert(found, Equals, false, Commentf(key))
	}
}

func (s *DashAccountingSuite) TestInstantSendQueryMarksVaultEligibility(c *C) {
	for _, mode := range []string{"active", "retiring", "compromised"} {
		ctx, mgr, vault, _ := dashAccountingFixture(c)
		if mode == "retiring" {
			vault.Status = RetiringVault
			c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)
		}
		if mode == "compromised" {
			mgr.Keeper().SetMimir(ctx, compromisedVaultMimirKeyPrefix+vault.PubKey.String(), 1)
		}
		server := queryServer{mgr: mgr}
		response, err := server.InstantSend(ctx, &types.QueryInstantSendRequest{Chain: "DASH"})
		c.Assert(err, IsNil)
		c.Assert(response.Vaults, HasLen, 1) // Retiring funds still need solvency reporting.
		c.Assert(response.Vaults[0].Eligible, Equals, mode == "active")
	}
}

func (s *DashAccountingSuite) TestInstantSendGenesisRejectsInconsistentCredits(c *C) {
	for _, invalid := range []string{"chain", "voter", "marker", "amount", "inputs", "height", "vault", "overdue", "orphan", "duplicate"} {
		ctx, mgr, vault, tx := dashAccountingFixture(c)
		creditTestDeposit(c, ctx, mgr, vault, tx)
		data := ExportGenesis(ctx, mgr.Keeper())
		c.Assert(validateInstantSendGenesis(data), IsNil)
		i := slices.IndexFunc(data.ObservedTxInVoters, func(v ObservedTxVoter) bool { return v.TxID.Equals(tx.Tx.ID) })
		c.Assert(i >= 0, Equals, true)
		switch invalid {
		case "chain":
			data.InstantSend[0].Chain = common.BTCChain
		case "voter":
			data.ObservedTxInVoters = nil
		case "marker":
			data.ObservedTxInVoters[i].Tx.InstantSend = nil
		case "amount":
			data.InstantSend[0].Pending[0].Amount++
		case "inputs":
			data.ObservedTxInVoters[i].Tx.SpentOutputRefs = nil
		case "height":
			data.InstantSend[0].Pending[0].Height = 0
		case "vault":
			data.Vaults = nil
		case "overdue":
			data.InstantSendStatus = []common.InstantSendStatus{{Chain: common.DASHChain, Overdue: []common.TxID{GetRandomTxHash()}}}
		case "orphan":
			data.InstantSend = nil
		case "duplicate":
			data.InstantSend = append(data.InstantSend, data.InstantSend[0])
		}
		c.Assert(validateInstantSendGenesis(data), NotNil, Commentf(invalid))
	}
}

func (s *DashAccountingSuite) TestManualRepairCanRetainInputsWithAdmissionDisabled(c *C) {
	ctx, mgr, vault, tx := dashAccountingFixture(c)
	creditTestDeposit(c, ctx, mgr, vault, tx)
	data := ExportGenesis(ctx, mgr.Keeper())
	data.InstantSend = nil // A coordinated repair has removed this loss from exposure.
	c.Assert(validateInstantSendGenesis(data), ErrorMatches, "unsettled InstantSend credit is not pending")
	data.InstantSendStatus = []common.InstantSendStatus{{Chain: common.DASHChain, Disabled: true}}
	c.Assert(validateInstantSendGenesis(data), IsNil)
}

type instantSendPruneFailure struct{ keeper.Keeper }

func (s *DashAccountingSuite) TestOtherChainSolvencyRecoveryKeepsExistingBehavior(c *C) {
	ctx, mgr, vault, _ := dashAccountingFixture(c)
	vault.Chains = []string{"BTC"}
	vault.Coins = common.NewCoins(common.NewCoin(common.BTCAsset, cosmos.NewUint(100*common.One)))
	c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)
	c.Assert(mgr.Keeper().SetLastChainHeight(ctx, common.BTCChain, 100), IsNil)
	nodes := NodeAccounts{GetRandomValidatorNode(NodeActive), GetRandomValidatorNode(NodeActive), GetRandomValidatorNode(NodeActive), GetRandomValidatorNode(NodeActive)}
	other := GetRandomPubKey()
	flagVaultInsolvent(ctx, mgr.Keeper(), other, common.BTCChain)
	mgr.Keeper().SetMimir(ctx, "SolvencyHaltBTCChain", 10)
	voter := NewSolvencyVoter(GetRandomTxHash(), common.BTCChain, vault.PubKey, vault.Coins, 100)
	msg := &common.Solvency{Id: voter.Id, Chain: common.BTCChain, PubKey: vault.PubKey, Coins: vault.Coins, Height: 100}
	for _, node := range nodes[:2] {
		c.Assert(processSolvencyAttestation(ctx, mgr, &voter, node.NodeAddress, nodes, msg, false), IsNil)
	}
	halt, err := mgr.Keeper().GetMimir(ctx, "SolvencyHaltBTCChain")
	c.Assert(err, IsNil)
	c.Assert(halt, Equals, int64(0))
	flag, err := mgr.Keeper().GetMimir(ctx, solvencyHaltVaultKey(other, common.BTCChain))
	c.Assert(err, IsNil)
	c.Assert(flag > 0, Equals, true)
}

func (k instantSendPruneFailure) PruneInstantSendCredits(ctx cosmos.Context, _ int64, _ int) error {
	k.SetMimir(ctx, "PrunePartialWrite", 1)
	return errors.New("injected prune failure")
}

func (s *DashAccountingSuite) TestPruneFailurePreservesInputsAndHeightBarrier(c *C) {
	ctx, mgr, _, tx := dashAccountingFixture(c)
	tx.InstantSend = nil
	_, err := recordInstantSendInputs(ctx, mgr.Keeper(), tx)
	c.Assert(err, IsNil)
	c.Assert(mgr.Keeper().SetLastChainHeight(ctx, common.DASHChain, 125), IsNil)
	pruneInstantSendCredits(ctx, instantSendPruneFailure{mgr.Keeper()})
	status, err := mgr.Keeper().GetInstantSendStatus(ctx, common.DASHChain)
	c.Assert(err, IsNil)
	c.Assert(status.MinHeight, Equals, int64(0))
	value, err := mgr.Keeper().GetMimir(ctx, "PrunePartialWrite")
	c.Assert(err, IsNil)
	c.Assert(value, Equals, int64(-1))
	credit, err := mgr.Keeper().GetInstantSendInput(ctx, tx.SpentOutputRefs[0])
	c.Assert(err, IsNil)
	c.Assert(credit.Id, Equals, tx.Tx.ID)
	pruneInstantSendCredits(ctx, mgr.Keeper())
	credit, err = mgr.Keeper().GetInstantSendInput(ctx, tx.SpentOutputRefs[0])
	c.Assert(err, IsNil)
	c.Assert(credit.Id.IsEmpty(), Equals, true)
}

func (s *DashAccountingSuite) TestChainLockVotesAccumulateAndCreditLateObservers(c *C) {
	for _, mode := range []string{"quorum", "legacy", "churn", "late"} {
		ctx, mgr, vault, early := dashAccountingFixture(c)
		mgr.Keeper().SetMimir(ctx, "ObservationDelayFlexibilityDASH", 10)
		nodes := make(NodeAccounts, 4)
		keys := make([]secp256k1.PrivKey, 4)
		for i := range nodes {
			nodes[i], keys[i] = nodeWithPrivKey(c, mgr.Keeper(), ctx)
		}
		pool := NewPool()
		pool.Asset, pool.Status = common.DASHAsset, PoolAvailable
		pool.BalanceAsset, pool.BalanceRune = cosmos.NewUint(1000*common.One), cosmos.NewUint(1000*common.One)
		c.Assert(mgr.Keeper().SetPool(ctx, pool), IsNil)
		send := func(tx common.ObservedTx, indices ...int) error {
			q := &common.QuorumTx{ObsTx: tx, Inbound: true}
			for _, i := range indices {
				q.Attestations = append(q.Attestations, attestObservedTx(c, keys[i], tx, true))
			}
			_, err := NewObservedTxQuorumHandler(mgr).Run(ctx, types.NewMsgObservedTxQuorum(q, nodes[1].NodeAddress))
			return err
		}
		pending := func(count int) {
			state, err := mgr.Keeper().GetInstantSend(ctx, common.DASHChain, vault.PubKey)
			c.Assert(err, IsNil)
			c.Assert(state.Pending, HasLen, count, Commentf(mode))
		}
		c.Assert(send(early, 0, 1, 2), IsNil)
		pending(1)
		before, err := mgr.Keeper().GetObservedTxInVoter(ctx, early.Tx.ID)
		c.Assert(err, IsNil)
		block := early
		block.InstantSend = nil
		block.BlockHeight, block.FinaliseHeight = 101, 101
		ctx = ctx.WithBlockHeight(ctx.BlockHeight() + 1)
		c.Assert(send(block, 0, 1), IsNil)
		pending(1)
		c.Assert(send(block, 0, 1), IsNil) // Replaying shares cannot reach quorum.
		pending(1)
		voter, err := mgr.Keeper().GetObservedTxInVoter(ctx, early.Tx.ID)
		c.Assert(err, IsNil)
		c.Assert(voter.Txs, HasLen, 2)
		c.Assert(voter.Txs[1].Signers, HasLen, 2)

		mismatch := block
		mismatch.Tx.Coins = common.NewCoins(common.NewCoin(common.DASHAsset, cosmos.NewUint(2*common.One)))
		c.Assert(send(mismatch, 2), ErrorMatches, "ChainLock deposit mismatch")
		pending(1)
		switch mode {
		case "legacy":
			_, err = NewObservedTxInHandler(mgr).Run(ctx, NewMsgObservedTxIn(common.ObservedTxs{block}, nodes[2].NodeAddress))
			c.Assert(err, IsNil)
		case "churn":
			nodes[0].Status = NodeStandby
			c.Assert(mgr.Keeper().SetNodeAccount(ctx, nodes[0]), IsNil)
			nodeWithPrivKey(c, mgr.Keeper(), ctx) // Keep four active validators.
			c.Assert(send(block, 2), IsNil)
			pending(1) // The departed validator's stored signature cannot settle it.
			other := block
			other.BlockHeight, other.FinaliseHeight = 102, 102
			c.Assert(send(other, 3), IsNil)
			pending(1) // A different block variant cannot complete this certificate.
			c.Assert(send(block, 3), IsNil)
		default:
			c.Assert(send(block, 2), IsNil)
		}
		pending(0)
		if mode == "late" {
			ctx = ctx.WithBlockHeight(before.FinalisedHeight + 11)
		}
		c.Assert(send(block, 3), IsNil) // Credit still works after the ledger entry is gone.
		points, err := mgr.Keeper().GetNodeAccountSlashPoints(ctx, nodes[3].NodeAddress)
		c.Assert(err, IsNil)
		if mode == "late" {
			c.Assert(points > 0, Equals, true)
		} else {
			c.Assert(points, Equals, int64(0))
		}
		c.Assert(send(block, 3), IsNil)
		repeated, err := mgr.Keeper().GetNodeAccountSlashPoints(ctx, nodes[3].NodeAddress)
		c.Assert(err, IsNil)
		c.Assert(repeated, Equals, points)
		voter, err = mgr.Keeper().GetObservedTxInVoter(ctx, early.Tx.ID)
		c.Assert(err, IsNil)
		c.Assert(voter.Tx.InstantSend, NotNil)
		c.Assert(voter.FinalisedHeight, Equals, before.FinalisedHeight)
		c.Assert(voter.Tx.HasSigned(nodes[3].NodeAddress), Equals, mode != "late")
		actual, err := mgr.Keeper().GetVault(ctx, vault.PubKey)
		c.Assert(err, IsNil)
		c.Assert(actual.GetCoin(common.DASHAsset).Amount.Uint64(), Equals, uint64(101*common.One))
		c.Assert(actual.InboundTxCount, Equals, vault.InboundTxCount+1)
		c.Assert(mgr.Keeper().HasSwapQueueItem(ctx, early.Tx.ID, 0) || mgr.Keeper().HasAdvSwapQueueItem(ctx, early.Tx.ID, 0), Equals, true)
	}
}
