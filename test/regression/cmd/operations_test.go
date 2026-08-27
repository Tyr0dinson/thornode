package main

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

func TestCheckQueryGasBaseline(t *testing.T) {
	baseline := uint64(100)
	newResponse := func(gas string) *http.Response {
		resp := &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{},
			Body:       io.NopCloser(strings.NewReader(`{}`)),
		}
		resp.Header.Set("Grpc-Metadata-"+types.QueryGasUsedMetadata, gas)
		return resp
	}

	op := &OpCheck{Status: http.StatusOK, GasUsed: &baseline, prefetchResp: newResponse("100")}
	if err := op.Execute(io.Discard, "", 0, 0, nil, make(chan string)); err != nil {
		t.Fatalf("equal query gas should pass: %v", err)
	}

	op.prefetchResp = newResponse("99")
	err := op.Execute(io.Discard, "", 0, 0, nil, make(chan string))
	if err == nil || !strings.Contains(err.Error(), "query gas mismatch") {
		t.Fatalf("lower query gas should fail as nondeterministic, got: %v", err)
	}

	op.prefetchResp = newResponse("101")
	err = op.Execute(io.Discard, "", 0, 0, nil, make(chan string))
	if err == nil || !strings.Contains(err.Error(), "query gas mismatch") {
		t.Fatalf("higher query gas should fail as nondeterministic, got: %v", err)
	}
}

func TestCheckQueryGasRequiresMetadata(t *testing.T) {
	baseline := uint64(100)
	op := &OpCheck{
		Status:       http.StatusOK,
		GasUsed:      &baseline,
		prefetchResp: &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{}`))},
	}

	err := op.Execute(io.Discard, "", 0, 0, nil, make(chan string))
	if err == nil || !strings.Contains(err.Error(), "missing query gas metadata") {
		t.Fatalf("missing metadata should fail, got: %v", err)
	}
}

func TestNewOperationDecodesQueryGas(t *testing.T) {
	op, err := NewOperation(map[string]interface{}{
		"type":     "check",
		"endpoint": "http://localhost:1317/thorchain/pools",
		"gas_used": 123,
	})
	if err != nil {
		t.Fatal(err)
	}

	check := op.(*OpCheck)
	if check.GasUsed == nil || *check.GasUsed != 123 {
		t.Fatalf("unexpected gas baseline: %v", check.GasUsed)
	}
}
