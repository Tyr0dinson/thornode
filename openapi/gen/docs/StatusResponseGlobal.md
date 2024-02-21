# StatusResponseGlobal

## Properties

Name | Type | Description | Notes
------------ | ------------- | ------------- | -------------
**Swapping** | [**Availability**](Availability.md) |  | 
**LpWithdrawAvailable** | [**Availability**](Availability.md) |  | 
**LpAddAvailable** | [**Availability**](Availability.md) |  | 
**Lending** | [**Availability**](Availability.md) |  | 
**Savers** | [**Availability**](Availability.md) |  | 
**Synths** | [**Availability**](Availability.md) |  | 

## Methods

### NewStatusResponseGlobal

`func NewStatusResponseGlobal(swapping Availability, lpWithdrawAvailable Availability, lpAddAvailable Availability, lending Availability, savers Availability, synths Availability, ) *StatusResponseGlobal`

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

`func (o *StatusResponseGlobal) GetSwapping() Availability`

GetSwapping returns the Swapping field if non-nil, zero value otherwise.

### GetSwappingOk

`func (o *StatusResponseGlobal) GetSwappingOk() (*Availability, bool)`

GetSwappingOk returns a tuple with the Swapping field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetSwapping

`func (o *StatusResponseGlobal) SetSwapping(v Availability)`

SetSwapping sets Swapping field to given value.


### GetLpWithdrawAvailable

`func (o *StatusResponseGlobal) GetLpWithdrawAvailable() Availability`

GetLpWithdrawAvailable returns the LpWithdrawAvailable field if non-nil, zero value otherwise.

### GetLpWithdrawAvailableOk

`func (o *StatusResponseGlobal) GetLpWithdrawAvailableOk() (*Availability, bool)`

GetLpWithdrawAvailableOk returns a tuple with the LpWithdrawAvailable field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetLpWithdrawAvailable

`func (o *StatusResponseGlobal) SetLpWithdrawAvailable(v Availability)`

SetLpWithdrawAvailable sets LpWithdrawAvailable field to given value.


### GetLpAddAvailable

`func (o *StatusResponseGlobal) GetLpAddAvailable() Availability`

GetLpAddAvailable returns the LpAddAvailable field if non-nil, zero value otherwise.

### GetLpAddAvailableOk

`func (o *StatusResponseGlobal) GetLpAddAvailableOk() (*Availability, bool)`

GetLpAddAvailableOk returns a tuple with the LpAddAvailable field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetLpAddAvailable

`func (o *StatusResponseGlobal) SetLpAddAvailable(v Availability)`

SetLpAddAvailable sets LpAddAvailable field to given value.


### GetLending

`func (o *StatusResponseGlobal) GetLending() Availability`

GetLending returns the Lending field if non-nil, zero value otherwise.

### GetLendingOk

`func (o *StatusResponseGlobal) GetLendingOk() (*Availability, bool)`

GetLendingOk returns a tuple with the Lending field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetLending

`func (o *StatusResponseGlobal) SetLending(v Availability)`

SetLending sets Lending field to given value.


### GetSavers

`func (o *StatusResponseGlobal) GetSavers() Availability`

GetSavers returns the Savers field if non-nil, zero value otherwise.

### GetSaversOk

`func (o *StatusResponseGlobal) GetSaversOk() (*Availability, bool)`

GetSaversOk returns a tuple with the Savers field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetSavers

`func (o *StatusResponseGlobal) SetSavers(v Availability)`

SetSavers sets Savers field to given value.


### GetSynths

`func (o *StatusResponseGlobal) GetSynths() Availability`

GetSynths returns the Synths field if non-nil, zero value otherwise.

### GetSynthsOk

`func (o *StatusResponseGlobal) GetSynthsOk() (*Availability, bool)`

GetSynthsOk returns a tuple with the Synths field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetSynths

`func (o *StatusResponseGlobal) SetSynths(v Availability)`

SetSynths sets Synths field to given value.



[[Back to Model list]](../README.md#documentation-for-models) [[Back to API list]](../README.md#documentation-for-api-endpoints) [[Back to README]](../README.md)


