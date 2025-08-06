package types

import (
	"errors"
	"fmt"

	"github.com/cosmos/cosmos-sdk/codec/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
	"github.com/cosmos/cosmos-sdk/x/auth/signing"
	"github.com/cosmos/cosmos-sdk/x/auth/legacy/legacytx"
)

var (
	_ sdk.Msg              = &MsgRegisterMemo{}
	_ sdk.HasValidateBasic = &MsgRegisterMemo{}
	_ legacytx.LegacyMsg    = &MsgRegisterMemo{}
)

// NewMsgRegisterMemo creates a new MsgRegisterMemo instance
func NewMsgRegisterMemo(memo string, signer sdk.AccAddress, ttl int64, chainID string) *MsgRegisterMemo {
	return &MsgRegisterMemo{
		Memo:    memo,
		Signer:  signer,
		TTL:     ttl,
		ChainID: chainID,
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

// ValidateBasic performs basic validation
func (m *MsgRegisterMemo) ValidateBasic() error {
	if len(m.Memo) == 0 {
		return sdkerrors.Wrap(sdkerrors.ErrInvalidRequest, "memo cannot be empty")
	}

	if len(m.Memo) > 1024 {
		return sdkerrors.Wrap(sdkerrors.ErrInvalidRequest, "memo too long (max 1024 chars)")
	}

	if m.Signer.Empty() {
		return sdkerrors.Wrap(sdkerrors.ErrInvalidAddress, "signer cannot be empty")
	}

	if m.TTL < 0 || m.TTL > 864000 { // Max 10 days in seconds
		return sdkerrors.Wrap(sdkerrors.ErrInvalidRequest, "invalid TTL")
	}

	if len(m.ChainID) == 0 {
		return sdkerrors.Wrap(sdkerrors.ErrInvalidRequest, "chain_id cannot be empty")
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

// RegisteredMemo represents a registered memo with metadata
type RegisteredMemo struct {
	Reference    string `json:"reference"`
	Memo         string `json:"memo"`
	RegisteredAt int64  `json:"registered_at"`
	ExpiresAt    int64  `json:"expires_at"`
	ChainID      string `json:"chain_id"`
	Status       string `json:"status"`
}

// NewRegisteredMemo creates a new RegisteredMemo instance
func NewRegisteredMemo(reference, memo, chainID string, registeredAt, expiresAt int64) RegisteredMemo {
	return RegisteredMemo{
		Reference:    reference,
		Memo:         memo,
		RegisteredAt: registeredAt,
		ExpiresAt:    expiresAt,
		ChainID:      chainID,
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