package observer

import (
	"fmt"
	"time"

	"gitlab.com/thorchain/thornode/v3/common"
)

// The caller holds the item lock. Count uncommitted evidence and reserve all
// resulting wire items before copying this bucket. The wire cap is not a cap
// on an accumulated key. Buckets without uncommitted evidence use no wire items.
func copyStateSyncAttestations[T AttestableItem](state *AttestationState[T], remainingItems int) ([][]*common.Attestation, error) {
	if remainingItems < 0 || remainingItems > maxAttestationStateItemsPerTransfer {
		return nil, fmt.Errorf("local state exceeds transfer maximum %d wire items", maxAttestationStateItemsPerTransfer)
	}
	count := 0
	for _, att := range state.attestations {
		if att.committed {
			continue
		}
		count++
		if count > remainingItems*maxAttestationStateAttestationsPerItem {
			return nil, fmt.Errorf("local state exceeds transfer maximum %d wire items after splitting attestations", maxAttestationStateItemsPerTransfer)
		}
		if att.attestation == nil {
			return nil, fmt.Errorf("local state attestation is nil")
		}
	}
	if count == 0 {
		return nil, nil
	}
	// Allocate for exported entries only, not the accumulated committed ones.
	atts := make([]*common.Attestation, 0, count)
	for _, att := range state.attestations {
		if !att.committed {
			atts = append(atts, &common.Attestation{
				PubKey:    append([]byte(nil), att.attestation.PubKey...),
				Signature: append([]byte(nil), att.attestation.Signature...),
			})
		}
	}
	chunks := make([][]*common.Attestation, 0, (count+maxAttestationStateAttestationsPerItem-1)/maxAttestationStateAttestationsPerItem)
	for len(atts) > maxAttestationStateAttestationsPerItem {
		chunks = append(chunks, atts[:maxAttestationStateAttestationsPerItem:maxAttestationStateAttestationsPerItem])
		atts = atts[maxAttestationStateAttestationsPerItem:]
	}
	return append(chunks, atts), nil
}

// Rate-limit local export warnings per service, without retaining a key/peer
// map that grows with rejected snapshots. Transport failures keep their own logs.
func (s *AttestationGossip) stateExportError(err error) error {
	now := time.Now()
	s.stateRequestMu.Lock()
	warn := s.stateExportWarnAt.IsZero() || now.Sub(s.stateExportWarnAt) >= time.Minute
	if warn {
		s.stateExportWarnAt = now
	}
	s.stateRequestMu.Unlock()
	if warn {
		s.logger.Warn().Err(err).Msg("cannot export local attestation state")
	}
	return err
}

func attestationStateBatchItemCount(batch common.QuorumState) int {
	return len(batch.QuoTxs) + len(batch.QuoNetworkFees) + len(batch.QuoSolvencies) + len(batch.QuoErrataTxs) + len(batch.QuoPriceFeeds)
}

func validateAttestationStateTransferCount(current int, batch common.QuorumState) (int, error) {
	count := attestationStateBatchItemCount(batch)
	if current < 0 || current > maxAttestationStateItemsPerTransfer || count > maxAttestationStateItemsPerTransfer-current {
		return current, fmt.Errorf("state item count exceeds transfer maximum %d", maxAttestationStateItemsPerTransfer)
	}
	return current + count, nil
}

// Preflight the entire decoded batch before signature work or publication.
// Count checks precede nested iteration. The byte packer can legitimately put
// more than 100 items of one category in a frame, so use the transfer ceiling.
// This is structural validation only; handlers retain semantic/signature policy.
func validateAttestationStateBatch(batch common.QuorumState) error {
	if _, err := validateAttestationStateTransferCount(0, batch); err != nil {
		return err
	}
	check := func(name string, index int, atts []*common.Attestation) error {
		if len(atts) > maxAttestationStateAttestationsPerItem {
			return fmt.Errorf("%s[%d] attestations count %d exceeds maximum %d", name, index, len(atts), maxAttestationStateAttestationsPerItem)
		}
		for i, att := range atts {
			if att == nil {
				return fmt.Errorf("%s[%d] attestation[%d] is nil", name, index, i)
			}
		}
		return nil
	}
	for i, item := range batch.QuoTxs {
		if item == nil {
			return fmt.Errorf("transaction[%d] is nil", i)
		}
		if err := check("transaction", i, item.Attestations); err != nil {
			return err
		}
	}
	for i, item := range batch.QuoNetworkFees {
		if item == nil || item.NetworkFee == nil {
			return fmt.Errorf("network fee[%d] or payload is nil", i)
		}
		if err := check("network fee", i, item.Attestations); err != nil {
			return err
		}
	}
	for i, item := range batch.QuoSolvencies {
		if item == nil || item.Solvency == nil {
			return fmt.Errorf("solvency[%d] or payload is nil", i)
		}
		if err := check("solvency", i, item.Attestations); err != nil {
			return err
		}
	}
	for i, item := range batch.QuoErrataTxs {
		if item == nil || item.ErrataTx == nil {
			return fmt.Errorf("errata[%d] or payload is nil", i)
		}
		if err := check("errata", i, item.Attestations); err != nil {
			return err
		}
	}
	for i, item := range batch.QuoPriceFeeds {
		if item == nil || item.PriceFeed == nil {
			return fmt.Errorf("price feed[%d] or payload is nil", i)
		}
		if err := check("price feed", i, item.Attestations); err != nil {
			return err
		}
		for j, rate := range item.PriceFeed.Rates {
			if rate == nil {
				return fmt.Errorf("price feed[%d] rate[%d] is nil", i, j)
			}
		}
	}
	return nil
}
