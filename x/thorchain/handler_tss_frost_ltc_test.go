package thorchain

import (
	"time"

	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

func (s *HandlerTssSuite) TestFrostLTCKeygenActive(c *C) {
	helper := newTssHandlerTestHelper(c)
	ctx, k := helper.ctx, helper.keeper
	supported := common.LTCChain.SupportsTaprootVaults()

	c.Check(frostLTCKeygenActive(ctx, k), Equals, false)
	k.SetMimir(ctx, constants.MimirKeyEnableFrostLTC, 1)
	c.Check(frostLTCKeygenActive(ctx, k), Equals, supported)
	// a halt at or below the current height deactivates it
	k.SetMimir(ctx, constants.MimirKeyHaltFrostLTCKeygen, ctx.BlockHeight())
	c.Check(frostLTCKeygenActive(ctx, k), Equals, false)
	// a halt scheduled for a future height does not
	k.SetMimir(ctx, constants.MimirKeyHaltFrostLTCKeygen, ctx.BlockHeight()+1)
	c.Check(frostLTCKeygenActive(ctx, k), Equals, supported)
	k.SetMimir(ctx, constants.MimirKeyHaltFrostLTCKeygen, 0)
	k.SetMimir(ctx, constants.MimirKeyEnableFrostLTC, 0)
	c.Check(frostLTCKeygenActive(ctx, k), Equals, false)
}

func (s *HandlerTssSuite) TestAsgardKeygenFrostPolicy(c *C) {
	verifySecp256K1Signature = func(_ common.PubKey, _ []byte) error { return nil }
	helper := newTssHandlerTestHelper(c)
	for _, member := range helper.members {
		setKeygenMemberNodeAccount(c, helper, member, NodeActive)
	}
	handler := NewTssHandler(NewDummyMgrWithKeeper(helper.keeper))
	ctx := helper.ctx
	supported := common.LTCChain.SupportsTaprootVaults()

	poolPubKey := GetRandomPubKey()
	eddsaPubKey := GetRandomPubKey()
	frostPubKey := types.GetTaprootTestPubKey()
	thorOnly := common.Chains{common.RuneAsset().Chain}
	withLTC := common.Chains{common.RuneAsset().Chain, common.LTCChain}
	newMsg := func(chains common.Chains, frost common.PubKey, blame []types.Blame) *MsgTssPool {
		msg, err := NewMsgTssPoolV2(helper.members.Strings(), poolPubKey, keygenCheckSig, nil, AsgardKeygen, ctx.BlockHeight(), blame, chains.Strings(), helper.signer, 1024, eddsaPubKey, nil, nil, frost)
		c.Assert(err, IsNil)
		return msg
	}
	validate := func(msg *MsgTssPool) error { return handler.validate(ctx, msg) }

	// Inactive (mimir unset): no asgard report may carry a FROST key, with or
	// without LTC in its chain set, and reports without one are unaffected.
	c.Assert(validate(newMsg(withLTC, common.EmptyPubKey, nil)), IsNil)
	c.Assert(validate(newMsg(withLTC, frostPubKey, nil)), NotNil)
	c.Assert(validate(newMsg(thorOnly, frostPubKey, nil)), NotNil)

	helper.keeper.SetMimir(ctx, constants.MimirKeyEnableFrostLTC, 1)
	if !supported {
		// mainnet and chainnet builds: the mimir has no effect
		c.Assert(validate(newMsg(withLTC, frostPubKey, nil)), NotNil)
		c.Assert(validate(newMsg(withLTC, common.EmptyPubKey, nil)), IsNil)
		return
	}

	// Active: a successful LTC keygen must carry the FROST key.
	c.Assert(validate(newMsg(withLTC, frostPubKey, nil)), IsNil)
	c.Assert(validate(newMsg(withLTC, common.EmptyPubKey, nil)), NotNil)
	// Keygens without LTC are unaffected and may not carry one.
	c.Assert(validate(newMsg(thorOnly, common.EmptyPubKey, nil)), IsNil)
	c.Assert(validate(newMsg(thorOnly, frostPubKey, nil)), NotNil)
	// The key must be a secp256k1 Taproot key distinct from the ECDSA pool key.
	c.Assert(validate(newMsg(withLTC, GetRandomEd25519PubKey(), nil)), NotNil)
	c.Assert(validate(newMsg(withLTC, poolPubKey, nil)), NotNil)
	// A failed keygen needs no FROST key and may not carry one.
	blame := []types.Blame{{FailReason: "frost dkg timeout", BlameNodes: []types.Node{{Pubkey: helper.members[1].String()}}}}
	c.Assert(validate(newMsg(withLTC, common.EmptyPubKey, blame)), IsNil)
	c.Assert(validate(newMsg(withLTC, frostPubKey, blame)), NotNil)
	// Once the active vaults serve LTC, a successful report may not drop LTC
	// to dodge the FROST requirement; a failed LTC DKG is reported with blame.
	ltcVault := NewVaultV2(ctx.BlockHeight(), ActiveVault, AsgardVault, GetRandomPubKey(), common.Chains{common.RuneAsset().Chain, common.LTCChain}.Strings(), []ChainContract{}, GetRandomPubKey())
	c.Assert(helper.keeper.SetVault(ctx, ltcVault), IsNil)
	c.Assert(validate(newMsg(thorOnly, common.EmptyPubKey, nil)), NotNil)
	c.Assert(validate(newMsg(thorOnly, common.EmptyPubKey, blame)), IsNil)
	c.Assert(validate(newMsg(withLTC, frostPubKey, nil)), IsNil)
	// A retiring vault no longer counts: the network is leaving LTC behind.
	ltcVault.Status = RetiringVault
	c.Assert(helper.keeper.SetVault(ctx, ltcVault), IsNil)
	c.Assert(validate(newMsg(thorOnly, common.EmptyPubKey, nil)), IsNil)
	// The halt mimir makes the policy inactive again.
	helper.keeper.SetMimir(ctx, constants.MimirKeyHaltFrostLTCKeygen, ctx.BlockHeight())
	c.Assert(validate(newMsg(withLTC, frostPubKey, nil)), NotNil)
	c.Assert(validate(newMsg(withLTC, common.EmptyPubKey, nil)), IsNil)
}

func (s *HandlerTssSuite) TestAsgardKeygenWithFrostKeyCreatesTaprootVault(c *C) {
	if !common.LTCChain.SupportsTaprootVaults() {
		c.Skip("taproot vaults are not enabled on this build")
	}
	helper := newTssHandlerTestHelper(c)
	handler := NewTssHandler(NewDummyMgrWithKeeper(helper.keeper))
	ctx := helper.ctx
	c.Assert(helper.keeper.Keeper.DeleteVault(ctx, helper.poolPk), IsNil)
	helper.keeper.SetMimir(ctx, constants.MimirKeyEnableFrostLTC, 1)
	members := helper.members[:4]

	keygenBlock := NewKeygenBlock(ctx.BlockHeight())
	keygenBlock.Keygens = []Keygen{{Type: AsgardKeygen, Members: members.Strings()}}
	helper.keeper.SetKeygenBlock(ctx, keygenBlock)

	poolPubKey := GetRandomPubKey()
	eddsaPubKey := GetRandomPubKey()
	frostPubKey := types.GetTaprootTestPubKey()
	chains := common.Chains{common.RuneAsset().Chain, common.LTCChain, common.BTCChain}
	msg, err := NewMsgTssPoolV2(members.Strings(), poolPubKey, nil, nil, AsgardKeygen, ctx.BlockHeight(), nil, chains.Strings(), helper.signer, 1024, eddsaPubKey, nil, nil, frostPubKey)
	c.Assert(err, IsNil)
	plain, err := NewMsgTssPoolV2(members.Strings(), poolPubKey, nil, nil, AsgardKeygen, ctx.BlockHeight(), nil, chains.Strings(), helper.signer, 1024, eddsaPubKey, nil, nil, common.EmptyPubKey)
	c.Assert(err, IsNil)
	c.Assert(msg.ID == plain.ID, Equals, false)

	setKeygenMemberNodeAccount(c, helper, members[0], NodeActive)
	setKeygenMemberNodeAccount(c, helper, members[1], NodeReady)
	setKeygenMemberNodeAccount(c, helper, members[2], NodeReady)
	setKeygenMemberNodeAccount(c, helper, members[3], NodeReady)

	voter := NewTssVoter(msg.ID, members.Strings(), poolPubKey, eddsaPubKey)
	voter.PoolPubKeyFrost = frostPubKey
	for _, member := range members[1:3] {
		addr, addrErr := member.GetThorAddress()
		c.Assert(addrErr, IsNil)
		voter.Signers = append(voter.Signers, addr.String())
		// each earlier reporter declared the same chain set, so the chains
		// reach the two-thirds majority ConsensusChains requires
		voter.Chains = append(voter.Chains, chains.Strings()...)
	}
	voter.Secp256K1Signatures = []string{"sig-a", "sig-a", "sig-a"}
	helper.keeper.SetTssVoter(ctx, voter)

	_, err = handler.Run(ctx, msg)
	c.Assert(err, IsNil)

	updatedVoter, err := helper.keeper.GetTssVoter(ctx, msg.ID)
	c.Assert(err, IsNil)
	c.Assert(updatedVoter.HasConsensus(), Equals, true)
	c.Assert(updatedVoter.PoolPubKeyFrost.Equals(frostPubKey), Equals, true)

	vault, err := helper.keeper.GetVault(ctx, poolPubKey)
	c.Assert(err, IsNil)
	c.Assert(vault.Status, Equals, InitVault)
	c.Assert(vault.PubKeyFrost.Equals(frostPubKey), Equals, true)
	c.Assert(vault.PubKeyEddsa.Equals(eddsaPubKey), Equals, true)
	c.Assert(vault.GetChains().Has(common.LTCChain), Equals, true)

	// LTC resolves to the Taproot address of the FROST key, BTC to the ECDSA key.
	ltcAddr, err := vault.GetAddress(common.LTCChain)
	c.Assert(err, IsNil)
	wantLTC, err := frostPubKey.GetAddressForAlgo(common.LTCChain, common.SigningAlgoSecp256k1Taproot)
	c.Assert(err, IsNil)
	c.Assert(ltcAddr.Equals(wantLTC), Equals, true)
	btcAddr, err := vault.GetAddress(common.BTCChain)
	c.Assert(err, IsNil)
	wantBTC, err := poolPubKey.GetAddress(common.BTCChain)
	c.Assert(err, IsNil)
	c.Assert(btcAddr.Equals(wantBTC), Equals, true)

	// The vault is reachable by its FROST key, which observations use.
	byFrost, err := helper.keeper.GetVault(ctx, frostPubKey)
	c.Assert(err, IsNil)
	c.Assert(byFrost.PubKey.Equals(poolPubKey), Equals, true)
}

func (s *HandlerTssSuite) TestFrostLTCKeygenValidityWindow(c *C) {
	helper := newTssHandlerTestHelper(c)
	ctx, k := helper.ctx, helper.keeper
	keygenBlock := NewKeygenBlock(ctx.BlockHeight())
	keygenBlock.Keygens = []Keygen{{Type: AsgardKeygen, Members: helper.members.Strings()}}

	base := keygenResultValidityBlocksForBlock(ctx, k, AsgardKeygen, keygenBlock)
	k.SetMimir(ctx, constants.MimirKeyEnableFrostLTC, 1)
	withFrost := keygenResultValidityBlocksForBlock(ctx, k, AsgardKeygen, keygenBlock)
	if !common.LTCChain.SupportsTaprootVaults() {
		c.Check(withFrost, Equals, base)
		return
	}
	floor := durationToBlocks(6*time.Minute) - 1
	c.Check(withFrost, Equals, max(base, floor))
	c.Check(withFrost >= floor, Equals, true)
	// the window never shrinks below the plain asgard window
	c.Check(withFrost >= base, Equals, true)
}
