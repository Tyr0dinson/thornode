package thorchain

import (
	"fmt"
	"slices"

	cometed25519 "github.com/cometbft/cometbft/crypto/ed25519"

	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

type ChainActivationTestSuite struct{}

var _ = Suite(&ChainActivationTestSuite{})

func (s *ChainActivationTestSuite) SetUpSuite(c *C) {
	SetupConfigForTest()
}

// the chain under test - deliberately not in the seeded vault chain sets
const activationChain = common.LTCChain

// the chain height the readiness proof is pinned to
const activationChainHeight = int64(1024)

func activationMimirKey(chain common.Chain) string {
	return fmt.Sprintf(constants.MimirTemplateEnableChain, chain)
}

// getRandomEddsaPubKey returns a real ed25519 key. GetRandomPubKey returns a
// secp256k1 key, which cannot derive an address for an ed25519 chain - using it as
// PubKeyEddsa makes an ed25519 test pass for the wrong reason.
func getRandomEddsaPubKey(c *C) common.PubKey {
	pubKey, err := common.NewPubKeyFromCrypto(cometed25519.GenPrivKey().PubKey())
	c.Assert(err, IsNil)
	_, err = pubKey.GetAddress(common.SOLChain)
	c.Assert(err, IsNil)
	return pubKey
}

// activationNodes builds n validator nodes without writing them. Vault membership and
// the fee-voter signer set are both drawn from these, so a node that signs the fee is
// the same identity the per-vault keysign check looks for: GetRandomValidatorNode
// derives NodeAddress from PubKeySet.Secp256k1, which is what vault Membership holds.
func activationNodes(n int) NodeAccounts {
	nodes := make(NodeAccounts, 0, n)
	for i := 0; i < n; i++ {
		nodes = append(nodes, GetRandomValidatorNode(NodeActive))
	}
	return nodes
}

func writeNodes(ctx cosmos.Context, c *C, k keeper.Keeper, nodes NodeAccounts) {
	for _, na := range nodes {
		c.Assert(k.SetNodeAccount(ctx, na), IsNil)
	}
}

// setupActivation seeds a fresh manager with n active validators.
func setupActivation(c *C, n int) (cosmos.Context, Manager, NodeAccounts) {
	ctx, mgr := setupManagerForTest(c)
	nodes := activationNodes(n)
	writeNodes(ctx, c, mgr.Keeper(), nodes)
	return ctx, mgr, nodes
}

func membershipOf(nodes NodeAccounts) []string {
	pks := make(common.PubKeys, 0, len(nodes))
	for _, na := range nodes {
		pks = append(pks, na.PubKeySet.Secp256k1)
	}
	return pks.Strings()
}

// newActivationVault builds an asgard vault with THOR+BTC, both key types present
// so address derivation succeeds for either signing algo. Its membership is the given
// nodes, so the fee quorum they produce also clears the vault's keysign threshold.
func newActivationVault(c *C, status types.VaultStatus, members NodeAccounts) Vault {
	vault := GetRandomVault()
	vault.Type = AsgardVault
	vault.Status = status
	vault.Chains = common.Chains{common.THORChain, common.BTCChain}.Strings()
	vault.Membership = membershipOf(members)
	vault.PubKeyEddsa = getRandomEddsaPubKey(c)
	return vault
}

// setReadinessProof writes the fee, the observed height, and the voter that produced
// them - the same three records processNetworkFeeAttestation writes on fee consensus.
// consensusHeight is the THORChain height the voter reached consensus at; feeHeight is
// the chain height the fee was reported for, which is also part of the voter key.
// Returns the voter's ID, for a caller that also maintains the latest-voter pointer.
func setReadinessProof(ctx cosmos.Context, c *C, k keeper.Keeper, chain common.Chain, signers NodeAccounts, consensusHeight, feeHeight int64) string {
	c.Assert(k.SaveNetworkFee(ctx, chain, types.NetworkFee{
		Chain:              chain,
		TransactionSize:    1000,
		TransactionFeeRate: 10,
	}), IsNil)
	c.Assert(k.SetLastChainHeight(ctx, chain, feeHeight), IsNil)

	voter := types.NewObservedNetworkFeeVoter(feeHeight, chain)
	voter.FeeRate = 10
	voter.TransactionSize = 1000
	voter.BlockHeight = consensusHeight
	for _, na := range signers {
		voter.Sign(na.NodeAddress)
	}
	k.SetObservedNetworkFeeVoter(ctx, voter)
	return voter.ID()
}

// satisfyActivationGates sets everything except the enable mimir itself.
func satisfyActivationGates(ctx cosmos.Context, c *C, k keeper.Keeper, chain common.Chain, signers NodeAccounts) {
	setReadinessProof(ctx, c, k, chain, signers, ctx.BlockHeight(), activationChainHeight)
	if chain.IsEVM() {
		// EVM chains additionally need a router before they may be advertised. Only
		// filled in when absent, so a caller that pinned its own contract keeps it.
		if cc, err := k.GetChainContract(ctx, chain); err == nil && cc.IsEmpty() {
			k.SetChainContract(ctx, types.ChainContract{Chain: chain, Router: GetRandomETHAddress()})
		}
	}
}

func runActivation(ctx cosmos.Context, c *C, mgr Manager) {
	networkMgr := newNetworkMgr(mgr.Keeper(), mgr.TxOutStore(), mgr.EventMgr())
	c.Assert(networkMgr.activateEnabledChains(ctx, mgr), IsNil)
}

func vaultChains(ctx cosmos.Context, c *C, k keeper.Keeper, pk common.PubKey) []string {
	vault, err := k.GetVault(ctx, pk)
	c.Assert(err, IsNil)
	return vault.Chains
}

// --- gates: each must independently prevent stamping ---

func (s *ChainActivationTestSuite) TestNoStampWhenMimirUnset(c *C) {
	ctx, mgr, nodes := setupActivation(c, 4)
	vault := newActivationVault(c, ActiveVault, nodes)
	c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)
	satisfyActivationGates(ctx, c, mgr.Keeper(), activationChain, nodes)

	runActivation(ctx, c, mgr)

	c.Assert(slices.Contains(vaultChains(ctx, c, mgr.Keeper(), vault.PubKey), activationChain.String()), Equals, false)
}

func (s *ChainActivationTestSuite) TestNoStampWhenMimirInFuture(c *C) {
	ctx, mgr, nodes := setupActivation(c, 4)
	vault := newActivationVault(c, ActiveVault, nodes)
	c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)
	satisfyActivationGates(ctx, c, mgr.Keeper(), activationChain, nodes)
	mgr.Keeper().SetMimir(ctx, activationMimirKey(activationChain), ctx.BlockHeight()+100)

	runActivation(ctx, c, mgr)

	c.Assert(slices.Contains(vaultChains(ctx, c, mgr.Keeper(), vault.PubKey), activationChain.String()), Equals, false)
}

// GetNetworkFee returns a zero-valued fee with a NIL error for an absent chain, so
// the gate must test Valid() rather than the error. This asserts that shape.
func (s *ChainActivationTestSuite) TestNoStampWhenNetworkFeeAbsent(c *C) {
	ctx, mgr, nodes := setupActivation(c, 4)
	vault := newActivationVault(c, ActiveVault, nodes)
	c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)
	c.Assert(mgr.Keeper().SetLastChainHeight(ctx, activationChain, activationChainHeight), IsNil)
	mgr.Keeper().SetMimir(ctx, activationMimirKey(activationChain), 1)

	nf, err := mgr.Keeper().GetNetworkFee(ctx, activationChain)
	c.Assert(err, IsNil)         // absent fee does NOT error
	c.Assert(nf.Valid(), NotNil) // it is simply invalid
	c.Assert(nf.TransactionSize, Equals, uint64(0))

	runActivation(ctx, c, mgr)

	c.Assert(slices.Contains(vaultChains(ctx, c, mgr.Keeper(), vault.PubKey), activationChain.String()), Equals, false)
}

func (s *ChainActivationTestSuite) TestNoStampWhenNoObservedChainHeight(c *C) {
	ctx, mgr, nodes := setupActivation(c, 4)
	vault := newActivationVault(c, ActiveVault, nodes)
	c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)
	c.Assert(mgr.Keeper().SaveNetworkFee(ctx, activationChain, types.NetworkFee{
		Chain: activationChain, TransactionSize: 1000, TransactionFeeRate: 10,
	}), IsNil)
	mgr.Keeper().SetMimir(ctx, activationMimirKey(activationChain), 1)

	runActivation(ctx, c, mgr)

	c.Assert(slices.Contains(vaultChains(ctx, c, mgr.Keeper(), vault.PubKey), activationChain.String()), Equals, false)
}

