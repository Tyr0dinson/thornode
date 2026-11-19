package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const bulkReceiptsRoute = "/wallet/gettransactioninfobyblocknum"

func bulkReceipt(id string, height int64) string {
	return fmt.Sprintf(`{"id":%q,"blockNumber":%d,"blockTimeStamp":1700000000000,"fee":100}`, id, height)
}

func TestBulkReceiptsRequestSupported(t *testing.T) {
	if !allowed(http.MethodPost, "/wallet/gettransactioninfobyblocknum", []byte(`{"num":90001}`)) {
		t.Fatal("valid bulk receipt request rejected")
	}
}

func TestBulkReceiptsResponseSupported(t *testing.T) {
	response := []byte(fmt.Sprintf(`[{"id":%q,"blockNumber":90001,"blockTimeStamp":1700000000000,"fee":100}]`, strings.Repeat("a", 64)))
	if !validResponse("/wallet/gettransactioninfobyblocknum", []byte(`{"num":90001}`), response) {
		t.Fatal("valid bulk receipt array rejected")
	}
}

func TestBulkReceiptsRequestValidation(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		valid      bool
	}{
		{"zero", `{"num":0}`, true},
		{"height", ` { "num":90001 } `, true},
		{"max-int64", `{"num":9223372036854775807}`, true},
		{"missing", `{}`, false},
		{"null", `null`, false},
		{"null-height", `{"num":null}`, false},
		{"negative", `{"num":-1}`, false},
		{"fraction", `{"num":1.5}`, false},
		{"decimal", `{"num":1.0}`, false},
		{"exponent", `{"num":1e3}`, false},
		{"string", `{"num":"90001"}`, false},
		{"bool", `{"num":true}`, false},
		{"array", `[{"num":90001}]`, false},
		{"overflow", `{"num":9223372036854775808}`, false},
		{"duplicate", `{"num":90001,"num":90002}`, false},
		{"escaped-duplicate", `{"num":90001,"n\u0075m":90002}`, false},
		{"case-alias", `{"Num":90001}`, false},
		{"case-duplicate", `{"num":90001,"NUM":90002}`, false},
		{"extra-field", `{"num":90001,"other":true}`, false},
		{"trailing", `{"num":90001} {}`, false},
		{"malformed", `{"num":}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := allowed(http.MethodPost, bulkReceiptsRoute, []byte(tc.body)); got != tc.valid {
				t.Fatalf("allowed=%t, want %t", got, tc.valid)
			}
		})
	}
	for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodDelete, http.MethodHead, http.MethodPatch} {
		if allowed(method, bulkReceiptsRoute, []byte(`{"num":90001}`)) {
			t.Errorf("accepted %s", method)
		}
	}
}

func TestBulkReceiptsResponseValidation(t *testing.T) {
	id := strings.Repeat("a", 64)
	item := bulkReceipt(id, 90001)
	for _, tc := range []struct {
		name, body string
		valid      bool
	}{
		{"empty", `[]`, true},
		{"partial", `[` + item + `]`, true},
		{"minimal-identity", fmt.Sprintf(`[{"id":%q,"blockNumber":90001}]`, id), true},
		{"multiple", `[` + item + `,` + bulkReceipt(strings.Repeat("b", 64), 90001) + `]`, true},
		{"failed-execution", `[` + strings.TrimSuffix(item, `}`) + `,"result":"FAILED","receipt":{"result":"REVERT"}}]`, true},
		{"null", `null`, false},
		{"object", `{}`, false},
		{"error", `{"Error":"unsupported"}`, false},
		{"wrapped", `{"receipts":[` + item + `]}`, false},
		{"null-entry", `[null]`, false},
		{"empty-entry", `[{}]`, false},
		{"nested-array", `[[]]`, false},
		{"wrong-height", `[` + bulkReceipt(id, 90002) + `]`, false},
		{"mixed-height", `[` + item + `,` + bulkReceipt(strings.Repeat("b", 64), 90002) + `]`, false},
		{"negative-height", `[` + bulkReceipt(id, -1) + `]`, false},
		{"bad-id", `[` + bulkReceipt("invalid", 90001) + `]`, false},
		{"non-hex-id", `[` + bulkReceipt(strings.Repeat("g", 64), 90001) + `]`, false},
		{"duplicate-id", `[` + item + `,` + item + `]`, false},
		{"case-duplicate-id", `[` + item + `,` + bulkReceipt(strings.ToUpper(id), 90001) + `]`, false},
		{"missing-id", `[{"blockNumber":90001,"blockTimeStamp":1}]`, false},
		{"missing-height", fmt.Sprintf(`[{"id":%q,"blockTimeStamp":1}]`, id), false},
		{"null-height", strings.ReplaceAll(`[`+item+`]`, `"blockNumber":90001`, `"blockNumber":null`), false},
		{"string-height", strings.ReplaceAll(`[`+item+`]`, `"blockNumber":90001`, `"blockNumber":"90001"`), false},
		{"fraction-height", strings.ReplaceAll(`[`+item+`]`, `"blockNumber":90001`, `"blockNumber":90001.0`), false},
		{"aliased-height", strings.ReplaceAll(`[`+item+`]`, `"blockNumber"`, `"BlockNumber"`), false},
		{"aliased-id", strings.ReplaceAll(`[`+item+`]`, `"id"`, `"ID"`), false},
		{"duplicate-key", `[` + strings.TrimSuffix(item, `}`) + `,"id":"bad"}]`, false},
		{"case-duplicate-key", `[` + strings.TrimSuffix(item, `}`) + `,"ID":"bad"}]`, false},
		{"nested-duplicate", `[` + strings.TrimSuffix(item, `}`) + `,"receipt":{"result":"SUCCESS","result":"FAILED"}}]`, false},
		{"entry-error", `[` + strings.TrimSuffix(item, `}`) + `,"Error":"unavailable"}]`, false},
		{"entry-error-null", `[` + strings.TrimSuffix(item, `}`) + `,"error":null}]`, false},
		{"entry-error-alias", `[` + strings.TrimSuffix(item, `}`) + `,"ERROR":"disabled"}]`, false},
		{"excessive-depth", `[` + strings.TrimSuffix(item, `}`) + `,"extra":` + strings.Repeat(`[`, 33) + `0` + strings.Repeat(`]`, 33) + `}]`, false},
		{"trailing", `[` + item + `] []`, false},
		{"truncated", `[` + item, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := validResponse(bulkReceiptsRoute, []byte(`{"num":90001}`), []byte(tc.body)); got != tc.valid {
				t.Fatalf("validResponse=%t, want %t", got, tc.valid)
			}
		})
	}
	if !validResponse(bulkReceiptsRoute, []byte(`{"num":0}`), []byte(`[`+bulkReceipt(id, 0)+`]`)) {
		t.Fatal("explicit zero height rejected")
	}
	for _, q := range []string{`{}`, `{"num":null}`, `{"num":-1}`, `{"num":90001,"num":90002}`} {
		if validResponse(bulkReceiptsRoute, []byte(q), []byte(`[]`)) {
			t.Errorf("validated against invalid request %s", q)
		}
	}
}

// Intercept only the optional endpoint, leaving health and individual reads real.
func mockBulkResponse(p *Upstream, code int, body string, fault error) *atomic.Int64 {
	calls := new(atomic.Int64)
	base := p.Client.Transport
	p.Client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != bulkReceiptsRoute {
			return base.RoundTrip(r)
		}
		calls.Add(1)
		if fault != nil {
			return nil, fault
		}
		return &http.Response{StatusCode: code, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})
	return calls
}

func bulkReadCount(n *fakeNode, route string) int {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.reads[route]
}

func TestBulkReceiptsRouting(t *testing.T) {
	localData := `[` + bulkReceipt(strings.Repeat("a", 64), 90001) + `]`
	externalData := `[` + bulkReceipt(strings.Repeat("b", 64), 90001) + `]`
	for _, tc := range []struct {
		name, mode, localData, externalData string
		localCode, externalCode, wantCode   int
		localFault                          error
		wantLocal, wantExternal             int64
		wantBody                            string
	}{
		{"local", "local", localData, externalData, 200, 200, 200, nil, 1, 0, localData},
		{"external", "external", localData, externalData, 200, 200, 200, nil, 0, 1, externalData},
		{"local-empty", "local", `[]`, externalData, 200, 200, 200, nil, 1, 0, `[]`},
		{"external-empty", "external", localData, `[]`, 200, 200, 200, nil, 0, 1, `[]`},
		{"unsupported", "local", `{}`, externalData, 404, 200, 200, nil, 1, 1, externalData},
		{"pruned-object", "local", `{}`, externalData, 200, 200, 200, nil, 1, 1, externalData},
		{"error-envelope", "local", `{"Error":"disabled"}`, externalData, 200, 200, 200, nil, 1, 1, externalData},
		{"invalid-json", "local", `not-json`, externalData, 200, 200, 200, nil, 1, 1, externalData},
		{"wrong-height", "local", `[` + bulkReceipt(strings.Repeat("a", 64), 90000) + `]`, externalData, 200, 200, 200, nil, 1, 1, externalData},
		{"duplicate-id", "local", `[` + bulkReceipt(strings.Repeat("a", 64), 90001) + `,` + bulkReceipt(strings.Repeat("a", 64), 90001) + `]`, externalData, 200, 200, 200, nil, 1, 1, externalData},
		{"timeout", "local", `{}`, externalData, 200, 200, 200, context.DeadlineExceeded, 1, 1, externalData},
		{"both-unsupported", "local", `{}`, `{}`, 404, 404, 503, nil, 1, 1, ""},
		{"external-missing", "local", `{}`, `{}`, 200, 200, 503, nil, 1, 1, ""},
		{"external-invalid", "external", localData, `null`, 200, 200, 503, nil, 0, 1, ""},
		{"external-wrong-height", "external", localData, `[` + bulkReceipt(strings.Repeat("b", 64), 90000) + `]`, 200, 200, 503, nil, 0, 1, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, _, local, external := fixture(t)
			p.cfg.AutoFailbackEnabled = true
			p.setMode(tc.mode)
			epoch := p.modeEpoch
			localCalls := mockBulkResponse(p.local, tc.localCode, tc.localData, tc.localFault)
			externalCalls := mockBulkResponse(p.external, tc.externalCode, tc.externalData, nil)
			beforeBlocks := bulkReadCount(local, "/wallet/getblockbynum") + bulkReadCount(external, "/wallet/getblockbynum")
			w := request(p, bulkReceiptsRoute, []byte(`{"num":90001}`))
			if w.Code != tc.wantCode || (tc.wantCode == 200 && w.Body.String() != tc.wantBody) {
				t.Fatalf("HTTP %d: %s", w.Code, w.Body.String())
			}
			if localCalls.Load() != tc.wantLocal || externalCalls.Load() != tc.wantExternal {
				t.Fatalf("dispatches local=%d external=%d", localCalls.Load(), externalCalls.Load())
			}
			if p.currentMode() != tc.mode || p.modeEpoch != epoch || p.failbackStatus().Eligible || p.unsafe.Load() {
				t.Fatal("bulk request changed persistent routing/recovery policy")
			}
			if after := bulkReadCount(local, "/wallet/getblockbynum") + bulkReadCount(external, "/wallet/getblockbynum"); beforeBlocks != after {
				t.Fatal("bulk request added a full-block fetch")
			}
			if w := request(p, "/wallet/getnowblock", []byte(`{}`)); w.Code != 200 {
				t.Fatal("bulk failure interrupted ordinary reads")
			}
		})
	}
}

func TestBulkReceiptsHistoryFloors(t *testing.T) {
	for _, tc := range []struct {
		name                             string
		blockFloor, receiptFloor, height int64
		wantExternal                     bool
	}{
		{"unset", 0, 0, 90001, false},
		{"below-block", 90000, 95000, 89999, true},
		{"between-floors", 90000, 95000, 92000, true},
		{"below-receipts", 90000, 95000, 94999, true},
		{"receipt-boundary", 90000, 95000, 95000, false},
		{"above-receipts", 90000, 95000, 95001, false},
		{"block-only", 90000, 0, 89999, true},
		{"receipt-only", 0, 95000, 94999, true},
		{"block-floor-higher", 95000, 90000, 94999, true},
		{"block-boundary-higher", 95000, 90000, 95000, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, _, local, _ := fixture(t)
			p.setMode("local")
			p.cfg.BlockFloor, p.cfg.ReceiptFloor = tc.blockFloor, tc.receiptFloor
			localCalls := mockBulkResponse(p.local, 200, `[]`, nil)
			externalCalls := mockBulkResponse(p.external, 200, `[]`, nil)
			q := []byte(fmt.Sprintf(`{"num":%d}`, tc.height))
			if w := request(p, bulkReceiptsRoute, q); w.Code != 200 || w.Body.String() != `[]` {
				t.Fatalf("empty historical batch: HTTP %d %s", w.Code, w.Body.String())
			}
			if (externalCalls.Load() == 1) != tc.wantExternal || localCalls.Load()+externalCalls.Load() != 1 || p.currentMode() != "local" {
				t.Fatalf("floor route: local=%d external=%d mode=%s", localCalls.Load(), externalCalls.Load(), p.currentMode())
			}
			before := bulkReadCount(local, "/wallet/getblockbynum")
			if w := request(p, "/wallet/getblockbynum", q); w.Code != 200 {
				t.Fatal("receipt floor changed block availability")
			}
			if gotLocal := bulkReadCount(local, "/wallet/getblockbynum") == before+1; gotLocal != (tc.height >= tc.blockFloor) {
				t.Fatal("block routing used the receipt floor")
			}
		})
	}
}

func TestBulkReceiptsHealthGates(t *testing.T) {
	for _, kind := range []string{"external-unhealthy", "historical-external-unhealthy", "external-fails-during-local-read", "stale-health", "unsafe-journal", "local-health-failure"} {
		t.Run(kind, func(t *testing.T) {
			p, _, local, _ := fixture(t)
			p.setMode("local")
			localCalls := mockBulkResponse(p.local, 404, `{}`, nil)
			externalCalls := mockBulkResponse(p.external, 200, `[]`, nil)
			wantLocal, wantExternal, wantCode, wantMode := int64(1), int64(0), 503, "local"
			switch kind {
			case "external-unhealthy", "historical-external-unhealthy":
				externalOutage(p)
				if kind == "historical-external-unhealthy" {
					p.cfg.ReceiptFloor, wantLocal = 95000, 0
				}
			case "external-fails-during-local-read":
				base := p.local.Client.Transport
				p.local.Client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
					resp, err := base.RoundTrip(r)
					p.healthMu.Lock()
					p.health.External = false
					p.healthMu.Unlock()
					return resp, err
				})
			case "stale-health":
				p.healthMu.Lock()
				p.health.Checked = time.Now().Add(-time.Hour)
				p.healthMu.Unlock()
				wantLocal = 0
			case "unsafe-journal":
				p.unsafe.Store(true)
				wantLocal = 0
			case "local-health-failure":
				local.mu.Lock()
				local.age = time.Minute
				local.mu.Unlock()
				p.probe(context.Background())
				wantLocal, wantExternal, wantCode, wantMode = 0, 1, 200, "external"
			}
			w := request(p, bulkReceiptsRoute, []byte(`{"num":90001}`))
			if w.Code != wantCode || localCalls.Load() != wantLocal || externalCalls.Load() != wantExternal || p.currentMode() != wantMode {
				t.Fatalf("HTTP=%d local=%d external=%d mode=%s", w.Code, localCalls.Load(), externalCalls.Load(), p.currentMode())
			}
		})
	}
}

func TestBulkReceiptsMissingEntriesRemainIndividualLookups(t *testing.T) {
	for _, kind := range []string{"empty", "partial", "unavailable"} {
		t.Run(kind, func(t *testing.T) {
			p, _, local, _ := fixture(t)
			p.setMode("local")
			ids := []string{strings.Repeat("a", 64), strings.Repeat("b", 64), strings.Repeat("c", 64)}
			body, code, wantLookups := `[]`, 200, 3
			if kind == "partial" {
				body, wantLookups = `[`+bulkReceipt(ids[0], 99990)+`]`, 2
			} else if kind == "unavailable" {
				body, code = `{}`, 404
			}
			mockBulkResponse(p.local, code, body, nil)
			mockBulkResponse(p.external, code, body, nil)
			w := request(p, bulkReceiptsRoute, []byte(`{"num":99990}`))
			var infos []struct {
				ID string `json:"id"`
			}
			if w.Code == 200 {
				if err := json.Unmarshal(w.Body.Bytes(), &infos); err != nil {
					t.Fatal(err)
				}
			} else if w.Code != 503 || kind != "unavailable" {
				t.Fatalf("unexpected batch failure: HTTP %d", w.Code)
			}
			// Mirror the resolver's missing-ID fallback, without importing Bifrost.
			found := make(map[string]bool)
			for _, info := range infos {
				found[info.ID] = true
			}
			for _, id := range ids {
				if !found[id] {
					q := []byte(fmt.Sprintf(`{"value":%q}`, id))
					if w := request(p, "/wallet/gettransactioninfobyid", q); w.Code != 200 || !validResponse("/wallet/gettransactioninfobyid", q, w.Body.Bytes()) {
						t.Fatal("missing batch entry lost individual fallback")
					}
				}
			}
			if bulkReadCount(local, "/wallet/gettransactioninfobyid") != wantLookups || p.currentMode() != "local" {
				t.Fatal("bulk result fabricated entries or abandoned healthy local")
			}
		})
	}
}

func TestBulkReceiptsResponseBounds(t *testing.T) {
	const limit = 8 * 1024 * 1024
	for _, tc := range []struct {
		name     string
		local    bool
		size     int
		wantCode int
	}{
		{"exact-limit", false, limit, 200},
		{"external-over-limit", false, limit + 1, 503},
		{"local-over-limit-falls-back", true, limit + 1, 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, _, _, _ := fixture(t)
			u := p.external
			if tc.local {
				p.setMode("local")
				u = p.local
				mockBulkResponse(p.external, 200, `[]`, nil)
			}
			calls := mockBulkResponse(u, 200, `[]`+strings.Repeat(" ", tc.size-2), nil)
			w := request(p, bulkReceiptsRoute, []byte(`{"num":90001}`))
			if w.Code != tc.wantCode || calls.Load() != 1 {
				t.Fatalf("HTTP %d, want %d; calls=%d", w.Code, tc.wantCode, calls.Load())
			}
			if tc.local && p.currentMode() != "local" {
				t.Fatal("oversized optional result evicted local")
			}
		})
	}
}

func TestBulkReceipts32ActiveWithQueuedHead(t *testing.T) {
	for _, mode := range []string{"local", "external", "historical", "fallback"} {
		t.Run(mode, func(t *testing.T) {
			p, _, _, _ := fixture(t)
			p.setMode("local")
			mockBulkResponse(p.local, 200, `[]`, nil)
			mockBulkResponse(p.external, 200, `[]`, nil)
			target := p.local
			if mode != "local" {
				target = p.external
			}
			switch mode {
			case "external":
				p.setMode("external")
			case "historical":
				p.cfg.ReceiptFloor = 95000
			case "fallback":
				mockBulkResponse(p.local, 404, `{}`, nil)
			}
			release := make(chan struct{})
			var once sync.Once
			var wg sync.WaitGroup
			unblock := func() { once.Do(func() { close(release) }) }
			defer func() { unblock(); wg.Wait() }()
			entered := make(chan struct{}, 33)
			target.Client.Timeout = 5 * time.Second
			target.Client.Transport = prefetchBarrierTransport{target.Client.Transport, entered, release}
			results := make(chan *httptest.ResponseRecorder, 33)
			launch := func(route, body string) {
				wg.Add(1)
				go func() {
					defer wg.Done()
					results <- request(p, route, []byte(body))
				}()
			}
			for range 32 {
				launch(bulkReceiptsRoute, `{"num":90001}`)
			}
			for range 32 {
				select {
				case <-entered:
				case <-time.After(2 * time.Second):
					t.Fatal("32 bulk requests did not reach upstream concurrently")
				}
			}
			launch("/wallet/getnowblock", `{}`)
			waitForAdmissionQueue(t, p.admission, 1)
			if len(p.admission.active) != 32 || cap(p.admission.waiting) != 32 {
				t.Fatal("bulk endpoint changed admission bounds")
			}
			unblock()
			wg.Wait()
			for range 33 {
				if w := <-results; w.Code != 200 {
					t.Errorf("burst failed: HTTP %d %s", w.Code, w.Body.String())
				}
			}
			wantMode := "local"
			if mode == "external" {
				wantMode = mode
			}
			if len(p.admission.active) != 0 || len(p.admission.waiting) != 0 || p.currentMode() != wantMode || p.admission.rejected.Load() != 0 {
				t.Fatal("bulk burst leaked permits, rejected work or changed mode")
			}
		})
	}
}

func TestBulkReceiptsRequestBounds(t *testing.T) {
	p, _, _, _ := fixture(t)
	localCalls := mockBulkResponse(p.local, 200, `[]`, nil)
	externalCalls := mockBulkResponse(p.external, 200, `[]`, nil)
	for _, tc := range []struct {
		name, route, body string
		wantCode          int
	}{
		{"limit", bulkReceiptsRoute, `{"num":0}` + strings.Repeat(" ", 65536-len(`{"num":0}`)), 200},
		{"over-limit", bulkReceiptsRoute, `{"num":0}` + strings.Repeat(" ", 65537-len(`{"num":0}`)), 413},
		{"query", bulkReceiptsRoute + "?num=1", `{"num":0}`, 400},
		{"duplicate", bulkReceiptsRoute, `{"num":0,"num":1}`, 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := localCalls.Load() + externalCalls.Load()
			w := request(p, tc.route, []byte(tc.body))
			if w.Code != tc.wantCode {
				t.Fatalf("HTTP %d, want %d", w.Code, tc.wantCode)
			}
			if tc.wantCode != 200 && localCalls.Load()+externalCalls.Load() != before {
				t.Fatal("invalid request reached an upstream")
			}
		})
	}
	// The existing admission gate must release permits on every validation exit.
	if len(p.admission.active) != 0 || len(p.admission.waiting) != 0 {
		t.Fatal("request validation leaked admission permits")
	}
	get := httptest.NewRecorder()
	p.ServeHTTP(get, httptest.NewRequest(http.MethodGet, bulkReceiptsRoute, bytes.NewBufferString(`{"num":0}`)))
	if get.Code != 400 {
		t.Fatalf("GET accepted: HTTP %d", get.Code)
	}
}
