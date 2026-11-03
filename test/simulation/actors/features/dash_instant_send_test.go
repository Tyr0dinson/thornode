package features

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/config"
	. "gitlab.com/thorchain/thornode/v3/test/simulation/pkg/types"
	"gitlab.com/thorchain/thornode/v3/test/simulation/pkg/utxo"
)

type instantSendTestBridge struct {
	thorclient.ThorchainBridge
	values     map[string]int64
	errors     map[string]error
	reads      []string
	beforeRead func()
}

// GetMimir models the per-key endpoint, including an unset key's negative value.
func (b *instantSendTestBridge) GetMimir(key string) (int64, error) {
	if b.beforeRead != nil {
		b.beforeRead()
	}
	b.reads = append(b.reads, key)
	if err := b.errors[key]; err != nil {
		return 0, err
	}
	if value, ok := b.values[key]; ok {
		return value, nil
	}
	return -1, nil
}

// newInstantSendTestActor provides a ready node with the mocknet allowance default.
func newInstantSendTestActor() (*DashInstantSendActor, *instantSendTestBridge) {
	bridge := &instantSendTestBridge{
		values: map[string]int64{"DashInstantSendMaxAmount": 1000 * common.One},
		errors: map[string]error{},
	}
	actor := &DashInstantSendActor{
		getMimir: bridge.GetMimir,
		readMetrics: func() (instantSendMetrics, error) {
			return instantSendMetrics{available: 1000 * common.One}, nil
		},
	}
	return actor, bridge
}

func TestInstantSendReadinessAmountSettings(t *testing.T) {
	for _, tc := range []struct {
		name, key string
		value     int64
		wantError string
	}{
		{"mocknet defaults", "", 0, ""},
		{"enabled", "DashInstantSendEnabled", 1, ""},
		{"disabled", "DashInstantSendEnabled", 0, "explicitly disables"},
		{"unset maximum", "DashInstantSendMaxAmount", -1, "must be positive"},
		{"zero maximum", "DashInstantSendMaxAmount", 0, "must be positive"},
		{"insufficient maximum", "DashInstantSendMaxAmount", dashInstantSendLargePayment - 1, "cannot cover"},
		{"exact maximum", "DashInstantSendMaxAmount", dashInstantSendLargePayment, ""},
		{"minimum too high", "DashInstantSendMinAmount", dashInstantSendSmallPayment + 1, "exceeds payment"},
		{"exact minimum", "DashInstantSendMinAmount", dashInstantSendSmallPayment, ""},
		{"minimum disabled", "DashInstantSendMinAmount", 0, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, bridge := newInstantSendTestActor()
			bridge.values[tc.key] = tc.value
			config := &OpConfig{NodeUsers: []*User{{Thorchain: bridge}}}
			result := a.waitForReadiness(config)
			if tc.wantError == "" {
				if !result.Continue || result.Finish || result.Error != nil {
					t.Fatalf("valid settings rejected: %+v", result)
				}
			} else if !result.Finish || result.Continue || result.Error == nil || !strings.Contains(result.Error.Error(), tc.wantError) {
				t.Fatalf("expected immediate failure containing %q, got %+v", tc.wantError, result)
			}
			if a.account != nil {
				t.Fatal("readiness acquired a user account")
			}
		})
	}
}

func TestInstantSendReadinessDefaultMinimum(t *testing.T) {
	a, bridge := newInstantSendTestActor()
	retry, err := a.checkReadiness(common.One/100-1, common.One/100-1)
	if err == nil || retry {
		t.Fatalf("unset minimum must reject a deposit below 0.01 DASH: retry=%v, err=%v", retry, err)
	}
	bridge.values["DashInstantSendMinAmount"] = 0
	_, err = a.checkReadiness(1, 1)
	if err != nil {
		t.Fatalf("zero minimum must allow small deposits: %v", err)
	}
}

