package evm

import (
	"context"
	"errors"
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
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/endpoints"
)

// EthRPC is a struct that interacts with an ETH RPC compatible blockchain
type EthRPC struct {
	client  *ethclient.Client
	timeout time.Duration
	logger  zerolog.Logger
}

func NewEthRPC(client *ethclient.Client, timeout time.Duration, chain string) (*EthRPC, error) {
	return &EthRPC{
		client:  client,
		timeout: timeout,
		logger:  log.Logger.With().Str("module", "eth_rpc").Str("chain", chain).Logger(),
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
	return e.client.TransactionReceipt(ctx, ecommon.HexToHash(hash))
}

func (e *EthRPC) GetHeader(height int64) (*etypes.Header, error) {
	ctx, cancel := e.getContext()
	defer cancel()
	return e.client.HeaderByNumber(ctx, big.NewInt(height))
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

// TxPresence is what reorg re-confirmation learned about an observed transaction.
type TxPresence int

const (
	// TxOnChain: mined and successful, pending, or its receipt cannot be read (an
	// errata needs certainty, so anything short of proof keeps the observation)
	TxOnChain TxPresence = iota
	// TxNotFound: the answering node says it does not know the transaction
	TxNotFound
	// TxFailed: mined, but the receipt says it failed
	TxFailed
)

// ReceiptBlock names the block a receipt was read from (zero for no receipt).
type ReceiptBlock struct {
	Number *big.Int
	Hash   ecommon.Hash
}

// CheckTransaction reports what the node knows about a transaction, for reorg
// re-confirmation. Only the node's "not found" answer, or a failed receipt, says it
// is not on chain; a lookup that fails is returned as an error, never as absence, so
// a transient failure can never produce an errata. Both reads go to the endpoint
// named by pin (see ReorgWitness; "" leaves them unpinned).
func (e *EthRPC) CheckTransaction(pin, hash string) (TxPresence, error) {
	presence, _, err := e.CheckTransactionWithBlock(pin, hash)
	return presence, err
}

// CheckTransactionWithBlock is CheckTransaction that also names the block of a
// failed receipt (TxFailed), so a reorg plan can check that block is on the scanned
// fork (FailedReceiptOnScannedFork).
func (e *EthRPC) CheckTransactionWithBlock(pin, hash string) (TxPresence, ReceiptBlock, error) {
	ctx, cancel := e.getContext()
	defer cancel()
	tx, pending, err := e.client.TransactionByHash(endpoints.PinContext(ctx, pin), ecommon.HexToHash(hash))
	if errors.Is(err, ethereum.NotFound) || (err == nil && tx == nil) {
		e.logger.Info().Str("txid", hash).Msg("tx not found")
		return TxNotFound, ReceiptBlock{}, nil
	}
	if err != nil {
		return TxOnChain, ReceiptBlock{}, fmt.Errorf("fail to look up tx %s: %w", hash, err)
	}

	// pending transactions may fail, but we should only errata when there is certainty
	if pending {
		e.logger.Warn().Str("txid", hash).Msg("observed transaction is pending")
		return TxOnChain, ReceiptBlock{}, nil // unknown, prefer false positive
	}

	// ensure the tx was successful
	rctx, rcancel := e.getContext()
	defer rcancel()
	receipt, err := e.client.TransactionReceipt(endpoints.PinContext(rctx, pin), ecommon.HexToHash(hash))
	if err != nil {
		e.logger.Warn().Str("txid", hash).Err(err).Msg("tx receipt not found")
		return TxOnChain, ReceiptBlock{}, nil // unknown, prefer false positive
	}
	if receipt.Status != etypes.ReceiptStatusSuccessful {
		return TxFailed, ReceiptBlock{Number: receipt.BlockNumber, Hash: receipt.BlockHash}, nil
	}
	return TxOnChain, ReceiptBlock{}, nil
}