// The fee record carries no signers and no height, so a fee with no voter behind it
// is not a readiness proof. Fails closed on the zero-signer record a miss returns.
func (s *ChainActivationTestSuite) TestNoStampWhenNoFeeVoter(c *C) {
	ctx, mgr, nodes := setupActivation(c, 4)
	vault := newActivationVault(c, ActiveVault, nodes)
	c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)
	// fee and height, but deliberately no voter
	c.Assert(mgr.Keeper().SaveNetworkFee(ctx, activationChain, types.NetworkFee{
		Chain: activationChain, TransactionSize: 1000, TransactionFeeRate: 10,
	}), IsNil)
	c.Assert(mgr.Keeper().SetLastChainHeight(ctx, activationChain, activationChainHeight), IsNil)
	mgr.Keeper().SetMimir(ctx, activationMimirKey(activationChain), 1)

	runActivation(ctx, c, mgr)

	c.Assert(slices.Contains(vaultChains(ctx, c, mgr.Keeper(), vault.PubKey), activationChain.String()), Equals, false)
}

func (s *ChainActivationTestSuite) TestNoStampWhenChainHalted(c *C) {
	ctx, mgr, nodes := setupActivation(c, 4)
	vault := newActivationVault(c, ActiveVault, nodes)
	c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)
	satisfyActivationGates(ctx, c, mgr.Keeper(), activationChain, nodes)
	mgr.Keeper().SetMimir(ctx, activationMimirKey(activationChain), 1)
	mgr.Keeper().SetMimir(ctx, fmt.Sprintf("Halt%sChain", activationChain), 1)

	runActivation(ctx, c, mgr)

	c.Assert(slices.Contains(vaultChains(ctx, c, mgr.Keeper(), vault.PubKey), activationChain.String()), Equals, false)
}

// A churn in flight must defer the stamp - tryRotateInitVaults promotes init vaults
// as a set and refuses on a chain-set mismatch.
func (s *ChainActivationTestSuite) TestNoStampWhileInitVaultPresent(c *C) {
	ctx, mgr, nodes := setupActivation(c, 4)
	active := newActivationVault(c, ActiveVault, nodes)
	initVault := newActivationVault(c, InitVault, nodes)
	c.Assert(mgr.Keeper().SetVault(ctx, active), IsNil)
	c.Assert(mgr.Keeper().SetVault(ctx, initVault), IsNil)
	satisfyActivationGates(ctx, c, mgr.Keeper(), activationChain, nodes)
	mgr.Keeper().SetMimir(ctx, activationMimirKey(activationChain), 1)

	runActivation(ctx, c, mgr)

	c.Assert(slices.Contains(vaultChains(ctx, c, mgr.Keeper(), active.PubKey), activationChain.String()), Equals, false)
}

func (s *ChainActivationTestSuite) TestActivationWaitsForPendingRegularKeygen(c *C) {
	for _, tc := range []struct {
		name       string
		keygenType types.KeygenType
		coSchedule bool
		resolved   bool
	}{
		{name: "regular", keygenType: AsgardKeygen},
		{name: "co-scheduled", keygenType: AsgardKeygen, coSchedule: true},
		{name: "resolved", keygenType: AsgardKeygen, resolved: true},
		{name: "XMR only", keygenType: XMRMonovaultKeygen},
	} {
		ctx, mgr, nodes := setupActivation(c, 4)
		ctx = ctx.WithBlockHeight(100)
		k := mgr.Keeper()
		k.SetMimir(ctx, constants.ChurnRetryInterval.String(), 2)
		active := newActivationVault(c, ActiveVault, nodes)
		c.Assert(k.SetVault(ctx, active), IsNil)
		satisfyActivationGates(ctx, c, k, activationChain, nodes)
		k.SetMimir(ctx, activationMimirKey(activationChain), 1)

		members := membershipOf(activationNodes(4))
		if tc.resolved {
			members = active.Membership
		}
		keygen, err := NewKeygen(ctx.BlockHeight(), members, tc.keygenType)
		c.Assert(err, IsNil)
		block := NewKeygenBlock(ctx.BlockHeight())
		block.Keygens = []Keygen{keygen}
		if tc.coSchedule {
			var xmr Keygen
			xmr, err = NewKeygen(ctx.BlockHeight(), members, XMRMonovaultKeygen)
			c.Assert(err, IsNil)
			block.Keygens = append(block.Keygens, xmr)
		}
		k.SetKeygenBlock(ctx, block)
		initVaults, err := k.GetAsgardVaultsByStatus(ctx, InitVault)
		c.Assert(err, IsNil)
		c.Assert(initVaults, HasLen, 0)

		validity := keygenResultValidityBlocksForBlock(ctx, k, tc.keygenType, block)
		for _, offset := range []int64{0, 1, validity, validity + 1} {
			at := ctx.WithBlockHeight(block.Height + offset)
			runActivation(at, c, mgr)
			wantStamp := tc.resolved || tc.keygenType == XMRMonovaultKeygen || offset > validity
			c.Assert(slices.Contains(vaultChains(at, c, k, active.PubKey), activationChain.String()), Equals, wantStamp,
				Commentf("%s at offset %d", tc.name, offset))
		}
	}
}

// --- readiness proof: binding to the current validator set ---

// Bifrost posts MsgNetworkFee only when the rate *changes*, so on a stable-fee chain
// (XRP, Cosmos, TRON - the chains this feature targets) the single voter is written
// once and never refreshed. Any bar measured on voter age would be unsatisfiable there,
// so an old voter must still activate. What keeps the proof honest is the active-set
// filter, not the clock.
func (s *ChainActivationTestSuite) TestActivatesOnLongStandingVoter(c *C) {
	ctx, mgr, nodes := setupActivation(c, 4)
	ctx = ctx.WithBlockHeight(2_000_000)
	writeNodes(ctx, c, mgr.Keeper(), nodes)

	vault := newActivationVault(c, ActiveVault, nodes)
	c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)
	// consensus reached a million blocks ago and never revisited since
	setReadinessProof(ctx, c, mgr.Keeper(), activationChain, nodes, 1, activationChainHeight)
	mgr.Keeper().SetMimir(ctx, activationMimirKey(activationChain), 1)

	runActivation(ctx, c, mgr)

	c.Assert(slices.Contains(vaultChains(ctx, c, mgr.Keeper(), vault.PubKey), activationChain.String()), Equals, true)
}

// The voter cannot be found by keying off GetLastChainHeight: fee consensus is only one
// of that value's three writers, and every observed inbound advances it too. Anyone can
// force the drift by sending dust to the derived pre-activation address, so activation
// must not depend on the two agreeing.
func (s *ChainActivationTestSuite) TestActivatesAfterObservedInboundAdvancedChainHeight(c *C) {
	ctx, mgr, nodes := setupActivation(c, 4)
	vault := newActivationVault(c, ActiveVault, nodes)
	c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)

	// fee consensus at chain height 1024 ...
	satisfyActivationGates(ctx, c, mgr.Keeper(), activationChain, nodes)
	// ... then an observed inbound pushes the last chain height well past it, exactly as
	// handler_observed_tx_helpers.go does on every inbound reaching consensus
	c.Assert(mgr.Keeper().SetLastChainHeight(ctx, activationChain, activationChainHeight+5000), IsNil)
	mgr.Keeper().SetMimir(ctx, activationMimirKey(activationChain), 1)

	runActivation(ctx, c, mgr)

	c.Assert(slices.Contains(vaultChains(ctx, c, mgr.Keeper(), vault.PubKey), activationChain.String()), Equals, true)
}

// A member who signed the fee while active and has since gone standby is no longer
// running the chain client, but vault Membership does not change until the vault
// retires. Counting them would let a vault clear its own keysign threshold on paper
// while short of live signers - the stranding case the per-vault gate exists to stop.
func (s *ChainActivationTestSuite) TestSignerGoneStandbyNoLongerCounts(c *C) {
	ctx, mgr, nodes := setupActivation(c, 4)
	vault := newActivationVault(c, ActiveVault, nodes)
	c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)
	satisfyActivationGates(ctx, c, mgr.Keeper(), activationChain, nodes)
	mgr.Keeper().SetMimir(ctx, activationMimirKey(activationChain), 1)

	// two of the four signers leave the active set; the vault keeps all four members
	for _, na := range nodes[:2] {
		na.Status = NodeStandby
		c.Assert(mgr.Keeper().SetNodeAccount(ctx, na), IsNil)
	}

	runActivation(ctx, c, mgr)

	// 2 of the vault's 4 members are still capable, below the ceil(2*4/3)=3 needed
	c.Assert(slices.Contains(vaultChains(ctx, c, mgr.Keeper(), vault.PubKey), activationChain.String()), Equals, false)

	// bring one back and the same vault activates, so it is the active-set filter that
	// rejected it rather than anything else
	na := nodes[0]
	na.Status = NodeActive
	c.Assert(mgr.Keeper().SetNodeAccount(ctx, na), IsNil)
	runActivation(ctx, c, mgr)
	c.Assert(slices.Contains(vaultChains(ctx, c, mgr.Keeper(), vault.PubKey), activationChain.String()), Equals, true)
}

