# InstantSendStatus

## Properties

Name | Type | Description | Notes
------------ | ------------- | ------------- | -------------
**Chain** | **string** |  | 
**Disabled** | **bool** | Permanent admission-disable flag, cleared only by a software-upgrade store migration. | 
**Overdue** | **[]string** | Pending transaction IDs whose settlement deadline has passed. | 
**MinHeight** | **string** | Minimum chain anchor height accepted for new early deposits. | 

## Methods

### NewInstantSendStatus

`func NewInstantSendStatus(chain string, disabled bool, overdue []string, minHeight string, ) *InstantSendStatus`

NewInstantSendStatus instantiates a new InstantSendStatus object
This constructor will assign default values to properties that have it defined,
and makes sure properties required by API are set, but the set of arguments
will change when the set of required properties is changed

### NewInstantSendStatusWithDefaults

`func NewInstantSendStatusWithDefaults() *InstantSendStatus`

NewInstantSendStatusWithDefaults instantiates a new InstantSendStatus object
This constructor will only assign default values to properties that have it defined,
but it doesn't guarantee that properties required by API are set

### GetChain

`func (o *InstantSendStatus) GetChain() string`

GetChain returns the Chain field if non-nil, zero value otherwise.

### GetChainOk

`func (o *InstantSendStatus) GetChainOk() (*string, bool)`

GetChainOk returns a tuple with the Chain field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetChain

`func (o *InstantSendStatus) SetChain(v string)`

SetChain sets Chain field to given value.


### GetDisabled

`func (o *InstantSendStatus) GetDisabled() bool`

GetDisabled returns the Disabled field if non-nil, zero value otherwise.

### GetDisabledOk

`func (o *InstantSendStatus) GetDisabledOk() (*bool, bool)`

GetDisabledOk returns a tuple with the Disabled field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetDisabled

`func (o *InstantSendStatus) SetDisabled(v bool)`

SetDisabled sets Disabled field to given value.


### GetOverdue

`func (o *InstantSendStatus) GetOverdue() []string`

GetOverdue returns the Overdue field if non-nil, zero value otherwise.

### GetOverdueOk

`func (o *InstantSendStatus) GetOverdueOk() (*[]string, bool)`

GetOverdueOk returns a tuple with the Overdue field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetOverdue

`func (o *InstantSendStatus) SetOverdue(v []string)`

SetOverdue sets Overdue field to given value.


### GetMinHeight

`func (o *InstantSendStatus) GetMinHeight() string`

GetMinHeight returns the MinHeight field if non-nil, zero value otherwise.

### GetMinHeightOk

`func (o *InstantSendStatus) GetMinHeightOk() (*string, bool)`

GetMinHeightOk returns a tuple with the MinHeight field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetMinHeight

`func (o *InstantSendStatus) SetMinHeight(v string)`

SetMinHeight sets MinHeight field to given value.



[[Back to Model list]](../README.md#documentation-for-models) [[Back to API list]](../README.md#documentation-for-api-endpoints) [[Back to README]](../README.md)


