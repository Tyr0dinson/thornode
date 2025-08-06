package keeperv1

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"gitlab.com/thorchain/thornode/common/cosmos"
	thorchaintypes "gitlab.com/thorchain/thornode/x/thorchain/types"
)

// MemolessStats represents statistics for the memoless protocol
type MemolessStats struct {
	TotalRegistered   int64 `json:"total_registered"`
	ActiveMemos       int64 `json:"active_memos"`
	ExpiredMemos      int64 `json:"expired_memos"`
	CollisionsHandled int64 `json:"collisions_handled"`
	LastCleanupHeight int64 `json:"last_cleanup_height"`
}

// Storage key for memoless statistics
var MemolessStatsKey = []byte("memoless_stats")

// GetMemolessStats retrieves current memoless protocol statistics
func (k KVStore) GetMemolessStats(ctx cosmos.Context) MemolessStats {
	store := ctx.KVStore(k.storeKey)
	bz := store.Get(MemolessStatsKey)
	
	if bz == nil {
		return MemolessStats{
			LastCleanupHeight: 0,
		}
	}

	var stats MemolessStats
	if err := json.Unmarshal(bz, &stats); err != nil {
		ctx.Logger().Error("Failed to unmarshal memoless stats", "error", err)
		return MemolessStats{
			LastCleanupHeight: 0,
		}
	}

	return stats
}

// SetMemolessStats updates memoless protocol statistics
func (k KVStore) SetMemolessStats(ctx cosmos.Context, stats MemolessStats) error {
	store := ctx.KVStore(k.storeKey)
	
	bz, err := json.Marshal(stats)
	if err != nil {
		return fmt.Errorf("failed to marshal memoless stats: %w", err)
	}

	store.Set(MemolessStatsKey, bz)
	return nil
}

// UpdateMemolessStats updates specific statistics
func (k KVStore) UpdateMemolessStats(ctx cosmos.Context, update func(*MemolessStats)) error {
	stats := k.GetMemolessStats(ctx)
	update(&stats)
	return k.SetMemolessStats(ctx, stats)
}

// IncrementRegistrations increments the total registered counter
func (k KVStore) IncrementRegistrations(ctx cosmos.Context) error {
	return k.UpdateMemolessStats(ctx, func(stats *MemolessStats) {
		stats.TotalRegistered++
		stats.ActiveMemos++
	})
}

// IncrementCollisions increments the collisions handled counter
func (k KVStore) IncrementCollisions(ctx cosmos.Context) error {
	return k.UpdateMemolessStats(ctx, func(stats *MemolessStats) {
		stats.CollisionsHandled++
	})
}

// UpdateLastCleanup updates the last cleanup height
func (k KVStore) UpdateLastCleanup(ctx cosmos.Context, height int64, expiredCount int64) error {
	return k.UpdateMemolessStats(ctx, func(stats *MemolessStats) {
		stats.LastCleanupHeight = height
		stats.ExpiredMemos += expiredCount
		stats.ActiveMemos -= expiredCount
	})
}

// GetActiveMemoCount returns the current count of active memos
func (k KVStore) GetActiveMemoCount(ctx cosmos.Context) int64 {
	stats := k.GetMemolessStats(ctx)
	return stats.ActiveMemos
}

// Utility functions for reference handling

// ParseReference safely parses a reference string to integer
func ParseReference(reference string) (int, error) {
	if len(reference) != 5 {
		return 0, fmt.Errorf("reference must be exactly 5 digits")
	}

	value, err := strconv.Atoi(reference)
	if err != nil {
		return 0, fmt.Errorf("reference must be numeric: %w", err)
	}

	if value < 10000 || value > 99999 {
		return 0, fmt.Errorf("reference must be in range 10000-99999")
	}

	return value, nil
}

// FormatReference formats an integer as a 5-digit reference
func FormatReference(value int) string {
	if value < 10000 || value > 99999 {
		// Return a safe default rather than panic
		return "10000"
	}
	return fmt.Sprintf("%05d", value)
}

// GetNextAvailableReference finds the next available reference starting from a base
func (k KVStore) GetNextAvailableReference(ctx cosmos.Context, baseReference string) (string, error) {
	baseValue, err := ParseReference(baseReference)
	if err != nil {
		return "", fmt.Errorf("invalid base reference: %w", err)
	}

	// Try up to 1000 sequential values
	for i := 0; i < 1000; i++ {
		candidate := ((baseValue + i - 10000) % 90000) + 10000
		candidateRef := FormatReference(candidate)
		
		if !k.HasRegisteredMemo(ctx, candidateRef) {
			return candidateRef, nil
		}
	}

	return "", fmt.Errorf("no available reference found near %s", baseReference)
}

// Memo content utilities

// ExtractMemoMetadata extracts metadata from memo content
func ExtractMemoMetadata(memo string) map[string]string {
	metadata := make(map[string]string)
	
	// Extract basic metadata
	metadata["length"] = fmt.Sprintf("%d", len(memo))
	metadata["lines"] = fmt.Sprintf("%d", strings.Count(memo, "\n")+1)
	
	// Check for common patterns
	if strings.Contains(strings.ToLower(memo), "swap") {
		metadata["type"] = "swap"
	} else if strings.Contains(strings.ToLower(memo), "add") {
		metadata["type"] = "add_liquidity"
	} else if strings.Contains(strings.ToLower(memo), "withdraw") {
		metadata["type"] = "withdraw"
	} else {
		metadata["type"] = "unknown"
	}

	// Check for URLs
	if strings.Contains(memo, "http://") || strings.Contains(memo, "https://") {
		metadata["contains_urls"] = "true"
	}

	return metadata
}