// An EVM chain advertised without a router is a dead deposit path: bifrost will not
// recognise router deposits, yet /inbound_addresses presents the chain as live.
func (s *ChainActivationTestSuite) TestNoStampWhenEVMChainHasNoRouter(c *C) {
	ctx, mgr, nodes := setupActivation(c, 4)
	evmChain := common.AVAXChain
	vault := newActivationVault(c, ActiveVault, nodes)
	c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)

	// readiness proof only - deliberately no ChainContract
	setReadinessProof(ctx, c, mgr.Keeper(), evmChain, nodes, ctx.BlockHeight(), activationChainHeight)
	mgr.Keeper().SetMimir(ctx, activationMimirKey(evmChain), 1)

	runActivation(ctx, c, mgr)

	c.Assert(slices.Contains(vaultChains(ctx, c, mgr.Keeper(), vault.PubKey), evmChain.String()), Equals, false)

	// once the router is stored it activates, so it is the contract gate doing the work
	mgr.Keeper().SetChainContract(ctx, types.ChainContract{Chain: evmChain, Router: GetRandomETHAddress()})
	runActivation(ctx, c, mgr)
	c.Assert(slices.Contains(vaultChains(ctx, c, mgr.Keeper(), vault.PubKey), evmChain.String()), Equals, true)
}

// A proof approved solely by nodes that have since churned out must not activate a
// chain for the validator set that replaced them.
func (s *ChainActivationTestSuite) TestNoStampWhenSignersChurnedOut(c *C) {
	ctx, mgr, current := setupActivation(c, 4)

	// the departed set: signed the fee, no longer active
	departed := activationNodes(4)
	for _, na := range departed {
		na.Status = NodeStandby
		c.Assert(mgr.Keeper().SetNodeAccount(ctx, na), IsNil)
	}

	// the vault belongs to the CURRENT validators, so this isolates the
	// current-supermajority gate from the per-vault keysign gate
	vault := newActivationVault(c, ActiveVault, current)
	c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)
	satisfyActivationGates(ctx, c, mgr.Keeper(), activationChain, departed)
	mgr.Keeper().SetMimir(ctx, activationMimirKey(activationChain), 1)

	runActivation(ctx, c, mgr)

	c.Assert(slices.Contains(vaultChains(ctx, c, mgr.Keeper(), vault.PubKey), activationChain.String()), Equals, false)

	// re-proved by the current set, it activates
	satisfyActivationGates(ctx, c, mgr.Keeper(), activationChain, current)
	runActivation(ctx, c, mgr)
	c.Assert(slices.Contains(vaultChains(ctx, c, mgr.Keeper(), vault.PubKey), activationChain.String()), Equals, true)
}

// Multi-Asgard keysign safety: a global two-thirds quorum can sit entirely outside one
// vault. TSS keysign needs a supermajority of THAT vault's membership, so a vault below
// it must not be given an inbound address it cannot sign outbounds for.
func (s *ChainActivationTestSuite) TestNoStampWhenOneVaultBelowKeysignThreshold(c *C) {
	ctx, mgr, nodes := setupActivation(c, 6)

	// two disjoint Asgards, three members each
	vaultA := newActivationVault(c, ActiveVault, nodes[0:3])
	vaultB := newActivationVault(c, ActiveVault, nodes[3:6])
	c.Assert(mgr.Keeper().SetVault(ctx, vaultA), IsNil)
	c.Assert(mgr.Keeper().SetVault(ctx, vaultB), IsNil)

	// 4 of 6 signed: a global supermajority, but concentrated in vaultA. vaultB has
	// only 1 of its 3 members, below the ceil(2*3/3)=2 it needs to sign.
	signers := NodeAccounts{nodes[0], nodes[1], nodes[2], nodes[3]}
	satisfyActivationGates(ctx, c, mgr.Keeper(), activationChain, signers)
	mgr.Keeper().SetMimir(ctx, activationMimirKey(activationChain), 1)

	// the global gate genuinely passes - it is the per-vault gate that must reject
	voter, err := mgr.Keeper().GetObservedNetworkFeeVoter(ctx, activationChainHeight, activationChain, 10, 1000)
	c.Assert(err, IsNil)
	activeNodes, err := mgr.Keeper().ListActiveValidators(ctx)
	c.Assert(err, IsNil)
	c.Assert(voter.HasConsensus(activeNodes), Equals, true)

	runActivation(ctx, c, mgr)

	// all-or-nothing: neither vault is stamped, so no unsignable address is published
	c.Assert(slices.Contains(vaultChains(ctx, c, mgr.Keeper(), vaultA.PubKey), activationChain.String()), Equals, false)
	c.Assert(slices.Contains(vaultChains(ctx, c, mgr.Keeper(), vaultB.PubKey), activationChain.String()), Equals, false)

	// one more vaultB member proving the chain takes it to 2 of 3, and both stamp
	satisfyActivationGates(ctx, c, mgr.Keeper(), activationChain, append(signers, nodes[4]))
	runActivation(ctx, c, mgr)
	c.Assert(slices.Contains(vaultChains(ctx, c, mgr.Keeper(), vaultA.PubKey), activationChain.String()), Equals, true)
	c.Assert(slices.Contains(vaultChains(ctx, c, mgr.Keeper(), vaultB.PubKey), activationChain.String()), Equals, true)
}

// The per-vault bar counts only members bifrost will actually sign with. A 4-member
// vault needs 3 signers; nodes[0..2] proved the chain, but nodes[0] is barred from the
// keysign party, leaving 2 capable signers - so the vault cannot sign and must not stamp.
func (s *ChainActivationTestSuite) TestNoStampWhenAttesterForcedToLeave(c *C) {
	ctx, mgr, nodes := setupActivation(c, 4)
	k := mgr.Keeper()
	vault := newActivationVault(c, ActiveVault, nodes)
	c.Assert(k.SetVault(ctx, vault), IsNil)
	satisfyActivationGates(ctx, c, k, activationChain, nodes[0:3])
	k.SetMimir(ctx, activationMimirKey(activationChain), 1)

	// still active, so it keeps counting toward the network-wide G6b quorum
	nodes[0].ForcedToLeave = true
	c.Assert(k.SetNodeAccount(ctx, nodes[0]), IsNil)

	runActivation(ctx, c, mgr)
	c.Assert(slices.Contains(vaultChains(ctx, c, k, vault.PubKey), activationChain.String()), Equals, false)

	// the emergency switch signs with the whole membership, so the same proof suffices
	k.SetMimir(ctx, constants.MimirKeyDisableTSSSignerFiltering, 1)
	runActivation(ctx, c, mgr)
	c.Assert(slices.Contains(vaultChains(ctx, c, k, vault.PubKey), activationChain.String()), Equals, true)
}

// A jailed member is dropped from the party whenever enough clean members remain, so
// its attestation cannot help the vault sign either.
func (s *ChainActivationTestSuite) TestNoStampWhenAttesterJailedOutOfParty(c *C) {
	ctx, mgr, nodes := setupActivation(c, 4)
	k := mgr.Keeper()
	vault := newActivationVault(c, ActiveVault, nodes)
	c.Assert(k.SetVault(ctx, vault), IsNil)
	satisfyActivationGates(ctx, c, k, activationChain, nodes[0:3])
	k.SetMimir(ctx, activationMimirKey(activationChain), 1)

	// nodes[1..3] are clean and exactly fill the 3-signer party, excluding nodes[0]
	c.Assert(k.SetNodeAccountJail(ctx, nodes[0].NodeAddress, ctx.BlockHeight()+100, "test"), IsNil)

	runActivation(ctx, c, mgr)
	c.Assert(slices.Contains(vaultChains(ctx, c, k, vault.PubKey), activationChain.String()), Equals, false)

	// once the party members themselves prove the chain, it stamps
	satisfyActivationGates(ctx, c, k, activationChain, nodes)
	runActivation(ctx, c, mgr)
	c.Assert(slices.Contains(vaultChains(ctx, c, k, vault.PubKey), activationChain.String()), Equals, true)
}

