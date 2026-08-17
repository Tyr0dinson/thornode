package core

import (
	"fmt"
	"time"

	rand "math/rand/v2"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	. "gitlab.com/thorchain/thornode/v3/test/simulation/actors/common"
	"gitlab.com/thorchain/thornode/v3/test/simulation/pkg/thornode"
	. "gitlab.com/thorchain/thornode/v3/test/simulation/pkg/types"
)

// InsolvencyActor is a simulation actor that checks if solvency halt/unhalt works.
//
// This actor is single-node mocknet only. It cannot run on mocknet-cluster
// because Asgard is controlled by a TSS pubkey there, and this actor relies on
// direct signing from a single node key to create the simulated insolvency gap.
//
// InsolvencyActor does this:
//  1. Find the active Asgard vault and lock one node user + one normal user.
//  2. "Steal" funds - create a temporary gap between vault accounting and the
//     real wallet balance using a mocknet-only ignore memo.
//  3. Wait until Mimir key SolvencyHalt<Chain>Chain is set (> 1).
//  4. Keep checking for a few blocks to confirm the halt stays active.
//  5. Return funds to the vault with the same ignore memo.
//  6. Wait until Mimir key SolvencyHalt<Chain>Chain returns to 0.
//  7. Keep checking for a few blocks to confirm the unhalt stays active.
//  8. Release all locked users.
//
// The observer drops any vault-adjacent tx whose memo is an exact match for the
// simulation ignore marker. The steal path therefore reduces the wallet balance
// without changing vault accounting, and the return path restores the wallet
// without creating a matching inbound observation. stolenAmount is recorded as
// the actual Asgard wallet delta after the steal so the restore closes the
// fee-inclusive gap instead of only the nominal transfer amount.
//
// Keep this memo in sync with bifrost/observer/simulation_ignore_mocknet.go.
type InsolvencyActor struct {
	Actor

	chain                    common.Chain
	asset                    common.Asset
	nodeClient               LiteChainClient
	userClient               LiteChainClient
	nodeUser                 *User
	userAccount              *User
	asgardVaultPubKey        common.PubKey
	nodeVaultPubKey          common.PubKey
	nodeL1Address            common.Address
	stolenAmount             cosmos.Uint
	insolvencyDurationBlocks int64
	solvencyDurationBlocks   int64
	solvencyStartAt          int64
}

const simulationIgnoreMemo = "MOCKNET_SIMULATION_ONLY_SKIP_OBSERVATION"

// bumpBlock submits a small number of self-transfers on chains that only
// produce blocks when txs are pending in mocknet.
func (a *InsolvencyActor) bumpBlock() {
	isCosmos := a.chain.Equals(common.GAIAChain) || a.chain.Equals(common.NOBLEChain)
	if !a.chain.IsEVM() && !isCosmos {
		return
	}

	// Cosmos simulation clients convert 1e8 to 1e6 base units for signing, so
	// 100 here is 1 uatom/uusdc (minimum non-zero amount).
	tickAmount := cosmos.ZeroUint()
	if isCosmos {
		tickAmount = cosmos.NewUint(100)
	}

	tickTarget := int64(5)
	a.Log().Info().
		Stringer("chain", a.chain).
		Int64("tick_target", tickTarget).
		Stringer("tick_amount", tickAmount).
		Msg("starting chain-bump tick loop")
	height := a.getHeight()
	to := a.userAccount.Address(a.chain)
	sent := 0
	for i := range tickTarget {
		memo := fmt.Sprintf("tick-%d-%d", height, i)
		if _, err := Send(a.Log(), a.userAccount, to, a.asset, memo, tickAmount); err != nil {
			a.Log().Debug().
				Err(err).
				Stringer("chain", a.chain).
				Int64("tick_index", i).
				Int("tick_sent", sent).
				Int64("tick_target", tickTarget).
				Msg("failed to send chain-bump tick tx")
			break
		}
		sent++
	}
	a.Log().Info().
		Stringer("chain", a.chain).
		Int64("chain_height", height).
		Int64("tick_target", tickTarget).
		Int("tick_sent", sent).
		Msg("chain-bump tick loop complete")
}

