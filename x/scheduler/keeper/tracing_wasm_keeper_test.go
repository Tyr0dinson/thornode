package keeper_test

import (
	"errors"
	"testing"
	"time"

	storetypes "github.com/cosmos/cosmos-sdk/store/v2/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"

	"gitlab.com/thorchain/thornode/v3/common/telemetry"
	"gitlab.com/thorchain/thornode/v3/x/scheduler/keeper"
)

type wasmExecutorFunc func(ctx sdk.Context, contractAddress, caller sdk.AccAddress, msg []byte, coins sdk.Coins) ([]byte, error)

func (f wasmExecutorFunc) Execute(ctx sdk.Context, contractAddress, caller sdk.AccAddress, msg []byte, coins sdk.Coins) ([]byte, error) {
	return f(ctx, contractAddress, caller, msg, coins)
}

type contractTrace struct {
	operation       string
	contractAddress string
	codeID          uint64
	payload         []byte
	outcome         string
	duration        time.Duration
}

type contractTracer struct {
	enabled        bool
	panicOnEnabled bool
	panicOnRecord  bool
	traces         []contractTrace
}

func (t *contractTracer) ContractTracingEnabled() bool {
	if t.panicOnEnabled {
		panic("telemetry probe failed")
	}
	return t.enabled
}

func (t *contractTracer) RecordContract(
	_ sdk.Context,
	operation, contractAddress string,
	codeID uint64,
	payload []byte,
	outcome string,
	duration time.Duration,
) {
	if t.panicOnRecord {
		panic("telemetry failed")
	}
	t.traces = append(t.traces, contractTrace{
		operation:       operation,
		contractAddress: contractAddress,
		codeID:          codeID,
		payload:         payload,
		outcome:         outcome,
		duration:        duration,
	})
}

func TestTracingWasmKeeperCallsExecutorDirectlyWhenTracingDisabled(t *testing.T) {
	tracer := &contractTracer{enabled: false}
	wasmCalls := 0
	wasmKeeper := keeper.NewTracingWasmKeeper(
		wasmExecutorFunc(func(sdk.Context, sdk.AccAddress, sdk.AccAddress, []byte, sdk.Coins) ([]byte, error) {
			wasmCalls++
			return nil, nil
		}),
		tracer,
	)

	_, err := wasmKeeper.Execute(sdk.Context{}, accAddrs[0], authAcc, []byte(`{"do":"something"}`), nil)
	require.NoError(t, err)
	require.Equal(t, 1, wasmCalls)
	require.Empty(t, tracer.traces)
}

func TestTracingWasmKeeperIgnoresProbePanic(t *testing.T) {
	tracer := &contractTracer{panicOnEnabled: true}
	wasmCalls := 0
	wasmKeeper := keeper.NewTracingWasmKeeper(
		wasmExecutorFunc(func(sdk.Context, sdk.AccAddress, sdk.AccAddress, []byte, sdk.Coins) ([]byte, error) {
			wasmCalls++
			return []byte("result"), nil
		}),
		tracer,
	)

	result, err := wasmKeeper.Execute(sdk.Context{}, accAddrs[0], authAcc, nil, nil)
	require.NoError(t, err)
	require.Equal(t, []byte("result"), result)
	require.Equal(t, 1, wasmCalls)
	require.Empty(t, tracer.traces)
}

func TestTracingWasmKeeperDoesNotChangeExecutionGas(t *testing.T) {
	run := func(enabled bool) storetypes.Gas {
		tracer := &contractTracer{enabled: enabled}
		wasmKeeper := keeper.NewTracingWasmKeeper(
			wasmExecutorFunc(func(ctx sdk.Context, _ sdk.AccAddress, _ sdk.AccAddress, _ []byte, _ sdk.Coins) ([]byte, error) {
				ctx.GasMeter().ConsumeGas(7, "test execution")
				return []byte("result"), nil
			}),
			tracer,
		)
		ctx := sdk.Context{}.WithGasMeter(storetypes.NewGasMeter(100))

		result, err := wasmKeeper.Execute(ctx, accAddrs[0], authAcc, []byte(`{"do":"something"}`), nil)
		require.NoError(t, err)
		require.Equal(t, []byte("result"), result)
		return ctx.GasMeter().GasConsumed()
	}

	require.Equal(t, run(false), run(true))
}

