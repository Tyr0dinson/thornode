package observer

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// Simple BFT test to verify basic consensus functionality
func TestSimpleBFTConsensus(t *testing.T) {
	// Create a network of 7 validators (can tolerate 2 Byzantine)
	validators := make([]*MockValidator, 7)
	for i := range validators {
		validators[i] = &MockValidator{
			id:        i,
			byzantine: false,
			responses: make(map[string]*types.RegisteredMemo),
		}
	}

	// Setup honest validators with correct memo
	correctMemo := &types.RegisteredMemo{
		Reference:    "12345",
		Memo:         "SWAP:BTC.BTC",
		ChainId:      "BTC",
		RegisteredAt: 100,
		ExpiresAt:    144100,
		Status:       "active",
	}

	for i := range validators {
		validators[i].responses["12345"] = correctMemo
	}

	network := &MockBFTNetwork{
		validators:    validators,
		partitioned:   make(map[int]bool),
		latencyMap:    make(map[int]time.Duration),
		faultInjector: &FaultInjector{},
	}

	// Create Byzantine memo resolver
	resolver := &ByzantineMemoResolver{
		network:   network,
		threshold: len(validators)/2 + 1, // Byzantine fault threshold
		timeout:   time.Second * 5,
	}

	// Test memo resolution with all honest validators
	ctx := context.Background()
	resolvedMemo, err := resolver.ResolveMemo(ctx, "12345")

	require.NoError(t, err, "Expected successful memo resolution")
	require.NotNil(t, resolvedMemo)
	require.Equal(t, correctMemo.Memo, resolvedMemo.Memo)
	require.Equal(t, correctMemo.Status, resolvedMemo.Status)
}

// Test with one Byzantine validator
func TestSingleByzantineValidator(t *testing.T) {
	// Create a network of 7 validators (can tolerate 2 Byzantine)
	validators := make([]*MockValidator, 7)
	for i := range validators {
		validators[i] = &MockValidator{
			id:        i,
			byzantine: false,
			responses: make(map[string]*types.RegisteredMemo),
		}
	}

	// Make first validator Byzantine
	validators[0].byzantine = true
	validators[0].behavior = FalseMemoContent

	// Setup honest validators with correct memo
	correctMemo := &types.RegisteredMemo{
		Reference:    "54321",
		Memo:         "ADD:ETH.ETH:tthor1test",
		ChainId:      "ETH",
		RegisteredAt: 200,
		ExpiresAt:    144200,
		Status:       "active",
	}

	// Set correct memo for honest validators (index 1-6)
	for i := 1; i < len(validators); i++ {
		validators[i].responses["54321"] = correctMemo
	}

	network := &MockBFTNetwork{
		validators:    validators,
		partitioned:   make(map[int]bool),
		latencyMap:    make(map[int]time.Duration),
		faultInjector: &FaultInjector{},
	}

	resolver := &ByzantineMemoResolver{
		network:   network,
		threshold: len(validators)/2 + 1,
		timeout:   time.Second * 5,
	}

	// Should still succeed with 1 Byzantine validator out of 7
	ctx := context.Background()
	resolvedMemo, err := resolver.ResolveMemo(ctx, "54321")

	require.NoError(t, err, "Expected successful memo resolution despite single Byzantine validator")
	require.NotNil(t, resolvedMemo)
	require.Equal(t, correctMemo.Memo, resolvedMemo.Memo)
	require.Equal(t, correctMemo.Status, resolvedMemo.Status)
}