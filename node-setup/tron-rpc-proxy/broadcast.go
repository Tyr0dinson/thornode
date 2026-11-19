package main

import (
	"context"
	"encoding/json"
	"net/http"
	"time"
)

func (p *Proxy) persist(entry *Entry) bool {
	if p.store.save(entry) != nil {
		p.unsafe.Store(true)
		return false
	}
	return true
}

func (p *Proxy) found(ctx context.Context, id string) bool {
	return p.reconcile(ctx, id, 0) != ""
}

func (p *Proxy) reconcile(ctx context.Context, id string, attempt int) string {
	query, _ := json.Marshal(map[string]string{"value": id})
	for _, route := range []string{"external", "local"} {
		event := broadcastDiagnostic{Event: "broadcast_reconciliation", TxID: id, Attempt: attempt, Upstream: route, Reason: reasonUnverified, Outcome: "skipped"}
		if !p.routeHealthy(route) || route == "local" && !p.localReconciliationTrusted() {
			p.recordBroadcastDiagnostic(event)
			continue
		}
		u := p.external
		if route == "local" {
			u = p.local
		}
		started := time.Now()
		data, err := u.call(ctx, "POST", "/wallet/gettransactionbyid", query)
		event.DurationMS = time.Since(started).Milliseconds()
		if err != nil {
			check := (checkResult{}).failed(err)
			event.Reason, event.HTTPStatus, event.Outcome = check.Reason, check.HTTPStatus, "failed"
		} else if transactionIdentity(data, id) {
			if !p.routeHealthy(route) || route == "local" && !p.localReconciliationTrusted() {
				p.recordBroadcastDiagnostic(event)
				continue
			}
			event.Reason, event.Outcome = reasonOK, "found"
			p.recordBroadcastDiagnostic(event)
			return route
		} else {
			event.Reason, event.Outcome = reasonIdentity, "miss"
			var object map[string]json.RawMessage
			if json.Unmarshal(data, &object) == nil && object != nil && len(object) == 0 {
				event.Reason = reasonLookupMiss
			}
		}
		p.recordBroadcastDiagnostic(event)
	}
	return ""
}

func broadcastAcknowledgement(data []byte, err error, id string) (checkResult, string) {
	check := checkResult{Reason: reasonInvalidAck}
	if err != nil {
		return check.failed(err), ""
	}
	var ack struct {
		Result json.RawMessage `json:"result"`
		ID     json.RawMessage `json:"txid"`
		Code   json.RawMessage `json:"code"`
	}
	if json.Unmarshal(data, &ack) != nil {
		return check, ""
	}
	code := ""
	if len(ack.Code) != 0 {
		if json.Unmarshal(ack.Code, &code) != nil {
			code = "UNKNOWN"
		}
		code = sanitizedDaemonCode(code)
	}
	if len(ack.Result) == 0 {
		// java-tron's proto3 JSON can omit a default false result. Only an
		// allowlisted rejection code diagnoses that case; it is never success.
		if code != "" && code != "UNKNOWN" && code != "SUCCESS" {
			check.Reason = reasonAckRejected
		}
		return check, code
	}
	var result *bool
	var ackID string
	if json.Unmarshal(ack.Result, &result) != nil || result == nil || len(ack.ID) != 0 && json.Unmarshal(ack.ID, &ackID) != nil {
		return check, code
	}
	switch {
	case !*result:
		check.Reason = reasonAckRejected
	case ackID != id:
		check.Reason = reasonAckID
	default:
		check.OK, check.Reason = true, reasonOK
	}
	return check, code
}

func (p *Proxy) localReconciliationTrusted() bool {
	h := p.snapshot()
	if p.unsafe.Load() || !p.fresh(h) || !h.Local {
		return false
	}
	// Independent health preserves an admitted route, but cannot authorize
	// chain-sensitive reconciliation from an unadmitted or rejected local node.
	mode := p.currentMode()
	return !p.unsafe.Load() && (mode == "local" || h.External && h.ChainCheck.OK)
}

func accepted(w http.ResponseWriter, id string) {
	data, _ := json.Marshal(map[string]any{"result": true, "txid": id})
	reply(w, data)
}

