// Package thorchain provides a client interface for interacting with THORChain nodes
package thorchain

import (
	"context"
	"fmt"
	"time"

	sdk "github.com/cosmos/cosmos-sdk/types"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// Client defines the interface for THORChain node interactions
type Client interface {
	// Core operations
	SubmitMsgRegisterMemo(ctx context.Context, msg *types.MsgRegisterMemo) (*MsgRegisterMemoResponse, error)
	QueryRegisteredMemo(ctx context.Context, reference string) (*types.RegisteredMemo, error)
	
	// Quote operations
	GetSwapQuote(ctx context.Context, req *SwapQuoteRequest) (*SwapQuote, error)
	GetLiquidityQuote(ctx context.Context, req *LiquidityQuoteRequest) (*LiquidityQuote, error)
	GetLoanQuote(ctx context.Context, req *LoanQuoteRequest) (*LoanQuote, error)
	
	// Network information
	GetNodeInfo(ctx context.Context) (*NodeInfo, error)
	GetConstants(ctx context.Context) (*NetworkConstants, error)
	GetNetwork(ctx context.Context) (*Network, error)
	GetMimirs(ctx context.Context) (*Mimirs, error)
	
	// Pool information
	GetPool(ctx context.Context, asset common.Asset) (*Pool, error)
	GetPools(ctx context.Context) ([]*Pool, error)
	GetPoolStatus(ctx context.Context, asset common.Asset) (*PoolStatus, error)
	
	// Transaction status
	GetTransaction(ctx context.Context, txID string) (*Transaction, error)
	GetTransactionStatus(ctx context.Context, txID string) (*TransactionStatus, error)
	
	// Address and balance information
	GetInboundAddresses(ctx context.Context) ([]*InboundAddress, error)
	GetBalance(ctx context.Context, address common.Address) (*Balance, error)
	
	// Health and status
	Ping(ctx context.Context) error
	GetHealth(ctx context.Context) (*Health, error)
	
	// Connection management
	Connect() error
	Disconnect() error
	IsConnected() bool
}

// MsgRegisterMemoResponse represents the response from registering a memo
type MsgRegisterMemoResponse struct {
	Reference string    `json:"reference"`
	ExpiresAt int64     `json:"expires_at"`
	TxHash    string    `json:"tx_hash,omitempty"`
	Height    int64     `json:"height,omitempty"`
	Events    []Event   `json:"events,omitempty"`
}

// Event represents a blockchain event
type Event struct {
	Type       string            `json:"type"`
	Attributes map[string]string `json:"attributes"`
}

// SwapQuoteRequest represents a request for swap quotes
type SwapQuoteRequest struct {
	FromAsset         common.Asset   `json:"from_asset"`
	ToAsset           common.Asset   `json:"to_asset"`
	Amount            cosmos.Uint    `json:"amount"`
	Destination       common.Address `json:"destination,omitempty"`
	TolerateBasisPts  cosmos.Uint    `json:"tolerate_basis_pts,omitempty"`
	AffiliateAddress  common.Address `json:"affiliate_address,omitempty"`
	AffiliateBasisPts cosmos.Uint    `json:"affiliate_basis_pts,omitempty"`
	StreamingInterval uint64         `json:"streaming_interval,omitempty"`
	StreamingQuantity uint64         `json:"streaming_quantity,omitempty"`
}

// SwapQuote represents a swap quote response
type SwapQuote struct {
	ExpectedAmountOut     cosmos.Uint         `json:"expected_amount_out"`
	ExpectedAmountOutUSD  cosmos.Uint         `json:"expected_amount_out_usd"`
	OutboundDelayBlocks   int64               `json:"outbound_delay_blocks"`
	OutboundDelaySeconds  int64               `json:"outbound_delay_seconds"`
	Fees                  QuoteFees           `json:"fees"`
	SlippageBasisPoints   int64               `json:"slippage_basis_points"`
	StreamingSwapBlocks   int64               `json:"streaming_swap_blocks,omitempty"`
	StreamingSwapSeconds  int64               `json:"streaming_swap_seconds,omitempty"`
	TotalSwapSeconds      int64               `json:"total_swap_seconds,omitempty"`
	Router                common.Address      `json:"router,omitempty"`
	Memo                  string              `json:"memo"`
	Warning               string              `json:"warning,omitempty"`
	Notes                 string              `json:"notes,omitempty"`
	Errors                []string            `json:"errors,omitempty"`
	RecommendedMinAmountIn cosmos.Uint        `json:"recommended_min_amount_in,omitempty"`
}

// LiquidityQuoteRequest represents a request for liquidity quotes
type LiquidityQuoteRequest struct {
	Asset             common.Asset   `json:"asset"`
	RuneAmount        cosmos.Uint    `json:"rune_amount,omitempty"`
	AssetAmount       cosmos.Uint    `json:"asset_amount,omitempty"`
	AffiliateAddress  common.Address `json:"affiliate_address,omitempty"`
	AffiliateBasisPts cosmos.Uint    `json:"affiliate_basis_pts,omitempty"`
}

// LiquidityQuote represents a liquidity quote response
type LiquidityQuote struct {
	ExpectedWaitTime     time.Duration       `json:"expected_wait_time"`
	Fees                 QuoteFees           `json:"fees"`
	SlippageBasisPoints  int64               `json:"slippage_basis_points"`
	PoolShareBasisPoints int64               `json:"pool_share_basis_points"`
	Memo                 string              `json:"memo"`
	Warning              string              `json:"warning,omitempty"`
	Notes                string              `json:"notes,omitempty"`
	Errors               []string            `json:"errors,omitempty"`
}

// LoanQuoteRequest represents a request for loan quotes
type LoanQuoteRequest struct {
	Asset           common.Asset   `json:"asset"`
	Amount          cosmos.Uint    `json:"amount"`
	TargetAsset     common.Asset   `json:"target_asset"`
	Destination     common.Address `json:"destination,omitempty"`
	MinOut          cosmos.Uint    `json:"min_out,omitempty"`
	AffiliateAddress common.Address `json:"affiliate_address,omitempty"`
	AffiliateBasisPts cosmos.Uint  `json:"affiliate_basis_pts,omitempty"`
}

// LoanQuote represents a loan quote response
type LoanQuote struct {
	ExpectedAmountOut       cosmos.Uint         `json:"expected_amount_out"`
	ExpectedCollateral      cosmos.Uint         `json:"expected_collateral"`
	ExpectedDebt            cosmos.Uint         `json:"expected_debt"`
	ExpectedCollateralRatio cosmos.Uint         `json:"expected_collateral_ratio"`
	Fees                    QuoteFees           `json:"fees"`
	StreamingSwapBlocks     int64               `json:"streaming_swap_blocks,omitempty"`
	StreamingSwapSeconds    int64               `json:"streaming_swap_seconds,omitempty"`
	TotalOpenLoanSeconds    int64               `json:"total_open_loan_seconds,omitempty"`
	Memo                    string              `json:"memo"`
	Warning                 string              `json:"warning,omitempty"`
	Notes                   string              `json:"notes,omitempty"`
	Errors                  []string            `json:"errors,omitempty"`
}

// QuoteFees represents fees in a quote
type QuoteFees struct {
	Asset       common.Asset `json:"asset"`
	Affiliate   cosmos.Uint  `json:"affiliate"`
	Outbound    cosmos.Uint  `json:"outbound"`
	Liquidity   cosmos.Uint  `json:"liquidity"`
	Total       cosmos.Uint  `json:"total"`
	SlippageFee cosmos.Uint  `json:"slippage_fee"`
	TotalBps    int64        `json:"total_bps"`
}

// NodeInfo represents THORNode information
type NodeInfo struct {
	SeqNo                int64         `json:"seqno"`
	Version              string        `json:"version"`
	Network              string        `json:"network"`
	BondAddress          common.Address `json:"bond_address"`
	IP                   string        `json:"ip_address"`
	Port                 string        `json:"port"`
	Status               string        `json:"status"`
	StatusSince          int64         `json:"status_since"`
	NodeOperatorAddress  common.Address `json:"node_operator_address"`
	Jail                 Jail          `json:"jail"`
	PubKeySet            PubKeySet     `json:"pub_key_set"`
	ActiveBlockHeight    int64         `json:"active_block_height"`
	BondProviders        []BondProvider `json:"bond_providers"`
}

// NetworkConstants represents network constants
type NetworkConstants struct {
	EmissionCurve               int64 `json:"emission_curve"`
	BlocksPerYear               int64 `json:"blocks_per_year"`
	OutboundTransactionFee      int64 `json:"outbound_transaction_fee"`
	NativeTransactionFee        int64 `json:"native_transaction_fee"`
	PoolCycle                   int64 `json:"pool_cycle"`
	MinRunePoolDepth            int64 `json:"min_rune_pool_depth"`
	MaxAvailablePools           int64 `json:"max_available_pools"`
	StagedPoolCost              int64 `json:"staged_pool_cost"`
	MemolessTTL                 int64 `json:"memoless_ttl"`
	MemolessEnabled             int64 `json:"memoless_enabled"`
	MemolessMaxMemoLength       int64 `json:"memoless_max_memo_length"`
	MemolessCollisionLimit      int64 `json:"memoless_collision_limit"`
	MemolessCleanupInterval     int64 `json:"memoless_cleanup_interval"`
}

// Network represents network information
type Network struct {
	BondRewardRune          cosmos.Uint `json:"bond_reward_rune"`
	TotalBondUnits          cosmos.Uint `json:"total_bond_units"`
	TotalActiveBond         cosmos.Uint `json:"total_active_bond"`
	TotalStandbyBond        cosmos.Uint `json:"total_standby_bond"`
	MaxStandbyBond          cosmos.Uint `json:"max_standby_bond"`
	PoolActivationCountdown int64       `json:"pool_activation_countdown"`
	MinimumNodesForBFT      int64       `json:"minimum_nodes_for_bft"`
	TotalReserve            cosmos.Uint `json:"total_reserve"`
	VaultsActivating        int64       `json:"vaults_activating"`
	VaultsMigrating         int64       `json:"vaults_migrating"`
	VaultsActive            int64       `json:"vaults_active"`
}

// Mimirs represents mimir values
type Mimirs struct {
	Values map[string]int64 `json:"mimirs"`
}

// Pool represents a liquidity pool
type Pool struct {
	Asset               common.Asset `json:"asset"`
	ShortCode           string       `json:"short_code"`
	Status              string       `json:"status"`
	DecimalsPegged      bool         `json:"decimals_pegged"`
	PendingInboundAsset cosmos.Uint  `json:"pending_inbound_asset"`
	PendingInboundRune  cosmos.Uint  `json:"pending_inbound_rune"`
	BalanceAsset        cosmos.Uint  `json:"balance_asset"`
	BalanceRune         cosmos.Uint  `json:"balance_rune"`
	PoolUnits           cosmos.Uint  `json:"pool_units"`
	LPUnits             cosmos.Uint  `json:"lp_units"`
	SynthUnits          cosmos.Uint  `json:"synth_units"`
	SynthSupply         cosmos.Uint  `json:"synth_supply"`
	SaversDepth         cosmos.Uint  `json:"savers_depth"`
	SaversUnits         cosmos.Uint  `json:"savers_units"`
	SaversFillBps       cosmos.Uint  `json:"savers_fill_bps"`
	SaversAPR           cosmos.Uint  `json:"savers_apr"`
	EarnedFees          cosmos.Uint  `json:"earned_fees"`
}

// PoolStatus represents pool status information
type PoolStatus struct {
	Asset  common.Asset `json:"asset"`
	Status string       `json:"status"`
}

// Transaction represents a THORChain transaction
type Transaction struct {
	ID       string      `json:"id"`
	Chain    common.Chain `json:"chain"`
	FromAddress common.Address `json:"from_address"`
	ToAddress   common.Address `json:"to_address"`
	Coins       common.Coins   `json:"coins"`
	Gas         common.Gas     `json:"gas"`
	Memo        string         `json:"memo"`
	Height      int64          `json:"height"`
}

// TransactionStatus represents transaction status
type TransactionStatus struct {
	TxID     string                 `json:"tx_id"`
	Stages   map[string]TxStage     `json:"stages"`
	Status   string                 `json:"status"`
	PlannedOutTxs []PlannedOutTx    `json:"planned_out_txs,omitempty"`
	OutTxs   []Transaction          `json:"out_txs,omitempty"`
}

// TxStage represents a transaction stage
type TxStage struct {
	Depth          int64  `json:"depth"`
	InboundObserved *StageStatus `json:"inbound_observed,omitempty"`
	InboundConfirmed *StageStatus `json:"inbound_confirmed,omitempty"`
	InboundFinalised *StageStatus `json:"inbound_finalised,omitempty"`
	SwapStatus       *StageStatus `json:"swap_status,omitempty"`
	SwapFinalised    *StageStatus `json:"swap_finalised,omitempty"`
	OutboundDelay    *StageStatus `json:"outbound_delay,omitempty"`
	OutboundSigned   *StageStatus `json:"outbound_signed,omitempty"`
}

// StageStatus represents the status of a transaction stage
type StageStatus struct {
	Completed bool  `json:"completed"`
	Height    int64 `json:"height"`
}

// PlannedOutTx represents a planned outbound transaction
type PlannedOutTx struct {
	Chain       common.Chain   `json:"chain"`
	ToAddress   common.Address `json:"to_address"`
	Coin        common.Coin    `json:"coin"`
	Refund      bool           `json:"refund"`
}

// InboundAddress represents inbound address information
type InboundAddress struct {
	Chain   common.Chain   `json:"chain"`
	Address common.Address `json:"address"`
	Router  common.Address `json:"router,omitempty"`
	Global  bool           `json:"global_trading_paused"`
	Halted  bool           `json:"chain_trading_paused"`
	LP      bool           `json:"chain_lp_actions_paused"`
}

// Balance represents account balance information
type Balance struct {
	Address common.Address `json:"address"`
	Coins   common.Coins   `json:"coins"`
}

// Health represents node health status
type Health struct {
	Database      bool `json:"database"`
	ScannerHeight int64 `json:"scanner_height"`
	Catching_up   bool `json:"catching_up"`
}

// Supporting types
type Jail struct {
	NodeAddress   common.Address `json:"node_address"`
	ReleaseHeight int64         `json:"release_height"`
	Reason        string        `json:"reason"`
}

type PubKeySet struct {
	Secp256k1 string `json:"secp256k1"`
	Ed25519   string `json:"ed25519"`
}

type BondProvider struct {
	BondAddress common.Address `json:"bond_address"`
	Bond        cosmos.Uint    `json:"bond"`
}

// ClientConfig represents configuration for the THORNode client
type ClientConfig struct {
	NodeURL       string        `json:"node_url"`
	Network       string        `json:"network"`        // mainnet, stagenet, testnet
	Timeout       time.Duration `json:"timeout"`
	RetryAttempts int           `json:"retry_attempts"`
	RetryDelay    time.Duration `json:"retry_delay"`
	TLSSkipVerify bool          `json:"tls_skip_verify"`
	
	// Rate limiting
	RateLimit     int           `json:"rate_limit"`      // requests per second
	BurstLimit    int           `json:"burst_limit"`     // burst allowance
	
	// Authentication (if required)
	APIKey        string        `json:"api_key,omitempty"`
	
	// Circuit breaker settings
	CircuitBreakerSettings CircuitBreakerConfig `json:"circuit_breaker"`
}

// CircuitBreakerConfig represents circuit breaker configuration
type CircuitBreakerConfig struct {
	Enabled                bool          `json:"enabled"`
	FailureThreshold       int           `json:"failure_threshold"`        // failures before opening
	SuccessThreshold       int           `json:"success_threshold"`        // successes to close
	HalfOpenMaxRequests    int           `json:"half_open_max_requests"`   // max requests in half-open state
	Timeout                time.Duration `json:"timeout"`                  // time before attempting half-open
}

// NewDefaultClientConfig returns a default client configuration
func NewDefaultClientConfig(nodeURL string) *ClientConfig {
	return &ClientConfig{
		NodeURL:       nodeURL,
		Network:       "mainnet",
		Timeout:       30 * time.Second,
		RetryAttempts: 3,
		RetryDelay:    time.Second,
		TLSSkipVerify: false,
		RateLimit:     10, // 10 requests per second
		BurstLimit:    20, // allow bursts up to 20
		CircuitBreakerSettings: CircuitBreakerConfig{
			Enabled:             true,
			FailureThreshold:    5,
			SuccessThreshold:    3,
			HalfOpenMaxRequests: 5,
			Timeout:             60 * time.Second,
		},
	}
}

// ClientError represents an error from the THORNode client
type ClientError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Details string `json:"details,omitempty"`
	
	// Original error for wrapping
	Err error `json:"-"`
}