// NewInsolvencyActor builds the actor DAG for the full insolvency lifecycle test.
func NewInsolvencyActor(chain common.Chain, rng *rand.Rand) *Actor {
	a := &InsolvencyActor{
		Actor:                    *NewActor(fmt.Sprintf("Insolvency-%s", chain), rng),
		chain:                    chain,
		asset:                    chain.GetGasAsset(),
		insolvencyDurationBlocks: 3,
		solvencyDurationBlocks:   20,
	}
	a.Timeout = 10 * time.Minute
	a.Interval = time.Second

	a.SetLogger(a.Log().With().Stringer("asset", a.asset).Logger())

	// 1. Setup
	a.Ops = append(a.Ops, a.setup)

	// 2. Steal
	a.Ops = append(a.Ops, a.stealFunds)

	// 3. Verify halt
	a.Ops = append(a.Ops, a.verifyInsolvencyHalt)

	// 4. Verify it stays halted for a few blocks
	a.Ops = append(a.Ops, a.verifyInsolvencyHaltDuration)

	// 5. Restore
	a.Ops = append(a.Ops, a.restoreFunds)

	// 6. Verify un-halt
	a.Ops = append(a.Ops, a.verifySolvencyUnhalt)

	// 7. Verify it stays unhalted for a few blocks
	a.Ops = append(a.Ops, a.verifySolvencyDuration)

	// 8. Cleanup
	a.Ops = append(a.Ops, a.cleanup)

	return &a.Actor
}

// setup acquires accounts and identifies the active Asgard vault.
func (a *InsolvencyActor) setup(config *OpConfig) OpResult {
	releaseSetupLocks := func() {
		if a.userAccount != nil {
			a.userAccount.Release()
			a.userAccount = nil
		}
		if a.nodeUser != nil {
			a.nodeUser.Release()
			a.nodeUser = nil
		}
		a.userClient = nil
		a.nodeClient = nil
		a.asgardVaultPubKey = ""
		a.nodeVaultPubKey = ""
	}

	failSetup := func(err error) OpResult {
		releaseSetupLocks()
		return OpResult{
			Error:  err,
			Finish: true,
		}
	}

	if len(config.NodeUsers) == 0 {
		return failSetup(fmt.Errorf("no node users configured"))
	}
	nodeUser := config.NodeUsers[0]
	if nodeUser == nil {
		return failSetup(fmt.Errorf("first node user is nil"))
	}

	// Resolve the chain's elected inbound vault instead of relying on Asgard
	// response ordering, then verify this single-node scenario controls its
	// chain-specific signing key. An unsupported topology is a failed scenario,
	// not a successful skip; cluster runs exclude this suite explicitly.
	asgardVault, asgardVaultPubKey, err := getActiveVaultForChain(a.chain)
	if err != nil {
		return failSetup(fmt.Errorf("failed to resolve active asgard vault for chain %s: %w", a.chain, err))
	}
	asgardSignerPubKey, err := vaultSigningPubKey(asgardVault, a.chain)
	if err != nil {
		return failSetup(err)
	}
	if !nodeUser.PubKey(a.chain).Equals(asgardSignerPubKey) {
		return failSetup(fmt.Errorf(
			"node user pubkey %s does not match active Asgard signer %s for chain %s",
			nodeUser.PubKey(a.chain),
			asgardSignerPubKey,
			a.chain,
		))
	}

	if !nodeUser.Acquire() {
		a.Log().Info().Msg("node user locked, waiting")
		return OpResult{Continue: false}
	}
	a.nodeUser = nodeUser

	if a.userAccount == nil {
		for _, user := range config.Users {
			a.SetLogger(a.Log().With().Str("user", user.Name()).Logger())
			if !user.Acquire() {
				continue
			}
			a.userAccount = user
			break
		}
	}
	if a.userAccount == nil {
		// Do not hold the node lock while waiting for a user account.
		a.nodeUser.Release()
		a.nodeUser = nil
		a.Log().Info().Msg("no available user accounts to acquire, waiting")
		return OpResult{Continue: false}
	}

	var ok bool
	a.userClient, ok = a.userAccount.ChainClients[a.asset.Chain]
	if !ok {
		return failSetup(fmt.Errorf("failed to get user chain client for chain %s", a.chain))
	}

	a.asgardVaultPubKey = asgardVaultPubKey
	a.nodeVaultPubKey = asgardSignerPubKey

	a.Log().Info().
		Stringer("vault_pubkey", a.asgardVaultPubKey).
		Stringer("signer_pubkey", a.nodeVaultPubKey).
		Msg("node pubkey matches active Asgard signer pubkey, insolvency test can proceed")

	a.nodeL1Address, err = a.nodeVaultPubKey.GetAddress(a.asset.Chain)
	if err != nil {
		return failSetup(fmt.Errorf("failed to get asgard L1 address for chain %s: %w", a.chain, err))
	}

	a.nodeClient, ok = a.nodeUser.ChainClients[a.chain]
	if !ok {
		return failSetup(fmt.Errorf("failed to get node chain client for chain %s", a.chain))
	}

	return OpResult{Continue: true}
}

