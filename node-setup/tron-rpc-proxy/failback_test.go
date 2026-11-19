package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

type recoveryFixture struct {
	p               *Proxy
	s               *Store
	local, external *fakeNode
	now             time.Time
}

func recovery(t *testing.T) *recoveryFixture {
	t.Helper()
	p, s, local, external := fixture(t)
	f := &recoveryFixture{p: p, s: s, local: local, external: external, now: time.Now()}
	p.cfg.AutoFailbackEnabled = true
	p.now = func() time.Time { return f.now }
	p.setMode("local")
	local.mu.Lock()
	local.age = time.Minute
	local.mu.Unlock()
	p.probe(context.Background())
	if p.currentMode() != "external" {
		t.Fatal("stale local did not trigger fallback")
	}
	local.mu.Lock()
	local.age = 0
	local.mu.Unlock()
	return f
}

func (f *recoveryFixture) step(delta time.Duration) Health {
	f.now = f.now.Add(delta)
	for _, n := range []*fakeNode{f.local, f.external} {
		n.mu.Lock()
		n.head++
		n.mu.Unlock()
	}
	return f.p.probe(context.Background())
}

func (f *recoveryFixture) requireMode(t *testing.T, want string) {
	t.Helper()
	if got := f.p.currentMode(); got != want {
		t.Fatalf("mode = %s, want %s", got, want)
	}
}

func (f *recoveryFixture) fullMinute(t *testing.T) {
	t.Helper()
	f.step(0)
	for i := 0; i < 5; i++ {
		f.step(10 * time.Second)
		f.requireMode(t, "external")
	}
	f.step(9 * time.Second)
	f.requireMode(t, "external")
	f.step(time.Second)
	f.requireMode(t, "local")
}

func TestAutoFailbackRequiresFullMinute(t *testing.T) {
	f := recovery(t)
	f.fullMinute(t)
	d := f.p.recentDiagnostics().LastTransition
	if d == nil || d.Stage != "auto_failback" || d.Reason != reasonOK {
		t.Fatalf("missing automatic return diagnostic: %+v", d)
	}
	mode, err := f.s.mode()
	if err != nil || mode != "local" {
		t.Fatal("automatic return was not journaled", mode, err)
	}
}

func TestAutoFailbackResetsOnFailedCheck(t *testing.T) {
	for _, kind := range []string{"stale", "fork", "behind", "external", "genesis"} {
		t.Run(kind, func(t *testing.T) {
			f := recovery(t)
			f.step(0)
			for i := 0; i < 5; i++ {
				f.step(10 * time.Second)
			}
			f.local.mu.Lock()
			f.external.mu.Lock()
			switch kind {
			case "stale":
				f.local.age = time.Minute
			case "fork":
				f.local.fork = true
			case "behind":
				f.local.head -= 10
			case "external":
				f.external.age = time.Minute
			case "genesis":
				f.local.genesis = "wrong"
			}
			f.external.mu.Unlock()
			f.local.mu.Unlock()
			f.step(10 * time.Second)
			f.requireMode(t, "external")
			f.local.mu.Lock()
			f.external.mu.Lock()
			f.local.age, f.external.age, f.local.fork = 0, 0, false
			f.local.genesis = testGenesis
			f.local.head = f.external.head
			f.external.mu.Unlock()
			f.local.mu.Unlock()
			f.fullMinute(t)
		})
	}
}

func TestAutoFailbackResetsOnMonitoringGap(t *testing.T) {
	f := recovery(t)
	f.step(0)
	for i := 0; i < 5; i++ {
		f.step(10 * time.Second)
	}
	f.step(time.Minute)
	f.requireMode(t, "external")
	f.fullMinute(t)
}

func TestAutoFailbackManualExternalHolds(t *testing.T) {
	f := recovery(t)
	f.step(0)
	for i := 0; i < 5; i++ {
		f.step(10 * time.Second)
	}
	w := httptest.NewRecorder()
	f.p.admin(w, httptest.NewRequest("POST", "/external", nil))
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	// A late in-flight local read failure must not undo the operator's hold.
	f.p.setModeFor("external", "read", reasonTimeout, "local")
	for i := 0; i < 12; i++ {
		f.step(10 * time.Second)
		f.requireMode(t, "external")
	}
}

func TestAutoFailbackDisabledAndStartupStayExternal(t *testing.T) {
	for _, kind := range []string{"disabled", "restart"} {
		t.Run(kind, func(t *testing.T) {
			f := recovery(t)
			f.step(0)
			if kind == "disabled" {
				f.p.cfg.AutoFailbackEnabled = false
			} else {
				f.p = newProxy(f.p.cfg, f.s, f.p.local, f.p.external)
				f.p.now = func() time.Time { return f.now }
				if !f.p.setModeFor("external", "startup", reasonStartup, "operator") {
					t.Fatal("startup")
				}
			}
			for i := 0; i < 12; i++ {
				f.step(10 * time.Second)
				f.requireMode(t, "external")
			}
		})
	}
}