// The attester set comes from the pointed-to voter alone, so its cost is bounded
// rather than a scan of the chain's lifetime fee history on every block. Without a
// pointer, one scan re-derives it from the latest consensus voter and stores it.
func (s *ChainActivationTestSuite) TestAttestersReadFromLatestVoterPointer(c *C) {
	ctx, mgr, nodes := setupActivation(c, 4)
	k := mgr.Keeper()

	setReadinessProof(ctx, c, k, activationChain, nodes[0:2], 10, activationChainHeight)
	// an older consensus whose key sorts after the latest one, which the backfill scan
	// must not pick
	older := types.NewObservedNetworkFeeVoter(activationChainHeight+5, activationChain)
	older.BlockHeight = 5
	older.Sign(nodes[3].NodeAddress)
	k.SetObservedNetworkFeeVoter(ctx, older)

	attesters, provedAt := chainAttesters(ctx, k, activationChain, nodes)
	c.Assert(attesters, DeepEquals, map[string]bool{
		nodes[0].NodeAddress.String(): true,
		nodes[1].NodeAddress.String(): true,
	})
	c.Assert(provedAt, Equals, int64(10))
	latest, found, err := k.GetLatestObservedNetworkFeeVoter(ctx, activationChain)
	c.Assert(err, IsNil)
	c.Assert(found, Equals, true)
	c.Assert(latest.ReportBlockHeight, Equals, activationChainHeight)

	// a later voter written behind the pointer's back is not scanned: the history is
	// no longer read once the pointer exists
	setReadinessProof(ctx, c, k, activationChain, nodes[2:3], 20, activationChainHeight+10)
	again, _ := chainAttesters(ctx, k, activationChain, nodes)
	c.Assert(again, DeepEquals, attesters)
}

// Fee consensus keeps the pointer current while activation is requested - including
// signers that arrive after consensus - and clears it otherwise, so a network that
// never sets the mimir stores nothing new.
func (s *ChainActivationTestSuite) TestFeeConsensusMaintainsLatestVoterPointer(c *C) {
	ctx, mgr, nodes := setupActivation(c, 4)
	k := mgr.Keeper()
	handler := NewNetworkFeeHandler(mgr)
	broadcast := func(feeHeight int64, signers NodeAccounts) {
		for _, na := range signers {
			_, err := handler.handle(ctx, *NewMsgNetworkFee(feeHeight, activationChain, 1000, 10, na.NodeAddress))
			c.Assert(err, IsNil)
		}
	}

	// mimir unset: consensus stores no pointer
	broadcast(activationChainHeight, nodes)
	_, found, err := k.GetLatestObservedNetworkFeeVoter(ctx, activationChain)
	c.Assert(err, IsNil)
	c.Assert(found, Equals, false)

	// mimir set: consensus at 3 of 4 records the voter, and the late 4th signer shows
	k.SetMimir(ctx, activationMimirKey(activationChain), 1)
	broadcast(activationChainHeight+1, nodes)
	latest, found, err := k.GetLatestObservedNetworkFeeVoter(ctx, activationChain)
	c.Assert(err, IsNil)
	c.Assert(found, Equals, true)
	c.Assert(latest.ReportBlockHeight, Equals, activationChainHeight+1)
	c.Assert(latest.GetSigners(), HasLen, 4)

	// mimir unset again: the next consensus clears it, so it cannot go stale
	k.SetMimir(ctx, activationMimirKey(activationChain), 0)
	broadcast(activationChainHeight+2, nodes)
	_, found, err = k.GetLatestObservedNetworkFeeVoter(ctx, activationChain)
	c.Assert(err, IsNil)
	c.Assert(found, Equals, false)
}

// --- the happy path ---

func (s *ChainActivationTestSuite) TestStampsActiveAndRetiringVaults(c *C) {
	ctx, mgr, nodes := setupActivation(c, 4)
	activeA := newActivationVault(c, ActiveVault, nodes)
	activeB := newActivationVault(c, ActiveVault, nodes)
	retiring := newActivationVault(c, RetiringVault, nodes)
	for _, v := range []Vault{activeA, activeB, retiring} {
		c.Assert(mgr.Keeper().SetVault(ctx, v), IsNil)
	}
	satisfyActivationGates(ctx, c, mgr.Keeper(), activationChain, nodes)
	mgr.Keeper().SetMimir(ctx, activationMimirKey(activationChain), 1)

	runActivation(ctx, c, mgr)

	for _, pk := range []common.PubKey{activeA.PubKey, activeB.PubKey, retiring.PubKey} {
		chains := vaultChains(ctx, c, mgr.Keeper(), pk)
		c.Assert(slices.Contains(chains, activationChain.String()), Equals, true)
		// exactly once
		count := 0
		for _, ch := range chains {
			if ch == activationChain.String() {
				count++
			}
		}
		c.Assert(count, Equals, 1)
	}
}

// vault.Chains must end up in the same canonical form ConsensusChains produces, so
// there is one representation regardless of which producer wrote it.
func (s *ChainActivationTestSuite) TestStampedChainsMatchConsensusChainsOrdering(c *C) {
	ctx, mgr, nodes := setupActivation(c, 4)
	vault := newActivationVault(c, ActiveVault, nodes)
	c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)
	satisfyActivationGates(ctx, c, mgr.Keeper(), activationChain, nodes)
	mgr.Keeper().SetMimir(ctx, activationMimirKey(activationChain), 1)

	runActivation(ctx, c, mgr)

	expected := common.Chains{common.THORChain, common.BTCChain, activationChain}
	voter := TssVoter{PubKeys: []string{"a", "b", "c"}}
	for _, pk := range voter.PubKeys {
		_ = pk
		voter.Chains = append(voter.Chains, expected.Strings()...)
	}
	c.Assert(vaultChains(ctx, c, mgr.Keeper(), vault.PubKey), DeepEquals, voter.ConsensusChains().Strings())
}

// A vault that already picked the chain up via AddFunds holds it in append order.
// Converging must normalise that vault too, not skip it.
func (s *ChainActivationTestSuite) TestConvergesVaultThatAlreadyHasChainUnsorted(c *C) {
	ctx, mgr, nodes := setupActivation(c, 4)
	already := newActivationVault(c, ActiveVault, nodes)
	// AddFunds appends, so the chain lands last regardless of sort order
	already.Chains = append(already.Chains, activationChain.String())
	sibling := newActivationVault(c, ActiveVault, nodes)
	c.Assert(mgr.Keeper().SetVault(ctx, already), IsNil)
	c.Assert(mgr.Keeper().SetVault(ctx, sibling), IsNil)
	satisfyActivationGates(ctx, c, mgr.Keeper(), activationChain, nodes)
	mgr.Keeper().SetMimir(ctx, activationMimirKey(activationChain), 1)

	runActivation(ctx, c, mgr)

	// both vaults end with the identical normalised set
	c.Assert(vaultChains(ctx, c, mgr.Keeper(), already.PubKey), DeepEquals,
		vaultChains(ctx, c, mgr.Keeper(), sibling.PubKey))
}

// Two chains enabled in the same block must both land. Stamping them one at a time
// reads a stale vault copy on the second chain and writes back a set missing the first.
func (s *ChainActivationTestSuite) TestTwoChainsActivatedInSameBlock(c *C) {
	ctx, mgr, nodes := setupActivation(c, 4)
	vault := newActivationVault(c, ActiveVault, nodes)
	c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)

	for _, chain := range []common.Chain{common.LTCChain, common.DOGEChain} {
		satisfyActivationGates(ctx, c, mgr.Keeper(), chain, nodes)
		mgr.Keeper().SetMimir(ctx, activationMimirKey(chain), 1)
	}

	runActivation(ctx, c, mgr)

	chains := vaultChains(ctx, c, mgr.Keeper(), vault.PubKey)
	c.Assert(slices.Contains(chains, common.LTCChain.String()), Equals, true)
	c.Assert(slices.Contains(chains, common.DOGEChain.String()), Equals, true)
}

func (s *ChainActivationTestSuite) TestSecondRunIsNoOp(c *C) {
	ctx, mgr, nodes := setupActivation(c, 4)
	vault := newActivationVault(c, ActiveVault, nodes)
	c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)
	satisfyActivationGates(ctx, c, mgr.Keeper(), activationChain, nodes)
	mgr.Keeper().SetMimir(ctx, activationMimirKey(activationChain), 1)

	runActivation(ctx, c, mgr)
	first := vaultChains(ctx, c, mgr.Keeper(), vault.PubKey)
	runActivation(ctx, c, mgr)

	c.Assert(vaultChains(ctx, c, mgr.Keeper(), vault.PubKey), DeepEquals, first)
}

// --- safety properties ---

