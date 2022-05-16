package evm

import (
	"context"
	"fmt"
	"time"

	"github.com/ethereum/go-ethereum"
	ecommon "github.com/ethereum/go-ethereum/common"
	etypes "github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethclient"
)

// EthRpc is a struct that interacts with an ETH RPC compatible blockchain
type EthRpc struct {
	host    string
	client  *ethclient.Client
	timeout time.Duration
}

func NewEthRpc(host string, timeout time.Duration) (*EthRpc, error) {
	ethClient, err := ethclient.Dial(host)
	if err != nil {
		return nil, fmt.Errorf("fail to dial ETH rpc host(%s): %w", host, err)
	}

	return &EthRpc{
		host:    host,
		client:  ethClient,
		timeout: timeout,
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
