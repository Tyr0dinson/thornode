package thorchain

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"sync"
	"time"
	"unicode/utf8"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/prometheus/client_golang/prometheus"

	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/common/telemetry"
)

const (
	unknownValue            = "unknown"
	unassignedContractLabel = "unassigned"
	maxLoggedCallBytes      = 256
	maxLoggedPayloadBytes   = 16 * 1024

	pathAdvancedSwapQueue = "advanced_swap_queue"
	pathBank              = "bank"
	pathExternal          = "external"
	pathInternal          = "internal"
	pathPostSwap          = "post_swap"
	pathWasm              = "wasm"
)

// TraceConfig controls execution tracing.
type TraceConfig struct {
	TraceManagers  bool
	TraceHandlers  bool
	TraceContracts bool
	TracePayloads  bool
}

// Tracer owns execution metrics and payload logging.
type Tracer struct {
	traceManagers  bool
	traceHandlers  bool
	traceContracts bool
	tracePayloads  bool

	managerRuns          *prometheus.CounterVec
	managerDuration      *prometheus.HistogramVec
	handlerRuns          *prometheus.CounterVec
	handlerDuration      *prometheus.HistogramVec
	contractRuns         *prometheus.CounterVec
	contractDuration     *prometheus.HistogramVec
	contractPayloadBytes *prometheus.HistogramVec
}

// NewTracer registers enabled collectors. Prometheus metrics also require the
// global Cosmos telemetry switch.
func NewTracer(cfg TraceConfig, metricsEnabled bool, registerer prometheus.Registerer) (*Tracer, error) {
	t := &Tracer{
		traceManagers:  metricsEnabled && cfg.TraceManagers,
		traceHandlers:  metricsEnabled && cfg.TraceHandlers,
		traceContracts: metricsEnabled && cfg.TraceContracts,
		// Payload logs do not require the global Prometheus switch.
		tracePayloads: cfg.TraceContracts && cfg.TracePayloads,
	}

	if !t.traceManagers && !t.traceHandlers && !t.traceContracts {
		return t, nil
	}
	if registerer == nil {
		return nil, fmt.Errorf("execution telemetry registerer is nil")
	}

	var err error
	if t.traceManagers {
		t.managerRuns, err = registerCounterVec(registerer, prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "thornode", Subsystem: "execution", Name: "manager_runs_total",
			Help: "Number of THORChain manager executions.",
		}, []string{"manager", "operation", "result"}))
		if err != nil {
			return nil, err
		}
		t.managerDuration, err = registerHistogramVec(registerer, prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: "thornode", Subsystem: "execution", Name: "manager_duration_seconds",
			Help:    "THORChain manager execution duration in seconds.",
			Buckets: prometheus.ExponentialBuckets(0.0005, 2, 18),
		}, []string{"manager", "operation", "result"}))
		if err != nil {
			return nil, err
		}
	}

	if t.traceHandlers {
		t.handlerRuns, err = registerCounterVec(registerer, prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "thornode", Subsystem: "execution", Name: "handler_runs_total",
			Help: "Number of THORChain message handler executions.",
		}, []string{"msg_type", "path", "mode", "result"}))
		if err != nil {
			return nil, err
		}
		t.handlerDuration, err = registerHistogramVec(registerer, prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: "thornode", Subsystem: "execution", Name: "handler_duration_seconds",
			Help:    "THORChain message handler execution duration in seconds.",
			Buckets: prometheus.ExponentialBuckets(0.0005, 2, 18),
		}, []string{"msg_type", "path", "mode", "result"}))
		if err != nil {
			return nil, err
		}
	}

	if t.traceContracts {
		contractLabels := []string{"operation", "contract", "code_id", "result"}
		t.contractRuns, err = registerCounterVec(registerer, prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "thornode", Subsystem: "execution", Name: "contract_runs_total",
			Help: "Number of smart contract executions.",
		}, contractLabels))
		if err != nil {
			return nil, err
		}
		t.contractDuration, err = registerHistogramVec(registerer, prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: "thornode", Subsystem: "execution", Name: "contract_duration_seconds",
			Help:    "Smart contract execution duration in seconds.",
			Buckets: prometheus.ExponentialBuckets(0.0005, 2, 18),
		}, contractLabels))
		if err != nil {
			return nil, err
		}
		t.contractPayloadBytes, err = registerHistogramVec(registerer, prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: "thornode", Subsystem: "execution", Name: "contract_payload_bytes",
			Help:    "Smart contract execution payload size in bytes.",
			Buckets: prometheus.ExponentialBuckets(64, 2, 18),
		}, []string{"operation", "contract", "code_id"}))
		if err != nil {
			return nil, err
		}
	}

	return t, nil
}