// If any eligible active vault cannot derive an address, none are stamped. A
// partially stamped set would publish an inbound address for a vault that cannot sign.
func (s *ChainActivationTestSuite) TestAllOrNothingAcrossActiveVaults(c *C) {
	ctx, mgr, nodes := setupActivation(c, 4)
	ed25519Chain := common.SOLChain

	good := newActivationVault(c, ActiveVault, nodes)
	bad := newActivationVault(c, ActiveVault, nodes)
	bad.PubKeyEddsa = common.EmptyPubKey // cannot address an ed25519 chain
	c.Assert(mgr.Keeper().SetVault(ctx, good), IsNil)
	c.Assert(mgr.Keeper().SetVault(ctx, bad), IsNil)
	satisfyActivationGates(ctx, c, mgr.Keeper(), ed25519Chain, nodes)
	mgr.Keeper().SetMimir(ctx, activationMimirKey(ed25519Chain), 1)

	runActivation(ctx, c, mgr)

	c.Assert(slices.Contains(vaultChains(ctx, c, mgr.Keeper(), good.PubKey), ed25519Chain.String()), Equals, false)
	c.Assert(slices.Contains(vaultChains(ctx, c, mgr.Keeper(), bad.PubKey), ed25519Chain.String()), Equals, false)
}

// The XMR monovault is pinned to {THOR, XMR}; a third chain would break the keygen
// family discrimination in keygenMatchesVault.
func (s *ChainActivationTestSuite) TestMonovaultUntouched(c *C) {
	ctx, mgr, nodes := setupActivation(c, 4)
	active := newActivationVault(c, ActiveVault, nodes)
	mono := GetRandomVault()
	mono.Type = AsgardVault
	mono.Status = ActiveVault
	mono.Chains = xmrMonovaultChains().Strings()
	mono.Membership = membershipOf(nodes)
	mono.PubKeyFrost = GetRandomPubKey()
	c.Assert(mgr.Keeper().SetVault(ctx, active), IsNil)
	c.Assert(mgr.Keeper().SetVault(ctx, mono), IsNil)
	satisfyActivationGates(ctx, c, mgr.Keeper(), activationChain, nodes)
	mgr.Keeper().SetMimir(ctx, activationMimirKey(activationChain), 1)

	runActivation(ctx, c, mgr)

	c.Assert(vaultChains(ctx, c, mgr.Keeper(), mono.PubKey), DeepEquals, xmrMonovaultChains().Strings())
	// the ordinary vault is still stamped
	c.Assert(slices.Contains(vaultChains(ctx, c, mgr.Keeper(), active.PubKey), activationChain.String()), Equals, true)
}

// A retiring vault that cannot address the chain must be skipped, not block the
// activation - otherwise a stale retiring vault holds the chain back forever.
func (s *ChainActivationTestSuite) TestRetiringVaultIsBestEffort(c *C) {
	ctx, mgr, nodes := setupActivation(c, 4)
	ed25519Chain := common.SOLChain

	active := newActivationVault(c, ActiveVault, nodes)
	retiring := newActivationVault(c, RetiringVault, nodes)
	retiring.PubKeyEddsa = common.EmptyPubKey
	c.Assert(mgr.Keeper().SetVault(ctx, active), IsNil)
	c.Assert(mgr.Keeper().SetVault(ctx, retiring), IsNil)
	satisfyActivationGates(ctx, c, mgr.Keeper(), ed25519Chain, nodes)
	mgr.Keeper().SetMimir(ctx, activationMimirKey(ed25519Chain), 1)

	runActivation(ctx, c, mgr)

	c.Assert(slices.Contains(vaultChains(ctx, c, mgr.Keeper(), active.PubKey), ed25519Chain.String()), Equals, true)
	c.Assert(slices.Contains(vaultChains(ctx, c, mgr.Keeper(), retiring.PubKey), ed25519Chain.String()), Equals, false)
}

// A retiring vault still signs migrations and outbounds, so the keysign bar applies to
// it too - but as a skip, not a veto. It must be left unstamped while the active vaults
// activate anyway, otherwise a stale retiring vault holds the chain back forever.
func (s *ChainActivationTestSuite) TestRetiringVaultBelowThresholdSkippedNotBlocking(c *C) {
	ctx, mgr, nodes := setupActivation(c, 4)
	active := newActivationVault(c, ActiveVault, nodes)
	// membership drawn from churned-out nodes that never proved the chain - standby,
	// so they do not dilute the active set the global quorum is measured against
	strangers := activationNodes(4)
	for _, na := range strangers {
		na.Status = NodeStandby
		c.Assert(mgr.Keeper().SetNodeAccount(ctx, na), IsNil)
	}
	retiring := newActivationVault(c, RetiringVault, strangers)
	c.Assert(mgr.Keeper().SetVault(ctx, active), IsNil)
	c.Assert(mgr.Keeper().SetVault(ctx, retiring), IsNil)
	satisfyActivationGates(ctx, c, mgr.Keeper(), activationChain, nodes)
	mgr.Keeper().SetMimir(ctx, activationMimirKey(activationChain), 1)

	runActivation(ctx, c, mgr)

	// the active vault activates ...
	c.Assert(slices.Contains(vaultChains(ctx, c, mgr.Keeper(), active.PubKey), activationChain.String()), Equals, true)
	// ... and the retiring vault, whose members never proved the chain, is not stamped
	c.Assert(slices.Contains(vaultChains(ctx, c, mgr.Keeper(), retiring.PubKey), activationChain.String()), Equals, false)
}

// The new-signing-scheme guard: XMR needs PubKeyFrost, which only XMRMonovaultKeygen
// produces, so it must never be activatable this way.
func (s *ChainActivationTestSuite) TestXMRNeverActivated(c *C) {
	ctx, mgr, nodes := setupActivation(c, 4)
	vault := newActivationVault(c, ActiveVault, nodes)
	c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)
	satisfyActivationGates(ctx, c, mgr.Keeper(), common.XMRChain, nodes)
	mgr.Keeper().SetMimir(ctx, activationMimirKey(common.XMRChain), 1)

	runActivation(ctx, c, mgr)

	c.Assert(slices.Contains(vaultChains(ctx, c, mgr.Keeper(), vault.PubKey), common.XMRChain.String()), Equals, false)
}

// The churn-time router refresh only fires when Routers is nil, so a live vault
// would otherwise never pick up a newly added EVM chain's contract.
func (s *ChainActivationTestSuite) TestRouterSetOnVaultWithExistingRouters(c *C) {
	ctx, mgr, nodes := setupActivation(c, 4)
	evmChain := common.AVAXChain

	vault := newActivationVault(c, ActiveVault, nodes)
	// pre-existing router for a different chain, so Routers is non-nil
	vault.Routers = []types.ChainContract{{Chain: common.ETHChain, Router: GetRandomETHAddress()}}
	c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)

	router := GetRandomETHAddress()
	mgr.Keeper().SetChainContract(ctx, types.ChainContract{Chain: evmChain, Router: router})
	satisfyActivationGates(ctx, c, mgr.Keeper(), evmChain, nodes)
	mgr.Keeper().SetMimir(ctx, activationMimirKey(evmChain), 1)

	runActivation(ctx, c, mgr)

	after, err := mgr.Keeper().GetVault(ctx, vault.PubKey)
	c.Assert(err, IsNil)
	c.Assert(after.GetContract(evmChain).Router.Equals(router), Equals, true)
}

// A vault's router is pinned for its lifetime and rolls over at churn - the
// RouterUpgradeController deliberately leaves live vaults on the old address. Because
// the enable mimir persists, this pass keeps running after a later router upgrade and
// must not repoint a vault that already has one. On a retiring vault that would aim
// ERC20 migrations at a contract holding none of its funds.
func (s *ChainActivationTestSuite) TestExistingRouterNotOverwritten(c *C) {
	ctx, mgr, nodes := setupActivation(c, 4)
	evmChain := common.AVAXChain

	pinned := GetRandomETHAddress()
	active := newActivationVault(c, ActiveVault, nodes)
	active.Routers = []types.ChainContract{{Chain: evmChain, Router: pinned}}
	retiring := newActivationVault(c, RetiringVault, nodes)
	retiring.Routers = []types.ChainContract{{Chain: evmChain, Router: pinned}}
	c.Assert(mgr.Keeper().SetVault(ctx, active), IsNil)
	c.Assert(mgr.Keeper().SetVault(ctx, retiring), IsNil)

	// the global contract is upgraded - vaults must stay on the old one
	upgraded := GetRandomETHAddress()
	c.Assert(upgraded.Equals(pinned), Equals, false)
	mgr.Keeper().SetChainContract(ctx, types.ChainContract{Chain: evmChain, Router: upgraded})
	satisfyActivationGates(ctx, c, mgr.Keeper(), evmChain, nodes)
	mgr.Keeper().SetMimir(ctx, activationMimirKey(evmChain), 1)

	runActivation(ctx, c, mgr)

	for _, pk := range []common.PubKey{active.PubKey, retiring.PubKey} {
		after, err := mgr.Keeper().GetVault(ctx, pk)
		c.Assert(err, IsNil)
		c.Assert(after.GetContract(evmChain).Router.Equals(pinned), Equals, true)
	}
}

