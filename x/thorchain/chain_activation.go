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
// Here the equivalent proof is network-fee attestation: a node cannot report a fee for
// a chain it did not scan, so the signers recorded on the chain's ObservedNetworkFeeVoters
// are exactly the nodes that have demonstrated they run its client. Those signers are
// filtered to the currently-active set, then required to clear the same ceil(2n/3) bar
// both network-wide and within every individual Asgard's membership - the latter because
// TSS keysign is per-vault, so a network-wide quorum can still leave one vault unable to
// sign. See chainAttesters for why the signers are gathered by scan rather than lookup.
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

	// G0: never mutate chain sets while a churn is settling. tryRotateInitVaults
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

	// Drop chains that are already fully stamped BEFORE running the gates. The enable
	// mimir persists after activation, so this routine keeps being entered for the rest
	// of the chain's life, and without this the fee-voter scan below would run on every
	// one of those blocks.
	//
	// Retiring vaults count too. One that missed the keysign quorum on the block the
	// active vaults activated would otherwise never be reconsidered - the chain drops out
	// of pending for good and stampChainsOnVaults, its only evaluation point, is never
	// reached again. Including them costs at most until they churn out, and only for
	// those that can address the chain at all, so a permanently unaddressable vault
	// still cannot hold the path open.
	//
	// Known cost, named rather than optimised: a retiring vault that can address the
	// chain but can never assemble its keysign quorum keeps the full gate - including the
	// chainAttesters scan - running once per block for the rest of the migration window,
	// even though the active set is already stamped. The scan is bounded to that one
	// chain's fee voters and the window ends when the vault churns out, so the ceiling is
	// a handful of KV reads per block for at most a migration period. Bounding it properly
	// would mean predicting the keysign outcome before computing the attester set the
	// prediction needs, which is the scan itself.
	pending := make(common.Chains, 0, len(requested))
	for _, chain := range requested {
		if chainNeedsStamping(ctx, k, chain, active) || retiringNeedsStamping(ctx, k, chain, retiring) {
			pending = append(pending, chain)
		}
	}
	if len(pending) == 0 {
		return nil
	}

	// The readiness proof is re-tested against today's validators, so it has to be
	// today's validators that are read - not whoever was active when the fee was agreed.
	activeNodes, err := k.ListActiveValidators(ctx)
	if err != nil {
		return fmt.Errorf("fail to list active validators: %w", err)
	}

	ready := make(common.Chains, 0, len(pending))
	// proved[chain] is the node set that demonstrably observed that chain, carried
	// through so the retiring-vault pass can apply the same keysign bar. Only ever
	// indexed by chain, never ranged over, so it introduces no map-order dependence.
	proved := make(map[common.Chain]map[string]bool, len(pending))
	for _, chain := range pending {
		ok, signed, reason := chainActivationReady(ctx, k, chain, active, activeNodes, retiring)
		if !ok {
			// Info, not Debug: production runs at info level, and an operator who has
			// set the mimir and is waiting needs to see which gate is holding it. Only
			// reached while a chain is genuinely awaiting activation, so it is not
			// per-block noise for the rest of time.
			ctx.Logger().Info("chain activation not ready", "chain", chain, "reason", reason)
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

// chainNeedsStamping reports whether stamping the chain would actually change an
// eligible active vault - either because the vault lacks it, because its chain set is
// not yet in canonical order, or because an EVM router is still to be populated.
func chainNeedsStamping(ctx cosmos.Context, k keeper.Keeper, chain common.Chain, active Vaults) bool {
	for _, vault := range active {
		if isChainActivationExempt(vault) {
			continue
		}
		target := sortChainsForVault(append(vault.GetChains(), chain).Distinct())
		if !slices.Equal(vault.Chains, target.Strings()) {
			return true
		}
		existing := vault.GetContract(chain)
		if existing.IsEmpty() {
			if cc, err := k.GetChainContract(ctx, chain); err == nil && !cc.IsEmpty() {
				return true
			}
		}
	}
	return false
}

// retiringNeedsStamping reports whether a retiring vault could still take the chain.
// Only vaults that can derive an address qualify: the keysign quorum is the condition
// worth retrying, an underivable address never is.
func retiringNeedsStamping(ctx cosmos.Context, k keeper.Keeper, chain common.Chain, retiring Vaults) bool {
	for _, vault := range retiring {
		if isChainActivationExempt(vault) || vault.HasChain(chain) {
			continue
		}
		if _, err := vault.GetAddress(chain); err != nil {
			continue
		}
		return true
	}
	return false
}

// chainAttesters returns the currently-active nodes that have proved they can observe
// the chain, by scanning the network-fee voters recorded for it. Signing a fee is
// direct evidence: a node cannot report a fee for a chain it did not scan.
//
// A scan rather than a keyed lookup, because nothing records which voter produced the
// stored NetworkFee. GetLastChainHeight cannot identify it: fee consensus is only one
// of its three writers - every observed inbound advances it too
// (handler_observed_tx_helpers.go) - so it drifts off the fee heights as soon as the
// chain sees any traffic, and anyone can push it off deliberately by sending dust to
// the derived pre-activation address.
//
// The union across voters is deliberate, and it is why no freshness window is applied
// to the voter itself. Bifrost posts MsgNetworkFee only when the rate *changes*
// (utxo/client_internal.go, xrp/block_scanner.go, gaia/cosmos_block_scanner.go), so on
// a stable-fee chain - XRP, Cosmos, TRON, precisely the chains this feature exists to
// turn on - there may be exactly one voter, written once and never refreshed. Any bar
// measured on voter age is therefore unsatisfiable there, not merely strict.
//
// The filter to activeNodes establishes who the attesters ARE, not that their
// attestation is still current - a node that scanned the chain a year ago, is still
// active, and has since dropped the client from its bifrost config counts the same as
// one scanning it now. G6b is what supplies currency, by deferring to the most recent
// keygen vote; this function deliberately supplies only identity.
//
// Known limit - the attester set cannot be refreshed on demand. Since a stable-fee chain
// posts a fee only when the rate changes and lastFee is per-process, the set is in
// practice frozen at the validators active when the fee last reached consensus: nodes
// churning in afterwards never join it (a standby node's MsgNetworkFee is rejected by
// isSignedByActiveNodeAccounts), so G6b and G7 decay monotonically across churns. Fine
// for the primary flow, where activation follows the first fee consensus immediately,
// but the funds carve-out in G6a may find the proof already unsatisfiable by the time it
// is needed. Where the scanner posts on a period-aligned height (XRP, Cosmos) a
// coordinated bifrost restart refreshes it; TRON has no alignment and so no
// operator-driven refresh at all - see docs/new-chains/new-chain-process.md.
//
// Scoped to the chain's own keys: the voter store is never pruned, and this runs every
// block for as long as a chain sits unconverged, so the unscoped iterator would make
// EndBlock cost scale with every fee voter the network has ever recorded.
func chainAttesters(ctx cosmos.Context, k keeper.Keeper, chain common.Chain, activeNodes NodeAccounts) map[string]bool {
	isActive := make(map[string]bool, len(activeNodes))
	for _, na := range activeNodes {
		isActive[na.NodeAddress.String()] = true
	}

	attesters := make(map[string]bool, len(activeNodes))
	iter := k.GetObservedNetworkFeeVoterIteratorForChain(ctx, chain)
	defer iter.Close()
	for ; iter.Valid(); iter.Next() {
		var voter ObservedNetworkFeeVoter
		if err := k.Cdc().Unmarshal(iter.Value(), &voter); err != nil {
			ctx.Logger().Error("fail to unmarshal network fee voter", "error", err)
			continue
		}
		if !voter.Chain.Equals(chain) {
			continue
		}
		// BlockHeight is stamped only by the attestation that carried the voter to a
		// supermajority, so an unfinished vote is not proof of anything.
		if voter.BlockHeight <= 0 {
			continue
		}
		for _, addr := range voter.GetSigners() {
			if isActive[addr.String()] {
				attesters[addr.String()] = true
			}
		}
	}
	return attesters
}

// chainActivationReady applies the objective gates. On success it also returns the set
// of node addresses that proved the chain observable, so the retiring-vault pass can
// apply the same keysign bar without repeating the voter lookup. The returned string
// names the failing gate, for logging only.
func chainActivationReady(ctx cosmos.Context, k keeper.Keeper, chain common.Chain, active Vaults, activeNodes NodeAccounts, retiring Vaults) (bool, map[string]bool, string) {
	// G1: THORChain is implicit, and XMR is FROST/monovault-only - its address
	// needs PubKeyFrost, which only XMRMonovaultKeygen produces.
	if chain.IsTHORChain() || chain.Equals(common.XMRChain) {
		return false, nil, "chain not eligible"
	}

	// G2: a valid network fee means ObservedNetworkFeeVoter reached a 2/3
	// supermajority. Note GetNetworkFee never errors on absence - it returns a
	// zero-valued fee with a nil error - so gate on Valid(), never on the error.
	nf, err := k.GetNetworkFee(ctx, chain)
	if err != nil {
		return false, nil, "fail to get network fee"
	}
	if nf.Valid() != nil {
		return false, nil, "no network fee consensus"
	}

	// G3: the observation pipeline is producing heights. Written by
	// processNetworkFeeAttestation on fee consensus, before the chain is on any vault.
	height, err := k.GetLastChainHeight(ctx, chain)
	if err != nil || height <= 0 {
		return false, nil, "no observed chain height"
	}

	// G4: stamping a halted chain would hand it a network-wide churn kill switch
	// (manager_validator_current.go aborts rotation on any halted active chain that
	// still holds funds).
	if k.IsChainHalted(ctx, chain) {
		return false, nil, "chain halted"
	}

	// G5: an EVM chain needs its router before it can be advertised. GetChainContracts
	// silently omits an empty or unreadable contract, so without this the chain lands in
	// vault.Chains and queryInboundAddresses publishes it with router "" - bifrost will
	// not recognise router deposits, leaving the token path dead while the chain reads
	// as live. RouterUpgradeController cannot bootstrap an initial contract, so a
	// governance ordering mistake is enough to reach this.
	if chain.IsEVM() {
		cc, err := k.GetChainContract(ctx, chain)
		if err != nil {
			return false, nil, "fail to get chain contract"
		}
		if cc.IsEmpty() {
			return false, nil, "evm chain has no router contract"
		}
	}

	// G6a: never overrule a keygen vote that is newer than the vault set which had the
	// chain.
	//
	// vault.Chains is written at keygen from TssVoter.ConsensusChains(), a 2/3 vote over
	// what each bifrost reports it actually runs. So when a retiring vault holds the chain
	// and a newer active vault does not, the churn that produced the active set was told
	// by a supermajority that the chain is no longer run - operators disabled it in
	// bifrost, say. Re-stamping it from the historical attester union would silently
	// override that fresh vote and publish an inbound address nobody is scanning.
	//
	// This is what supplies currency to the proof - but only for a clean drop, see the
	// funds carve-out below. The active-set filter in chainAttesters cannot supply it at
	// all, since it establishes who the attesters are rather than that they still run the
	// client. The signal is deliberately drawn from the vaults rather than from the
	// mimir's effective height: that value is routinely backdated to 1 to mean "now", so
	// it cannot date the operator's intent.
	//
	// Known limit: it lasts only while a retiring vault that held the chain is still
	// around, i.e. the migration window after the churn that dropped it. Beyond that the
	// evidence is gone from state and the operator is expected to unset the mimir. That
	// window is exactly when the re-stamp would otherwise happen.
	//
	// The funds carve-out: the veto stands down when the drop stranded funds.
	//
	// An orderly chain removal is ragnarok first, which drains the pools and empties the
	// vaults, so the veto still holds there - it sees no funds. Funds still on the chain
	// mean the drop was NOT orderly: migrateFunds has no target vault for a chain no
	// active vault carries, so it stalls; the retiring-vault funds check in
	// ValidatorMgr.BeginBlock then holds the churn; and with the veto in force there is
	// no way out at all, since stamping the chain back on is the only thing that unblocks
	// migration. A permanent network-wide churn halt is the worse outcome, and the
	// operator setting the mimir is the explicit override. Every other gate still applies.
	//
	// The carve-out is measured over the retiring SET, not per vault, because the deadlock
	// it escapes is a property of the set: BeginBlock halts the churn if ANY retiring vault
	// still holds funds, and migrateFunds can only drain any of them once the chain is back
	// on the active set. Mainnet runs several Asgards churning out together with the same
	// BlockHeight and the same Chains, so per-vault evaluation would let a sibling that
	// happens to be drained re-arm the veto and leave the funded one stuck - the exact
	// topology the escape hatch exists for.
	//
	// Residual risk, stated plainly: if the chain really has been abandoned by every
	// bifrost and funds were left behind anyway, this lets an operator re-stamp it and
	// publish an inbound address nobody scans. Nothing in state distinguishes that from
	// the accidental drop - a per-node freshness bar would, but SetLastObserveHeight is
	// only written when a node observes an actual tx, so on a quiet stranded chain it is
	// unsatisfiable for the same reason a fee-voter freshness window is (see
	// chainAttesters).
	//
	// Deliberately looser than the churn blocker, which is HasFundsAboveDust over the
	// whole vault: any non-zero balance on this chain disarms the veto, so a sub-dust
	// remainder does too even though it would not have stalled anything. The exact
	// predicate would be per-chain-above-dust, which no helper offers.
	stranded := false
	for _, retired := range retiring {
		if isChainActivationExempt(retired) || !retired.HasChain(chain) {
			continue
		}
		if retired.HasFundsForChain(chain) {
			stranded = true
			break
		}
	}
	if !stranded {
		for _, retired := range retiring {
			if isChainActivationExempt(retired) || !retired.HasChain(chain) {
				continue
			}
			for _, vault := range active {
				if isChainActivationExempt(vault) || vault.HasChain(chain) {
					continue
				}
				if vault.BlockHeight > retired.BlockHeight {
					return false, nil, fmt.Sprintf(
						"vault %s was keygen'd at %d without the chain, after retiring vault %s at %d held it",
						vault.PubKey, vault.BlockHeight, retired.PubKey, retired.BlockHeight)
				}
			}
		}
	}

	// The nodes that proved the chain observable, restricted to those still active.
	// See chainAttesters for why this is a scan and what it does and does not prove.
	signed := chainAttesters(ctx, k, chain, activeNodes)

	// G6b: the proof still holds across the network as it stands today. Same ceil(2n/3)
	// bar ObservedNetworkFeeVoter.HasConsensus applies, but recomputed from the filtered
	// attester set, so a quorum assembled by validators who have since churned out - or
	// one propped up by members who have gone standby - no longer clears it.
	if !HasSuperMajority(len(signed), len(activeNodes)) {
		return false, nil, fmt.Sprintf("readiness proof lacks a current supermajority: %d of %d active nodes",
			len(signed), len(activeNodes))
	}

	// G7: every eligible active vault must derive an address *and* hold enough
	// chain-capable members to sign for it. The network-wide quorum in G6b is not sufficient
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

	// All-or-nothing has to survive a failed write, not just a failed gate. SetVault can
	// error (keeper_vault.go, via the asgard index writers), NetworkMgr.EndBlock only
	// logs what this returns, and there is no enclosing cache - so writing straight
	// through would leave one Asgard advertising an inbound address for the chain while
	// its sibling does not, which is the asymmetry the gate exists to prevent.
	cacheCtx, commit := ctx.CacheContext()
	stamped := make([]string, 0, len(pending))
	for _, vault := range pending {
		if err := k.SetVault(cacheCtx, vault); err != nil {
			return fmt.Errorf("fail to set vault %s: %w", vault.PubKey, err)
		}
		stamped = append(stamped, vault.PubKey.String())
	}
	commit()

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
