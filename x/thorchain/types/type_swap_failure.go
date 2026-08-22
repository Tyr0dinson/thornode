package types

import "strings"

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

	failedSwaps = append(failedSwaps, index)
	failedReasons = append(failedReasons, compactSwapFailureReason(reason))
	failedSwaps, failedReasons = compactSwapFailureHistory(failedSwaps, failedReasons)
	return stored, failedSwaps, failedReasons
}

func compactSwapFailureHistory(failedSwaps []uint64, failedReasons []string) ([]uint64, []string) {
	if len(failedSwaps) > MaxSwapFailureHistory {
		failedSwaps = append([]uint64(nil), failedSwaps[len(failedSwaps)-MaxSwapFailureHistory:]...)
	}

	if len(failedReasons) > MaxSwapFailureHistory {
		failedReasons = append([]string(nil), failedReasons[len(failedReasons)-MaxSwapFailureHistory:]...)
	}
	for i := range failedReasons {
		failedReasons[i] = compactSwapFailureReason(failedReasons[i])
	}

	return failedSwaps, failedReasons
}

func compactSwapFailureReason(reason string) string {
	if len(reason) <= MaxSwapFailureReasonBytes {
		return reason
	}

	// ToValidUTF8 removes a partial or invalid rune if the byte boundary cuts
	// through UTF-8. Clone releases the potentially much larger backing string.
	return strings.Clone(strings.ToValidUTF8(reason[:MaxSwapFailureReasonBytes], ""))
}
