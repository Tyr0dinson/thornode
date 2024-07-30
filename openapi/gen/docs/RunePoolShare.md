# RunePoolShare

## Properties

Name | Type | Description | Notes
------------ | ------------- | ------------- | -------------
**Asset** | **string** |  | 
**RuneValue** | **string** | POL position value in rune | 
**AssetValue** | **string** | POL position value in asset | 
**TorValue** | **string** | POL position value in tor | 
**OwnershipBps** | **int64** | POL share of the pool in basis points | 

## Methods

### NewRunePoolShare

`func NewRunePoolShare(asset string, runeValue string, assetValue string, torValue string, ownershipBps int64, ) *RunePoolShare`

NewRunePoolShare instantiates a new RunePoolShare object
This constructor will assign default values to properties that have it defined,
and makes sure properties required by API are set, but the set of arguments
will change when the set of required properties is changed

### NewRunePoolShareWithDefaults

`func NewRunePoolShareWithDefaults() *RunePoolShare`

NewRunePoolShareWithDefaults instantiates a new RunePoolShare object
This constructor will only assign default values to properties that have it defined,
but it doesn't guarantee that properties required by API are set

### GetAsset

`func (o *RunePoolShare) GetAsset() string`

GetAsset returns the Asset field if non-nil, zero value otherwise.

### GetAssetOk

`func (o *RunePoolShare) GetAssetOk() (*string, bool)`

GetAssetOk returns a tuple with the Asset field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetAsset

`func (o *RunePoolShare) SetAsset(v string)`

SetAsset sets Asset field to given value.


### GetRuneValue

`func (o *RunePoolShare) GetRuneValue() string`

GetRuneValue returns the RuneValue field if non-nil, zero value otherwise.

### GetRuneValueOk

`func (o *RunePoolShare) GetRuneValueOk() (*string, bool)`

GetRuneValueOk returns a tuple with the RuneValue field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetRuneValue

`func (o *RunePoolShare) SetRuneValue(v string)`

SetRuneValue sets RuneValue field to given value.


### GetAssetValue

`func (o *RunePoolShare) GetAssetValue() string`

GetAssetValue returns the AssetValue field if non-nil, zero value otherwise.

### GetAssetValueOk

`func (o *RunePoolShare) GetAssetValueOk() (*string, bool)`

GetAssetValueOk returns a tuple with the AssetValue field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetAssetValue

`func (o *RunePoolShare) SetAssetValue(v string)`

SetAssetValue sets AssetValue field to given value.


### GetTorValue

`func (o *RunePoolShare) GetTorValue() string`

GetTorValue returns the TorValue field if non-nil, zero value otherwise.

### GetTorValueOk

`func (o *RunePoolShare) GetTorValueOk() (*string, bool)`

GetTorValueOk returns a tuple with the TorValue field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetTorValue

`func (o *RunePoolShare) SetTorValue(v string)`

SetTorValue sets TorValue field to given value.


### GetOwnershipBps

`func (o *RunePoolShare) GetOwnershipBps() int64`

GetOwnershipBps returns the OwnershipBps field if non-nil, zero value otherwise.

### GetOwnershipBpsOk

`func (o *RunePoolShare) GetOwnershipBpsOk() (*int64, bool)`

GetOwnershipBpsOk returns a tuple with the OwnershipBps field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetOwnershipBps

`func (o *RunePoolShare) SetOwnershipBps(v int64)`

SetOwnershipBps sets OwnershipBps field to given value.



[[Back to Model list]](../README.md#documentation-for-models) [[Back to API list]](../README.md#documentation-for-api-endpoints) [[Back to README]](../README.md)


