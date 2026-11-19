package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestBothCommonBlockFailuresRejectExternalDispatch(t *testing.T) {
	for _, kind := range []string{"unavailable", "missing"} {
		t.Run(kind, func(t *testing.T) {
			p, _, _, external := fixture(t)
			p.setMode("local")
			for _, u := range []*Upstream{p.local, p.external} {
				code := 503
				if kind == "missing" {
					code = 200
				}
				u.Client.Transport = probeFaultTransport{base: u.Client.Transport, stage: "common", code: code, body: `{}`}
			}
			h := p.probe(context.Background())
			if h.External || h.Local || h.ExternalCheck.OK || h.LocalCheck.OK || p.healthy() || p.currentMode() != "external" {
				t.Errorf("simultaneous faults lost an upstream failure: %+v", h)
			}
			external.mu.Lock()
			external.reads = map[string]int{}
			external.mu.Unlock()
			if w := request(p, "/wallet/getnowblock", []byte(`null`)); w.Code != 503 {
				t.Error("read reached known unhealthy external")
			}
			if w := request(p, "/wallet/broadcasttransaction", signed(t, false, false, time.Now().Add(time.Hour))); w.Code != 503 {
				t.Error("write reached known unhealthy external")
			}
			external.mu.Lock()
			defer external.mu.Unlock()
			if len(external.reads) != 0 || len(external.writes) != 0 {
				t.Fatal("unhealthy external dispatched")
			}
		})
	}
}

func TestUntrustedLocalCannotReconcileExternalBroadcast(t *testing.T) {
	for _, state := range []string{"never-admitted", "rejected-fork", "fresh-common-chain"} {
		t.Run(state, func(t *testing.T) {
			p, s, local, external := fixture(t)
			if state == "rejected-fork" {
				p.setMode("local")
				local.mu.Lock()
				local.fork = true
				local.mu.Unlock()
				p.probe(context.Background())
				if p.currentMode() != "external" {
					t.Fatal("control fork was not rejected")
				}
			}
			body := signed(t, false, false, time.Now().Add(time.Hour))
			local.mu.Lock()
			local.tx, local.lookup = bytes.Clone(body), true
			local.mu.Unlock()
			external.mu.Lock()
			external.response = "duplicate"
			external.mu.Unlock()
			base := p.external.Client.Transport
			var outage atomic.Bool
			p.external.Client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
				if r.URL.Path == "/wallet/getnowblock" && outage.Load() {
					return &http.Response{StatusCode: 503, Body: io.NopCloser(strings.NewReader(`{}`)), Header: make(http.Header), Request: r}, nil
				}
				resp, err := base.RoundTrip(r)
				if r.URL.Path == "/wallet/broadcasttransaction" && state != "fresh-common-chain" {
					outage.Store(true)
					p.probe(context.Background())
				}
				return resp, err
			})
			w := request(p, "/wallet/broadcasttransaction", body)
			want := 503
			if state == "fresh-common-chain" {
				want = 200
			}
			if w.Code != want {
				t.Errorf("local reconciliation accepted without route/chain trust: got %d want %d", w.Code, want)
			}
			entry, _ := parseSigned(body)
			stored, _ := s.get(entry.ID)
			if stored == nil || stored.Accepted != (want == 200) || stored.Attempts != 1 {
				t.Fatal("incorrect reconciliation journal")
			}
			local.mu.Lock()
			defer local.mu.Unlock()
			if state != "fresh-common-chain" && local.reads["/wallet/gettransactionbyid"] != 0 {
				t.Fatal("queried untrusted local reconciliation source")
			}
		})
	}
}

