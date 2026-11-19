package main

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestOmittedFalseBroadcastResultRetainsRejectionCode(t *testing.T) {
	id := strings.Repeat("a", 64)
	for _, code := range []string{"DUP_TRANSACTION_ERROR", "SERVER_BUSY", "CONTRACT_VALIDATE_ERROR"} {
		t.Run(code, func(t *testing.T) {
			check, got := broadcastAcknowledgement([]byte(`{"code":"`+code+`","txid":"`+id+`"}`), nil, id)
			if check.OK || check.Reason != reasonAckRejected || got != code {
				t.Fatalf("omitted false result lost daemon rejection: check=%+v code=%q", check, got)
			}
		})
	}
}

func TestAmbiguousBroadcastRetryPreservesHealthyLocalRoute(t *testing.T) {
	for _, problem := range []string{"disconnect", "duplicate", "wrongid", "timeout"} {
		t.Run(problem, func(t *testing.T) {
			p, s, local, external := fixture(t)
			if !p.setMode("local") {
				t.Fatal("local admission failed")
			}
			local.mu.Lock()
			local.response = problem
			if problem == "timeout" {
				local.delay = 150 * time.Millisecond
			}
			local.mu.Unlock()
			body := signed(t, true, true, time.Now().Add(time.Hour))
			if w := request(p, "/wallet/broadcasttransaction", body); w.Code != 200 {
				t.Fatalf("external retry failed: %d %s", w.Code, w.Body.String())
			}
			entry, err := parseSigned(body)
			if err != nil {
				t.Fatal(err)
			}
			stored, err := s.get(entry.ID)
			if err != nil || stored == nil || !stored.Accepted || stored.Attempts != 2 || !bytes.Equal(stored.Body, body) {
				t.Fatalf("journal did not retain the accepted original transaction: %+v %v", stored, err)
			}
			if mode, err := s.mode(); err != nil || mode != "local" || !p.routeHealthy("local") {
				t.Errorf("ambiguous broadcast abandoned healthy local routing: mode=%q error=%v", mode, err)
			}
			local.mu.Lock()
			localReads := local.reads["/wallet/getnowblock"]
			local.mu.Unlock()
			external.mu.Lock()
			externalReads := external.reads["/wallet/getnowblock"]
			external.mu.Unlock()
			if w := request(p, "/wallet/getnowblock", []byte(`{}`)); w.Code != 200 {
				t.Fatalf("subsequent read failed: %d", w.Code)
			}
			if w := request(p, "/wallet/broadcasttransaction", body); w.Code != 200 {
				t.Fatalf("accepted transaction retry failed: %d", w.Code)
			}
			local.mu.Lock()
			defer local.mu.Unlock()
			external.mu.Lock()
			defer external.mu.Unlock()
			if local.reads["/wallet/getnowblock"] != localReads+1 || external.reads["/wallet/getnowblock"] != externalReads {
				t.Error("subsequent read did not stay on local")
			}
			if len(local.writes) != 1 || len(external.writes) != 1 || !bytes.Equal(local.writes[0], body) || !bytes.Equal(external.writes[0], body) {
				t.Fatal("retry changed the signed payload or dispatched extra writes")
			}
		})
	}
}

func TestAmbiguousBroadcastWithoutHealthyExternalKeepsLocal(t *testing.T) {
	p, s, local, external := fixture(t)
	p.setMode("local")
	externalOutage(p)
	local.mu.Lock()
	local.response = "duplicate"
	local.mu.Unlock()
	external.mu.Lock()
	external.reads = map[string]int{}
	external.mu.Unlock()
	body := signed(t, false, false, time.Now().Add(time.Hour))
	if w := request(p, "/wallet/broadcasttransaction", body); w.Code != 503 {
		t.Fatalf("unknown outcome reported as success: %d", w.Code)
	}
	if p.currentMode() != "local" || !p.healthy() {
		t.Error("unavailable retry provider evicted healthy admitted local")
	}
	if w := request(p, "/wallet/getnowblock", []byte(`{}`)); w.Code != 200 {
		t.Errorf("unrelated local read failed: %d", w.Code)
	}
	entry, _ := parseSigned(body)
	stored, err := s.get(entry.ID)
	if err != nil || stored == nil || stored.Accepted || stored.Attempts != 1 {
		t.Fatalf("unknown transaction journal changed: %+v %v", stored, err)
	}
	external.mu.Lock()
	defer external.mu.Unlock()
	if len(external.writes) != 0 || len(external.reads) != 0 {
		t.Fatal("unhealthy external provider contacted")
	}
}

func TestAmbiguousBroadcastRetainsAttemptBudgetAndReconciliation(t *testing.T) {
	p, s, local, external := fixture(t)
	p.setMode("local")
	local.mu.Lock()
	local.response = "duplicate"
	local.mu.Unlock()
	external.mu.Lock()
	external.response = "duplicate"
	external.mu.Unlock()
	body := signed(t, false, true, time.Now().Add(time.Hour))
	for i := 0; i < 6; i++ {
		if w := request(p, "/wallet/broadcasttransaction", body); w.Code != 503 {
			t.Fatalf("unconfirmed duplicate claimed success on request %d", i)
		}
	}
	if p.currentMode() != "local" {
		t.Error("request-scoped failures changed healthy local mode")
	}
	entry, _ := parseSigned(body)
	stored, err := s.get(entry.ID)
	if err != nil || stored == nil || stored.Accepted || stored.Attempts != 4 {
		t.Fatalf("attempt budget not retained: %+v %v", stored, err)
	}
	local.mu.Lock()
	local.lookup = true
	local.mu.Unlock()
	if w := request(p, "/wallet/broadcasttransaction", body); w.Code != 200 {
		t.Fatalf("exhausted transaction could not reconcile: %d", w.Code)
	}
	stored, err = s.get(entry.ID)
	if err != nil || stored == nil || !stored.Accepted || stored.Attempts != 4 {
		t.Fatalf("reconciliation changed dispatch budget: %+v %v", stored, err)
	}
	local.mu.Lock()
	defer local.mu.Unlock()
	external.mu.Lock()
	defer external.mu.Unlock()
	if len(local.writes) != 2 || len(external.writes) != 2 {
		t.Fatalf("wrong dispatch counts: local=%d external=%d", len(local.writes), len(external.writes))
	}
	for _, sent := range append(local.writes, external.writes...) {
		if !bytes.Equal(sent, body) {
			t.Fatal("resend changed signed bytes")
		}
	}
}

