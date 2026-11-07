package signer

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	chainclienttypes "gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/types"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/constants"
	mem "gitlab.com/thorchain/thornode/v3/x/thorchain/memo"
	ttypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

const (
	refundFundingTimeout              = 30 * time.Second
	refundFundingRetryBlocks    int64 = 1
	refundFundingMaxHeightHints       = 16
)

type refundFundingVoterReader interface {
	GetRefundFundingVoter(context.Context, string) (*ttypes.QueryObservedTxVoter, bool, error)
}

// validateRefundFunding deliberately follows the source voter rather than the
// payout chain: a refund can pay another asset after a failed swap. No positive
// result is retained across a keysign or a retry of already signed bytes.
func (s *Signer) validateRefundFunding(tx types.TxOutItem) error {
	semanticMemo := tx.GetMemo()
	parsed, err := mem.ParseMemo(common.LatestVersion, semanticMemo)
	if err != nil {
		if strings.EqualFold(strings.SplitN(semanticMemo, ":", 2)[0], "refund") {
			return fmt.Errorf("refund funding: invalid memo: %w", err)
		}
		return nil
	}
	if !parsed.IsType(mem.TxRefund) {
		return nil
	}
	if tx.InHash.IsEmpty() || !parsed.GetTxID().Equals(tx.InHash) {
		return fmt.Errorf("refund funding: memo and inbound hash disagree")
	}
	ctx, cancel := context.WithTimeout(context.Background(), refundFundingTimeout)
	defer cancel()
	var voter *ttypes.QueryObservedTxVoter
	var found bool
	if reader, ok := s.thorchainBridge.(refundFundingVoterReader); ok {
		voter, found, err = reader.GetRefundFundingVoter(ctx, tx.InHash.String())
	} else {
		voter, found, err = s.thorchainBridge.GetObservedTxVoterUncached(tx.InHash.String())
	}
	if err != nil {
		return fmt.Errorf("refund funding: fresh inbound voter: %w", err)
	}
	if !found || voter == nil || !voter.TxID.Equals(tx.InHash) {
		return fmt.Errorf("refund funding: source voter is unavailable or mismatched")
	}
	if voter.Reverted || voter.FinalisedHeight <= 0 {
		return fmt.Errorf("refund funding: source voter is reverted or not finalised")
	}
	if !refundActionMatches(voter.Actions, tx) {
		return fmt.Errorf("refund funding: source voter does not own this refund action")
	}
	// Protocol-generated recovery swaps have no external inbound observation.
	// Their actions-only voter is explicitly supported by the tx-details query.
	if len(voter.Txs) == 0 && voter.Height == 0 && voter.Tx.Tx.ID.IsEmpty() &&
		voter.Tx.Tx.Chain.IsEmpty() && voter.Tx.Tx.FromAddress.IsEmpty() &&
		voter.Tx.Tx.ToAddress.IsEmpty() && len(voter.Tx.Tx.Coins) == 0 &&
		voter.Tx.ObservedPubKey.IsEmpty() && voter.Tx.BlockHeight == 0 && voter.Tx.FinaliseHeight == 0 {
		return nil
	}
	if !voter.Tx.Tx.ID.Equals(tx.InHash) || voter.Height <= 0 {
		return fmt.Errorf("refund funding: source observation is unavailable or mismatched")
	}
	source := voter.Tx.Tx.Chain
	if source.IsEmpty() {
		return fmt.Errorf("refund funding: source chain is unavailable")
	}
	// This quick patch protects only EVM and UTXO sources. TRON is a separate rollout.
	if !(source.IsEVM() || source.IsUTXO()) {
		return nil
	}
	chain, err := s.getChain(source)
	if err != nil {
		return fmt.Errorf("refund funding: source client unavailable: %w", err)
	}
	validator, ok := chain.(chainclienttypes.RefundFundingValidator)
	if !ok {
		// A declared protected chain must not silently bypass its checker.
		return fmt.Errorf("refund funding: %s source validation is unavailable", source)
	}
	original := common.NewObservedTx(voter.Tx.Tx, voter.Tx.BlockHeight, voter.Tx.ObservedPubKey, voter.Tx.FinaliseHeight)
	if original.BlockHeight <= 0 || original.BlockHeight != original.FinaliseHeight ||
		original.Tx.FromAddress.IsEmpty() || original.Tx.ToAddress.IsEmpty() || original.Tx.Coins.IsEmpty() {
		return fmt.Errorf("refund funding: source observation is incomplete or not final")
	}
	if hinted, ok := validator.(chainclienttypes.RefundFundingHintValidator); ok {
		err = hinted.ValidateRefundFundingWithHints(ctx, original, refundFundingHeightHints(original, voter.Txs))
	} else {
		err = validator.ValidateRefundFunding(ctx, original)
	}
	if err != nil {
		return fmt.Errorf("refund funding: %s deposit %s: %w", source, tx.InHash, err)
	}
	return ctx.Err()
}