func registerCounterVec(registerer prometheus.Registerer, collector *prometheus.CounterVec) (*prometheus.CounterVec, error) {
	if err := registerer.Register(collector); err != nil {
		if alreadyRegistered, ok := err.(prometheus.AlreadyRegisteredError); ok {
			existing, ok := alreadyRegistered.ExistingCollector.(*prometheus.CounterVec)
			if ok {
				return existing, nil
			}
		}
		return nil, err
	}
	return collector, nil
}

func registerHistogramVec(registerer prometheus.Registerer, collector *prometheus.HistogramVec) (*prometheus.HistogramVec, error) {
	if err := registerer.Register(collector); err != nil {
		if alreadyRegistered, ok := err.(prometheus.AlreadyRegisteredError); ok {
			existing, ok := alreadyRegistered.ExistingCollector.(*prometheus.HistogramVec)
			if ok {
				return existing, nil
			}
		}
		return nil, err
	}
	return collector, nil
}

func (t *Tracer) managersEnabled() bool {
	return t != nil && t.traceManagers
}

func (t *Tracer) handlersEnabled() bool {
	return t != nil && t.traceHandlers
}

func (t *Tracer) contractMetricsEnabled() bool {
	return t != nil && t.traceContracts
}

// ContractTracingEnabled reports whether contract metrics or payload logs are enabled,
// allowing Wasm callers to skip disabled tracing.
func (t *Tracer) ContractTracingEnabled() bool {
	return t != nil && (t.traceContracts || t.tracePayloads)
}

func (t *Tracer) recordManager(manager, operation, outcome string, duration time.Duration) {
	defer discardTelemetryPanic()
	if !t.managersEnabled() {
		return
	}
	t.managerRuns.WithLabelValues(manager, operation, outcome).Inc()
	t.managerDuration.WithLabelValues(manager, operation, outcome).Observe(duration.Seconds())
}

func (t *Tracer) recordHandler(msgType, path, mode, outcome string, duration time.Duration) {
	defer discardTelemetryPanic()
	if !t.handlersEnabled() {
		return
	}
	t.handlerRuns.WithLabelValues(msgType, path, mode, outcome).Inc()
	t.handlerDuration.WithLabelValues(msgType, path, mode, outcome).Observe(duration.Seconds())
}

// RecordContract records contract metrics and payload logs.
// Zero codeID becomes an empty label and log field.
func (t *Tracer) RecordContract(
	ctx cosmos.Context,
	operation, contractAddress string,
	codeID uint64,
	payload []byte,
	outcome string,
	duration time.Duration,
) {
	defer discardTelemetryPanic()
	if contractAddress == "" {
		contractAddress = unknownValue
	}
	codeIDLabel := ""
	if codeID > 0 {
		codeIDLabel = strconv.FormatUint(codeID, 10)
	}

	if t.contractMetricsEnabled() {
		t.contractRuns.WithLabelValues(operation, contractAddress, codeIDLabel, outcome).Inc()
		t.contractDuration.WithLabelValues(operation, contractAddress, codeIDLabel, outcome).Observe(duration.Seconds())
		t.contractPayloadBytes.WithLabelValues(operation, contractAddress, codeIDLabel).Observe(float64(len(payload)))
	}

	if t == nil || !t.tracePayloads {
		return
	}
	t.logPayload(ctx, operation, contractAddress, codeIDLabel, payload, outcome, duration)
}

// discardTelemetryPanic isolates telemetry failures from consensus execution.
// It must be deferred directly.
func discardTelemetryPanic() {
	_ = recover()
}

