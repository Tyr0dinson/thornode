package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func broadcastLogEvents(t *testing.T, logs *bytes.Buffer) []map[string]any {
	t.Helper()
	var events []map[string]any
	decoder := json.NewDecoder(bytes.NewReader(logs.Bytes()))
	for decoder.More() {
		var event map[string]any
		if err := decoder.Decode(&event); err != nil {
			t.Fatal(err)
		}
		events = append(events, event)
	}
	return events
}

func broadcastStatus(t *testing.T, p *Proxy) (map[string]any, string) {
	t.Helper()
	w := httptest.NewRecorder()
	p.admin(w, httptest.NewRequest("GET", "/status", nil))
	var state struct {
		Diagnostics map[string]any `json:"diagnostics"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &state); err != nil {
		t.Fatal(err)
	}
	if w.Body.Len() > 4096 {
		t.Fatalf("status exceeds CLI read limit: %d", w.Body.Len())
	}
	return state.Diagnostics, w.Body.String()
}

func TestBroadcastDiagnosticsClassifiedAndSanitized(t *testing.T) {
	const secret = "NEVER_LOG_BROADCAST_CREDENTIAL"
	const privateURL = "https://user:" + secret + "@private.invalid/signed-payload"
	for _, tc := range []struct {
		name, body, reason, daemonCode string
		status                         int
		err                            error
	}{
		{name: "transport", err: errors.New(privateURL), reason: "transport"},
		{name: "timeout", err: context.DeadlineExceeded, reason: "timeout"},
		{name: "canceled", err: context.Canceled, reason: "canceled"},
		{name: "http", status: 503, body: secret, reason: "http_status"},
		{name: "invalid-json", body: secret, reason: "invalid_json"},
		{name: "invalid-ack", body: `{"result":"` + secret + `"}`, reason: "invalid_ack"},
		{name: "empty-ack", body: `{}`, reason: "invalid_ack"},
		{name: "wrong-id", body: `{"result":true,"txid":"` + secret + `"}`, reason: "ack_id_mismatch"},
		{name: "duplicate", body: `{"result":false,"code":"DUP_TRANSACTION_ERROR","message":"` + privateURL + `"}`, reason: "ack_rejected", daemonCode: "DUP_TRANSACTION_ERROR"},
		{name: "busy", body: `{"result":false,"code":"SERVER_BUSY"}`, reason: "ack_rejected", daemonCode: "SERVER_BUSY"},
		{name: "unknown-code", body: `{"result":false,"code":"` + privateURL + `"}`, reason: "ack_rejected", daemonCode: "UNKNOWN"},
		{name: "code-object", body: `{"result":false,"code":{"secret":"` + secret + `"}}`, reason: "ack_rejected", daemonCode: "UNKNOWN"},
		{name: "code-prefix", body: `{"result":false,"code":"DUP_TRANSACTION_ERROR ` + secret + `"}`, reason: "ack_rejected", daemonCode: "UNKNOWN"},
		{name: "oversized-code", body: `{"result":false,"code":"` + strings.Repeat(secret, 1024) + `"}`, reason: "ack_rejected", daemonCode: "UNKNOWN"},
		{name: "omitted-result-duplicate", body: `{"code":"DUP_TRANSACTION_ERROR","message":"` + privateURL + `"}`, reason: "ack_rejected", daemonCode: "DUP_TRANSACTION_ERROR"},
		{name: "omitted-result-busy", body: `{"code":"SERVER_BUSY"}`, reason: "ack_rejected", daemonCode: "SERVER_BUSY"},
		{name: "omitted-result-validation", body: `{"code":"CONTRACT_VALIDATE_ERROR"}`, reason: "ack_rejected", daemonCode: "CONTRACT_VALIDATE_ERROR"},
		{name: "omitted-result-unknown-code", body: `{"code":"` + privateURL + `"}`, reason: "invalid_ack", daemonCode: "UNKNOWN"},
		{name: "omitted-result-code-object", body: `{"code":{"secret":"` + secret + `"}}`, reason: "invalid_ack", daemonCode: "UNKNOWN"},
		{name: "omitted-result-code-prefix", body: `{"code":"SERVER_BUSY ` + secret + `"}`, reason: "invalid_ack", daemonCode: "UNKNOWN"},
		{name: "omitted-result-success-code", body: `{"code":"SUCCESS"}`, reason: "invalid_ack", daemonCode: "SUCCESS"},
		{name: "null-result-rejection", body: `{"result":null,"code":"DUP_TRANSACTION_ERROR"}`, reason: "invalid_ack", daemonCode: "DUP_TRANSACTION_ERROR"},
		{name: "invalid-result-rejection", body: `{"result":"` + secret + `","code":"SERVER_BUSY"}`, reason: "invalid_ack", daemonCode: "SERVER_BUSY"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, _, _, _ := fixture(t)
			p.setMode("local")
			// Populate health diagnostics so broadcast events must coexist with them.
			p.recordDiagnostic("health_failure", checkResult{Upstream: "external", Stage: "head", Reason: reasonHTTP, HTTPStatus: 503}, p.snapshot(), "local", "local")
			p.recordDiagnostic("health_recovery", checkResult{Upstream: "external", Stage: "head", Reason: reasonOK}, p.snapshot(), "local", "local")
			before := p.recentDiagnostics()
			var logs bytes.Buffer
			p.logger = slog.New(slog.NewJSONHandler(&logs, nil))
			p.local.API.User = url.UserPassword("user", secret)
			base := p.local.Client.Transport
			p.local.Client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
				if r.URL.Path != "/wallet/broadcasttransaction" {
					return base.RoundTrip(r)
				}
				if tc.err != nil {
					return nil, tc.err
				}
				code := tc.status
				if code == 0 {
					code = 200
				}
				return &http.Response{StatusCode: code, Body: io.NopCloser(strings.NewReader(tc.body)), Header: make(http.Header), Request: r}, nil
			})
			body := signed(t, false, false, time.Now().Add(time.Hour))
			entry, _ := parseSigned(body)
			if w := request(p, "/wallet/broadcasttransaction", body); w.Code != 200 {
				t.Fatalf("retry failed: %d", w.Code)
			}
			state, statusBody := broadcastStatus(t, p)
			failure, ok := state["last_broadcast_failure"].(map[string]any)
			if !ok || failure["reason"] != tc.reason || failure["txid"] != entry.ID || failure["upstream"] != "local" || failure["attempt"] != float64(1) {
				t.Fatalf("missing classified failure: %+v", failure)
			}
			if tc.daemonCode != "" && failure["daemon_code"] != tc.daemonCode {
				t.Fatalf("daemon code not sanitized: %+v", failure)
			}
			if tc.status != 0 && failure["http_status"] != float64(tc.status) {
				t.Fatalf("HTTP failure lost status: %+v", failure)
			}
			final, ok := state["last_broadcast"].(map[string]any)
			if !ok || final["event"] != "broadcast_outcome" || final["outcome"] != "accepted" || final["upstream"] != "external" || final["attempt"] != float64(2) {
				t.Fatalf("missing final outcome: %+v", final)
			}
			after := p.recentDiagnostics()
			if before.LastFailure != after.LastFailure || before.LastRecovery != after.LastRecovery || before.LastTransition != after.LastTransition {
				t.Fatal("broadcast clobbered health or routing diagnostics")
			}
			var attempts, lookups, retries, outcomes int
			for _, event := range broadcastLogEvents(t, &logs) {
				if event["txid"] != entry.ID || event["timestamp"] == nil {
					t.Fatalf("missing validated identity or timestamp: %+v", event)
				}
				if duration, ok := event["duration_ms"].(float64); !ok || duration < 0 {
					t.Fatalf("invalid duration: %+v", event)
				}
				switch event["event"] {
				case "broadcast_attempt":
					attempts++
				case "broadcast_reconciliation":
					lookups++
					if event["outcome"] != "miss" {
						t.Fatalf("unexpected lookup result: %+v", event)
					}
				case "broadcast_retry":
					retries++
					if event["upstream"] != "external" || event["attempt"] != float64(2) || event["reason"] != tc.reason {
						t.Fatalf("retry lost source reason or target: %+v", event)
					}
				case "broadcast_outcome":
					outcomes++
				default:
					t.Fatalf("unexpected event: %+v", event)
				}
			}
			if attempts != 2 || lookups != 2 || retries != 1 || outcomes != 1 {
				t.Fatalf("events: attempts=%d lookups=%d retries=%d outcomes=%d", attempts, lookups, retries, outcomes)
			}
			var tx transactionJSON
			_ = json.Unmarshal(body, &tx)
			for _, forbidden := range []string{secret, "private.invalid", "signed-payload", tx.Signature[0], tx.RawHex, "raw_data", "signature", string(body)} {
				if strings.Contains(logs.String()+statusBody, forbidden) {
					t.Fatalf("diagnostics leaked forbidden field or value: %q", forbidden)
				}
			}
			metrics := httptest.NewRecorder()
			p.ServeHTTP(metrics, httptest.NewRequest("GET", "/metrics", nil))
			for _, metric := range []string{"tron_proxy_broadcast_retries_total 1\n", "tron_proxy_fallbacks_total 0\n", "tron_proxy_external_mode 0\n", "tron_proxy_broadcast_attempts_total 2\n", "tron_proxy_ambiguous_total 0\n"} {
				if !strings.Contains(metrics.Body.String(), metric) {
					t.Errorf("missing metric %q", metric)
				}
			}
		})
	}
}

func TestBroadcastDaemonCodeAllowlist(t *testing.T) {
	for _, code := range []string{"", "SUCCESS", "SIGERROR", "CONTRACT_VALIDATE_ERROR", "CONTRACT_EXE_ERROR", "BANDWITH_ERROR", "DUP_TRANSACTION_ERROR", "TAPOS_ERROR", "TOO_BIG_TRANSACTION_ERROR", "TRANSACTION_EXPIRATION_ERROR", "SERVER_BUSY", "NO_CONNECTION", "NOT_ENOUGH_EFFECTIVE_CONNECTION", "BLOCK_UNSOLIDIFIED", "OTHER_ERROR"} {
		if got := sanitizedDaemonCode(code); got != code {
			t.Errorf("known code %q became %q", code, got)
		}
	}
	for _, code := range []string{"server_busy", "SERVER_BUSY\nsecret", " SERVER_BUSY", "SERVER_BUSY ", "https://user:secret@private.invalid", strings.Repeat("s", 65536)} {
		if got := sanitizedDaemonCode(code); got != "UNKNOWN" {
			t.Errorf("unknown code not sanitized: %q", got)
		}
	}
	// Code parsing is diagnostic only: a valid matching acknowledgement retains
	// its existing acceptance semantics even with an unrecognized code value.
	id := strings.Repeat("a", 64)
	for _, code := range []string{`"secret"`, `123`, `{"secret":true}`} {
		check, got := broadcastAcknowledgement([]byte(`{"result":true,"txid":"`+id+`","code":`+code+`}`), nil, id)
		if !check.OK || got != "UNKNOWN" {
			t.Fatalf("code changed acknowledgement acceptance: %+v %q", check, got)
		}
	}
}

func TestBroadcastAcknowledgementRequiresExplicitSuccess(t *testing.T) {
	id := strings.Repeat("a", 64)
	for _, tc := range []struct {
		name, fields, code string
		reason             failureReason
	}{
		{name: "only-id", reason: reasonInvalidAck},
		{name: "omitted-result-success", fields: `"code":"SUCCESS",`, code: "SUCCESS", reason: reasonInvalidAck},
		{name: "omitted-result-rejection", fields: `"code":"CONTRACT_VALIDATE_ERROR",`, code: "CONTRACT_VALIDATE_ERROR", reason: reasonAckRejected},
		{name: "omitted-result-unknown", fields: `"code":"SECRET",`, code: "UNKNOWN", reason: reasonInvalidAck},
		{name: "null-result", fields: `"result":null,"code":"SERVER_BUSY",`, code: "SERVER_BUSY", reason: reasonInvalidAck},
		{name: "string-result", fields: `"result":"true","code":"SERVER_BUSY",`, code: "SERVER_BUSY", reason: reasonInvalidAck},
		{name: "numeric-result", fields: `"result":1,"code":"SUCCESS",`, code: "SUCCESS", reason: reasonInvalidAck},
	} {
		t.Run(tc.name, func(t *testing.T) {
			check, code := broadcastAcknowledgement([]byte(`{`+tc.fields+`"txid":"`+id+`"}`), nil, id)
			if check.OK || check.Reason != tc.reason || code != tc.code {
				t.Fatalf("missing/invalid result misclassified: %+v code=%q", check, code)
			}
		})
	}
}

func TestBroadcastDiagnosticsPostPersistenceGates(t *testing.T) {
	for _, cause := range []string{"canceled", "expired"} {
		t.Run(cause, func(t *testing.T) {
			p, s, _, _ := fixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			expiration := time.Now().Add(2 * time.Second)
			body := signed(t, false, false, expiration)
			p.store = &dispatchGateJournal{journal: s, onSave: func(e *Entry) {
				if e.Attempts != 1 {
					return
				}
				if cause == "canceled" {
					cancel()
				} else {
					time.Sleep(time.Until(expiration.Add(-time.Second)) + time.Millisecond)
				}
			}}
			w := httptest.NewRecorder()
			p.ServeHTTP(w, httptest.NewRequest("POST", "/wallet/broadcasttransaction", bytes.NewReader(body)).WithContext(ctx))
			if w.Code != 503 || p.broadcasts.Load() != 0 || p.broadcastRetries.Load() != 0 {
				t.Fatalf("post-persistence gate dispatched: HTTP %d", w.Code)
			}
			state, _ := broadcastStatus(t, p)
			outcome, ok := state["last_broadcast"].(map[string]any)
			if !ok || outcome["outcome"] != "unknown" || outcome["reason"] != cause || outcome["attempt"] != float64(1) {
				t.Fatalf("terminal gate diagnosis lost: %+v", outcome)
			}
		})
	}
}

func TestBroadcastDiagnosticsReconciliationAndFinalOutcome(t *testing.T) {
	for _, reconcile := range []bool{false, true} {
		t.Run(map[bool]string{false: "miss", true: "found"}[reconcile], func(t *testing.T) {
			p, _, local, external := fixture(t)
			p.setMode("local")
			local.mu.Lock()
			local.response, local.lookup = "duplicate", reconcile
			local.mu.Unlock()
			external.mu.Lock()
			external.response = "duplicate"
			external.mu.Unlock()
			var logs bytes.Buffer
			p.logger = slog.New(slog.NewJSONHandler(&logs, nil))
			body := signed(t, false, false, time.Now().Add(time.Hour))
			w := request(p, "/wallet/broadcasttransaction", body)
			wantCode, wantOutcome := 503, "unknown"
			if reconcile {
				wantCode, wantOutcome = 200, "accepted"
			}
			if w.Code != wantCode {
				t.Fatalf("HTTP %d want %d", w.Code, wantCode)
			}
			state, _ := broadcastStatus(t, p)
			final, ok := state["last_broadcast"].(map[string]any)
			if !ok || final["outcome"] != wantOutcome {
				t.Fatalf("missing final outcome: %+v", final)
			}
			if reconcile && (final["reason"] != "reconciled" || final["upstream"] != "local" || final["attempt"] != float64(1)) {
				t.Fatalf("reconciled outcome missing provenance: %+v", final)
			}
			var found bool
			for _, event := range broadcastLogEvents(t, &logs) {
				if event["event"] == "broadcast_reconciliation" && event["outcome"] == "found" {
					found = true
				}
			}
			if found != reconcile {
				t.Fatalf("lookup result not logged: found=%t", found)
			}
			if reconcile {
				if p.broadcasts.Load() != 1 {
					t.Fatal("reconciliation caused an extra send")
				}
				lastFailure := state["last_broadcast_failure"]
				if w := request(p, "/wallet/broadcasttransaction", body); w.Code != 200 {
					t.Fatal("cached acceptance failed")
				}
				state, _ = broadcastStatus(t, p)
				before, _ := json.Marshal(lastFailure)
				after, _ := json.Marshal(state["last_broadcast_failure"])
				if !bytes.Equal(before, after) || p.broadcasts.Load() != 1 {
					t.Fatal("cached success lost original failure or retried")
				}
			}
		})
	}
}
