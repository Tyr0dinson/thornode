package common

import (
	"strings"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/rs/zerolog"
)

const (
	PaillierValidationOperationKeygen  = "keygen"
	PaillierValidationOperationKeysign = "keysign"
)

var paillierValidationFailureCounter = prometheus.NewCounterVec(
	prometheus.CounterOpts{
		Namespace: "thornode",
		Subsystem: "tss",
		Name:      "paillier_validation_failure_total",
		Help:      "Total number of TSS ECDSA Paillier localstate validation failures.",
	},
	[]string{"operation", "reason"},
)

// RegisterPaillierValidationMetric wires the Paillier security counter into the
// active Prometheus registry. It is called from the TSS monitor Enable path so
// disabled TSS monitoring does not register a standalone metric.
func RegisterPaillierValidationMetric() {
	if err := prometheus.Register(paillierValidationFailureCounter); err != nil {
		if alreadyRegistered, ok := err.(prometheus.AlreadyRegisteredError); ok {
			if counter, ok := alreadyRegistered.ExistingCollector.(*prometheus.CounterVec); ok {
				paillierValidationFailureCounter = counter
			}
		}
	}
}

// PaillierValidationLogContext carries security-relevant validation failure
// context into a structured SECURITY/CRITICAL log line.
type PaillierValidationLogContext struct {
	Operation         string
	PoolPubKey        string
	KeygenPubKey      string
	ParticipantPubKey string
	LocalNodePubKey   string
	MsgID             string
	EmergencyAction   string
}

// RecordPaillierValidationFailure increments the exported validation failure
// counter. The reason label is normalized to avoid unbounded cardinality.
func RecordPaillierValidationFailure(operation, reason string) {
	if operation == "" {
		operation = "unknown"
	}
	paillierValidationFailureCounter.WithLabelValues(operation, paillierValidationMetricReason(reason)).Inc()
}

// LogPaillierValidationFailure emits the operator-facing critical security log
// and records the matching metric.
func LogPaillierValidationFailure(logger zerolog.Logger, ctx PaillierValidationLogContext, err error) {
	validationErr, ok := AsPaillierValidationError(err)
	participantIndex := paillierValidationUnknownParticipant
	reason := "unknown"
	if ok {
		participantIndex = validationErr.ParticipantIndex
		reason = validationErr.Reason
	}

	RecordPaillierValidationFailure(ctx.Operation, reason)

	event := logger.Error().
		Err(err).
		Str("event", "SECURITY").
		Str("severity", "CRITICAL").
		Str("security_event", "tss_paillier_validation_failure").
		Str("operation", ctx.Operation).
		Str("pool_pub_key", ctx.PoolPubKey).
		Str("keygen_pub_key", ctx.KeygenPubKey).
		Str("participant_pub_key", ctx.ParticipantPubKey).
		Int("participant_index", participantIndex).
		Str("reason", reason).
		Str("local_node_pub_key", ctx.LocalNodePubKey).
		Str("msg_id", ctx.MsgID)

	if ctx.EmergencyAction != "" {
		event = event.Str("emergency_action", ctx.EmergencyAction)
	}

	event.Msg("SECURITY CRITICAL Paillier localstate validation failure")
}

func paillierValidationMetricReason(reason string) string {
	if reason == "" {
		return "unknown"
	}
	if strings.HasPrefix(reason, "small_factor=") {
		return "small_factor"
	}
	return reason
}

// ParticipantPubKeyAt returns the participant pubkey for a validation index
// when that index can be mapped to the keygen/localstate participant list.
func ParticipantPubKeyAt(participants []string, index int) string {
	if index < 0 || index >= len(participants) {
		return ""
	}
	return participants[index]
}
