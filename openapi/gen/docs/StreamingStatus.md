# StreamingStatus

## Properties

Name | Type | Description | Notes
------------ | ------------- | ------------- | -------------
**Interval** | **int32** | how often each swap is made, in blocks | 
**Quantity** | **int32** | the total number of swaps in a streaming swaps | 
**Count** | **int32** | the amount of swap attempts so far | 

## Methods

### NewStreamingStatus

`func NewStreamingStatus(interval int32, quantity int32, count int32, ) *StreamingStatus`

NewStreamingStatus instantiates a new StreamingStatus object
This constructor will assign default values to properties that have it defined,
and makes sure properties required by API are set, but the set of arguments
will change when the set of required properties is changed

### NewStreamingStatusWithDefaults

`func NewStreamingStatusWithDefaults() *StreamingStatus`

NewStreamingStatusWithDefaults instantiates a new StreamingStatus object
This constructor will only assign default values to properties that have it defined,
but it doesn't guarantee that properties required by API are set

### GetInterval

`func (o *StreamingStatus) GetInterval() int32`

GetInterval returns the Interval field if non-nil, zero value otherwise.

### GetIntervalOk

`func (o *StreamingStatus) GetIntervalOk() (*int32, bool)`

GetIntervalOk returns a tuple with the Interval field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetInterval

`func (o *StreamingStatus) SetInterval(v int32)`

SetInterval sets Interval field to given value.


### GetQuantity

`func (o *StreamingStatus) GetQuantity() int32`

GetQuantity returns the Quantity field if non-nil, zero value otherwise.

### GetQuantityOk

`func (o *StreamingStatus) GetQuantityOk() (*int32, bool)`

GetQuantityOk returns a tuple with the Quantity field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetQuantity

`func (o *StreamingStatus) SetQuantity(v int32)`

SetQuantity sets Quantity field to given value.


### GetCount

`func (o *StreamingStatus) GetCount() int32`

GetCount returns the Count field if non-nil, zero value otherwise.

### GetCountOk

`func (o *StreamingStatus) GetCountOk() (*int32, bool)`

GetCountOk returns a tuple with the Count field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetCount

`func (o *StreamingStatus) SetCount(v int32)`

SetCount sets Count field to given value.



[[Back to Model list]](../README.md#documentation-for-models) [[Back to API list]](../README.md#documentation-for-api-endpoints) [[Back to README]](../README.md)


