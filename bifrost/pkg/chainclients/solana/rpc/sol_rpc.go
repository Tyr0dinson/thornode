package rpc

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

// SolRPC is a struct that interacts with the Solana blockchain
type SolRPC struct {
	host    string
	timeout time.Duration
	logger  zerolog.Logger
}

func NewSolRPC(host string, timeout time.Duration) *SolRPC {
	return &SolRPC{
		host:    host,
		timeout: timeout,
		logger:  log.Logger.With().Str("module", "sol_rpc").Logger(),
	}
}

////////////////////////////////////////////////////////////////////////////////////////
// Private Methods
////////////////////////////////////////////////////////////////////////////////////////

func (s *SolRPC) getContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), s.timeout)
}

// post - Make a POST request to the Solana RPC
func (s *SolRPC) post(method string, params interface{}) ([]byte, error) {
	ctx, cancel := s.getContext()
	defer cancel()

	payload := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  method,
		"params":  params,
	}

	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", s.host, bytes.NewBuffer(payloadBytes))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: s.timeout}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to perform request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("received non-200 response status: %d", resp.StatusCode)
	}

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	return respBody, nil
}

////////////////////////////////////////////////////////////////////////////////////////
// RPC Methods
////////////////////////////////////////////////////////////////////////////////////////

// GetBlockHeight - makes a getSlot RPC call to the Solana RPC. Returns the latest
// "finalized" block height.
func (s *SolRPC) GetBlockHeight() (uint64, error) {
	params := []interface{}{
		map[string]interface{}{
			"commitment": "finalized",
		},
	}
	respBody, err := s.post("getSlot", params)
	if err != nil {
		return 0, fmt.Errorf("failed to call getSlot: %w", err)
	}

	var result struct {
		Result uint64 `json:"result"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return 0, fmt.Errorf("failed to unmarshal response: %w", err)
	}

	return result.Result, nil
}

func (s *SolRPC) GetLatestBlockhash() (string, error) {
	params := []interface{}{}
	respBody, err := s.post("getLatestBlockhash", params)
	if err != nil {
		return "", fmt.Errorf("failed to call getLatestBlockhash: %w", err)
	}

	var result struct {
		Result struct {
			Value struct {
				Blockhash string `json:"blockhash"`
			} `json:"value"`
		} `json:"result"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return "", fmt.Errorf("failed to unmarshal response: %w", err)
	}

	return result.Result.Value.Blockhash, nil
}

func (s *SolRPC) GetBalance(address, commitment string, minSlot uint64) (uint64, error) {
	additionalParams := map[string]interface{}{"commitment": commitment}
	if minSlot > 0 {
		additionalParams["minContextSlot"] = minSlot
	}
	params := []interface{}{
		address,
		additionalParams,
	}
	respBody, err := s.post("getBalance", params)
	if err != nil {
		return 0, fmt.Errorf("failed to call getBalance: %w", err)
	}

	var result struct {
		Result struct {
			Value uint64 `json:"value"`
		} `json:"result"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return 0, fmt.Errorf("failed to unmarshal response: %w", err)
	}

	return result.Result.Value, nil
}

func (s *SolRPC) BroadcastTx(rawTx string) (string, error) {
	params := []interface{}{rawTx, map[string]interface{}{"encoding": "base64"}}
	respBody, err := s.post("sendTransaction", params)
	if err != nil {
		return "", fmt.Errorf("failed to sendTransaction: %w", err)
	}

	var response struct {
		Result string `json:"result"`
		Error  struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error,omitempty"`
	}
	if err := json.Unmarshal(respBody, &response); err != nil {
		return "", fmt.Errorf("failed to unmarshal sendTransaction response: %w", err)
	}
	if response.Error.Message != "" {
		return "", fmt.Errorf("sendTransaction error: %s (code: %d)", response.Error.Message, response.Error.Code)
	}

	fmt.Println("response", response)

	return response.Result, nil
}
