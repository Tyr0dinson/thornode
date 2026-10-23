# ConstantsResponse

## Properties

Name | Type | Description | Notes
------------ | ------------- | ------------- | -------------
**Int64Values** | Pointer to **map[string]string** |  | [optional] 
**BoolValues** | Pointer to **map[string]string** |  | [optional] 
**StringValues** | Pointer to **map[string]string** |  | [optional] 
**BlockTimeMs** | Pointer to **int64** | the effective block time in milliseconds that the block-denominated int_64_values are scaled to | [optional] 
**BlockTimeHistory** | Pointer to [**[]ConstantsResponseBlockTimeHistoryInner**](ConstantsResponseBlockTimeHistoryInner.md) | every change of the effective block time, oldest first; omitted while it never changed | [optional] 

## Methods

### NewConstantsResponse

`func NewConstantsResponse() *ConstantsResponse`

NewConstantsResponse instantiates a new ConstantsResponse object
This constructor will assign default values to properties that have it defined,
and makes sure properties required by API are set, but the set of arguments
will change when the set of required properties is changed

### NewConstantsResponseWithDefaults

`func NewConstantsResponseWithDefaults() *ConstantsResponse`

NewConstantsResponseWithDefaults instantiates a new ConstantsResponse object
This constructor will only assign default values to properties that have it defined,
but it doesn't guarantee that properties required by API are set

### GetInt64Values

`func (o *ConstantsResponse) GetInt64Values() map[string]string`

GetInt64Values returns the Int64Values field if non-nil, zero value otherwise.

### GetInt64ValuesOk

`func (o *ConstantsResponse) GetInt64ValuesOk() (*map[string]string, bool)`

GetInt64ValuesOk returns a tuple with the Int64Values field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetInt64Values

`func (o *ConstantsResponse) SetInt64Values(v map[string]string)`

SetInt64Values sets Int64Values field to given value.

### HasInt64Values

`func (o *ConstantsResponse) HasInt64Values() bool`

HasInt64Values returns a boolean if a field has been set.

### GetBoolValues

`func (o *ConstantsResponse) GetBoolValues() map[string]string`

GetBoolValues returns the BoolValues field if non-nil, zero value otherwise.

### GetBoolValuesOk

`func (o *ConstantsResponse) GetBoolValuesOk() (*map[string]string, bool)`

GetBoolValuesOk returns a tuple with the BoolValues field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetBoolValues

`func (o *ConstantsResponse) SetBoolValues(v map[string]string)`

SetBoolValues sets BoolValues field to given value.

### HasBoolValues

`func (o *ConstantsResponse) HasBoolValues() bool`

HasBoolValues returns a boolean if a field has been set.

### GetStringValues

`func (o *ConstantsResponse) GetStringValues() map[string]string`

GetStringValues returns the StringValues field if non-nil, zero value otherwise.

### GetStringValuesOk

`func (o *ConstantsResponse) GetStringValuesOk() (*map[string]string, bool)`

GetStringValuesOk returns a tuple with the StringValues field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetStringValues

`func (o *ConstantsResponse) SetStringValues(v map[string]string)`

SetStringValues sets StringValues field to given value.

### HasStringValues

`func (o *ConstantsResponse) HasStringValues() bool`

HasStringValues returns a boolean if a field has been set.

### GetBlockTimeMs

`func (o *ConstantsResponse) GetBlockTimeMs() int64`

GetBlockTimeMs returns the BlockTimeMs field if non-nil, zero value otherwise.

### GetBlockTimeMsOk

`func (o *ConstantsResponse) GetBlockTimeMsOk() (*int64, bool)`

GetBlockTimeMsOk returns a tuple with the BlockTimeMs field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetBlockTimeMs

`func (o *ConstantsResponse) SetBlockTimeMs(v int64)`

SetBlockTimeMs sets BlockTimeMs field to given value.

### HasBlockTimeMs

`func (o *ConstantsResponse) HasBlockTimeMs() bool`

HasBlockTimeMs returns a boolean if a field has been set.

### GetBlockTimeHistory

`func (o *ConstantsResponse) GetBlockTimeHistory() []ConstantsResponseBlockTimeHistoryInner`

GetBlockTimeHistory returns the BlockTimeHistory field if non-nil, zero value otherwise.

### GetBlockTimeHistoryOk

`func (o *ConstantsResponse) GetBlockTimeHistoryOk() (*[]ConstantsResponseBlockTimeHistoryInner, bool)`

GetBlockTimeHistoryOk returns a tuple with the BlockTimeHistory field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetBlockTimeHistory

`func (o *ConstantsResponse) SetBlockTimeHistory(v []ConstantsResponseBlockTimeHistoryInner)`

SetBlockTimeHistory sets BlockTimeHistory field to given value.

### HasBlockTimeHistory

`func (o *ConstantsResponse) HasBlockTimeHistory() bool`

HasBlockTimeHistory returns a boolean if a field has been set.


[[Back to Model list]](../README.md#documentation-for-models) [[Back to API list]](../README.md#documentation-for-api-endpoints) [[Back to README]](../README.md)