func (p *Proxy) broadcast(w http.ResponseWriter, r *http.Request, body []byte) {
	incoming, err := parseSigned(body)
	if err != nil {
		failure(w, 400, "invalid or unsupported signed transaction")
		return
	}
	started := time.Now()
	outcome := broadcastDiagnostic{Event: "broadcast_outcome", TxID: incoming.ID, Reason: reasonUnknownWrite, Outcome: "unknown"}
	defer func() {
		outcome.DurationMS = time.Since(started).Milliseconds()
		p.recordBroadcastDiagnostic(outcome)
	}()
	select {
	case p.writeGate <- struct{}{}:
		defer func() { <-p.writeGate }()
	case <-r.Context().Done():
		outcome.Reason = failureReasonFor(transportFailure(r.Context().Err()))
		failure(w, 503, "broadcast outcome unknown")
		return
	}
	if p.unsafe.Load() || !p.healthy() {
		outcome.Reason = reasonUnverified
		failure(w, 503, "broadcast admission unavailable")
		return
	}
	entry, err := p.store.get(incoming.ID)
	if err != nil {
		p.unsafe.Store(true)
		outcome.Reason = reasonJournal
		failure(w, 503, "journal unavailable")
		return
	}
	if entry != nil {
		stored, err := parseSigned(entry.Body)
		if err != nil || stored.ID != entry.ID || stored.Fingerprint != entry.Fingerprint || stored.Expiration != entry.Expiration || entry.Attempts < 0 || entry.Attempts > 4 {
			p.unsafe.Store(true)
			outcome.Reason = reasonJournalBad
			failure(w, 503, "journal invalid")
			return
		}
		if incoming.Fingerprint != entry.Fingerprint {
			outcome.Reason, outcome.Outcome = reasonConflict, "rejected"
			failure(w, 409, "transaction payload conflicts with journal")
			return
		}
		if entry.Accepted {
			outcome.Attempt, outcome.Reason, outcome.Outcome = entry.Attempts, reasonCached, "accepted"
			accepted(w, entry.ID)
			return
		}
	} else {
		entry = incoming
		if !p.persist(entry) {
			outcome.Reason = reasonJournal
			failure(w, 503, "journal unavailable")
			return
		}
	}
	outcome.Attempt = entry.Attempts
	if entry.Attempts > 0 {
		if route := p.reconcile(r.Context(), entry.ID, entry.Attempts); route != "" {
			outcome.Upstream = route
			entry.Accepted = true
			if !p.persist(entry) {
				outcome.Reason = reasonJournal
				failure(w, 503, "journal unavailable")
				return
			}
			outcome.Reason, outcome.Outcome = reasonReconciled, "accepted"
			accepted(w, entry.ID)
			return
		}
	}
	mode := p.currentMode()
	if p.unsafe.Load() {
		outcome.Reason = reasonJournal
		failure(w, 503, "journal unavailable")
		return
	}
	targets := []string{"external"}
	if mode == "local" && p.routeHealthy("local") {
		targets = []string{"local", "external"}
	}
	var retryReason failureReason
	for _, route := range targets {
		if r.Context().Err() != nil {
			outcome.Reason = failureReasonFor(transportFailure(r.Context().Err()))
			break
		}
		if time.Now().UnixMilli()+1000 >= entry.Expiration {
			outcome.Reason = reasonExpired
			break
		}
		if entry.Attempts >= 4 {
			outcome.Reason = reasonAttemptLimit
			break
		}
		if !p.routeHealthy(route) || route == "local" && p.currentMode() != "local" {
			outcome.Reason = reasonUnverified
			continue
		}
		// The attempt and original payload reach durable storage before dispatch.
		entry.Attempts++
		outcome.Attempt = entry.Attempts
		if !p.persist(entry) {
			outcome.Reason = reasonJournal
			failure(w, 503, "journal unavailable")
			return
		}
		// Durable IO and reconciliation may outlive the snapshot that admitted
		// this request. Do not dispatch a fallback with stale/unhealthy evidence.
		switch {
		case !p.routeHealthy(route) || route == "local" && p.currentMode() != "local":
			outcome.Reason = reasonUnverified
			continue
		case r.Context().Err() != nil:
			outcome.Reason = failureReasonFor(transportFailure(r.Context().Err()))
			continue
		case time.Now().UnixMilli()+1000 >= entry.Expiration:
			outcome.Reason = reasonExpired
			continue
		}
		u := p.external
		if route == "local" {
			u = p.local
		}
		if route == "external" && retryReason != "" {
			// Count only a dispatched request-scoped retry, not a routing change.
			p.broadcastRetries.Add(1)
			p.recordBroadcastDiagnostic(broadcastDiagnostic{Event: "broadcast_retry", TxID: entry.ID, Attempt: entry.Attempts, Upstream: route, Reason: retryReason, Outcome: "dispatch"})
		}
		p.broadcasts.Add(1)
		outcome.Upstream, outcome.Reason = route, reasonUnknownWrite
		attemptStarted := time.Now()
		data, err := u.call(r.Context(), "POST", "/wallet/broadcasttransaction", entry.Body)
		check, code := broadcastAcknowledgement(data, err, entry.ID)
		attemptOutcome := "indeterminate"
		if check.OK {
			attemptOutcome = "accepted"
		}
		p.recordBroadcastDiagnostic(broadcastDiagnostic{Event: "broadcast_attempt", TxID: entry.ID, Attempt: entry.Attempts, Upstream: route, Reason: check.Reason, HTTPStatus: check.HTTPStatus, DaemonCode: code, Outcome: attemptOutcome, DurationMS: time.Since(attemptStarted).Milliseconds()})
		reconciled := ""
		if !check.OK {
			reconciled = p.reconcile(r.Context(), entry.ID, entry.Attempts)
		}
		if check.OK || reconciled != "" {
			entry.Accepted = true
			if !p.persist(entry) {
				outcome.Reason = reasonJournal
				failure(w, 503, "journal unavailable")
				return
			}
			outcome.Reason, outcome.Outcome = reasonOK, "accepted"
			if reconciled != "" {
				outcome.Reason, outcome.Upstream = reasonReconciled, reconciled
			}
			accepted(w, entry.ID)
			return
		}
		// DUP_TRANSACTION_ERROR is not success without an identity-verified lookup.
		if route == "local" {
			// Ambiguity belongs to this transaction, not the admitted read route.
			// Health checks and operator actions remain authoritative for mode.
			retryReason = check.Reason
		}
	}
	if r.Context().Err() != nil {
		outcome.Reason = failureReasonFor(transportFailure(r.Context().Err()))
	}
	p.ambiguous.Add(1)
	failure(w, 503, "broadcast outcome unknown; retry identical transaction only")
}