// Final votes carry a finalise height, not necessarily the mined height. Only
// matching non-final variants supply location hints; their memo/gas can differ
// after THORNode resolves a memo or a transaction is re-mined.
func refundFundingHeightHints(original common.ObservedTx, variants []ttypes.QueryObservedTx) []int64 {
	var hints []int64
	seen := make(map[int64]struct{})
	for _, variant := range variants {
		if variant.BlockHeight <= 0 || variant.BlockHeight >= variant.FinaliseHeight ||
			variant.FinaliseHeight != original.FinaliseHeight || !variant.Tx.Chain.Equals(original.Tx.Chain) ||
			!variant.ObservedPubKey.Equals(original.ObservedPubKey) ||
			!chainclienttypes.RefundFundingMatches(original, &types.TxInItem{
				Tx: variant.Tx.ID.String(), Sender: variant.Tx.FromAddress.String(),
				To: variant.Tx.ToAddress.String(), Coins: variant.Tx.Coins,
			}) {
			continue
		}
		if _, exists := seen[variant.BlockHeight]; exists {
			continue
		}
		seen[variant.BlockHeight] = struct{}{}
		hints = append(hints, variant.BlockHeight)
		if len(hints) == refundFundingMaxHeightHints {
			break
		}
	}
	return hints
}

func refundActionMatches(actions []ttypes.TxOutItem, tx types.TxOutItem) bool {
	for _, action := range actions {
		sameAddress := action.ToAddress == tx.ToAddress
		if tx.Chain.IsEVM() {
			sameAddress = strings.EqualFold(action.ToAddress.String(), tx.ToAddress.String())
		}
		if action.InHash.Equals(tx.InHash) && action.Chain.Equals(tx.Chain) && sameAddress &&
			strings.EqualFold(action.GetMemo(), tx.GetMemo()) && !tx.Coins.GetCoin(action.Coin.Asset).IsEmpty() {
			return true
		}
	}
	return false
}

// A distinct error identifies the first check, before SignTx can expose material.
// Post-signing failures must never yield based on the caller's stale item copy.
type refundFundingPreSignHold struct{ err error }

func (e *refundFundingPreSignHold) Error() string { return e.err.Error() }
func (e *refundFundingPreSignHold) Unwrap() error { return e.err }

func refundFundingUnexposed(item TxOutStoreItem) bool {
	return len(item.SignedTx) == 0 && len(item.Checkpoint) == 0 &&
		len(item.TxOutItem.Checkpoint) == 0 && !item.Round7Retry && item.Observation == nil
}

func (s *Signer) deferRefundFunding(item TxOutStoreItem) error {
	height, err := s.thorchainBridge.GetBlockHeight()
	if err != nil {
		return err
	}
	if height <= 0 || height > math.MaxInt64-refundFundingRetryBlocks {
		return fmt.Errorf("invalid refund retry height")
	}
	return s.storage.DeferRefundFunding(item.Key(), height+refundFundingRetryBlocks)
}

// Failed unsigned checks stay out of signing until the background check clears
// their marker. Expired attempts still enter the normal queue for its cleanup.
func (s *Signer) readyRefundFunding(items []TxOutStoreItem) []TxOutStoreItem {
	var height, period int64
	var policyErr error
	queried := false
	ready := items[:0]
	for _, item := range items {
		if item.RefundFundingRetryHeight > 0 && refundFundingUnexposed(item) {
			if !queried {
				height, period, policyErr = s.refundFundingRetryPolicy()
				queried = true
			}
			if policyErr != nil || !s.refundFundingExpired(item, height, period) {
				continue
			}
		}
		ready = append(ready, item)
	}
	return ready
}

func (s *Signer) refundFundingRetryPolicy() (int64, int64, error) {
	height, err := s.thorchainBridge.GetBlockHeight()
	if err != nil {
		return 0, 0, err
	}
	period, err := s.constantsProvider.GetInt64Value(height, constants.SigningTransactionPeriod)
	return height, period, err
}

// Match signAndBroadcast's expiry for an unexposed, non-terminal attempt. Cleanup
// stays on the signing queue; the background worker never signs or removes work.
func (s *Signer) refundFundingExpired(item TxOutStoreItem, height, period int64) bool {
	if item.TxOutItem.Chain.Equals(common.XMRChain) {
		period = ttypes.MaxTxOutSigningTransactionPeriod(period)
	}
	return height-period > item.Height-s.cfg.Signer.RescheduleBufferBlocks
}

// One signer-owned worker does serial sweeps. It owns no retry queue or cache:
// the existing persisted marker reconstructs all pending checks after restart.
// Stop waits for its active check before closing the signer store.
func (s *Signer) retryRefundFunding() {
	defer s.wg.Done()
	for !s.isStopped() {
		s.recheckRefundFunding()
		timer := time.NewTimer(constants.ThorchainBlockTime)
		select {
		case <-s.stopChan:
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func (s *Signer) recheckRefundFunding() {
	items := s.storage.List()
	var height, period int64
	queried := false
	for _, item := range items {
		if s.isStopped() {
			return
		}
		if item.RefundFundingRetryHeight <= 0 || !refundFundingUnexposed(item) {
			continue
		}
		if !queried {
			var err error
			height, period, err = s.refundFundingRetryPolicy()
			if err != nil {
				s.logger.Warn().Err(err).Msg("refund funding retry policy unavailable")
				return
			}
			queried = true
		}
		if height < item.RefundFundingRetryHeight || s.refundFundingExpired(item, height, period) {
			continue
		}
		retryHeight := int64(0)
		if err := s.validateRefundFunding(item.TxOutItem); err != nil {
			s.logger.Debug().Err(err).Msg("refund funding still unavailable")
			currentHeight, heightErr := s.thorchainBridge.GetBlockHeight()
			if heightErr != nil || currentHeight <= 0 || currentHeight > math.MaxInt64-refundFundingRetryBlocks {
				continue
			}
			height = currentHeight
			retryHeight = currentHeight + refundFundingRetryBlocks
		}
		if err := s.storage.UpdateRefundFundingRetry(item, retryHeight); err != nil {
			s.logger.Error().Err(err).Msg("fail to update refund funding retry")
		}
	}
}
