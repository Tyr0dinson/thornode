# DynamicL1FeeHistoryEntry

## Properties

Name | Type | Description | Notes
------------ | ------------- | ------------- | -------------
**Epoch** | **int32** |  | 
**VolumeTor** | **string** | TOR-denominated (1e8-precision USD value) swap volume in this epoch  | 
**FeesTor** | **string** | TOR-denominated (1e8-precision USD value) fees accrued in this epoch  | 
**BpsAtClose** | **int32** | dynamic_bps value at the close of the epoch | 
**SwapCount** | **int32** | user swaps counted in this epoch; a streaming swap counts once, not once per sub-swap. Epochs below L1DynamicFeeMinSwaps swaps or L1DynamicFeeMinVolumeUSD of volume do not seal and roll into the next one, so this may span several epochs  | 
**PoolVolumeTor** | **string** | TOR-denominated (1e8-precision USD value) total volume through the pair (for a global \&quot;*|*\&quot; record, through the pairs the thorname traded) over the same span, regardless of affiliate  | 
**OwnPoolVolumeTor** | Pointer to **string** | the thorname&#39;s own share of pool_volume_tor, counting every swap leg. The signal is fees_tor / (pool_volume_tor - own_pool_volume_tor): fee revenue against other swappers&#39; volume. An epoch whose other flow is under L1DynamicFeeMinOtherFlowBPS of the pool is recorded but not used as a measurement  | [optional] 
**Blocks** | Pointer to **int64** | length of the span this entry covers, in blocks. A span below the liquidity gate rolls into the next epoch, up to L1DynamicFeeMaxEpochBlocks, after which it is sealed regardless  | [optional] 

## Methods

### NewDynamicL1FeeHistoryEntry

`func NewDynamicL1FeeHistoryEntry(epoch int32, volumeTor string, feesTor string, bpsAtClose int32, swapCount int32, poolVolumeTor string, ) *DynamicL1FeeHistoryEntry`

NewDynamicL1FeeHistoryEntry instantiates a new DynamicL1FeeHistoryEntry object
This constructor will assign default values to properties that have it defined,
and makes sure properties required by API are set, but the set of arguments
will change when the set of required properties is changed

### NewDynamicL1FeeHistoryEntryWithDefaults

`func NewDynamicL1FeeHistoryEntryWithDefaults() *DynamicL1FeeHistoryEntry`

NewDynamicL1FeeHistoryEntryWithDefaults instantiates a new DynamicL1FeeHistoryEntry object
This constructor will only assign default values to properties that have it defined,
but it doesn't guarantee that properties required by API are set

### GetEpoch

`func (o *DynamicL1FeeHistoryEntry) GetEpoch() int32`

GetEpoch returns the Epoch field if non-nil, zero value otherwise.

### GetEpochOk

`func (o *DynamicL1FeeHistoryEntry) GetEpochOk() (*int32, bool)`

GetEpochOk returns a tuple with the Epoch field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetEpoch

`func (o *DynamicL1FeeHistoryEntry) SetEpoch(v int32)`

SetEpoch sets Epoch field to given value.


### GetVolumeTor

`func (o *DynamicL1FeeHistoryEntry) GetVolumeTor() string`

GetVolumeTor returns the VolumeTor field if non-nil, zero value otherwise.

### GetVolumeTorOk

`func (o *DynamicL1FeeHistoryEntry) GetVolumeTorOk() (*string, bool)`

GetVolumeTorOk returns a tuple with the VolumeTor field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetVolumeTor

`func (o *DynamicL1FeeHistoryEntry) SetVolumeTor(v string)`

SetVolumeTor sets VolumeTor field to given value.


### GetFeesTor

`func (o *DynamicL1FeeHistoryEntry) GetFeesTor() string`

GetFeesTor returns the FeesTor field if non-nil, zero value otherwise.

### GetFeesTorOk

