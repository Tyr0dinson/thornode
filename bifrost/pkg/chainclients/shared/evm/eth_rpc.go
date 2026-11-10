package evm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net"
	"strings"
	"sync/atomic"
	"time"

	"github.com/ethereum/go-ethereum"
	ecommon "github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	etypes "github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethereum/go-ethereum/rpc"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	btypes "gitlab.com/thorchain/thornode/v3/bifrost/blockscanner/types"
	"gitlab.com/thorchain/thornode/v3/common"
)

// compactHeaderCooldown is how long reads skip the compact method after it
// failed but the standard read succeeded. Only reads overlapping its expiry probe
// again, which bounds the cost of an endpoint that never supports the method, and
// a transient failure cannot cost a healthy endpoint the optimization for the life
// of the process.
const compactHeaderCooldown = 10 * time.Minute

// EthRPC is a struct that interacts with an ETH RPC compatible blockchain
type EthRPC struct {
	client         *ethclient.Client
	timeout        time.Duration
	logger         zerolog.Logger
	compactHeaders bool
	compactRetryAt atomic.Int64 // unix nanoseconds; compact reads resume at this time
}

func NewEthRPC(client *ethclient.Client, timeout time.Duration, chain string) (*EthRPC, error) {
	return &EthRPC{
		client:         client,
		timeout:        timeout,
		logger:         log.Logger.With().Str("module", "eth_rpc").Str("chain", chain).Logger(),
		compactHeaders: chain == common.BSCChain.String(),
	}, nil
}

func (e *EthRPC) getContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), e.timeout)
}

func (e *EthRPC) EstimateGas(from string, tx *etypes.Transaction) (uint64, error) {
	ctx, cancel := e.getContext()
	defer cancel()
	return e.client.EstimateGas(ctx, ethereum.CallMsg{
		From:     ecommon.HexToAddress(from),
		To:       tx.To(),
		GasPrice: tx.GasPrice(),
		// Gas:      tx.Gas(),
		Value: tx.Value(),
		Data:  tx.Data(),
	})
}

func (e *EthRPC) GetReceipt(hash string) (*etypes.Receipt, error) {
	ctx, cancel := e.getContext()
	defer cancel()
	var raw json.RawMessage
	if err := e.client.Client().CallContext(ctx, &raw, "eth_getTransactionReceipt", ecommon.HexToHash(hash)); err != nil {
		return nil, err
	}
	if len(raw) == 0 || string(raw) == "null" {
		return nil, ethereum.NotFound
	}
	// geth decodes an omitted status as zero. That default must never become
	// proof of failed execution and authorize a financial correction.
	var execution struct {
		Status *hexutil.Uint64 `json:"status"`
	}
	if err := json.Unmarshal(raw, &execution); err != nil {
		return nil, err
	}
	if execution.Status == nil || uint64(*execution.Status) > etypes.ReceiptStatusSuccessful {
		return nil, errors.New("receipt execution status is missing or invalid")
	}
	var receipt etypes.Receipt
	if err := json.Unmarshal(raw, &receipt); err != nil {
		return nil, err
	}
	return &receipt, nil
}

func (e *EthRPC) GetHeader(height int64) (*etypes.Header, error) {
	return e.GetHeaderContext(context.Background(), height)
}

func (e *EthRPC) GetHeaderContext(parent context.Context, height int64) (*etypes.Header, error) {
	return e.headerByNumber(parent, rpc.BlockNumber(height))
}

