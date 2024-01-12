# Availability

## Properties

Name | Type | Description | Notes
------------ | ------------- | ------------- | -------------
**IsAvailable** | Pointer to **bool** | returns true if this operation is available | [optional] 
**Reason** | Pointer to **string** | the reason why this operation is unavailable | [optional] 

## Methods

### NewAvailability

`func NewAvailability() *Availability`

NewAvailability instantiates a new Availability object
This constructor will assign default values to properties that have it defined,
and makes sure properties required by API are set, but the set of arguments
will change when the set of required properties is changed

### NewAvailabilityWithDefaults

`func NewAvailabilityWithDefaults() *Availability`

NewAvailabilityWithDefaults instantiates a new Availability object
This constructor will only assign default values to properties that have it defined,
but it doesn't guarantee that properties required by API are set

### GetIsAvailable

`func (o *Availability) GetIsAvailable() bool`

GetIsAvailable returns the IsAvailable field if non-nil, zero value otherwise.

### GetIsAvailableOk

`func (o *Availability) GetIsAvailableOk() (*bool, bool)`

GetIsAvailableOk returns a tuple with the IsAvailable field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetIsAvailable

`func (o *Availability) SetIsAvailable(v bool)`

SetIsAvailable sets IsAvailable field to given value.

### HasIsAvailable

`func (o *Availability) HasIsAvailable() bool`

HasIsAvailable returns a boolean if a field has been set.

### GetReason

`func (o *Availability) GetReason() string`

GetReason returns the Reason field if non-nil, zero value otherwise.

### GetReasonOk

`func (o *Availability) GetReasonOk() (*string, bool)`

GetReasonOk returns a tuple with the Reason field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetReason

`func (o *Availability) SetReason(v string)`

SetReason sets Reason field to given value.

### HasReason

`func (o *Availability) HasReason() bool`

HasReason returns a boolean if a field has been set.


[[Back to Model list]](../README.md#documentation-for-models) [[Back to API list]](../README.md#documentation-for-api-endpoints) [[Back to README]](../README.md)


