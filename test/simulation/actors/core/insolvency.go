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
//  2. "Steal" funds - create a temporary gap between "vault and wallet (insolvency).
//  3. Wait until Mimir key SolvencyHalt<Chain>Chain is set (> 1).
//  4. Keep checking for a few blocks to confirm the halt stays active.
//  5. Restore funds using a chain-specific method.
//  6. Wait until Mimir key SolvencyHalt<Chain>Chain returns to 0.
//  7. Keep checking for a few blocks to confirm the unhalt stays active.
//  8. Release all locked users.
//
// The actor currently serves these chain categories:
//
// A) EVM chains & GAIA
//   - Steal path:
//     Send from Asgard wallet to a user with empty memo.
//     This creates a real wallet delta using stolenAmount as the transfer target.
//   - Restore path:
//     1) Send from Asgard wallet to itself with memo "NOOP:novault"
//     to reconcile vault accounting once observed.
//     2) Send user->Asgard "DONATE:<asset>" to raise wallet balance and let
//     auto-unhalt resume scanner processing of pending EVM txs.
//
// B) UTXO chains
//   - Steal path:
//     Send from Asgard wallet to user using "CONSOLIDATE" memo.
//     This is used to build a simulation-only mismatch scenario.
//   - Restore path:
//     User deposits back to Asgard with "CONSOLIDATE".
//     In simulation this uses the unobserved consolidate behavior to close the test flow.
//
// C) SOL
//   - Steal path:
//     Same as EVM-like send, but the actor records the real wallet delta
//     (amount + fee) so restore can close the full gap exactly.
//   - Restore path:
//     User deposits to Asgard with memo "NOOP:novault" using a custom tx shape
//     that contains two transfer instructions. SOL scanner rejects multi-transfer
//     txs, so wallet can be re-funded without increasing vault accounting.
//
// D) TRON
//   - Steal path:
//     Compute the minimum required gap (from pool ratio + PermittedSolvencyGapUSD &
//     DollarsPerRune config mimirs) that should cross solvency threshold,
//     then execute steal and track the real wallet delta (amount + fee).
//   - Restore path:
//     Split restore amount into many sub-dust TRX inbounds (< DustThreshold).
//     TRON scanner drops these txs while wallet balance still increases.
//
// E) XRP
//   - XRP restore flow remains provisional and not treated as suite-stable.
//
// Extra notes:
//   - stolenAmount target starts as 10% of Asgard wallet balance by default.
//   - TRON steal path computes a lower target that is just above permitted
//     solvency gap (to minimize restore tx count), and fails if lookup fails.
//   - Suite-level inclusion/exclusion is controlled by the insolvency launcher.
type InsolvencyActor struct {
	Actor

	chain                    common.Chain
	asset                    common.Asset
	nodeClient               LiteChainClient
	userClient               LiteChainClient
	nodeUser                 *User
	userAccount              *User
	nodeVaultPubKey          common.PubKey
	nodeL1Address            common.Address
	stolenAmount             cosmos.Uint
	insolvencyDurationBlocks int64
	solvencyDurationBlocks   int64
	solvencyStartAt          int64
}

type solanaExtraTransferSigner interface {
	SignTxWithExtraTransfer(tx SimTx, extraTo common.Address, extraAmount cosmos.Uint) ([]byte, error)
}

