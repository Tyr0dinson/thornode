package thorchain

import (
	"gitlab.com/thorchain/thornode/common"
	cosmos "gitlab.com/thorchain/thornode/common/cosmos"
)

type MintLpMemo struct {
	MemoBase
	Amount    cosmos.Uint
	Recipient cosmos.AccAddress
}

func (m MintLpMemo) GetAmount() cosmos.Uint { return m.Amount }

func NewMintLpMemo(asset common.Asset, amt cosmos.Uint, recipient cosmos.AccAddress) MintLpMemo {
	return MintLpMemo{
		MemoBase:  MemoBase{TxType: TxWithdraw, Asset: asset},
		Amount:    amt,
		Recipient: recipient,
	}
}

func (p *parser) ParseMintLpMemo() (MintLpMemo, error) {
	asset := p.getAsset(1, true, common.EmptyAsset)
	amount := p.getUint(2, true, cosmos.ZeroUint().Uint64())
	recipient := p.getAccAddress(3, true, cosmos.AccAddress{})
	return NewMintLpMemo(asset, amount, recipient), p.Error()
}

type RedeemLpMemo struct {
	MemoBase
	Amount cosmos.Uint
}

func (m RedeemLpMemo) GetAmount() cosmos.Uint { return m.Amount }

func NewRedeemLpMemo(asset common.Asset, amt cosmos.Uint) RedeemLpMemo {
	return RedeemLpMemo{
		MemoBase: MemoBase{TxType: TxWithdraw, Asset: asset},
		Amount:   amt,
	}
}

func (p *parser) ParseRedeemLpMemo() (RedeemLpMemo, error) {
	asset := p.getAsset(1, true, common.EmptyAsset)
	amount := p.getUint(2, true, cosmos.ZeroUint().Uint64())
	return NewRedeemLpMemo(asset, amount), p.Error()
}
