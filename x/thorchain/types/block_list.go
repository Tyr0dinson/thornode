package types

import (
	sdk "github.com/cosmos/cosmos-sdk/types"
)

var (
	_ sdk.Msg              = &QueryBlockListAddressesRequest{}
	_ sdk.HasValidateBasic = &QueryBlockListAddressesRequest{}
)

// ValidateBasic implements sdk.HasValidateBasic
func (m *QueryBlockListAddressesRequest) ValidateBasic() error {
	return nil
}

// GetSigners implements sdk.Msg
func (m *QueryBlockListAddressesRequest) GetSigners() []sdk.AccAddress {
	return []sdk.AccAddress{}
}
