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

	// The readiness proof is re-tested against today's validators, so it has to be
	// today's validators that are read - not whoever was active when the fee was agreed.
	activeNodes, err := k.ListActiveValidators(ctx)
	if err != nil {
		return fmt.Errorf("fail to list active validators: %w", err)
	}

	ready := make(common.Chains, 0, len(requested))
	// proved[chain] is the node set that demonstrably observed that chain, carried
	// through so the retiring-vault pass can apply the same keysign bar. Only ever
	// indexed by chain, never ranged over, so it introduces no map-order dependence.
	proved := make(map[common.Chain]map[string]bool, len(requested))
	for _, chain := range requested {
		ok, signed, reason := chainActivationReady(ctx, k, chain, active, activeNodes)
		if !ok {
			ctx.Logger().Debug("chain activation not ready", "chain", chain, "reason", reason)
			continue
		}
		ready = append(ready, chain)
		proved[chain] = signed
	}
	if len(ready) == 0 {
		return nil
	}

	// One converge pass for all ready chains. Stamping them one at a time would read
	// stale vault copies on the second chain and write back a set missing the first.
	return stampChainsOnVaults(ctx, mgr, ready, active, retiring, proved)
}

// chainActivationRequested reports whether Enable<CHAIN>Chain is set and in effect.
// Same value convention as every halt key in keeper_halt.go, so unset, absent and 0
// all mean disabled through one path.
func chainActivationRequested(ctx cosmos.Context, k keeper.Keeper, chain common.Chain) bool {
	v, err := k.GetMimirWithRef(ctx, constants.MimirTemplateEnableChain, chain)
	if err != nil {
		return false
	}
	return v > 0 && v <= ctx.BlockHeight()
}

// chainActivationReadinessWindow bounds how old the network-fee quorum backing an
// activation may be. Freshness is a separate property from who signed: G4b already
// re-tests the signer set against today's validators, but a node that proved the chain
// observable a year ago and is still active proves nothing about today - bifrost can be
// restarted with that chain disabled in its config. Because SetLastChainHeight only
// moves forward on fee consensus, a chain whose observations stop has its voter age
// out, and an already-granted activation is not renewed.
//
// ponytail: fixed window, promote to a mimir if operators need it per chain. ~1 day at
// 6s blocks - generous enough that the slowest supported chain still reports well
// inside it, tight enough that a proof from a bygone validator set cannot be reused.
const chainActivationReadinessWindow = int64(14400)

// chainActivationReady applies the objective gates. On success it also returns the set
// of node addresses that proved the chain observable, so the retiring-vault pass can
// apply the same keysign bar without repeating the voter lookup. The returned string
// names the failing gate, for logging only.
func chainActivationReady(ctx cosmos.Context, k keeper.Keeper, chain common.Chain, active Vaults, activeNodes NodeAccounts) (bool, map[string]bool, string) {
	// G1: THORChain is implicit, and XMR is FROST/monovault-only - its address
	// needs PubKeyFrost, which only XMRMonovaultKeygen produces.
	if chain.IsTHORChain() || chain.Equals(common.XMRChain) {
		return false, nil, "chain not eligible"
	}

	// G3: a valid network fee means ObservedNetworkFeeVoter reached a 2/3
	// supermajority. Note GetNetworkFee never errors on absence - it returns a
	// zero-valued fee with a nil error - so gate on Valid(), never on the error.
	nf, err := k.GetNetworkFee(ctx, chain)
	if err != nil {
		return false, nil, "fail to get network fee"
	}
	if nf.Valid() != nil {
		return false, nil, "no network fee consensus"
	}

	// G4: the observation pipeline is producing heights. Written by
	// processNetworkFeeAttestation on fee consensus, before the chain is on any vault.
	height, err := k.GetLastChainHeight(ctx, chain)
	if err != nil || height <= 0 {
		return false, nil, "no observed chain height"
	}

	// G5: stamping a halted chain would hand it a network-wide churn kill switch
	// (manager_validator_current.go aborts rotation on any halted active chain that
	// still holds funds).
	if k.IsChainHalted(ctx, chain) {
		return false, nil, "chain halted"
	}

	// The stored NetworkFee is a bare (size, rate) pair - it records neither who proved
	// the chain observable nor when, so on its own it cannot carry a readiness proof.
	// The voter behind it can: height plus that fee's rate and size are exactly its key,
	// and processNetworkFeeAttestation writes both records from the same attestation.
	//
	// A miss returns a zero-signer record rather than an error, so G4a/G4b/G6 all fail
	// closed on one. The pair can only desync when SetLastChainHeight rejects a backwards
	// height after SaveNetworkFee already succeeded; the next attestation resyncs them and
	// activation simply retries, since this runs every block.
	voter, err := k.GetObservedNetworkFeeVoter(ctx, height, chain, int64(nf.TransactionFeeRate), int64(nf.TransactionSize))
	if err != nil {
		return false, nil, "fail to get network fee voter"
	}

	// G4a: the proof is recent. BlockHeight is stamped once, by the attestation that
	// carried the voter over the line, so 0 means it never reached consensus at all.
	if voter.BlockHeight <= 0 {
		return false, nil, "readiness proof never reached consensus"
	}
	if ctx.BlockHeight()-voter.BlockHeight > chainActivationReadinessWindow {
		return false, nil, "readiness proof is stale"
	}

	// G4b: the proof still holds among *today's* active nodes. HasConsensus counts only
	// signers that are currently node keys, so a quorum assembled by validators who have
	// since churned out no longer clears the bar.
	if !voter.HasConsensus(activeNodes) {
		return false, nil, "readiness proof lacks a current supermajority"
	}

	// Signers are the nodes that demonstrably ran the chain client: reporting a fee for
	// a chain requires having scanned it.
	signed := make(map[string]bool, len(voter.Signers))
	for _, addr := range voter.GetSigners() {
		signed[addr.String()] = true
	}

	// G6: every eligible active vault must derive an address *and* hold enough
	// chain-capable members to sign for it. The global quorum in G4b is not sufficient
	// here: with multiple Asgards it can be concentrated outside one vault, and TSS
	// keysign needs a supermajority of *that vault's* membership. Publishing an inbound
	// address for a vault that cannot assemble a keysign party would strand deposits.
	eligible := 0
	for _, vault := range active {
		if isChainActivationExempt(vault) {
			continue
		}
		eligible++
		if _, err := vault.GetAddress(chain); err != nil {
			return false, nil, fmt.Sprintf("vault %s cannot derive address", vault.PubKey)
		}
		if ok, reason := vaultHasChainKeysignQuorum(ctx, vault, signed); !ok {
			return false, nil, reason
		}
	}
	if eligible == 0 {
		return false, nil, "no eligible active vaults"
	}

	return true, signed, ""
}