`func (o *DynamicL1FeeHistoryEntry) GetFeesTorOk() (*string, bool)`

GetFeesTorOk returns a tuple with the FeesTor field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetFeesTor

`func (o *DynamicL1FeeHistoryEntry) SetFeesTor(v string)`

SetFeesTor sets FeesTor field to given value.


### GetBpsAtClose

`func (o *DynamicL1FeeHistoryEntry) GetBpsAtClose() int32`

GetBpsAtClose returns the BpsAtClose field if non-nil, zero value otherwise.

### GetBpsAtCloseOk

`func (o *DynamicL1FeeHistoryEntry) GetBpsAtCloseOk() (*int32, bool)`

GetBpsAtCloseOk returns a tuple with the BpsAtClose field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetBpsAtClose

`func (o *DynamicL1FeeHistoryEntry) SetBpsAtClose(v int32)`

SetBpsAtClose sets BpsAtClose field to given value.


### GetSwapCount

`func (o *DynamicL1FeeHistoryEntry) GetSwapCount() int32`

GetSwapCount returns the SwapCount field if non-nil, zero value otherwise.

### GetSwapCountOk

`func (o *DynamicL1FeeHistoryEntry) GetSwapCountOk() (*int32, bool)`

GetSwapCountOk returns a tuple with the SwapCount field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetSwapCount

`func (o *DynamicL1FeeHistoryEntry) SetSwapCount(v int32)`

SetSwapCount sets SwapCount field to given value.


### GetPoolVolumeTor

`func (o *DynamicL1FeeHistoryEntry) GetPoolVolumeTor() string`

GetPoolVolumeTor returns the PoolVolumeTor field if non-nil, zero value otherwise.

### GetPoolVolumeTorOk

`func (o *DynamicL1FeeHistoryEntry) GetPoolVolumeTorOk() (*string, bool)`

GetPoolVolumeTorOk returns a tuple with the PoolVolumeTor field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetPoolVolumeTor

`func (o *DynamicL1FeeHistoryEntry) SetPoolVolumeTor(v string)`

SetPoolVolumeTor sets PoolVolumeTor field to given value.


### GetOwnPoolVolumeTor

`func (o *DynamicL1FeeHistoryEntry) GetOwnPoolVolumeTor() string`

GetOwnPoolVolumeTor returns the OwnPoolVolumeTor field if non-nil, zero value otherwise.

### GetOwnPoolVolumeTorOk

`func (o *DynamicL1FeeHistoryEntry) GetOwnPoolVolumeTorOk() (*string, bool)`

GetOwnPoolVolumeTorOk returns a tuple with the OwnPoolVolumeTor field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetOwnPoolVolumeTor

`func (o *DynamicL1FeeHistoryEntry) SetOwnPoolVolumeTor(v string)`

SetOwnPoolVolumeTor sets OwnPoolVolumeTor field to given value.

### HasOwnPoolVolumeTor

`func (o *DynamicL1FeeHistoryEntry) HasOwnPoolVolumeTor() bool`

HasOwnPoolVolumeTor returns a boolean if a field has been set.

### GetBlocks

`func (o *DynamicL1FeeHistoryEntry) GetBlocks() int64`

GetBlocks returns the Blocks field if non-nil, zero value otherwise.

### GetBlocksOk

`func (o *DynamicL1FeeHistoryEntry) GetBlocksOk() (*int64, bool)`

GetBlocksOk returns a tuple with the Blocks field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetBlocks

`func (o *DynamicL1FeeHistoryEntry) SetBlocks(v int64)`

SetBlocks sets Blocks field to given value.

### HasBlocks

`func (o *DynamicL1FeeHistoryEntry) HasBlocks() bool`

HasBlocks returns a boolean if a field has been set.


[[Back to Model list]](../README.md#documentation-for-models) [[Back to API list]](../README.md#documentation-for-api-endpoints) [[Back to README]](../README.md)


