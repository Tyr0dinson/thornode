package rpc

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"golang.org/x/crypto/blake2b"

	"gitlab.com/thorchain/thornode/v3/common/crypto/ss58"
)

// Client is a JSON-RPC client for Substrate-based chains.
type Client struct {
	host       string
	httpClient *http.Client
}

// NewClient creates a new Substrate RPC client.
func NewClient(host string, timeout time.Duration) *Client {
	return &Client{
		host: host,
		httpClient: &http.Client{
			Timeout: timeout,
		},
	}
}

// jsonRPCRequest is a standard JSON-RPC 2.0 request.
type jsonRPCRequest struct {
	JSONRPC string        `json:"jsonrpc"`
	ID      int           `json:"id"`
	Method  string        `json:"method"`
	Params  []interface{} `json:"params"`
}

// jsonRPCResponse is a standard JSON-RPC 2.0 response.
type jsonRPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int             `json:"id"`
	Result  json.RawMessage `json:"result"`
	Error   *jsonRPCError   `json:"error"`
}

// jsonRPCError represents a JSON-RPC error.
// Substrate includes error details in the "data" field (e.g., "Stale", "AlreadyImported").
type jsonRPCError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

func (e *jsonRPCError) Error() string {
	if len(e.Data) > 0 {
		return fmt.Sprintf("rpc error (code: %d): %s: %s", e.Code, e.Message, string(e.Data))
	}
	return fmt.Sprintf("rpc error (code: %d): %s", e.Code, e.Message)
}

// call executes a JSON-RPC call.
func (c *Client) call(method string, params []interface{}) (json.RawMessage, error) {
	req := jsonRPCRequest{
		JSONRPC: "2.0",
		ID:      1,
		Method:  method,
		Params:  params,
	}

	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	resp, err := c.httpClient.Post(c.host, "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("rpc request failed with status %d", resp.StatusCode)
	}

	const maxResponseSize = 16 << 20 // 16 MB
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize))
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	var rpcResp jsonRPCResponse
	if err := json.Unmarshal(respBody, &rpcResp); err != nil {
		return nil, fmt.Errorf("failed to unmarshal response: %w", err)
	}

	if rpcResp.Error != nil {
		return nil, rpcResp.Error
	}

	return rpcResp.Result, nil
}

// GetBlockHash returns the block hash for the given block number.
// If blockNumber is nil, returns the latest finalized block hash.
func (c *Client) GetBlockHash(blockNumber *uint64) (string, error) {
	var params []interface{}
	if blockNumber != nil {
		params = []interface{}{*blockNumber}
	}

	result, err := c.call("chain_getBlockHash", params)
	if err != nil {
		return "", fmt.Errorf("failed to get block hash: %w", err)
	}

	var hash string
	if err := json.Unmarshal(result, &hash); err != nil {
		return "", fmt.Errorf("failed to unmarshal block hash: %w", err)
	}

	return hash, nil
}

// GetFinalizedHead returns the hash of the latest finalized block.
func (c *Client) GetFinalizedHead() (string, error) {
	result, err := c.call("chain_getFinalizedHead", nil)
	if err != nil {
		return "", fmt.Errorf("failed to get finalized head: %w", err)
	}

	var hash string
	if err := json.Unmarshal(result, &hash); err != nil {
		return "", fmt.Errorf("failed to unmarshal finalized head: %w", err)
	}

	return hash, nil
}

// GetBlock returns the block for the given hash.
func (c *Client) GetBlock(hash string) (*Block, error) {
	result, err := c.call("chain_getBlock", []interface{}{hash})
	if err != nil {
		return nil, fmt.Errorf("failed to get block: %w", err)
	}

	var signedBlock SignedBlock
	if err := json.Unmarshal(result, &signedBlock); err != nil {
		return nil, fmt.Errorf("failed to unmarshal block: %w", err)
	}

	return &signedBlock.Block, nil
}

// GetHeader returns the block header for the given hash.
func (c *Client) GetHeader(hash string) (*Header, error) {
	result, err := c.call("chain_getHeader", []interface{}{hash})
	if err != nil {
		return nil, fmt.Errorf("failed to get header: %w", err)
	}

	var header Header
	if err := json.Unmarshal(result, &header); err != nil {
		return nil, fmt.Errorf("failed to unmarshal header: %w", err)
	}

	return &header, nil
}

// GetBalance returns the free balance for the given address at the given block hash.
// It queries System::Account storage directly via state_getStorage and SCALE-decodes
// the AccountInfo struct to extract the free balance.
func (c *Client) GetBalance(address, blockHash string, balanceSize int) (cosmos.Uint, error) {
	accountID, err := ss58DecodeAccountID(address)
	if err != nil {
		return cosmos.ZeroUint(), fmt.Errorf("failed to decode SS58 address: %w", err)
	}

	storageKey := systemAccountStorageKey(accountID)

	params := []interface{}{storageKey}
	if blockHash != "" {
		params = append(params, blockHash)
	}

	result, err := c.call("state_getStorage", params)
	if err != nil {
		return cosmos.ZeroUint(), fmt.Errorf("failed to get account storage: %w", err)
	}

	var hexData string
	if err = json.Unmarshal(result, &hexData); err != nil {
		return cosmos.ZeroUint(), fmt.Errorf("failed to unmarshal storage result: %w", err)
	}
	if hexData == "" {
		return cosmos.ZeroUint(), nil // account not found, zero balance
	}

	data, err := hex.DecodeString(strings.TrimPrefix(hexData, "0x"))
	if err != nil {
		return cosmos.ZeroUint(), fmt.Errorf("failed to decode storage hex: %w", err)
	}

	free, err := decodeAccountInfoFreeBalance(data, balanceSize)
	if err != nil {
		return cosmos.ZeroUint(), fmt.Errorf("failed to decode AccountInfo: %w", err)
	}

	return free, nil
}

