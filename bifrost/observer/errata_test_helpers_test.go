package observer

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/syndtr/goleveldb/leveldb/util"

	"gitlab.com/thorchain/thornode/v3/common"
)

// handleErrataTxCommitted preserves the pre-terminal-outcome XMR event shape for
// tests which exercise delivery and retry behavior independently of THORChain's
// committed outcome.
func (o *Observer) handleErrataTxCommitted(errata common.ErrataTx) error {
	return o.handleErrataTxCommittedOutcome(
		errata,
		common.ErrataTxOutcome_ERRATA_TX_OUTCOME_UNSPECIFIED,
	)
}

// getErrataTombstones is a test-only full-store inspection helper. Production
// reconciliation deliberately scans only the XMR tombstone prefix.
func (s *ObserverStorage) getErrataTombstones() ([]errataTombstone, error) {
	iter := s.db.NewIterator(util.BytesPrefix([]byte(ErrataTombstoneKeyPrefix)), nil)
	defer iter.Release()
	result := make([]errataTombstone, 0)
	for iter.Next() {
		var tombstone errataTombstone
		if err := json.Unmarshal(iter.Value(), &tombstone); err != nil {
			return nil, fmt.Errorf("decode observer errata tombstone: %w", err)
		}
		if err := validateErrataTombstone(tombstone); err != nil {
			return nil, err
		}
		if string(iter.Key()) != errataTombstoneKey(tombstone.Chain, tombstone.TxID.String()) {
			return nil, errors.New("observer errata tombstone key does not match its payload")
		}
		result = append(result, tombstone)
	}
	if err := iter.Error(); err != nil {
		return nil, fmt.Errorf("iterate observer errata tombstones: %w", err)
	}
	return result, nil
}

func (s *ObserverStorage) getPendingErrata() ([]errataTombstone, error) {
	tombstones, err := s.getErrataTombstones()
	if err != nil {
		return nil, err
	}
	result := make([]errataTombstone, 0, len(tombstones))
	for _, tombstone := range tombstones {
		if !tombstone.Committed {
			result = append(result, tombstone)
		}
	}
	return result, nil
}
