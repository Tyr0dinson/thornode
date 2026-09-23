package rpc

import (
	"errors"
	"fmt"
)

// ErrNonceAccountNotProvisioned marks the cases where a durable-nonce account does
// not yet exist in a usable form (missing, uninitialized, wrong program, or an
// empty stored nonce). These are RECOVERABLE by provisioning, so the caller must
// treat them as "no nonce yet, fall through to the recent-blockhash path" — as
// distinct from a transport/RPC error, which fails closed. Wrap with %w and test
// with errors.Is.
var ErrNonceAccountNotProvisioned = errors.New("durable-nonce account not provisioned")

// getAccountInfoResponse is the response for a getAccountInfo call made with the
// jsonParsed encoding. For a SystemProgram durable-nonce account the runtime
// returns data.program == "nonce" with parsed.type == "initialized" and
// parsed.info.{authority, blockhash, feeCalculator}. The blockhash field is the
// STORED nonce value.
type getAccountInfoResponse struct {
	Jsonrpc string `json:"jsonrpc"`
	Id      int    `json:"id"`
	Result  *struct {
		Context struct {
			Slot uint64 `json:"slot"`
		} `json:"context"`
		Value *struct {
			Owner string `json:"owner"`
			Data  struct {
				Program string `json:"program"`
				Parsed  struct {
					Type string `json:"type"`
					Info struct {
						Authority string `json:"authority"`
						Blockhash string `json:"blockhash"`
					} `json:"info"`
				} `json:"parsed"`
			} `json:"data"`
		} `json:"value"`
	} `json:"result"`
	Error *RPCError `json:"error"`
}

// GetNonceAccount reads a SystemProgram durable-nonce account and returns its
// stored nonce and its nonce authority. The stored nonce is what a durable-nonce
// transaction bakes into the message's recentBlockhash field so the transaction
// does not expire during the multi-party TSS keysign+broadcast window (#661).
//
// jsonParsed encoding at finalized commitment. Fails CLOSED: an RPC error, a
// missing/uninitialized account, a non-nonce account, or an empty stored nonce is
// returned as an error rather than a silent empty value — a wrong or empty nonce
// would produce a transaction the keysign party can never land.
func (s *SolRPC) GetNonceAccount(pubkey string) (nonce string, authority string, err error) {
	cfg := map[string]interface{}{"encoding": "jsonParsed", "commitment": "finalized"}
	params := []interface{}{pubkey, cfg}

	var response getAccountInfoResponse
	if err := s.post("getAccountInfo", params, &response); err != nil {
		return "", "", fmt.Errorf("failed to call getAccountInfo: %w", err)
	}
	if response.Error != nil && response.Error.Code != 0 {
		return "", "", fmt.Errorf("RPC Error %d: %s", response.Error.Code, response.Error.Message)
	}
	if response.Result == nil || response.Result.Value == nil {
		return "", "", fmt.Errorf("%w: account %s not found", ErrNonceAccountNotProvisioned, pubkey)
	}
	v := response.Result.Value
	if v.Data.Program != "nonce" {
		return "", "", fmt.Errorf("%w: account %s is not a nonce account (program=%q)", ErrNonceAccountNotProvisioned, pubkey, v.Data.Program)
	}
	if v.Data.Parsed.Type != "initialized" {
		return "", "", fmt.Errorf("%w: account %s not initialized (type=%q)", ErrNonceAccountNotProvisioned, pubkey, v.Data.Parsed.Type)
	}
	if v.Data.Parsed.Info.Blockhash == "" {
		return "", "", fmt.Errorf("%w: account %s has empty stored nonce", ErrNonceAccountNotProvisioned, pubkey)
	}
	return v.Data.Parsed.Info.Blockhash, v.Data.Parsed.Info.Authority, nil
}
