package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

type checkResult struct {
	OK         bool          `json:"ok"`
	Upstream   string        `json:"upstream"`
	Stage      string        `json:"stage"`
	Reason     failureReason `json:"reason"`
	HTTPStatus int           `json:"http_status,omitempty"`
}

func (c checkResult) failed(err error) checkResult {
	c.OK, c.Reason, c.HTTPStatus = false, failureReasonFor(err), 0
	var failure upstreamFailure
	if errors.As(err, &failure) {
		c.HTTPStatus = failure.httpStatus
	}
	return c
}

type Health struct {
	Checked           time.Time   `json:"checked"`
	External          bool        `json:"external"`
	Local             bool        `json:"local"`
	ExternalHeight    int64       `json:"external_height"`
	LocalHeight       int64       `json:"local_height"`
	ExternalFinalized int64       `json:"external_finalized"`
	LocalFinalized    int64       `json:"local_finalized"`
	CommonHeight      int64       `json:"common_height"`
	ExternalCheck     checkResult `json:"external_check"`
	LocalCheck        checkResult `json:"local_check"`
	ChainCheck        checkResult `json:"chain_check"`
}

func (p *Proxy) fresh(h Health) bool {
	age := time.Since(h.Checked)
	return !h.Checked.IsZero() && age >= 0 && age < time.Duration(p.cfg.PollSeconds*3)*time.Second
}

func (p *Proxy) probeNode(ctx context.Context, u *Upstream) (block, int64, bool) {
	b, n, check := p.checkNode(ctx, u, "local")
	return b, n, check.OK
}

func (p *Proxy) checkNode(ctx context.Context, u *Upstream, name string) (b block, finalized int64, check checkResult) {
	check = checkResult{Upstream: name, Stage: "rest_genesis", Reason: reasonResponse}
	gen, err := u.call(ctx, "POST", "/wallet/getblockbynum", []byte(`{"num":0}`))
	if err != nil {
		check = check.failed(err)
		return
	}
	genesis, err := parseBlock(gen, 0)
	if err != nil {
		return
	}
	if genesis.ID != p.cfg.Genesis {
		check.Reason = reasonGenesis
		return
	}

	check.Stage = "rpc_genesis"
	rpcGenesis, err := u.call(ctx, "POST", "/jsonrpc", []byte(`{"jsonrpc":"2.0","id":1,"method":"eth_getBlockByNumber","params":["0x0",false]}`))
	if err != nil {
		check = check.failed(err)
		return
	}
	var rpcBlock struct {
		Version string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Error   json.RawMessage `json:"error"`
		Result  struct {
			Hash string `json:"hash"`
		} `json:"result"`
	}
	if json.Unmarshal(rpcGenesis, &rpcBlock) != nil || rpcBlock.Version != "2.0" || string(rpcBlock.ID) != "1" || len(rpcBlock.Error) != 0 || !strings.HasPrefix(rpcBlock.Result.Hash, "0x") || !validID(strings.TrimPrefix(rpcBlock.Result.Hash, "0x")) {
		return
	}
	if rpcBlock.Result.Hash != "0x"+p.cfg.Genesis {
		check.Reason = reasonGenesis
		return
	}

	check.Stage = "head"
	head, err := u.call(ctx, "POST", "/wallet/getnowblock", []byte(`null`))
	if err != nil {
		check = check.failed(err)
		return
	}
	b, err = parseBlock(head, -1)
	if err != nil {
		return
	}
	if !validResponse("/wallet/getnowblock", nil, head) {
		check.Reason = reasonStaleHead
		return
	}

	check.Stage = "finalized"
	q := []byte(`{"jsonrpc":"2.0","id":1,"method":"eth_blockNumber","params":[]}`)
	r, err := u.call(ctx, "POST", "/jsonrpc", q)
	if err != nil {
		check = check.failed(err)
		return
	}
	if !validResponse("/jsonrpc", q, r) {
		return
	}
	var rpc struct {
		Result string `json:"result"`
	}
	_ = json.Unmarshal(r, &rpc)
	finalized, err = strconv.ParseInt(strings.TrimPrefix(rpc.Result, "0x"), 16, 64)
	if err != nil || finalized <= 0 || b.Header.Raw.Number < finalized || b.Header.Raw.Number-finalized > 64 {
		check.Reason = reasonFinalized
		return
	}
	check.OK, check.Stage, check.Reason = true, "complete", reasonOK
	return
}