// Runs in EndBlock on every node, so the result must not depend on the order vaults
// were written. Uses the SAME vault pubkeys in both runs - generating fresh random
// vaults per run would compare two unrelated states and prove nothing.
func (s *ChainActivationTestSuite) TestDeterministicAcrossVaultOrdering(c *C) {
	// build the node set and vaults once, with differing prior chain sets so per-vault
	// output is distinguishable
	nodes := activationNodes(4)
	vaultA := newActivationVault(c, ActiveVault, nodes)
	vaultB := newActivationVault(c, ActiveVault, nodes)
	vaultB.Chains = common.Chains{common.THORChain, common.BTCChain, common.BCHChain}.Strings()

	run := func(order []Vault) map[string][]string {
		ctx, mgr := setupManagerForTest(c)
		writeNodes(ctx, c, mgr.Keeper(), nodes)
		for _, v := range order {
			c.Assert(mgr.Keeper().SetVault(ctx, v), IsNil)
		}
		satisfyActivationGates(ctx, c, mgr.Keeper(), activationChain, nodes)
		mgr.Keeper().SetMimir(ctx, activationMimirKey(activationChain), 1)
		runActivation(ctx, c, mgr)
		return map[string][]string{
			vaultA.PubKey.String(): vaultChains(ctx, c, mgr.Keeper(), vaultA.PubKey),
			vaultB.PubKey.String(): vaultChains(ctx, c, mgr.Keeper(), vaultB.PubKey),
		}
	}

	forward := run([]Vault{vaultA, vaultB})
	reverse := run([]Vault{vaultB, vaultA})

	c.Assert(forward, DeepEquals, reverse)
	// and each vault kept its own prior chains, rather than inheriting a sibling's
	c.Assert(slices.Contains(forward[vaultB.PubKey.String()], common.BCHChain.String()), Equals, true)
	c.Assert(slices.Contains(forward[vaultA.PubKey.String()], common.BCHChain.String()), Equals, false)
}

// --- the readiness proof must belong to the chain being activated ---

// chainAttesters filters voters by chain. Without that filter a fully-attested voter
// for ANY chain would satisfy the readiness proof for EVERY chain, so a chain could go
// live on zero attestations of its own. No other test catches this: the negative cases
// all leave the voter store empty, and TestTwoChainsActivatedInSameBlock seeds two
// chains that are both meant to activate.
func (s *ChainActivationTestSuite) TestAttestationForOtherChainDoesNotCount(c *C) {
	ctx, mgr, nodes := setupActivation(c, 4)
	vault := newActivationVault(c, ActiveVault, nodes)
	c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)

	// a complete, consensus-reached proof for BCH ...
	satisfyActivationGates(ctx, c, mgr.Keeper(), common.BCHChain, nodes)
	// ... plus a valid fee and height for LTC, but no LTC voter at all
	c.Assert(mgr.Keeper().SaveNetworkFee(ctx, activationChain, types.NetworkFee{
		Chain: activationChain, TransactionSize: 1000, TransactionFeeRate: 10,
	}), IsNil)
	c.Assert(mgr.Keeper().SetLastChainHeight(ctx, activationChain, activationChainHeight), IsNil)
	mgr.Keeper().SetMimir(ctx, activationMimirKey(activationChain), 1)

	runActivation(ctx, c, mgr)

	c.Assert(slices.Contains(vaultChains(ctx, c, mgr.Keeper(), vault.PubKey), activationChain.String()), Equals, false)
	// directly: BCH's attesters must not be visible to LTC
	activeNodes, err := mgr.Keeper().ListActiveValidators(ctx)
	c.Assert(err, IsNil)
	bchAttesters, _ := chainAttesters(ctx, mgr.Keeper(), common.BCHChain, activeNodes)
	c.Assert(len(bchAttesters), Equals, 4)
	ltcAttesters, ltcProvedAt := chainAttesters(ctx, mgr.Keeper(), activationChain, activeNodes)
	c.Assert(len(ltcAttesters), Equals, 0)
	c.Assert(ltcProvedAt, Equals, int64(0))

	// and once LTC has its own proof it activates
	satisfyActivationGates(ctx, c, mgr.Keeper(), activationChain, nodes)
	runActivation(ctx, c, mgr)
	c.Assert(slices.Contains(vaultChains(ctx, c, mgr.Keeper(), vault.PubKey), activationChain.String()), Equals, true)
}

// G6a heights: the retiring vault that held the chain, the churn that dropped it, a
// proof from between the two, and one from after the drop.
const (
	droppedRetiringHeight = int64(100)
	droppedKeygenHeight   = int64(200)
	staleProofHeight      = int64(150)
	freshProofHeight      = int64(250)
	droppedBlockHeight    = int64(300)
)

// setupDroppedChain is a churn that dropped the chain from the fresh keygen consensus:
// each retiring vault (pre-churn) held it, the active vault the churn produced does
// not, because ConsensusChains no longer had it. The readiness proof is signed by
// everyone but reached consensus at proofHeight.
func setupDroppedChain(c *C, retiringCount int, proofHeight int64) (cosmos.Context, Manager, Vaults, Vault) {
	ctx, mgr, nodes := setupActivation(c, 4)
	ctx = ctx.WithBlockHeight(droppedBlockHeight)

	retiring := make(Vaults, 0, retiringCount)
	for range retiringCount {
		vault := newActivationVault(c, RetiringVault, nodes)
		vault.BlockHeight = droppedRetiringHeight
		vault.Chains = append(vault.Chains, activationChain.String())
		c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)
		retiring = append(retiring, vault)
	}
	fresh := newActivationVault(c, ActiveVault, nodes)
	fresh.BlockHeight = droppedKeygenHeight
	c.Assert(mgr.Keeper().SetVault(ctx, fresh), IsNil)

	setReadinessProof(ctx, c, mgr.Keeper(), activationChain, nodes, proofHeight, activationChainHeight)
	mgr.Keeper().SetMimir(ctx, activationMimirKey(activationChain), 1)
	return ctx, mgr, retiring, fresh
}

// setupRotatedDrop runs a real RotateVault at the given height: an active vault serving
// the chain is replaced by one keygen'd at droppedKeygenHeight without it.
func setupRotatedDrop(c *C, height int64) (cosmos.Context, Manager, *NetworkMgr, NodeAccounts, Vault, Vault) {
	ctx, mgr, nodes := setupActivation(c, 4)
	ctx = ctx.WithBlockHeight(height)
	k := mgr.Keeper()
	networkMgr := newNetworkMgr(k, mgr.TxOutStore(), mgr.EventMgr())

	old := newActivationVault(c, ActiveVault, nodes)
	old.BlockHeight = droppedRetiringHeight
	old.Chains = append(old.Chains, activationChain.String())
	c.Assert(k.SetVault(ctx, old), IsNil)
	fresh := newActivationVault(c, InitVault, nodes)
	fresh.BlockHeight = droppedKeygenHeight
	c.Assert(k.SetVault(ctx, fresh), IsNil)
	c.Assert(networkMgr.RotateVault(ctx, fresh), IsNil)
	return ctx, mgr, networkMgr, nodes, old, fresh
}

// A churn that drops the chain must not be overridden by the older attestation...
func (s *ChainActivationTestSuite) TestNoRestampWhenNewerKeygenDroppedChain(c *C) {
	ctx, mgr, _, fresh := setupDroppedChain(c, 1, staleProofHeight)

	runActivation(ctx, c, mgr)

	c.Assert(slices.Contains(vaultChains(ctx, c, mgr.Keeper(), fresh.PubKey), activationChain.String()), Equals, false)
}

// ...but a proof that reached consensus after the drop is a newer vote than the keygen,
// and re-enables the chain even while the retiring vault is still around.
func (s *ChainActivationTestSuite) TestRestampAllowedWithProofNewerThanDrop(c *C) {
	ctx, mgr, _, fresh := setupDroppedChain(c, 1, freshProofHeight)

	runActivation(ctx, c, mgr)

	c.Assert(slices.Contains(vaultChains(ctx, c, mgr.Keeper(), fresh.PubKey), activationChain.String()), Equals, true)
}

// A proof from the same block as the keygen does not date either way, so it is stale.
func (s *ChainActivationTestSuite) TestNoRestampWithProofAtDropHeight(c *C) {
	ctx, mgr, _, fresh := setupDroppedChain(c, 1, droppedKeygenHeight)

	runActivation(ctx, c, mgr)

	c.Assert(slices.Contains(vaultChains(ctx, c, mgr.Keeper(), fresh.PubKey), activationChain.String()), Equals, false)
}

