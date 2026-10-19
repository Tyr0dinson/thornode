# DynamicL1FeeCurrentEntry

## Properties

Name | Type | Description | Notes
------------ | ------------- | ------------- | -------------
**Thorname** | **string** |  | 
**Pair** | **string** | normalized asset pair encoded as \&quot;assetA|assetB\&quot; | 
**VolumeTor** | **string** | running TOR-denominated (1e8-precision USD value) volume for this (thorname, pair) accumulator in the current epoch  | 
**FeesTor** | **string** | running TOR-denominated (1e8-precision USD value) fees for this (thorname, pair) accumulator in the current epoch  | 
**Epoch** | **int32** | current epoch index this accumulator belongs to | 
**SwapCount** | **int32** | running count of user swaps for this accumulator, summed across any epochs it has rolled over. The epoch seals only if swap_count &gt;&#x3D; L1DynamicFeeMinSwaps and volume_tor &gt;&#x3D; L1DynamicFeeMinVolumeUSD * 1e8; if either falls short, the accumulator rolls into the next epoch  | 
**PoolVolumeTor** | **string** | running TOR-denominated total volume through the pair (for a global \&quot;*|*\&quot; accumulator, through the pairs the thorname traded), including the in-progress slice that is only folded in at the seal  | 
**OwnPoolVolumeTor** | Pointer to **string** | the thorname&#39;s own share of pool_volume_tor so far, counting every swap leg  | [optional] 

## Methods

### NewDynamicL1FeeCurrentEntry

`func NewDynamicL1FeeCurrentEntry(thorname string, pair string, volumeTor string, feesTor string, epoch int32, swapCount int32, poolVolumeTor string, ) *DynamicL1FeeCurrentEntry`

NewDynamicL1FeeCurrentEntry instantiates a new DynamicL1FeeCurrentEntry object
This constructor will assign default values to properties that have it defined,
and makes sure properties required by API are set, but the set of arguments
will change when the set of required properties is changed

### NewDynamicL1FeeCurrentEntryWithDefaults

`func NewDynamicL1FeeCurrentEntryWithDefaults() *DynamicL1FeeCurrentEntry`

NewDynamicL1FeeCurrentEntryWithDefaults instantiates a new DynamicL1FeeCurrentEntry object
This constructor will only assign default values to properties that have it defined,
but it doesn't guarantee that properties required by API are set

### GetThorname

`func (o *DynamicL1FeeCurrentEntry) GetThorname() string`

GetThorname returns the Thorname field if non-nil, zero value otherwise.

### GetThornameOk

`func (o *DynamicL1FeeCurrentEntry) GetThornameOk() (*string, bool)`

GetThornameOk returns a tuple with the Thorname field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetThorname

`func (o *DynamicL1FeeCurrentEntry) SetThorname(v string)`

SetThorname sets Thorname field to given value.


### GetPair

`func (o *DynamicL1FeeCurrentEntry) GetPair() string`

GetPair returns the Pair field if non-nil, zero value otherwise.

### GetPairOk

`func (o *DynamicL1FeeCurrentEntry) GetPairOk() (*string, bool)`

GetPairOk returns a tuple with the Pair field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetPair

`func (o *DynamicL1FeeCurrentEntry) SetPair(v string)`

SetPair sets Pair field to given value.


### GetVolumeTor

`func (o *DynamicL1FeeCurrentEntry) GetVolumeTor() string`

GetVolumeTor returns the VolumeTor field if non-nil, zero value otherwise.

### GetVolumeTorOk

`func (o *DynamicL1FeeCurrentEntry) GetVolumeTorOk() (*string, bool)`

GetVolumeTorOk returns a tuple with the VolumeTor field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetVolumeTor

`func (o *DynamicL1FeeCurrentEntry) SetVolumeTor(v string)`

SetVolumeTor sets VolumeTor field to given value.


### GetFeesTor

`func (o *DynamicL1FeeCurrentEntry) GetFeesTor() string`

GetFeesTor returns the FeesTor field if non-nil, zero value otherwise.

### GetFeesTorOk

`func (o *DynamicL1FeeCurrentEntry) GetFeesTorOk() (*string, bool)`

GetFeesTorOk returns a tuple with the FeesTor field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetFeesTor

`func (o *DynamicL1FeeCurrentEntry) SetFeesTor(v string)`

SetFeesTor sets FeesTor field to given value.


### GetEpoch

`func (o *DynamicL1FeeCurrentEntry) GetEpoch() int32`

GetEpoch returns the Epoch field if non-nil, zero value otherwise.

### GetEpochOk

`func (o *DynamicL1FeeCurrentEntry) GetEpochOk() (*int32, bool)`

GetEpochOk returns a tuple with the Epoch field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetEpoch

`func (o *DynamicL1FeeCurrentEntry) SetEpoch(v int32)`

SetEpoch sets Epoch field to given value.


### GetSwapCount

`func (o *DynamicL1FeeCurrentEntry) GetSwapCount() int32`

GetSwapCount returns the SwapCount field if non-nil, zero value otherwise.

### GetSwapCountOk

`func (o *DynamicL1FeeCurrentEntry) GetSwapCountOk() (*int32, bool)`

GetSwapCountOk returns a tuple with the SwapCount field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetSwapCount

`func (o *DynamicL1FeeCurrentEntry) SetSwapCount(v int32)`

SetSwapCount sets SwapCount field to given value.


### GetPoolVolumeTor

`func (o *DynamicL1FeeCurrentEntry) GetPoolVolumeTor() string`

GetPoolVolumeTor returns the PoolVolumeTor field if non-nil, zero value otherwise.

### GetPoolVolumeTorOk

`func (o *DynamicL1FeeCurrentEntry) GetPoolVolumeTorOk() (*string, bool)`

GetPoolVolumeTorOk returns a tuple with the PoolVolumeTor field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetPoolVolumeTor

`func (o *DynamicL1FeeCurrentEntry) SetPoolVolumeTor(v string)`

SetPoolVolumeTor sets PoolVolumeTor field to given value.


### GetOwnPoolVolumeTor

`func (o *DynamicL1FeeCurrentEntry) GetOwnPoolVolumeTor() string`

GetOwnPoolVolumeTor returns the OwnPoolVolumeTor field if non-nil, zero value otherwise.

### GetOwnPoolVolumeTorOk

`func (o *DynamicL1FeeCurrentEntry) GetOwnPoolVolumeTorOk() (*string, bool)`

GetOwnPoolVolumeTorOk returns a tuple with the OwnPoolVolumeTor field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetOwnPoolVolumeTor

`func (o *DynamicL1FeeCurrentEntry) SetOwnPoolVolumeTor(v string)`

SetOwnPoolVolumeTor sets OwnPoolVolumeTor field to given value.

### HasOwnPoolVolumeTor

`func (o *DynamicL1FeeCurrentEntry) HasOwnPoolVolumeTor() bool`

HasOwnPoolVolumeTor returns a boolean if a field has been set.


[[Back to Model list]](../README.md#documentation-for-models) [[Back to API list]](../README.md#documentation-for-api-endpoints) [[Back to README]](../README.md)