type xrpMemoTypeOnlySigner interface {
	SignTxWithMemoTypeOnly(tx SimTx, memoType string) ([]byte, error)
}

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

	// Resolve the active Asgard vault signer for this chain's signer type
	// (secp256k1 vs ed25519) and ensure it matches NodeUsers[0].
	// On mocknet-cluster this is expected to fail (TSS vault).
	asgards, err := thornode.GetVaults()
	if err != nil || len(asgards) == 0 {
		return failSetup(fmt.Errorf("failed to get asgard vaults: %w", err))
	}
	asgardVault := asgards[0]
	if asgardVault.PubKey == nil {
		return failSetup(fmt.Errorf("active asgard vault pubkey is nil"))
	}
	asgardSignerPubKey := *asgardVault.PubKey
	if a.chain.GetSigningAlgo() == common.SigningAlgoEd25519 {
		if asgardVault.PubKeyEddsa == nil {
			a.Log().Info().
				Stringer("chain", a.chain).
				Msg("skipping actor: active Asgard vault has no eddsa signer pubkey")
			releaseSetupLocks()
			return OpResult{Finish: true}
		}
		asgardSignerPubKey = *asgardVault.PubKeyEddsa
	}
	if nodeUser.PubKey(a.chain).String() != asgardSignerPubKey {
		a.Log().Info().
			Str("asgard_signer_pubkey", asgardSignerPubKey).
			Stringer("node_user_pubkey", nodeUser.PubKey(a.chain)).
			Stringer("chain", a.chain).
			Msg("skipping actor: nodeUser does not match active Asgard signer pubkey")
		releaseSetupLocks()
		return OpResult{Finish: true}
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

	a.nodeVaultPubKey, err = common.NewPubKey(asgardSignerPubKey)
	if err != nil {
		return failSetup(fmt.Errorf("failed to parse active Asgard signer pubkey for chain %s: %w", a.chain, err))
	}

	a.Log().Info().Msg("node pubkey matches active Asgard signer pubkey, insolvency test can proceed")

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

// stealFunds creates the accounting/wallet delta that should trigger solvency halt.
// The exact transaction type is chain-specific, but each path is intended to produce
// a temporary discrepancy between observed vault accounting and real wallet balance.
func (a *InsolvencyActor) stealFunds(config *OpConfig) OpResult {
	a.Log().Info().Msg("attempting to steal funds from Asgard vault to create insolvency")

	nodeBalance, err := a.getNodeBalance()
	if err != nil {
		return OpResult{
			Error:  fmt.Errorf("failed to get asgard node balance for chain %s: %w", a.chain, err),
			Finish: true,
		}
	}

	if nodeBalance.IsZero() {
		return OpResult{
			Error:  fmt.Errorf("asgard node %s has no %s balance to steal", a.nodeL1Address, a.asset),
			Finish: true,
		}
	}

	// TRON-specific target: keep insolvency delta as low as possible while still
	// crossing the configured solvency gap threshold. This is required for TRON
	// so scanner-blind restore does not need an excessive number of txs.
	if a.chain.Equals(common.TRONChain) {
		targetAmount, permittedGapRune, expectedGapRune, err := a.getTRONMinimalStealAmount(nodeBalance)
		if err != nil {
			return OpResult{
				Error:  fmt.Errorf("failed to compute minimal TRON steal target: %w", err),
				Finish: true,
			}
		}

		a.stolenAmount = targetAmount
		a.Log().Info().
			Stringer("node_balance", nodeBalance).
			Stringer("target_stolen_amount", targetAmount).
			Stringer("permitted_gap_in_rune", permittedGapRune).
			Stringer("expected_gap_in_rune", expectedGapRune).
			Msg("using minimal TRON steal target for insolvency trigger")
	} else {
		// Default target: meaningful but non-exhaustive delta so restore paths remain feasible.
		a.stolenAmount = nodeBalance.QuoUint64(10)
	}

	if a.stolenAmount.IsZero() {
		return OpResult{
			Error:  fmt.Errorf("calculated stolen amount is zero, node balance %s too small", nodeBalance),
			Finish: true,
		}
	}

	var txid string

	switch {
	case a.chain.IsUTXO():
		// CONSOLIDATE memo is used by simulation plumbing for UTXO mismatch scenarios.
		toAddress := a.userAccount.Address(a.chain)
		txid, err = Send(a.Log(), a.nodeUser, toAddress, a.asset, "CONSOLIDATE", a.stolenAmount)
		if err != nil {
			return OpResult{
				Error:  fmt.Errorf("failed to execute steal transaction: %w", err),
				Finish: true,
			}
		}
		a.Log().Info().
			Str("txid", txid).
			Stringer("stolen_amount", a.stolenAmount).
			Str("balances", a.getBalancesStr()).
			Int64("chain_height", a.getHeight()).
			Msg("broadcasted UTXO steal transaction")
		return OpResult{Continue: true}
	case a.chain.IsEVM(),
		a.chain.Equals(common.GAIAChain):
		// EVM-like steal path: plain vault->user transfer with empty memo.
		toAddress := a.userAccount.Address(a.chain)
		txid, err = Send(a.Log(), a.nodeUser, toAddress, a.asset, "", a.stolenAmount)
		if err != nil {
			return OpResult{
				Error:  fmt.Errorf("failed to execute send-steal transaction: %w", err),
				Finish: true,
			}
		}
		a.Log().Info().
			Str("txid", txid).
			Stringer("stolen_amount", a.stolenAmount).
			Str("balances", a.getBalancesStr()).
			Int64("chain_height", a.getHeight()).
			Msg("broadcasted send-steal transaction")
		return OpResult{Continue: true}
	case a.chain.Equals(common.SOLChain),
		a.chain.Equals(common.TRONChain),
		a.chain.Equals(common.XRPChain):
		// Fee-aware steal for SOL/TRON/XRP:
		// track the real Asgard wallet delta (amount + network fee) so restore
		// can close the exact solvency gap without leaving fee residue.
		toAddress := a.userAccount.Address(a.chain)
		targetAmount := a.stolenAmount
		startBalance, err := a.getNodeBalance()
		if err != nil {
			return OpResult{
				Error:  fmt.Errorf("failed to get starting node balance for %s steal: %w", a.chain, err),
				Finish: true,
			}
		}

		txid, err = Send(a.Log(), a.nodeUser, toAddress, a.asset, "", targetAmount)
		if err != nil {
			return OpResult{
				Error:  fmt.Errorf("failed to execute %s send-steal transaction: %w", a.chain, err),
				Finish: true,
			}
		}

		actualDelta, err := a.getBalanceDelta(startBalance)
		if err != nil {
			return OpResult{
				Error:  fmt.Errorf("failed to compute %s steal delta after tx %s: %w", a.chain, txid, err),
				Finish: true,
			}
		}
		a.stolenAmount = actualDelta

		a.Log().Info().
			Str("txid", txid).
			Stringer("target_stolen_amount", targetAmount).
			Stringer("stolen_amount", a.stolenAmount).
			Str("balances", a.getBalancesStr()).
			Int64("chain_height", a.getHeight()).
			Msg("broadcasted fee-aware send-steal transaction")
		return OpResult{Continue: true}
	default:
		a.Log().Info().
			Stringer("chain", a.chain).
			Msg("skipping actor: unsupported chain for insolvency steal path")
		// Release acquired accounts before finishing early.
		return a.cleanup(config)
	}
}

// getTRONMinimalStealAmount computes a TRON steal amount that is just above the
// configured solvency gap threshold, to minimize scanner-blind restore tx count.
func (a *InsolvencyActor) getTRONMinimalStealAmount(nodeBalance cosmos.Uint) (cosmos.Uint, cosmos.Uint, cosmos.Uint, error) {
	if !a.asset.Chain.Equals(common.TRONChain) {
		return cosmos.ZeroUint(), cosmos.ZeroUint(), cosmos.ZeroUint(), fmt.Errorf("getTRONMinimalStealAmount only supports TRON, got %s", a.asset.Chain)
	}

	pool, err := thornode.GetPool(a.asset.GetLayer1Asset())
	if err != nil {
		return cosmos.ZeroUint(), cosmos.ZeroUint(), cosmos.ZeroUint(), fmt.Errorf("failed to get pool for %s: %w", a.asset, err)
	}

	runeDepth := cosmos.NewUintFromString(pool.BalanceRune)
	assetDepth := cosmos.NewUintFromString(pool.BalanceAsset)
	if runeDepth.IsZero() || assetDepth.IsZero() {
		return cosmos.ZeroUint(), cosmos.ZeroUint(), cosmos.ZeroUint(), fmt.Errorf("invalid pool depth for %s: rune=%s asset=%s", a.asset, runeDepth, assetDepth)
	}

	// Mimir values are stored in 1e8 notation:
	// - PermittedSolvencyGapUSD: USD value (1e8)
	// - DollarsPerRune:          USD per RUNE (1e8)
	permittedGapUSD, err := thornode.GetMimir("PermittedSolvencyGapUSD")
	if err != nil {
		return cosmos.ZeroUint(), cosmos.ZeroUint(), cosmos.ZeroUint(), fmt.Errorf("failed to get PermittedSolvencyGapUSD: %w", err)
	}
	if permittedGapUSD <= 0 {
		return cosmos.ZeroUint(), cosmos.ZeroUint(), cosmos.ZeroUint(), fmt.Errorf("invalid PermittedSolvencyGapUSD value: %d", permittedGapUSD)
	}

	dollarsPerRune, err := thornode.GetMimir("DollarsPerRune")
	if err != nil {
		return cosmos.ZeroUint(), cosmos.ZeroUint(), cosmos.ZeroUint(), fmt.Errorf("failed to get DollarsPerRune: %w", err)
	}
	if dollarsPerRune <= 0 {
		return cosmos.ZeroUint(), cosmos.ZeroUint(), cosmos.ZeroUint(), fmt.Errorf("invalid DollarsPerRune value: %d", dollarsPerRune)
	}

	// Match thornode's solvency threshold conversion:
	// permittedGapRune ~= permittedGapUSD * 1e8 / dollarsPerRune
	permittedGapRune := cosmos.NewUint(uint64(permittedGapUSD)).
		MulUint64(common.One).
		Quo(cosmos.NewUint(uint64(dollarsPerRune)))
	if permittedGapRune.IsZero() {
		permittedGapRune = cosmos.OneUint()
	}

	// TRON tx amounts are signed in 1e6 units while simulation tracks 1e8.
	tronBaseUnit := cosmos.NewUint(100)

	// Choose requiredStealAmount such that
	// round(requiredStealAmount * runeDepth / assetDepth) > permittedGapRune.
	// Conservatively target unrounded value >= permittedGapRune+1, then ceil-divide.
	requiredGapRune := permittedGapRune.Add(cosmos.OneUint())
	numerator := requiredGapRune.Mul(assetDepth)
	requiredStealAmount := numerator.Add(runeDepth.Sub(cosmos.OneUint())).Quo(runeDepth) // ceil(numerator / runeDepth)
	// Align up to TRON base unit (1 sun => 100 in 1e8 units).
	requiredStealAmount = requiredStealAmount.Add(tronBaseUnit.Sub(cosmos.OneUint())).Quo(tronBaseUnit).Mul(tronBaseUnit)
	if requiredStealAmount.IsZero() {
		requiredStealAmount = tronBaseUnit
	}
	if !requiredStealAmount.LT(nodeBalance) {
		return cosmos.ZeroUint(), permittedGapRune, cosmos.ZeroUint(), fmt.Errorf("computed TRON steal amount %s is not below node balance %s", requiredStealAmount, nodeBalance)
	}

	// Safety check for rounding/edge cases before executing steal tx.
	expectedGapRune := common.GetUncappedShare(runeDepth, assetDepth, requiredStealAmount)
	if !expectedGapRune.GT(permittedGapRune) {
		return cosmos.ZeroUint(), permittedGapRune, expectedGapRune, fmt.Errorf("computed TRON steal target did not exceed permitted gap (expected=%s permitted=%s)", expectedGapRune, permittedGapRune)
	}

	return requiredStealAmount, permittedGapRune, expectedGapRune, nil
}

// restoreFunds attempts to close the induced insolvency gap using chain-specific
// simulation plumbing. GAIA follows the stable EVM-like reconcile flow.
// SOL/TRON/XRP remain simulation-only scanner-blind restore paths.
func (a *InsolvencyActor) restoreFunds(config *OpConfig) OpResult {
	a.Log().Info().Msg("attempting to restore solvency after induced insolvency")

	restoreAmount := a.stolenAmount
	switch {
	case a.chain.IsUTXO():
		// - For UTXO chains, user->Asgard with CONSOLIDATE is not observed as
		//   an inbound (sender is non-vault while memo is internal consolidate),
		//   so wallet balance is restored without changing vault accounting.
		txid, err := DepositL1(a.Log(), a.userClient, a.asset, "CONSOLIDATE", restoreAmount)
		if err != nil {
			return OpResult{
				Error:  fmt.Errorf("failed to execute restore transaction: %w", err),
				Finish: true,
			}
		}
		a.Log().Info().
			Str("txid", txid).
			Stringer("stolen_amount", a.stolenAmount).
			Stringer("restored_amount", restoreAmount).
			Str("restore_mode", "user_to_asgard_unobserved_consolidate").
			Int64("chain_height", a.getHeight()).
			Str("balances", a.getBalancesStr()).
			Msg("broadcasted restore transaction")
		return OpResult{Continue: true}
	case a.chain.IsEVM(),
		a.chain.Equals(common.GAIAChain):
		// EVM and GAIA restore:
		// 1) Use self-transfer reconciliation to trigger the NOOP path.
		// 2) Send DONATE inbound to raise wallet balance so auto-unhalt can resume
		//    scanner processing and observe the queued reconcile tx.
		txid, reconciledAmount, err := a.reconcileVaultGapBySelfTransfer(restoreAmount)
		if err != nil {
			return OpResult{
				Error:  fmt.Errorf("failed to execute restore transaction: %w", err),
				Finish: true,
			}
		}

		a.Log().Info().
			Str("txid", txid).
			Stringer("stolen_amount", a.stolenAmount).
			Stringer("restored_amount", reconciledAmount).
			Str("restore_mode", "self_transfer_reconcile").
			Int64("chain_height", a.getHeight()).
			Str("balances", a.getBalancesStr()).
			Msg("broadcasted restore transaction")

		donateMemo := fmt.Sprintf("DONATE:%s", a.asset)
		donateTxID, err := DepositL1(a.Log(), a.userClient, a.asset, donateMemo, reconciledAmount)
		if err != nil {
			return OpResult{
				Error:  fmt.Errorf("failed to execute donate restore transaction: %w", err),
				Finish: true,
			}
		}

		a.Log().Info().
			Str("txid", donateTxID).
			Stringer("donated_amount", reconciledAmount).
			Str("memo", donateMemo).
			Str("restore_mode", "user_to_asgard_donate_unhalt_bootstrap").
			Int64("chain_height", a.getHeight()).
			Str("balances", a.getBalancesStr()).
			Msg("broadcasted donate transaction after reconcile")

		return OpResult{Continue: true}
	case a.chain.Equals(common.TRONChain):
		// Simulation-only TRON restore trick:
		// split restore into sub-dust TRX inbounds. Scanner drops each tx by dust
		// filter, while Asgard wallet balance still increases.
		txids, restoredAmount, err := a.restoreSolvencyTRONScannerBlindDust(restoreAmount)
		if err != nil {
			return OpResult{
				Error:  fmt.Errorf("failed to execute restore transaction: %w", err),
				Finish: true,
			}
		}

		lastTxID := ""
		if len(txids) > 0 {
			lastTxID = txids[len(txids)-1]
		}
		a.Log().Info().
			Str("txid", lastTxID).
			Int("tx_count", len(txids)).
			Stringer("stolen_amount", a.stolenAmount).
			Stringer("restored_amount", restoredAmount).
			Str("restore_mode", "user_to_asgard_scanner_blind_sub_dust").
			Int64("chain_height", a.getHeight()).
			Str("balances", a.getBalancesStr()).
			Msg("broadcasted restore transaction batch")
		return OpResult{Continue: true}
	case a.chain.Equals(common.XRPChain):
		// Simulation-only XRP restore trick:
		// send a valid Payment with memo fields that omit MemoData, so scanner
		// parsing drops it while Asgard wallet balance still increases.
		txid, err := a.restoreSolvencyXRPScannerBlind(restoreAmount)
		if err != nil {
			return OpResult{
				Error:  fmt.Errorf("failed to execute restore transaction: %w", err),
				Finish: true,
			}
		}
		a.Log().Info().
			Str("txid", txid).
			Stringer("stolen_amount", a.stolenAmount).
			Stringer("restored_amount", restoreAmount).
			Str("restore_mode", "user_to_asgard_scanner_blind_payment").
			Int64("chain_height", a.getHeight()).
			Str("balances", a.getBalancesStr()).
			Msg("broadcasted restore transaction")
		return OpResult{Continue: true}
	case a.chain.Equals(common.SOLChain):
		// Simulation-only SOL restore trick:
		// Build a valid inbound tx with two transfer instructions. SOL scanner
		// rejects multi-transfer txs, so wallet gets credited while vault
		// accounting remains unchanged.
		txid, extraTo, extraAmount, err := a.restoreSolvencySOLScannerBlind(config, restoreAmount)
		if err != nil {
			return OpResult{
				Error:  fmt.Errorf("failed to execute restore transaction: %w", err),
				Finish: true,
			}
		}
		a.Log().Info().
			Str("txid", txid).
			Stringer("stolen_amount", a.stolenAmount).
			Stringer("restored_amount", restoreAmount).
			Stringer("extra_to", extraTo).
			Stringer("extra_amount", extraAmount).
			Str("restore_mode", "user_to_asgard_scanner_blind_noop").
			Int64("chain_height", a.getHeight()).
			Str("balances", a.getBalancesStr()).
			Msg("broadcasted restore transaction")
		return OpResult{Continue: true}
	default:
		return OpResult{
			Error:  fmt.Errorf("unsupported chain type: %s", a.chain),
			Finish: true,
		}
	}
}

// restoreSolvencyTRONScannerBlindDust submits a batch of TRX inbounds below
// TRON DustThreshold so scanner parsing drops them while wallet balance rises.
func (a *InsolvencyActor) restoreSolvencyTRONScannerBlindDust(amount cosmos.Uint) ([]string, cosmos.Uint, error) {
	if !a.asset.Chain.Equals(common.TRONChain) {
		return nil, cosmos.ZeroUint(), fmt.Errorf("restoreSolvencyTRONScannerBlindDust only supports TRON, got %s", a.asset.Chain)
	}
	if amount.IsZero() {
		return nil, cosmos.ZeroUint(), fmt.Errorf("restore amount is zero")
	}

	// TRON tx amounts are signed in 1e6 units while simulation tracks 1e8,
	// so keep all chunks aligned to 100.
	tronBaseUnit := cosmos.NewUint(100)
	dust := common.TRONChain.DustThreshold()
	if !dust.GT(tronBaseUnit) {
		return nil, cosmos.ZeroUint(), fmt.Errorf("invalid TRON dust threshold: %s", dust)
	}

	maxChunk := dust.Sub(tronBaseUnit)
	maxChunk = maxChunk.Quo(tronBaseUnit).Mul(tronBaseUnit)
	if maxChunk.IsZero() {
		return nil, cosmos.ZeroUint(), fmt.Errorf("invalid TRON max restore chunk")
	}
	// Each chunk is strictly below DustThreshold so scanner drops every inbound.

	// Ceil(amount/maxChunk) without floating point.
	txEstimate := amount.Quo(maxChunk)
	if txEstimate.Mul(maxChunk).LT(amount) {
		txEstimate = txEstimate.Add(cosmos.OneUint())
	}

	a.Log().Info().
		Stringer("restore_amount", amount).
		Stringer("chunk_max_amount", maxChunk).
		Stringer("estimated_tx_count", txEstimate).
		Msg("starting TRON scanner-blind restore batch")

	remaining := amount
	restored := cosmos.ZeroUint()
	txids := make([]string, 0)
	sent := cosmos.ZeroUint()
	// Keep logs readable on large batches: first few txs, then periodic updates.
	progressLogEvery := uint64(25)

	for remaining.GT(cosmos.ZeroUint()) {
		chunk := remaining
		if chunk.GT(maxChunk) {
			chunk = maxChunk
		}
		chunk = chunk.Quo(tronBaseUnit).Mul(tronBaseUnit)
		if chunk.IsZero() {
			return txids, restored, fmt.Errorf("remaining amount %s is below TRON base unit", remaining)
		}

		txid, err := DepositL1(a.Log(), a.userClient, a.asset, "", chunk)
		if err != nil {
			return txids, restored, err
		}

		txids = append(txids, txid)
		restored = restored.Add(chunk)
		remaining = remaining.Sub(chunk)
		sent = sent.Add(cosmos.OneUint())

		// Log early chunks and then periodic progress for visibility.
		shouldLog := sent.LTE(cosmos.NewUint(10))
		if !shouldLog {
			multiple := sent.QuoUint64(progressLogEvery).MulUint64(progressLogEvery)
			shouldLog = sent.Equal(multiple)
		}
		if !shouldLog && remaining.IsZero() {
			shouldLog = true
		}
		if shouldLog {
			// Remaining tx count is exact because chunk sizing is deterministic:
			// every tx uses maxChunk except the last one (ceil division).
			remainingTxEstimate := remaining.Quo(maxChunk)
			if remainingTxEstimate.Mul(maxChunk).LT(remaining) {
				remainingTxEstimate = remainingTxEstimate.Add(cosmos.OneUint())
			}
			a.Log().Info().
				Str("txid", txid).
				Stringer("restore_tx_index", sent).
				Stringer("restore_tx_total_estimate", txEstimate).
				Stringer("restore_tx_sent", sent).
				Stringer("restore_tx_total", txEstimate).
				Stringer("restore_tx_remaining", remainingTxEstimate).
				Stringer("chunk_amount", chunk).
				Stringer("restore_amount_sent", restored).
				Stringer("restore_amount_total", amount).
				Stringer("restore_amount_remaining", remaining).
				Stringer("remaining_amount", remaining).
				Msg("broadcasted TRON restore chunk")
		}
	}

	return txids, restored, nil
}

// restoreSolvencyXRPScannerBlind submits an XRP payment inbound that credits
// Asgard wallet while avoiding scanner accounting updates by omitting MemoData.
func (a *InsolvencyActor) restoreSolvencyXRPScannerBlind(amount cosmos.Uint) (string, error) {
	if amount.IsZero() {
		return "", fmt.Errorf("restore amount is zero")
	}
	return a.depositL1XRPScannerBlind("novault", amount)
}

// restoreSolvencySOLScannerBlind submits a SOL inbound that credits Asgard wallet
// while avoiding scanner accounting updates by using a multi-transfer tx shape.
func (a *InsolvencyActor) restoreSolvencySOLScannerBlind(config *OpConfig, amount cosmos.Uint) (string, common.Address, cosmos.Uint, error) {
	if amount.IsZero() {
		return "", common.NoAddress, cosmos.ZeroUint(), fmt.Errorf("restore amount is zero")
	}
	extraTo, err := a.getSOLDistinctUserRecipient(config)
	if err != nil {
		return "", common.NoAddress, cosmos.ZeroUint(), err
	}
	// Keep the second transfer tiny, it only exists to force transferCount > 1.
	extraAmount := cosmos.NewUint(1)

	txid, err := a.depositL1SOLScannerBlind("NOOP:novault", amount, extraTo, extraAmount)
	if err != nil {
		return "", common.NoAddress, cosmos.ZeroUint(), err
	}
	return txid, extraTo, extraAmount, nil
}

// depositL1SOLScannerBlind submits a SOL inbound tx with an extra transfer
// instruction so scanner-side single-transfer parsing ignores it.
func (a *InsolvencyActor) depositL1SOLScannerBlind(
	memo string,
	amount cosmos.Uint,
	extraTo common.Address,
	extraAmount cosmos.Uint,
) (string, error) {
	if !a.asset.Chain.Equals(common.SOLChain) {
		return "", fmt.Errorf("depositL1SOLScannerBlind only supports SOL, got %s", a.asset.Chain)
	}

	solClient, ok := a.userClient.(solanaExtraTransferSigner)
	if !ok {
		return "", fmt.Errorf("solana client does not support extra-transfer signing")
	}

	inboundAddr, _, err := thornode.GetInboundAddress(a.asset.Chain)
	if err != nil {
		a.Log().Error().Err(err).Msg("failed to get inbound address")
		return "", err
	}

	tx := SimTx{
		Chain:     a.asset.Chain,
		ToAddress: inboundAddr,
		Coin:      common.NewCoin(a.asset, amount),
		Memo:      memo,
	}

	signed, err := solClient.SignTxWithExtraTransfer(tx, extraTo, extraAmount)
	if err != nil {
		a.Log().Error().Err(err).Msg("failed to sign scanner-blind sol tx")
		return "", err
	}

	txid, err := a.userClient.BroadcastTx(signed)
	if err != nil {
		a.Log().Error().Err(err).Msg("failed to broadcast scanner-blind sol tx")
	}

	return txid, err
}

// depositL1XRPScannerBlind submits an XRP Payment with memo type only (no
// MemoData), so scanner parsing drops it while wallet balance still increases.
func (a *InsolvencyActor) depositL1XRPScannerBlind(memoType string, amount cosmos.Uint) (string, error) {
	if !a.asset.Chain.Equals(common.XRPChain) {
		return "", fmt.Errorf("depositL1XRPScannerBlind only supports XRP, got %s", a.asset.Chain)
	}

	xrpClient, ok := a.userClient.(xrpMemoTypeOnlySigner)
	if !ok {
		return "", fmt.Errorf("xrp client does not support memo-type-only signing")
	}

	inboundAddr, _, err := thornode.GetInboundAddress(a.asset.Chain)
	if err != nil {
		a.Log().Error().Err(err).Msg("failed to get inbound address")
		return "", err
	}

	tx := SimTx{
		Chain:     a.asset.Chain,
		ToAddress: inboundAddr,
		Coin:      common.NewCoin(a.asset, amount),
	}

	signed, err := xrpClient.SignTxWithMemoTypeOnly(tx, memoType)
	if err != nil {
		a.Log().Error().Err(err).Msg("failed to sign scanner-blind xrp tx")
		return "", err
	}

	txid, err := a.userClient.BroadcastTx(signed)
	if err != nil {
		a.Log().Error().Err(err).Msg("failed to broadcast scanner-blind xrp tx")
	}

	return txid, err
}

// getSOLDistinctUserRecipient returns any configured simulation user address that
// differs from the currently locked sender account.
func (a *InsolvencyActor) getSOLDistinctUserRecipient(config *OpConfig) (common.Address, error) {
	if config == nil {
		return common.NoAddress, fmt.Errorf("op config is nil")
	}
	sender := a.userAccount
	for _, u := range config.Users {
		if u == nil || u == sender {
			continue
		}
		return u.Address(common.SOLChain), nil
	}
	return common.NoAddress, fmt.Errorf("failed to find distinct SOL user recipient")
}

// reconcileVaultGapBySelfTransfer uses an Asgard self-transfer with NOOP memo to
// close the temporary accounting gap on EVM chains.
func (a *InsolvencyActor) reconcileVaultGapBySelfTransfer(amount cosmos.Uint) (string, cosmos.Uint, error) {
	if amount.IsZero() {
		return "", cosmos.ZeroUint(), fmt.Errorf("restore amount is zero")
	}

	// NOOP inbound memo - on observed tx out drives the inbound-memo slash path,
	// which reconciles vault accounting without inbound NOOP handler execution.
	memo := "NOOP:novault"

	txid, err := Send(a.Log(), a.nodeUser, a.nodeL1Address, a.asset, memo, amount)
	if err != nil {
		return "", cosmos.ZeroUint(), fmt.Errorf("failed self-transfer restore send: %w", err)
	}

	return txid, amount, nil
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
	asgards, err := thornode.GetVaults()
	if err != nil || len(asgards) == 0 {
		asgardStr = fmt.Sprintf("failed to get vaults: %v", err)
	} else {
		for _, coin := range asgards[0].Coins {
			asset, err := common.NewAsset(coin.Asset)
			if err != nil {
				continue
			}
			if a.asset.Equals(asset) {
				asgardStr = fmt.Sprintf("%s %s", coin.Amount, a.asset)
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
