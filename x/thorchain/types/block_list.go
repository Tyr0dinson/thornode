package types

import (
	sdk "github.com/cosmos/cosmos-sdk/types"
	"google.golang.org/protobuf/runtime/protoimpl"
)

var (
	_ sdk.Msg              = &QueryBlockListAddressesRequest{}
	_ sdk.HasValidateBasic = &QueryBlockListAddressesRequest{}
)

// QueryBlockListAddressesRequest is a request message for querying block list addresses
type QueryBlockListAddressesRequest struct {
	state         protoimpl.MessageState
	sizeCache     protoimpl.SizeCache
	unknownFields protoimpl.UnknownFields
}

// BlockListAddressSet represents a set of blocked addresses for different chains
type BlockListAddressSet struct {
	state         protoimpl.MessageState
	sizeCache     protoimpl.SizeCache
	unknownFields protoimpl.UnknownFields
	Eth           []string `protobuf:"bytes,1,rep,name=eth,proto3" json:"eth,omitempty"`
	Btc           []string `protobuf:"bytes,2,rep,name=btc,proto3" json:"btc,omitempty"`
}

// QueryBlockListAddressesResponse is the response message for block list addresses query
type QueryBlockListAddressesResponse struct {
	state         protoimpl.MessageState
	sizeCache     protoimpl.SizeCache
	unknownFields protoimpl.UnknownFields
	Addresses     map[string]*BlockListAddressSet `protobuf:"bytes,1,rep,name=addresses,proto3" json:"addresses,omitempty" protobuf_key:"bytes,1,opt,name=key,proto3,type=string" protobuf_val:"bytes,2,opt,name=value,proto3"`
}

// ValidateBasic implements sdk.HasValidateBasic
func (m *QueryBlockListAddressesRequest) ValidateBasic() error {
	return nil
}

// GetSigners implements sdk.Msg
func (m *QueryBlockListAddressesRequest) GetSigners() []sdk.AccAddress {
	return []sdk.AccAddress{}
}
