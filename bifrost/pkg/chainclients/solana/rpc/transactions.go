package rpc

import (
	"encoding/json"
	"fmt"
)

// Response struct for getSignaturesForAddress
type GetSignaturesForAddressResponse struct {
	Jsonrpc string                       `json:"jsonrpc"`
	Id      int                          `json:"id"`
	Result  []SignaturesForAddressResult `json:"result"`
	Error   *RPCError                    `json:"error"`
}

// getSignaturesForAddress response data
type SignaturesForAddressResult struct {
	Signature string `json:"signature"`
	Slot      uint64 `json:"slot"`
	Err       any    `json:"err"`
}

// GetSignaturesForAddressUntil - makes a getSignaturesForAddress RPC call to the Solana RPC. Will only return "finalized" data.
func (s *SolRPC) GetSignaturesForAddressUntil(address string, minContextSlot uint64, until string) ([]SignaturesForAddressResult, error) {
	var objParams = map[string]interface{}{
		"commitment": "finalized",
	}
	if minContextSlot > 0 {
		objParams["minContextSlot"] = minContextSlot
	}
	if until != "" {
		objParams["until"] = until
	}
	params := []interface{}{
		address,
		objParams,
	}

	respBody, err := s.post("getSignaturesForAddress", params)
	if err != nil {
		return nil, fmt.Errorf("failed to call getSignaturesForAddress: %w", err)
	}

	var response GetSignaturesForAddressResponse
	if err := json.Unmarshal(respBody, &response); err != nil {
		return nil, err
	}

	if response.Error != nil && response.Error.Code != 0 {
		return nil, fmt.Errorf("RPC Error %d: %s", response.Error.Code, response.Error.Message)
	}

	return response.Result, nil
}

// Response struct for getSignaturesForAddress
type GetTransactionResponse struct {
	Jsonrpc string            `json:"jsonrpc"`
	Id      int               `json:"id"`
	Result  TransactionResult `json:"result"`
	Error   *RPCError         `json:"error"`
}

// getTransaction response data
type TransactionResult struct {
	Slot        uint64     `json:"slot"`
	Meta        RPCMeta    `json:"meta"`
	Transaction RPCTxnData `json:"transaction"`
}

// GetTransactionUntil - makes a getTransaction RPC call to the Solana RPC. Will only return "finalized" data.
func (s *SolRPC) GetTransaction(signature string) (*TransactionResult, error) {
	params := []interface{}{
		signature,
		map[string]interface{}{
			// "commitment":     "finalized", Default is finalized
			"encoding": "json",
		},
	}

	respBody, err := s.post("getTransaction", params)
	if err != nil {
		return nil, fmt.Errorf("failed to call getTransaction: %w", err)
	}

	var response GetTransactionResponse
	if err := json.Unmarshal(respBody, &response); err != nil {
		return nil, err
	}

	if response.Error != nil && response.Error.Code != 0 {
		return nil, fmt.Errorf("RPC Error %d: %s", response.Error.Code, response.Error.Message)
	}

	return &response.Result, nil
}
