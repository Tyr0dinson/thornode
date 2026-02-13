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
// It does this:
//  1. Find the active Asgard vault and lock one node user + one normal user.
//  2. Create a temporary gap between "what THORChain thinks is in the vault" and
//     "what is really in the chain wallet" (this is the simulated insolvency).
//  3. Wait until Mimir key SolvencyHalt<Chain>Chain is set (> 0).
//  4. Keep checking for a few blocks to confirm the halt stays active.
//  5. Restore funds using a chain-specific method.
//  6. Wait until Mimir key SolvencyHalt<Chain>Chain returns to 0.
//  7. Release all locked users.
//
// The actor currently serves these chain categories:
//
// A) EVM chains
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
// C) GAIA / TRON / XRP / SOL
//   - Steal path:
//     Same as EVM steal path (Asgard -> user, empty memo).
//   - Restore path:
//     User deposits to Asgard with "NOOP:novault".
//
// Extra notes:
//   - stolenAmount target starts as 10% of current Asgard wallet balance.
type InsolvencyActor struct {
	Actor

	chain           common.Chain
	asset           common.Asset
	nodeClient      LiteChainClient
	userClient      LiteChainClient
	nodeUser        *User
	userAccount     *User
	nodeVaultPubKey common.PubKey
	nodeL1Address   common.Address
	stolenAmount    cosmos.Uint
	haltDuration    int64
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
		Msg("bump tick target")
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
				Msg("maybeBumpBlock: failed to send tick tx")
			break
		}
		sent++
	}
	a.Log().Info().
		Stringer("chain", a.chain).
		Int64("chain_height", height).
		Int64("tick_target", tickTarget).
		Int("tick_sent", sent).
		Msg("bumper progressed chain")
}

// NewInsolvencyActor builds the actor DAG for the full insolvency lifecycle test.
// The operations are ordered to induce an insolvency gap, verify halt behavior, then
// restore solvency and verify un-halt.
func NewInsolvencyActor(chain common.Chain, rng *rand.Rand) *Actor {
	a := &InsolvencyActor{
		Actor:        *NewActor(fmt.Sprintf("Insolvency-%s", chain), rng),
		chain:        chain,
		asset:        chain.GetGasAsset(),
		haltDuration: 3,
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

	// 7. Cleanup
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

	// Target a meaningful but non-exhaustive delta so restore paths remain feasible.
	a.stolenAmount = nodeBalance.QuoUint64(10)
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
		a.chain.Equals(common.GAIAChain),
		a.chain.Equals(common.TRONChain),
		a.chain.Equals(common.SOLChain):
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
	case a.chain.Equals(common.XRPChain):
		// XRP "steal" should track the real Asgard wallet delta (amount + network fee),
		// so restore can close the exact solvency gap.
		toAddress := a.userAccount.Address(a.chain)
		targetAmount := a.stolenAmount
		startBalance, err := a.getNodeBalance()
		if err != nil {
			return OpResult{
				Error:  fmt.Errorf("failed to get starting node balance for XRP steal: %w", err),
				Finish: true,
			}
		}

		txid, err = Send(a.Log(), a.nodeUser, toAddress, a.asset, "", targetAmount)
		if err != nil {
			return OpResult{
				Error:  fmt.Errorf("failed to execute XRP send-steal transaction: %w", err),
				Finish: true,
			}
		}

		actualDelta, err := a.getBalanceDeltaSince(startBalance)
		if err != nil {
			return OpResult{
				Error:  fmt.Errorf("failed to compute XRP steal delta after tx %s: %w", txid, err),
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
			Msg("broadcasted XRP send-steal transaction")
		return OpResult{Continue: true}
	default:
		a.Log().Info().
			Stringer("chain", a.chain).
			Msg("skipping actor: unsupported chain for insolvency steal path")
		// Release acquired accounts before finishing early.
		return a.cleanup(config)
	}
}

// restoreFunds attempts to close the induced insolvency gap using chain-specific
// simulation plumbing.
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
		// EVM & GAIA restore:
		// 1) Use self-transfer reconciliation to trigger the NOOP slash path.
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
	case a.chain.Equals(common.SOLChain),
		a.chain.Equals(common.TRONChain),
		a.chain.Equals(common.XRPChain):
		// SOL/TRON/XRP restore via normal inbound NOOP deposit.
		// Solana rejects self-transfer restore txs with "Account loaded twice".
		txid, err := DepositL1(a.Log(), a.userClient, a.asset, "NOOP:novault", restoreAmount)
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
			Str("restore_mode", "user_to_asgard_noop").
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
// SolvencyHalt<Chain>Chain stores a THORChain block height, so duration must be
// evaluated against THOR height (not external chain height).
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
	finished := mimir+a.haltDuration <= thorHeight

	a.Log().Info().
		Bool("finished", finished).
		Int64("mimir", mimir).
		Int64("waitBlocks", a.haltDuration).
		Str("balances", a.getBalancesStr()).
		Int64("thor_height", thorHeight).
		Int64("chain_height", a.getHeight()).
		Msg("insolvency duration check finished")

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

// getThorHeight returns latest THORChain block height. This is the same height
// domain used by SolvencyHalt<Chain>Chain mimir values.
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

// getBalanceDeltaSince returns the decrease between a starting Asgard balance and
// the current balance (including chain-specific fee effects).
func (a *InsolvencyActor) getBalanceDeltaSince(startBalance cosmos.Uint) (cosmos.Uint, error) {
	endBalance, err := a.getNodeBalance()
	if err != nil {
		return cosmos.ZeroUint(), fmt.Errorf("failed to get ending node balance for chain %s: %w", a.chain, err)
	}
	if !endBalance.LT(startBalance) {
		return cosmos.ZeroUint(), fmt.Errorf("no balance delta detected (start=%s end=%s)", startBalance, endBalance)
	}
	return startBalance.Sub(endBalance), nil
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