// systemAccountStorageKey computes the storage key for System::Account(account_id).
// Key = twox128("System") ++ twox128("Account") ++ blake2_128_concat(account_id)
func systemAccountStorageKey(accountID []byte) string {
	// Well-known twox128 hashes (precomputed, never change)
	const systemPrefix = "26aa394eea5630e07c48ae0c9558cef7"
	const accountPrefix = "b99d880ec681799c0cf30e8886371da9"

	// blake2_128_concat = blake2b_128(account_id) ++ account_id
	h, _ := blake2b.New(16, nil) // 128-bit = 16 bytes
	h.Write(accountID)
	hash := h.Sum(nil)

	return "0x" + systemPrefix + accountPrefix +
		hex.EncodeToString(hash) + hex.EncodeToString(accountID)
}

// decodeAccountInfoFreeBalance extracts the free balance from a SCALE-encoded
// Substrate AccountInfo struct. The free balance starts at a fixed offset:
//
//   nonce(4) + consumers(4) + providers(4) + sufficients(4) = 16 bytes header
//
// AccountData.free is stored as either u64 (8 bytes) or u128 LE (16 bytes).
// We read the amount specified via balanceSize and return it as cosmos.Uint.
// This layout is stable across both old (misc_frozen/fee_frozen) and new
// (frozen/flags) AccountData variants.

func decodeAccountInfoFreeBalance(data []byte, balanceSize int) (cosmos.Uint, error) {
	const offset = 16 // 4 × u32 fields before AccountData.free

	if len(data) < offset+balanceSize {
		return cosmos.ZeroUint(), fmt.Errorf("AccountInfo data too short: %d bytes (need %d)", len(data), offset+balanceSize)
	}

	switch balanceSize {
	case 8:
		return cosmos.NewUint(binary.LittleEndian.Uint64(data[offset : offset+8])), nil
	case 16:
		return cosmos.ParseUint128(data[offset : offset+16])
	default:
		return cosmos.ZeroUint(), fmt.Errorf("unsupported balance size: %d", balanceSize)
	}
}

// ss58DecodeAccountID decodes an SS58 address to its 32-byte account ID.
// Uses the shared ss58 package which verifies the Blake2b-512 checksum.
func ss58DecodeAccountID(address string) ([]byte, error) {
	_, pubkey, err := ss58.Decode(address)
	if err != nil {
		return nil, fmt.Errorf("failed to decode SS58 address: %w", err)
	}
	return pubkey, nil
}

// GetNonce returns the account nonce for the given address.
func (c *Client) GetNonce(address string) (uint32, error) {
	result, err := c.call("system_accountNextIndex", []interface{}{address})
	if err != nil {
		return 0, fmt.Errorf("failed to get nonce: %w", err)
	}

	var nonce uint32
	if err := json.Unmarshal(result, &nonce); err != nil {
		return 0, fmt.Errorf("failed to unmarshal nonce: %w", err)
	}

	return nonce, nil
}

// GetRuntimeVersion returns the runtime version at the given block hash.
func (c *Client) GetRuntimeVersion(blockHash string) (*RuntimeVersion, error) {
	var params []interface{}
	if blockHash != "" {
		params = []interface{}{blockHash}
	}

	result, err := c.call("state_getRuntimeVersion", params)
	if err != nil {
		return nil, fmt.Errorf("failed to get runtime version: %w", err)
	}

	var rv RuntimeVersion
	if err := json.Unmarshal(result, &rv); err != nil {
		return nil, fmt.Errorf("failed to unmarshal runtime version: %w", err)
	}

	return &rv, nil
}

// GetGenesisHash returns the genesis block hash.
func (c *Client) GetGenesisHash() (string, error) {
	zero := uint64(0)
	return c.GetBlockHash(&zero)
}

// SubmitExtrinsic submits a signed extrinsic to the network.
func (c *Client) SubmitExtrinsic(hexEncoded string) (string, error) {
	result, err := c.call("author_submitExtrinsic", []interface{}{hexEncoded})
	if err != nil {
		return "", fmt.Errorf("failed to submit extrinsic: %w", err)
	}

	var hash string
	if err := json.Unmarshal(result, &hash); err != nil {
		return "", fmt.Errorf("failed to unmarshal extrinsic hash: %w", err)
	}

	return hash, nil
}

// GetEventsStorage fetches the raw System::Events storage at the given block hash.
// The storage key is twox128("System") ++ twox128("Events").
func (c *Client) GetEventsStorage(blockHash string) (string, error) {
	const eventsKey = "0x26aa394eea5630e07c48ae0c9558cef780d41e5e16056765bc8461851072c9d7"
	result, err := c.call("state_getStorage", []interface{}{eventsKey, blockHash})
	if err != nil {
		return "", fmt.Errorf("failed to get events storage: %w", err)
	}

	var hexData string
	if err := json.Unmarshal(result, &hexData); err != nil {
		return "", fmt.Errorf("failed to unmarshal events storage: %w", err)
	}

	return hexData, nil
}
