package rpc

import (
	"encoding/json"
	"fmt"
	"math/big"
)

// RPCTokenBalance is one entry of a transaction's meta.preTokenBalances or
// meta.postTokenBalances: the token account at AccountIndex (into the resolved
// account list, static keys then loaded addresses), its mint, owner and raw amount.
type RPCTokenBalance struct {
	AccountIndex  int    `json:"accountIndex"`
	Mint          string `json:"mint"`
	Owner         string `json:"owner"`
	ProgramID     string `json:"programId"`
	UITokenAmount struct {
		Amount   string `json:"amount"`
		Decimals int    `json:"decimals"`
	} `json:"uiTokenAmount"`
}

// RawAmount parses the base-unit token amount.
func (b RPCTokenBalance) RawAmount() (*big.Int, bool) {
	v, ok := new(big.Int).SetString(b.UITokenAmount.Amount, 10)
	if !ok || v.Sign() < 0 {
		return nil, false
	}
	return v, true
}

// ParseTokenBalances decodes meta token balances, skipping any entry that does not
// have the expected shape. The meta fields stay untyped in RPCMeta so an unexpected
// entry can never fail getTransaction decoding (which would mark the scanner
// unhealthy); a skipped entry only means that account's balance is unknown, and
// callers treat an unknown balance as "do not observe".
func ParseTokenBalances(raw []interface{}) []RPCTokenBalance {
	out := make([]RPCTokenBalance, 0, len(raw))
	for _, entry := range raw {
		b, err := json.Marshal(entry)
		if err != nil {
			continue
		}
		var tb RPCTokenBalance
		if err := json.Unmarshal(b, &tb); err != nil || tb.Mint == "" {
			continue
		}
		out = append(out, tb)
	}
	return out
}

// FindTokenBalance returns the entry for the token account at accountIndex.
func FindTokenBalance(balances []RPCTokenBalance, accountIndex int) (RPCTokenBalance, bool) {
	for _, b := range balances {
		if b.AccountIndex == accountIndex {
			return b, true
		}
	}
	return RPCTokenBalance{}, false
}

// TokenAccount is the parsed state of an SPL token account.
type TokenAccount struct {
	Exists   bool
	Program  string // "spl-token" for the classic program
	Mint     string
	Owner    string
	State    string // "initialized" or "frozen"
	Amount   *big.Int
	Decimals int
}

type tokenAccountInfoResponse struct {
	Result *struct {
		Value *struct {
			Owner string          `json:"owner"`
			Data  json.RawMessage `json:"data"`
		} `json:"value"`
	} `json:"result"`
	Error *RPCError `json:"error"`
}

type parsedTokenAccountData struct {
	Program string `json:"program"`
	Parsed  struct {
		Type string `json:"type"`
		Info struct {
			Mint        string `json:"mint"`
			Owner       string `json:"owner"`
			State       string `json:"state"`
			TokenAmount struct {
				Amount   string `json:"amount"`
				Decimals int    `json:"decimals"`
			} `json:"tokenAmount"`
		} `json:"info"`
	} `json:"parsed"`
}

// GetTokenAccount reads an SPL token account with the jsonParsed encoding. A
// missing account returns Exists=false and a nil error, so a vault that has never
// held a token reads as a zero balance. So does an address holding anything other
// than a parsed token account: anyone can send lamports to a not-yet-created
// associated token account address, leaving a plain System account there (the
// associated token program still creates the token account over it). Treating that
// as an error would let a 1-lamport transfer fail every solvency read for the vault.
func (s *SolRPC) GetTokenAccount(address, commitment string) (TokenAccount, error) {
	params := []interface{}{
		address,
		map[string]interface{}{
			"encoding":   "jsonParsed",
			"commitment": commitment,
		},
	}
	var resp tokenAccountInfoResponse
	if err := s.post("getAccountInfo", params, &resp); err != nil {
		return TokenAccount{}, fmt.Errorf("failed to call getAccountInfo: %w", err)
	}
	if resp.Error != nil && resp.Error.Code != 0 {
		return TokenAccount{}, fmt.Errorf("RPC Error %d: %s", resp.Error.Code, resp.Error.Message)
	}
	if resp.Result == nil {
		return TokenAccount{}, fmt.Errorf("getAccountInfo: empty result")
	}
	if resp.Result.Value == nil {
		return TokenAccount{Amount: big.NewInt(0)}, nil
	}
	var data parsedTokenAccountData
	if err := json.Unmarshal(resp.Result.Value.Data, &data); err != nil || data.Parsed.Type != "account" {
		// jsonParsed returns an array ([data, encoding]) for an account it cannot
		// parse, such as a pre-funded System account.
		return TokenAccount{Amount: big.NewInt(0)}, nil
	}
	amount, ok := new(big.Int).SetString(data.Parsed.Info.TokenAmount.Amount, 10)
	if !ok || amount.Sign() < 0 {
		return TokenAccount{}, fmt.Errorf("account %s has invalid token amount %q", address, data.Parsed.Info.TokenAmount.Amount)
	}
	return TokenAccount{
		Exists:   true,
		Program:  data.Program,
		Mint:     data.Parsed.Info.Mint,
		Owner:    data.Parsed.Info.Owner,
		State:    data.Parsed.Info.State,
		Amount:   amount,
		Decimals: data.Parsed.Info.TokenAmount.Decimals,
	}, nil
}
