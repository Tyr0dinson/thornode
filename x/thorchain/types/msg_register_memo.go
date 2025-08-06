package types

import (
	errorsmod "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
)

var (
	_ sdk.Msg              = &MsgRegisterMemo{}
	_ sdk.HasValidateBasic = &MsgRegisterMemo{}
)

// NewMsgRegisterMemo creates a new MsgRegisterMemo instance
func NewMsgRegisterMemo(memo string, signer sdk.AccAddress, ttl int64, chainID string) *MsgRegisterMemo {
	return &MsgRegisterMemo{
		Memo:    memo,
		Signer:  signer,
		Ttl:     ttl,
		ChainId: chainID,
	}
}

// Route returns the route for MsgRegisterMemo
func (m *MsgRegisterMemo) Route() string {
	return RouterKey
}

// Type returns the message type
func (m *MsgRegisterMemo) Type() string {
	return "register_memo"
}

// ValidateBasic performs basic validation (required by sdk.Msg interface)
func (m *MsgRegisterMemo) ValidateBasic() error {
	// Basic non-empty checks
	if len(m.Memo) == 0 {
		return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "memo cannot be empty")
	}

	if len(m.Memo) > 1024 {
		return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "memo too long (max 1024 chars)")
	}

	if m.Signer.Empty() {
		return errorsmod.Wrap(sdkerrors.ErrInvalidAddress, "signer cannot be empty")
	}

	if m.Ttl < 0 || m.Ttl > 864000 { // Max 10 days in seconds
		return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "invalid TTL")
	}

	if len(m.ChainId) == 0 {
		return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "chain_id cannot be empty")
	}

	return nil
}

// GetSignBytes returns the bytes all signers sign over
func (m *MsgRegisterMemo) GetSignBytes() []byte {
	bz := ModuleCdc.MustMarshalJSON(m)
	return sdk.MustSortJSON(bz)
}

// GetSigners returns the expected signers for the message
func (m *MsgRegisterMemo) GetSigners() []sdk.AccAddress {
	return []sdk.AccAddress{m.Signer}
}

// NewRegisteredMemo creates a new RegisteredMemo instance
func NewRegisteredMemo(reference, memo, chainID string, registeredAt, expiresAt int64) RegisteredMemo {
	return RegisteredMemo{
		Reference:    reference,
		Memo:         memo,
		RegisteredAt: registeredAt,
		ExpiresAt:    expiresAt,
		ChainId:      chainID,
		Status:       "active",
	}
}

// IsExpired checks if the memo has expired at the given block height
func (rm RegisteredMemo) IsExpired(blockHeight int64) bool {
	return blockHeight > rm.ExpiresAt
}

// IsActive checks if the memo is currently active
func (rm RegisteredMemo) IsActive(blockHeight int64) bool {
	return rm.Status == "active" && !rm.IsExpired(blockHeight)
}
