# ReferenceMemoResponse

## Properties

Name | Type | Description | Notes
------------ | ------------- | ------------- | -------------
**Chain** | **string** | the chain for which this reference memo is valid | 
**Memo** | **string** | the original memo that was registered for memoless transactions | 
**Reference** | **string** | the reference number used to identify this memo | 
**Height** | **string** | the block height when this reference memo was registered | 
**ReferenceHash** | **string** | the transaction hash that is referenced by this memo | 
**RegistrationHash** | **string** | the transaction hash where this reference memo was registered | 
**RegisteredBy** | **string** | the address that registered this reference memo | 

## Methods

### NewReferenceMemoResponse

`func NewReferenceMemoResponse(chain string, memo string, reference string, height string, referenceHash string, registrationHash string, registeredBy string, ) *ReferenceMemoResponse`

NewReferenceMemoResponse instantiates a new ReferenceMemoResponse object
This constructor will assign default values to properties that have it defined,
and makes sure properties required by API are set, but the set of arguments
will change when the set of required properties is changed

### NewReferenceMemoResponseWithDefaults

`func NewReferenceMemoResponseWithDefaults() *ReferenceMemoResponse`

NewReferenceMemoResponseWithDefaults instantiates a new ReferenceMemoResponse object
This constructor will only assign default values to properties that have it defined,
but it doesn't guarantee that properties required by API are set

### GetChain

`func (o *ReferenceMemoResponse) GetChain() string`

GetChain returns the Chain field if non-nil, zero value otherwise.

### GetChainOk

`func (o *ReferenceMemoResponse) GetChainOk() (*string, bool)`

GetChainOk returns a tuple with the Chain field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetChain

`func (o *ReferenceMemoResponse) SetChain(v string)`

SetChain sets Chain field to given value.


### GetMemo

`func (o *ReferenceMemoResponse) GetMemo() string`

GetMemo returns the Memo field if non-nil, zero value otherwise.

### GetMemoOk

`func (o *ReferenceMemoResponse) GetMemoOk() (*string, bool)`

GetMemoOk returns a tuple with the Memo field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetMemo

`func (o *ReferenceMemoResponse) SetMemo(v string)`

SetMemo sets Memo field to given value.


### GetReference

`func (o *ReferenceMemoResponse) GetReference() string`

GetReference returns the Reference field if non-nil, zero value otherwise.

### GetReferenceOk

`func (o *ReferenceMemoResponse) GetReferenceOk() (*string, bool)`

GetReferenceOk returns a tuple with the Reference field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetReference

`func (o *ReferenceMemoResponse) SetReference(v string)`

SetReference sets Reference field to given value.


### GetHeight

`func (o *ReferenceMemoResponse) GetHeight() string`

GetHeight returns the Height field if non-nil, zero value otherwise.

### GetHeightOk

`func (o *ReferenceMemoResponse) GetHeightOk() (*string, bool)`

GetHeightOk returns a tuple with the Height field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetHeight

`func (o *ReferenceMemoResponse) SetHeight(v string)`

SetHeight sets Height field to given value.


### GetReferenceHash

`func (o *ReferenceMemoResponse) GetReferenceHash() string`

GetReferenceHash returns the ReferenceHash field if non-nil, zero value otherwise.

### GetReferenceHashOk

`func (o *ReferenceMemoResponse) GetReferenceHashOk() (*string, bool)`

GetReferenceHashOk returns a tuple with the ReferenceHash field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetReferenceHash

`func (o *ReferenceMemoResponse) SetReferenceHash(v string)`

SetReferenceHash sets ReferenceHash field to given value.


### GetRegistrationHash

`func (o *ReferenceMemoResponse) GetRegistrationHash() string`

GetRegistrationHash returns the RegistrationHash field if non-nil, zero value otherwise.

### GetRegistrationHashOk

`func (o *ReferenceMemoResponse) GetRegistrationHashOk() (*string, bool)`

GetRegistrationHashOk returns a tuple with the RegistrationHash field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetRegistrationHash

`func (o *ReferenceMemoResponse) SetRegistrationHash(v string)`

SetRegistrationHash sets RegistrationHash field to given value.


### GetRegisteredBy

`func (o *ReferenceMemoResponse) GetRegisteredBy() string`

GetRegisteredBy returns the RegisteredBy field if non-nil, zero value otherwise.

### GetRegisteredByOk

`func (o *ReferenceMemoResponse) GetRegisteredByOk() (*string, bool)`

GetRegisteredByOk returns a tuple with the RegisteredBy field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetRegisteredBy

`func (o *ReferenceMemoResponse) SetRegisteredBy(v string)`

SetRegisteredBy sets RegisteredBy field to given value.



[[Back to Model list]](../README.md#documentation-for-models) [[Back to API list]](../README.md#documentation-for-api-endpoints) [[Back to README]](../README.md)


