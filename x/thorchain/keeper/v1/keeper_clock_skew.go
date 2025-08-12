package keeperv1

import (
	"fmt"
	"sort"
	"time"

	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

// CalculateConsensusTime calculates consensus time from multiple node timestamps
func (k KVStore) CalculateConsensusTime(ctx cosmos.Context, nodeTimestamps []time.Time) (time.Time, error) {
	if len(nodeTimestamps) == 0 {
		return time.Time{}, fmt.Errorf("no timestamps provided")
	}

	// Single timestamp case
	if len(nodeTimestamps) == 1 {
		return nodeTimestamps[0], nil
	}

	// Validate clock skew within tolerance
	err := k.validateClockSkew(nodeTimestamps)
	if err != nil {
		ctx.Logger().Error("Clock skew validation failed",
			"node_count", len(nodeTimestamps),
			"error", err)
		return time.Time{}, err
	}

	// Calculate median timestamp for consensus
	sortedTimes := make([]time.Time, len(nodeTimestamps))
	copy(sortedTimes, nodeTimestamps)

	sort.Slice(sortedTimes, func(i, j int) bool {
		return sortedTimes[i].Before(sortedTimes[j])
	})

	var consensusTime time.Time
	medianIndex := len(sortedTimes) / 2

	if len(sortedTimes)%2 == 0 {
		// Even number of timestamps - average the two middle values
		mid1 := sortedTimes[medianIndex-1]
		mid2 := sortedTimes[medianIndex]
		duration := mid2.Sub(mid1) / 2
		consensusTime = mid1.Add(duration)
	} else {
		// Odd number of timestamps - return middle value
		consensusTime = sortedTimes[medianIndex]
	}

	// Log consensus calculation for monitoring
	skew := sortedTimes[len(sortedTimes)-1].Sub(sortedTimes[0])
	ctx.Logger().Debug("Consensus time calculated",
		"node_count", len(nodeTimestamps),
		"clock_skew", skew,
		"consensus_time", consensusTime.Format(time.RFC3339))

	return consensusTime, nil
}

// validateClockSkew validates that clock skew is within acceptable tolerance
func (k KVStore) validateClockSkew(timestamps []time.Time) error {
	if len(timestamps) < 2 {
		return nil // Single timestamp, no skew to validate
	}

	// Find min and max timestamps
	var minTime, maxTime time.Time = timestamps[0], timestamps[0]
	for _, t := range timestamps[1:] {
		if t.Before(minTime) {
			minTime = t
		}
		if t.After(maxTime) {
			maxTime = t
		}
	}

	// Check if skew exceeds tolerance (±60 seconds)
	skew := maxTime.Sub(minTime)
	tolerance := k.getSkewTolerance()

	if skew > tolerance {
		return fmt.Errorf("excessive clock skew: %v (max allowed: %v)", skew, tolerance)
	}

	return nil
}

// CalculateExpiryWithSkew calculates effective TTL accounting for clock skew buffer
func (k KVStore) CalculateExpiryWithSkew(ctx cosmos.Context, registrationTime time.Time, ttl int64) (int64, error) {
	// Calculate effective TTL with clock skew buffer
	// Formula: TTL_effective = TTL_registered - 2δ - processing_buffer

	clockSkewBuffer := k.getClockSkewBuffer()   // 2 * 60 seconds tolerance
	processingBuffer := k.getProcessingBuffer() // Processing safety buffer
	totalBuffer := clockSkewBuffer + processingBuffer

	effectiveTTL := ttl - totalBuffer

	// Ensure effective TTL is positive
	if effectiveTTL <= 0 {
		return 0, fmt.Errorf("TTL %d too short after applying skew buffer %d", ttl, totalBuffer)
	}

	// Log effective TTL calculation for monitoring
	ctx.Logger().Debug("Effective TTL calculated",
		"original_ttl", ttl,
		"clock_skew_buffer", clockSkewBuffer,
		"processing_buffer", processingBuffer,
		"effective_ttl", effectiveTTL)

	return effectiveTTL, nil
}

// GetConsensusTimeWithFallback gets consensus time with fallback to current block time
func (k KVStore) GetConsensusTimeWithFallback(ctx cosmos.Context, nodeTimestamps []time.Time) time.Time {
	if len(nodeTimestamps) == 0 {
		// Fallback to current block time
		return ctx.BlockTime()
	}

	consensusTime, err := k.CalculateConsensusTime(ctx, nodeTimestamps)
	if err != nil {
		// Log error and fallback to current block time
		ctx.Logger().Warn("Failed to calculate consensus time, using block time",
			"error", err,
			"node_count", len(nodeTimestamps))
		return ctx.BlockTime()
	}

	return consensusTime
}

// Helper functions

// getClockSkewBuffer returns the clock skew buffer (2 * 60 seconds = 120)
func (k KVStore) getClockSkewBuffer() int64 {
	return int64(120) // 2 * 60 seconds for ±60 second tolerance
}

// getProcessingBuffer returns the processing buffer for safety margin
func (k KVStore) getProcessingBuffer() int64 {
	return int64(120) // 2 minutes processing buffer
}

// getSkewTolerance returns the maximum allowed clock skew between nodes
func (k KVStore) getSkewTolerance() time.Duration {
	return 120 * time.Second // ±60 seconds tolerance = 120 second spread
}

// ValidateNodeClockSync validates that node clocks are reasonably synchronized
func (k KVStore) ValidateNodeClockSync(ctx cosmos.Context, nodeTimestamps []time.Time) error {
	if len(nodeTimestamps) <= 1 {
		return nil // Cannot validate sync with 1 or 0 timestamps
	}

	// Check overall skew
	if err := k.validateClockSkew(nodeTimestamps); err != nil {
		return fmt.Errorf("node clock synchronization failed: %w", err)
	}

	// Check for outliers (nodes significantly out of sync)
	avgTime := k.calculateAverageTime(nodeTimestamps)
	outlierTolerance := 45 * time.Second // Allow 45 seconds from average

	for i, timestamp := range nodeTimestamps {
		diff := timestamp.Sub(avgTime)
		if diff < 0 {
			diff = -diff
		}

		if diff > outlierTolerance {
			ctx.Logger().Warn("Node timestamp outlier detected",
				"node_index", i,
				"deviation_seconds", diff.Seconds(),
				"tolerance_seconds", outlierTolerance.Seconds())
		}
	}

	return nil
}

// calculateAverageTime calculates the average of all timestamps
func (k KVStore) calculateAverageTime(timestamps []time.Time) time.Time {
	if len(timestamps) == 0 {
		return time.Time{}
	}

	var totalNanos int64
	for _, t := range timestamps {
		totalNanos += t.UnixNano()
	}

	avgNanos := totalNanos / int64(len(timestamps))
	return time.Unix(0, avgNanos)
}
