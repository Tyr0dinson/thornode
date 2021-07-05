package types

import (
	"gitlab.com/thorchain/thornode/common"
	"gitlab.com/thorchain/thornode/common/cosmos"
)

// NewMsgSolvency create a new MsgSolvency
func NewMsgSolvency(id string, chain common.Chain, address common.Address, coins common.Coins, height int64, signer cosmos.AccAddress) *MsgSolvency {
	return &MsgSolvency{
		Id:      id,
		Chain:   chain,
		Address: address,
		Coins:   coins,
		Height:  height,
		Signer:  signer,
	}
}

// Route Implements Msg.
func (m *MsgSolvency) Route() string { return RouterKey }

// Type Implements Msg.
func (m MsgSolvency) Type() string { return "solvency" }

// ValidateBasic Implements Msg.
func (m *MsgSolvency) ValidateBasic() error {
	if len(m.Id) == 0 {
		return cosmos.ErrUnknownRequest("invalid id")
	}
	if m.Coins.IsEmpty() {
		return cosmos.ErrUnknownRequest("coins must be valid")
	}
	if m.Chain.IsEmpty() {
		return cosmos.ErrUnknownRequest("chain can't be empty")
	}
	if m.Address.IsEmpty() {
		return cosmos.ErrUnknownRequest("address can't be empty")
	}
	if m.Address.IsChain(m.Chain) {
		return cosmos.ErrUnknownRequest("invalid address")
	}
	if m.Height <= 0 {
		return cosmos.ErrUnknownRequest("block height is invalid")
	}

	return nil
}

// GetSignBytes Implements Msg.
func (m *MsgSolvency) GetSignBytes() []byte {
	return cosmos.MustSortJSON(ModuleCdc.MustMarshalJSON(m))
}

// GetSigners Implements Msg.
func (m *MsgSolvency) GetSigners() []cosmos.AccAddress {
	return []cosmos.AccAddress{m.Signer}
}
