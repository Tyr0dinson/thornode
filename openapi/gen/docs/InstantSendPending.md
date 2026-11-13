# InstantSendPending

## Properties

Name | Type | Description | Notes
------------ | ------------- | ------------- | -------------
**Id** | **string** | Credited transaction ID. | 
**Amount** | **string** | Credited amount in base units (duffs for DASH). | 
**Height** | **string** | THORChain block height of the original early credit. | 

## Methods

### NewInstantSendPending

`func NewInstantSendPending(id string, amount string, height string, ) *InstantSendPending`

NewInstantSendPending instantiates a new InstantSendPending object
This constructor will assign default values to properties that have it defined,
and makes sure properties required by API are set, but the set of arguments
will change when the set of required properties is changed

### NewInstantSendPendingWithDefaults

`func NewInstantSendPendingWithDefaults() *InstantSendPending`

NewInstantSendPendingWithDefaults instantiates a new InstantSendPending object
This constructor will only assign default values to properties that have it defined,
but it doesn't guarantee that properties required by API are set

### GetId

`func (o *InstantSendPending) GetId() string`

GetId returns the Id field if non-nil, zero value otherwise.

### GetIdOk

`func (o *InstantSendPending) GetIdOk() (*string, bool)`

GetIdOk returns a tuple with the Id field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetId

`func (o *InstantSendPending) SetId(v string)`

SetId sets Id field to given value.


### GetAmount

`func (o *InstantSendPending) GetAmount() string`

GetAmount returns the Amount field if non-nil, zero value otherwise.

### GetAmountOk

`func (o *InstantSendPending) GetAmountOk() (*string, bool)`

GetAmountOk returns a tuple with the Amount field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetAmount

`func (o *InstantSendPending) SetAmount(v string)`

SetAmount sets Amount field to given value.


### GetHeight

`func (o *InstantSendPending) GetHeight() string`

GetHeight returns the Height field if non-nil, zero value otherwise.

### GetHeightOk

`func (o *InstantSendPending) GetHeightOk() (*string, bool)`

GetHeightOk returns a tuple with the Height field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetHeight

`func (o *InstantSendPending) SetHeight(v string)`

SetHeight sets Height field to given value.



[[Back to Model list]](../README.md#documentation-for-models) [[Back to API list]](../README.md#documentation-for-api-endpoints) [[Back to README]](../README.md)


