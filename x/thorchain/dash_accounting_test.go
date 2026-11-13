package thorchain

import (
	"errors"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
	. "gopkg.in/check.v1"
)

type DashAccountingSuite struct{}

var _ = Suite(&DashAccountingSuite{})

func dashAccountingFixture(c *C) (cosmos.Context, *Mgrs, Vault, common.ObservedTx) {
	ctx, mgr := setupManagerForTest(c)
	ctx = ctx.WithBlockHeight(1024)
	vault := NewVault(1, ActiveVault, AsgardVault, GetRandomPubKey(), []string{"DASH"}, nil)
	vault.Coins = common.NewCoins(common.NewCoin(common.DASHAsset, cosmos.NewUint(100*common.One)))
	c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)
	to, err := vault.PubKey.GetAddress(common.DASHChain)
	c.Assert(err, IsNil)
	from, err := GetRandomPubKey().GetAddress(common.DASHChain)
	c.Assert(err, IsNil)
	tx := common.NewObservedTx(common.Tx{
		ID: GetRandomTxHash(), Chain: common.DASHChain, FromAddress: from, ToAddress: to,
		Coins: common.NewCoins(common.NewCoin(common.DASHAsset, cosmos.NewUint(common.One))), Gas: common.Gas{common.NewCoin(common.DASHAsset, cosmos.NewUint(1000))}, Memo: "=:THOR.RUNE:" + GetRandomTHORAddress().String(),
	}, 96, vault.PubKey, 96)
	tx.InstantSend = &common.InstantSend{}
	tx.SpentOutputRefs = []common.OutputRef{{TxHash: GetRandomTxHash().String()}}
	mgr.Keeper().SetMimir(ctx, "DashInstantSendEnabled", 1)
	mgr.Keeper().SetMimir(ctx, "DashInstantSendMaxAmount", 10*common.One)
	c.Assert(mgr.Keeper().SetLastChainHeight(ctx, common.DASHChain, 100), IsNil)
	return ctx, mgr, vault, tx
}

func creditTestDeposit(c *C, ctx cosmos.Context, mgr *Mgrs, vault Vault, tx common.ObservedTx) (Vault, ObservedTxVoter) {
	state, allowed, err := prepareInstantSendCredit(ctx, mgr, tx, ObservedTxVoter{}, 3, 4)
	c.Assert(err, IsNil)
	c.Assert(allowed, Equals, true)
	c.Assert(state, NotNil)
	c.Assert(mgr.Keeper().SetInstantSendCredit(ctx, common.InstantSendCredit{Id: tx.Tx.ID, Inputs: tx.SpentOutputRefs, Early: true}), IsNil)
	voter := NewObservedTxVoter(tx.Tx.ID, nil)
	voter.Tx = tx
	voter.Txs = common.ObservedTxs{tx}
	voter.UpdatedVault = true
	voter.FinalisedHeight = ctx.BlockHeight()
	mgr.Keeper().SetObservedTxInVoter(ctx, voter)
	vault.AddFunds(tx.Tx.Coins)
	c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)
	return vault, voter
}

// Ledger-focused tests supply a certificate directly; handler tests below exercise
// authentication, partial delivery, validator churn and observation credit.
func settleTestInstantSend(ctx cosmos.Context, mgr *Mgrs, tx common.ObservedTx, voter ObservedTxVoter, signers []cosmos.AccAddress, active int) error {
	nodes := make(NodeAccounts, active)
	for i := range nodes {
		nodes[i] = GetRandomValidatorNode(NodeActive)
		if i < len(signers) {
			nodes[i].NodeAddress = signers[i]
		}
	}
	return settleInstantSend(ctx, mgr, tx, voter, signers, nodes, false)
}

func (s *DashAccountingSuite) TestAdmissionPolicy(c *C) {
	for _, mode := range []string{"accept", "disabled", "halted", "zero amount", "minimum", "partial", "future", "non swap", "retiring", "vault sender"} {
		ctx, mgr, vault, tx := dashAccountingFixture(c)
		signatures := 3
		switch mode {
		case "disabled":
			mgr.Keeper().SetMimir(ctx, "DashInstantSendEnabled", 0)
		case "halted":
			mgr.Keeper().SetMimir(ctx, "HaltDASHTrading", ctx.BlockHeight())
		case "zero amount":
			mgr.Keeper().SetMimir(ctx, "DashInstantSendMaxAmount", 0)
		case "minimum":
			mgr.Keeper().SetMimir(ctx, "DashInstantSendMinAmount", 2*common.One)
		case "partial":
			signatures = 2
		case "future":
			tx.BlockHeight = 101
			tx.FinaliseHeight = 101
		case "non swap":
			tx.Tx.Memo = "NOOP"
		case "retiring":
			vault.Status = RetiringVault
			c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)
		case "vault sender":
			tx.Tx.FromAddress = tx.Tx.ToAddress
		}
		_, allowed, err := prepareInstantSendCredit(ctx, mgr, tx, ObservedTxVoter{}, signatures, 4)
		c.Assert(err, IsNil, Commentf(mode))
		c.Assert(allowed, Equals, mode == "accept", Commentf(mode))
	}
}

