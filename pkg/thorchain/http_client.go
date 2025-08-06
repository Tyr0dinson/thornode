package thorchain

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// HTTPClient implements the Client interface with HTTP transport
type HTTPClient struct {
	config         *ClientConfig
	httpClient     *http.Client
	rateLimiter    *rate.Limiter
	circuitBreaker *CircuitBreaker
	baseURL        *url.URL
	mutex          sync.RWMutex
	connected      bool
}

// NewHTTPClient creates a new HTTP client for THORNode communication
func NewHTTPClient(config *ClientConfig) (*HTTPClient, error) {
	if config == nil {
		return nil, fmt.Errorf("config cannot be nil")
	}

	baseURL, err := url.Parse(config.NodeURL)
	if err != nil {
		return nil, fmt.Errorf("invalid node URL: %w", err)
	}

	// Create HTTP client with timeout and custom transport
	httpClient := &http.Client{
		Timeout: config.Timeout,
		Transport: &http.Transport{
			DialContext: (&net.Dialer{
				Timeout:   10 * time.Second,
				KeepAlive: 30 * time.Second,
			}).DialContext,
			TLSHandshakeTimeout:   10 * time.Second,
			ResponseHeaderTimeout: config.Timeout,
			ExpectContinueTimeout: 1 * time.Second,
			MaxIdleConns:          100,
			MaxIdleConnsPerHost:   10,
			IdleConnTimeout:       90 * time.Second,
			DisableCompression:    false,
		},
	}

	// Setup rate limiter
	var rateLimiter *rate.Limiter
	if config.RateLimit > 0 {
		rateLimiter = rate.NewLimiter(rate.Limit(config.RateLimit), config.BurstLimit)
	}

	// Setup circuit breaker
	var circuitBreaker *CircuitBreaker
	if config.CircuitBreakerSettings.Enabled {
		circuitBreaker = NewCircuitBreaker(&config.CircuitBreakerSettings)
	}

	return &HTTPClient{
		config:         config,
		httpClient:     httpClient,
		rateLimiter:    rateLimiter,
		circuitBreaker: circuitBreaker,
		baseURL:        baseURL,
		connected:      false,
	}, nil
}

// Connect establishes connection to the THORNode
func (c *HTTPClient) Connect() error {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	// Test connection with a simple ping
	if err := c.ping(context.Background()); err != nil {
		return fmt.Errorf("failed to connect to THORNode: %w", err)
	}

	c.connected = true
	return nil
}

// Disconnect closes the connection to the THORNode
func (c *HTTPClient) Disconnect() error {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	c.connected = false
	c.httpClient.CloseIdleConnections()
	return nil
}

// IsConnected returns true if connected to the THORNode
func (c *HTTPClient) IsConnected() bool {
	c.mutex.RLock()
	defer c.mutex.RUnlock()
	return c.connected
}