// stealFunds creates the wallet/accounting delta that should trigger solvency halt.
// The tx is intentionally ignored by the mocknet biforst observer, so vault accounting
// stays unchanged while the on-chain vault wallet balance decreases.
func (a *InsolvencyActor) stealFunds(config *OpConfig) OpResult {
	a.Log().Info().Msg("attempting to steal funds from Asgard vault to create insolvency")

	startBalance, err := a.getNodeBalance()
	if err != nil {
		return OpResult{
			Error:  fmt.Errorf("failed to get asgard node balance for chain %s: %w", a.chain, err),
			Finish: true,
		}
	}

	if startBalance.IsZero() {
		return OpResult{
			Error:  fmt.Errorf("asgard node %s has no %s balance to steal", a.nodeL1Address, a.asset),
			Finish: true,
		}
	}

	// Default target: meaningful but non-exhaustive delta so restore remains feasible.
	targetAmount := startBalance.QuoUint64(10)

	if targetAmount.IsZero() {
		return OpResult{
			Error:  fmt.Errorf("calculated stolen amount is zero, node balance %s too small", startBalance),
			Finish: true,
		}
	}

	toAddress := a.userAccount.Address(a.chain)
	txid, err := Send(a.Log(), a.nodeUser, toAddress, a.asset, simulationIgnoreMemo, targetAmount)
	if err != nil {
		return OpResult{
			Error:  fmt.Errorf("failed to execute steal transaction: %w", err),
			Finish: true,
		}
	}

	actualDelta, err := a.getBalanceDelta(startBalance)
	if err != nil {
		return OpResult{
			Error:  fmt.Errorf("failed to compute steal delta after tx %s on %s: %w", txid, a.chain, err),
			Finish: true,
		}
	}
	a.stolenAmount = actualDelta

	a.Log().Info().
		Str("txid", txid).
		Str("memo", simulationIgnoreMemo).
		Stringer("target_stolen_amount", targetAmount).
		Stringer("stolen_amount", a.stolenAmount).
		Str("balances", a.getBalancesStr()).
		Int64("chain_height", a.getHeight()).
		Msg("broadcasted simulation-ignore steal transaction")
	return OpResult{Continue: true}
}

// restoreFunds closes the induced insolvency gap by sending funds back to the
// vault with the same ignore memo, so wallet balance rises without observation.
func (a *InsolvencyActor) restoreFunds(config *OpConfig) OpResult {
	a.Log().Info().Msg("attempting to restore solvency after induced insolvency")

	if a.stolenAmount.IsZero() {
		return OpResult{
			Error:  fmt.Errorf("restore amount is zero"),
			Finish: true,
		}
	}

	txid, err := Send(a.Log(), a.userAccount, a.nodeL1Address, a.asset, simulationIgnoreMemo, a.stolenAmount)
	if err != nil {
		return OpResult{
			Error:  fmt.Errorf("failed to execute restore transaction: %w", err),
			Finish: true,
		}
	}

	a.Log().Info().
		Str("txid", txid).
		Str("memo", simulationIgnoreMemo).
		Stringer("stolen_amount", a.stolenAmount).
		Stringer("restored_amount", a.stolenAmount).
		Str("balances", a.getBalancesStr()).
		Int64("chain_height", a.getHeight()).
		Msg("broadcasted simulation-ignore restore transaction")
	return OpResult{Continue: true}
}

// verifyInsolvencyHalt waits for SolvencyHalt<Chain>Chain to become active (> 0).
func (a *InsolvencyActor) verifyInsolvencyHalt(config *OpConfig) OpResult {
	key := fmt.Sprintf("SolvencyHalt%sChain", a.asset.Chain)
	mimir, err := thornode.GetMimir(key)
	if err != nil {
		return OpResult{
			Error:  fmt.Errorf("failed to get mimir: %w", err),
			Finish: true,
		}
	}
	insolvent := mimir > 0
	if insolvent {
		a.Log().Info().
			Bool("insolvent", insolvent).
			Str("balances", a.getBalancesStr()).
			Int64(key, mimir).
			Int64("chain_height", a.getHeight()).
			Msg("insolvency detected as expected")
	}

	if !insolvent {
		// Progress chain incrementally when waiting for the halt to be set.
		a.bumpBlock()
	}

	return OpResult{Continue: insolvent}
}

