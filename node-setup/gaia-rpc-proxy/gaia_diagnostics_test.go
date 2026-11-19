package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const gaiaDiagnosticSecret = "private-route-credential-do-not-log"

type gaiaDiagnosticBody struct{}

func (gaiaDiagnosticBody) Read([]byte) (int, error) { return 0, errors.New(gaiaDiagnosticSecret) }
func (gaiaDiagnosticBody) Close() error             { return nil }

func gaiaCheckDiagnostic(t *testing.T, output string, stage, reason string, status int, methods ...string) {
	t.Helper()
	method := "block_results"
	if len(methods) != 0 {
		method = methods[0]
	}
	for _, required := range []string{"gaia_read_failed", "method=" + method, "height=10", "stage=" + stage, "reason=" + reason, fmt.Sprintf("http_status=%d", status)} {
		if !strings.Contains(output, required) {
			t.Errorf("diagnostic missing %q: %s", required, output)
		}
	}
	if strings.Count(output, "\n") != 1 || len(output) > 256 || strings.Contains(output, gaiaDiagnosticSecret) || strings.Contains(output, "http://") {
		t.Fatalf("unsafe or unbounded diagnostic: %q", output)
	}
}

func TestGaiaCallFailureDiagnostics(t *testing.T) {
	valid := `{"jsonrpc":"2.0","id":7,"result":{"height":"10","txs_results":[]}}`
	for _, tc := range []struct {
		name, body, reason                string
		status                            int
		transport, readFailure, oversized bool
	}{
		{name: "rate_limit", status: 429, body: gaiaDiagnosticSecret, reason: "http_status"},
		{name: "bad_gateway", status: 502, body: gaiaDiagnosticSecret, reason: "http_status"},
		{name: "bounded_status", status: 799, reason: "http_status"},
		{name: "transport", transport: true, reason: "transport"},
		{name: "body_read", status: 200, readFailure: true, reason: "body_read"},
		{name: "body_size", status: 200, oversized: true, reason: "body_size"},
		{name: "malformed", status: 200, body: `{` + gaiaDiagnosticSecret, reason: "decode"},
		{name: "duplicate", status: 200, body: `{"jsonrpc":"2.0","id":7,"id":7,"result":{"height":"10","txs_results":[]}}`, reason: "duplicate_json_or_depth"},
		{name: "decode", status: 200, body: `{"jsonrpc":"2.0","id":7,"result":{"height":10,"txs_results":[]}}`, reason: "decode"},
		{name: "identity", status: 200, body: strings.Replace(valid, `"id":7`, `"id":8`, 1), reason: "response_identity"},
		{name: "version", status: 200, body: strings.Replace(valid, `"2.0"`, `"1.0"`, 1), reason: "response_identity"},
		{name: "shape", status: 200, body: strings.Replace(valid, `"height":"10"`, `"height":"11"`, 1), reason: "response_shape"},
		{name: "http500_result", status: 500, body: valid, reason: "response_shape"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, a := newGaiaTestNode(t, 1), newGaiaTestNode(t, 1)
			g := gaiaTestProxy(t, p, a)
			g.primary.User = url.UserPassword("private-user", gaiaDiagnosticSecret)
			g.primary.Path = "/" + gaiaDiagnosticSecret
			var output bytes.Buffer
			g.logger = log.New(&output, "", 0)
			g.client.Transport = gaiaTestRoundTrip(func(*http.Request) (*http.Response, error) {
				if tc.transport {
					return nil, errors.New(g.primary.String())
				}
				var body io.ReadCloser = io.NopCloser(strings.NewReader(tc.body))
				if tc.readFailure {
					body = gaiaDiagnosticBody{}
				}
				length := int64(len(tc.body))
				if tc.oversized {
					length = gaiaMaxResponse + 1
				}
				return &http.Response{StatusCode: tc.status, Body: body, ContentLength: length, Header: make(http.Header)}, nil
			})
			_, _, err := g.call(context.Background(), g.primary, gaiaReadRequest("block_results", 10))
			if err == nil || strings.Contains(err.Error(), gaiaDiagnosticSecret) {
				t.Fatalf("call returned unsafe/missing error: %v", err)
			}
			w := gaiaTestRead(g, "block_results", 10)
			if w.Code != http.StatusBadGateway || a.total() != 0 || strings.Contains(w.Body.String(), gaiaDiagnosticSecret) {
				t.Fatalf("failure did not remain isolated: %d %s", w.Code, w.Body.String())
			}
			status := tc.status
			if status < 100 || status > 599 {
				status = 0
			}
			gaiaCheckDiagnostic(t, output.String(), "primary", tc.reason, status)
		})
	}
}