// SafeMemoSubstring extracts a safe substring of memo for logging
func SafeMemoSubstring(memo string, maxLength int) string {
	if len(memo) <= maxLength {
		return memo
	}
	
	// Truncate and add ellipsis
	return memo[:maxLength-3] + "..."
}

// Cleanup utilities

// EstimateCleanupWorkload estimates how many expired memos need cleanup
func (k KVStore) EstimateCleanupWorkload(ctx cosmos.Context, blockHeight int64) (int, error) {
	// This would iterate through the expiry index to count expired entries
	// For performance, we could maintain separate counters
	
	// Simple implementation - would need optimization for production
	count := 0
	iterator := k.GetRegisteredMemoIterator(ctx)
	defer iterator.Close()

	for ; iterator.Valid() && count < 1000; iterator.Next() {
		var memo thorchaintypes.RegisteredMemo
		if err := json.Unmarshal(iterator.Value(), &memo); err != nil {
			continue
		}

		if memo.ExpiresAt <= blockHeight {
			count++
		}
	}

	return count, nil
}

// ShouldPerformCleanup determines if cleanup should be performed
func (k KVStore) ShouldPerformCleanup(ctx cosmos.Context, cleanupInterval int64) bool {
	if cleanupInterval <= 0 {
		return false
	}

	stats := k.GetMemolessStats(ctx)
	currentHeight := ctx.BlockHeight()
	
	return (currentHeight - stats.LastCleanupHeight) >= cleanupInterval
}

// Validation utilities

// IsValidMemoType checks if memo represents a valid THORChain transaction type
func IsValidMemoType(memo string) bool {
	// Extract the first part (transaction type) from memo
	parts := strings.Split(memo, ":")
	if len(parts) == 0 {
		return false
	}

	// Check against known transaction types
	validTypes := map[string]bool{
		"SWAP": true, "=": true, "s": true,
		"ADD": true, "+": true,
		"WITHDRAW": true, "wd": true, "-": true,
		"DONATE": true, "d": true,
		"BOND": true, "UNBOND": true, "REBOND": true,
		"LEAVE": true, "RESERVE": true, "REFUND": true,
		"MIGRATE": true, "RAGNAROK": true, "NOOP": true,
		// Add other valid memo types as needed
	}

	memoType := strings.ToUpper(strings.TrimSpace(parts[0]))
	return validTypes[memoType]
}

// SecurityPattern represents a pattern that might indicate security issues
type SecurityPattern struct {
	Pattern     string
	Description string
	Severity    string // "low", "medium", "high"
}

// GetSecurityPatterns returns patterns to check for security issues
func GetSecurityPatterns() []SecurityPattern {
	return []SecurityPattern{
		{"<script", "Potential XSS attempt", "high"},
		{"javascript:", "JavaScript URL scheme", "high"},
		{"data:", "Data URL scheme", "medium"},
		{"vbscript:", "VBScript URL scheme", "high"},
		{"onload=", "HTML event handler", "medium"},
		{"onclick=", "HTML event handler", "medium"},
		{"\x00", "Null byte injection", "high"},
		{"../", "Path traversal attempt", "medium"},
		{"SELECT * FROM", "SQL injection attempt", "high"},
		{"UNION SELECT", "SQL injection attempt", "high"},
		{"DROP TABLE", "SQL injection attempt", "high"},
	}
}

// CheckSecurityPatterns checks memo content for security patterns
func CheckSecurityPatterns(memo string) []SecurityPattern {
	var found []SecurityPattern
	patterns := GetSecurityPatterns()
	lowerMemo := strings.ToLower(memo)

	for _, pattern := range patterns {
		if strings.Contains(lowerMemo, strings.ToLower(pattern.Pattern)) {
			found = append(found, pattern)
		}
	}

	return found
}

// Logging utilities

// LogMemoRegistration logs memo registration with appropriate detail level
func (k KVStore) LogMemoRegistration(ctx cosmos.Context, memo thorchaintypes.RegisteredMemo, reference string) {
	// Use safe substring to avoid logging sensitive data
	safeMemo := SafeMemoSubstring(memo.Memo, 50)
	
	ctx.Logger().Info("Memo registered",
		"reference", reference,
		"chain_id", memo.ChainID,
		"memo_length", len(memo.Memo),
		"memo_preview", safeMemo,
		"expires_at", memo.ExpiresAt,
		"registered_at", memo.RegisteredAt,
	)
}

// LogMemoUsage logs memo usage/lookup
func (k KVStore) LogMemoUsage(ctx cosmos.Context, reference string, chainID string) {
	ctx.Logger().Debug("Memo accessed",
		"reference", reference,
		"chain_id", chainID,
		"block_height", ctx.BlockHeight(),
	)
}

// LogCleanupOperation logs cleanup operations
func (k KVStore) LogCleanupOperation(ctx cosmos.Context, cleanedCount int, blockHeight int64) {
	if cleanedCount > 0 {
		ctx.Logger().Info("Memoless cleanup completed",
			"cleaned_memos", cleanedCount,
			"block_height", blockHeight,
		)
	}
}