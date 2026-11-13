# InstantSendState

## Properties

Name | Type | Description | Notes
------------ | ------------- | ------------- | -------------
**PubKey** | **string** |  | 
**Pending** | [**[]InstantSendPending**](InstantSendPending.md) |  | 
**Chain** | **string** |  | 

## Methods

### NewInstantSendState

`func NewInstantSendState(pubKey string, pending []InstantSendPending, chain string, ) *InstantSendState`

NewInstantSendState instantiates a new InstantSendState object
This constructor will assign default values to properties that have it defined,
and makes sure properties required by API are set, but the set of arguments
will change when the set of required properties is changed

### NewInstantSendStateWithDefaults

`func NewInstantSendStateWithDefaults() *InstantSendState`

NewInstantSendStateWithDefaults instantiates a new InstantSendState object
This constructor will only assign default values to properties that have it defined,
but it doesn't guarantee that properties required by API are set

### GetPubKey

`func (o *InstantSendState) GetPubKey() string`

GetPubKey returns the PubKey field if non-nil, zero value otherwise.

### GetPubKeyOk

`func (o *InstantSendState) GetPubKeyOk() (*string, bool)`

GetPubKeyOk returns a tuple with the PubKey field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetPubKey

`func (o *InstantSendState) SetPubKey(v string)`

SetPubKey sets PubKey field to given value.


### GetPending

`func (o *InstantSendState) GetPending() []InstantSendPending`

GetPending returns the Pending field if non-nil, zero value otherwise.

### GetPendingOk

`func (o *InstantSendState) GetPendingOk() (*[]InstantSendPending, bool)`

GetPendingOk returns a tuple with the Pending field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetPending

`func (o *InstantSendState) SetPending(v []InstantSendPending)`

SetPending sets Pending field to given value.


### GetChain

`func (o *InstantSendState) GetChain() string`

GetChain returns the Chain field if non-nil, zero value otherwise.

### GetChainOk

`func (o *InstantSendState) GetChainOk() (*string, bool)`

GetChainOk returns a tuple with the Chain field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetChain

`func (o *InstantSendState) SetChain(v string)`

SetChain sets Chain field to given value.



[[Back to Model list]](../README.md#documentation-for-models) [[Back to API list]](../README.md#documentation-for-api-endpoints) [[Back to README]](../README.md)


