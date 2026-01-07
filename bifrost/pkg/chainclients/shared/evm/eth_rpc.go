package evm

import (
	"context"
	"fmt"
	"math/big"
	"time"

	"github.com/ethereum/go-ethereum"
	ecommon "github.com/ethereum/go-ethereum/common"
	etypes "github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethereum/go-ethereum/rpc"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	btypes "gitlab.com/thorchain/thornode/v3/bifrost/blockscanner/types"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/fallback"
)

// EthRPC is a struct that interacts with an ETH RPC compatible blockchain
type EthRPC struct {
	client     *ethclient.Client
	fallback   *ethclient.Client
	fbMgr      *fallback.Manager
	timeout    time.Duration
	logger     zerolog.Logger
	chain      string
	hasFallbck bool
}

func NewEthRPC(client *ethclient.Client, timeout time.Duration, chain string) (*EthRPC, error) {
	return &EthRPC{
		client:  client,
		timeout: timeout,
		chain:   chain,
		logger:  log.Logger.With().Str("module", "eth_rpc").Str("chain", chain).Logger(),
	}, nil
}

// SetFallback configures a fallback RPC client with health check interval
func (e *EthRPC) SetFallback(fallbackClient *ethclient.Client, healthCheckInterval time.Duration) {
	if fallbackClient == nil {
		return
	}
	e.fallback = fallbackClient
	e.hasFallbck = true
	e.fbMgr = fallback.NewManager(
		e.chain,
		e.primaryHealthCheck,
		healthCheckInterval,
		e.logger,
	)
	e.fbMgr.Start()
	e.logger.Info().Msg("fallback RPC endpoint configured")
}

// Stop stops the fallback manager if running
func (e *EthRPC) Stop() {
	if e.fbMgr != nil {
		e.fbMgr.Stop()
	}
}

// primaryHealthCheck checks if the primary endpoint is healthy
func (e *EthRPC) primaryHealthCheck(ctx context.Context) error {
	_, err := e.client.BlockNumber(ctx)
	return err
}

// getActiveClient returns the currently active client based on fallback state
func (e *EthRPC) getActiveClient() *ethclient.Client {
	if e.hasFallbck && e.fbMgr != nil && e.fbMgr.IsFallback() {
		return e.fallback
	}
	return e.client
}

// handleFallback switches to fallback if the error warrants it and retries with fallback
func (e *EthRPC) tryFallback(err error) bool {
	if !e.hasFallbck || e.fbMgr == nil {
		return false
	}
	if fallback.ShouldSwitchToFallback(err) {
		e.fbMgr.SetFallback()
		return true
	}
	return false
}

// IsFallbackActive returns true if currently using fallback endpoint
func (e *EthRPC) IsFallbackActive() bool {
	return e.hasFallbck && e.fbMgr != nil && e.fbMgr.IsFallback()
}

func (e *EthRPC) getContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), e.timeout)
}

func (e *EthRPC) EstimateGas(from string, tx *etypes.Transaction) (uint64, error) {
	ctx, cancel := e.getContext()
	defer cancel()
	msg := ethereum.CallMsg{
		From:     ecommon.HexToAddress(from),
		To:       tx.To(),
		GasPrice: tx.GasPrice(),
		Value:    tx.Value(),
		Data:     tx.Data(),
	}
	gas, err := e.getActiveClient().EstimateGas(ctx, msg)
	if err != nil && e.tryFallback(err) {
		ctx2, cancel2 := e.getContext()
		defer cancel2()
		return e.fallback.EstimateGas(ctx2, msg)
	}
	return gas, err
}

func (e *EthRPC) GetReceipt(hash string) (*etypes.Receipt, error) {
	ctx, cancel := e.getContext()
	defer cancel()
	h := ecommon.HexToHash(hash)
	receipt, err := e.getActiveClient().TransactionReceipt(ctx, h)
	if err != nil && e.tryFallback(err) {
		ctx2, cancel2 := e.getContext()
		defer cancel2()
		return e.fallback.TransactionReceipt(ctx2, h)
	}
	return receipt, err
}

func (e *EthRPC) GetHeader(height int64) (*etypes.Header, error) {
	ctx, cancel := e.getContext()
	defer cancel()
	h := big.NewInt(height)
	header, err := e.getActiveClient().HeaderByNumber(ctx, h)
	if err != nil && e.tryFallback(err) {
		ctx2, cancel2 := e.getContext()
		defer cancel2()
		return e.fallback.HeaderByNumber(ctx2, h)
	}
	return header, err
}