func TestTracingWasmKeeperIgnoresRecorderPanics(t *testing.T) {
	t.Run("successful execution", func(t *testing.T) {
		wasmKeeper := keeper.NewTracingWasmKeeper(
			wasmExecutorFunc(func(sdk.Context, sdk.AccAddress, sdk.AccAddress, []byte, sdk.Coins) ([]byte, error) {
				return []byte("result"), nil
			}),
			&contractTracer{enabled: true, panicOnRecord: true},
		)

		result, err := wasmKeeper.Execute(sdk.Context{}, accAddrs[0], authAcc, nil, nil)
		require.NoError(t, err)
		require.Equal(t, []byte("result"), result)
	})

	t.Run("execution panic", func(t *testing.T) {
		wasmKeeper := keeper.NewTracingWasmKeeper(
			wasmExecutorFunc(func(sdk.Context, sdk.AccAddress, sdk.AccAddress, []byte, sdk.Coins) ([]byte, error) {
				panic("execution failed")
			}),
			&contractTracer{enabled: true, panicOnRecord: true},
		)

		require.PanicsWithValue(t, "execution failed", func() {
			_, _ = wasmKeeper.Execute(sdk.Context{}, accAddrs[0], authAcc, nil, nil)
		})
	})
}

func TestTracingWasmKeeperTracesOutcomes(t *testing.T) {
	expectedErr := errors.New("contract failed")
	tests := []struct {
		name            string
		wasmExecutor    wasmExecutorFunc
		expectedOutcome string
	}{
		{
			name: "success",
			wasmExecutor: func(sdk.Context, sdk.AccAddress, sdk.AccAddress, []byte, sdk.Coins) ([]byte, error) {
				return []byte("result"), nil
			},
			expectedOutcome: telemetry.OutcomeSuccess,
		},
		{
			name: "error",
			wasmExecutor: func(sdk.Context, sdk.AccAddress, sdk.AccAddress, []byte, sdk.Coins) ([]byte, error) {
				return nil, expectedErr
			},
			expectedOutcome: telemetry.OutcomeError,
		},
		{
			name: "panic",
			wasmExecutor: func(sdk.Context, sdk.AccAddress, sdk.AccAddress, []byte, sdk.Coins) ([]byte, error) {
				panic("contract execution panic")
			},
			expectedOutcome: telemetry.OutcomePanic,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tracer := &contractTracer{enabled: true}
			wasmKeeper := keeper.NewTracingWasmKeeper(tt.wasmExecutor, tracer)
			payload := []byte(`{"do":"something"}`)

			if tt.expectedOutcome == telemetry.OutcomePanic {
				require.PanicsWithValue(t, "contract execution panic", func() {
					_, _ = wasmKeeper.Execute(sdk.Context{}, accAddrs[0], authAcc, payload, nil)
				})
			} else {
				result, err := wasmKeeper.Execute(sdk.Context{}, accAddrs[0], authAcc, payload, nil)
				if tt.expectedOutcome == telemetry.OutcomeError {
					require.ErrorIs(t, err, expectedErr)
				} else {
					require.NoError(t, err)
					require.Equal(t, []byte("result"), result)
				}
			}

			require.Len(t, tracer.traces, 1)
			trace := tracer.traces[0]
			require.Equal(t, telemetry.ContractExecute, trace.operation)
			require.Equal(t, accAddrs[0].String(), trace.contractAddress)
			require.Zero(t, trace.codeID)
			require.Equal(t, payload, trace.payload)
			require.Equal(t, tt.expectedOutcome, trace.outcome)
		})
	}
}

func TestTracingWasmKeeperMeasuresDuration(t *testing.T) {
	const delay = 5 * time.Millisecond
	tracer := &contractTracer{enabled: true}
	wasmKeeper := keeper.NewTracingWasmKeeper(
		wasmExecutorFunc(func(sdk.Context, sdk.AccAddress, sdk.AccAddress, []byte, sdk.Coins) ([]byte, error) {
			time.Sleep(delay)
			return nil, nil
		}),
		tracer,
	)

	_, err := wasmKeeper.Execute(sdk.Context{}, accAddrs[0], authAcc, nil, nil)
	require.NoError(t, err)
	require.Len(t, tracer.traces, 1)
	require.GreaterOrEqual(t, tracer.traces[0].duration, delay)
}
