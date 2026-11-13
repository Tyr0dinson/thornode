package thorchain

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	sdklog "cosmossdk.io/log"
	"cosmossdk.io/store"
	storemetrics "cosmossdk.io/store/metrics"
	storetypes "cosmossdk.io/store/types"
	wasmtypes "github.com/CosmWasm/wasmd/x/wasm/types"
	tmproto "github.com/cometbft/cometbft/proto/tendermint/types"
	dbm "github.com/cosmos/cosmos-db"
	sdk "github.com/cosmos/cosmos-sdk/types"
	banktypes "github.com/cosmos/cosmos-sdk/x/bank/types"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	dto "github.com/prometheus/client_model/go"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/common/telemetry"
	"gitlab.com/thorchain/thornode/v3/constants"
)

type msgHandlerFunc func(ctx cosmos.Context, msg cosmos.Msg) (*cosmos.Result, error)

func (f msgHandlerFunc) Run(ctx cosmos.Context, msg cosmos.Msg) (*cosmos.Result, error) {
	return f(ctx, msg)
}

func requireHistogram(
	t *testing.T,
	registry *prometheus.Registry,
	name string,
	labels prometheus.Labels,
	count uint64,
	sum float64,
) {
	t.Helper()
	families, err := registry.Gather()
	require.NoError(t, err)

	var histogram *dto.Histogram
	for _, family := range families {
		if family.GetName() != name {
			continue
		}
		for _, metric := range family.GetMetric() {
			if metricHasLabels(metric, labels) {
				histogram = metric.GetHistogram()
				break
			}
		}
	}

	if histogram == nil {
		require.Fail(t, "histogram not found", "name: %s, labels: %v", name, labels)
		return
	}
	require.Equal(t, count, histogram.GetSampleCount())
	require.InDelta(t, sum, histogram.GetSampleSum(), 1e-9)
}

func metricHasLabels(metric *dto.Metric, expected prometheus.Labels) bool {
	if len(metric.GetLabel()) != len(expected) {
		return false
	}
	for _, label := range metric.GetLabel() {
		if expected[label.GetName()] != label.GetValue() {
			return false
		}
	}
	return true
}

func TestTracerGlobalTelemetryGate(t *testing.T) {
	tracer, err := NewTracer(TraceConfig{
		TraceManagers:  true,
		TraceHandlers:  true,
		TraceContracts: true,
	}, false, nil)
	require.NoError(t, err)
	require.False(t, tracer.managersEnabled())
	require.False(t, tracer.handlersEnabled())
	require.False(t, tracer.contractMetricsEnabled())
}

func TestTracePayloadsRequiresContractTracing(t *testing.T) {
	var output bytes.Buffer
	logger := sdklog.NewLogger(&output, sdklog.LevelOption(zerolog.DebugLevel), sdklog.OutputJSONOption())
	ctx := cosmos.Context{}.WithLogger(logger)
	tracer, err := NewTracer(TraceConfig{TracePayloads: true}, false, nil)
	require.NoError(t, err)
	require.False(t, tracer.ContractTracingEnabled())

	tracer.RecordContract(
		ctx,
		"execute",
		"thor1contract",
		7,
		[]byte(`{"swap":{}}`),
		"success",
		time.Second,
	)

	require.Empty(t, output.String())
}

func TestManagerTracerReachesExternalHandler(t *testing.T) {
	tracer, err := NewTracer(TraceConfig{TraceHandlers: true}, true, prometheus.NewRegistry())
	require.NoError(t, err)
	ms := msgServer{mgr: &Mgrs{tracer: tracer}}
	msg := &wasmtypes.MsgExecuteContract{}
	ctx := cosmos.Context{}.
		WithContext(context.Background()).
		WithLogger(sdklog.NewNopLogger())

	_, err = ms.externalHandler(
		ctx,
		msgHandlerFunc(func(cosmos.Context, cosmos.Msg) (*cosmos.Result, error) {
			return &cosmos.Result{}, nil
		}),
		msg,
	)

	require.NoError(t, err)
	require.Equal(t, float64(1), testutil.ToFloat64(tracer.handlerRuns.WithLabelValues(
		msgTypeURL(msg), "external", telemetry.ModeExecution, telemetry.OutcomeSuccess,
	)))
}