// verifyInsolvencyHaltDuration checks that insolvency halt remains active long enough.
// SolvencyHalt<Chain>Chain stores a THORChain block height, so duration is evaluated against THOR height.
func (a *InsolvencyActor) verifyInsolvencyHaltDuration(config *OpConfig) OpResult {
	key := fmt.Sprintf("SolvencyHalt%sChain", a.asset.Chain)
	mimir, err := thornode.GetMimir(key)
	if err != nil {
		return OpResult{
			Error:  fmt.Errorf("failed to get mimir: %w", err),
			Finish: true,
		}
	}
	if mimir <= 1 {
		return OpResult{
			Error:  fmt.Errorf("mimir %q should be > 1 but is %d", key, mimir),
			Finish: true,
		}
	}

	thorHeight, err := a.getThorHeight()
	if err != nil {
		return OpResult{
			Error:  fmt.Errorf("failed to fetch THOR height while checking insolvency duration: %w", err),
			Finish: true,
		}
	}
	finished := mimir+a.insolvencyDurationBlocks <= thorHeight

	a.Log().Info().
		Bool("finished", finished).
		Int64("mimir", mimir).
		Int64("waitBlocks", a.insolvencyDurationBlocks).
		Str("balances", a.getBalancesStr()).
		Int64("thor_height", thorHeight).
		Int64("chain_height", a.getHeight()).
		Msg("insolvency duration check progress")

	if finished {
		a.Log().Info().
			Int64("mimir", mimir).
			Int64("waitBlocks", a.insolvencyDurationBlocks).
			Int64("thor_height", thorHeight).
			Msg("insolvency duration requirement satisfied")
	}

	return OpResult{Continue: finished}
}

// verifySolvencyUnhalt waits for SolvencyHalt<Chain>Chain to reset to 0.
func (a *InsolvencyActor) verifySolvencyUnhalt(config *OpConfig) OpResult {
	key := fmt.Sprintf("SolvencyHalt%sChain", a.asset.Chain)
	mimir, err := thornode.GetMimir(key)
	if err != nil {
		return OpResult{
			Error:  fmt.Errorf("failed to get mimir: %w", err),
			Finish: true,
		}
	}
	solvent := mimir <= 0
	if solvent {
		a.Log().Info().
			Bool("solvent", solvent).
			Str("balances", a.getBalancesStr()).
			Int64(key, mimir).
			Int64("chain_height", a.getHeight()).
			Msg("solvency restored")
	}

	if !solvent {
		// Progress chain incrementally while waiting for auto-unhalt to clear Mimir.
		a.bumpBlock()
	}

	return OpResult{Continue: solvent}
}

// verifySolvencyDuration checks that solvency remains active long enough after unhalt.
func (a *InsolvencyActor) verifySolvencyDuration(config *OpConfig) OpResult {
	key := fmt.Sprintf("SolvencyHalt%sChain", a.asset.Chain)
	mimir, err := thornode.GetMimir(key)
	if err != nil {
		return OpResult{
			Error:  fmt.Errorf("failed to get mimir: %w", err),
			Finish: true,
		}
	}
	if mimir > 0 {
		return OpResult{
			Error:  fmt.Errorf("mimir %q should be <= 0 but is %d", key, mimir),
			Finish: true,
		}
	}

	thorHeight, err := a.getThorHeight()
	if err != nil {
		return OpResult{
			Error:  fmt.Errorf("failed to fetch THOR height while checking solvency duration: %w", err),
			Finish: true,
		}
	}

	if a.solvencyStartAt == 0 {
		a.solvencyStartAt = thorHeight
	}
	finished := thorHeight >= a.solvencyStartAt+a.solvencyDurationBlocks

	a.Log().Info().
		Bool("finished", finished).
		Int64("mimir", mimir).
		Int64("waitBlocks", a.solvencyDurationBlocks).
		Int64("solvency_start_thor_height", a.solvencyStartAt).
		Str("balances", a.getBalancesStr()).
		Int64("thor_height", thorHeight).
		Int64("chain_height", a.getHeight()).
		Msg("solvency duration check progress")

	if finished {
		a.Log().Info().
			Int64("waitBlocks", a.solvencyDurationBlocks).
			Int64("solvency_start_thor_height", a.solvencyStartAt).
			Int64("thor_height", thorHeight).
			Msg("solvency duration requirement satisfied")
	}

	if !finished {
		// Progress chain incrementally while validating post-unhalt stability.
		a.bumpBlock()
	}

	return OpResult{Continue: finished}
}

