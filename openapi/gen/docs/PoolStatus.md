# PoolStatus

## Properties

Name | Type | Description | Notes
------------ | ------------- | ------------- | -------------
**Status** | **string** |  | 
**Saver** | **bool** |  | 
**TORAnchor** | **bool** |  | 
**LendingCollateral** | **bool** |  | 
**POL** | **bool** |  | 
**SynthsAvailable** | [**Availability**](Availability.md) |  | 

## Methods

### NewPoolStatus

`func NewPoolStatus(status string, saver bool, tORAnchor bool, lendingCollateral bool, pOL bool, synthsAvailable Availability, ) *PoolStatus`

NewPoolStatus instantiates a new PoolStatus object
This constructor will assign default values to properties that have it defined,
and makes sure properties required by API are set, but the set of arguments
will change when the set of required properties is changed

### NewPoolStatusWithDefaults

`func NewPoolStatusWithDefaults() *PoolStatus`

NewPoolStatusWithDefaults instantiates a new PoolStatus object
This constructor will only assign default values to properties that have it defined,
but it doesn't guarantee that properties required by API are set

### GetStatus

`func (o *PoolStatus) GetStatus() string`

GetStatus returns the Status field if non-nil, zero value otherwise.

### GetStatusOk

`func (o *PoolStatus) GetStatusOk() (*string, bool)`

GetStatusOk returns a tuple with the Status field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetStatus

`func (o *PoolStatus) SetStatus(v string)`

SetStatus sets Status field to given value.


### GetSaver

`func (o *PoolStatus) GetSaver() bool`

GetSaver returns the Saver field if non-nil, zero value otherwise.

### GetSaverOk

`func (o *PoolStatus) GetSaverOk() (*bool, bool)`

GetSaverOk returns a tuple with the Saver field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetSaver

`func (o *PoolStatus) SetSaver(v bool)`

SetSaver sets Saver field to given value.


### GetTORAnchor

`func (o *PoolStatus) GetTORAnchor() bool`

GetTORAnchor returns the TORAnchor field if non-nil, zero value otherwise.

### GetTORAnchorOk

`func (o *PoolStatus) GetTORAnchorOk() (*bool, bool)`

GetTORAnchorOk returns a tuple with the TORAnchor field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetTORAnchor

`func (o *PoolStatus) SetTORAnchor(v bool)`

SetTORAnchor sets TORAnchor field to given value.


### GetLendingCollateral

`func (o *PoolStatus) GetLendingCollateral() bool`

GetLendingCollateral returns the LendingCollateral field if non-nil, zero value otherwise.

### GetLendingCollateralOk

`func (o *PoolStatus) GetLendingCollateralOk() (*bool, bool)`

GetLendingCollateralOk returns a tuple with the LendingCollateral field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetLendingCollateral

`func (o *PoolStatus) SetLendingCollateral(v bool)`

SetLendingCollateral sets LendingCollateral field to given value.


### GetPOL

`func (o *PoolStatus) GetPOL() bool`

GetPOL returns the POL field if non-nil, zero value otherwise.

### GetPOLOk

`func (o *PoolStatus) GetPOLOk() (*bool, bool)`

GetPOLOk returns a tuple with the POL field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetPOL

`func (o *PoolStatus) SetPOL(v bool)`

SetPOL sets POL field to given value.


### GetSynthsAvailable

`func (o *PoolStatus) GetSynthsAvailable() Availability`

GetSynthsAvailable returns the SynthsAvailable field if non-nil, zero value otherwise.

### GetSynthsAvailableOk

`func (o *PoolStatus) GetSynthsAvailableOk() (*Availability, bool)`

GetSynthsAvailableOk returns a tuple with the SynthsAvailable field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetSynthsAvailable

`func (o *PoolStatus) SetSynthsAvailable(v Availability)`

SetSynthsAvailable sets SynthsAvailable field to given value.



[[Back to Model list]](../README.md#documentation-for-models) [[Back to API list]](../README.md#documentation-for-api-endpoints) [[Back to README]](../README.md)


