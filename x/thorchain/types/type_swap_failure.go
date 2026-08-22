package types

import (
	"strings"
	"unicode/utf8"
)

const (
	// MaxSwapFailureHistory bounds the diagnostic tail persisted for a swap.
	// FailedSwapCount remains the authoritative total after older entries drop.
	MaxSwapFailureHistory = 64

	// MaxSwapFailureReasonBytes bounds each persisted diagnostic. Swap handler
	// errors are diagnostic only and must not cause consensus state to grow
	// without bound.
	MaxSwapFailureReasonBytes = 256
)

// FailureCount returns the total number of failed attempts. Records written
// before FailedSwapCount was added fall back to the legacy history length.
func (m *SwapState) FailureCount() uint64 {
	return totalSwapFailureCount(m.FailedSwapCount, m.FailedSwaps)
}

// RecordFailure increments the authoritative total and retains only the most
// recent bounded diagnostics.
func (m *SwapState) RecordFailure(index uint64, reason string) {
	m.FailedSwapCount, m.FailedSwaps, m.FailedSwapReasons = recordSwapFailure(
		m.FailedSwapCount,
		m.FailedSwaps,
		m.FailedSwapReasons,
		index,
		reason,
	)
}

// CompactFailureHistory upgrades legacy accounting and bounds its diagnostic
// tail without recording a new failure.
func (m *SwapState) CompactFailureHistory() {
	m.FailedSwapCount = totalSwapFailureCount(m.FailedSwapCount, m.FailedSwaps)
	m.FailedSwaps, m.FailedSwapReasons = compactSwapFailureHistory(m.FailedSwaps, m.FailedSwapReasons)
}

// FailureCount returns the total number of failed legacy streaming attempts.
func (m *StreamingSwap) FailureCount() uint64 {
	return totalSwapFailureCount(m.FailedSwapCount, m.FailedSwaps)
}

// RecordFailure increments the authoritative total and retains only the most
// recent bounded diagnostics for a legacy streaming swap.
func (m *StreamingSwap) RecordFailure(index uint64, reason string) {
	m.FailedSwapCount, m.FailedSwaps, m.FailedSwapReasons = recordSwapFailure(
		m.FailedSwapCount,
		m.FailedSwaps,
		m.FailedSwapReasons,
		index,
		reason,
	)
}

// CompactFailureHistory upgrades legacy accounting and bounds its diagnostic
// tail without recording a new failure.
func (m *StreamingSwap) CompactFailureHistory() {
	m.FailedSwapCount = totalSwapFailureCount(m.FailedSwapCount, m.FailedSwaps)
	m.FailedSwaps, m.FailedSwapReasons = compactSwapFailureHistory(m.FailedSwaps, m.FailedSwapReasons)
}

func totalSwapFailureCount(stored uint64, retained []uint64) uint64 {
	legacyCount := uint64(len(retained))
	if stored < legacyCount {
		return legacyCount
	}
	return stored
}

func recordSwapFailure(stored uint64, failedSwaps []uint64, failedReasons []string, index uint64, reason string) (uint64, []uint64, []string) {
	stored = totalSwapFailureCount(stored, failedSwaps)
	if stored < ^uint64(0) {
		stored++
	}

	// Repair any malformed legacy mismatch before adding the new aligned pair.
	failedSwaps, failedReasons = compactSwapFailureHistory(failedSwaps, failedReasons)
	failedSwaps = append(failedSwaps, index)
	failedReasons = append(failedReasons, compactSwapFailureReason(reason))
	failedSwaps, failedReasons = compactSwapFailureHistory(failedSwaps, failedReasons)
	return stored, failedSwaps, failedReasons
}

func compactSwapFailureHistory(failedSwaps []uint64, failedReasons []string) ([]uint64, []string) {
	if len(failedSwaps) == len(failedReasons) && len(failedSwaps) <= MaxSwapFailureHistory {
		valid := true
		for _, reason := range failedReasons {
			if len(reason) > MaxSwapFailureReasonBytes || !utf8.ValidString(reason) {
				valid = false
				break
			}
		}
		if valid {
			return failedSwaps, failedReasons
		}
	}

	pairCount := len(failedSwaps)
	if len(failedReasons) < pairCount {
		pairCount = len(failedReasons)
	}
	if pairCount > MaxSwapFailureHistory {
		pairCount = MaxSwapFailureHistory
	}
	if pairCount == 0 {
		if failedSwaps == nil && failedReasons == nil {
			return nil, nil
		}
		return []uint64{}, []string{}
	}

	// A shared suffix keeps indexes and reasons aligned even if a malformed
	// legacy record contains different slice lengths. Always allocate fresh
	// tails so compaction releases oversized backing arrays and never mutates a
	// caller-owned slice.
	compactedSwaps := append([]uint64(nil), failedSwaps[len(failedSwaps)-pairCount:]...)
	compactedReasons := make([]string, pairCount)
	start := len(failedReasons) - pairCount
	for i := range compactedReasons {
		compactedReasons[i] = compactSwapFailureReason(failedReasons[start+i])
	}

	return compactedSwaps, compactedReasons
}

func compactSwapFailureReason(reason string) string {
	// Handler errors should already be valid UTF-8, but imported or legacy
	// state may not be. Normalize every value before applying the byte cap.
	reason = strings.ToValidUTF8(reason, "")
	if len(reason) > MaxSwapFailureReasonBytes {
		reason = reason[:MaxSwapFailureReasonBytes]
		for !utf8.ValidString(reason) {
			reason = reason[:len(reason)-1]
		}
	}

	// Clone releases a potentially much larger backing string.
	return strings.Clone(reason)
}
