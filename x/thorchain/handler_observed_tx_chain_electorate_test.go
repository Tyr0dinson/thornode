package thorchain

import (
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
)

// setupChainElectorate stores 10 active validators and an active BTC vault
// whose membership is only the first 4, so the chain's vault members are
// well short of 2/3 of the active set.
func setupChainElectorate(c *C, ctx cosmos.Context, mgr *Mgrs) (NodeAccounts, Vault) {
	nas := make(NodeAccounts, 10)
	for i := range nas {
		nas[i] = GetRandomValidatorNode(NodeActive)
		c.Assert(mgr.Keeper().SetNodeAccount(ctx, nas[i]), IsNil)
	}
	vault := GetRandomVault()
	vault.Status = ActiveVault
	vault.Type = AsgardVault
	vault.Chains = common.Chains{common.BTCChain}.Strings()
	vault.Membership = nil
	for _, na := range nas[:4] {
		vault.Membership = append(vault.Membership, na.PubKeySet.Secp256k1.String())
	}
	c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)
	mgr.Keeper().SetMimir(ctx, constants.EnableNodeChainConsensus.String(), 1)
	return nas, vault
}

func chainElectorateObservedTx(c *C, vault Vault, memo string) common.ObservedTx {
	tx := common.NewObservedTx(common.Tx{
		ID:          GetRandomTxHash(),
		Chain:       common.BTCChain,
		FromAddress: GetRandomBTCAddress(),
		ToAddress:   GetRandomBTCAddress(),
		Coins:       common.NewCoins(common.NewCoin(common.BTCAsset, cosmos.NewUint(common.One))),
		Gas:         common.Gas{common.NewCoin(common.BTCAsset, cosmos.NewUint(10000))},
		Memo:        memo,
	}, 100, vault.PubKey, 100)
	c.Assert(tx.Valid(), IsNil)
	return tx
}

func (s *HandlerObservedTxHelpersSuite) TestObservationElectorate(c *C) {
	ctx, mgr := setupManagerForTest(c)
	nas, _ := setupChainElectorate(c, ctx, mgr)

	electorate := observationElectorate(ctx, mgr.Keeper(), common.BTCChain, nas)
	c.Assert(electorate, HasLen, 4)
	for _, na := range electorate {
		c.Check(nas[:4].Contains(na), Equals, true)
	}
	// No active vault serves ETH: nobody can reach consensus on it.
	c.Check(observationElectorate(ctx, mgr.Keeper(), common.ETHChain, nas), HasLen, 0)

	mgr.Keeper().SetMimir(ctx, constants.EnableNodeChainConsensus.String(), 0)
	c.Check(observationElectorate(ctx, mgr.Keeper(), common.BTCChain, nas), HasLen, 10)
}

// The quorum is reached among the chain's vault members, so the consensus tx
// must be resolved among them too, not against all active validators.
func (s *HandlerObservedTxHelpersSuite) TestChainAwareInboundQuorumResolvesConsensusTx(c *C) {
	ctx, mgr := setupManagerForTest(c)
	ctx = ctx.WithBlockHeight(100)
	nas, vault := setupChainElectorate(c, ctx, mgr)
	tx := chainElectorateObservedTx(c, vault, "=:THOR.RUNE:"+GetRandomTHORAddress().String())

	voter := NewObservedTxVoter(tx.Tx.ID, nil)
	var reached bool
	for i := 0; i < 3; i++ {
		voter, reached = processTxInAttestation(ctx, mgr, voter, nas, tx, nas[i].NodeAddress, true)
		c.Check(reached, Equals, i == 2, Commentf("signer %d", i))
	}
	c.Check(voter.FinalisedHeight, Equals, int64(100))
	c.Assert(voter.Tx.IsEmpty(), Equals, false)
	c.Check(voter.Tx.Tx.Coins, DeepEquals, tx.Tx.Coins)
	c.Check(voter.Tx.GetSigners(), HasLen, 3)

	// A member who observed gets the point back; only the silent member is
	// charged, and no validator outside the vault is.
	lack := mgr.GetConstants().GetInt64Value(constants.LackOfObservationPenalty)
	for i, na := range nas {
		points, err := mgr.Keeper().GetNodeAccountSlashPoints(ctx, na.NodeAddress)
		c.Assert(err, IsNil)
		want := int64(0)
		if i == 3 {
			want = lack
		}
		c.Check(points, Equals, want, Commentf("node %d", i))
	}
}

func (s *HandlerObservedTxHelpersSuite) TestChainAwareOutboundQuorumResolvesConsensusTx(c *C) {
	ctx, mgr := setupManagerForTest(c)
	ctx = ctx.WithBlockHeight(100)
	nas, vault := setupChainElectorate(c, ctx, mgr)
	tx := chainElectorateObservedTx(c, vault, "OUT:"+GetRandomTxHash().String())

	voter := NewObservedTxVoter(tx.Tx.ID, nil)
	var reached bool
	for i := 0; i < 3; i++ {
		voter, reached = processTxOutAttestation(ctx, mgr, voter, nas, tx, nas[i].NodeAddress, true)
		c.Check(reached, Equals, i == 2, Commentf("signer %d", i))
	}
	c.Check(voter.FinalisedHeight, Equals, int64(100))
	c.Assert(voter.Tx.IsEmpty(), Equals, false)
	c.Check(voter.Tx.Tx.Coins, DeepEquals, tx.Tx.Coins)
	// handleObservedTxOutQuorum resolves the same tx it will match against the
	// scheduled outbound.
	c.Check(voter.GetTx(observationElectorate(ctx, mgr.Keeper(), common.BTCChain, nas)).IsEmpty(), Equals, false)
}
