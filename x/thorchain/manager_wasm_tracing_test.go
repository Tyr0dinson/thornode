package thorchain

import (
	"errors"
	"testing"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"

	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/common/telemetry"
)

func TestWasmTraceExecutionPreservesResults(t *testing.T) {
	expectedErr := errors.New("execution failed")
	tests := []struct {
		name     string
		run      func() ([]byte, error)
		expected []byte
		err      error
	}{
		{
			name: "success",
			run: func() ([]byte, error) {
				return []byte("result"), nil
			},
			expected: []byte("result"),
		},
		{
			name: "error",
			run: func() ([]byte, error) {
				return nil, expectedErr
			},
			err: expectedErr,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// A missing collector must not let a recording panic replace the Wasm result.
			mgr := WasmMgr{tracer: &Tracer{traceContracts: true}}
			result, err := mgr.traceExecution(
				cosmos.Context{},
				telemetry.ContractExecute,
				sdk.AccAddress([]byte("contract")),
				7,
				nil,
				tt.run,
			)
			if tt.err != nil {
				require.ErrorIs(t, err, tt.err)
			} else {
				require.NoError(t, err)
			}
			require.Equal(t, tt.expected, result)
		})
	}
}

func TestWasmTraceExecutionPreservesPanic(t *testing.T) {
	mgr := WasmMgr{tracer: &Tracer{traceContracts: true}}
	require.PanicsWithValue(t, "execution panic", func() {
		_, _ = mgr.traceExecution(
			cosmos.Context{},
			telemetry.ContractExecute,
			sdk.AccAddress([]byte("contract")),
			7,
			nil,
			func() ([]byte, error) {
				panic("execution panic")
			},
		)
	})
}

func TestWasmTraceExecutionRecordsCodeID(t *testing.T) {
	tracer, err := NewTracer(TraceConfig{TraceContracts: true}, true, prometheus.NewRegistry())
	require.NoError(t, err)
	mgr := WasmMgr{tracer: tracer}
	contract := sdk.AccAddress([]byte("contract"))

	_, err = mgr.traceExecution(
		cosmos.Context{},
		telemetry.ContractExecute,
		contract,
		7,
		nil,
		func() ([]byte, error) {
			return nil, nil
		},
	)

	require.NoError(t, err)
	require.Equal(t, float64(1), testutil.ToFloat64(tracer.contractRuns.WithLabelValues(
		telemetry.ContractExecute,
		contract.String(),
		"7",
		telemetry.OutcomeSuccess,
	)))
}

func TestWasmTraceInstantiationPreservesAddressAndData(t *testing.T) {
	tracer, err := NewTracer(TraceConfig{TraceContracts: true}, true, prometheus.NewRegistry())
	require.NoError(t, err)
	mgr := WasmMgr{tracer: tracer}
	expectedAddress := sdk.AccAddress([]byte("contract"))
	expectedData := []byte("result")

	address, data, err := mgr.traceInstantiation(
		cosmos.Context{},
		telemetry.ContractInstantiate,
		7,
		nil,
		func() (sdk.AccAddress, []byte, error) {
			return expectedAddress, expectedData, nil
		},
	)

	require.NoError(t, err)
	require.Equal(t, expectedAddress, address)
	require.Equal(t, expectedData, data)
	require.Equal(t, float64(1), testutil.ToFloat64(tracer.contractRuns.WithLabelValues(
		telemetry.ContractInstantiate,
		expectedAddress.String(),
		"7",
		telemetry.OutcomeSuccess,
	)))
}

func TestWasmTraceInstantiationUsesUnassignedContractLabel(t *testing.T) {
	tracer, err := NewTracer(TraceConfig{TraceContracts: true}, true, prometheus.NewRegistry())
	require.NoError(t, err)
	mgr := WasmMgr{tracer: tracer}
	expectedErr := errors.New("instantiation failed")

	address, data, err := mgr.traceInstantiation(
		cosmos.Context{},
		telemetry.ContractInstantiate,
		7,
		nil,
		func() (sdk.AccAddress, []byte, error) {
			return nil, nil, expectedErr
		},
	)

	require.ErrorIs(t, err, expectedErr)
	require.Nil(t, address)
	require.Nil(t, data)
	require.Equal(t, float64(1), testutil.ToFloat64(tracer.contractRuns.WithLabelValues(
		telemetry.ContractInstantiate,
		unassignedContractLabel,
		"7",
		telemetry.OutcomeError,
	)))
}