func TestInstantSendReadinessRetriesLookupFailures(t *testing.T) {
	keys := []string{"DashInstantSendEnabled", "DashInstantSendMinAmount", "DashInstantSendMaxAmount"}
	for i, key := range keys {
		t.Run(key, func(t *testing.T) {
			a, bridge := newInstantSendTestActor()
			failed := errors.New("node unavailable")
			bridge.errors[key] = failed
			config := &OpConfig{NodeUsers: []*User{{Thorchain: bridge}}}
			result := a.waitForReadiness(config)
			if result.Finish || result.Continue || !errors.Is(result.Error, failed) {
				t.Fatalf("lookup failure must retry: %+v", result)
			}
			if len(bridge.reads) != i+1 {
				t.Fatalf("made more lookups after failure: %v", bridge.reads)
			}
			delete(bridge.errors, key)
			result = a.waitForReadiness(config)
			if !result.Continue || result.Error != nil {
				t.Fatalf("did not recover: %+v", result)
			}
		})
	}
}

func TestInstantSendReadinessWarmupAndAllowance(t *testing.T) {
	a, bridge := newInstantSendTestActor()
	config := &OpConfig{NodeUsers: []*User{{Thorchain: bridge}}}
	state := instantSendMetrics{warmingUp: true, available: 1000 * common.One}
	var scrapeErr error = errors.New("metrics unavailable")
	a.readMetrics = func() (instantSendMetrics, error) { return state, scrapeErr }
	for _, reason := range []string{"metrics unavailable", "warming up", "available allowance"} {
		result := a.waitForReadiness(config)
		if result.Finish || result.Continue || result.Error == nil || !strings.Contains(result.Error.Error(), reason) {
			t.Fatalf("expected retry for %s: %+v", reason, result)
		}
		if scrapeErr != nil {
			scrapeErr = nil
		} else {
			state.warmingUp = false
			state.available = dashInstantSendLargePayment - 1
		}
	}
	state.available = dashInstantSendLargePayment
	result := a.waitForReadiness(config)
	if !result.Continue || result.Error != nil {
		t.Fatalf("exactly enough allowance must be ready: %+v", result)
	}
}

func TestInstantSendReadinessTimeout(t *testing.T) {
	for _, reason := range []string{"warming up", "metrics unavailable", "node unavailable"} {
		t.Run(reason, func(t *testing.T) {
			a, bridge := newInstantSendTestActor()
			a.readMetrics = func() (instantSendMetrics, error) {
				if reason == "metrics unavailable" {
					return instantSendMetrics{}, errors.New(reason)
				}
				return instantSendMetrics{warmingUp: true}, nil
			}
			if reason == "node unavailable" {
				bridge.errors["DashInstantSendEnabled"] = errors.New(reason)
			}
			config := &OpConfig{NodeUsers: []*User{{Thorchain: bridge}}}
			result := a.waitForReadiness(config)
			if result.Finish || result.Continue || result.Error == nil {
				t.Fatalf("temporary failure must retry first: %+v", result)
			}
			a.readyDeadline = time.Now().Add(-time.Second)
			result = a.waitForReadiness(config)
			if !result.Finish || result.Error == nil || !strings.Contains(result.Error.Error(), "timed out") || !strings.Contains(result.Error.Error(), reason) {
				t.Fatalf("timeout must include the last failure: %+v", result)
			}
		})
	}
}

func TestInstantSendReadinessTimeoutWithStalledLookup(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		a, bridge := newInstantSendTestActor()
		pending := make(chan struct{})
		defer close(pending)
		bridge.beforeRead = func() { <-pending }
		started := time.Now()
		result := a.waitForReadiness(&OpConfig{NodeUsers: []*User{{Thorchain: bridge}}})
		if !result.Finish || result.Error == nil || !strings.Contains(result.Error.Error(), "lookup did not finish") {
			t.Fatalf("stalled lookup did not time out: %+v", result)
		}
		if elapsed := time.Since(started); elapsed != 2*time.Minute {
			t.Fatalf("readiness deadline took %s instead of two minutes", elapsed)
		}
	})
}

