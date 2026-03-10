//go:build !mocknet
// +build !mocknet

package observer

import (
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
)

// Non-mocknet builds never suppress observations via simulation memo.
func shouldIgnoreSimulationTx(_ *types.TxInItem, _ common.Address) bool {
	return false
}