func TestAutoFailbackJournalFailureClosesAdmission(t *testing.T) {
	for _, kind := range []string{"already-unsafe", "mode-write"} {
		t.Run(kind, func(t *testing.T) {
			f := recovery(t)
			f.step(0)
			for i := 0; i < 5; i++ {
				f.step(10 * time.Second)
			}
			if kind == "already-unsafe" {
				f.p.unsafe.Store(true)
			} else {
				f.p.store = brokenMode{f.s}
			}
			f.step(10 * time.Second)
			f.requireMode(t, "external")
			if !f.p.unsafe.Load() || f.p.healthy() || f.p.autoFailbacks.Load() != 0 {
				t.Fatal("unsafe automatic admission")
			}
		})
	}
}

func TestAutoFailbackDiscardsProbeRacingWithFailure(t *testing.T) {
	for _, kind := range []string{"operator", "read"} {
		t.Run(kind, func(t *testing.T) {
			f := recovery(t)
			f.step(0)
			for i := 0; i < 5; i++ {
				f.step(10 * time.Second)
			}
			base := f.p.external.Client.Transport
			var once sync.Once
			f.p.external.Client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
				resp, err := base.RoundTrip(r)
				once.Do(func() {
					if kind == "operator" {
						f.p.setMode("external")
					} else {
						f.p.setModeFor("external", "read", reasonTimeout, "local")
					}
				})
				return resp, err
			})
			f.step(10 * time.Second)
			f.requireMode(t, "external")
			if f.p.failbackStatus().HealthySince != nil {
				t.Fatal("counted an invalidated in-flight probe")
			}
			f.p.external.Client.Transport = base
			if kind == "read" {
				f.fullMinute(t)
			} else {
				for i := 0; i < 12; i++ {
					f.step(10 * time.Second)
					f.requireMode(t, "external")
				}
			}
		})
	}
}

func TestAutoFailbackRequiresAdvancingHeads(t *testing.T) {
	f := recovery(t)
	f.step(0)
	for i := 0; i < 8; i++ {
		f.now = f.now.Add(10 * time.Second)
		f.p.probe(context.Background())
		f.requireMode(t, "external")
	}
	f.step(10 * time.Second)
	f.requireMode(t, "local")
}

func TestAutoFailbackRegressionAndClockReversalResetWindow(t *testing.T) {
	for _, kind := range []string{"height", "clock"} {
		t.Run(kind, func(t *testing.T) {
			f := recovery(t)
			f.step(0)
			for i := 0; i < 5; i++ {
				f.step(10 * time.Second)
			}
			if kind == "height" {
				for _, n := range []*fakeNode{f.local, f.external} {
					n.mu.Lock()
					n.head -= 3
					n.mu.Unlock()
				}
				f.step(10 * time.Second)
			} else {
				f.step(-time.Second)
			}
			f.requireMode(t, "external")
			f.fullMinute(t)
		})
	}
}

func TestAutoFailbackStaleProbeCannotAdmit(t *testing.T) {
	for _, offset := range []time.Duration{-time.Minute, time.Minute} {
		f := recovery(t)
		f.step(0)
		for i := 0; i < 5; i++ {
			f.step(10 * time.Second)
		}
		h := f.p.snapshot()
		h.Checked = time.Now().Add(offset)
		f.now = f.now.Add(10 * time.Second)
		f.p.considerFailback(h, f.p.modeEpoch)
		f.requireMode(t, "external")
		if f.p.failbackStatus().HealthySince != nil {
			t.Fatal("stale/future health counted")
		}
		f.fullMinute(t)
	}
}

func TestAutoFailbackStatusAndRepeatedOutage(t *testing.T) {
	f := recovery(t)
	f.step(0)
	f.step(10 * time.Second)
	w := httptest.NewRecorder()
	f.p.admin(w, httptest.NewRequest("GET", "/status", nil))
	var result struct {
		Failback FailbackStatus `json:"failback"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	s := result.Failback
	if !s.Enabled || !s.Eligible || s.HealthySince == nil || s.HealthySeconds != 10 || s.RequiredSeconds != 60 {
		t.Fatalf("bad status: %+v", s)
	}
	for i := 0; i < 5; i++ {
		f.step(10 * time.Second)
	}
	f.requireMode(t, "local")
	if f.p.autoFailbacks.Load() != 1 {
		t.Fatal("missing counter")
	}
	f.local.mu.Lock()
	f.local.age = time.Minute
	f.local.mu.Unlock()
	f.step(10 * time.Second)
	f.requireMode(t, "external")
	f.local.mu.Lock()
	f.local.age = 0
	f.local.mu.Unlock()
	f.fullMinute(t)
	if f.p.autoFailbacks.Load() != 2 {
		t.Fatal("second outage did not restart observation window")
	}
}

func TestAutoFailbackReadFallbackAndManualRearm(t *testing.T) {
	f := recovery(t)
	f.p.setMode("external")
	w := httptest.NewRecorder()
	f.p.admin(w, httptest.NewRequest("POST", "/arm-local", nil))
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	f.local.mu.Lock()
	f.local.missing = true
	f.local.mu.Unlock()
	if w := request(f.p, "/wallet/getnowblock", []byte(`null`)); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	f.requireMode(t, "external")
	f.local.mu.Lock()
	f.local.missing = false
	f.local.mu.Unlock()
	f.fullMinute(t)
}
