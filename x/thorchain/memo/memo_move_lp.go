package thorchain

import (
	"fmt"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

type MoveLpMemo struct {
	MemoBase
	NewRuneAddress  common.Address
	NewAssetAddress common.Address
	Amount          cosmos.Uint
}

func (m MoveLpMemo) String() string {
	return fmt.Sprintf(
		"MOVELP:%s:%s:%s:%s",
		m.GetAsset(),
		m.NewRuneAddress,
		m.NewAssetAddress,
		m.GetAmount(),
	)
}

func NewMoveLpMemo(
	asset common.Asset,
	newRuneAddress, newAssetAddress common.Address,
	amount cosmos.Uint,
) MoveLpMemo {
	return MoveLpMemo{
		MemoBase:        MemoBase{TxType: TxMoveLp, Asset: asset},
		NewRuneAddress:  newRuneAddress,
		NewAssetAddress: newAssetAddress,
		Amount:          amount,
	}
}

func (p *parser) ParseMoveLpMemo() (MoveLpMemo, error) {
	asset := p.getAsset(1, true, common.EmptyAsset)
	address1 := p.getAddress(2, true, common.NoAddress)
	address2 := p.getAddress(3, true, common.NoAddress)
	amount := p.getUint(4, false, 10_000)

	var newRuneAddress common.Address
	var newAssetAddress common.Address

	if address1.Equals(address2) {
		newRuneAddress = address1
		newAssetAddress = address1
	} else {
		for _, address := range []common.Address{address1, address2} {
			if address.IsEmpty() {
				continue
			}

			if address.IsChain(common.THORChain) {
				newRuneAddress = address
			} else {
				newAssetAddress = address
			}
		}
	}

	return NewMoveLpMemo(asset, newRuneAddress, newAssetAddress, amount), p.Error()
}
