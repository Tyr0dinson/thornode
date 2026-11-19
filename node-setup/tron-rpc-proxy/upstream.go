package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

var errUpstream = errors.New("upstream response unavailable or invalid")

type failureReason string

const (
	reasonOK           failureReason = "ok"
	reasonTimeout      failureReason = "timeout"
	reasonCanceled     failureReason = "canceled"
	reasonTransport    failureReason = "transport"
	reasonHTTP         failureReason = "http_status"
	reasonBodyRead     failureReason = "body_read"
	reasonTooLarge     failureReason = "response_too_large"
	reasonJSON         failureReason = "invalid_json"
	reasonResponse     failureReason = "invalid_response"
	reasonGenesis      failureReason = "genesis_mismatch"
	reasonStaleHead    failureReason = "stale_head"
	reasonFinalized    failureReason = "finalized_height_mismatch"
	reasonGap          failureReason = "height_gap"
	reasonCommon       failureReason = "common_block_mismatch"
	reasonUnverified   failureReason = "upstream_unverified"
	reasonOperator     failureReason = "operator_request"
	reasonStartup      failureReason = "startup_external"
	reasonUnknownWrite failureReason = "broadcast_indeterminate"
	reasonInvalidAck   failureReason = "invalid_ack"
	reasonAckID        failureReason = "ack_id_mismatch"
	reasonAckRejected  failureReason = "ack_rejected"
	reasonLookupMiss   failureReason = "not_found"
	reasonIdentity     failureReason = "identity_mismatch"
	reasonReconciled   failureReason = "reconciled"
	reasonCached       failureReason = "journal_accepted"
	reasonExpired      failureReason = "expired"
	reasonAttemptLimit failureReason = "attempt_limit"
	reasonJournal      failureReason = "journal_unavailable"
	reasonJournalBad   failureReason = "journal_invalid"
	reasonConflict     failureReason = "payload_conflict"
)

// Deliberately discard the underlying error: it can contain a credentialed URL.
type upstreamFailure struct {
	reason     failureReason
	httpStatus int
}

func (e upstreamFailure) Error() string { return string(e.reason) }
func (e upstreamFailure) Unwrap() error { return errUpstream }

func failureReasonFor(err error) failureReason {
	var e upstreamFailure
	if errors.As(err, &e) {
		return e.reason
	}
	return reasonResponse
}

func transportFailure(err error) error {
	if errors.Is(err, context.Canceled) {
		return upstreamFailure{reason: reasonCanceled}
	}
	var e net.Error
	if errors.Is(err, context.DeadlineExceeded) || errors.As(err, &e) && e.Timeout() {
		return upstreamFailure{reason: reasonTimeout}
	}
	return upstreamFailure{reason: reasonTransport}
}

type Upstream struct {
	API, RPC *url.URL
	Client   *http.Client
}