func TestTracerReachesHandlerEntryPoints(t *testing.T) {
	storeKey := storetypes.NewKVStoreKey("execution_handler_test")
	db := dbm.NewMemDB()
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	multiStore := store.NewCommitMultiStore(db, sdklog.NewNopLogger(), storemetrics.NewNoOpMetrics())
	multiStore.MountStoreWithDB(storeKey, storetypes.StoreTypeIAVL, db)
	require.NoError(t, multiStore.LoadLatestVersion())
	ctx := cosmos.NewContext(multiStore, tmproto.Header{}, false, sdklog.NewNopLogger())

	tests := []struct {
		name    string
		path    string
		msg     cosmos.Msg
		execute func(context.Context, Manager) error
	}{
		{
			name: "internal",
			path: pathInternal,
			msg:  &MsgNoOp{},
			execute: func(_ context.Context, mgr Manager) error {
				_, err := NewInternalHandler(mgr)(ctx, &MsgNoOp{})
				return err
			},
		},
		{
			name: "bank",
			path: pathBank,
			msg:  &banktypes.MsgSend{},
			execute: func(goCtx context.Context, mgr Manager) error {
				_, err := NewBankSendHandler(NewSendHandler(mgr)).Send(goCtx, &banktypes.MsgSend{})
				return err
			},
		},
		{
			name: "wasm",
			path: pathWasm,
			msg:  &wasmtypes.MsgStoreCode{},
			execute: func(goCtx context.Context, mgr Manager) error {
				_, err := (msgServer{mgr: mgr}).StoreCode(goCtx, &wasmtypes.MsgStoreCode{})
				return err
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tracer, err := NewTracer(
				TraceConfig{TraceHandlers: true},
				true,
				prometheus.NewRegistry(),
			)
			require.NoError(t, err)
			mgr := NewDummyMgr()
			mgr.tracer = tracer

			err = tt.execute(ctx, mgr)
			require.Error(t, err)
			require.Equal(t, float64(1), testutil.ToFloat64(tracer.handlerRuns.WithLabelValues(
				msgTypeURL(tt.msg),
				tt.path,
				telemetry.ModeExecution,
				telemetry.OutcomeError,
			)))
		})
	}
}

func TestTracerRecordsManagerOutcomes(t *testing.T) {
	tracer, err := NewTracer(TraceConfig{TraceManagers: true}, true, prometheus.NewRegistry())
	require.NoError(t, err)

	require.NoError(t, runTracedManager(tracer, "network", "begin_block", func() error { return nil }))
	expectedErr := errors.New("failed")
	require.ErrorIs(t, runTracedManager(tracer, "network", "begin_block", func() error { return expectedErr }), expectedErr)

	require.Equal(t, float64(1), testutil.ToFloat64(
		tracer.managerRuns.WithLabelValues("network", "begin_block", telemetry.OutcomeSuccess),
	))
	require.Equal(t, float64(1), testutil.ToFloat64(
		tracer.managerRuns.WithLabelValues("network", "begin_block", telemetry.OutcomeError),
	))
}

func TestTracerRecordsContractIdentity(t *testing.T) {
	tracer, err := NewTracer(TraceConfig{TraceContracts: true}, true, prometheus.NewRegistry())
	require.NoError(t, err)

	payload := []byte(`{"swap":{"asset":"BTC.BTC"}}`)
	tracer.RecordContract(
		cosmos.Context{},
		"execute",
		"thor1contract",
		7,
		payload,
		"success",
		25*time.Millisecond,
	)

	require.Equal(t, float64(1), testutil.ToFloat64(
		tracer.contractRuns.WithLabelValues("execute", "thor1contract", "7", "success"),
	))
}

