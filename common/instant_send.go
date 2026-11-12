package common

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"

	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

const (
	DefaultInstantSendMinAmount uint64 = One / 100

	// InstantSendMaxPending bounds pending deposits per vault.
	InstantSendMaxPending = 128

	// DASH input references contain only a canonical transaction hash and output index.
	// The bound covers the inputs of a standard Dash transaction.
	InstantSendMaxInputs = 2500

	// InstantSendAnchorWindow is measured in agreed DASH blocks, not local time.
	InstantSendAnchorWindow int64 = 24
	InstantSendAnchorStep   int64 = 12
)

// InstantSendAnchor groups nearby agreed DASH heights without using height zero.
func InstantSendAnchor(height int64) int64 {
	if height <= 0 {
		return 0
	}
	return max(1, height-height%InstantSendAnchorStep)
}

// Copy keeps cached accounting state independent of its caller.
func (s InstantSendState) Copy() InstantSendState {
	s.Pending = slices.Clone(s.Pending)
	return s
}

// PendingAmount sums exposure without imposing a spend reservation.
func (s InstantSendState) PendingAmount() cosmos.Uint {
	amount := cosmos.ZeroUint()
	for _, entry := range s.Pending {
		amount = amount.Add(cosmos.NewUint(entry.Amount))
	}
	return amount
}

// SolvencyAllowance excuses only pending deposits that have not timed out.
// Overdue deposits still count toward PendingAmount and the admission limit.
func (s InstantSendState) SolvencyAllowance(overdue []TxID) cosmos.Uint {
	amount := cosmos.ZeroUint()
	for _, entry := range s.Pending {
		if !slices.Contains(overdue, entry.Id) {
			amount = amount.Add(cosmos.NewUint(entry.Amount))
		}
	}
	return amount
}

// Validate checks persisted state before admission or settlement.
func (s InstantSendState) Validate() error {
	if !s.Chain.Equals(DASHChain) {
		return errors.New("unsupported InstantSend chain")
	}
	if s.PubKey.IsEmpty() {
		return errors.New("empty InstantSend vault")
	}
	if len(s.Pending) > InstantSendMaxPending {
		return errors.New("too many pending InstantSend inbounds")
	}
	var previous string
	for _, pending := range s.Pending {
		id := pending.Id.String()
		if len(id) != 64 || id != strings.ToUpper(id) || pending.Id.IsBlank() {
			return errors.New("invalid pending InstantSend transaction ID")
		}
		_, err := hex.DecodeString(id)
		if err != nil {
			return fmt.Errorf("invalid pending InstantSend transaction ID: %w", err)
		}
		if id <= previous {
			return errors.New("pending InstantSend inbounds must be sorted and unique")
		}
		if pending.Amount == 0 {
			return errors.New("pending InstantSend amount must be positive")
		}
		if pending.Height <= 0 {
			return errors.New("invalid InstantSend credit height")
		}
		previous = id
	}
	return nil
}

// Equals separates early admission from ordinary block observations.
func (s *InstantSend) Equals(other *InstantSend) bool {
	return (s == nil) == (other == nil)
}

// Copy preserves optional admission metadata across durable handoffs.
func (s *InstantSend) Copy() *InstantSend {
	if s == nil {
		return nil
	}
	result := *s
	return &result
}

func (s InstantSendStatus) MinimumHeight(height int64) int64 {
	return max(s.MinHeight, 1, height-InstantSendAnchorWindow)
}

func ValidDashHash(hash string) bool {
	if len(hash) != 64 || hash != strings.ToUpper(hash) || hash == strings.Repeat("0", 64) {
		return false
	}
	_, err := hex.DecodeString(hash)
	return err == nil
}

func (ref OutputRef) Outpoint() string { return fmt.Sprintf("%s:%010d", ref.TxHash, ref.OutputIndex) }

func ValidateDashInputs(refs []OutputRef) error {
	if len(refs) == 0 || len(refs) > InstantSendMaxInputs {
		return errors.New("invalid DASH input count")
	}
	previous := ""
	for _, ref := range refs {
		if !ValidDashHash(ref.TxHash) || ref.KeyImage != "" || ref.SpendTxHash != "" {
			return errors.New("invalid DASH input")
		}
		key := ref.Outpoint()
		if key <= previous {
			return errors.New("DASH inputs must be sorted and unique")
		}
		previous = key
	}
	return nil
}

func DashInputsHash(base string, chain Chain, refs []OutputRef) string {
	if !chain.Equals(DASHChain) || len(refs) == 0 {
		return base
	}
	for _, ref := range refs {
		base += "|" + ref.Outpoint()
	}
	hash := sha256.Sum256([]byte(base))
	return hex.EncodeToString(hash[:])
}