func (e *EthRPC) GetBlockHeight() (int64, error) {
	ctx, cancel := e.getContext()
	defer cancel()
	height, err := e.getActiveClient().BlockNumber(ctx)
	if err != nil {
		if e.tryFallback(err) {
			ctx2, cancel2 := e.getContext()
			defer cancel2()
			height, err = e.fallback.BlockNumber(ctx2)
			if err == nil {
				return int64(height), nil
			}
		}
		e.logger.Info().Err(err).Msg("failed to get block height")
		return -1, fmt.Errorf("fail to get block height: %w", err)
	}
	return int64(height), nil
}

func (e *EthRPC) GetBlockHeightSafe() (int64, error) {
	ctx, cancel := e.getContext()
	defer cancel()
	safeNum := big.NewInt(rpc.SafeBlockNumber.Int64())
	block, err := e.getActiveClient().BlockByNumber(ctx, safeNum)
	if err != nil {
		if e.tryFallback(err) {
			ctx2, cancel2 := e.getContext()
			defer cancel2()
			block, err = e.fallback.BlockByNumber(ctx2, safeNum)
			if err == nil {
				return block.Number().Int64(), nil
			}
		}
		e.logger.Info().Err(err).Msg("failed to get block")
		return -1, fmt.Errorf("fail to get block: %w", err)
	}
	return block.Number().Int64(), nil
}

func (e *EthRPC) GetBlock(height int64) (*etypes.Block, error) {
	ctx, cancel := e.getContext()
	defer cancel()
	h := big.NewInt(height)
	block, err := e.getActiveClient().BlockByNumber(ctx, h)
	if err != nil && e.tryFallback(err) {
		ctx2, cancel2 := e.getContext()
		defer cancel2()
		return e.fallback.BlockByNumber(ctx2, h)
	}
	return block, err
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
	a := ecommon.HexToAddress(addr)
	nonce, err := e.getActiveClient().PendingNonceAt(ctx, a)
	if err != nil {
		if e.tryFallback(err) {
			ctx2, cancel2 := e.getContext()
			defer cancel2()
			nonce, err = e.fallback.PendingNonceAt(ctx2, a)
			if err == nil {
				return nonce, nil
			}
		}
		return 0, fmt.Errorf("fail to get account nonce: %w", err)
	}
	return nonce, nil
}

// GetNonceFinalized gets the nonce excluding pending transactions.
func (e *EthRPC) GetNonceFinalized(addr string) (uint64, error) {
	ctx, cancel := e.getContext()
	defer cancel()
	a := ecommon.HexToAddress(addr)
	nonce, err := e.getActiveClient().NonceAt(ctx, a, nil)
	if err != nil {
		if e.tryFallback(err) {
			ctx2, cancel2 := e.getContext()
			defer cancel2()
			nonce, err = e.fallback.NonceAt(ctx2, a, nil)
			if err == nil {
				return nonce, nil
			}
		}
		return 0, fmt.Errorf("fail to get account nonce: %w", err)
	}
	return nonce, nil
}

// CheckTransaction returns true if a transaction is found and successful on chain. This
// is used to determine when a transaction has been dropped from the chain or failed on
// subsequent execution after reorgs. This can return false positives, but should not
// return false negatives - as we want to errata an observation only when we are certain
// it has later been dropped or failed.
func (e *EthRPC) CheckTransaction(hash string) bool {
	ctx, cancel := e.getContext()
	defer cancel()
	h := ecommon.HexToHash(hash)
	tx, pending, err := e.getActiveClient().TransactionByHash(ctx, h)
	if err != nil || tx == nil {
		if e.tryFallback(err) {
			ctx2, cancel2 := e.getContext()
			defer cancel2()
			tx, pending, err = e.fallback.TransactionByHash(ctx2, h)
		}
		if err != nil || tx == nil {
			e.logger.Info().Str("txid", hash).Err(err).Msg("tx not found")
			return false
		}
	}

	// pending transactions may fail, but we should only errata when there is certainty
	if pending {
		e.logger.Warn().Str("txid", hash).Msg("observed transaction is pending")
		return true // unknown, prefer false positive
	}

	// ensure the tx was successful
	receipt, err := e.GetReceipt(hash)
	if err != nil {
		e.logger.Warn().Str("txid", hash).Err(err).Msg("tx receipt not found")
		return true // unknown, prefer false positive
	}
	return receipt.Status == etypes.ReceiptStatusSuccessful
}