func TestTracerRecordsEmptyCodeID(t *testing.T) {
	tracer, err := NewTracer(TraceConfig{TraceContracts: true}, true, prometheus.NewRegistry())
	require.NoError(t, err)

	tracer.RecordContract(
		cosmos.Context{},
		"execute",
		"thor1contract",
		0,
		nil,
		"success",
		time.Millisecond,
	)

	require.Equal(t, float64(1), testutil.ToFloat64(
		tracer.contractRuns.WithLabelValues("execute", "thor1contract", "", "success"),
	))
}

func TestTracerRecordsHistograms(t *testing.T) {
	registry := prometheus.NewRegistry()
	tracer, err := NewTracer(TraceConfig{
		TraceManagers:  true,
		TraceHandlers:  true,
		TraceContracts: true,
	}, true, registry)
	require.NoError(t, err)

	tracer.recordManager("network", "begin_block", telemetry.OutcomeSuccess, time.Second)
	tracer.recordHandler(
		"/types.MsgSwap",
		"internal",
		telemetry.ModeExecution,
		telemetry.OutcomeSuccess,
		2*time.Second,
	)
	payload := make([]byte, 64)
	tracer.RecordContract(
		cosmos.Context{},
		telemetry.ContractExecute,
		"thor1contract",
		7,
		payload,
		telemetry.OutcomeSuccess,
		3*time.Second,
	)

	requireHistogram(t, registry, "thornode_execution_manager_duration_seconds", prometheus.Labels{
		"manager":   "network",
		"operation": "begin_block",
		"result":    telemetry.OutcomeSuccess,
	}, 1, 1)
	requireHistogram(t, registry, "thornode_execution_handler_duration_seconds", prometheus.Labels{
		"msg_type": "/types.MsgSwap",
		"path":     "internal",
		"mode":     telemetry.ModeExecution,
		"result":   telemetry.OutcomeSuccess,
	}, 1, 2)
	requireHistogram(t, registry, "thornode_execution_contract_duration_seconds", prometheus.Labels{
		"operation": telemetry.ContractExecute,
		"contract":  "thor1contract",
		"code_id":   "7",
		"result":    telemetry.OutcomeSuccess,
	}, 1, 3)
	requireHistogram(t, registry, "thornode_execution_contract_payload_bytes", prometheus.Labels{
		"operation": telemetry.ContractExecute,
		"contract":  "thor1contract",
		"code_id":   "7",
	}, 1, 64)
}

func TestTracerReusesRegisteredCollectors(t *testing.T) {
	registry := prometheus.NewRegistry()
	cfg := TraceConfig{
		TraceManagers:  true,
		TraceHandlers:  true,
		TraceContracts: true,
	}
	first, err := NewTracer(cfg, true, registry)
	require.NoError(t, err)
	second, err := NewTracer(cfg, true, registry)
	require.NoError(t, err)

	require.Same(t, first.managerRuns, second.managerRuns)
	require.Same(t, first.managerDuration, second.managerDuration)
	require.Same(t, first.handlerRuns, second.handlerRuns)
	require.Same(t, first.handlerDuration, second.handlerDuration)
	require.Same(t, first.contractRuns, second.contractRuns)
	require.Same(t, first.contractDuration, second.contractDuration)
	require.Same(t, first.contractPayloadBytes, second.contractPayloadBytes)
}

func TestRunTracedHandlerRecordsOutcomes(t *testing.T) {
	tracer, err := NewTracer(TraceConfig{TraceHandlers: true}, true, prometheus.NewRegistry())
	require.NoError(t, err)
	msg := &wasmtypes.MsgExecuteContract{}
	msgType := msgTypeURL(msg)

	ctx := cosmos.Context{}.WithContext(context.Background())
	trace := handlerTrace{path: "test", msg: msg}
	_, err = runTracedHandler(
		ctx,
		tracer,
		trace,
		func() (*cosmos.Result, error) {
			return &cosmos.Result{}, nil
		},
	)
	require.NoError(t, err)

	expectedErr := errors.New("handler failed")
	_, err = runTracedHandler(
		ctx,
		tracer,
		trace,
		func() (*cosmos.Result, error) {
			return nil, expectedErr
		},
	)
	require.ErrorIs(t, err, expectedErr)

	require.Panics(t, func() {
		_, _ = runTracedHandler(
			ctx,
			tracer,
			trace,
			func() (*cosmos.Result, error) {
				panic("handler panic")
			},
		)
	})

	for _, outcome := range []string{
		telemetry.OutcomeSuccess,
		telemetry.OutcomeError,
		telemetry.OutcomePanic,
	} {
		require.Equal(t, float64(1), testutil.ToFloat64(
			tracer.handlerRuns.WithLabelValues(msgType, "test", telemetry.ModeExecution, outcome),
		))
	}
}

