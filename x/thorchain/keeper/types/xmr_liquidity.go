package types

import (
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

// XMRLiquidityReceipt is immutable attribution for one asset-side contribution
// to one LP (an ADD may also credit an affiliate). Pending receipts acquire their
// proportional booked effects when the paired RUNE leg arrives. No memo or
// THORName is consulted when reversing a receipt.
type XMRLiquidityReceipt struct {
	TxID              common.TxID    `json:"tx_id"`
	Owner             common.Address `json:"owner"`
	RuneAddress       common.Address `json:"rune_address"`
	AssetAddress      common.Address `json:"asset_address"`
	AssetAmount       cosmos.Uint    `json:"asset_amount"`
	RuneAmount        cosmos.Uint    `json:"rune_amount"`
	Units             cosmos.Uint    `json:"units"`
	RuneDepositValue  cosmos.Uint    `json:"rune_deposit_value"`
	AssetDepositValue cosmos.Uint    `json:"asset_deposit_value"`
	RuneTxID          common.TxID    `json:"rune_tx_id"`
	Pending           bool           `json:"pending"`
	Reverted          bool           `json:"reverted"`
	Withdrawn         bool           `json:"withdrawn"`
}
