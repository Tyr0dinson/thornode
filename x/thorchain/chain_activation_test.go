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

// newActivationVault builds an asgard vault with THOR+BTC, both key types present
// so address derivation succeeds for either signing algo.
func newActivationVault(c *C, status types.VaultStatus) Vault {
	vault := GetRandomVault()
	vault.Type = AsgardVault
	vault.Status = status
	vault.Chains = common.Chains{common.THORChain, common.BTCChain}.Strings()
	vault.Membership = common.PubKeys{GetRandomPubKey(), GetRandomPubKey()}.Strings()
	vault.PubKeyEddsa = getRandomEddsaPubKey(c)
	return vault
}

// satisfyActivationGates sets everything except the enable mimir itself.
func satisfyActivationGates(ctx cosmos.Context, c *C, k keeper.Keeper, chain common.Chain) {
	c.Assert(k.SaveNetworkFee(ctx, chain, types.NetworkFee{
		Chain:              chain,
		TransactionSize:    1000,
		TransactionFeeRate: 10,
	}), IsNil)
	c.Assert(k.SetLastChainHeight(ctx, chain, 1024), IsNil)
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
	ctx, mgr := setupManagerForTest(c)
	vault := newActivationVault(c, ActiveVault)
	c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)
	satisfyActivationGates(ctx, c, mgr.Keeper(), activationChain)

	runActivation(ctx, c, mgr)

	c.Assert(slices.Contains(vaultChains(ctx, c, mgr.Keeper(), vault.PubKey), activationChain.String()), Equals, false)
}

func (s *ChainActivationTestSuite) TestNoStampWhenMimirInFuture(c *C) {
	ctx, mgr := setupManagerForTest(c)
	vault := newActivationVault(c, ActiveVault)
	c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)
	satisfyActivationGates(ctx, c, mgr.Keeper(), activationChain)
	mgr.Keeper().SetMimir(ctx, activationMimirKey(activationChain), ctx.BlockHeight()+100)

	runActivation(ctx, c, mgr)

	c.Assert(slices.Contains(vaultChains(ctx, c, mgr.Keeper(), vault.PubKey), activationChain.String()), Equals, false)
}

// GetNetworkFee returns a zero-valued fee with a NIL error for an absent chain, so
// the gate must test Valid() rather than the error. This asserts that shape.
func (s *ChainActivationTestSuite) TestNoStampWhenNetworkFeeAbsent(c *C) {
	ctx, mgr := setupManagerForTest(c)
	vault := newActivationVault(c, ActiveVault)
	c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)
	c.Assert(mgr.Keeper().SetLastChainHeight(ctx, activationChain, 1024), IsNil)
	mgr.Keeper().SetMimir(ctx, activationMimirKey(activationChain), 1)

	nf, err := mgr.Keeper().GetNetworkFee(ctx, activationChain)
	c.Assert(err, IsNil)         // absent fee does NOT error
	c.Assert(nf.Valid(), NotNil) // it is simply invalid
	c.Assert(nf.TransactionSize, Equals, uint64(0))

	runActivation(ctx, c, mgr)

	c.Assert(slices.Contains(vaultChains(ctx, c, mgr.Keeper(), vault.PubKey), activationChain.String()), Equals, false)
}

func (s *ChainActivationTestSuite) TestNoStampWhenNoObservedChainHeight(c *C) {
	ctx, mgr := setupManagerForTest(c)
	vault := newActivationVault(c, ActiveVault)
	c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)
	c.Assert(mgr.Keeper().SaveNetworkFee(ctx, activationChain, types.NetworkFee{
		Chain: activationChain, TransactionSize: 1000, TransactionFeeRate: 10,
	}), IsNil)
	mgr.Keeper().SetMimir(ctx, activationMimirKey(activationChain), 1)

	runActivation(ctx, c, mgr)

	c.Assert(slices.Contains(vaultChains(ctx, c, mgr.Keeper(), vault.PubKey), activationChain.String()), Equals, false)
}

func (s *ChainActivationTestSuite) TestNoStampWhenChainHalted(c *C) {
	ctx, mgr := setupManagerForTest(c)
	vault := newActivationVault(c, ActiveVault)
	c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)
	satisfyActivationGates(ctx, c, mgr.Keeper(), activationChain)
	mgr.Keeper().SetMimir(ctx, activationMimirKey(activationChain), 1)
	mgr.Keeper().SetMimir(ctx, fmt.Sprintf("Halt%sChain", activationChain), 1)

	runActivation(ctx, c, mgr)

	c.Assert(slices.Contains(vaultChains(ctx, c, mgr.Keeper(), vault.PubKey), activationChain.String()), Equals, false)
}

