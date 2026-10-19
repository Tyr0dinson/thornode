# DynamicL1FeePairDetail

## Properties

Name | Type | Description | Notes
------------ | ------------- | ------------- | -------------
**Pair** | **string** |  | 
**DynamicBps** | **int32** |  | 
**LastActiveEpoch** | **int32** |  | 
**History** | [**[]DynamicL1FeeHistoryEntry**](DynamicL1FeeHistoryEntry.md) |  | 
**Phase** | Pointer to **string** | &#x60;search&#x60; while the bracket search is still testing bps values, &#x60;track&#x60; once it has converged and the +/-step controller follows drift from the best value found, &#x60;monitor&#x60; for a thorname in a monitor whitelist state (history is collected, no fee decisions)  | [optional] 
**Probes** | Pointer to [**[]DynamicL1FeeProbe**](DynamicL1FeeProbe.md) | one entry per bps value tested by the current search | [optional] 

## Methods

### NewDynamicL1FeePairDetail

`func NewDynamicL1FeePairDetail(pair string, dynamicBps int32, lastActiveEpoch int32, history []DynamicL1FeeHistoryEntry, ) *DynamicL1FeePairDetail`

NewDynamicL1FeePairDetail instantiates a new DynamicL1FeePairDetail object
This constructor will assign default values to properties that have it defined,
and makes sure properties required by API are set, but the set of arguments
will change when the set of required properties is changed

### NewDynamicL1FeePairDetailWithDefaults

`func NewDynamicL1FeePairDetailWithDefaults() *DynamicL1FeePairDetail`

NewDynamicL1FeePairDetailWithDefaults instantiates a new DynamicL1FeePairDetail object
This constructor will only assign default values to properties that have it defined,
but it doesn't guarantee that properties required by API are set

### GetPair

`func (o *DynamicL1FeePairDetail) GetPair() string`

GetPair returns the Pair field if non-nil, zero value otherwise.

### GetPairOk

`func (o *DynamicL1FeePairDetail) GetPairOk() (*string, bool)`

GetPairOk returns a tuple with the Pair field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetPair

`func (o *DynamicL1FeePairDetail) SetPair(v string)`

SetPair sets Pair field to given value.


### GetDynamicBps

`func (o *DynamicL1FeePairDetail) GetDynamicBps() int32`

GetDynamicBps returns the DynamicBps field if non-nil, zero value otherwise.

### GetDynamicBpsOk

`func (o *DynamicL1FeePairDetail) GetDynamicBpsOk() (*int32, bool)`

GetDynamicBpsOk returns a tuple with the DynamicBps field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetDynamicBps

`func (o *DynamicL1FeePairDetail) SetDynamicBps(v int32)`

SetDynamicBps sets DynamicBps field to given value.


### GetLastActiveEpoch

`func (o *DynamicL1FeePairDetail) GetLastActiveEpoch() int32`

GetLastActiveEpoch returns the LastActiveEpoch field if non-nil, zero value otherwise.

### GetLastActiveEpochOk

`func (o *DynamicL1FeePairDetail) GetLastActiveEpochOk() (*int32, bool)`

GetLastActiveEpochOk returns a tuple with the LastActiveEpoch field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetLastActiveEpoch

`func (o *DynamicL1FeePairDetail) SetLastActiveEpoch(v int32)`

SetLastActiveEpoch sets LastActiveEpoch field to given value.


### GetHistory

`func (o *DynamicL1FeePairDetail) GetHistory() []DynamicL1FeeHistoryEntry`

GetHistory returns the History field if non-nil, zero value otherwise.

### GetHistoryOk

`func (o *DynamicL1FeePairDetail) GetHistoryOk() (*[]DynamicL1FeeHistoryEntry, bool)`

GetHistoryOk returns a tuple with the History field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetHistory

`func (o *DynamicL1FeePairDetail) SetHistory(v []DynamicL1FeeHistoryEntry)`

SetHistory sets History field to given value.


### GetPhase

`func (o *DynamicL1FeePairDetail) GetPhase() string`

GetPhase returns the Phase field if non-nil, zero value otherwise.

### GetPhaseOk

`func (o *DynamicL1FeePairDetail) GetPhaseOk() (*string, bool)`

GetPhaseOk returns a tuple with the Phase field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetPhase

`func (o *DynamicL1FeePairDetail) SetPhase(v string)`

SetPhase sets Phase field to given value.

### HasPhase

`func (o *DynamicL1FeePairDetail) HasPhase() bool`

HasPhase returns a boolean if a field has been set.

### GetProbes

`func (o *DynamicL1FeePairDetail) GetProbes() []DynamicL1FeeProbe`

GetProbes returns the Probes field if non-nil, zero value otherwise.

### GetProbesOk

`func (o *DynamicL1FeePairDetail) GetProbesOk() (*[]DynamicL1FeeProbe, bool)`

GetProbesOk returns a tuple with the Probes field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetProbes

`func (o *DynamicL1FeePairDetail) SetProbes(v []DynamicL1FeeProbe)`

SetProbes sets Probes field to given value.

### HasProbes

`func (o *DynamicL1FeePairDetail) HasProbes() bool`

HasProbes returns a boolean if a field has been set.


[[Back to Model list]](../README.md#documentation-for-models) [[Back to API list]](../README.md#documentation-for-api-endpoints) [[Back to README]](../README.md)


