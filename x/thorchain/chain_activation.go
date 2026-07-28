package thorchain

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"
)

// activateEnabledChains stamps a mimir-enabled chain onto the live vaults, so a
// chain whose client already ships in the binary can be turned on without waiting
// for a churn.
//
// Normally vault.Chains is written once, at keygen, from TssVoter.ConsensusChains()
// - a 2/3 vote over what each bifrost self-reported it runs (handler_tss.go). That
// vote is the network's proof that a supermajority can actually observe the chain.
// Here the equivalent proof is the network fee: SaveNetworkFee only persists after
// ObservedNetworkFeeVoter reaches a 2/3 supermajority, so a valid fee means 2/3 of
// active nodes independently scanned the chain and agreed on a rate.
//
// Bifrost needs no change: it starts chain clients from local config, and derives
// each vault's address per chain from the vault pubkey alone (pubkeymanager), so a
// node running the client already watches the right address.
func (vm *NetworkMgr) activateEnabledChains(ctx cosmos.Context, mgr Manager) error {
	k := vm.k

	// Cheap mimir reads first, so the overwhelmingly common case - no chain awaiting
	// activation - costs no vault-store scans at all. AllChains is a fixed array, so
	// this iteration is deterministic.
	requested := make(common.Chains, 0)
	for _, chain := range common.AllChains {
		if chainActivationRequested(ctx, k, chain) {
			requested = append(requested, chain)
		}
	}
	if len(requested) == 0 {
		return nil
	}

	active, err := k.GetAsgardVaultsByStatus(ctx, ActiveVault)
	if err != nil {
		return fmt.Errorf("fail to get active vaults: %w", err)
	}
	if len(active) == 0 {
		return nil
	}

	// G7: never mutate chain sets while a churn is settling. tryRotateInitVaults
	// promotes init vaults as a set and refuses on a chain-set mismatch, so an
	// interleaved stamp could stall rotation.
	initVaults, err := k.GetAsgardVaultsByStatus(ctx, InitVault)
	if err != nil {
		return fmt.Errorf("fail to get init vaults: %w", err)
	}
	if len(initVaults) > 0 {
		return nil
	}

	retiring, err := k.GetAsgardVaultsByStatus(ctx, RetiringVault)
	if err != nil {
		return fmt.Errorf("fail to get retiring vaults: %w", err)
	}

	ready := make(common.Chains, 0, len(requested))
	for _, chain := range requested {
		if ok, reason := chainActivationReady(ctx, k, chain, active); !ok {
			ctx.Logger().Debug("chain activation not ready", "chain", chain, "reason", reason)
			continue
		}
		ready = append(ready, chain)
	}
	if len(ready) == 0 {
		return nil
	}

	// One converge pass for all ready chains. Stamping them one at a time would read
	// stale vault copies on the second chain and write back a set missing the first.
	return stampChainsOnVaults(ctx, mgr, ready, active, retiring)
}

// chainActivationRequested reports whether Enable<CHAIN>Chain is set and in effect.
// Same value convention as every halt key in keeper_halt.go, so unset, absent and 0
// all mean disabled through one path.
//
// ponytail: global gate, per-vault membership check if multi-Asgard skew bites.
// A node without the chain client cannot sign for it (signer.getChain errors
// "not supported"), and TSS keysign needs GetThreshold(n)+1 = ceil(2n/3) of *that
// vault's* membership. A global 2/3 can hold while one vault sits below threshold,
// giving that vault an inbound address it cannot sign outbounds for. To tighten:
//
//	nodeMimirs, _ := k.GetNodeMimirs(ctx, key)
//	for each active vault: count members whose vote > 0 (pk.GetThorAddress(),
//	idiom at handler_tss.go:349-360), require HasSuperMajority(votes, len(Membership))
func chainActivationRequested(ctx cosmos.Context, k keeper.Keeper, chain common.Chain) bool {
	v, err := k.GetMimirWithRef(ctx, constants.MimirTemplateEnableChain, chain)
	if err != nil {
		return false
	}
	return v > 0 && v <= ctx.BlockHeight()
}

// chainActivationReady applies the objective gates. The returned string names the
// failing gate, for logging only.
func chainActivationReady(ctx cosmos.Context, k keeper.Keeper, chain common.Chain, active Vaults) (bool, string) {
	// G1: THORChain is implicit, and XMR is FROST/monovault-only - its address
	// needs PubKeyFrost, which only XMRMonovaultKeygen produces.
	if chain.IsTHORChain() || chain.Equals(common.XMRChain) {
		return false, "chain not eligible"
	}

	// G3: a valid network fee means ObservedNetworkFeeVoter reached a 2/3
	// supermajority. Note GetNetworkFee never errors on absence - it returns a
	// zero-valued fee with a nil error - so gate on Valid(), never on the error.
	nf, err := k.GetNetworkFee(ctx, chain)
	if err != nil {
		return false, "fail to get network fee"
	}
	if err := nf.Valid(); err != nil {
		return false, "no network fee consensus"
	}

	// G4: the observation pipeline is producing heights. Written by
	// processNetworkFeeAttestation on fee consensus, before the chain is on any vault.
	height, err := k.GetLastChainHeight(ctx, chain)
	if err != nil || height <= 0 {
		return false, "no observed chain height"
	}

	// G5: stamping a halted chain would hand it a network-wide churn kill switch
	// (manager_validator_current.go aborts rotation on any halted active chain).
	if k.IsChainHalted(ctx, chain) {
		return false, "chain halted"
	}

	// G6: every eligible active vault must derive an address. This is what fails
	// closed on a chain needing a signing scheme the live vaults don't hold.
	eligible := 0
	for _, vault := range active {
		if isChainActivationExempt(vault) {
			continue
		}
		eligible++
		if _, err := vault.GetAddress(chain); err != nil {
			return false, fmt.Sprintf("vault %s cannot derive address", vault.PubKey)
		}
	}
	if eligible == 0 {
		return false, "no eligible active vaults"
	}

	return true, ""
}