// A churn in flight must defer the stamp - tryRotateInitVaults promotes init vaults
// as a set and refuses on a chain-set mismatch.
func (s *ChainActivationTestSuite) TestNoStampWhileInitVaultPresent(c *C) {
	ctx, mgr := setupManagerForTest(c)
	active := newActivationVault(c, ActiveVault)
	initVault := newActivationVault(c, InitVault)
	c.Assert(mgr.Keeper().SetVault(ctx, active), IsNil)
	c.Assert(mgr.Keeper().SetVault(ctx, initVault), IsNil)
	satisfyActivationGates(ctx, c, mgr.Keeper(), activationChain)
	mgr.Keeper().SetMimir(ctx, activationMimirKey(activationChain), 1)

	runActivation(ctx, c, mgr)

	c.Assert(slices.Contains(vaultChains(ctx, c, mgr.Keeper(), active.PubKey), activationChain.String()), Equals, false)
}

// --- the happy path ---

func (s *ChainActivationTestSuite) TestStampsActiveAndRetiringVaults(c *C) {
	ctx, mgr := setupManagerForTest(c)
	activeA := newActivationVault(c, ActiveVault)
	activeB := newActivationVault(c, ActiveVault)
	retiring := newActivationVault(c, RetiringVault)
	for _, v := range []Vault{activeA, activeB, retiring} {
		c.Assert(mgr.Keeper().SetVault(ctx, v), IsNil)
	}
	satisfyActivationGates(ctx, c, mgr.Keeper(), activationChain)
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
	ctx, mgr := setupManagerForTest(c)
	vault := newActivationVault(c, ActiveVault)
	c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)
	satisfyActivationGates(ctx, c, mgr.Keeper(), activationChain)
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
	ctx, mgr := setupManagerForTest(c)
	already := newActivationVault(c, ActiveVault)
	// AddFunds appends, so the chain lands last regardless of sort order
	already.Chains = append(already.Chains, activationChain.String())
	sibling := newActivationVault(c, ActiveVault)
	c.Assert(mgr.Keeper().SetVault(ctx, already), IsNil)
	c.Assert(mgr.Keeper().SetVault(ctx, sibling), IsNil)
	satisfyActivationGates(ctx, c, mgr.Keeper(), activationChain)
	mgr.Keeper().SetMimir(ctx, activationMimirKey(activationChain), 1)

	runActivation(ctx, c, mgr)

	// both vaults end with the identical normalised set
	c.Assert(vaultChains(ctx, c, mgr.Keeper(), already.PubKey), DeepEquals,
		vaultChains(ctx, c, mgr.Keeper(), sibling.PubKey))
}

// Two chains enabled in the same block must both land. Stamping them one at a time
// reads a stale vault copy on the second chain and writes back a set missing the first.
func (s *ChainActivationTestSuite) TestTwoChainsActivatedInSameBlock(c *C) {
	ctx, mgr := setupManagerForTest(c)
	vault := newActivationVault(c, ActiveVault)
	c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)

	for _, chain := range []common.Chain{common.LTCChain, common.DOGEChain} {
		satisfyActivationGates(ctx, c, mgr.Keeper(), chain)
		mgr.Keeper().SetMimir(ctx, activationMimirKey(chain), 1)
	}

	runActivation(ctx, c, mgr)

	chains := vaultChains(ctx, c, mgr.Keeper(), vault.PubKey)
	c.Assert(slices.Contains(chains, common.LTCChain.String()), Equals, true)
	c.Assert(slices.Contains(chains, common.DOGEChain.String()), Equals, true)
}

func (s *ChainActivationTestSuite) TestSecondRunIsNoOp(c *C) {
	ctx, mgr := setupManagerForTest(c)
	vault := newActivationVault(c, ActiveVault)
	c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)
	satisfyActivationGates(ctx, c, mgr.Keeper(), activationChain)
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
	ctx, mgr := setupManagerForTest(c)
	ed25519Chain := common.SOLChain

	good := newActivationVault(c, ActiveVault)
	bad := newActivationVault(c, ActiveVault)
	bad.PubKeyEddsa = common.EmptyPubKey // cannot address an ed25519 chain
	c.Assert(mgr.Keeper().SetVault(ctx, good), IsNil)
	c.Assert(mgr.Keeper().SetVault(ctx, bad), IsNil)
	satisfyActivationGates(ctx, c, mgr.Keeper(), ed25519Chain)
	mgr.Keeper().SetMimir(ctx, activationMimirKey(ed25519Chain), 1)

	runActivation(ctx, c, mgr)

	c.Assert(slices.Contains(vaultChains(ctx, c, mgr.Keeper(), good.PubKey), ed25519Chain.String()), Equals, false)
	c.Assert(slices.Contains(vaultChains(ctx, c, mgr.Keeper(), bad.PubKey), ed25519Chain.String()), Equals, false)
}

