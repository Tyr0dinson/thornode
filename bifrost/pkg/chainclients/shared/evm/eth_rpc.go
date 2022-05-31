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
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	btypes "gitlab.com/thorchain/thornode/bifrost/blockscanner/types"
)

// EthRpc is a struct that interacts with an ETH RPC compatible blockchain
type EthRpc struct {
	host    string
	client  *ethclient.Client
	timeout time.Duration
	logger  zerolog.Logger
}

func NewEthRpc(host string, timeout time.Duration, chain string) (*EthRpc, error) {
	ethClient, err := ethclient.Dial(host)
	if err != nil {
		return nil, fmt.Errorf("fail to dial ETH rpc host(%s): %w", host, err)
	}

	return &EthRpc{
		host:    host,
		client:  ethClient,
		timeout: timeout,
		logger:  log.Logger.With().Str("module", "eth_rpc").Str("chain", chain).Logger(),
	}, nil
}

func (e *EthRpc) getContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), e.timeout)
}

func (e *EthRpc) EstimateGas(from string, tx *etypes.Transaction) (uint64, error) {
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

func (e *EthRpc) GetReceipt(hash string) (*etypes.Receipt, error) {
	ctx, cancel := e.getContext()
	defer cancel()
	return e.client.TransactionReceipt(ctx, ecommon.HexToHash(hash))
}

func (e *EthRpc) GetHeader(height int64) (*etypes.Header, error) {
	ctx, cancel := e.getContext()
	defer cancel()
	return e.client.HeaderByNumber(ctx, big.NewInt(height))
}

func (e *EthRpc) GetBlock(height int64) (*etypes.Block, error) {
	ctx, cancel := e.getContext()
	defer cancel()
	return e.client.BlockByNumber(ctx, big.NewInt(height))
}

func (e *EthRpc) GetRPCBlock(height int64) (*etypes.Block, error) {
	block, err := e.GetBlock(height)
	if err == ethereum.NotFound {
		return nil, btypes.ErrUnavailableBlock
	}
	if err != nil {
		return nil, fmt.Errorf("fail to fetch block: %w", err)
	}
	return block, nil
}

// GetNonce gets nonce
func (e *EthRpc) GetNonce(addr string) (uint64, error) {
	ctx, cancel := e.getContext()
	defer cancel()
	nonce, err := e.client.PendingNonceAt(ctx, ecommon.HexToAddress(addr))
	if err != nil {
		return 0, fmt.Errorf("fail to get account nonce: %w", err)
	}
	return nonce, nil
}

// CheckTransaction returns true if a tx can be found by TransactionByHash rpc method
func (e *EthRpc) CheckTransaction(hash string) bool {
	ctx, cancel := e.getContext()
	defer cancel()
	tx, pending, err := e.client.TransactionByHash(ctx, ecommon.HexToHash(hash))
	if err != nil || tx == nil {
		return false
	}
	if pending {
		e.logger.Info().Msgf("tx: %s is in pending status", hash)
	}
	return true
}