func TestGaiaArchiveFailureDiagnostics(t *testing.T) {
	for _, tc := range []struct {
		name, stage, reason string
		status              int
	}{
		{"rpc", "archive", "rpc_error", 0},
		{"block_rpc", "archive", "rpc_error", 0},
		{"block_identity", "archive", "response_identity", 200},
		{"count", "archive_pair", "tx_count_mismatch", 0},
		{"expired", "archive", "proof_expired", 0},
		{"replaced", "archive", "proof_replaced", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, a := newGaiaTestNode(t, 50), newGaiaTestNode(t, 1)
			g := gaiaTestProxy(t, p, a)
			var clock atomic.Int64
			clock.Store(time.Now().UnixNano())
			g.now = func() time.Time { return time.Unix(0, clock.Load()) }
			gaiaTestOK(t, gaiaTestRead(g, "block", 10))
			var output bytes.Buffer
			g.logger = log.New(&output, "", 0)
			a.update(func() {
				a.mutate = func(method string, height int64, response map[string]any) {
					if height != 10 {
						return
					}
					if tc.name == "rpc" && method == "block_results" || tc.name == "block_rpc" && method == "block" {
						delete(response, "result")
						response["error"] = map[string]any{"code": -32603, "message": gaiaDiagnosticSecret, "data": gaiaDiagnosticSecret}
					}
					if tc.name == "block_identity" && method == "block" {
						response["id"] = gaiaDiagnosticSecret
					}
					if method != "block_results" {
						return
					}
					switch tc.name {
					case "count":
						response["result"].(map[string]any)["txs_results"] = []any{map[string]any{"code": 0}}
					case "expired":
						clock.Add(int64(gaiaProofTTL + time.Second))
					case "replaced":
						g.proofMu.Lock()
						g.proof.generation++
						g.proofMu.Unlock()
					}
				}
			})
			method := "block_results"
			if tc.name == "block_rpc" || tc.name == "block_identity" {
				method = "block"
			}
			w := gaiaTestRead(g, method, 10)
			if tc.name == "expired" || tc.name == "replaced" {
				gaiaTestOK(t, w)
				if output.Len() != 0 {
					t.Fatal("admitted snapshot produced a failure diagnostic")
				}
				return
			}
			if w.Code != http.StatusBadGateway || strings.Contains(w.Body.String(), gaiaDiagnosticSecret) {
				t.Fatalf("archive did not fail closed: %d %s", w.Code, w.Body.String())
			}
			gaiaCheckDiagnostic(t, output.String(), tc.stage, tc.reason, tc.status, method)
		})
	}
}

func TestGaiaAgreementFailureDiagnostic(t *testing.T) {
	p, a := newGaiaTestNode(t, 50), newGaiaTestNode(t, 1)
	a.update(func() { a.down = true })
	g := gaiaTestProxy(t, p, a)
	var output bytes.Buffer
	g.logger = log.New(&output, "", 0)
	w := gaiaTestRead(g, "block_results", 10)
	if w.Code != http.StatusBadGateway {
		t.Fatal(w.Code)
	}
	gaiaCheckDiagnostic(t, output.String(), "agreement_archive_status", "http_status", 503)
}