// SubmitMsgRegisterMemo submits a memo registration message to THORChain
func (c *HTTPClient) SubmitMsgRegisterMemo(ctx context.Context, msg *types.MsgRegisterMemo) (*MsgRegisterMemoResponse, error) {
	if msg == nil {
		return nil, NewClientError(ErrCodeBadRequest, "message cannot be nil", "", nil)
	}

	// Validate message
	if err := msg.ValidateBasicEnhanced(); err != nil {
		return nil, NewClientError(ErrCodeBadRequest, "invalid message", err.Error(), err)
	}

	// Create transaction body
	txBody := map[string]interface{}{
		"messages": []interface{}{msg},
		"memo":     "",
		"timeout_height": "0",
		"extension_options": []interface{}{},
		"non_critical_extension_options": []interface{}{},
	}

	// Submit transaction
	endpoint := "/cosmos/tx/v1beta1/txs"
	response, err := c.doRequest(ctx, "POST", endpoint, txBody)
	if err != nil {
		return nil, fmt.Errorf("failed to submit memo registration: %w", err)
	}

	// Parse response
	var txResponse struct {
		TxResponse struct {
			TxHash string `json:"txhash"`
			Height string `json:"height"`
			Code   int    `json:"code"`
			RawLog string `json:"raw_log"`
			Events []struct {
				Type       string `json:"type"`
				Attributes []struct {
					Key   string `json:"key"`
					Value string `json:"value"`
				} `json:"attributes"`
			} `json:"events"`
		} `json:"tx_response"`
	}

	if err := json.Unmarshal(response, &txResponse); err != nil {
		return nil, NewClientError(ErrCodeInternalServer, "failed to parse response", err.Error(), err)
	}

	if txResponse.TxResponse.Code != 0 {
		return nil, NewClientError(ErrCodeBadRequest, "transaction failed", txResponse.TxResponse.RawLog, nil)
	}

	// Extract reference and expiry from events
	var reference string
	var expiresAt int64

	for _, event := range txResponse.TxResponse.Events {
		if event.Type == "memo_registered" {
			for _, attr := range event.Attributes {
				switch attr.Key {
				case "reference":
					reference = attr.Value
				case "expires_at":
					if expiry, err := cosmos.ParseUint(attr.Value); err == nil {
						expiresAt = int64(expiry.Uint64())
					}
				}
			}
		}
	}

	if reference == "" {
		return nil, NewClientError(ErrCodeInternalServer, "reference not found in response", "", nil)
	}

	// Convert events
	var events []Event
	for _, event := range txResponse.TxResponse.Events {
		attributes := make(map[string]string)
		for _, attr := range event.Attributes {
			attributes[attr.Key] = attr.Value
		}
		events = append(events, Event{
			Type:       event.Type,
			Attributes: attributes,
		})
	}

	height, _ := cosmos.ParseUint(txResponse.TxResponse.Height)

	return &MsgRegisterMemoResponse{
		Reference: reference,
		ExpiresAt: expiresAt,
		TxHash:    txResponse.TxResponse.TxHash,
		Height:    int64(height.Uint64()),
		Events:    events,
	}, nil
}

// QueryRegisteredMemo queries a registered memo by reference
func (c *HTTPClient) QueryRegisteredMemo(ctx context.Context, reference string) (*types.RegisteredMemo, error) {
	if reference == "" {
		return nil, NewClientError(ErrCodeBadRequest, "reference cannot be empty", "", nil)
	}

	if err := types.ValidateReference(reference); err != nil {
		return nil, NewClientError(ErrCodeBadRequest, "invalid reference format", err.Error(), err)
	}

	endpoint := fmt.Sprintf("/thorchain/memo/%s", reference)
	response, err := c.doRequest(ctx, "GET", endpoint, nil)
	if err != nil {
		if clientErr, ok := err.(*ClientError); ok && clientErr.Code == ErrCodeNotFound {
			return nil, NewClientError(ErrCodeNotFound, "memo not found", reference, nil)
		}
		return nil, fmt.Errorf("failed to query memo: %w", err)
	}

	var memo types.RegisteredMemo
	if err := json.Unmarshal(response, &memo); err != nil {
		return nil, NewClientError(ErrCodeInternalServer, "failed to parse memo response", err.Error(), err)
	}

	return &memo, nil
}