func (s *DashAccountingSuite) TestExposureAndIndependentSettlement(c *C) {
	ctx, mgr, vault, tx := dashAccountingFixture(c)
	vault, voter := creditTestDeposit(c, ctx, mgr, vault, tx)
	available, err := solvencyVault(ctx, mgr.Keeper(), vault, common.DASHChain)
	c.Assert(err, IsNil)
	c.Assert(available.GetCoin(common.DASHAsset).Amount.Uint64(), Equals, uint64(100*common.One))
	c.Assert(vault.GetCoin(common.DASHAsset).Amount.Uint64(), Equals, uint64(101*common.One))
	signers := []cosmos.AccAddress{GetRandomBech32Addr(), GetRandomBech32Addr(), GetRandomBech32Addr()}
	block := tx
	block.InstantSend = nil
	block.BlockHeight = 101
	block.FinaliseHeight = 101
	c.Assert(settleTestInstantSend(ctx, mgr, tx, voter, signers, 4), IsNil) // early signatures do not settle
	c.Assert(settleTestInstantSend(ctx, mgr, block, voter, signers[:2], 4), IsNil)
	state, err := mgr.Keeper().GetInstantSend(ctx, common.DASHChain, vault.PubKey)
	c.Assert(err, IsNil)
	c.Assert(state.Pending, HasLen, 1)
	mismatch := block
	mismatch.Tx.Coins = block.Tx.Coins.Copy()
	mismatch.Tx.Coins[0].Amount = cosmos.NewUint(2 * common.One)
	c.Assert(settleTestInstantSend(ctx, mgr, mismatch, voter, signers, 4), ErrorMatches, "ChainLock deposit mismatch")
	c.Assert(settleTestInstantSend(ctx, mgr, block, voter, signers, 4), IsNil)
	c.Assert(settleTestInstantSend(ctx, mgr, block, voter, signers, 4), IsNil)
	state, err = mgr.Keeper().GetInstantSend(ctx, common.DASHChain, vault.PubKey)
	c.Assert(err, IsNil)
	c.Assert(state.Pending, HasLen, 0)
	actual, err := mgr.Keeper().GetVault(ctx, vault.PubKey)
	c.Assert(err, IsNil)
	c.Assert(actual.Coins, DeepEquals, vault.Coins)
	available, err = solvencyVault(ctx, mgr.Keeper(), actual, common.DASHChain)
	c.Assert(err, IsNil)
	c.Assert(available.Coins, DeepEquals, vault.Coins)
	height, err := mgr.Keeper().GetLastChainHeight(ctx, common.DASHChain)
	c.Assert(err, IsNil)
	c.Assert(height, Equals, int64(100))
}

func (s *DashAccountingSuite) TestAmountLimitAcrossVaultsAndRevertedExposure(c *C) {
	ctx, mgr, vault, tx := dashAccountingFixture(c)
	mgr.Keeper().SetMimir(ctx, "DashInstantSendMaxAmount", common.One)
	vault, voter := creditTestDeposit(c, ctx, mgr, vault, tx)
	other := NewVault(1, ActiveVault, AsgardVault, GetRandomPubKey(), []string{"DASH"}, nil)
	other.Coins = vault.Coins.Copy()
	c.Assert(mgr.Keeper().SetVault(ctx, other), IsNil)
	next := tx
	next.Tx.ID = GetRandomTxHash()
	next.ObservedPubKey = other.PubKey
	var err error
	next.Tx.ToAddress, err = other.PubKey.GetAddress(common.DASHChain)
	c.Assert(err, IsNil)
	_, allowed, err := prepareInstantSendCredit(ctx, mgr, next, ObservedTxVoter{}, 3, 4)
	c.Assert(err, IsNil)
	c.Assert(allowed, Equals, false)
	voter.Reverted = true
	mgr.Keeper().SetObservedTxInVoter(ctx, voter)
	vault.Coins = vault.Coins.SafeSub(tx.Tx.Coins[0])
	c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)
	_, err = solvencyVault(ctx, mgr.Keeper(), vault, common.DASHChain)
	c.Assert(err, ErrorMatches, "inconsistent InstantSend credit")
	_, allowed, err = prepareInstantSendCredit(ctx, mgr, next, ObservedTxVoter{}, 3, 4)
	c.Assert(err, IsNil)
	c.Assert(allowed, Equals, false)
}

