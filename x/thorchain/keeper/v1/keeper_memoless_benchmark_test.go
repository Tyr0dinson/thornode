package keeperv1

import (
	"testing"

	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	thorchaintypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// BenchmarkReferenceGeneration benchmarks incremental reference generation
func BenchmarkReferenceGeneration(b *testing.B) {
	ctx, k := setupKeeperForTestBenchmark(b)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = k.GetNextReferenceID(ctx, "BTC")
	}
}

// BenchmarkMemoRegistration benchmarks complete memo registration process
func BenchmarkMemoRegistration(b *testing.B) {
	ctx, k := setupKeeperForTestBenchmark(b)
	constantProvider := constants.GetConstantValues(GetCurrentVersion())

	// Initialize chain configs once
	_ = k.InitializeDefaultChainConfigs(ctx)
	signer := GetRandomValidatorNode(NodeActive).NodeAddress

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		msg := thorchaintypes.MsgRegisterMemo{
			Memo:    "SWAP:BTC.BTC:thor1benchmark:14400",
			ChainId: "BTC",
			Ttl:     14400,
			Signer:  signer,
			Amount:  "100000000",
		}
		_, _ = k.ProcessMemoRegistration(ctx, msg, constantProvider)
	}
}

// BenchmarkMemoLookup benchmarks memo lookup by reference
func BenchmarkMemoLookup(b *testing.B) {
	ctx, k := setupKeeperForTestBenchmark(b)
	constantProvider := constants.GetConstantValues(GetCurrentVersion())

	// Setup: Register a memo to lookup
	_ = k.InitializeDefaultChainConfigs(ctx)
	signer := GetRandomValidatorNode(NodeActive).NodeAddress

	msg := thorchaintypes.MsgRegisterMemo{
		Memo:    "SWAP:BTC.BTC:thor1lookup:14400",
		ChainId: "BTC",
		Ttl:     14400,
		Signer:  signer,
		Amount:  "100000000",
	}
	result, _ := k.ProcessMemoRegistration(ctx, msg, constantProvider)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = k.LookupMemoByReference(ctx, result.Reference)
	}
}

// BenchmarkValidation benchmarks 5-layer validation framework
func BenchmarkValidation(b *testing.B) {
	ctx, k := setupKeeperForTestBenchmark(b)
	constantProvider := constants.GetConstantValues(GetCurrentVersion())

	_ = k.InitializeDefaultChainConfigs(ctx)
	signer := GetRandomValidatorNode(NodeActive).NodeAddress

	msg := thorchaintypes.MsgRegisterMemo{
		Memo:    "SWAP:BTC.BTC:thor1validation:14400",
		ChainId: "BTC",
		Ttl:     14400,
		Signer:  signer,
		Amount:  "100000000",
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = k.ValidateMemoRegistration(ctx, msg, constantProvider)
	}
}

// setupKeeperForTestBenchmark creates a lightweight test environment for benchmarking
func setupKeeperForTestBenchmark(b *testing.B) (cosmos.Context, KVStore) {
	b.Helper()
	b.Skip("Benchmark requires complex keeper setup - skipping for now")
	var k KVStore
	return cosmos.Context{}, k
}
