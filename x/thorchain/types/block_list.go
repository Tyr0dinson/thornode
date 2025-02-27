package types

import "google.golang.org/protobuf/runtime/protoimpl"

// Add these type definitions to the existing file
type QueryBlockListAddressesRequest struct {
	state         protoimpl.MessageState
	sizeCache     protoimpl.SizeCache
	unknownFields protoimpl.UnknownFields
}

type BlockListAddressSet struct {
	state         protoimpl.MessageState
	sizeCache     protoimpl.SizeCache
	unknownFields protoimpl.UnknownFields
	Eth           []string `protobuf:"bytes,1,rep,name=eth,proto3" json:"eth,omitempty"`
	Btc           []string `protobuf:"bytes,2,rep,name=btc,proto3" json:"btc,omitempty"`
}

type QueryBlockListAddressesResponse struct {
	state         protoimpl.MessageState
	sizeCache     protoimpl.SizeCache
	unknownFields protoimpl.UnknownFields
	Addresses     map[string]*BlockListAddressSet `protobuf:"bytes,1,rep,name=addresses,proto3" json:"addresses,omitempty" protobuf_key:"bytes,1,opt,name=key,proto3,type=string" protobuf_val:"bytes,2,opt,name=value,proto3"`
}
