package thorchain

import (
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/config"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

func covQObservedTx(c *C, memo string, blockHeight, finaliseHeight int64, signers ...cosmos.AccAddress) common.ObservedTx {
	tx := GetRandomTx()
	tx.Chain = common.BTCChain
	tx.Coins = common.NewCoins(common.NewCoin(common.BTCAsset, cosmos.NewUint(common.One)))
	tx.Memo = memo
	obs := common.NewObservedTx(tx, blockHeight, GetRandomPubKey(), finaliseHeight)
	for _, s := range signers {
		c.Assert(obs.Sign(s), Equals, true)
	}
	return obs
}

func (s *QuerierCovQSuite) TestCountSignersCovQ(c *C) {
	a1, a2, a3 := GetRandomBech32Addr(), GetRandomBech32Addr(), GetRandomBech32Addr()
	final1 := covQObservedTx(c, "", 10, 10, a1)
	final2 := final1
	final2.Signers = nil
	c.Assert(final2.Sign(a2), Equals, true)
	notFinal := final1
	notFinal.Signers = nil
	notFinal.FinaliseHeight = 20
	c.Assert(notFinal.Sign(a3), Equals, true)
	// a different tx is never merged with the others
	other := covQObservedTx(c, "", 10, 10, a3)

	voter := NewObservedTxVoter(final1.Tx.ID, []common.ObservedTx{final1, notFinal, final2, other})
	notFinalCount, finalCount := countSigners(voter)
	c.Assert(notFinalCount, Equals, int64(1))
	c.Assert(finalCount, Equals, int64(2))

	empty0, empty1 := countSigners(ObservedTxVoter{})
	c.Assert(empty0, Equals, int64(0))
	c.Assert(empty1, Equals, int64(0))
}

func (s *QuerierCovQSuite) TestNewTxStagesResponseCovQ(c *C) {
	ctx := s.ctx.WithBlockHeight(100)
	signer := GetRandomBech32Addr()

	// nothing observed yet
	res := newTxStagesResponse(ctx, ObservedTxVoter{}, false, false, false, StreamingSwap{})
	c.Assert(res.InboundObserved.Completed, Equals, false)
	c.Assert(res.InboundObserved.Started, Equals, false)
	c.Assert(res.InboundFinalised, IsNil)

	// observations, but no consensus
	obs := covQObservedTx(c, "", 50, 60, signer)
	voter := NewObservedTxVoter(obs.Tx.ID, []common.ObservedTx{obs})
	res = newTxStagesResponse(ctx, voter, false, false, false, StreamingSwap{})
	c.Assert(res.InboundObserved.Started, Equals, true)
	c.Assert(res.InboundObserved.Completed, Equals, false)
	c.Assert(res.InboundObserved.PreConfirmationCount, Equals, int64(1))

	// consensus reached, still counting confirmations
	voter.Tx = obs
	voter.Height = 95
	res = newTxStagesResponse(ctx, voter, false, false, false, StreamingSwap{})
	c.Assert(res.InboundObserved.Completed, Equals, true)
	cc := res.InboundConfirmationCounted
	c.Assert(cc, NotNil)
	c.Assert(cc.Completed, Equals, false)
	c.Assert(cc.CountingStartHeight, Equals, int64(95))
	c.Assert(cc.Chain, Equals, "BTC")
	c.Assert(cc.ExternalObservedHeight, Equals, int64(50))
	c.Assert(cc.ExternalConfirmationDelayHeight, Equals, int64(60))
	expSec := (common.BTCChain.ApproximateBlockMilliseconds()*10 - 5*common.THORChain.ApproximateBlockMilliseconds()) / 1000
	c.Assert(cc.RemainingConfirmationSeconds, Equals, expSec)
	c.Assert(res.InboundFinalised.Completed, Equals, false)
	c.Assert(res.SwapStatus.Pending, Equals, false)
	c.Assert(res.SwapStatus.Streaming, IsNil)
	c.Assert(res.SwapFinalised, IsNil)
	c.Assert(res.OutboundSigned, IsNil)

	// very long since counting started: remaining time floors at zero
	voter.Height = 1
	res = newTxStagesResponse(ctx.WithBlockHeight(100000), voter, false, false, false, StreamingSwap{})
	c.Assert(res.InboundConfirmationCounted.RemainingConfirmationSeconds, Equals, int64(0))

	// finalised swap with a pending, streaming swap and a future outbound
	voter.Tx = covQObservedTx(c, "", 50, 50, signer)
	voter.Height = 90
	voter.FinalisedHeight = 90
	voter.OutboundHeight = 110
	ss := NewStreamingSwap(voter.TxID, 4, 2, cosmos.ZeroUint(), cosmos.NewUint(100))
	ss.Count = 1
	res = newTxStagesResponse(ctx, voter, true, true, true, ss)
	c.Assert(res.InboundConfirmationCounted.Completed, Equals, true)
	c.Assert(res.InboundFinalised.Completed, Equals, true)
	c.Assert(res.SwapStatus.Pending, Equals, true)
	c.Assert(res.SwapStatus.Streaming, NotNil)
	c.Assert(res.SwapStatus.Streaming.Quantity, Equals, int64(4))
	c.Assert(res.SwapStatus.Streaming.Interval, Equals, int64(2))
	c.Assert(res.SwapStatus.Streaming.Count, Equals, int64(1))
	c.Assert(res.SwapFinalised.Completed, Equals, false)
	c.Assert(res.OutboundDelay, NotNil)
	c.Assert(res.OutboundDelay.Completed, Equals, false)
	c.Assert(res.OutboundDelay.RemainingDelayBlocks, Equals, int64(10))
	c.Assert(res.OutboundDelay.RemainingDelaySeconds, Equals, 10*common.THORChain.ApproximateBlockMilliseconds()/1000)
	c.Assert(res.OutboundSigned.Completed, Equals, false)
	c.Assert(res.OutboundSigned.ScheduledOutboundHeight, Equals, int64(110))
	c.Assert(res.OutboundSigned.BlocksSinceScheduled, IsNil)

	// swap done, outbound delay elapsed but not yet signed
	res = newTxStagesResponse(ctx.WithBlockHeight(115), voter, true, false, false, StreamingSwap{})
	c.Assert(res.SwapFinalised.Completed, Equals, true)
	c.Assert(res.OutboundDelay.Completed, Equals, true)
	c.Assert(res.OutboundSigned.BlocksSinceScheduled, NotNil)
	c.Assert(res.OutboundSigned.BlocksSinceScheduled.Value, Equals, int64(5))

	// outbound signed; no delay stage when outbound height == finalised height
	voter.Tx.Status = common.Status_done
	voter.OutboundHeight = 90
	res = newTxStagesResponse(ctx, voter, false, false, false, StreamingSwap{})
	c.Assert(res.OutboundDelay, IsNil)
	c.Assert(res.OutboundSigned.Completed, Equals, true)
}

func (s *QuerierCovQSuite) TestCheckPendingCovQ(c *C) {
	ctx, k := s.ctx, s.mgr.Keeper()

	// not final: nothing is checked
	notFinal := covQObservedTx(c, "=:ETH.ETH:"+GetRandomETHAddress().String(), 10, 20)
	voter := NewObservedTxVoter(notFinal.Tx.ID, nil)
	voter.Tx = notFinal
	isSwap, isPending, pending, ss := checkPending(ctx, k, voter)
	c.Assert(isSwap || isPending || pending, Equals, false)
	c.Assert(ss.Valid(), NotNil)

	// final swap with a queued item and legacy streaming info
	obs := covQObservedTx(c, "=:ETH.ETH:"+GetRandomETHAddress().String(), 10, 10)
	voter = NewObservedTxVoter(obs.Tx.ID, nil)
	voter.Tx = obs
	voter.FinalisedHeight = 12
	msg := NewMsgSwap(obs.Tx, common.ETHAsset, GetRandomETHAddress(), cosmos.ZeroUint(),
		common.NoAddress, cosmos.ZeroUint(), "", "", nil, types.SwapType_market, 0, 0, types.SwapVersion_v1, GetRandomBech32Addr())
	c.Assert(k.SetSwapQueueItem(ctx, *msg, 0), IsNil)
	k.SetStreamingSwap(ctx, NewStreamingSwap(obs.Tx.ID, 3, 1, cosmos.ZeroUint(), cosmos.NewUint(common.One)))
	isSwap, isPending, pending, ss = checkPending(ctx, k, voter)
	c.Assert(isSwap, Equals, true)
	c.Assert(isPending, Equals, true)
	c.Assert(pending, Equals, true)
	c.Assert(ss.Quantity, Equals, uint64(3))

	// planned actions mean the swap is no longer reported pending
	voter.Actions = []TxOutItem{covQTxOutItem()}
	isSwap, isPending, pending, _ = checkPending(ctx, k, voter)
	c.Assert(isSwap, Equals, true)
	c.Assert(isPending, Equals, false)
	c.Assert(pending, Equals, true)

	// unparseable memo: not a swap
	bad := covQObservedTx(c, "covq-not-a-memo", 10, 10)
	voter = NewObservedTxVoter(bad.Tx.ID, nil)
	voter.Tx = bad
	isSwap, isPending, pending, _ = checkPending(ctx, k, voter)
	c.Assert(isSwap || isPending || pending, Equals, false)

	// non-swap memo
	add := covQObservedTx(c, "+:BTC.BTC", 10, 10)
	voter = NewObservedTxVoter(add.Tx.ID, nil)
	voter.Tx = add
	isSwap, _, _, _ = checkPending(ctx, k, voter)
	c.Assert(isSwap, Equals, false)
}

func (s *QuerierCovQSuite) TestTxStatusAndStagesCovQ(c *C) {
	ctx, k := s.ctx, s.mgr.Keeper()

	_, err := s.qs.TxStatus(ctx, &types.QueryTxStatusRequest{})
	c.Assert(err, ErrorMatches, "tx id not provided")
	_, err = s.qs.TxStages(ctx, &types.QueryTxStagesRequest{TxId: "zz"})
	c.Assert(err, ErrorMatches, "fail to parse tx id.*")
	_, err = s.qs.Tx(ctx, &types.QueryTxRequest{})
	c.Assert(err, ErrorMatches, "tx id not provided")

	signer := GetRandomBech32Addr()
	obs := covQObservedTx(c, "=:ETH.ETH:"+GetRandomETHAddress().String(), 10, 10, signer)
	voter := NewObservedTxVoter(obs.Tx.ID, []common.ObservedTx{obs})
	k.SetObservedTxInVoter(ctx, voter)

	// only observations: the first observation's tx is displayed
	status, err := s.qs.TxStatus(ctx, &types.QueryTxStatusRequest{TxId: obs.Tx.ID.String()})
	c.Assert(err, IsNil)
	c.Assert(status.Tx, NotNil)
	c.Assert(status.Tx.ID.Equals(obs.Tx.ID), Equals, true)
	c.Assert(status.PlannedOutTxs, IsNil)
	c.Assert(status.OutTxs, IsNil)
	c.Assert(status.Stages.InboundObserved.Started, Equals, true)

	refund := covQTxOutItem()
	refund.Memo = "REFUND:" + obs.Tx.ID.String()
	out := covQTxOutItem()
	out.Memo = "OUT:" + obs.Tx.ID.String()
	voter.Tx = obs
	voter.Height = 11
	voter.FinalisedHeight = 11
	voter.Actions = []TxOutItem{refund, out}
	voter.OutTxs = common.Txs{GetRandomTx()}
	k.SetObservedTxInVoter(ctx, voter)

	status, err = s.qs.TxStatus(ctx, &types.QueryTxStatusRequest{TxId: obs.Tx.ID.String()})
	c.Assert(err, IsNil)
	c.Assert(status.PlannedOutTxs, HasLen, 2)
	c.Assert(status.PlannedOutTxs[0].Refund, Equals, true)
	c.Assert(status.PlannedOutTxs[1].Refund, Equals, false)
	c.Assert(status.PlannedOutTxs[1].ToAddress, Equals, out.ToAddress.String())
	c.Assert(status.OutTxs, HasLen, 1)
	c.Assert(status.Stages.InboundObserved.Completed, Equals, true)
	c.Assert(status.Stages.InboundFinalised.Completed, Equals, true)
	c.Assert(status.Stages.SwapFinalised, NotNil)
	c.Assert(status.Stages.SwapFinalised.Completed, Equals, true)

	stages, err := s.qs.TxStages(ctx, &types.QueryTxStagesRequest{TxId: obs.Tx.ID.String()})
	c.Assert(err, IsNil)
	c.Assert(stages.InboundObserved.FinalCount, Equals, int64(1))
	c.Assert(stages.SwapFinalised.Completed, Equals, true)

	// an outbound-only hash is resolved via the tx out voter; the consensus
	// tx is selected from the active validators' observations
	na := GetRandomValidatorNode(NodeActive)
	c.Assert(k.SetNodeAccount(ctx, na), IsNil)
	outObs := covQObservedTx(c, "OUT:"+obs.Tx.ID.String(), 10, 10, na.NodeAddress)
	k.SetObservedTxOutVoter(ctx, NewObservedTxVoter(outObs.Tx.ID, []common.ObservedTx{outObs}))
	txResp, err := s.qs.Tx(ctx, &types.QueryTxRequest{TxId: outObs.Tx.ID.String()})
	c.Assert(err, IsNil)
	c.Assert(txResp.ObservedTx.Tx.ID.Equals(outObs.Tx.ID), Equals, true)

	missing := GetRandomTxHash()
	_, err = s.qs.Tx(ctx, &types.QueryTxRequest{TxId: missing.String()})
	c.Assert(err, ErrorMatches, "tx: .* doesn't exist")
}

func (s *QuerierCovQSuite) TestVaultErrorsCovQ(c *C) {
	ctx := s.ctx
	_, err := s.qs.Vault(ctx, &types.QueryVaultRequest{})
	c.Assert(err, ErrorMatches, "missing vault pub_key parameter")
	_, err = s.qs.Vault(ctx, &types.QueryVaultRequest{PubKey: "bad"})
	c.Assert(err, ErrorMatches, "bad is invalid pubkey")
	_, err = s.qs.Vault(ctx, &types.QueryVaultRequest{PubKey: GetRandomPubKey().String()})
	c.Assert(err, NotNil)

	_, err = s.qs.KeysignParty(ctx, &types.QueryKeysignPartyRequest{})
	c.Assert(err, ErrorMatches, "missing vault pub_key parameter")
	_, err = s.qs.KeysignParty(ctx, &types.QueryKeysignPartyRequest{PubKey: "bad"})
	c.Assert(err, ErrorMatches, "bad is invalid pubkey")
	_, err = s.qs.KeysignParty(ctx, &types.QueryKeysignPartyRequest{PubKey: GetRandomPubKey().String()})
	c.Assert(err, NotNil)

	_, err = s.qs.SolVaultNonce(ctx, &types.QuerySolVaultNonceRequest{})
	c.Assert(err, ErrorMatches, "missing vault_pub_key parameter")
	_, err = s.qs.SolVaultNonce(ctx, &types.QuerySolVaultNonceRequest{VaultPubKey: "bad"})
	c.Assert(err, ErrorMatches, "bad is invalid pubkey")
}

func (s *QuerierCovQSuite) TestVaultAddressesCovQ(c *C) {
	ctx, k := s.ctx, s.mgr.Keeper()
	vault := GetRandomVault()
	vault.Type = AsgardVault
	vault.Status = ActiveVault
	vault.PubKeyEddsa = ""
	vault.PubKeyFrost = ""
	vault.Chains = []string{common.BTCChain.String(), common.SOLChain.String(), common.XMRChain.String(), common.ZECChain.String()}
	c.Assert(k.SetVault(ctx, vault), IsNil)

	resp, err := s.qs.Vault(ctx, &types.QueryVaultRequest{PubKey: vault.PubKey.String()})
	c.Assert(err, IsNil)
	chains := map[string]string{}
	for _, a := range resp.Addresses {
		chains[a.Chain] = a.Address
	}
	// eddsa-only chains are skipped when the vault lacks the matching key
	_, hasSOL := chains["SOL"]
	_, hasXMR := chains["XMR"]
	c.Assert(hasSOL, Equals, false)
	c.Assert(hasXMR, Equals, false)
	c.Assert(chains["BTC"], Not(Equals), "")
	c.Assert(chains["THOR"], Not(Equals), "")
	// ZEC deposit addresses are shown in TEX form
	zecAddr, err := vault.PubKey.GetAddress(common.ZECChain)
	c.Assert(err, IsNil)
	tex, err := zecAddr.ToTexAddress()
	c.Assert(err, IsNil)
	c.Assert(chains["ZEC"], Equals, tex.String())

	// with an eddsa key SOL is listed
	vault.PubKeyEddsa = GetRandomEd25519PubKey()
	c.Assert(k.SetVault(ctx, vault), IsNil)
	resp, err = s.qs.Vault(ctx, &types.QueryVaultRequest{PubKey: vault.PubKey.String()})
	c.Assert(err, IsNil)
	found := false
	for _, a := range resp.Addresses {
		if a.Chain == "SOL" {
			found = true
		}
	}
	c.Assert(found, Equals, true)
}

func (s *QuerierCovQSuite) TestVaultsPubkeysInactiveCovQ(c *C) {
	k := s.mgr.Keeper()
	cutoffBlocks := config.GetThornode().VaultPubkeysCutoffBlocks
	ctx := s.ctx.WithBlockHeight(cutoffBlocks + 1000)

	na1 := GetRandomValidatorNode(NodeActive)
	na2 := GetRandomValidatorNode(NodeActive)
	c.Assert(k.SetNodeAccount(ctx, na1), IsNil)
	c.Assert(k.SetNodeAccount(ctx, na2), IsNil)
	activeMembers := []string{na1.PubKeySet.Secp256k1.String(), na2.PubKeySet.Secp256k1.String()}

	mkInactive := func(members []string, inbound, height int64) Vault {
		v := GetRandomVault()
		v.Type = AsgardVault
		v.Status = InactiveVault
		v.Membership = members
		v.InboundTxCount = inbound
		v.BlockHeight = height
		c.Assert(k.SetVault(ctx, v), IsNil)
		return v
	}
	included := mkInactive(activeMembers, 1, ctx.BlockHeight())
	mkInactive(activeMembers, 0, ctx.BlockHeight())                                                    // never received
	mkInactive(activeMembers, 1, 1)                                                                    // too old
	mkInactive([]string{GetRandomPubKey().String(), GetRandomPubKey().String()}, 1, ctx.BlockHeight()) // no active members

	resp, err := s.qs.VaultsPubkeys(ctx, &types.QueryVaultsPubkeysRequest{})
	c.Assert(err, IsNil)
	c.Assert(resp.Inactive, HasLen, 1)
	c.Assert(resp.Inactive[0].PubKey, Equals, included.PubKey.String())
	c.Assert(resp.Inactive[0].Membership, DeepEquals, activeMembers)
}

func (s *QuerierCovQSuite) TestVaultSolvencyCovQ(c *C) {
	ctx, k := s.ctx, s.mgr.Keeper()
	covQPool(c, ctx, s.mgr, common.BTCAsset, 1000*common.One, 10*common.One)

	vault := GetRandomVault()
	vault.Type = AsgardVault
	vault.Status = ActiveVault
	vault.Coins = common.NewCoins(common.NewCoin(common.BTCAsset, cosmos.NewUint(15*common.One)))
	c.Assert(k.SetVault(ctx, vault), IsNil)

	resp, err := s.qs.VaultSolvency(ctx, &types.QueryVaultSolvencyRequest{})
	c.Assert(err, IsNil)
	c.Assert(resp.Assets, HasLen, 1)
	c.Assert(resp.Assets[0].Asset.Equals(common.BTCAsset), Equals, true)
	c.Assert(resp.Assets[0].Amount.String(), Equals, cosmos.NewInt(5*common.One).String())

	// churn in progress
	retiring := GetRandomVault()
	retiring.Type = AsgardVault
	retiring.Status = RetiringVault
	c.Assert(k.SetVault(ctx, retiring), IsNil)
	_, err = s.qs.VaultSolvency(ctx, &types.QueryVaultSolvencyRequest{})
	c.Assert(err, ErrorMatches, "failed to calculate network solvency.*")
}

func (s *QuerierCovQSuite) TestKeygenPubKeyFilterCovQ(c *C) {
	ctx, k := s.ctx, s.mgr.Keeper()
	pk1, pk2 := GetRandomPubKey(), GetRandomPubKey()
	kg1, err := NewKeygen(ctx.BlockHeight(), []string{pk1.String(), GetRandomPubKey().String()}, AsgardKeygen)
	c.Assert(err, IsNil)
	kg2, err := NewKeygen(ctx.BlockHeight(), []string{pk2.String()}, AsgardKeygen)
	c.Assert(err, IsNil)
	kb := NewKeygenBlock(ctx.BlockHeight())
	kb.Keygens = []Keygen{kg1, kg2}
	k.SetKeygenBlock(ctx, kb)

	h := ctx.BlockHeight()
	resp, err := s.qs.Keygen(ctx, &types.QueryKeygenRequest{Height: cosmos.NewUint(uint64(h)).String(), PubKey: pk1.String()})
	c.Assert(err, IsNil)
	c.Assert(resp.KeygenBlock.Keygens, HasLen, 1)
	c.Assert(resp.KeygenBlock.Keygens[0].ID, Equals, kg1.ID)
	c.Assert(resp.Signature, Not(Equals), "")

	_, err = s.qs.Keygen(ctx, &types.QueryKeygenRequest{Height: "1", PubKey: "bad"})
	c.Assert(err, ErrorMatches, "fail to parse pubkey.*")
	_, err = s.qs.Keygen(ctx, &types.QueryKeygenRequest{Height: "-1"})
	c.Assert(err, ErrorMatches, "block height must not be negative")

	_, err = s.qs.KeysignPubkey(ctx, &types.QueryKeysignPubkeyRequest{Height: "1", PubKey: "bad"})
	c.Assert(err, ErrorMatches, "fail to parse pubkey.*")
	ks, err := s.qs.Keysign(ctx, &types.QueryKeysignRequest{Height: "1"})
	c.Assert(err, IsNil)
	c.Assert(ks.Signature, Not(Equals), "")
}

func (s *QuerierCovQSuite) TestQueueMemoCountsCovQ(c *C) {
	ctx, k := s.ctx, s.mgr.Keeper()
	outItem := covQTxOutItem()
	outItem.Memo = "OUT:" + GetRandomTxHash().String()
	internal := covQTxOutItem()
	internal.Memo = "MIGRATE:3"
	done := covQTxOutItem()
	done.Memo = "OUT:" + GetRandomTxHash().String()
	done.OutHash = GetRandomTxHash()
	for _, it := range []TxOutItem{outItem, internal, done} {
		c.Assert(k.AppendTxOut(ctx, ctx.BlockHeight(), it), IsNil)
	}
	resp, err := s.qs.Queue(ctx, &types.QueryQueueRequest{})
	c.Assert(err, IsNil)
	c.Assert(resp.Outbound, Equals, int64(1))
	c.Assert(resp.Internal, Equals, int64(1))
}

func (s *QuerierCovQSuite) TestLastBlocksFromActiveVaultCovQ(c *C) {
	ctx, k := s.ctx, s.mgr.Keeper()
	vault := GetRandomVault()
	vault.Type = AsgardVault
	vault.Status = ActiveVault
	vault.Chains = []string{common.THORChain.String(), common.BTCChain.String()}
	c.Assert(k.SetVault(ctx, vault), IsNil)
	c.Assert(k.SetLastChainHeight(ctx, common.BTCChain, 77), IsNil)

	resp, err := s.qs.LastBlocks(ctx, &types.QueryLastBlocksRequest{})
	c.Assert(err, IsNil)
	// THORChain itself is not listed
	c.Assert(resp.LastBlocks, HasLen, 1)
	c.Assert(resp.LastBlocks[0].Chain, Equals, "BTC")
	c.Assert(resp.LastBlocks[0].LastObservedIn, Equals, int64(77))
	c.Assert(resp.LastBlocks[0].Thorchain, Equals, ctx.BlockHeight())
}

func (s *QuerierCovQSuite) TestUpgradeProposalErrorsCovQ(c *C) {
	ctx := s.ctx
	_, err := s.qs.UpgradeProposal(ctx, &types.QueryUpgradeProposalRequest{})
	c.Assert(err, ErrorMatches, "upgrade name not provided")
	_, err = s.qs.UpgradeProposal(ctx, &types.QueryUpgradeProposalRequest{Name: "covq-none"})
	c.Assert(err, ErrorMatches, "upgrade proposal not found: covq-none")
	_, err = s.qs.UpgradeVotes(ctx, &types.QueryUpgradeVotesRequest{})
	c.Assert(err, ErrorMatches, "upgrade name not provided")
}