// isChainActivationExempt reports whether a vault must be left alone. The XMR
// monovault is pinned to {THOR, XMR} by xmrMonovaultChains(); adding a third chain
// would break keygenMatchesVault, which discriminates keygen families on
// HasChain(XMR), and the monovault accounting in the validator manager.
func isChainActivationExempt(vault Vault) bool {
	return vault.HasChain(common.XMRChain)
}

// stampChainsOnVaults converges every eligible vault onto the same chain set.
//
// Converge rather than append: a vault may already hold a chain via AddFunds (which
// appends on observing a coin for an unknown chain), in append order. A per-vault
// "append if missing" would skip exactly that vault, leaving it with a divergent
// ordering while its siblings get normalised.
func stampChainsOnVaults(ctx cosmos.Context, mgr Manager, chains common.Chains, active, retiring Vaults) error {
	k := mgr.Keeper()

	pending := make(Vaults, 0, len(active)+len(retiring))
	changed := false

	// Active vaults are all-or-nothing on the gate, which chainActivationReady has
	// already applied: a partially stamped set would publish an inbound address for a
	// vault that cannot sign, and leaves Asgards asymmetric.
	for _, vault := range active {
		if isChainActivationExempt(vault) {
			continue
		}
		updated, dirty := applyChainsToVault(ctx, k, vault, chains)
		pending = append(pending, updated)
		changed = changed || dirty
	}

	// Retiring vaults are best-effort. They still sign migrations and outbounds, and
	// bifrost matches their address on any chain its client runs, so keeping them in
	// step keeps solvency and outbound routing coherent. But they must not be able to
	// block activation: a stale retiring vault on sub-dust, or one missing PubKeyEddsa,
	// would otherwise hold the chain back forever.
	for _, vault := range retiring {
		if isChainActivationExempt(vault) {
			continue
		}
		addable := make(common.Chains, 0, len(chains))
		for _, chain := range chains {
			if _, err := vault.GetAddress(chain); err != nil {
				ctx.Logger().Info("skipping retiring vault that cannot address chain",
					"chain", chain, "pub_key", vault.PubKey, "error", err)
				continue
			}
			addable = append(addable, chain)
		}
		if len(addable) == 0 {
			continue
		}
		updated, dirty := applyChainsToVault(ctx, k, vault, addable)
		pending = append(pending, updated)
		changed = changed || dirty
	}

	if !changed {
		return nil
	}

	stamped := make([]string, 0, len(pending))
	for _, vault := range pending {
		if err := k.SetVault(ctx, vault); err != nil {
			return fmt.Errorf("fail to set vault %s: %w", vault.PubKey, err)
		}
		stamped = append(stamped, vault.PubKey.String())
	}

	// ponytail: log only, no event. EventSecurity is the obvious candidate but every
	// other emitter uses it for an anomaly, and tools/events pages the security channel
	// on EndBlock ones - a planned activation must not read as an incident. The
	// set_mimir event already records the intent on-chain. A dedicated event type would
	// mean proto changes; add one if operators need this machine-readable.
	ctx.Logger().Info("chains activated on live vaults",
		"chains", chains.Strings(), "vaults", strings.Join(stamped, ","))

	return nil
}

// applyChainsToVault returns the vault with chains present and its chain set in
// canonical form, plus whether anything actually changed. Also refreshes the
// router: the churn-time refresh in migrateFunds only fires when Routers is nil,
// so a live vault would otherwise never pick up a new EVM chain's contract.
func applyChainsToVault(ctx cosmos.Context, k keeper.Keeper, vault Vault, chains common.Chains) (Vault, bool) {
	target := sortChainsForVault(append(vault.GetChains(), chains...).Distinct())
	dirty := false

	if !slices.Equal(vault.Chains, target.Strings()) {
		vault.Chains = target.Strings()
		dirty = true
	}

	for _, cc := range k.GetChainContracts(ctx, chains) {
		if vault.GetContract(cc.Chain).Router.Equals(cc.Router) {
			continue
		}
		vault.UpdateContract(cc)
		dirty = true
	}

	return vault, dirty
}

// sortChainsForVault puts a chain set in the same canonical form that
// TssVoter.ConsensusChains produces, so vault.Chains has one representation
// regardless of which producer wrote it.
func sortChainsForVault(chains common.Chains) common.Chains {
	sort.SliceStable(chains, func(i, j int) bool {
		return chains[i].String() < chains[j].String()
	})
	return chains
}
