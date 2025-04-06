package thorchain

import (
	"gitlab.com/thorchain/thornode/v3/common"
	cosmos "gitlab.com/thorchain/thornode/v3/common/cosmos"
)

type LimitOrderCancelMemo struct {
	MemoBase
	Asset     common.Asset
	SlipLimit cosmos.Uint
}

func (m LimitOrderCancelMemo) GetAsset() common.Asset    { return m.Asset }
func (m LimitOrderCancelMemo) GetSlipLimit() cosmos.Uint { return m.SlipLimit }

func NewLimitOrderCancelMemo(asset common.Asset, slip cosmos.Uint) LimitOrderCancelMemo {
	return LimitOrderCancelMemo{
		MemoBase:  MemoBase{TxType: TxLimitOrderCancel},
		Asset:     asset,
		SlipLimit: slip,
	}
}

func (p *parser) ParseLimitOrderCancel() (LimitOrderCancelMemo, error) {
	target := p.getAsset(1, true, common.EmptyAsset)
	slip := p.getUint(2, false, 0)
	return NewLimitOrderCancelMemo(target, slip), p.Error()
}