// The drop decision must outlive the retiring vault that used to carry the evidence.
// The drop comes from a real RotateVault, then NetworkMgr.EndBlock runs activation and
// migration in their real order: the drained retiring vault is marked inactive by
// migrateFunds in the first block, and the next block - mimir still set, stale proof
// still the latest - must still not re-stamp. A fresh fee consensus then re-enables it.
func (s *ChainActivationTestSuite) TestDropDecisionSurvivesRetiringVaultMigration(c *C) {
	// rotation lands a few blocks after the keygen, and the drop is dated by the keygen
	ctx, mgr, networkMgr, nodes, old, fresh := setupRotatedDrop(c, droppedKeygenHeight+10)
	k := mgr.Keeper()
	setReadinessProof(ctx, c, k, activationChain, nodes, staleProofHeight, activationChainHeight)
	k.SetMimir(ctx, activationMimirKey(activationChain), 1)

	droppedAt, err := k.GetChainDroppedHeight(ctx, activationChain)
	c.Assert(err, IsNil)
	c.Assert(droppedAt, Equals, droppedKeygenHeight)
	keptAt, err := k.GetChainDroppedHeight(ctx, common.BTCChain)
	c.Assert(err, IsNil)
	c.Assert(keptAt, Equals, int64(0))

	ctx = ctx.WithBlockHeight(droppedBlockHeight)
	c.Assert(networkMgr.EndBlock(ctx, mgr), IsNil)
	retired, err := k.GetVault(ctx, old.PubKey)
	c.Assert(err, IsNil)
	c.Assert(retired.Status, Equals, InactiveVault)
	c.Assert(slices.Contains(vaultChains(ctx, c, k, fresh.PubKey), activationChain.String()), Equals, false)

	ctx = ctx.WithBlockHeight(ctx.BlockHeight() + 1)
	retiringNow, err := k.GetAsgardVaultsByStatus(ctx, RetiringVault)
	c.Assert(err, IsNil)
	c.Assert(retiringNow, HasLen, 0)
	c.Assert(networkMgr.EndBlock(ctx, mgr), IsNil)
	c.Assert(slices.Contains(vaultChains(ctx, c, k, fresh.PubKey), activationChain.String()), Equals, false)

	ctx = ctx.WithBlockHeight(ctx.BlockHeight() + 1)
	freshID := setReadinessProof(ctx, c, k, activationChain, nodes, ctx.BlockHeight(), activationChainHeight+1)
	// as processNetworkFeeAttestation does on consensus while the mimir is set
	k.SetLatestObservedNetworkFeeVoterID(ctx, activationChain, freshID)
	c.Assert(networkMgr.EndBlock(ctx, mgr), IsNil)
	c.Assert(slices.Contains(vaultChains(ctx, c, k, fresh.PubKey), activationChain.String()), Equals, true)
}

// A drop from a rotation that ran before RotateVault recorded drops lives only in the
// retiring vault. Migrate21to22 saves it at upgrade, so it survives migration the same
// way: the drained vault goes inactive, the next block still must not re-stamp from the
// stale proof, and a fresh fee consensus then re-enables the chain.
func (s *ChainActivationTestSuite) TestPreUpgradeDropSurvivesRetiringVaultMigration(c *C) {
	ctx, mgr, retiring, fresh := setupDroppedChain(c, 1, staleProofHeight)
	k := mgr.Keeper()
	nodes, err := k.ListActiveValidators(ctx)
	c.Assert(err, IsNil)
	networkMgr := newNetworkMgr(k, mgr.TxOutStore(), mgr.EventMgr())

	droppedAt, err := k.GetChainDroppedHeight(ctx, activationChain)
	c.Assert(err, IsNil)
	c.Assert(droppedAt, Equals, int64(0))
	c.Assert(NewMigrator(mgr.(*Mgrs)).Migrate21to22(ctx), IsNil)
	droppedAt, err = k.GetChainDroppedHeight(ctx, activationChain)
	c.Assert(err, IsNil)
	c.Assert(droppedAt, Equals, droppedKeygenHeight)
	keptAt, err := k.GetChainDroppedHeight(ctx, common.BTCChain)
	c.Assert(err, IsNil)
	c.Assert(keptAt, Equals, int64(0))

	c.Assert(networkMgr.EndBlock(ctx, mgr), IsNil)
	retired, err := k.GetVault(ctx, retiring[0].PubKey)
	c.Assert(err, IsNil)
	c.Assert(retired.Status, Equals, InactiveVault)
	c.Assert(slices.Contains(vaultChains(ctx, c, k, fresh.PubKey), activationChain.String()), Equals, false)

	ctx = ctx.WithBlockHeight(ctx.BlockHeight() + 1)
	c.Assert(networkMgr.EndBlock(ctx, mgr), IsNil)
	c.Assert(slices.Contains(vaultChains(ctx, c, k, fresh.PubKey), activationChain.String()), Equals, false)

	ctx = ctx.WithBlockHeight(ctx.BlockHeight() + 1)
	freshID := setReadinessProof(ctx, c, k, activationChain, nodes, ctx.BlockHeight(), activationChainHeight+1)
	k.SetLatestObservedNetworkFeeVoterID(ctx, activationChain, freshID)
	c.Assert(networkMgr.EndBlock(ctx, mgr), IsNil)
	c.Assert(slices.Contains(vaultChains(ctx, c, k, fresh.PubKey), activationChain.String()), Equals, true)
}

// The upgrade step only ever raises a record, and finds nothing to record for a chain
// no retiring vault carries.
func (s *ChainActivationTestSuite) TestPreUpgradeDropRecordOnlyRaises(c *C) {
	ctx, mgr, _, _ := setupDroppedChain(c, 1, staleProofHeight)
	k := mgr.Keeper()
	k.SetChainDroppedHeight(ctx, activationChain, droppedBlockHeight)
	c.Assert(NewMigrator(mgr.(*Mgrs)).Migrate21to22(ctx), IsNil)
	droppedAt, err := k.GetChainDroppedHeight(ctx, activationChain)
	c.Assert(err, IsNil)
	c.Assert(droppedAt, Equals, droppedBlockHeight)

	ctx, mgr, _, _ = setupDroppedChain(c, 0, staleProofHeight)
	c.Assert(NewMigrator(mgr.(*Mgrs)).Migrate21to22(ctx), IsNil)
	droppedAt, err = mgr.Keeper().GetChainDroppedHeight(ctx, activationChain)
	c.Assert(err, IsNil)
	c.Assert(droppedAt, Equals, int64(0))
}

// A proof that reached consensus after the dropping keygen but before the rotation is
// newer than the vote, so it re-enables the chain.
func (s *ChainActivationTestSuite) TestProofBetweenKeygenAndRotationIsFresh(c *C) {
	ctx, mgr, _, nodes, _, fresh := setupRotatedDrop(c, droppedBlockHeight)
	k := mgr.Keeper()
	setReadinessProof(ctx, c, k, activationChain, nodes, freshProofHeight, activationChainHeight)
	k.SetMimir(ctx, activationMimirKey(activationChain), 1)

	runActivation(ctx, c, mgr)

	c.Assert(slices.Contains(vaultChains(ctx, c, k, fresh.PubKey), activationChain.String()), Equals, true)
}

// A first activation is not held to the drop rule: an active vault keygen'd without the
// chain after the proof is the chain's default state, not a reversal - one early
// reporter without the client is enough to leave it off.
func (s *ChainActivationTestSuite) TestFirstActivationNotHeldToNewerKeygen(c *C) {
	ctx, mgr, _, fresh := setupDroppedChain(c, 0, staleProofHeight)

	runActivation(ctx, c, mgr)

	c.Assert(slices.Contains(vaultChains(ctx, c, mgr.Keeper(), fresh.PubKey), activationChain.String()), Equals, true)
}

// The recorded drop holds with no retiring vault left at all...
func (s *ChainActivationTestSuite) TestNoRestampFromRecordedDropAlone(c *C) {
	ctx, mgr, _, fresh := setupDroppedChain(c, 0, staleProofHeight)
	mgr.Keeper().SetChainDroppedHeight(ctx, activationChain, droppedKeygenHeight)

	runActivation(ctx, c, mgr)

	c.Assert(slices.Contains(vaultChains(ctx, c, mgr.Keeper(), fresh.PubKey), activationChain.String()), Equals, false)
}

// ...but not once every active vault carries the chain again: a retiring vault still
// missing it is then stamped as usual, stale record or not.
func (s *ChainActivationTestSuite) TestRecordedDropIgnoredOnceActiveCarriesChain(c *C) {
	ctx, mgr, nodes := setupActivation(c, 4)
	ctx = ctx.WithBlockHeight(droppedBlockHeight)
	active := newActivationVault(c, ActiveVault, nodes)
	active.BlockHeight = droppedKeygenHeight
	active.Chains = append(active.Chains, activationChain.String())
	retiring := newActivationVault(c, RetiringVault, nodes)
	retiring.BlockHeight = droppedRetiringHeight
	c.Assert(mgr.Keeper().SetVault(ctx, active), IsNil)
	c.Assert(mgr.Keeper().SetVault(ctx, retiring), IsNil)
	mgr.Keeper().SetChainDroppedHeight(ctx, activationChain, droppedKeygenHeight)
	setReadinessProof(ctx, c, mgr.Keeper(), activationChain, nodes, staleProofHeight, activationChainHeight)
	mgr.Keeper().SetMimir(ctx, activationMimirKey(activationChain), 1)

	runActivation(ctx, c, mgr)

	c.Assert(slices.Contains(vaultChains(ctx, c, mgr.Keeper(), retiring.PubKey), activationChain.String()), Equals, true)
}

