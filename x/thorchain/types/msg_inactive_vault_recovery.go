package types

import (
	"errors"
	"strings"

	sdk "github.com/cosmos/cosmos-sdk/types"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

var (
	_ sdk.Msg              = &MsgInactiveVaultRecovery{}
	_ sdk.HasValidateBasic = &MsgInactiveVaultRecovery{}
	_ sdk.LegacyMsg        = &MsgInactiveVaultRecovery{}
)

// NewMsgInactiveVaultRecovery is a constructor function for MsgInactiveVaultRecovery
func NewMsgInactiveVaultRecovery(
	recoverFromAddress string,
	recoverToAddress string,
	recoverAmount cosmos.Uint,
	asset common.Asset,
	memo string,
	signer cosmos.AccAddress,
) *MsgInactiveVaultRecovery {
	return &MsgInactiveVaultRecovery{
		RecoverFromAddress: recoverFromAddress,
		RecoverToAddress:   recoverToAddress,
		RecoverAmount:      recoverAmount,
		Asset:              asset,
		Memo:               memo,
		Signer:             signer,
	}
}

// ValidateBasic runs stateless checks on the message
func (m *MsgInactiveVaultRecovery) ValidateBasic() error {
	if m.Signer.Empty() {
		return cosmos.ErrInvalidAddress(m.Signer.String())
	}

	if strings.TrimSpace(m.RecoverFromAddress) == "" {
		return errors.New("recover from address cannot be empty")
	}

	if strings.TrimSpace(m.RecoverToAddress) == "" {
		return errors.New("recover to address cannot be empty")
	}

	if m.RecoverAmount.IsZero() {
		return errors.New("recover amount cannot be zero")
	}

	if m.Asset.IsEmpty() {
		return errors.New("asset cannot be empty")
	}

	return nil
}

// GetSigners defines whose signature is required
func (m *MsgInactiveVaultRecovery) GetSigners() []cosmos.AccAddress {
	return []cosmos.AccAddress{m.Signer}
}
