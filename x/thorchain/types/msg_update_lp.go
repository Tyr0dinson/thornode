package types

import (
	"gitlab.com/thorchain/thornode/common"
	"gitlab.com/thorchain/thornode/common/cosmos"
)

func NewMsgUpdateLpRecord(tx common.Tx, poolAsset common.Asset, newAddress common.Address, signer cosmos.AccAddress) *MsgUpdateLpRecord {
	return &MsgUpdateLpRecord{
		Tx:         tx,
		PoolAsset:  poolAsset,
		NewAddress: newAddress,
		Signer:     signer,
	}
}

// Route should return the route key of the module
func (m *MsgUpdateLpRecord) Route() string { return RouterKey }

// Type should return the action
func (m MsgUpdateLpRecord) Type() string { return "update_lp_record" }

// ValidateBasic runs stateless checks on the message
func (m *MsgUpdateLpRecord) ValidateBasic() error {
	if m.Signer.Empty() {
		return cosmos.ErrInvalidAddress(m.Signer.String())
	}
	if m.Tx.ID.IsEmpty() {
		return cosmos.ErrInvalidAddress("tx id cannot be empty")
	}
	if m.PoolAsset.IsEmpty() {
		return cosmos.ErrUnknownRequest("pool asset cannot be empty")
	}
	if m.PoolAsset.IsRune() {
		return cosmos.ErrUnknownRequest("pool asset cannot be rune")
	}
	if m.NewAddress.IsEmpty() {
		return cosmos.ErrUnknownRequest("new address cannot be empty")
	}
	// Unless setting the $RUNE address, chain of new address and pool must match
	if !m.NewAddress.IsChain(common.THORChain) {
		if !m.NewAddress.GetChain().Equals(m.PoolAsset.GetChain()) {
			return cosmos.ErrUnknownRequest("new address must match pool asset")
		}
	}
	return nil
}

// GetSignBytes encodes the message for signing
func (m *MsgUpdateLpRecord) GetSignBytes() []byte {
	return cosmos.MustSortJSON(ModuleCdc.MustMarshalJSON(m))
}

// GetSigners defines whose signature is required
func (m *MsgUpdateLpRecord) GetSigners() []cosmos.AccAddress {
	return []cosmos.AccAddress{m.Signer}
}