func TestReconciliationRechecksLocalTrustAfterLookup(t *testing.T) {
	p, s, local, external := fixture(t)
	body := signed(t, false, false, time.Now().Add(time.Hour))
	local.mu.Lock()
	local.tx, local.lookup = bytes.Clone(body), true
	local.mu.Unlock()
	external.mu.Lock()
	external.response = "duplicate"
	external.mu.Unlock()
	base := p.local.Client.Transport
	p.local.Client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		resp, err := base.RoundTrip(r)
		if r.URL.Path == "/wallet/gettransactionbyid" {
			p.healthMu.Lock()
			p.health.External, p.health.ChainCheck.OK = false, false
			p.healthMu.Unlock()
		}
		return resp, err
	})
	if w := request(p, "/wallet/broadcasttransaction", body); w.Code != 503 {
		t.Fatal("accepted local reconciliation after its chain trust was withdrawn")
	}
	entry, _ := parseSigned(body)
	stored, _ := s.get(entry.ID)
	if stored == nil || stored.Accepted || stored.Attempts != 1 {
		t.Fatal("late loss of reconciliation trust corrupted journal")
	}
}

type dispatchGateJournal struct {
	journal
	modeCalls    int
	onSecondMode func()
	onSave       func(*Entry)
}

func (j *dispatchGateJournal) mode() (string, error) {
	j.modeCalls++
	if j.modeCalls == 2 && j.onSecondMode != nil {
		j.onSecondMode()
	}
	return j.journal.mode()
}
func (j *dispatchGateJournal) save(e *Entry) error {
	err := j.journal.save(e)
	if err == nil && j.onSave != nil {
		j.onSave(e)
	}
	return err
}

func TestHistoricalDispatchRechecksExternalHealth(t *testing.T) {
	for _, kind := range []string{"unhealthy", "stale"} {
		t.Run(kind, func(t *testing.T) {
			p, s, _, external := fixture(t)
			p.setMode("local")
			p.cfg.BlockFloor = 90000
			p.store = &dispatchGateJournal{journal: s, onSecondMode: func() {
				p.healthMu.Lock()
				defer p.healthMu.Unlock()
				if kind == "unhealthy" {
					p.health.External = false
				} else {
					p.health.Checked = time.Now().Add(-time.Minute)
				}
			}}
			external.mu.Lock()
			external.reads = map[string]int{}
			external.mu.Unlock()
			if w := request(p, "/wallet/getblockbynum", []byte(`{"num":89999}`)); w.Code != 503 {
				t.Fatalf("late %s external used", kind)
			}
			external.mu.Lock()
			defer external.mu.Unlock()
			if len(external.reads) != 0 {
				t.Fatal("external historical dispatch escaped health gate")
			}
		})
	}
}

func TestBroadcastDispatchRechecksExternalAfterPersistence(t *testing.T) {
	for _, mode := range []string{"local", "external"} {
		for _, kind := range []string{"unhealthy", "stale"} {
			t.Run(mode+"/"+kind, func(t *testing.T) {
				p, s, local, external := fixture(t)
				p.setMode(mode)
				local.mu.Lock()
				local.response = "duplicate"
				local.mu.Unlock()
				attempt := 1
				if mode == "local" {
					attempt = 2
				}
				p.store = &dispatchGateJournal{journal: s, onSave: func(e *Entry) {
					if e.Attempts != attempt {
						return
					}
					p.healthMu.Lock()
					defer p.healthMu.Unlock()
					if kind == "unhealthy" {
						p.health.External = false
					} else {
						p.health.Checked = time.Now().Add(-time.Minute)
					}
				}}
				body := signed(t, false, false, time.Now().Add(time.Hour))
				if w := request(p, "/wallet/broadcasttransaction", body); w.Code != 503 {
					t.Fatalf("late %s external used", kind)
				}
				external.mu.Lock()
				defer external.mu.Unlock()
				if len(external.writes) != 0 {
					t.Fatal("broadcast escaped post-persistence health gate")
				}
				entry, _ := parseSigned(body)
				stored, _ := s.get(entry.ID)
				if stored == nil || stored.Accepted || stored.Attempts != attempt || !bytes.Equal(stored.Body, body) {
					t.Fatal("unsafe journal after refused dispatch")
				}
			})
		}
	}
}