// GetSwapQuote gets a swap quote from THORNode
func (c *HTTPClient) GetSwapQuote(ctx context.Context, req *SwapQuoteRequest) (*SwapQuote, error) {
	if req == nil {
		return nil, NewClientError(ErrCodeBadRequest, "request cannot be nil", "", nil)
	}

	// Build query parameters
	params := url.Values{}
	params.Set("from_asset", req.FromAsset.String())
	params.Set("to_asset", req.ToAsset.String())
	params.Set("amount", req.Amount.String())

	if !req.Destination.IsEmpty() {
		params.Set("destination", req.Destination.String())
	}
	if !req.TolerateBasisPts.IsZero() {
		params.Set("tolerate_basis_pts", req.TolerateBasisPts.String())
	}
	if !req.AffiliateAddress.IsEmpty() {
		params.Set("affiliate_address", req.AffiliateAddress.String())
	}
	if !req.AffiliateBasisPts.IsZero() {
		params.Set("affiliate_basis_pts", req.AffiliateBasisPts.String())
	}
	if req.StreamingInterval > 0 {
		params.Set("streaming_interval", fmt.Sprintf("%d", req.StreamingInterval))
	}
	if req.StreamingQuantity > 0 {
		params.Set("streaming_quantity", fmt.Sprintf("%d", req.StreamingQuantity))
	}

	endpoint := fmt.Sprintf("/thorchain/quote/swap?%s", params.Encode())
	response, err := c.doRequest(ctx, "GET", endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get swap quote: %w", err)
	}

	var quote SwapQuote
	if err := json.Unmarshal(response, &quote); err != nil {
		return nil, NewClientError(ErrCodeInternalServer, "failed to parse quote response", err.Error(), err)
	}

	return &quote, nil
}

// GetLiquidityQuote gets a liquidity quote from THORNode
func (c *HTTPClient) GetLiquidityQuote(ctx context.Context, req *LiquidityQuoteRequest) (*LiquidityQuote, error) {
	if req == nil {
		return nil, NewClientError(ErrCodeBadRequest, "request cannot be nil", "", nil)
	}

	// Build query parameters
	params := url.Values{}
	params.Set("asset", req.Asset.String())

	if !req.RuneAmount.IsZero() {
		params.Set("rune_amount", req.RuneAmount.String())
	}
	if !req.AssetAmount.IsZero() {
		params.Set("asset_amount", req.AssetAmount.String())
	}
	if !req.AffiliateAddress.IsEmpty() {
		params.Set("affiliate_address", req.AffiliateAddress.String())
	}
	if !req.AffiliateBasisPts.IsZero() {
		params.Set("affiliate_basis_pts", req.AffiliateBasisPts.String())
	}

	endpoint := fmt.Sprintf("/thorchain/quote/add?%s", params.Encode())
	response, err := c.doRequest(ctx, "GET", endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get liquidity quote: %w", err)
	}

	var quote LiquidityQuote
	if err := json.Unmarshal(response, &quote); err != nil {
		return nil, NewClientError(ErrCodeInternalServer, "failed to parse quote response", err.Error(), err)
	}

	return &quote, nil
}

// GetLoanQuote gets a loan quote from THORNode
func (c *HTTPClient) GetLoanQuote(ctx context.Context, req *LoanQuoteRequest) (*LoanQuote, error) {
	if req == nil {
		return nil, NewClientError(ErrCodeBadRequest, "request cannot be nil", "", nil)
	}

	// Build query parameters
	params := url.Values{}
	params.Set("asset", req.Asset.String())
	params.Set("amount", req.Amount.String())
	params.Set("target_asset", req.TargetAsset.String())

	if !req.Destination.IsEmpty() {
		params.Set("destination", req.Destination.String())
	}
	if !req.MinOut.IsZero() {
		params.Set("min_out", req.MinOut.String())
	}
	if !req.AffiliateAddress.IsEmpty() {
		params.Set("affiliate_address", req.AffiliateAddress.String())
	}
	if !req.AffiliateBasisPts.IsZero() {
		params.Set("affiliate_basis_pts", req.AffiliateBasisPts.String())
	}

	endpoint := fmt.Sprintf("/thorchain/quote/loan/open?%s", params.Encode())
	response, err := c.doRequest(ctx, "GET", endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get loan quote: %w", err)
	}

	var quote LoanQuote
	if err := json.Unmarshal(response, &quote); err != nil {
		return nil, NewClientError(ErrCodeInternalServer, "failed to parse quote response", err.Error(), err)
	}

	return &quote, nil
}