func TestInstantSendReadinessRunsBeforeAcquiringAccount(t *testing.T) {
	a := DashInstantSend(rand.New(rand.NewPCG(1, 2)))
	if !strings.Contains(a.Ops[0].String(), "waitForReadiness") || a.Interval != time.Second {
		t.Fatal("readiness must be the first operation, retried once per second")
	}
	for _, config := range []*OpConfig{nil, {}, {NodeUsers: []*User{nil}}, {NodeUsers: []*User{{}}}} {
		result := a.Ops[0](config)
		if !result.Finish || result.Error == nil {
			t.Fatalf("missing node bridge must fail clearly: %+v", result)
		}
	}
}

type instantSendTestChainClient struct {
	LiteChainClient
	signed, broadcast int
}

// SignTx records whether a readiness failure allowed funds to be signed.
func (c *instantSendTestChainClient) SignTx(SimTx) ([]byte, error) {
	c.signed++
	return []byte("signed"), nil
}

// BroadcastTx records whether a readiness failure allowed funds to be sent.
func (c *instantSendTestChainClient) BroadcastTx([]byte) (string, error) {
	c.broadcast++
	return "txid", nil
}

func TestInstantSendBroadcastRechecksReadiness(t *testing.T) {
	for _, failure := range []string{"disabled", "maximum lowered", "minimum raised", "lookup failure", "warming up", "allowance exhausted", "scrape failure"} {
		t.Run(failure, func(t *testing.T) {
			a, bridge := newInstantSendTestActor()
			chain := &instantSendTestChainClient{}
			a.account = &User{ChainClients: map[common.Chain]LiteChainClient{common.DASHChain: chain}}
			_, err := a.checkReadiness(dashInstantSendSmallPayment, dashInstantSendLargePayment)
			if err != nil {
				t.Fatal(err)
			}
			// Change policy or readiness after startup succeeded.
			switch failure {
			case "disabled":
				bridge.values["DashInstantSendEnabled"] = 0
			case "maximum lowered":
				bridge.values["DashInstantSendMaxAmount"] = 1
			case "minimum raised":
				bridge.values["DashInstantSendMinAmount"] = dashInstantSendLargePayment + 1
			case "lookup failure":
				bridge.errors["DashInstantSendEnabled"] = errors.New("lookup failed")
			default:
				a.readMetrics = func() (instantSendMetrics, error) {
					if failure == "scrape failure" {
						return instantSendMetrics{}, errors.New("scrape failed")
					}
					return instantSendMetrics{warmingUp: failure == "warming up"}, nil
				}
			}
			_, err = a.broadcastTx("test", dashInstantSendSmallPayment, 0)
			if err == nil || chain.signed != 0 || chain.broadcast != 0 {
				t.Fatalf("unready actor spent funds: signed=%d, broadcast=%d, err=%v", chain.signed, chain.broadcast, err)
			}
		})
	}
}

func TestInstantSendBroadcastWhenReady(t *testing.T) {
	a, _ := newInstantSendTestActor()
	chain := &instantSendTestChainClient{}
	a.account = &User{ChainClients: map[common.Chain]LiteChainClient{common.DASHChain: chain}}
	a.readMetrics = func() (instantSendMetrics, error) {
		return instantSendMetrics{available: dashInstantSendLargePayment}, nil
	}
	id, err := a.broadcastTx("test", dashInstantSendSmallPayment, 0)
	if err != nil || id != "txid" || chain.signed != 1 || chain.broadcast != 1 {
		t.Fatalf("ready actor did not broadcast: id=%s, err=%v", id, err)
	}
}

// initInstantSendFundingTest restores the global config after its environment resets.
func initInstantSendFundingTest(t *testing.T) {
	t.Helper()
	t.Cleanup(config.Init)
	t.Setenv("CHAIN_ID", "thorchain-mocknet-v1")
	config.Init()
}

