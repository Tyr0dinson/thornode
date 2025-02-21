package cardano

import (
	"math/big"

	shared "gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/types"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/config"
)

var _ shared.ChainClient = &Client{}

type Client struct{}

func (c *Client) BroadcastTx(_ types.TxOutItem, _ []byte) (string, error) {
	panic("unimplemented")
}

func (c *Client) ConfirmationCountReady(txIn types.TxIn) bool {
	panic("unimplemented")
}

func (c *Client) GetAccount(poolPubKey common.PubKey, height *big.Int) (common.Account, error) {
	panic("unimplemented")
}

func (c *Client) GetAccountByAddress(address string, height *big.Int) (common.Account, error) {
	panic("unimplemented")
}

func (c *Client) GetAddress(poolPubKey common.PubKey) string {
	panic("unimplemented")
}

func (c *Client) GetBlockScannerHeight() (int64, error) {
	panic("unimplemented")
}

func (c *Client) GetChain() common.Chain {
	panic("unimplemented")
}

func (c *Client) GetConfig() config.BifrostChainConfiguration {
	panic("unimplemented")
}

func (c *Client) GetConfirmationCount(txIn types.TxIn) int64 {
	panic("unimplemented")
}

func (c *Client) GetHeight() (int64, error) {
	panic("unimplemented")
}

func (c *Client) GetLatestTxForVault(vault string) (string, string, error) {
	panic("unimplemented")
}

func (c *Client) IsBlockScannerHealthy() bool {
	panic("unimplemented")
}

func (c *Client) OnObservedTxIn(txIn types.TxInItem, blockHeight int64) {
	panic("unimplemented")
}

func (c *Client) SignTx(tx types.TxOutItem, height int64) ([]byte, []byte, *types.TxInItem, error) {
	panic("unimplemented")
}

func (c *Client) Start(globalTxsQueue chan types.TxIn, globalErrataQueue chan types.ErrataBlock, globalSolvencyQueue chan types.Solvency) {
	panic("unimplemented")
}

func (c *Client) Stop() {
	panic("unimplemented")
}
