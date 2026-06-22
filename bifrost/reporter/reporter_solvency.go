package reporter

import (
	"errors"
	"fmt"
	"math/big"
	"sync/atomic"

	"github.com/rs/zerolog/log"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

type solvencyReporter struct {
	baseReporter
	lastSolvencyCheckHeight atomic.Int64
}

// newSolvencyReporter creates a solvency reporter bound to a specific chain client
func newSolvencyReporter(client ChainClient, thorchainBridge ThorchainBridge) *solvencyReporter {
	logger := log.Logger.With().Str("module", "solvency_reporter").Str("chain", string(client.GetChain())).Logger()
	return &solvencyReporter{
		baseReporter: baseReporter{
			logger:          logger,
			client:          client,
			thorchainBridge: thorchainBridge,
		},
	}
}

// String returns a stable reporter identifier used in logs.
func (r *solvencyReporter) String() string {
	return "solvency_reporter"
}

func (r *solvencyReporter) getLastSolvencyCheckHeight() int64 {
	return r.lastSolvencyCheckHeight.Load()
}

func (r *solvencyReporter) setLastSolvencyCheckHeight(height int64) {
	r.lastSolvencyCheckHeight.Store(height)
}

// Collect gathers solvency messages for all Asgard vaults on this chain.
// It prefers reporting insolvent vaults, and only runner-triggered checks can
// emit "all solvent" reports to allow auto-unhalt when scanner health is degraded.
func (r *solvencyReporter) Collect(from triggeredFrom, height int64) (ReportBatch, error) {
	// report insolvent asgard vaults,
	// or else all if the chain is halted and all are solvent
	currentGasFee := r.client.GetGasEstimateForSolvency()
	asgardVaults, err := r.thorchainBridge.GetAsgards()
	if err != nil {
		return nil, fmt.Errorf("fail to get asgards: %w", err)
	}
	if height <= 0 {
		return nil, fmt.Errorf("invalid report height: %d", height)
	}

	// Build a chain-specific Asgard vault list before calling AlgoPubKey or GetAccount,
	// so unrelated vaults are skipped early
	chain := r.client.GetChain()
	chainVaults := make(types.Vaults, 0, len(asgardVaults))
	for _, asgard := range asgardVaults {
		if asgard.HasFundsForChain(chain) {
			chainVaults = append(chainVaults, asgard)
		}
	}

	msgs := make(ReportBatch, 0, len(chainVaults))
	solventMsgs := make(ReportBatch, 0, len(chainVaults))
	processedVaults := 0
	msgHeight := solvencyMsgHeight(chain, height)
	queryHeight := big.NewInt(height)
	for _, asgard := range chainVaults {
		acct, err := r.client.GetVaultAccount(asgard, queryHeight)
		if err != nil {
			if isSolvencyDeferred(err) {
				r.logger.Info().
					Err(err).
					Stringer("vault", asgard.PubKey).
					Int64("query_height", height).
					Msg("defer vault solvency while balance reconciliation is pending")
				continue
			}
			r.logger.Err(err).
				Stringer("vault", asgard.PubKey).
				Int64("query_height", height).
				Msg("fail to get vault-scoped account balance")
			continue
		}
		var msg *types.MsgSolvency
		msg, err = r.createSolvencyMsg(asgard.PubKey, acct.Coins, msgHeight)
		if err != nil {
			r.logger.Err(err).Stringer("chain", r.client.GetChain()).Msg("fail to create solvency msg")
			continue
		}
		processedVaults++
		if r.isVaultSolvent(acct, asgard, currentGasFee) {
			solventMsgs = append(solventMsgs, msg) // Solvent-vault message
		} else {
			msgs = append(msgs, msg) // Insolvent-vault message
		}
	}

	// Only if the runner is checking while the block scanner is unhealthy (e.g. solvency-halted)
	// and all vaults are solvent,
	// report that all the vaults are solvent.
	// If there are any insolvent vaults, report only them.
	// Not reporting both solvent and insolvent vaults is to avoid noise (spam):
	// Reporting both could halt-and-unhalt SolvencyHalt in the same THOR block
	// (resetting its height), plus making it harder to know at a glance from solvency reports which vaults were insolvent.
	//
	// Note: only report "all solvent" when every chain-specific vault was
	// successfully checked, to avoid clearing a solvency halt on partial data.
	// This matches the legacy behavior where only the auto-unhalt runner could
	// reach this path.
	solvent := false
	// Treat an empty chain-specific Asgard set as an incomplete check so transient
	// bridge/RPC glitches do not consume the next cadence window.
	allVaultsProcessed := len(chainVaults) > 0 && processedVaults == len(chainVaults)
	if from == fromRunner &&
		!r.client.IsBlockScannerHealthy() &&
		allVaultsProcessed &&
		len(solventMsgs) == processedVaults {
		msgs = solventMsgs
		solvent = true
	}

	for i := range msgs {
		msg, ok := msgs[i].(*types.MsgSolvency)
		if !ok {
			r.logger.Warn().Msgf("unexpected report type from solvency reporter: %T", msgs[i])
			continue
		}
		r.logger.Info().
			Stringer("asgard_solvency", msg).
			Bool("solvent", solvent).
			Msg("reporting solvency")
	}

	// Only advance cadence state when every vault was checked successfully.
	if allVaultsProcessed {
		r.setLastSolvencyCheckHeight(height)
	}

	return msgs, nil
}

// solvencyMsgHeight applies legacy chain-specific report height shaping while
// keeping cadence checks on the unmodified trigger height.
func solvencyMsgHeight(chain common.Chain, height int64) int64 {
	switch chain {
	case common.TRONChain:
		return height - tronSolvencyFinalityBlocks
	case common.SOLChain:
		return (height / solanaSolvencyHeightFloorStep) * solanaSolvencyHeightFloorStep
	default:
		return height
	}
}

// ShouldReport decides whether a solvency collection/report cycle should run for
// a given trigger source and raw (unnormalized) height.
func (r *solvencyReporter) ShouldReport(from triggeredFrom, height int64) bool {
	if height <= 0 {
		r.logger.Warn().
			Stringer("from", from).
			Int64("height", height).
			Msg("skip solvency report: invalid raw height")
		return false
	}
	if r.client.GetChain() == common.TRONChain && height <= tronSolvencyFinalityBlocks {
		// Safety addition versus the legacy per-chain code: do not emit non-positive
		// TRON solvency message heights on low/mocknet block heights.
		r.logger.Debug().
			Stringer("from", from).
			Int64("height", height).
			Msg("skip solvency report: TRON report height would be non-positive")
		return false
	}
	if from == fromRunner && !r.shouldReportFromRunner() {
		return false
	}
	// when block scanner is not healthy, only report from runner
	isHealthy := r.client.IsBlockScannerHealthy()
	if !isHealthy && from == fromBlockScanner {
		return false
	}
	if !r.shouldReportAtHeight(height) {
		return false
	}
	if r.requiresNonZeroGas() && r.client.GetGasEstimateForSolvency().IsZero() {
		// GAIA/NOBLE/XRP/SOL/XMR use observed fee as insolvency buffer and should not report
		// before scanner fee initialization.
		return false
	}
	if from == fromBlockScanner {
		chainHeight, err := r.client.GetHeight()
		if err != nil {
			r.logger.Err(err).Msg("fail to get chain height")
			return false
		}
		// skip reporting solvency from block scanner if block is more than flexibility blocks from tip
		if chainHeight-height > r.client.GetConfig().BlockScanner.ObservationFlexibilityBlocks {
			return false
		}
	}

	return true
}

// requiresNonZeroGas returns true for chains that must wait until fee/gas
// estimation is initialized before solvency checks are meaningful.
func (r *solvencyReporter) requiresNonZeroGas() bool {
	switch r.client.GetChain() {
	case common.GAIAChain, common.NOBLEChain, common.SOLChain, common.XRPChain, common.XMRChain:
		return true
	default:
		return false
	}
}

// shouldReportAtHeight applies per-chain solvency cadence rules.
// SOL keeps its legacy "strictly after the next boundary" behavior.
// All other chains report once they enter a later cadence bucket
// than the last successful report.
func (r *solvencyReporter) shouldReportAtHeight(height int64) bool {
	solvencyBlocks := r.solvencyBlocks()

	switch r.client.GetChain() {
	case common.SOLChain:
		lastSlotMultiple := r.getLastSolvencyCheckHeight() / solvencyBlocks
		return height > (lastSlotMultiple+1)*solvencyBlocks
	default:
		// return true once the current height has moved into a later solvency
		// cadence bucket than the last successful report height. This avoids
		// missing reports when a runner skips over the exact modulo height.
		return height/solvencyBlocks > r.getLastSolvencyCheckHeight()/solvencyBlocks
	}
}

// solvencyBlocks returns the effective cadence interval for the current chain.
// It prefers the configured `solvency_blocks` value when present and otherwise
// falls back to the legacy per-chain defaults.
func (r *solvencyReporter) solvencyBlocks() int64 {
	solvencyBlocks := r.client.GetConfig().SolvencyBlocks
	if solvencyBlocks > 0 {
		return solvencyBlocks
	}

	switch r.client.GetChain() {
	case common.ETHChain:
		return 20
	case common.SOLChain:
		return 50
	case common.GAIAChain, common.NOBLEChain, common.TRONChain, common.DOGEChain, common.ZECChain, common.XMRChain:
		return 10
	case common.BTCChain, common.BCHChain:
		return 2
	case common.LTCChain:
		return 5
	default:
		return 1 // default to 1 if not set, to allow reporting every block
	}
}

// shouldReportFromRunner gates runner-triggered reporting by the active Mimir halt state.
// The runner only takes over once THORChain considers a halt active, because the
// block scanner pauses on active chain/solvency halts. Manual Halt<Chain>Chain == 1
// is excluded to avoid auto-unhalt/reporting during an explicit operator halt.
func (r *solvencyReporter) shouldReportFromRunner() bool {
	var err error
	var haltHeight, solvencyHaltHeight int64

	// check whether the chain is halted via mimir or not
	mimir := fmt.Sprintf("Halt%sChain", r.client.GetChain())
	haltHeight, err = r.thorchainBridge.GetMimir(mimir)
	if err != nil {
		r.logger.Err(err).Msgf("fail to get mimir '%s'", mimir)
		return false
	}

	// check whether the chain is halted via solvency check
	mimir = fmt.Sprintf("SolvencyHalt%sChain", r.client.GetChain())
	solvencyHaltHeight, err = r.thorchainBridge.GetMimir(mimir)
	if err != nil {
		r.logger.Err(err).Msgf("fail to get mimir '%s'", mimir)
		return false
	}

	// Manual halt `== 1` is an explicit operator pause; do not auto-report from the runner.
	// If neither halt type is configured, the block scanner remains responsible for reporting.
	if haltHeight <= 1 && solvencyHaltHeight <= 0 {
		return false
	}

	thorHeight, err := r.thorchainBridge.GetBlockHeight()
	if err != nil {
		r.logger.Err(err).Msg("fail to get thorchain block height")
		return false
	}

	// Case 2: a solvency halt is active, so runner should triggered reporting
	if solvencyHaltHeight > 0 && thorHeight >= solvencyHaltHeight {
		return true
	}
	// Case 3: no solvency halt is active, but a regular chain halt is active
	if haltHeight > 1 && thorHeight >= haltHeight {
		return true
	}
	return false
}

// isVaultSolvent checks whether the chain-specific portion of the vault is solvent.
// Vaults contain assets for all chains, so filter to the caller's chain before comparing
// the account balances.
func (r *solvencyReporter) isVaultSolvent(account common.Account, vault types.Vault, currentGasFee cosmos.Uint) bool {
	chain := r.client.GetChain()

	// thornode allow 10x of MaxGas as the gap
	currentGasFee10x := currentGasFee.MulUint64(10)

	// Intentionally compare against vault expectations (treat absent wallet asset as zero).
	// Legacy chain-specific code iterated wallet coins, which could miss vault-required
	// assets that were absent in the wallet (effectively zero) and incorrectly appear
	// solvent. This common reporter iterates vault coins (chain-filtered), so a vault
	// asset missing from the wallet is treated as insolvent by design.
	//
	// This also intentionally keeps zero tolerance for non-gas assets: any shortfall is
	// insolvent, while gas assets alone retain the historical 10x max-gas allowance.
	for _, asgardCoin := range vault.Coins {
		// Ignore assets from other chains
		if !asgardCoin.Asset.Chain.Equals(chain) {
			continue
		}

		walletAmt := cosmos.ZeroUint()
		walletCoin := account.Coins.GetCoin(asgardCoin.Asset)
		if walletCoin.IsEmpty() {
			r.logger.Info().Stringer("asset", asgardCoin.Asset).Stringer("amount", asgardCoin.Amount).Msg("asset exists in vault but not in wallet, insolvent")
		} else {
			walletAmt = walletCoin.Amount
		}
		if walletAmt.GTE(asgardCoin.Amount) {
			continue
		}
		gap := asgardCoin.Amount.Sub(walletAmt)
		if asgardCoin.Asset.IsGasAsset() && gap.LT(currentGasFee10x) {
			continue
		}
		r.logger.Info().
			Stringer("chain", chain).
			Stringer("asset", asgardCoin.Asset).
			Stringer("asgard_amount", asgardCoin.Amount).
			Stringer("wallet_amount", walletAmt).
			Stringer("gap", gap).
			Msg("insolvency detected")
		return false
	}
	r.logger.Debug().
		Stringer("chain", chain).
		Stringer("vault", vault.Coins).
		Stringer("wallet", account.Coins).
		Stringer("currentGasFee10x", currentGasFee10x).
		Msg("vault is solvent")
	return true
}

// createSolvencyMsg builds a MsgSolvency report for a single vault.
// MsgSolvency.PubKey should remain the vault's canonical secp256k1 pubkey
// (not chain-specific algo keys like SOL ed25519), because the solvency message ID
// and voter identity are keyed by this field across nodes.
func (r *solvencyReporter) createSolvencyMsg(pubKey common.PubKey, coins common.Coins, height int64) (*types.MsgSolvency, error) {
	msg := r.thorchainBridge.GetSolvencyMsg(height, r.client.GetChain(), pubKey, coins)
	if msg == nil {
		return nil, fmt.Errorf("fail to create msg solvency")
	}
	return msg, nil
}

type solvencyDeferredError interface {
	error
	SolvencyDeferred() bool
}

func isSolvencyDeferred(err error) bool {
	var deferred solvencyDeferredError
	return errors.As(err, &deferred) && deferred.SolvencyDeferred()
}