func (s *DashAccountingSuite) TestOutboundSchedulesPendingCredit(c *C) {
	ctx, mgr, vault, tx := dashAccountingFixture(c)
	vault, _ = creditTestDeposit(c, ctx, mgr, vault, tx)
	output := TxOutItem{
		Chain: common.DASHChain, VaultPubKey: vault.PubKey, ToAddress: tx.Tx.FromAddress,
		Coin:   common.NewCoin(common.DASHAsset, cosmos.NewUint(101*common.One)),
		MaxGas: common.Gas{common.NewCoin(common.DASHAsset, cosmos.NewUint(1000))},
		InHash: tx.Tx.ID, Memo: NewOutboundMemo(tx.Tx.ID).String(),
	}
	err := mgr.TxOutStore().UnSafeAddTxOutItem(ctx, mgr, output, ctx.BlockHeight())
	c.Assert(err, IsNil)
	out, err := mgr.Keeper().GetTxOut(ctx, ctx.BlockHeight())
	c.Assert(err, IsNil)
	c.Assert(out.TxArray, HasLen, 1)
	c.Assert(out.TxArray[0].Coin, DeepEquals, output.Coin)
}

func (s *DashAccountingSuite) TestInsolventVaultBlocksRecovery(c *C) {
	ctx, mgr, vault, _ := dashAccountingFixture(c)
	other := GetRandomPubKey()
	flagVaultInsolvent(ctx, mgr.Keeper(), other, common.DASHChain)
	blocked, err := chainHasInsolventVault(ctx, mgr.Keeper(), common.DASHChain)
	c.Assert(err, IsNil)
	c.Assert(blocked, Equals, true)
	clearVaultInsolvent(ctx, mgr.Keeper(), vault.PubKey, common.DASHChain)
	blocked, err = chainHasInsolventVault(ctx, mgr.Keeper(), common.DASHChain)
	c.Assert(err, IsNil)
	c.Assert(blocked, Equals, true)
	clearVaultInsolvent(ctx, mgr.Keeper(), other, common.DASHChain)
	blocked, err = chainHasInsolventVault(ctx, mgr.Keeper(), common.DASHChain)
	c.Assert(err, IsNil)
	c.Assert(blocked, Equals, false)
}

func (s *DashAccountingSuite) TestPositiveAnchorIgnoresEarlyTransfers(c *C) {
	ctx, mgr, _, tx := dashAccountingFixture(c)
	for _, memo := range []string{"MIGRATE:1", "CONSOLIDATE", tx.Tx.Memo} {
		tx.Tx.Memo = memo
		tx.BlockHeight, tx.FinaliseHeight = 101, 101 // Actual zero-confirmation Bifrost encoding.
		tx.InstantSend = nil
		c.Assert(tx.IsFinal(), Equals, true)
		c.Assert(updateInboundChainHeight(ctx, mgr.Keeper(), tx), IsNil)
		height, err := mgr.Keeper().GetLastChainHeight(ctx, common.DASHChain)
		c.Assert(err, IsNil)
		c.Assert(height, Equals, int64(100))
	}
	tx.Tx.Chain = common.BTCChain
	c.Assert(updateInboundChainHeight(ctx, mgr.Keeper(), tx), IsNil)
	height, err := mgr.Keeper().GetLastChainHeight(ctx, common.BTCChain)
	c.Assert(err, IsNil)
	c.Assert(height, Equals, int64(101))
}

type dashAdmissionWriteFailure struct{ keeper.Keeper }

func (k dashAdmissionWriteFailure) SetInstantSend(cosmos.Context, common.InstantSendState) error {
	return errors.New("accounting write failed")
}