func TestHealthDiagnosticsSanitizedAndBounded(t *testing.T) {
	p, _, local, _ := fixture(t)
	var logs bytes.Buffer
	p.logger = slog.New(slog.NewJSONHandler(&logs, nil))
	p.setMode("local")
	logs.Reset()
	original := p.external.Client.Transport
	secret := "NEVER_LOG_THIS_SECRET"
	p.external.API.User = url.UserPassword("user", secret)
	for _, tc := range []struct {
		code   int
		body   string
		err    error
		reason failureReason
	}{
		{code: 503, body: secret, reason: reasonHTTP},
		{code: 429, body: secret, reason: reasonHTTP},
		{code: 200, body: secret, reason: reasonJSON},
		{err: errors.New("https://user:" + secret + "@private.invalid/signed-payload"), reason: reasonTransport},
	} {
		p.external.Client.Transport = probeFaultTransport{base: original, stage: "common", code: tc.code, body: tc.body, err: tc.err}
		h := p.probe(context.Background())
		if h.ExternalCheck.Reason != tc.reason || h.ExternalCheck.HTTPStatus != func() int {
			if tc.reason == reasonHTTP {
				return tc.code
			}
			return 0
		}() {
			t.Fatalf("missing bounded failure detail: %+v", h.ExternalCheck)
		}
		n := logs.Len()
		p.probe(context.Background())
		if logs.Len() != n {
			t.Fatal("unchanged failing poll flooded logs")
		}
	}
	p.external.Client.Transport = original
	p.probe(context.Background())
	n := logs.Len()
	p.probe(context.Background())
	if logs.Len() != n {
		t.Fatal("routine healthy poll logged")
	}
	local.mu.Lock()
	local.age = time.Minute
	local.mu.Unlock()
	p.probe(context.Background())
	n = logs.Len()
	p.setMode("external")
	if logs.Len() != n {
		t.Fatal("unchanged mode logged a transition")
	}
	w := httptest.NewRecorder()
	p.admin(w, httptest.NewRequest("GET", "/status", nil))
	if strings.Contains(logs.String()+w.Body.String(), secret) || strings.Contains(logs.String()+w.Body.String(), "private.invalid") || strings.Contains(logs.String()+w.Body.String(), "signed-payload") {
		t.Fatal("diagnostics leaked untrusted upstream information")
	}
	for _, field := range []string{`"event":"health_failure"`, `"event":"health_recovery"`, `"event":"routing_changed"`, `"http_status":503`, `"http_status":429`, `"timestamp":`, `"previous_mode":"local"`, `"new_mode":"external"`, `"local_height":`, `"external_height":`} {
		if !strings.Contains(logs.String(), field) {
			t.Fatal("missing structured log field", field)
		}
	}
	for _, field := range []string{`"external_check":`, `"local_check":`, `"chain_check":`, `"last_failure":`, `"last_recovery":`, `"last_transition":`} {
		if !strings.Contains(w.Body.String(), field) {
			t.Fatal("missing status diagnostic", field)
		}
	}
	if w.Body.Len() > 4096 {
		t.Fatalf("status exceeds CLI read limit: %d", w.Body.Len())
	}
}

func TestUpstreamErrorsRemainSanitizedAndClassifiable(t *testing.T) {
	p, _, _, _ := fixture(t)
	p.external.Client.Transport = probeFaultTransport{base: p.external.Client.Transport, stage: "common", code: 503, body: "secret"}
	_, err := p.external.call(context.Background(), "POST", "/wallet/getblockbynum", []byte(`{"num":10}`))
	if !errors.Is(err, errUpstream) || err.Error() != "http_status" || failureReasonFor(err) != reasonHTTP {
		t.Fatal("sanitized error compatibility changed")
	}
}

func TestConcurrentProbeAndManualAdmission(t *testing.T) {
	p, _, _, _ := fixture(t)
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 5 {
				p.probe(context.Background())
			}
		}()
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 5 {
				w := httptest.NewRecorder()
				p.admin(w, httptest.NewRequest("POST", "/arm-local", nil))
				if w.Code != 200 {
					t.Error("healthy admission failed")
				}
			}
		}()
	}
	wg.Wait()
	if !p.healthy() || p.currentMode() != "local" {
		t.Fatal("serialized admission lost health")
	}
}