// ...but the rule must not survive stranded funds. With the chain off every active
// vault, migrateFunds has no target and stalls, the retiring-vault funds check holds
// the churn, and re-stamping the chain is the only way out. Same setup as the drop test
// above plus a balance on the chain.
func (s *ChainActivationTestSuite) TestRestampAllowedWhenDroppedChainStrandedFunds(c *C) {
	ctx, mgr, retiring, fresh := setupDroppedChain(c, 1, staleProofHeight)
	retiring[0].AddFunds(common.NewCoins(common.NewCoin(activationChain.GetGasAsset(), cosmos.NewUint(100*common.One))))
	c.Assert(mgr.Keeper().SetVault(ctx, retiring[0]), IsNil)

	runActivation(ctx, c, mgr)

	c.Assert(slices.Contains(vaultChains(ctx, c, mgr.Keeper(), fresh.PubKey), activationChain.String()), Equals, true)
}

// Mainnet churns several Asgards out together, so the carve-out has to be measured over
// the retiring set rather than per vault. Two retiring vaults with the same BlockHeight
// and the same Chains, one still holding a balance on the chain and one already drained:
// the drained sibling must not re-arm the rule, or the funded one stays stranded and the
// churn stays halted forever - the exact deadlock the carve-out exists to break.
func (s *ChainActivationTestSuite) TestRestampAllowedWhenOneOfSeveralRetiringVaultsHoldsFunds(c *C) {
	ctx, mgr, retiring, fresh := setupDroppedChain(c, 2, staleProofHeight)
	retiring[1].AddFunds(common.NewCoins(common.NewCoin(activationChain.GetGasAsset(), cosmos.NewUint(100*common.One))))
	c.Assert(mgr.Keeper().SetVault(ctx, retiring[1]), IsNil)

	runActivation(ctx, c, mgr)

	c.Assert(slices.Contains(vaultChains(ctx, c, mgr.Keeper(), fresh.PubKey), activationChain.String()), Equals, true)
}

// The converse: several retiring vaults, none holding anything on the chain, is a clean
// drop however many of them there are, so the rule still holds.
func (s *ChainActivationTestSuite) TestNoRestampWhenAllRetiringVaultsDrained(c *C) {
	ctx, mgr, _, fresh := setupDroppedChain(c, 2, staleProofHeight)

	runActivation(ctx, c, mgr)

	c.Assert(slices.Contains(vaultChains(ctx, c, mgr.Keeper(), fresh.PubKey), activationChain.String()), Equals, false)
}

// The same evidence must not block an ordinary first activation: a proof newer than the
// active vault's keygen, with a retiring vault that never held the chain.
func (s *ChainActivationTestSuite) TestOlderRetiringVaultDoesNotBlockFirstActivation(c *C) {
	ctx, mgr, nodes := setupActivation(c, 4)
	ctx = ctx.WithBlockHeight(droppedBlockHeight)
	retiring := newActivationVault(c, RetiringVault, nodes)
	retiring.BlockHeight = droppedRetiringHeight
	fresh := newActivationVault(c, ActiveVault, nodes)
	fresh.BlockHeight = droppedKeygenHeight
	c.Assert(mgr.Keeper().SetVault(ctx, retiring), IsNil)
	c.Assert(mgr.Keeper().SetVault(ctx, fresh), IsNil)
	setReadinessProof(ctx, c, mgr.Keeper(), activationChain, nodes, freshProofHeight, activationChainHeight)
	mgr.Keeper().SetMimir(ctx, activationMimirKey(activationChain), 1)

	runActivation(ctx, c, mgr)

	c.Assert(slices.Contains(vaultChains(ctx, c, mgr.Keeper(), fresh.PubKey), activationChain.String()), Equals, true)
}

// --- chainNeedsStamping: the switch that decides whether the voter scan runs ---

func (s *ChainActivationTestSuite) TestChainNeedsStampingBranches(c *C) {
	ctx, mgr, nodes := setupActivation(c, 4)
	k := mgr.Keeper()

	// chain absent
	absent := newActivationVault(c, ActiveVault, nodes)
	c.Assert(chainNeedsStamping(ctx, k, activationChain, Vaults{absent}), Equals, true)

	// present but not in canonical order (the AddFunds append case)
	unsorted := newActivationVault(c, ActiveVault, nodes)
	unsorted.Chains = append(unsorted.Chains, activationChain.String())
	c.Assert(slices.IsSorted(unsorted.Chains), Equals, false)
	c.Assert(chainNeedsStamping(ctx, k, activationChain, Vaults{unsorted}), Equals, true)

	// present and canonical - nothing to do, so the scan must not run
	done := newActivationVault(c, ActiveVault, nodes)
	done.Chains = sortChainsForVault(append(done.GetChains(), activationChain).Distinct()).Strings()
	c.Assert(chainNeedsStamping(ctx, k, activationChain, Vaults{done}), Equals, false)

	// canonical, but an EVM router is still missing
	evmChain := common.AVAXChain
	evm := newActivationVault(c, ActiveVault, nodes)
	evm.Chains = sortChainsForVault(append(evm.GetChains(), evmChain).Distinct()).Strings()
	c.Assert(chainNeedsStamping(ctx, k, evmChain, Vaults{evm}), Equals, false) // no contract stored yet
	k.SetChainContract(ctx, types.ChainContract{Chain: evmChain, Router: GetRandomETHAddress()})
	c.Assert(chainNeedsStamping(ctx, k, evmChain, Vaults{evm}), Equals, true)

	// the XMR monovault is exempt and never counts as needing work
	mono := newActivationVault(c, ActiveVault, nodes)
	mono.Chains = xmrMonovaultChains().Strings()
	c.Assert(chainNeedsStamping(ctx, k, activationChain, Vaults{mono}), Equals, false)
}

// The mimir key this feature depends on has to be a legal mimir key for every chain,
// or the chain is simply unactivatable through this mechanism with nothing to say so.
func (s *ChainActivationTestSuite) TestActivationMimirKeysAreValid(c *C) {
	for _, chain := range common.AllChains {
		key := activationMimirKey(chain)
		c.Assert(len(key) <= constants.MaxMimirLength, Equals, true,
			Commentf("mimir key %q exceeds MaxMimirLength", key))
		// mimirValidKey is the very matcher handler_mimir.go gates MsgMimir on
		c.Assert(mimirValidKey(key), Equals, true,
			Commentf("mimir key %q does not match MimirKeyRegex", key))
	}
}

// TestXMRNeverActivated passes even with G1 removed, because the test vault has no
// PubKeyFrost so G7 blocks XMR anyway. These call the gate directly so the eligibility
// check is pinned on its own reason rather than on a downstream accident.
func (s *ChainActivationTestSuite) TestIneligibleChainsRejectedByEligibilityGate(c *C) {
	ctx, mgr, nodes := setupActivation(c, 4)
	vault := newActivationVault(c, ActiveVault, nodes)
	c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)
	active := Vaults{vault}

	for _, chain := range []common.Chain{common.THORChain, common.XMRChain} {
		satisfyActivationGates(ctx, c, mgr.Keeper(), chain, nodes)
		ok, signed, reason := chainActivationReady(ctx, mgr.Keeper(), chain, active, nodes, nil)
		c.Assert(ok, Equals, false)
		c.Assert(signed, IsNil)
		c.Assert(reason, Equals, "chain not eligible", Commentf("chain %s", chain))
	}
}

// With every active vault exempt there is no one to prove the chain for, so readiness
// must fail rather than vacuously succeed on an empty loop.
func (s *ChainActivationTestSuite) TestNoEligibleActiveVaults(c *C) {
	ctx, mgr, nodes := setupActivation(c, 4)
	mono := newActivationVault(c, ActiveVault, nodes)
	mono.Chains = xmrMonovaultChains().Strings()
	c.Assert(mgr.Keeper().SetVault(ctx, mono), IsNil)
	satisfyActivationGates(ctx, c, mgr.Keeper(), activationChain, nodes)

	ok, _, reason := chainActivationReady(ctx, mgr.Keeper(), activationChain, Vaults{mono}, nodes, nil)
	c.Assert(ok, Equals, false)
	c.Assert(reason, Equals, "no eligible active vaults")
}
