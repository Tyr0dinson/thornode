# Pool

## Properties

Name | Type | Description | Notes
------------ | ------------- | ------------- | -------------
**BalanceRune** | **string** |  | 
**BalanceAsset** | **string** |  | 
**Asset** | **string** |  | 
**LPUnits** | **string** | the total pool liquidity provider units | 
**PoolUnits** | **string** | the total pool units, this is the sum of LP and synth units | 
**Status** | **string** |  | 
**Decimals** | Pointer to **int64** |  | [optional] 
**SynthUnits** | **string** | the total synth units in the pool | 
**SynthSupply** | **string** | the total supply of synths for the asset | 
**PendingInboundRune** | **string** |  | 
**PendingInboundAsset** | **string** |  | 

## Methods

### NewPool

`func NewPool(balanceRune string, balanceAsset string, asset string, lPUnits string, poolUnits string, status string, synthUnits string, synthSupply string, pendingInboundRune string, pendingInboundAsset string, ) *Pool`

NewPool instantiates a new Pool object
This constructor will assign default values to properties that have it defined,
and makes sure properties required by API are set, but the set of arguments
will change when the set of required properties is changed

### NewPoolWithDefaults

`func NewPoolWithDefaults() *Pool`

NewPoolWithDefaults instantiates a new Pool object
This constructor will only assign default values to properties that have it defined,
but it doesn't guarantee that properties required by API are set

### GetBalanceRune

`func (o *Pool) GetBalanceRune() string`

GetBalanceRune returns the BalanceRune field if non-nil, zero value otherwise.

### GetBalanceRuneOk

`func (o *Pool) GetBalanceRuneOk() (*string, bool)`

GetBalanceRuneOk returns a tuple with the BalanceRune field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetBalanceRune

`func (o *Pool) SetBalanceRune(v string)`

SetBalanceRune sets BalanceRune field to given value.


### GetBalanceAsset

`func (o *Pool) GetBalanceAsset() string`

GetBalanceAsset returns the BalanceAsset field if non-nil, zero value otherwise.

### GetBalanceAssetOk

`func (o *Pool) GetBalanceAssetOk() (*string, bool)`

GetBalanceAssetOk returns a tuple with the BalanceAsset field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetBalanceAsset

`func (o *Pool) SetBalanceAsset(v string)`

SetBalanceAsset sets BalanceAsset field to given value.


### GetAsset

`func (o *Pool) GetAsset() string`

GetAsset returns the Asset field if non-nil, zero value otherwise.

### GetAssetOk

`func (o *Pool) GetAssetOk() (*string, bool)`

GetAssetOk returns a tuple with the Asset field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetAsset

`func (o *Pool) SetAsset(v string)`

SetAsset sets Asset field to given value.


### GetLPUnits

`func (o *Pool) GetLPUnits() string`

GetLPUnits returns the LPUnits field if non-nil, zero value otherwise.

### GetLPUnitsOk

`func (o *Pool) GetLPUnitsOk() (*string, bool)`

GetLPUnitsOk returns a tuple with the LPUnits field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetLPUnits

`func (o *Pool) SetLPUnits(v string)`

SetLPUnits sets LPUnits field to given value.


### GetPoolUnits

`func (o *Pool) GetPoolUnits() string`

GetPoolUnits returns the PoolUnits field if non-nil, zero value otherwise.

### GetPoolUnitsOk

`func (o *Pool) GetPoolUnitsOk() (*string, bool)`

GetPoolUnitsOk returns a tuple with the PoolUnits field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetPoolUnits

`func (o *Pool) SetPoolUnits(v string)`

SetPoolUnits sets PoolUnits field to given value.


### GetStatus

`func (o *Pool) GetStatus() string`

GetStatus returns the Status field if non-nil, zero value otherwise.

### GetStatusOk

`func (o *Pool) GetStatusOk() (*string, bool)`

GetStatusOk returns a tuple with the Status field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetStatus

`func (o *Pool) SetStatus(v string)`

SetStatus sets Status field to given value.


### GetDecimals

`func (o *Pool) GetDecimals() int64`

GetDecimals returns the Decimals field if non-nil, zero value otherwise.

### GetDecimalsOk

`func (o *Pool) GetDecimalsOk() (*int64, bool)`

GetDecimalsOk returns a tuple with the Decimals field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetDecimals

`func (o *Pool) SetDecimals(v int64)`

SetDecimals sets Decimals field to given value.

### HasDecimals

`func (o *Pool) HasDecimals() bool`

HasDecimals returns a boolean if a field has been set.

### GetSynthUnits

`func (o *Pool) GetSynthUnits() string`

GetSynthUnits returns the SynthUnits field if non-nil, zero value otherwise.

### GetSynthUnitsOk

`func (o *Pool) GetSynthUnitsOk() (*string, bool)`

GetSynthUnitsOk returns a tuple with the SynthUnits field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetSynthUnits

`func (o *Pool) SetSynthUnits(v string)`

SetSynthUnits sets SynthUnits field to given value.


### GetSynthSupply

`func (o *Pool) GetSynthSupply() string`

GetSynthSupply returns the SynthSupply field if non-nil, zero value otherwise.

### GetSynthSupplyOk

`func (o *Pool) GetSynthSupplyOk() (*string, bool)`

GetSynthSupplyOk returns a tuple with the SynthSupply field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetSynthSupply

`func (o *Pool) SetSynthSupply(v string)`

SetSynthSupply sets SynthSupply field to given value.


### GetPendingInboundRune

`func (o *Pool) GetPendingInboundRune() string`

GetPendingInboundRune returns the PendingInboundRune field if non-nil, zero value otherwise.

### GetPendingInboundRuneOk

`func (o *Pool) GetPendingInboundRuneOk() (*string, bool)`

GetPendingInboundRuneOk returns a tuple with the PendingInboundRune field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetPendingInboundRune

`func (o *Pool) SetPendingInboundRune(v string)`

SetPendingInboundRune sets PendingInboundRune field to given value.


### GetPendingInboundAsset

`func (o *Pool) GetPendingInboundAsset() string`

GetPendingInboundAsset returns the PendingInboundAsset field if non-nil, zero value otherwise.

### GetPendingInboundAssetOk

`func (o *Pool) GetPendingInboundAssetOk() (*string, bool)`

GetPendingInboundAssetOk returns a tuple with the PendingInboundAsset field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetPendingInboundAsset

`func (o *Pool) SetPendingInboundAsset(v string)`

SetPendingInboundAsset sets PendingInboundAsset field to given value.



[[Back to Model list]](../README.md#documentation-for-models) [[Back to API list]](../README.md#documentation-for-api-endpoints) [[Back to README]](../README.md)


