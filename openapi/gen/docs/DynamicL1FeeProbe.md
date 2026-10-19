# DynamicL1FeeProbe

## Properties

Name | Type | Description | Notes
------------ | ------------- | ------------- | -------------
**Bps** | **int32** | the tested dynamic_bps value | 
**Signal** | **string** | mean normalized fee signal (fees_tor / pool_volume_tor, scaled by 1e12) over the sealed epochs measured at this bps  | 
**Epochs** | **int32** | sealed epochs measured at this bps | 

## Methods

### NewDynamicL1FeeProbe

`func NewDynamicL1FeeProbe(bps int32, signal string, epochs int32, ) *DynamicL1FeeProbe`

NewDynamicL1FeeProbe instantiates a new DynamicL1FeeProbe object
This constructor will assign default values to properties that have it defined,
and makes sure properties required by API are set, but the set of arguments
will change when the set of required properties is changed

### NewDynamicL1FeeProbeWithDefaults

`func NewDynamicL1FeeProbeWithDefaults() *DynamicL1FeeProbe`

NewDynamicL1FeeProbeWithDefaults instantiates a new DynamicL1FeeProbe object
This constructor will only assign default values to properties that have it defined,
but it doesn't guarantee that properties required by API are set

### GetBps

`func (o *DynamicL1FeeProbe) GetBps() int32`

GetBps returns the Bps field if non-nil, zero value otherwise.

### GetBpsOk

`func (o *DynamicL1FeeProbe) GetBpsOk() (*int32, bool)`

GetBpsOk returns a tuple with the Bps field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetBps

`func (o *DynamicL1FeeProbe) SetBps(v int32)`

SetBps sets Bps field to given value.


### GetSignal

`func (o *DynamicL1FeeProbe) GetSignal() string`

GetSignal returns the Signal field if non-nil, zero value otherwise.

### GetSignalOk

`func (o *DynamicL1FeeProbe) GetSignalOk() (*string, bool)`

GetSignalOk returns a tuple with the Signal field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetSignal

`func (o *DynamicL1FeeProbe) SetSignal(v string)`

SetSignal sets Signal field to given value.


### GetEpochs

`func (o *DynamicL1FeeProbe) GetEpochs() int32`

GetEpochs returns the Epochs field if non-nil, zero value otherwise.

### GetEpochsOk

`func (o *DynamicL1FeeProbe) GetEpochsOk() (*int32, bool)`

GetEpochsOk returns a tuple with the Epochs field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetEpochs

`func (o *DynamicL1FeeProbe) SetEpochs(v int32)`

SetEpochs sets Epochs field to given value.



[[Back to Model list]](../README.md#documentation-for-models) [[Back to API list]](../README.md#documentation-for-api-endpoints) [[Back to README]](../README.md)