func newUpstream(api, rpc *url.URL, timeout time.Duration) *Upstream {
	return &Upstream{api, rpc, &http.Client{Timeout: timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }, Transport: &http.Transport{
		Proxy: nil, DialContext: (&net.Dialer{Timeout: timeout, KeepAlive: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout: timeout, ResponseHeaderTimeout: timeout, MaxConnsPerHost: 32, MaxIdleConnsPerHost: 16, IdleConnTimeout: 60 * time.Second,
	}}}
}

func (u *Upstream) call(ctx context.Context, method, route string, body []byte) ([]byte, error) {
	target := *u.API
	if route == "/jsonrpc" {
		target = *u.RPC
	} else {
		target.Path = strings.TrimRight(target.Path, "/") + route
		target.RawPath = ""
	}
	// An io.NopCloser deliberately prevents net/http from replaying POST bodies.
	req, err := http.NewRequestWithContext(ctx, method, target.String(), io.NopCloser(bytes.NewReader(body)))
	if err != nil {
		return nil, upstreamFailure{reason: reasonResponse}
	}
	req.ContentLength = int64(len(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := u.Client.Do(req)
	if err != nil {
		return nil, transportFailure(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		code := resp.StatusCode
		if code < 100 || code > 599 {
			code = 0
		}
		return nil, upstreamFailure{reason: reasonHTTP, httpStatus: code}
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8*1024*1024+1))
	if err != nil {
		return nil, upstreamFailure{reason: reasonBodyRead}
	}
	if len(data) > 8*1024*1024 {
		return nil, upstreamFailure{reason: reasonTooLarge}
	}
	if uniqueJSON(data) != nil {
		return nil, upstreamFailure{reason: reasonJSON}
	}
	return data, nil
}

type block struct {
	ID     string `json:"blockID"`
	Header struct {
		Raw struct {
			Number    int64 `json:"number"`
			Timestamp int64 `json:"timestamp"`
		} `json:"raw_data"`
	} `json:"block_header"`
}

func parseBlock(data []byte, height int64) (block, error) {
	var b block
	if json.Unmarshal(data, &b) != nil || !validID(b.ID) || b.Header.Raw.Number < 0 || (height >= 0 && b.Header.Raw.Number != height) {
		return b, errUpstream
	}
	idHeight, err := strconv.ParseUint(b.ID[:16], 16, 64)
	if err != nil || idHeight != uint64(b.Header.Raw.Number) {
		return b, errUpstream
	}
	return b, nil
}

func transactionIdentity(data []byte, id string) bool {
	var tx struct {
		ID  string `json:"txID"`
		Raw string `json:"raw_data_hex"`
	}
	if json.Unmarshal(data, &tx) != nil || tx.ID != id {
		return false
	}
	raw, err := hex.DecodeString(tx.Raw)
	if err != nil || len(raw) == 0 {
		return false
	}
	hash := sha256.Sum256(raw)
	return hex.EncodeToString(hash[:]) == id
}

func validResponse(route string, request, response []byte) bool {
	if route == "/wallet/gettransactioninfobyblocknum" {
		return validBulkReceipts(request, response)
	}
	var obj map[string]json.RawMessage
	if json.Unmarshal(response, &obj) != nil || len(obj) == 0 || obj["Error"] != nil || obj["error"] != nil {
		return false
	}
	switch route {
	case "/wallet/getnowblock":
		b, err := parseBlock(response, -1)
		age := time.Now().UnixMilli() - b.Header.Raw.Timestamp
		return err == nil && age >= -10000 && age <= 30000
	case "/wallet/getblockbynum":
		var q struct {
			Num int64 `json:"num"`
		}
		if json.Unmarshal(request, &q) != nil {
			return false
		}
		_, err := parseBlock(response, q.Num)
		return err == nil
	case "/wallet/gettransactionbyid", "/wallet/gettransactioninfobyid":
		var q struct {
			Value string `json:"value"`
		}
		if json.Unmarshal(request, &q) != nil || !validID(q.Value) {
			return false
		}
		if route == "/wallet/gettransactionbyid" {
			return transactionIdentity(response, q.Value)
		}
		var receipt struct {
			ID             string `json:"id"`
			BlockNumber    int64  `json:"blockNumber"`
			BlockTimestamp int64  `json:"blockTimeStamp"`
		}
		return json.Unmarshal(response, &receipt) == nil && receipt.ID == q.Value && receipt.BlockNumber > 0 && receipt.BlockTimestamp > 0
	case "/wallet/getaccount":
		var q struct {
			Address string `json:"address"`
			Visible bool   `json:"visible"`
		}
		var a struct {
			Address string `json:"address"`
		}
		if json.Unmarshal(request, &q) != nil || json.Unmarshal(response, &a) != nil {
			return false
		}
		want, err := address(q.Address, q.Visible)
		got, gotErr := address(a.Address, q.Visible)
		return err == nil && gotErr == nil && bytes.Equal(want, got)
	case "/wallet/getchainparameters":
		return obj["chainParameter"] != nil && string(obj["chainParameter"]) != "null"
	case "/wallet/estimateenergy":
		return obj["result"] != nil && obj["energy_required"] != nil
	case "/wallet/createtransaction":
		return obj["txID"] != nil && obj["raw_data_hex"] != nil
	case "/wallet/triggersmartcontract":
		return obj["result"] != nil && obj["transaction"] != nil
	case "/jsonrpc":
		var q struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		var a struct {
			ID      json.RawMessage `json:"id"`
			Version string          `json:"jsonrpc"`
			Result  string          `json:"result"`
		}
		if json.Unmarshal(request, &q) != nil || json.Unmarshal(response, &a) != nil || !bytes.Equal(bytes.TrimSpace(q.ID), bytes.TrimSpace(a.ID)) || a.Version != "2.0" || !strings.HasPrefix(a.Result, "0x") {
			return false
		}
		if q.Method == "eth_blockNumber" {
			_, err := strconv.ParseUint(strings.TrimPrefix(a.Result, "0x"), 16, 64)
			return err == nil
		}
		_, err := hex.DecodeString(strings.TrimPrefix(a.Result, "0x"))
		return err == nil
	}
	return false
}

func validBulkReceipts(request, response []byte) bool {
	height, ok := bulkReceiptHeight(request)
	if !ok || uniqueJSON(response) != nil {
		return false
	}
	var receipts []map[string]json.RawMessage
	if json.Unmarshal(response, &receipts) != nil || receipts == nil {
		return false
	}
	seen := make(map[string]bool, len(receipts))
	for _, receipt := range receipts {
		for key := range receipt {
			if strings.EqualFold(key, "error") || strings.EqualFold(key, "id") && key != "id" || strings.EqualFold(key, "blockNumber") && key != "blockNumber" {
				return false
			}
		}
		var id string
		var number *int64
		if json.Unmarshal(receipt["id"], &id) != nil || !validID(id) || seen[id] || json.Unmarshal(receipt["blockNumber"], &number) != nil || number == nil || *number != height {
			return false
		}
		seen[id] = true
	}
	// Empty/partial arrays are usable hints, not proof of archival completeness.
	// Bifrost's resolver still fetches every missing ID individually.
	return true
}