func (p *Proxy) probe(ctx context.Context) Health {
	p.probeMu.Lock()
	defer p.probeMu.Unlock()
	return p.probeLocked(ctx)
}

// probeMu also serializes operator admission with a complete probe. A later
// failed check cannot race between an admission's evidence and its mode write.
func (p *Proxy) probeLocked(ctx context.Context) Health {
	p.modeMu.Lock()
	epoch := p.modeEpoch
	p.modeMu.Unlock()
	previous := p.snapshot()
	// Age the whole snapshot from the start, not the end of potentially slow IO.
	h := Health{Checked: time.Now().UTC(), ChainCheck: checkResult{Upstream: "both", Stage: "common_block", Reason: reasonUnverified}}
	ext, extFinal, extCheck := p.checkNode(ctx, p.external, "external")
	local, localFinal, localCheck := p.checkNode(ctx, p.local, "local")
	h.ExternalHeight, h.LocalHeight = ext.Header.Raw.Number, local.Header.Raw.Number
	h.ExternalFinalized, h.LocalFinalized = extFinal, localFinal
	h.ExternalCheck, h.LocalCheck = extCheck, localCheck
	h.External, h.Local = extCheck.OK, localCheck.OK
	// External unavailability/invalid identity is not evidence of a local fault.
	// Only admitted routes use this independently healthy local result;
	// cold admission additionally requires ChainCheck.OK and both upstreams.
	if h.External && h.Local {
		gap := h.ExternalHeight - h.LocalHeight
		if gap < -5 || gap > 5 {
			h.Local = false
			h.ChainCheck = checkResult{Upstream: "both", Stage: "height_gap", Reason: reasonGap}
		} else {
			h.CommonHeight = min(extFinal, localFinal)
			q := []byte(fmt.Sprintf(`{"num":%d}`, h.CommonHeight))
			// Always inspect local evidence, even when the external request fails.
			b, eb := p.local.call(ctx, "POST", "/wallet/getblockbynum", q)
			bb, pb := parseBlock(b, h.CommonHeight)
			a, ea := p.external.call(ctx, "POST", "/wallet/getblockbynum", q)
			ab, pa := parseBlock(a, h.CommonHeight)
			if eb != nil || pb != nil {
				h.Local = false
				h.LocalCheck = (checkResult{Upstream: "local", Stage: "common_block"}).failed(eb)
			}
			if ea != nil || pa != nil {
				h.External = false
				h.ExternalCheck = (checkResult{Upstream: "external", Stage: "common_block"}).failed(ea)
			}
			switch {
			case eb != nil || pb != nil:
				h.ChainCheck = h.LocalCheck
			case ea != nil || pa != nil:
				// Invalid external evidence cannot establish a local fork. Keep
				// admitted local eligible, but forbid external use/new admission.
				h.ChainCheck = h.ExternalCheck
			case ab.ID != bb.ID:
				h.Local = false
				h.ChainCheck = checkResult{Upstream: "both", Stage: "common_block", Reason: reasonCommon}
			default:
				h.ChainCheck = checkResult{OK: true, Upstream: "both", Stage: "common_block", Reason: reasonOK}
			}
		}
	}
	p.healthMu.Lock()
	p.health = h
	p.healthMu.Unlock()
	p.logHealthChanges(previous, h)
	if !h.Local && p.currentMode() == "local" {
		cause := h.LocalCheck
		if cause.OK {
			cause = h.ChainCheck
		}
		p.setModeCheck("external", cause)
	}
	p.considerFailback(h, epoch)
	return h
}
