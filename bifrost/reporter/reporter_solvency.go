package reporter

import (
	"fmt"

	"github.com/rs/zerolog/log"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

type solvencyReporter struct {
	baseReporter
	lastSolvencyCheckHeight int64
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

// Collect gathers solvency messages for all Asgard vaults on this chain.
// It prefers reporting insolvent vaults, and when scanner health is degraded it can
// emit "all solvent" reports to allow auto-unhalt.
func (r *solvencyReporter) Collect(from triggeredFrom) (ReportBatch, error) {
	// report insolvent asgard vaults,
	// or else all if the chain is halted and all are solvent
	currentGasFee := r.client.GetGasEstimateForSolvency()
	asgardVaults, err := r.thorchainBridge.GetAsgards()
	msgBatch := make(ReportBatch, 0, len(asgardVaults))
	solventMsgBatch := make(ReportBatch, 0, len(asgardVaults))
	if err != nil {
		return nil, fmt.Errorf("fail to get asgards: %w", err)
	}

	var height int64
	height, err = r.getRelevantHeight(from)
	if err != nil {
		return nil, fmt.Errorf("fail to get relevant height for solvency report: %w", err)
	}

	checkedVaults := 0
	for _, asgard := range asgardVaults {
		var acct common.Account
		acct, err = r.client.GetAccount(asgard.PubKey, nil)
		if err != nil {
			r.logger.Err(err).Msg("fail to get account balance")
			continue
		}
		checkedVaults++

		var msg *types.MsgSolvency
		msg, err = r.createSolvencyMsg(asgard.PubKey, acct.Coins, height)
		if err != nil {
			r.logger.Err(err).Stringer("chain", r.client.GetChain()).Msg("fail to create solvency msg")
			continue
		}
		if r.isVaultSolvent(acct, asgard, currentGasFee) {
			solventMsgBatch = append(solventMsgBatch, msg) // Solvent-vault message
		} else {
			msgBatch = append(msgBatch, msg) // Insolvent-vault message
		}
	}

	// Only if the block scanner is unhealthy (e.g. solvency-halted) and all vaults are solvent,
	// report that all the vaults are solvent.
	// If there are any insolvent vaults, report only them.
	// Not reporting both solvent and insolvent vaults is to avoid noise (spam):
	// Reporting both could halt-and-unhalt SolvencyHalt in the same THOR block
	// (resetting its height), plus making it harder to know at a glance from solvency reports which vaults were insolvent.
	//
	// Note: only report "all solvent" when every vault was successfully checked,
	// to avoid clearing a solvency halt on partial data.
	solvent := false
	if !r.client.IsBlockScannerHealthy() && checkedVaults == len(asgardVaults) && len(solventMsgBatch) == len(asgardVaults) {
		msgBatch = solventMsgBatch
		solvent = true
	}

	for i := range msgBatch {
		msg, ok := msgBatch[i].(*types.MsgSolvency)
		if !ok {
			r.logger.Warn().Msgf("unexpected report type from solvency reporter: %T", msgBatch[i])
			continue
		}
		r.logger.Info().
			Stringer("asgard_solvency", msg).
			Bool("solvent", solvent).
			Msg("reporting solvency")
	}

	r.lastSolvencyCheckHeight = height

	return msgBatch, nil
}

// ShouldReport decides whether a solvency collection/report cycle should run for
// a given trigger source and height.
func (r *solvencyReporter) ShouldReport(from triggeredFrom, height int64) bool {
	if from == fromRunner && !r.shouldReportFromRunner() {
		return false
	}
	// when block scanner is not healthy, only report from runner
	isHealthy := r.client.IsBlockScannerHealthy()
	if !isHealthy && from == fromBlockScanner {
		return false
	}
	chainHeight, err := r.client.GetHeight()
	if err != nil {
		r.logger.Err(err).Msg("fail to get chain height")
		return false
	}
	if from == fromBlockScanner {
		if height <= 0 {
			height, err = r.client.GetBlockScannerHeight()
			if err != nil {
				r.logger.Err(err).Msg("fail to get fetch (blockscanner) height")
				return false
			}
		}
		// skip reporting solvency from block scanner if block is more than flexibility blocks from tip
		if chainHeight-height > r.client.GetConfig().BlockScanner.ObservationFlexibilityBlocks {
			return false
		}
	}
	if height <= 0 {
		var err error
		height, err = r.getRelevantHeight(from)
		if err != nil {
			r.logger.Err(err).Msg("fail to get relevant height for solvency report")
			return false
		}
	}
	if !r.shouldReportAtHeight(height) {
		return false
	}
	if r.requiresNonZeroGas() && r.client.GetGasEstimateForSolvency().IsZero() {
		// GAIA/NOBLE/XRP use observed fee as insolvency buffer and should not report
		// before scanner fee initialization to avoid false positives.
		return false
	}

	return true
}

// requiresNonZeroGas returns true for chains that must wait until fee/gas
// estimation is initialized before solvency checks are meaningful.
func (r *solvencyReporter) requiresNonZeroGas() bool {
	switch r.client.GetChain() {
	case common.GAIAChain, common.NOBLEChain, common.XRPChain:
		return true
	default:
		return false
	}
}

// shouldReportAtHeight applies per-chain solvency cadence rules.
// Some chains use fixed intervals while others rely on configured
// solvency_blocks and/or last report height.
func (r *solvencyReporter) shouldReportAtHeight(height int64) bool {
	chain := r.client.GetChain()
	solvencyBlocks := r.client.GetConfig().SolvencyBlocks

	switch chain {
	case common.ETHChain:
		return height%20 == 0
	case common.AVAXChain, common.BSCChain, common.BASEChain:
		if solvencyBlocks <= 0 {
			return false
		}
		return height%solvencyBlocks == 0
	case common.GAIAChain, common.NOBLEChain, common.TRONChain:
		return height%10 == 0
	case common.XRPChain:
		if solvencyBlocks <= 0 {
			return false
		}
		return height%solvencyBlocks == 0
	case common.SOLChain:
		if solvencyBlocks <= 0 {
			return false
		}
		lastSlotMultiple := r.lastSolvencyCheckHeight / solvencyBlocks
		return height > (lastSlotMultiple+1)*solvencyBlocks
	case common.DOGEChain, common.ZECChain:
		if height-r.lastSolvencyCheckHeight <= 1 {
			return false
		}
		return height%10 == 0
	case common.BTCChain, common.BCHChain:
		return height-r.lastSolvencyCheckHeight > 1
	case common.LTCChain:
		return height-r.lastSolvencyCheckHeight > 5 && height%5 == 0
	default:
		if solvencyBlocks <= 0 {
			return false
		}
		return height-r.lastSolvencyCheckHeight > solvencyBlocks
	}
}

// shouldReportFromRunner gates runner-triggered reporting by Mimir halt state.
// It reports only when chain halt or solvency halt conditions indicate the
// runner should actively attempt auto-unhalt checks.
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

	// when HaltHeight == 1 means admin halt the chain, no need to do solvency check
	// when Chain is not halted, the normal chain client will report solvency when it need to
	// But if SolvencyHalt<chain>Chain > 0 this means the chain is halted, and we need to report solvency here
	if haltHeight <= 1 && solvencyHaltHeight <= 0 {
		return false
	}

	// should report solvency from runner
	return true
}

// isVaultSolvent checks whether the chain-specific portion of the vault is solvent.
// Vaults contain assets for all chains, so filter to the caller's chain before comparing
// the account balances.
func (r *solvencyReporter) isVaultSolvent(account common.Account, vault types.Vault, currentGasFee cosmos.Uint) bool {
	chain := r.client.GetChain()

	// thornode allow 10x of MaxGas as the gap
	currentGasFee10x := currentGasFee.MulUint64(10)

	// Compare against vault expectations (treat absent wallet asset as zero)
	for _, asgardCoin := range vault.Coins {
		// Ignore assets from other chains
		if !asgardCoin.Asset.Chain.Equals(chain) {
			continue
		}

		walletAmt := cosmos.ZeroUint()
		walletCoin := account.Coins.GetCoin(asgardCoin.Asset)
		if walletCoin.IsEmpty() {
			r.logger.Info().Stringer("asset", asgardCoin.Asset).Stringer("amount", asgardCoin.Amount).Msg("asset exist in vault , but not in wallet, insolvent")
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
func (r *solvencyReporter) createSolvencyMsg(pubKey common.PubKey, coins common.Coins, height int64) (*types.MsgSolvency, error) {
	msg := r.thorchainBridge.GetSolvencyMsg(height, r.client.GetChain(), pubKey, coins)
	if msg == nil {
		return nil, fmt.Errorf("fail to create msg solvency")
	}
	return msg, nil
}