func TestRunTracedHandlerSeparatesSimulationMode(t *testing.T) {
	tracer, err := NewTracer(TraceConfig{TraceHandlers: true}, true, prometheus.NewRegistry())
	require.NoError(t, err)
	trace := handlerTrace{
		path:    "internal",
		msgType: "/types.MsgSwap",
	}

	executionCtx := cosmos.Context{}.WithContext(context.Background())
	_, err = runTracedHandler(
		executionCtx,
		tracer,
		trace,
		func() (*cosmos.Result, error) {
			return &cosmos.Result{}, nil
		},
	)
	require.NoError(t, err)

	simulationCtx := executionCtx.WithValue(constants.CtxSimulationMode, true)
	_, err = runTracedHandler(
		simulationCtx,
		tracer,
		trace,
		func() (*cosmos.Result, error) {
			return &cosmos.Result{}, nil
		},
	)
	require.NoError(t, err)
	sdkSimulationCtx := executionCtx.WithExecMode(sdk.ExecModeSimulate)
	_, err = runTracedHandler(
		sdkSimulationCtx,
		tracer,
		trace,
		func() (*cosmos.Result, error) {
			return &cosmos.Result{}, nil
		},
	)
	require.NoError(t, err)

	require.Equal(t, float64(1), testutil.ToFloat64(tracer.handlerRuns.WithLabelValues(
		trace.msgType, trace.path, telemetry.ModeExecution, telemetry.OutcomeSuccess,
	)))
	require.Equal(t, float64(2), testutil.ToFloat64(tracer.handlerRuns.WithLabelValues(
		trace.msgType, trace.path, telemetry.ModeSimulation, telemetry.OutcomeSuccess,
	)))
}

func TestTelemetryFailuresDoNotAffectExecution(t *testing.T) {
	t.Run("manager result", func(t *testing.T) {
		// Missing collectors force a recording panic.
		tracer := &Tracer{traceManagers: true}
		expectedErr := errors.New("manager failed")
		require.ErrorIs(t, runTracedManager(tracer, "test", "test", func() error {
			return expectedErr
		}), expectedErr)
	})

	t.Run("manager panic", func(t *testing.T) {
		tracer := &Tracer{traceManagers: true}
		require.PanicsWithValue(t, "manager failed", func() {
			_ = runTracedManager(tracer, "test", "test", func() error {
				panic("manager failed")
			})
		})
	})

	t.Run("handler result", func(t *testing.T) {
		tracer := &Tracer{traceHandlers: true}
		expectedErr := errors.New("handler failed")
		_, err := runTracedHandler(
			cosmos.Context{},
			tracer,
			handlerTrace{path: "test", msg: &wasmtypes.MsgExecuteContract{}},
			func() (*cosmos.Result, error) {
				return nil, expectedErr
			},
		)
		require.ErrorIs(t, err, expectedErr)
	})

	t.Run("handler panic", func(t *testing.T) {
		tracer := &Tracer{traceHandlers: true}
		require.PanicsWithValue(t, "handler failed", func() {
			_, _ = runTracedHandler(
				cosmos.Context{},
				tracer,
				handlerTrace{path: "test", msg: &wasmtypes.MsgExecuteContract{}},
				func() (*cosmos.Result, error) {
					panic("handler failed")
				},
			)
		})
	})

	t.Run("contract recorder", func(t *testing.T) {
		tracer := &Tracer{traceContracts: true}
		require.NotPanics(t, func() {
			tracer.RecordContract(
				cosmos.Context{},
				"execute",
				"thor1contract",
				7,
				nil,
				"success",
				time.Second,
			)
		})
	})
}