func (e *ClientError) Error() string {
	if e.Details != "" {
		return fmt.Sprintf("THORNode client error %d: %s (%s)", e.Code, e.Message, e.Details)
	}
	return fmt.Sprintf("THORNode client error %d: %s", e.Code, e.Message)
}

func (e *ClientError) Unwrap() error {
	return e.Err
}

// IsRetryable determines if an error should trigger a retry
func (e *ClientError) IsRetryable() bool {
	// Network errors, timeouts, and 5xx responses are typically retryable
	return e.Code >= 500 || e.Code == 408 || e.Code == 429 || e.Code == 0
}

// Common error codes
const (
	ErrCodeNetworkError     = 0
	ErrCodeBadRequest      = 400
	ErrCodeUnauthorized    = 401
	ErrCodeForbidden       = 403
	ErrCodeNotFound        = 404
	ErrCodeMethodNotAllowed = 405
	ErrCodeTimeout         = 408
	ErrCodeTooManyRequests = 429
	ErrCodeInternalServer  = 500
	ErrCodeBadGateway      = 502
	ErrCodeServiceUnavailable = 503
	ErrCodeGatewayTimeout  = 504
)

// NewClientError creates a new ClientError
func NewClientError(code int, message string, details string, err error) *ClientError {
	return &ClientError{
		Code:    code,
		Message: message,
		Details: details,
		Err:     err,
	}
}

// Mock interfaces for testing (will be implemented in client_mock.go)
//go:generate mockgen -source=client.go -destination=client_mock.go -package=thorchain