// The XMR monovault is pinned to {THOR, XMR}; a third chain would break the keygen
// family discrimination in keygenMatchesVault.
func (s *ChainActivationTestSuite) TestMonovaultUntouched(c *C) {
	ctx, mgr := setupManagerForTest(c)
	active := newActivationVault(c, ActiveVault)
	mono := GetRandomVault()
	mono.Type = AsgardVault
	mono.Status = ActiveVault
	mono.Chains = xmrMonovaultChains().Strings()
	mono.PubKeyFrost = GetRandomPubKey()
	c.Assert(mgr.Keeper().SetVault(ctx, active), IsNil)
	c.Assert(mgr.Keeper().SetVault(ctx, mono), IsNil)
	satisfyActivationGates(ctx, c, mgr.Keeper(), activationChain)
	mgr.Keeper().SetMimir(ctx, activationMimirKey(activationChain), 1)

	runActivation(ctx, c, mgr)

	c.Assert(vaultChains(ctx, c, mgr.Keeper(), mono.PubKey), DeepEquals, xmrMonovaultChains().Strings())
	// the ordinary vault is still stamped
	c.Assert(slices.Contains(vaultChains(ctx, c, mgr.Keeper(), active.PubKey), activationChain.String()), Equals, true)
}

// A retiring vault that cannot address the chain must be skipped, not block the
// activation - otherwise a stale retiring vault holds the chain back forever.
func (s *ChainActivationTestSuite) TestRetiringVaultIsBestEffort(c *C) {
	ctx, mgr := setupManagerForTest(c)
	ed25519Chain := common.SOLChain

	active := newActivationVault(c, ActiveVault)
	retiring := newActivationVault(c, RetiringVault)
	retiring.PubKeyEddsa = common.EmptyPubKey
	c.Assert(mgr.Keeper().SetVault(ctx, active), IsNil)
	c.Assert(mgr.Keeper().SetVault(ctx, retiring), IsNil)
	satisfyActivationGates(ctx, c, mgr.Keeper(), ed25519Chain)
	mgr.Keeper().SetMimir(ctx, activationMimirKey(ed25519Chain), 1)

	runActivation(ctx, c, mgr)

	c.Assert(slices.Contains(vaultChains(ctx, c, mgr.Keeper(), active.PubKey), ed25519Chain.String()), Equals, true)
	c.Assert(slices.Contains(vaultChains(ctx, c, mgr.Keeper(), retiring.PubKey), ed25519Chain.String()), Equals, false)
}

// The new-signing-scheme guard: XMR needs PubKeyFrost, which only XMRMonovaultKeygen
// produces, so it must never be activatable this way.
func (s *ChainActivationTestSuite) TestXMRNeverActivated(c *C) {
	ctx, mgr := setupManagerForTest(c)
	vault := newActivationVault(c, ActiveVault)
	c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)
	satisfyActivationGates(ctx, c, mgr.Keeper(), common.XMRChain)
	mgr.Keeper().SetMimir(ctx, activationMimirKey(common.XMRChain), 1)

	runActivation(ctx, c, mgr)

	c.Assert(slices.Contains(vaultChains(ctx, c, mgr.Keeper(), vault.PubKey), common.XMRChain.String()), Equals, false)
}

// The churn-time router refresh only fires when Routers is nil, so a live vault
// would otherwise never pick up a newly added EVM chain's contract.
func (s *ChainActivationTestSuite) TestRouterSetOnVaultWithExistingRouters(c *C) {
	ctx, mgr := setupManagerForTest(c)
	evmChain := common.AVAXChain

	vault := newActivationVault(c, ActiveVault)
	// pre-existing router for a different chain, so Routers is non-nil
	vault.Routers = []types.ChainContract{{Chain: common.ETHChain, Router: GetRandomETHAddress()}}
	c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)

	router := GetRandomETHAddress()
	mgr.Keeper().SetChainContract(ctx, types.ChainContract{Chain: evmChain, Router: router})
	satisfyActivationGates(ctx, c, mgr.Keeper(), evmChain)
	mgr.Keeper().SetMimir(ctx, activationMimirKey(evmChain), 1)

	runActivation(ctx, c, mgr)

	after, err := mgr.Keeper().GetVault(ctx, vault.PubKey)
	c.Assert(err, IsNil)
	c.Assert(after.GetContract(evmChain).Router.Equals(router), Equals, true)
}

// Runs in EndBlock on every node, so the result must not depend on the order vaults
// were written. Uses the SAME vault pubkeys in both runs - generating fresh random
// vaults per run would compare two unrelated states and prove nothing.
func (s *ChainActivationTestSuite) TestDeterministicAcrossVaultOrdering(c *C) {
	// build the vault set once, with differing prior chain sets so per-vault output
	// is distinguishable
	vaultA := newActivationVault(c, ActiveVault)
	vaultB := newActivationVault(c, ActiveVault)
	vaultB.Chains = common.Chains{common.THORChain, common.BTCChain, common.BCHChain}.Strings()

	run := func(order []Vault) map[string][]string {
		ctx, mgr := setupManagerForTest(c)
		for _, v := range order {
			c.Assert(mgr.Keeper().SetVault(ctx, v), IsNil)
		}
		satisfyActivationGates(ctx, c, mgr.Keeper(), activationChain)
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
