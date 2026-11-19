package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type admissionBody struct {
	io.Reader
	reads  atomic.Int32
	cancel context.CancelFunc
}

func (b *admissionBody) Read(dst []byte) (int, error) {
	b.reads.Add(1)
	if b.cancel != nil {
		b.cancel()
	}
	return b.Reader.Read(dst)
}

type admissionJournal struct {
	journal
	calls atomic.Int32
}

func (j *admissionJournal) mode() (string, error) {
	j.calls.Add(1)
	return j.journal.mode()
}

func (j *admissionJournal) setMode(mode string) error {
	j.calls.Add(1)
	return j.journal.setMode(mode)
}

func (j *admissionJournal) get(id string) (*Entry, error) {
	j.calls.Add(1)
	return j.journal.get(id)
}

func (j *admissionJournal) save(entry *Entry) error {
	j.calls.Add(1)
	return j.journal.save(entry)
}

func holdAdmission(t *testing.T, a *requestAdmission) func() {
	t.Helper()
	for range cap(a.active) {
		if err := a.acquire(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	var once sync.Once
	release := func() {
		once.Do(func() {
			for range cap(a.active) {
				a.release()
			}
		})
	}
	t.Cleanup(release)
	return release
}

func waitForAdmissionQueue(t *testing.T, a *requestAdmission, count int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for len(a.waiting) != count {
		if time.Now().After(deadline) {
			t.Fatalf("waiting requests: got %d, want %d", len(a.waiting), count)
		}
		time.Sleep(time.Millisecond)
	}
}

func admissionResponse(t *testing.T, responses <-chan *httptest.ResponseRecorder) *httptest.ResponseRecorder {
	t.Helper()
	select {
	case response := <-responses:
		return response
	case <-time.After(2 * time.Second):
		t.Fatal("admission did not finish")
		return nil
	}
}

func TestAdmissionLimits(t *testing.T) {
	a := newRequestAdmission()
	if cap(a.active) != 32 || cap(a.waiting) != 32 || a.waitTimeout != 5*time.Second {
		t.Fatal("unexpected admission budget")
	}
}

func TestAdmissionAlreadyCanceled(t *testing.T) {
	for _, saturated := range []bool{false, true} {
		name := "available"
		if saturated {
			name = "saturated"
		}
		t.Run(name, func(t *testing.T) {
			p, _, _, _ := fixture(t)
			j := &admissionJournal{journal: p.store}
			p.store = j
			if saturated {
				holdAdmission(t, p.admission)
			}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			body := &admissionBody{Reader: bytes.NewReader(signed(t, false, false, time.Now().Add(time.Hour)))}
			w := httptest.NewRecorder()
			p.ServeHTTP(w, httptest.NewRequest("POST", "/wallet/broadcasttransaction", body).WithContext(ctx))
			if w.Code != 503 || body.reads.Load() != 0 || j.calls.Load() != 0 || p.requests.Load() != 0 {
				t.Fatalf("canceled request had side effects: HTTP %d, reads=%d, journal=%d, requests=%d", w.Code, body.reads.Load(), j.calls.Load(), p.requests.Load())
			}
			if len(p.admission.waiting) != 0 || p.admission.canceled.Load() != 1 {
				t.Fatal("canceled admission leaked a queue slot or was not counted")
			}
			if !saturated && len(p.admission.active) != 0 {
				t.Fatal("canceled admission leaked an active slot")
			}
		})
	}
}

func TestAdmissionWaitingCancellationAndTimeout(t *testing.T) {
	for _, reason := range []string{"cancel", "deadline", "timeout"} {
		t.Run(reason, func(t *testing.T) {
			p, _, _, _ := fixture(t)
			p.setMode("local")
			j := &admissionJournal{journal: p.store}
			p.store = j
			release := holdAdmission(t, p.admission)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if reason == "timeout" {
				p.admission.waitTimeout = 30 * time.Millisecond
			} else if reason == "deadline" {
				var stop context.CancelFunc
				ctx, stop = context.WithTimeout(ctx, 30*time.Millisecond)
				defer stop()
			}
			body := &admissionBody{Reader: bytes.NewReader(signed(t, false, false, time.Now().Add(time.Hour)))}
			responses := make(chan *httptest.ResponseRecorder, 1)
			go func() {
				w := httptest.NewRecorder()
				p.ServeHTTP(w, httptest.NewRequest("POST", "/wallet/broadcasttransaction", body).WithContext(ctx))
				responses <- w
			}()
			if reason == "cancel" {
				waitForAdmissionQueue(t, p.admission, 1)
				cancel()
			}
			w := admissionResponse(t, responses)
			if w.Code != 503 || w.Header().Get("Retry-After") != "2" || body.reads.Load() != 0 || j.calls.Load() != 0 || p.requests.Load() != 0 {
				t.Fatalf("rejected waiter had side effects: HTTP %d, reads=%d, journal=%d, requests=%d", w.Code, body.reads.Load(), j.calls.Load(), p.requests.Load())
			}
			if len(p.admission.waiting) != 0 || len(p.admission.active) != maxActiveRequests {
				t.Fatal("rejected waiter leaked or consumed a permit")
			}
			if reason == "timeout" {
				if p.admission.timedOut.Load() != 1 || p.admission.canceled.Load() != 0 {
					t.Fatal("admission timeout not counted separately")
				}
			} else if p.admission.canceled.Load() != 1 || p.admission.timedOut.Load() != 0 {
				t.Fatal("caller cancellation not counted separately")
			}
			release()
			if w := request(p, "/wallet/getnowblock", []byte(`null`)); w.Code != 200 || p.currentMode() != "local" || p.fallbacks.Load() != 0 {
				t.Fatal("request admission did not recover without changing routing")
			}
		})
	}
}

func TestAdmissionQueueFullAndControlBypass(t *testing.T) {
	p, _, _, _ := fixture(t)
	release := holdAdmission(t, p.admission)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	results := make(chan error, 32)
	for range 32 {
		go func() { results <- p.admission.acquire(ctx) }()
	}
	waitForAdmissionQueue(t, p.admission, 32)
	body := &admissionBody{Reader: strings.NewReader(`null`)}
	w := httptest.NewRecorder()
	p.ServeHTTP(w, httptest.NewRequest("POST", "/wallet/getnowblock", body))
	if w.Code != 503 || w.Header().Get("Retry-After") != "2" || body.reads.Load() != 0 || p.requests.Load() != 0 || p.admission.rejected.Load() != 1 {
		t.Fatal("full queue was not rejected before reading the body", w.Code, w.Body.String())
	}
	for _, route := range []string{"/healthz", "/readyz", "/metrics"} {
		w = httptest.NewRecorder()
		p.ServeHTTP(w, httptest.NewRequest("GET", route, nil))
		if w.Code != 200 {
			t.Fatal("control endpoint blocked by saturation", route, w.Code)
		}
	}
	for _, metric := range []string{"tron_proxy_active_requests 32\n", "tron_proxy_waiting_requests 32\n", "tron_proxy_admission_rejected_total 1\n"} {
		if !strings.Contains(w.Body.String(), metric) {
			t.Fatal("missing admission metric", metric)
		}
	}
	w = httptest.NewRecorder()
	p.admin(w, httptest.NewRequest("GET", "/status", nil))
	if w.Code != 200 {
		t.Fatal("operator status blocked by saturation")
	}
	cancel()
	for range 32 {
		select {
		case err := <-results:
			if !errors.Is(err, context.Canceled) {
				t.Fatal("unexpected waiter result", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("queue did not drain on cancellation")
		}
	}
	if len(p.admission.waiting) != 0 || p.admission.canceled.Load() != 32 {
		t.Fatal("canceled queue leaked permits")
	}
	release()
	if w := request(p, "/wallet/getnowblock", []byte(`null`)); w.Code != 200 || len(p.admission.active) != 0 {
		t.Fatal("overloaded admission failed to recover")
	}
}

func TestAdmissionCancellationWinsReleasedPermit(t *testing.T) {
	for range 100 {
		a := newRequestAdmission()
		a.active = make(chan struct{}, 1)
		a.active <- struct{}{}
		ctx, cancel := context.WithCancel(context.Background())
		result := make(chan error, 1)
		go func() { result <- a.acquire(ctx) }()
		waitForAdmissionQueue(t, a, 1)
		cancel()
		a.release()
		select {
		case err := <-result:
			if !errors.Is(err, context.Canceled) {
				t.Fatal("canceled waiter acquired a simultaneously released permit", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("canceled waiter did not finish")
		}
		if len(a.active) != 0 || len(a.waiting) != 0 {
			t.Fatal("canceled waiter leaked a permit")
		}
	}
}

func TestAdmissionCancellationDuringBodyRead(t *testing.T) {
	p, _, _, _ := fixture(t)
	j := &admissionJournal{journal: p.store}
	p.store = j
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	body := &admissionBody{Reader: bytes.NewReader(signed(t, false, false, time.Now().Add(time.Hour))), cancel: cancel}
	w := httptest.NewRecorder()
	p.ServeHTTP(w, httptest.NewRequest("POST", "/wallet/broadcasttransaction", body).WithContext(ctx))
	if w.Code != 503 || body.reads.Load() == 0 || j.calls.Load() != 0 || p.broadcasts.Load() != 0 || len(p.admission.active) != 0 {
		t.Fatal("cancellation during body read reached journal or leaked a permit")
	}
}
