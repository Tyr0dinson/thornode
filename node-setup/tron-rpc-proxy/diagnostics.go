package main

import "time"

type diagnosticEvent struct {
	Timestamp      time.Time     `json:"timestamp"`
	Event          string        `json:"event"`
	Upstream       string        `json:"upstream"`
	Stage          string        `json:"stage"`
	Reason         failureReason `json:"reason"`
	HTTPStatus     int           `json:"http_status,omitempty"`
	ExternalHeight int64         `json:"external_height"`
	LocalHeight    int64         `json:"local_height"`
	CommonHeight   int64         `json:"common_height"`
	PreviousMode   string        `json:"previous_mode"`
	NewMode        string        `json:"new_mode"`
}

// Fixed-size recent diagnostics remain useful after recovery, without keeping
// unbounded events or any upstream URL, error text, response or signed payload.
type Diagnostics struct {
	LastFailure          *diagnosticEvent     `json:"last_failure,omitempty"`
	LastRecovery         *diagnosticEvent     `json:"last_recovery,omitempty"`
	LastTransition       *diagnosticEvent     `json:"last_transition,omitempty"`
	LastBroadcast        *broadcastDiagnostic `json:"last_broadcast,omitempty"`
	LastBroadcastFailure *broadcastDiagnostic `json:"last_broadcast_failure,omitempty"`
}

type broadcastDiagnostic struct {
	Timestamp  time.Time     `json:"timestamp"`
	Event      string        `json:"event"`
	TxID       string        `json:"txid"`
	Attempt    int           `json:"attempt"`
	Upstream   string        `json:"upstream,omitempty"`
	Reason     failureReason `json:"reason"`
	Outcome    string        `json:"outcome"`
	HTTPStatus int           `json:"http_status,omitempty"`
	DaemonCode string        `json:"daemon_code,omitempty"`
	DurationMS int64         `json:"duration_ms"`
}

// Only exact protocol enum names are safe diagnostic values. Never retain a
// prefix, truncated unknown code, message, or other daemon-supplied text.
func sanitizedDaemonCode(code string) string {
	switch code {
	case "", "SUCCESS", "SIGERROR", "CONTRACT_VALIDATE_ERROR", "CONTRACT_EXE_ERROR",
		"BANDWITH_ERROR", "DUP_TRANSACTION_ERROR", "TAPOS_ERROR", "TOO_BIG_TRANSACTION_ERROR",
		"TRANSACTION_EXPIRATION_ERROR", "SERVER_BUSY", "NO_CONNECTION",
		"NOT_ENOUGH_EFFECTIVE_CONNECTION", "BLOCK_UNSOLIDIFIED", "OTHER_ERROR":
		return code
	default:
		return "UNKNOWN"
	}
}

func (p *Proxy) recordBroadcastDiagnostic(e broadcastDiagnostic) {
	if !validID(e.TxID) {
		return
	}
	e.Timestamp = time.Now().UTC()
	e.DaemonCode = sanitizedDaemonCode(e.DaemonCode)
	p.diagnosticMu.Lock()
	defer p.diagnosticMu.Unlock()
	if e.Event == "broadcast_outcome" {
		p.diagnostics.LastBroadcast = &e
	}
	// Lookup misses and final outcomes must not erase the actionable attempt
	// failure, including when reconciliation or a later retry succeeds.
	if e.Event == "broadcast_attempt" && e.Reason != reasonOK {
		p.diagnostics.LastBroadcastFailure = &e
	}
	fields := []any{"timestamp", e.Timestamp, "event", e.Event, "txid", e.TxID, "attempt", e.Attempt, "upstream", e.Upstream, "reason", e.Reason, "outcome", e.Outcome, "duration_ms", e.DurationMS}
	if e.HTTPStatus != 0 {
		fields = append(fields, "http_status", e.HTTPStatus)
	}
	if e.DaemonCode != "" {
		fields = append(fields, "daemon_code", e.DaemonCode)
	}
	p.logger.Info("tron_proxy", fields...)
}

func (p *Proxy) recentDiagnostics() Diagnostics {
	p.diagnosticMu.Lock()
	defer p.diagnosticMu.Unlock()
	return p.diagnostics
}

func (p *Proxy) recordDiagnostic(event string, check checkResult, h Health, previous, mode string) {
	p.diagnosticMu.Lock()
	defer p.diagnosticMu.Unlock()
	e := &diagnosticEvent{Timestamp: time.Now().UTC(), Event: event, Upstream: check.Upstream, Stage: check.Stage, Reason: check.Reason, HTTPStatus: check.HTTPStatus, ExternalHeight: h.ExternalHeight, LocalHeight: h.LocalHeight, CommonHeight: h.CommonHeight, PreviousMode: previous, NewMode: mode}
	switch event {
	case "health_failure":
		p.diagnostics.LastFailure = e
	case "health_recovery":
		p.diagnostics.LastRecovery = e
	case "routing_changed":
		p.diagnostics.LastTransition = e
	}
	fields := []any{"timestamp", e.Timestamp, "event", e.Event, "upstream", e.Upstream, "stage", e.Stage, "reason", e.Reason, "external_height", e.ExternalHeight, "local_height", e.LocalHeight, "common_height", e.CommonHeight, "previous_mode", e.PreviousMode, "new_mode", e.NewMode}
	if e.HTTPStatus != 0 {
		fields = append(fields, "http_status", e.HTTPStatus)
	}
	p.logger.Info("tron_proxy", fields...)
}

func (p *Proxy) logHealthChanges(previous, current Health) {
	mode := p.currentMode()
	before := []checkResult{previous.ExternalCheck, previous.LocalCheck, previous.ChainCheck}
	after := []checkResult{current.ExternalCheck, current.LocalCheck, current.ChainCheck}
	for i, check := range after {
		if i == 2 && (check == after[0] || check == after[1]) {
			continue
		}
		if check == before[i] {
			continue
		}
		if !check.OK {
			// An unattempted comparison is explained by the independent check.
			if check.Reason != reasonUnverified {
				p.recordDiagnostic("health_failure", check, current, mode, mode)
			}
		} else if before[i].Stage != "" && !before[i].OK {
			p.recordDiagnostic("health_recovery", check, current, mode, mode)
		}
	}
}