// newInstantSendFundingUser uses the real Dash client against an account-only RPC.
func newInstantSendFundingUser(t *testing.T, balance *atomic.Uint64, fail *atomic.Bool) *User {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Method string          `json:"method"`
			ID     json.RawMessage `json:"id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode RPC request: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		response := map[string]any{"jsonrpc": "2.0", "id": request.ID}
		switch request.Method {
		case "getaddressinfo":
			response["result"] = map[string]any{"iswatchonly": true}
		case "listunspent":
			if fail.Load() {
				response["error"] = map[string]any{"code": -1, "message": "account unavailable"}
				break
			}
			amount := balance.Load()
			response["result"] = []map[string]any{{
				"amount": json.Number(fmt.Sprintf("%d.%08d", amount/common.One, amount%common.One)),
			}}
		default:
			t.Errorf("account selection made unexpected RPC %s", request.Method)
			response["error"] = map[string]any{"code": -32601, "message": "unexpected method"}
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(response); err != nil {
			t.Errorf("encode RPC response: %v", err)
		}
	}))
	t.Cleanup(server.Close)
	return NewUser(strings.Repeat("dog ", 23)+"fossil", map[common.Chain]LiteChainClientConstructor{
		common.DASHChain: utxo.NewConstructor(server.URL),
	})
}

// TestInstantSendRequiredBalance checks the complete retry budget and fee changes.
func TestInstantSendRequiredBalance(t *testing.T) {
	a, _ := newInstantSendTestActor()
	for _, tc := range []struct {
		fee, want uint64
	}{
		{15_000, 5_800_306_000}, // 58 DASH plus all thirty fees
		{0, 5_800_006_000},
		{30_000, 5_800_606_000},
	} {
		if got := a.requiredBalance(tc.fee); got != tc.want {
			t.Fatalf("fee=%d: budget=%d, want %d", tc.fee, got, tc.want)
		}
	}
}

// TestInstantSendAcquireUserChecksBalance covers balance boundaries, RPC failure
// and busy accounts without changing the production client's concrete type.
func TestInstantSendAcquireUserChecksBalance(t *testing.T) {
	initInstantSendFundingTest(t)
	for _, tc := range []struct {
		name          string
		amount        uint64
		fail, busy    bool
		wantSelection bool
	}{
		{name: "old seed insufficient", amount: 10 * common.One},
		{name: "one duff short", amount: 5_800_305_999},
		{name: "exact budget", amount: 5_800_306_000, wantSelection: true},
		{name: "new seed", amount: 100 * common.One, wantSelection: true},
		{name: "account read fails", amount: 100 * common.One, fail: true},
		{name: "account busy", amount: 100 * common.One, busy: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var balance atomic.Uint64
			var fail atomic.Bool
			balance.Store(tc.amount)
			fail.Store(tc.fail)
			user := newInstantSendFundingUser(t, &balance, &fail)
			if tc.busy {
				if !user.Acquire() {
					t.Fatal("failed to reserve busy user")
				}
				t.Cleanup(user.Release)
			}
			a, _ := newInstantSendTestActor()
			result := a.acquireUser(&OpConfig{Users: []*User{user}})
			if result.Finish || result.Continue != tc.wantSelection {
				t.Fatalf("unexpected acquisition result: %+v", result)
			}
			if tc.wantSelection {
				if result.Error != nil || a.account != user || a.dash == nil || !user.IsLocked() {
					t.Fatalf("funded account was not reserved: %+v", result)
				}
				a.releaseAccount(nil)
			} else {
				if result.Error == nil || !strings.Contains(result.Error.Error(), "retry budget") {
					t.Fatalf("missing acquisition diagnostic: %+v", result)
				}
				if a.account != nil || a.dash != nil || user.IsLocked() != tc.busy {
					t.Fatal("rejected account was retained or another actor's lock was released")
				}
			}
		})
	}
}

// TestInstantSendAcquireUserTriesNextAccount ensures one unavailable account does
// not prevent using a later funded account, and releases the unsuccessful candidate.
func TestInstantSendAcquireUserTriesNextAccount(t *testing.T) {
	initInstantSendFundingTest(t)
	for _, readFails := range []bool{false, true} {
		var poorBalance, richBalance atomic.Uint64
		var poorFailure, richFailure atomic.Bool
		poorBalance.Store(10 * common.One)
		richBalance.Store(100 * common.One)
		poorFailure.Store(readFails)
		poor := newInstantSendFundingUser(t, &poorBalance, &poorFailure)
		rich := newInstantSendFundingUser(t, &richBalance, &richFailure)
		a, _ := newInstantSendTestActor()
		result := a.acquireUser(&OpConfig{Users: []*User{poor, rich}})
		if !result.Continue || result.Error != nil || a.account != rich || poor.IsLocked() || !rich.IsLocked() {
			t.Fatalf("failed to select the later funded user: %+v", result)
		}
		a.releaseAccount(nil)
	}
}

// TestInstantSendAcquireUserRetriesFunding ensures a later poll can select an
// account once funds become available, without reserving it during the wait.
func TestInstantSendAcquireUserRetriesFunding(t *testing.T) {
	initInstantSendFundingTest(t)
	var balance atomic.Uint64
	var fail atomic.Bool
	user := newInstantSendFundingUser(t, &balance, &fail)
	a, _ := newInstantSendTestActor()
	opConfig := &OpConfig{Users: []*User{user}}
	result := a.acquireUser(opConfig)
	if result.Continue || result.Finish || result.Error == nil || user.IsLocked() {
		t.Fatalf("unfunded account must remain available for retry: %+v", result)
	}
	balance.Store(100 * common.One)
	result = a.acquireUser(opConfig)
	if !result.Continue || result.Error != nil || a.account != user {
		t.Fatalf("funded account was not selected on retry: %+v", result)
	}
	a.releaseAccount(nil)
}

func TestScrapeInstantSendMetricsTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer server.Close()
	_, err := scrapeInstantSendMetrics(server.URL)
	var timeout net.Error
	if !errors.As(err, &timeout) || !timeout.Timeout() {
		t.Fatalf("stalled scrape must time out: %v", err)
	}
}

func TestScrapeInstantSendMetrics(t *testing.T) {
	valid := "# HELP dash_instantsend_available_duffs Available allowance\n" +
		"dash_instantsend_available_duffs 220000000\n" +
		"dash_instantsend_warming_up 0\n"
	for _, tc := range []struct {
		name, body string
		status     int
		wantError  string
	}{
		{"valid", valid, http.StatusOK, ""},
		{"empty", "", http.StatusOK, "missing"},
		{"wrong allowance name", strings.ReplaceAll(valid, "dash_instantsend_available_duffs", "dash_instantsend_available_duffs_extra"), http.StatusOK, "missing"},
		{"unrelated counter ignored", valid + "dash_instantsend_observed_total NaN\n", http.StatusOK, ""},
		{"missing allowance", strings.ReplaceAll(valid, "dash_instantsend_available_duffs 220000000\n", ""), http.StatusOK, "missing"},
		{"missing warmup", strings.ReplaceAll(valid, "dash_instantsend_warming_up 0\n", ""), http.StatusOK, "missing"},
		{"malformed", strings.ReplaceAll(valid, "duffs 220000000", "duffs"), http.StatusOK, "malformed"},
		{"invalid number", strings.ReplaceAll(valid, "duffs 220000000", "duffs nope"), http.StatusOK, "parse"},
		{"negative", strings.ReplaceAll(valid, "duffs 220000000", "duffs -1"), http.StatusOK, "invalid"},
		{"NaN", strings.ReplaceAll(valid, "duffs 220000000", "duffs NaN"), http.StatusOK, "invalid"},
		{"infinity", strings.ReplaceAll(valid, "duffs 220000000", "duffs +Inf"), http.StatusOK, "invalid"},
		{"invalid warmup", strings.ReplaceAll(valid, "up 0", "up 0.5"), http.StatusOK, "invalid"},
		{"HTTP failure", valid, http.StatusServiceUnavailable, "HTTP 503"},
		{"truncated scrape", valid + strings.Repeat("x", 70_000), http.StatusOK, "token too long"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.body)
			}))
			defer server.Close()
			state, err := scrapeInstantSendMetrics(server.URL)
			if tc.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantError) {
					t.Fatalf("expected %q, got %v", tc.wantError, err)
				}
				return
			}
			if err != nil || state.available != 220000000 || state.warmingUp {
				t.Fatalf("incorrect metric snapshot: %+v, %v", state, err)
			}
		})
	}
}
