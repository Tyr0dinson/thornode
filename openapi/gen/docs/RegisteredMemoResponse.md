# RegisteredMemoResponse

## Properties

Name | Type | Description | Notes
------------ | ------------- | ------------- | -------------
**Memo** | **string** | The registered memo content | 
**Reference** | **string** | The 5-digit reference code | 
**ExpiresAt** | **int64** | Unix timestamp when the memo expires | 
**ChainId** | **string** | Chain identifier for the memo | 
**RegisteredAt** | **int64** | Unix timestamp when the memo was registered | 

## Methods

### NewRegisteredMemoResponse

`func NewRegisteredMemoResponse(memo string, reference string, expiresAt int64, chainId string, registeredAt int64, ) *RegisteredMemoResponse`

NewRegisteredMemoResponse instantiates a new RegisteredMemoResponse object
This constructor will assign default values to properties that have it defined,
and makes sure properties required by API are set, but the set of arguments
will change when the set of required properties is changed

### NewRegisteredMemoResponseWithDefaults

`func NewRegisteredMemoResponseWithDefaults() *RegisteredMemoResponse`

NewRegisteredMemoResponseWithDefaults instantiates a new RegisteredMemoResponse object
This constructor will only assign default values to properties that have it defined,
but it doesn't guarantee that properties required by API are set

### GetMemo

`func (o *RegisteredMemoResponse) GetMemo() string`

GetMemo returns the Memo field if non-nil, zero value otherwise.

### GetMemoOk

`func (o *RegisteredMemoResponse) GetMemoOk() (*string, bool)`

GetMemoOk returns a tuple with the Memo field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetMemo

`func (o *RegisteredMemoResponse) SetMemo(v string)`

SetMemo sets Memo field to given value.


### GetReference

`func (o *RegisteredMemoResponse) GetReference() string`

GetReference returns the Reference field if non-nil, zero value otherwise.

### GetReferenceOk

`func (o *RegisteredMemoResponse) GetReferenceOk() (*string, bool)`

GetReferenceOk returns a tuple with the Reference field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetReference

`func (o *RegisteredMemoResponse) SetReference(v string)`

SetReference sets Reference field to given value.


### GetExpiresAt

`func (o *RegisteredMemoResponse) GetExpiresAt() int64`

GetExpiresAt returns the ExpiresAt field if non-nil, zero value otherwise.

### GetExpiresAtOk

`func (o *RegisteredMemoResponse) GetExpiresAtOk() (*int64, bool)`

GetExpiresAtOk returns a tuple with the ExpiresAt field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetExpiresAt

`func (o *RegisteredMemoResponse) SetExpiresAt(v int64)`

SetExpiresAt sets ExpiresAt field to given value.


### GetChainId

`func (o *RegisteredMemoResponse) GetChainId() string`

GetChainId returns the ChainId field if non-nil, zero value otherwise.

### GetChainIdOk

`func (o *RegisteredMemoResponse) GetChainIdOk() (*string, bool)`

GetChainIdOk returns a tuple with the ChainId field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetChainId

`func (o *RegisteredMemoResponse) SetChainId(v string)`

SetChainId sets ChainId field to given value.


### GetRegisteredAt

`func (o *RegisteredMemoResponse) GetRegisteredAt() int64`

GetRegisteredAt returns the RegisteredAt field if non-nil, zero value otherwise.

### GetRegisteredAtOk

`func (o *RegisteredMemoResponse) GetRegisteredAtOk() (*int64, bool)`

GetRegisteredAtOk returns a tuple with the RegisteredAt field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetRegisteredAt

`func (o *RegisteredMemoResponse) SetRegisteredAt(v int64)`

SetRegisteredAt sets RegisteredAt field to given value.



[[Back to Model list]](../README.md#documentation-for-models) [[Back to API list]](../README.md#documentation-for-api-endpoints) [[Back to README]](../README.md)


