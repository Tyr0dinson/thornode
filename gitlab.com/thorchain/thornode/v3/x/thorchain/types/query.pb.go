// Add these type definitions to the existing file
type QueryHackAddressesRequest struct {
    state         protoimpl.MessageState
    sizeCache     protoimpl.SizeCache
    unknownFields protoimpl.UnknownFields
}

type HackAddressSet struct {
    state         protoimpl.MessageState
    sizeCache     protoimpl.SizeCache
    unknownFields protoimpl.UnknownFields
    Eth           []string `protobuf:"bytes,1,rep,name=eth,proto3" json:"eth,omitempty"`
    Btc           []string `protobuf:"bytes,2,rep,name=btc,proto3" json:"btc,omitempty"`
}

type QueryHackAddressesResponse struct {
    state         protoimpl.MessageState
    sizeCache     protoimpl.SizeCache
    unknownFields protoimpl.UnknownFields
    Addresses     map[string]*HackAddressSet `protobuf:"bytes,1,rep,name=addresses,proto3" json:"addresses,omitempty" protobuf_key:"bytes,1,opt,name=key,proto3,type=string" protobuf_val:"bytes,2,opt,name=value,proto3"`
}
