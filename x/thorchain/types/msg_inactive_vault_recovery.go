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
	inactiveVaultPubKey string,
	recoverAddress string,
	recoverAmount cosmos.Uint,
	asset common.Asset,
	toAddress string,
	memo string,
	signer cosmos.AccAddress,
) *MsgInactiveVaultRecovery {
	return &MsgInactiveVaultRecovery{
		InactiveVaultPubKey: inactiveVaultPubKey,
		RecoverAddress:      recoverAddress,
		RecoverAmount:       recoverAmount,
		Asset:               asset,
		ToAddress:           toAddress,
		Memo:                memo,
		Signer:              signer,
	}
}

// ValidateBasic runs stateless checks on the message
func (m *MsgInactiveVaultRecovery) ValidateBasic() error {
	if m.Signer.Empty() {
		return cosmos.ErrInvalidAddress(m.Signer.String())
	}

	if strings.TrimSpace(m.InactiveVaultPubKey) == "" {
		return errors.New("inactive vault public key cannot be empty")
	}

	if strings.TrimSpace(m.RecoverAddress) == "" {
		return errors.New("recover address cannot be empty")
	}

	if m.RecoverAmount.IsZero() {
		return errors.New("recover amount cannot be zero")
	}

	if m.Asset.IsEmpty() {
		return errors.New("asset cannot be empty")
	}

	if strings.TrimSpace(m.ToAddress) == "" {
		return errors.New("to address cannot be empty")
	}

	return nil
}

// GetSigners defines whose signature is required
func (m *MsgInactiveVaultRecovery) GetSigners() []cosmos.AccAddress {
	return []cosmos.AccAddress{m.Signer}
}