func TestLogContractPayload(t *testing.T) {
	const payloadPrefix = `{"swap":{"memo":"`
	const payloadSuffix = `"}}`
	payloadAtLimit := []byte(payloadPrefix + strings.Repeat(
		"x",
		maxLoggedPayloadBytes-len(payloadPrefix)-len(payloadSuffix),
	) + payloadSuffix)
	payloadOverLimit := []byte(payloadPrefix + strings.Repeat(
		"x",
		maxLoggedPayloadBytes-len(payloadPrefix)-len(payloadSuffix)+1,
	) + payloadSuffix)
	require.Len(t, payloadAtLimit, maxLoggedPayloadBytes)
	require.Len(t, payloadOverLimit, maxLoggedPayloadBytes+1)
	invalidJSONPayload := []byte("not-json")
	invalidUTF8Payload := []byte("{\"swap\":\"\xff\"}")
	require.False(t, json.Valid(invalidJSONPayload))
	require.True(t, utf8.Valid(invalidJSONPayload))
	require.True(t, json.Valid(invalidUTF8Payload))
	require.False(t, utf8.Valid(invalidUTF8Payload))

	tests := []struct {
		name              string
		payload           []byte
		expectedPayload   string
		expectedEncoding  string
		expectedTruncated bool
		expectedCall      string
	}{
		{
			name:             "complete json",
			payload:          []byte(`{"swap":{}}`),
			expectedPayload:  `{"swap":{}}`,
			expectedEncoding: "json",
			expectedCall:     "swap",
		},
		{
			name:             "payload at fixed limit",
			payload:          payloadAtLimit,
			expectedPayload:  string(payloadAtLimit),
			expectedEncoding: "json",
			expectedCall:     "swap",
		},
		{
			name:              "payload one byte over fixed limit",
			payload:           payloadOverLimit,
			expectedPayload:   base64.StdEncoding.EncodeToString(payloadOverLimit[:maxLoggedPayloadBytes]),
			expectedEncoding:  "base64",
			expectedTruncated: true,
			expectedCall:      "swap",
		},
		{
			name:             "invalid json",
			payload:          invalidJSONPayload,
			expectedPayload:  base64.StdEncoding.EncodeToString(invalidJSONPayload),
			expectedEncoding: "base64",
			expectedCall:     unknownValue,
		},
		{
			name:             "invalid utf-8",
			payload:          invalidUTF8Payload,
			expectedPayload:  base64.StdEncoding.EncodeToString(invalidUTF8Payload),
			expectedEncoding: "base64",
			expectedCall:     "swap",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var output bytes.Buffer
			logger := sdklog.NewLogger(&output, sdklog.LevelOption(zerolog.DebugLevel), sdklog.OutputJSONOption())
			ctx := cosmos.Context{}.WithLogger(logger).WithBlockHeight(123)
			tracer, err := NewTracer(TraceConfig{
				TraceContracts: true,
				TracePayloads:  true,
			}, false, nil)
			require.NoError(t, err)

			tracer.RecordContract(
				ctx,
				"execute",
				"thor1contract",
				7,
				tt.payload,
				"success",
				time.Second,
			)

			var entry map[string]any
			require.NoError(t, json.Unmarshal(bytes.TrimSpace(output.Bytes()), &entry))
			require.Equal(t, tt.expectedPayload, entry["payload"])
			require.Equal(t, tt.expectedEncoding, entry["payload_encoding"])
			require.Equal(t, tt.expectedTruncated, entry["payload_truncated"])
			require.Equal(t, float64(len(tt.payload)), entry["payload_bytes"])
			require.Equal(t, tt.expectedCall, entry["call"])
			require.Equal(t, "thor1contract", entry["contract_address"])
			require.Equal(t, "7", entry["code_id"])
			hash := sha256.Sum256(tt.payload)
			require.Equal(t, hex.EncodeToString(hash[:]), entry["payload_sha256"])
		})
	}
}