// GetNodeInfo gets node information
func (c *HTTPClient) GetNodeInfo(ctx context.Context) (*NodeInfo, error) {
	endpoint := "/thorchain/node"
	response, err := c.doRequest(ctx, "GET", endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get node info: %w", err)
	}

	var nodeInfo NodeInfo
	if err := json.Unmarshal(response, &nodeInfo); err != nil {
		return nil, NewClientError(ErrCodeInternalServer, "failed to parse node info", err.Error(), err)
	}

	return &nodeInfo, nil
}

// GetConstants gets network constants
func (c *HTTPClient) GetConstants(ctx context.Context) (*NetworkConstants, error) {
	endpoint := "/thorchain/constants"
	response, err := c.doRequest(ctx, "GET", endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get constants: %w", err)
	}

	var constants NetworkConstants
	if err := json.Unmarshal(response, &constants); err != nil {
		return nil, NewClientError(ErrCodeInternalServer, "failed to parse constants", err.Error(), err)
	}

	return &constants, nil
}

// GetNetwork gets network information
func (c *HTTPClient) GetNetwork(ctx context.Context) (*Network, error) {
	endpoint := "/thorchain/network"
	response, err := c.doRequest(ctx, "GET", endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get network info: %w", err)
	}

	var network Network
	if err := json.Unmarshal(response, &network); err != nil {
		return nil, NewClientError(ErrCodeInternalServer, "failed to parse network info", err.Error(), err)
	}

	return &network, nil
}

// GetMimirs gets mimir values
func (c *HTTPClient) GetMimirs(ctx context.Context) (*Mimirs, error) {
	endpoint := "/thorchain/mimir"
	response, err := c.doRequest(ctx, "GET", endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get mimirs: %w", err)
	}

	var mimirs Mimirs
	if err := json.Unmarshal(response, &mimirs); err != nil {
		return nil, NewClientError(ErrCodeInternalServer, "failed to parse mimirs", err.Error(), err)
	}

	return &mimirs, nil
}

// GetPool gets pool information
func (c *HTTPClient) GetPool(ctx context.Context, asset common.Asset) (*Pool, error) {
	if asset.IsEmpty() {
		return nil, NewClientError(ErrCodeBadRequest, "asset cannot be empty", "", nil)
	}

	endpoint := fmt.Sprintf("/thorchain/pool/%s", asset.String())
	response, err := c.doRequest(ctx, "GET", endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get pool: %w", err)
	}

	var pool Pool
	if err := json.Unmarshal(response, &pool); err != nil {
		return nil, NewClientError(ErrCodeInternalServer, "failed to parse pool", err.Error(), err)
	}

	return &pool, nil
}

// GetPools gets all pools
func (c *HTTPClient) GetPools(ctx context.Context) ([]*Pool, error) {
	endpoint := "/thorchain/pools"
	response, err := c.doRequest(ctx, "GET", endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get pools: %w", err)
	}

	var pools []*Pool
	if err := json.Unmarshal(response, &pools); err != nil {
		return nil, NewClientError(ErrCodeInternalServer, "failed to parse pools", err.Error(), err)
	}

	return pools, nil
}

// GetPoolStatus gets pool status
func (c *HTTPClient) GetPoolStatus(ctx context.Context, asset common.Asset) (*PoolStatus, error) {
	if asset.IsEmpty() {
		return nil, NewClientError(ErrCodeBadRequest, "asset cannot be empty", "", nil)
	}

	endpoint := fmt.Sprintf("/thorchain/pool/%s/status", asset.String())
	response, err := c.doRequest(ctx, "GET", endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get pool status: %w", err)
	}

	var status PoolStatus
	if err := json.Unmarshal(response, &status); err != nil {
		return nil, NewClientError(ErrCodeInternalServer, "failed to parse pool status", err.Error(), err)
	}

	return &status, nil
}