func (t *Tracer) logPayload(
	ctx cosmos.Context,
	operation, contractAddress, codeID string,
	payload []byte,
	outcome string,
	duration time.Duration,
) {
	content := newPayloadLogContent(payload)

	ctx.Logger().Debug("smart contract payload",
		"operation", operation,
		"contract_address", contractAddress,
		"code_id", codeID,
		"call", lazyString(func() string { return contractCall(payload) }),
		"payload_bytes", len(payload),
		"payload_sha256", lazyString(func() string {
			hash := sha256.Sum256(payload)
			return hex.EncodeToString(hash[:])
		}),
		"payload_encoding", lazyString(content.Encoding),
		"payload", lazyString(content.Value),
		"payload_truncated", content.truncated,
		"duration_seconds", duration.Seconds(),
		"result", outcome,
		"height", ctx.BlockHeight(),
	)
}

// lazyString formats payload data only for enabled debug events.
type lazyString func() string

func (s lazyString) String() string {
	return s()
}

type payloadLogContent struct {
	payload   []byte
	truncated bool
	once      sync.Once
	encoding  string
	value     string
}

func newPayloadLogContent(payload []byte) *payloadLogContent {
	return &payloadLogContent{
		payload:   payload,
		truncated: len(payload) > maxLoggedPayloadBytes,
	}
}

func (c *payloadLogContent) prepare() {
	c.once.Do(func() {
		loggedPayload := c.payload
		if c.truncated {
			loggedPayload = loggedPayload[:maxLoggedPayloadBytes]
		}

		c.encoding = "json"
		if c.truncated || !json.Valid(loggedPayload) || !utf8.Valid(loggedPayload) {
			c.encoding = "base64"
			c.value = base64.StdEncoding.EncodeToString(loggedPayload)
			return
		}
		c.value = string(loggedPayload)
	})
}

func (c *payloadLogContent) Encoding() string {
	c.prepare()
	return c.encoding
}

func (c *payloadLogContent) Value() string {
	c.prepare()
	return c.value
}

func contractCall(payload []byte) string {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(payload, &object); err != nil || len(object) != 1 {
		return unknownValue
	}
	// analyze-ignore(map-iteration) - object contains exactly one key
	for call := range object {
		if len(call) > maxLoggedCallBytes {
			return unknownValue
		}
		return call
	}
	return unknownValue
}

type handlerTrace struct {
	path    string
	msg     cosmos.Msg
	msgType string
}

func (t handlerTrace) resolvedMsgType() string {
	if t.msgType != "" {
		return t.msgType
	}
	return msgTypeURL(t.msg)
}

func runTracedHandler[T any](
	ctx cosmos.Context,
	tracer *Tracer,
	trace handlerTrace,
	run func() (T, error),
) (result T, err error) {
	if !tracer.handlersEnabled() {
		return run()
	}
	msgType := trace.resolvedMsgType()
	mode := handlerMode(ctx)
	start := time.Now()
	outcome := telemetry.OutcomePanic
	defer func() {
		tracer.recordHandler(msgType, trace.path, mode, outcome, time.Since(start))
	}()
	result, err = run()
	outcome = telemetry.Outcome(err)
	return result, err
}

func handlerMode(ctx cosmos.Context) (mode string) {
	mode = telemetry.ModeExecution
	if ctx.ExecMode() == sdk.ExecModeSimulate {
		return telemetry.ModeSimulation
	}
	defer discardTelemetryPanic()
	if isSimulationMode(ctx) {
		return telemetry.ModeSimulation
	}
	return mode
}

func msgTypeURL(msg cosmos.Msg) (msgType string) {
	msgType = unknownValue
	defer discardTelemetryPanic()
	if msg == nil {
		return msgType
	}
	return sdk.MsgTypeURL(msg)
}

func runTracedManager(tracer *Tracer, manager, operation string, run func() error) (err error) {
	if !tracer.managersEnabled() {
		return run()
	}
	start := time.Now()
	outcome := telemetry.OutcomePanic
	defer func() {
		tracer.recordManager(manager, operation, outcome, time.Since(start))
	}()
	err = run()
	outcome = telemetry.Outcome(err)
	return err
}