func TestLazyStringOnlyFormatsEnabledDebugLogs(t *testing.T) {
	var output bytes.Buffer
	resolved := false
	value := lazyString(func() string {
		resolved = true
		return "formatted"
	})

	infoLogger := sdklog.NewLogger(&output, sdklog.LevelOption(zerolog.InfoLevel), sdklog.OutputJSONOption())
	infoLogger.Debug("discarded", "value", value)
	require.False(t, resolved)
	require.Empty(t, output.String())

	debugLogger := sdklog.NewLogger(&output, sdklog.LevelOption(zerolog.DebugLevel), sdklog.OutputJSONOption())
	debugLogger.Debug("recorded", "value", value)
	require.True(t, resolved)
	require.Contains(t, output.String(), "formatted")
}

func TestContractRecorderDoesNotChangeGasStoreOrEvents(t *testing.T) {
	tests := []struct {
		name           string
		traceConfig    TraceConfig
		metricsEnabled bool
		logger         sdklog.Logger
		registerer     prometheus.Registerer
	}{
		{
			name:           "contract metrics",
			traceConfig:    TraceConfig{TraceContracts: true},
			metricsEnabled: true,
			logger:         sdklog.NewNopLogger(),
			registerer:     prometheus.NewRegistry(),
		},
		{
			name: "payload logging",
			traceConfig: TraceConfig{
				TraceContracts: true,
				TracePayloads:  true,
			},
			logger: sdklog.NewLogger(&bytes.Buffer{}, sdklog.LevelOption(zerolog.DebugLevel), sdklog.OutputJSONOption()),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			storeKey := storetypes.NewKVStoreKey("execution_telemetry_test")
			db := dbm.NewMemDB()
			t.Cleanup(func() { require.NoError(t, db.Close()) })
			multiStore := store.NewCommitMultiStore(db, sdklog.NewNopLogger(), storemetrics.NewNoOpMetrics())
			multiStore.MountStoreWithDB(storeKey, storetypes.StoreTypeIAVL, db)
			require.NoError(t, multiStore.LoadLatestVersion())

			ctx := cosmos.NewContext(multiStore, tmproto.Header{Height: 123}, false, tt.logger)
			sentinelKey := []byte("sentinel")
			sentinelValue := []byte("unchanged")
			ctx.KVStore(storeKey).Set(sentinelKey, sentinelValue)
			ctx = ctx.WithGasMeter(storetypes.NewGasMeter(1_000_000))
			initialEventCount := len(ctx.EventManager().Events())

			tracer, err := NewTracer(tt.traceConfig, tt.metricsEnabled, tt.registerer)
			require.NoError(t, err)
			tracer.RecordContract(
				ctx,
				telemetry.ContractExecute,
				"thor1contract",
				7,
				[]byte(`{"swap":{}}`),
				telemetry.OutcomeSuccess,
				time.Millisecond,
			)

			require.Zero(t, ctx.GasMeter().GasConsumed())
			require.Len(t, ctx.EventManager().Events(), initialEventCount)
			readCtx := ctx.WithGasMeter(storetypes.NewInfiniteGasMeter())
			require.Equal(t, sentinelValue, readCtx.KVStore(storeKey).Get(sentinelKey))
		})
	}
}

func TestContractCall(t *testing.T) {
	require.Equal(t, "swap", contractCall([]byte(`{"swap":{"asset":"BTC.BTC"}}`)))
	require.Equal(t, unknownValue, contractCall([]byte(`{"one":{},"two":{}}`)))
	require.Equal(t, unknownValue, contractCall([]byte(`not-json`)))

	maxCall := strings.Repeat("x", maxLoggedCallBytes)
	require.Equal(t, maxCall, contractCall([]byte(`{"`+maxCall+`":{}}`)))
	require.Equal(t, unknownValue, contractCall([]byte(`{"`+maxCall+`x":{}}`)))
}