// GetTransaction gets transaction information
func (c *HTTPClient) GetTransaction(ctx context.Context, txID string) (*Transaction, error) {
	if txID == "" {
		return nil, NewClientError(ErrCodeBadRequest, "txID cannot be empty", "", nil)
	}

	endpoint := fmt.Sprintf("/cosmos/tx/v1beta1/txs/%s", txID)
	response, err := c.doRequest(ctx, "GET", endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get transaction: %w", err)
	}

	var txResponse struct {
		Tx         Transaction `json:"tx"`
		TxResponse struct {
			Height string `json:"height"`
		} `json:"tx_response"`
	}

	if err := json.Unmarshal(response, &txResponse); err != nil {
		return nil, NewClientError(ErrCodeInternalServer, "failed to parse transaction", err.Error(), err)
	}

	// Set height from response
	if height, err := cosmos.ParseUint(txResponse.TxResponse.Height); err == nil {
		txResponse.Tx.Height = int64(height.Uint64())
	}

	return &txResponse.Tx, nil
}

// GetTransactionStatus gets transaction status
func (c *HTTPClient) GetTransactionStatus(ctx context.Context, txID string) (*TransactionStatus, error) {
	if txID == "" {
		return nil, NewClientError(ErrCodeBadRequest, "txID cannot be empty", "", nil)
	}

	endpoint := fmt.Sprintf("/thorchain/tx/status/%s", txID)
	response, err := c.doRequest(ctx, "GET", endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get transaction status: %w", err)
	}

	var status TransactionStatus
	if err := json.Unmarshal(response, &status); err != nil {
		return nil, NewClientError(ErrCodeInternalServer, "failed to parse transaction status", err.Error(), err)
	}

	return &status, nil
}

// GetInboundAddresses gets inbound addresses
func (c *HTTPClient) GetInboundAddresses(ctx context.Context) ([]*InboundAddress, error) {
	endpoint := "/thorchain/inbound_addresses"
	response, err := c.doRequest(ctx, "GET", endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get inbound addresses: %w", err)
	}

	var addresses []*InboundAddress
	if err := json.Unmarshal(response, &addresses); err != nil {
		return nil, NewClientError(ErrCodeInternalServer, "failed to parse inbound addresses", err.Error(), err)
	}

	return addresses, nil
}

// GetBalance gets balance for an address
func (c *HTTPClient) GetBalance(ctx context.Context, address common.Address) (*Balance, error) {
	if address.IsEmpty() {
		return nil, NewClientError(ErrCodeBadRequest, "address cannot be empty", "", nil)
	}

	endpoint := fmt.Sprintf("/cosmos/bank/v1beta1/balances/%s", address.String())
	response, err := c.doRequest(ctx, "GET", endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get balance: %w", err)
	}

	var balanceResponse struct {
		Balances []struct {
			Denom  string `json:"denom"`
			Amount string `json:"amount"`
		} `json:"balances"`
	}

	if err := json.Unmarshal(response, &balanceResponse); err != nil {
		return nil, NewClientError(ErrCodeInternalServer, "failed to parse balance", err.Error(), err)
	}

	// Convert to common.Coins
	var coins common.Coins
	for _, bal := range balanceResponse.Balances {
		asset, err := common.NewAsset(bal.Denom)
		if err != nil {
			continue // Skip invalid assets
		}
		amount, err := cosmos.ParseUint(bal.Amount)
		if err != nil {
			continue // Skip invalid amounts
		}
		coins = append(coins, common.NewCoin(asset, amount))
	}

	return &Balance{
		Address: address,
		Coins:   coins,
	}, nil
}

// Ping checks connectivity to THORNode
func (c *HTTPClient) Ping(ctx context.Context) error {
	return c.ping(ctx)
}

// ping performs the actual ping
func (c *HTTPClient) ping(ctx context.Context) error {
	endpoint := "/health"
	_, err := c.doRequest(ctx, "GET", endpoint, nil)
	return err
}

// GetHealth gets node health status
func (c *HTTPClient) GetHealth(ctx context.Context) (*Health, error) {
	endpoint := "/health"
	response, err := c.doRequest(ctx, "GET", endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get health: %w", err)
	}

	var health Health
	if err := json.Unmarshal(response, &health); err != nil {
		return nil, NewClientError(ErrCodeInternalServer, "failed to parse health", err.Error(), err)
	}

	return &health, nil
}

