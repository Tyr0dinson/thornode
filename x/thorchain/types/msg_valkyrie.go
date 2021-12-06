package types

import (
	"gitlab.com/thorchain/thornode/common"
	cosmos "gitlab.com/thorchain/thornode/common/cosmos"
)

var _ cosmos.Msg = &MsgValkyrie{}

// NewMsgValkyrie is a constructor function for MsgValkyrie
func NewMsgValkyrie(tx common.Tx, pk common.PubKey, blockHeight int64, addFunds bool, coins common.Coins, signer cosmos.AccAddress) *MsgValkyrie {
	return &MsgValkyrie{
		Tx:          tx,
		PubKey:      pk,
		AddFunds:    addFunds,
		Coins:       coins,
		BlockHeight: blockHeight,
		Signer:      signer,
	}
}

// Route should return the route key of the module
func (m *MsgValkyrie) Route() string { return RouterKey }

// Type should return the action
func (m MsgValkyrie) Type() string { return "set_valkyrie" }

// ValidateBasic runs stateless checks on the message
func (m *MsgValkyrie) ValidateBasic() error {
	if m.Signer.Empty() {
		return cosmos.ErrInvalidAddress(m.Signer.String())
	}
	if m.PubKey.IsEmpty() {
		return cosmos.ErrUnknownRequest("pubkey cannot be empty")
	}
	if m.BlockHeight <= 0 {
		return cosmos.ErrUnknownRequest("invalid block height")
	}
	if err := m.Tx.Valid(); err != nil {
		return cosmos.ErrUnknownRequest(err.Error())
	}
	if len(m.Coins) == 0 {
		return cosmos.ErrUnknownRequest("no coins")
	}
	if err := m.Coins.Valid(); err != nil {
		return cosmos.ErrInvalidCoins(err.Error())
	}
	return nil
}

// GetSignBytes encodes the message for signing
func (m *MsgValkyrie) GetSignBytes() []byte {
	return cosmos.MustSortJSON(ModuleCdc.MustMarshalJSON(m))
}

// GetSigners defines whose signature is required
func (m *MsgValkyrie) GetSigners() []cosmos.AccAddress {
	return []cosmos.AccAddress{m.Signer}
}