// The cached transaction must roll back vault credit and voters when reserving
// the network slot fails, rather than leaving an uncounted early credit.
func (s *DashAccountingSuite) TestAtomicCreditFailure(c *C) {
	ctx, mgr, vault, tx := dashAccountingFixture(c)
	node, key := nodeWithPrivKey(c, mgr.Keeper(), ctx)
	base := mgr.Keeper()
	mgr.K = dashAdmissionWriteFailure{base}
	cached, _ := ctx.CacheContext()
	q := &common.QuorumTx{ObsTx: tx, Inbound: true, Attestations: []*common.Attestation{attestObservedTx(c, key, tx, true)}}
	_, err := NewObservedTxQuorumHandler(mgr).Run(cached, types.NewMsgObservedTxQuorum(q, node.NodeAddress))
	c.Assert(err, NotNil)
	actual, err := base.GetVault(ctx, vault.PubKey)
	c.Assert(err, IsNil)
	c.Assert(actual.Coins, DeepEquals, vault.Coins)
	voter, err := base.GetObservedTxInVoter(ctx, tx.Tx.ID)
	c.Assert(err, IsNil)
	c.Assert(voter.Txs, HasLen, 0)
}

// Admission, vault credit and queue insertion commit together; the second
// certificate only frees the exposure and records ChainLock evidence.
func (s *DashAccountingSuite) TestQuorumCreditAndSettlement(c *C) {
	ctx, mgr, vault, tx := dashAccountingFixture(c)
	node, key := nodeWithPrivKey(c, mgr.Keeper(), ctx)
	pool := NewPool()
	pool.Asset = common.DASHAsset
	pool.Status = PoolAvailable
	pool.BalanceAsset = cosmos.NewUint(1000 * common.One)
	pool.BalanceRune = cosmos.NewUint(1000 * common.One)
	c.Assert(mgr.Keeper().SetPool(ctx, pool), IsNil)
	send := func(observation common.ObservedTx) {
		q := &common.QuorumTx{ObsTx: observation, Inbound: true, Attestations: []*common.Attestation{attestObservedTx(c, key, observation, true)}}
		_, err := NewObservedTxQuorumHandler(mgr).Run(ctx, types.NewMsgObservedTxQuorum(q, node.NodeAddress))
		c.Assert(err, IsNil)
	}
	send(tx)
	voter, err := mgr.Keeper().GetObservedTxInVoter(ctx, tx.Tx.ID)
	c.Assert(err, IsNil)
	c.Assert(voter.UpdatedVault, Equals, true)
	c.Assert(voter.FinalisedHeight, Equals, ctx.BlockHeight())
	c.Assert(mgr.Keeper().HasSwapQueueItem(ctx, tx.Tx.ID, 0) || mgr.Keeper().HasAdvSwapQueueItem(ctx, tx.Tx.ID, 0), Equals, true)
	actual, err := mgr.Keeper().GetVault(ctx, vault.PubKey)
	c.Assert(err, IsNil)
	c.Assert(actual.GetCoin(common.DASHAsset).Amount.Uint64(), Equals, uint64(101*common.One))
	state, err := mgr.Keeper().GetInstantSend(ctx, common.DASHChain, vault.PubKey)
	c.Assert(err, IsNil)
	c.Assert(state.Pending, HasLen, 1)
	block := tx
	block.InstantSend = nil
	block.BlockHeight, block.FinaliseHeight = 101, 101
	before, err := mgr.Keeper().GetNodeAccountSlashPoints(ctx, node.NodeAddress)
	c.Assert(err, IsNil)
	send(block)
	send(block)
	state, err = mgr.Keeper().GetInstantSend(ctx, common.DASHChain, vault.PubKey)
	c.Assert(err, IsNil)
	c.Assert(state.Pending, HasLen, 0)
	actual, err = mgr.Keeper().GetVault(ctx, vault.PubKey)
	c.Assert(err, IsNil)
	c.Assert(actual.GetCoin(common.DASHAsset).Amount.Uint64(), Equals, uint64(101*common.One))
	after, err := mgr.Keeper().GetNodeAccountSlashPoints(ctx, node.NodeAddress)
	c.Assert(err, IsNil)
	c.Assert(after, Equals, before)
}

