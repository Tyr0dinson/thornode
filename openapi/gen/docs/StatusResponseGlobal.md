# StatusResponseGlobal

## Properties

Name | Type | Description | Notes
------------ | ------------- | ------------- | -------------
**Swapping** | **bool** | returns true if global trading is enabled | 
**LpActions** | **bool** | returns true if global liquidity provider actions are enabled | 
**Lending** | **bool** | returns true if lending is enabled | 

## Methods

### NewStatusResponseGlobal

`func NewStatusResponseGlobal(swapping bool, lpActions bool, lending bool, ) *StatusResponseGlobal`

NewStatusResponseGlobal instantiates a new StatusResponseGlobal object
This constructor will assign default values to properties that have it defined,
and makes sure properties required by API are set, but the set of arguments
will change when the set of required properties is changed

### NewStatusResponseGlobalWithDefaults

`func NewStatusResponseGlobalWithDefaults() *StatusResponseGlobal`

NewStatusResponseGlobalWithDefaults instantiates a new StatusResponseGlobal object
This constructor will only assign default values to properties that have it defined,
but it doesn't guarantee that properties required by API are set

### GetSwapping

`func (o *StatusResponseGlobal) GetSwapping() bool`

GetSwapping returns the Swapping field if non-nil, zero value otherwise.

### GetSwappingOk

`func (o *StatusResponseGlobal) GetSwappingOk() (*bool, bool)`

GetSwappingOk returns a tuple with the Swapping field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetSwapping

`func (o *StatusResponseGlobal) SetSwapping(v bool)`

SetSwapping sets Swapping field to given value.


### GetLpActions

`func (o *StatusResponseGlobal) GetLpActions() bool`

GetLpActions returns the LpActions field if non-nil, zero value otherwise.

### GetLpActionsOk

`func (o *StatusResponseGlobal) GetLpActionsOk() (*bool, bool)`

GetLpActionsOk returns a tuple with the LpActions field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetLpActions

`func (o *StatusResponseGlobal) SetLpActions(v bool)`

SetLpActions sets LpActions field to given value.


### GetLending

`func (o *StatusResponseGlobal) GetLending() bool`

GetLending returns the Lending field if non-nil, zero value otherwise.

### GetLendingOk

`func (o *StatusResponseGlobal) GetLendingOk() (*bool, bool)`

GetLendingOk returns a tuple with the Lending field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetLending

`func (o *StatusResponseGlobal) SetLending(v bool)`

SetLending sets Lending field to given value.



[[Back to Model list]](../README.md#documentation-for-models) [[Back to API list]](../README.md#documentation-for-api-endpoints) [[Back to README]](../README.md)