func (e *EthRPC) headerByNumber(parent context.Context, number rpc.BlockNumber) (*etypes.Header, error) {
	entryTime := time.Now()
	parentDeadline, hasParentDeadline := parent.Deadline()
	callerShortenedWindow := hasParentDeadline && parentDeadline.Before(entryTime.Add(e.timeout))
	ctx, cancel := context.WithTimeout(parent, e.timeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var compactErr error
	pauseCompact := false
	retryAt := e.compactRetryAt.Load()
	if e.compactHeaders && time.Now().UnixNano() >= retryAt {
		// The overall context always has a deadline. Reserve half its remaining
		// budget so a slow optional method cannot starve the standard fallback.
		deadline, _ := ctx.Deadline()
		now := time.Now()
		compactCtx, cancelCompact := context.WithDeadline(ctx, now.Add(deadline.Sub(now)/2))
		var raw json.RawMessage
		var header *etypes.Header
		compactErr = e.client.Client().CallContext(compactCtx, &raw, "eth_getHeaderByNumber", number.String())
		compactContextErr := compactCtx.Err()
		cancelCompact()
		if compactContextErr != nil {
			compactErr = compactContextErr
		}
		if compactErr == nil {
			// Omitted optional fields can decode successfully with a new hash.
			pauseCompact = true
			var reported struct {
				Hash ecommon.Hash
			}
			compactErr = json.Unmarshal(raw, &header)
			if compactErr == nil && header == nil {
				compactErr = ethereum.NotFound
				pauseCompact = false
			}
			if compactErr == nil {
				compactErr = json.Unmarshal(raw, &reported)
			}
			if compactErr == nil && header.Hash() != reported.Hash {
				compactErr = errors.New("compact header does not match its reported hash")
			}
		} else {
			var rpcError rpc.Error
			var httpError rpc.HTTPError
			var syntaxError *json.SyntaxError
			var typeError *json.UnmarshalTypeError
			pauseCompact = errors.As(compactErr, &rpcError) || errors.As(compactErr, &httpError) ||
				errors.As(compactErr, &syntaxError) || errors.As(compactErr, &typeError) ||
				errors.Is(compactErr, rpc.ErrNoResult) ||
				(errors.Is(compactContextErr, context.DeadlineExceeded) && !callerShortenedWindow)
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if compactErr == nil {
			return header, nil
		}
	}
	header, err := e.client.HeaderByNumber(ctx, big.NewInt(number.Int64()))
	if contextErr := ctx.Err(); contextErr != nil {
		return nil, contextErr
	}
	// Only successful fallback can pause the method. Missing blocks and connection
	// failures leave it available to later reads. Overlapping failures of one
	// probe share one pause and one warning.
	if err == nil && pauseCompact && e.compactRetryAt.CompareAndSwap(retryAt, time.Now().Add(compactHeaderCooldown).UnixNano()) {
		e.logger.Warn().Err(compactErr).Dur("cooldown", compactHeaderCooldown).Msg("compact BSC headers paused; standard header read succeeded")
	}
	return header, err
}

// GetHeaderByHash resolves a chain-reported block hash through the node's own
// hash index; see SameBlock.
func (e *EthRPC) GetHeaderByHash(hash ecommon.Hash) (*etypes.Header, error) {
	ctx, cancel := e.getContext()
	defer cancel()
	return e.client.HeaderByHash(ctx, hash)
}

func (e *EthRPC) GetBlockHeight() (int64, error) {
	ctx, cancel := e.getContext()
	defer cancel()
	height, err := e.client.BlockNumber(ctx)
	if err != nil {
		e.logger.Info().Err(err).Msg("failed to get block height")
		return -1, fmt.Errorf("fail to get block height: %w", err)
	}
	return int64(height), nil
}

// GetBlockHeightSafe returns the height of the "safe" block. Only the header
// is fetched: the scanner decodes each block's transactions in FetchTxs.
func (e *EthRPC) GetBlockHeightSafe() (int64, error) {
	ctx, cancel := e.getContext()
	defer cancel()
	header, err := e.client.HeaderByNumber(ctx, big.NewInt(rpc.SafeBlockNumber.Int64()))
	if err != nil {
		e.logger.Info().Err(err).Msg("failed to get block")
		return -1, fmt.Errorf("fail to get block: %w", err)
	}
	return header.Number.Int64(), nil
}

func (e *EthRPC) GetBlock(height int64) (*etypes.Block, error) {
	ctx, cancel := e.getContext()
	defer cancel()
	return e.client.BlockByNumber(ctx, big.NewInt(height))
}

func (e *EthRPC) GetRPCBlock(height int64) (*etypes.Block, error) {
	block, err := e.GetBlock(height)
	if err == ethereum.NotFound {
		return nil, btypes.ErrUnavailableBlock
	}
	if err != nil {
		return nil, fmt.Errorf("fail to fetch block: %w", err)
	}
	return block, nil
}

// GetNonce gets nonce (including pending) of an address.
func (e *EthRPC) GetNonce(addr string) (uint64, error) {
	ctx, cancel := e.getContext()
	defer cancel()
	nonce, err := e.client.PendingNonceAt(ctx, ecommon.HexToAddress(addr))
	if err != nil {
		return 0, fmt.Errorf("fail to get account nonce: %w", err)
	}
	return nonce, nil
}

// GetNonceFinalized gets the nonce excluding pending transactions.
func (e *EthRPC) GetNonceFinalized(addr string) (uint64, error) {
	ctx, cancel := e.getContext()
	defer cancel()
	nonce, err := e.client.NonceAt(ctx, ecommon.HexToAddress(addr), nil)
	if err != nil {
		return 0, fmt.Errorf("fail to get account nonce: %w", err)
	}
	return nonce, nil
}

// GetTransaction preserves pending and RPC errors for canonical classification.
func (e *EthRPC) GetTransaction(hash string) (*etypes.Transaction, bool, error) {
	ctx, cancel := e.getContext()
	defer cancel()
	return e.client.TransactionByHash(ctx, ecommon.HexToHash(hash))
}

func (e *EthRPC) GetTipHeader() (*etypes.Header, error) {
	return e.GetTipHeaderContext(context.Background())
}

func (e *EthRPC) GetTipHeaderContext(parent context.Context) (*etypes.Header, error) {
	return e.headerByNumber(parent, rpc.LatestBlockNumber)
}

// ValidateReceiptLocation binds execution data to the exact block being extracted.
// A receipt for a re-mined copy must not be attached to the older occurrence.
func ValidateReceiptLocation(block *etypes.Block, tx *etypes.Transaction, receipt *etypes.Receipt) error {
	if block == nil || tx == nil || receipt == nil || receipt.BlockNumber == nil || receipt.BlockNumber.Cmp(block.Number()) != 0 || receipt.BlockHash != block.Hash() || receipt.TxHash != tx.Hash() {
		return errors.New("receipt does not belong to extracted canonical block")
	}
	return nil
}

type BlockHashRPC interface {
	GetHeaderByHash(ecommon.Hash) (*etypes.Header, error)
}

// SameBlock reports whether a chain-reported hash, such as a receipt's blockHash
// or a child's parentHash, names the block whose local hash is given. Local
// hashes are go-ethereum's header recomputation. On chains whose headers carry
// fields go-ethereum does not decode, such as Avalanche's extDataHash, that is
// not the chain's own hash, so a mismatch is resolved by the node's hash index
// rather than taken as a different block. An RPC failure is returned as a hold.
func SameBlock(rpc BlockHashRPC, reported ecommon.Hash, local string, height int64) (bool, error) {
	if strings.EqualFold(reported.Hex(), local) {
		return true, nil
	}
	header, err := rpc.GetHeaderByHash(reported)
	if errors.Is(err, ethereum.NotFound) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("resolve reported block hash: %w", err)
	}
	return header != nil && header.Number != nil && header.Number.IsInt64() && header.Number.Int64() == height && strings.EqualFold(header.Hash().Hex(), local), nil
}

// evmRejections are EVM execution failures returned by eth_call. They are fixed
// by the called contract's code and state, so retrying cannot change them. Out
// of gas is not among them: a low node RPC gas cap can cause it for a valid token.
var evmRejections = []string{"execution reverted", "invalid opcode", "invalid jump destination", "stack underflow", "stack overflow", "write protection", "return data out of bounds"}

// IsRPCError preserves a retryable hold when conversion depends on unavailable
// chain data; ordinary policy/parser rejection remains a filtered transaction.
// A contract call rejected by EVM execution is such a rejection: holding it
// would stall the block on an attacker-deployed token for good.
func IsRPCError(err error) bool {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, ethereum.NotFound) {
		return true
	}
	var networkError net.Error
	if errors.As(err, &networkError) {
		return true
	}
	// A non-2xx response such as 429 or 503 is a transport failure, not an
	// execution result. It implements only Error(), so check it before rpc.Error.
	var httpError rpc.HTTPError
	if errors.As(err, &httpError) {
		return true
	}
	var rpcError rpc.Error
	if !errors.As(err, &rpcError) {
		return false
	}
	// Geth reports a revert carrying data with code 3.
	if rpcError.ErrorCode() == 3 {
		return false
	}
	message := strings.ToLower(rpcError.Error())
	for _, rejection := range evmRejections {
		if strings.Contains(message, rejection) {
			return false
		}
	}
	return true
}