// doRequest performs HTTP requests with retry logic, rate limiting, and circuit breaking
func (c *HTTPClient) doRequest(ctx context.Context, method, endpoint string, body interface{}) ([]byte, error) {
	// Rate limiting
	if c.rateLimiter != nil {
		if err := c.rateLimiter.Wait(ctx); err != nil {
			return nil, NewClientError(ErrCodeTooManyRequests, "rate limit exceeded", err.Error(), err)
		}
	}

	// Circuit breaker
	if c.circuitBreaker != nil {
		return c.circuitBreaker.Execute(func() ([]byte, error) {
			return c.doHTTPRequest(ctx, method, endpoint, body)
		})
	}

	return c.doHTTPRequest(ctx, method, endpoint, body)
}

// doHTTPRequest performs the actual HTTP request with retries
func (c *HTTPClient) doHTTPRequest(ctx context.Context, method, endpoint string, body interface{}) ([]byte, error) {
	var lastErr error
	
	for attempt := 0; attempt <= c.config.RetryAttempts; attempt++ {
		// Add delay for retries
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(c.config.RetryDelay * time.Duration(attempt)):
			}
		}

		response, err := c.performHTTPRequest(ctx, method, endpoint, body)
		if err == nil {
			return response, nil
		}

		lastErr = err

		// Check if error is retryable
		if clientErr, ok := err.(*ClientError); ok && !clientErr.IsRetryable() {
			break // Non-retryable error, stop retrying
		}

		// Don't retry on context cancellation
		if ctx.Err() != nil {
			break
		}
	}

	return nil, lastErr
}

// performHTTPRequest performs a single HTTP request
func (c *HTTPClient) performHTTPRequest(ctx context.Context, method, endpoint string, body interface{}) ([]byte, error) {
	// Build URL
	requestURL := c.baseURL.ResolveReference(&url.URL{Path: endpoint})

	// Prepare request body
	var bodyReader io.Reader
	if body != nil {
		bodyBytes, err := json.Marshal(body)
		if err != nil {
			return nil, NewClientError(ErrCodeBadRequest, "failed to marshal request body", err.Error(), err)
		}
		bodyReader = bytes.NewReader(bodyBytes)
	}

	// Create request
	req, err := http.NewRequestWithContext(ctx, method, requestURL.String(), bodyReader)
	if err != nil {
		return nil, NewClientError(ErrCodeNetworkError, "failed to create request", err.Error(), err)
	}

	// Set headers
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", fmt.Sprintf("THORNode-Client/1.0 (%s)", c.config.Network))

	// Add API key if configured
	if c.config.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.config.APIKey)
	}

	// Perform request
	resp, err := c.httpClient.Do(req)
	if err != nil {
		// Determine error type
		if strings.Contains(err.Error(), "timeout") {
			return nil, NewClientError(ErrCodeTimeout, "request timeout", err.Error(), err)
		}
		return nil, NewClientError(ErrCodeNetworkError, "network error", err.Error(), err)
	}
	defer resp.Body.Close()

	// Read response body
	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, NewClientError(ErrCodeNetworkError, "failed to read response", err.Error(), err)
	}

	// Check status code
	if resp.StatusCode >= 400 {
		var errorMessage string
		
		// Try to parse error response
		var errorResp struct {
			Error   string `json:"error"`
			Message string `json:"message"`
			Details string `json:"details"`
		}
		if json.Unmarshal(responseBody, &errorResp) == nil {
			if errorResp.Error != "" {
				errorMessage = errorResp.Error
			} else if errorResp.Message != "" {
				errorMessage = errorResp.Message
			}
		}
		
		if errorMessage == "" {
			errorMessage = "HTTP " + resp.Status
		}

		return nil, NewClientError(resp.StatusCode, errorMessage, string(responseBody), nil)
	}

	return responseBody, nil
}