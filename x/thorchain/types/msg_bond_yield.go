package types

import (
	sdk "github.com/cosmos/cosmos-sdk/types"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

var (
	_ sdk.Msg              = &MsgBondYield{}
	_ sdk.HasValidateBasic = &MsgBondYield{}
	_ sdk.LegacyMsg        = &MsgBondYield{}
)

// NewMsgBondYield constructs the message used to claim bond yield or update auto-claim settings.
func NewMsgBondYield(
	txin common.Tx,
	nodeAddress cosmos.AccAddress,
	amount cosmos.Uint,
	autoClaim bool,
	autoClaimBps uint64,
	payoutAsset common.Asset,
	payoutAddress common.Address,
	signer cosmos.AccAddress,
) *MsgBondYield {
	return &MsgBondYield{
		TxIn:          txin,
		NodeAddress:   nodeAddress,
		Amount:        amount,
		AutoClaim:     autoClaim,
		AutoClaimBps:  autoClaimBps,
		PayoutAsset:   payoutAsset,
		PayoutAddress: payoutAddress,
		Signer:        signer,
	}
}

// ValidateBasic performs stateless validation for bond yield messages.
func (m *MsgBondYield) ValidateBasic() error {
	if m.NodeAddress.Empty() {
		return cosmos.ErrInvalidAddress("node address cannot be empty")
	}
	if m.AutoClaimBps > 10000 {
		return cosmos.ErrUnknownRequest("auto claim basis points cannot exceed 10000")
	}
	if m.TxIn.ID.IsEmpty() {
		return cosmos.ErrUnknownRequest("tx id cannot be empty")
	}
	if m.TxIn.FromAddress.IsEmpty() {
		return cosmos.ErrInvalidAddress("tx from address cannot be empty")
	}
	if !m.TxIn.Coins.IsEmpty() {
		return cosmos.ErrUnknownRequest("bond yield message cannot have a non-zero coin amount")
	}
	if m.Signer.Empty() {
		return cosmos.ErrInvalidAddress("empty signer address")
	}
	if !m.AutoClaim && !m.PayoutAsset.IsEmpty() {
		return cosmos.ErrUnknownRequest("payout asset can only be set with auto claim")
	}
	if !m.AutoClaim && !m.PayoutAddress.IsEmpty() {
		return cosmos.ErrUnknownRequest("payout address can only be set with auto claim")
	}
	return nil
}

// GetSigners returns the signer that authorized the bond yield message.
func (m *MsgBondYield) GetSigners() []cosmos.AccAddress {
	return []cosmos.AccAddress{m.Signer}
}