// cleanup releases all actor-held user locks and marks execution complete.
func (a *InsolvencyActor) cleanup(config *OpConfig) OpResult {
	if a.userAccount != nil {
		a.userAccount.Release()
		a.userAccount = nil
		a.Log().Info().Msg("cleanup complete, user account released")
	}
	if a.nodeUser != nil {
		a.nodeUser.Release()
		a.nodeUser = nil
		a.Log().Info().Msg("cleanup complete, node account released")
	}
	return OpResult{Finish: true}
}

// getThorHeight returns latest THORChain block height.
func (a *InsolvencyActor) getThorHeight() (int64, error) {
	block, err := thornode.GetBlock(0)
	if err != nil {
		return -1, fmt.Errorf("failed to get thornode block: %w", err)
	}
	return block.Header.Height, nil
}

// getNodeBalance returns the current on-chain wallet balance for the selected
// Asgard-signing node on this actor chain.
func (a *InsolvencyActor) getNodeBalance() (cosmos.Uint, error) {
	pk := a.nodeVaultPubKey
	if pk.IsEmpty() {
		return cosmos.ZeroUint(), fmt.Errorf("vault pubkey is empty for chain %s", a.chain)
	}
	nodeAcct, err := a.nodeClient.GetAccount(&pk)
	if err != nil {
		return cosmos.ZeroUint(), fmt.Errorf("failed to get node account for chain %s: %w", a.chain, err)
	}
	return nodeAcct.Coins.GetCoin(a.asset).Amount, nil
}

// getBalanceDelta returns the decrease between a starting Asgard balance and
// the current balance (including chain-specific fee effects).
//
// Some chains (notably SOL with finalized commitment reads) may lag briefly after
// broadcast, so this polls for a short window before declaring no delta.
func (a *InsolvencyActor) getBalanceDelta(startBalance cosmos.Uint) (cosmos.Uint, error) {
	const (
		maxWait      = 30 * time.Second
		pollInterval = 250 * time.Millisecond
	)

	deadline := time.Now().Add(maxWait)
	endBalance := startBalance
	for {
		var err error
		endBalance, err = a.getNodeBalance()
		if err != nil {
			return cosmos.ZeroUint(), fmt.Errorf("failed to get ending node balance for chain %s: %w", a.chain, err)
		}
		if endBalance.LT(startBalance) {
			return startBalance.Sub(endBalance), nil
		}
		if time.Now().After(deadline) {
			break
		}
		time.Sleep(pollInterval)
	}

	return cosmos.ZeroUint(), fmt.Errorf("no balance delta detected after waiting %s (start=%s end=%s)", maxWait, startBalance, endBalance)
}

// getHeight returns chain height when the client supports it, else -1.
func (a *InsolvencyActor) getHeight() int64 {
	client, ok := a.nodeClient.(interface{ GetHeight() (int64, error) })
	if !ok {
		return -1
	}
	height, err := client.GetHeight()
	if err != nil {
		a.Log().Error().Err(err).Msg("client.GetHeight error")
		return -1
	}
	return height
}

// getBalancesStr builds a compact debug string with Asgard-reported balance and
// wallet-reported balance for quick operator diagnostics.
func (a *InsolvencyActor) getBalancesStr() string {
	var asgardStr, accountStr string
	if a.asgardVaultPubKey.IsEmpty() {
		asgardStr = "active vault pubkey is empty"
	} else {
		asgard, err := thornode.GetVault(a.asgardVaultPubKey.String())
		if err != nil {
			asgardStr = fmt.Sprintf("failed to get vault %s: %v", a.asgardVaultPubKey, err)
		} else {
			for _, coin := range asgard.Coins {
				asset, err := common.NewAsset(coin.Asset)
				if err != nil {
					continue
				}
				if a.asset.Equals(asset) {
					asgardStr = fmt.Sprintf("%s %s", coin.Amount, a.asset)
				}
			}
		}
	}
	acctBal, err := a.getNodeBalance()
	if err != nil {
		accountStr = err.Error()
	} else {
		accountStr = acctBal.String()
	}
	return fmt.Sprintf("asgard_bal = %s, account_bal = %s", asgardStr, accountStr)
}