// vaultHasChainKeysignQuorum reports whether enough of the vault's own members proved
// the chain observable to clear a TSS keysign.
//
// HasSuperMajority is the same ceil(2n/3) bar as conversion.GetThreshold(n)+1, the
// requiredSigners figure the keysign party is built against (querier.go). It is used
// here rather than GetThreshold because it is integer-only - GetThreshold reaches for
// math.Ceil on a float, which has no place in EndBlock consensus code.
func vaultHasChainKeysignQuorum(ctx cosmos.Context, vault Vault, signed map[string]bool) (bool, string) {
	members := vault.GetMembership()
	if len(members) == 0 {
		return false, fmt.Sprintf("vault %s has empty membership", vault.PubKey)
	}

	capable := 0
	for _, pk := range members {
		addr, err := pk.GetThorAddress()
		if err != nil {
			// Fail closed: an unreadable member counts as incapable, never as capable.
			ctx.Logger().Error("fail to get thor address for vault member",
				"pub_key", pk, "vault", vault.PubKey, "error", err)
			continue
		}
		if signed[addr.String()] {
			capable++
		}
	}

	if !HasSuperMajority(capable, len(members)) {
		return false, fmt.Sprintf("vault %s below keysign threshold: %d of %d members proved the chain",
			vault.PubKey, capable, len(members))
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
func stampChainsOnVaults(ctx cosmos.Context, mgr Manager, chains common.Chains, active, retiring Vaults, proved map[common.Chain]map[string]bool) error {
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
			// Same keysign bar the active vaults cleared. A retiring vault still signs
			// migrations and outbounds, so one whose own members never proved the chain
			// would be handed an address it cannot sign for - the active-vault harm in
			// miniature. Skipped rather than fatal, per the best-effort rule above.
			if ok, reason := vaultHasChainKeysignQuorum(ctx, vault, proved[chain]); !ok {
				ctx.Logger().Info("skipping retiring vault below keysign threshold",
					"chain", chain, "pub_key", vault.PubKey, "reason", reason)
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

	// Populate only a router the vault is missing, never replace one it already has.
	// A vault's router is pinned for its lifetime and rolls over at churn -
	// migrateFunds refreshes Routers only when nil, and RouterUpgradeController
	// deliberately leaves live vaults on the old address (its test asserts exactly
	// that). Overwriting here would break that invariant every time a chain with an
	// Enable<CHAIN>Chain mimir still set later has its router upgraded: the mimir
	// persists, so this pass runs forever. Worse on a retiring vault, where it would
	// point ERC20 migrations at a contract that never held that vault's funds.
	for _, cc := range k.GetChainContracts(ctx, chains) {
		existing := vault.GetContract(cc.Chain)
		if !existing.IsEmpty() {
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
