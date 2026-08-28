package wasmgas

import (
	"math"
	"testing"

	storetypes "cosmossdk.io/store/types"
	wasmtypes "github.com/CosmWasm/wasmd/x/wasm/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"
)

func testContext() sdk.Context {
	return sdk.Context{}.
		WithGasMeter(NewInfiniteMeter()).
		WithBlockGasMeter(storetypes.NewInfiniteGasMeter())
}

func TestMeterInactiveIsUnbounded(t *testing.T) {
	ctx := Activate(testContext(), 0, 0)
	require.NotPanics(t, func() {
		ctx.GasMeter().ConsumeGas(1_000_000, "inactive")
	})
	require.Equal(t, uint64(1_000_000), ctx.GasMeter().GasConsumed())
}

func TestActivateCapsWholeTransactionOnce(t *testing.T) {
	ctx := Activate(testContext(), 100, 0)

	ctx.GasMeter().ConsumeGas(60, "first wasm call")
	ctx = Activate(ctx, 100, 0) // a nested call must not reset the budget
	ctx.GasMeter().ConsumeGas(40, "nested wasm call")

	require.Equal(t, uint64(100), ctx.GasMeter().GasConsumed())
	require.PanicsWithValue(t, storetypes.ErrorOutOfGas{Descriptor: "over cap"}, func() {
		ctx.GasMeter().ConsumeGas(1, "over cap")
	})
}

func TestActivateUsesSharedBlockBudget(t *testing.T) {
	ctx := testContext()
	ctx.BlockGasMeter().ConsumeGas(120, "previous transactions")
	ctx = Activate(ctx, 100, 150)

	require.Equal(t, uint64(30), ctx.GasMeter().Limit())
	ctx.GasMeter().ConsumeGas(30, "remaining block budget")
	require.PanicsWithValue(t, storetypes.ErrorOutOfGas{Descriptor: "block cap"}, func() {
		ctx.GasMeter().ConsumeGas(1, "block cap")
	})
	require.Equal(t, uint64(30), ctx.GasMeter().GasConsumedToLimit())
}

func TestTransactionsShareBlockBudget(t *testing.T) {
	blockMeter := storetypes.NewInfiniteGasMeter()

	first := sdk.Context{}.
		WithGasMeter(NewInfiniteMeter()).
		WithBlockGasMeter(blockMeter)
	first = Activate(first, 100, 150)
	first.GasMeter().ConsumeGas(100, "first transaction")
	blockMeter.ConsumeGas(first.GasMeter().GasConsumedToLimit(), "commit first transaction")

	second := sdk.Context{}.
		WithGasMeter(NewInfiniteMeter()).
		WithBlockGasMeter(blockMeter)
	second = Activate(second, 100, 150)
	require.Equal(t, uint64(50), second.GasMeter().Limit())
	require.Panics(t, func() {
		second.GasMeter().ConsumeGas(51, "second transaction")
	})
	blockMeter.ConsumeGas(second.GasMeter().GasConsumedToLimit(), "commit second transaction")
	require.Equal(t, uint64(150), blockMeter.GasConsumed())
}

func TestActivateCountsGasConsumedBeforeWasm(t *testing.T) {
	ctx := testContext()
	ctx.GasMeter().ConsumeGas(25, "ante")
	ctx = Activate(ctx, 100, 200)

	// The per-transaction Wasm allowance starts at activation, while the block
	// ceiling includes all gas already consumed by this transaction.
	require.Equal(t, uint64(125), ctx.GasMeter().Limit())
	ctx.GasMeter().ConsumeGas(100, "wasm")
	require.Panics(t, func() {
		ctx.GasMeter().ConsumeGas(1, "over tx cap")
	})
}

func TestActivateClampsLimitsToWasmVMConversionRange(t *testing.T) {
	require.Less(t, uint64(maxWasmSDKGas), uint64(math.MaxInt64))

	for _, limits := range [][2]int64{
		{math.MaxInt64, 0},
		{0, math.MaxInt64},
		{int64(maxWasmSDKGas + 1), int64(maxWasmSDKGas + 1)},
	} {
		ctx := Activate(testContext(), limits[0], limits[1])
		require.Equal(t, uint64(maxWasmSDKGas), ctx.GasMeter().Limit())
		require.NotPanics(t, func() {
			wasmtypes.NewDefaultWasmGasRegister().ToWasmVMGas(ctx.GasMeter().GasRemaining())
		})
	}
}