func (s *DashAccountingSuite) TestRejectedEarlySwapLeavesNoCredit(c *C) {
	for _, memo := range []string{"NOOP", "=:INVALID", "", "halted"} {
		ctx, mgr, vault, tx := dashAccountingFixture(c)
		node, key := nodeWithPrivKey(c, mgr.Keeper(), ctx)
		if memo == "halted" {
			mgr.Keeper().SetMimir(ctx, "HaltTrading", 1)
		} else {
			tx.Tx.Memo = memo
		}
		q := &common.QuorumTx{ObsTx: tx, Inbound: true, Attestations: []*common.Attestation{attestObservedTx(c, key, tx, true)}}
		_, err := NewObservedTxQuorumHandler(mgr).Run(ctx, types.NewMsgObservedTxQuorum(q, node.NodeAddress))
		c.Assert(err, IsNil)
		actual, err := mgr.Keeper().GetVault(ctx, vault.PubKey)
		c.Assert(err, IsNil)
		c.Assert(actual.Coins, DeepEquals, vault.Coins)
		state, err := mgr.Keeper().GetInstantSend(ctx, common.DASHChain, vault.PubKey)
		c.Assert(err, IsNil)
		c.Assert(state.Pending, HasLen, 0)
		voter, err := mgr.Keeper().GetObservedTxInVoter(ctx, tx.Tx.ID)
		c.Assert(err, IsNil)
		c.Assert(voter.FinalisedHeight, Equals, int64(0))
	}
}

func (s *DashAccountingSuite) TestSolvencyRequiresRecoveryQuorumForEveryVault(c *C) {
	ctx, mgr, vault, _ := dashAccountingFixture(c)
	other := vault
	other.PubKey = GetRandomPubKey()
	c.Assert(mgr.Keeper().SetVault(ctx, other), IsNil)
	nodes := NodeAccounts{GetRandomValidatorNode(NodeActive), GetRandomValidatorNode(NodeActive), GetRandomValidatorNode(NodeActive), GetRandomValidatorNode(NodeActive)}
	for _, node := range nodes {
		c.Assert(mgr.Keeper().SetNodeAccount(ctx, node), IsNil)
	}
	report := func(v Vault, coins common.Coins, count int) types.SolvencyVoter {
		voter := NewSolvencyVoter(GetRandomTxHash(), common.DASHChain, v.PubKey, coins, 100)
		msg := &common.Solvency{Id: voter.Id, Chain: common.DASHChain, PubKey: v.PubKey, Coins: coins, Height: 100}
		for _, node := range nodes[:count] {
			c.Assert(processSolvencyAttestation(ctx, mgr, &voter, node.NodeAddress, nodes, msg, false), IsNil)
		}
		return voter
	}
	report(vault, nil, 2)
	report(other, nil, 2)
	ctx = ctx.WithBlockHeight(1025)
	voter := report(other, other.Coins, 2)
	c.Assert(voter.ConsensusBlockHeight, Equals, int64(0))
	flag, err := mgr.Keeper().GetMimir(ctx, solvencyHaltVaultKey(other.PubKey, common.DASHChain))
	c.Assert(err, IsNil)
	c.Assert(flag > 0, Equals, true)
	report(other, other.Coins, 3)
	halt, err := mgr.Keeper().GetMimir(ctx, "SolvencyHaltDASHChain")
	c.Assert(err, IsNil)
	c.Assert(halt > 0, Equals, true) // first vault is still insolvent
	report(vault, vault.Coins, 3)
	halt, err = mgr.Keeper().GetMimir(ctx, "SolvencyHaltDASHChain")
	c.Assert(err, IsNil)
	c.Assert(halt, Equals, int64(0))
}

func (s *DashAccountingSuite) TestGenesisPreservesExposure(c *C) {
	ctx, mgr, vault, tx := dashAccountingFixture(c)
	vault, _ = creditTestDeposit(c, ctx, mgr, vault, tx)
	exported := ExportGenesis(ctx, mgr.Keeper())
	c.Assert(exported.InstantSend, HasLen, 1)
	nextCtx, nextMgr := setupManagerForTest(c)
	nextCtx = nextCtx.WithBlockHeight(ctx.BlockHeight())
	InitGenesis(nextCtx, nextMgr.Keeper(), exported)
	actual, err := nextMgr.Keeper().GetVault(nextCtx, vault.PubKey)
	c.Assert(err, IsNil)
	available, err := solvencyVault(nextCtx, nextMgr.Keeper(), actual, common.DASHChain)
	c.Assert(err, IsNil)
	c.Assert(actual.GetCoin(common.DASHAsset).Amount.Uint64(), Equals, uint64(101*common.One))
	c.Assert(available.GetCoin(common.DASHAsset).Amount.Uint64(), Equals, uint64(100*common.One))
}