func TestBroadcastRetryDoesNotUndoHealthOrOperatorFallback(t *testing.T) {
	for _, fault := range []string{"stale-head", "fork", "operator"} {
		t.Run(fault, func(t *testing.T) {
			p, _, local, external := fixture(t)
			p.setMode("local")
			local.mu.Lock()
			local.response = "duplicate"
			local.mu.Unlock()
			base := p.local.Client.Transport
			p.local.Client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
				resp, err := base.RoundTrip(r)
				if r.URL.Path == "/wallet/broadcasttransaction" {
					if fault == "operator" {
						p.setMode("external")
					} else {
						local.mu.Lock()
						local.age = time.Minute
						if fault == "fork" {
							local.age, local.fork = 0, true
						}
						local.mu.Unlock()
						p.probe(context.Background())
					}
				}
				return resp, err
			})
			body := signed(t, false, false, time.Now().Add(time.Hour))
			if w := request(p, "/wallet/broadcasttransaction", body); w.Code != 200 {
				t.Fatalf("healthy external retry failed: %d", w.Code)
			}
			if p.currentMode() != "external" {
				t.Fatal("broadcast completion undid a real fallback")
			}
			local.mu.Lock()
			local.age, local.fork = 0, false
			local.mu.Unlock()
			p.probe(context.Background())
			if p.currentMode() != "external" {
				t.Fatal("health recovery automatically readmitted local")
			}
			local.mu.Lock()
			defer local.mu.Unlock()
			external.mu.Lock()
			defer external.mu.Unlock()
			if len(local.writes) != 1 || len(external.writes) != 1 || !bytes.Equal(external.writes[0], body) {
				t.Fatal("safety fallback changed dispatch count or payload")
			}
		})
	}
}

func TestCanceledAmbiguousBroadcastDoesNotRetryOrChangeMode(t *testing.T) {
	p, s, local, external := fixture(t)
	p.setMode("local")
	local.mu.Lock()
	local.response = "duplicate"
	local.mu.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	base := p.local.Client.Transport
	p.local.Client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		resp, err := base.RoundTrip(r)
		if r.URL.Path == "/wallet/broadcasttransaction" {
			cancel()
		}
		return resp, err
	})
	body := signed(t, false, false, time.Now().Add(time.Hour))
	w := httptest.NewRecorder()
	p.ServeHTTP(w, httptest.NewRequest("POST", "/wallet/broadcasttransaction", bytes.NewReader(body)).WithContext(ctx))
	if w.Code != 503 || p.currentMode() != "local" {
		t.Fatalf("canceled ambiguity changed outcome or mode: code=%d mode=%s", w.Code, p.currentMode())
	}
	entry, _ := parseSigned(body)
	stored, err := s.get(entry.ID)
	if err != nil || stored == nil || stored.Accepted || stored.Attempts != 1 {
		t.Fatalf("canceled request journal unsafe: %+v %v", stored, err)
	}
	external.mu.Lock()
	defer external.mu.Unlock()
	if len(external.writes) != 0 {
		t.Fatal("canceled request sent an external retry")
	}
}

func TestBroadcastRetryJournalFailureStillClosesAdmission(t *testing.T) {
	for _, tc := range []struct {
		name         string
		failAt       int
		wantExternal int
	}{
		{name: "before-external-dispatch", failAt: 3, wantExternal: 0},
		{name: "after-external-acceptance", failAt: 4, wantExternal: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, s, local, external := fixture(t)
			p.setMode("local")
			local.mu.Lock()
			local.response = "duplicate"
			local.mu.Unlock()
			p.store = &faultJournal{journal: s, fail: tc.failAt}
			body := signed(t, false, false, time.Now().Add(time.Hour))
			if w := request(p, "/wallet/broadcasttransaction", body); w.Code != 503 || !p.unsafe.Load() || p.healthy() {
				t.Fatalf("journal failure did not close admission: code=%d unsafe=%t", w.Code, p.unsafe.Load())
			}
			if w := request(p, "/wallet/getnowblock", []byte(`{}`)); w.Code != 503 {
				t.Fatal("unsafe journal still served local reads")
			}
			entry, _ := parseSigned(body)
			stored, err := s.get(entry.ID)
			if err != nil || stored == nil || stored.Accepted || stored.Attempts != 1+tc.wantExternal {
				t.Fatalf("lost pending entry after journal failure: %+v %v", stored, err)
			}
			external.mu.Lock()
			defer external.mu.Unlock()
			if len(external.writes) != tc.wantExternal {
				t.Fatalf("external dispatch count=%d want=%d", len(external.writes), tc.wantExternal)
			}
		})
	}
}
