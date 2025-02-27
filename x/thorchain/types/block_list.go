package types

import (
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/gogoproto/proto"
)

var (
	_ sdk.Msg              = &QueryBlockListAddressesRequest{}
	_ sdk.HasValidateBasic = &QueryBlockListAddressesRequest{}
	_ proto.Message        = &BlockListAddressesMap{}
)

// ValidateBasic implements sdk.HasValidateBasic
func (m *QueryBlockListAddressesRequest) ValidateBasic() error {
	return nil
}

// GetSigners implements sdk.Msg
func (m *QueryBlockListAddressesRequest) GetSigners() []sdk.AccAddress {
	return []sdk.AccAddress{}
}

// BlockListAddresses represents addresses that are blocked
type BlockListAddresses struct {
	Addresses []string `json:"addresses"`
}

// BlockListAddressesMap is a wrapper for a map of BlockListAddresses that implements proto.Message
type BlockListAddressesMap struct {
	Addresses map[string]BlockListAddresses `json:"addresses"`
}

// Reset implements proto.Message
func (m *BlockListAddressesMap) Reset() {
	*m = BlockListAddressesMap{}
}

// String implements proto.Message
func (m *BlockListAddressesMap) String() string {
	return proto.CompactTextString(m)
}

// ProtoMessage implements proto.Message
func (*BlockListAddressesMap) ProtoMessage() {}
