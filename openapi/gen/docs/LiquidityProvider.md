# LiquidityProvider

## Properties

Name | Type | Description | Notes
------------ | ------------- | ------------- | -------------
**Asset** | **string** |  | 
**RuneAddress** | Pointer to **string** |  | [optional] 
**AssetAddress** | Pointer to **string** |  | [optional] 
**LastAddHeight** | Pointer to **int64** |  | [optional] 
**LastWithdrawHeight** | Pointer to **int64** |  | [optional] 
**Units** | **string** |  | 
**PendingRune** | **string** |  | 
**PendingAsset** | **string** |  | 
**PendingTxId** | Pointer to **string** |  | [optional] 
**RuneDepositValue** | **string** |  | 
**AssetDepositValue** | **string** |  | 

## Methods

### NewLiquidityProvider

`func NewLiquidityProvider(asset string, units string, pendingRune string, pendingAsset string, runeDepositValue string, assetDepositValue string, ) *LiquidityProvider`

NewLiquidityProvider instantiates a new LiquidityProvider object
This constructor will assign default values to properties that have it defined,
and makes sure properties required by API are set, but the set of arguments
will change when the set of required properties is changed

### NewLiquidityProviderWithDefaults

`func NewLiquidityProviderWithDefaults() *LiquidityProvider`

NewLiquidityProviderWithDefaults instantiates a new LiquidityProvider object
This constructor will only assign default values to properties that have it defined,
but it doesn't guarantee that properties required by API are set

### GetAsset

`func (o *LiquidityProvider) GetAsset() string`

GetAsset returns the Asset field if non-nil, zero value otherwise.

### GetAssetOk

`func (o *LiquidityProvider) GetAssetOk() (*string, bool)`

GetAssetOk returns a tuple with the Asset field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetAsset

`func (o *LiquidityProvider) SetAsset(v string)`

SetAsset sets Asset field to given value.


### GetRuneAddress

`func (o *LiquidityProvider) GetRuneAddress() string`

GetRuneAddress returns the RuneAddress field if non-nil, zero value otherwise.

### GetRuneAddressOk

`func (o *LiquidityProvider) GetRuneAddressOk() (*string, bool)`

GetRuneAddressOk returns a tuple with the RuneAddress field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetRuneAddress

`func (o *LiquidityProvider) SetRuneAddress(v string)`

SetRuneAddress sets RuneAddress field to given value.

### HasRuneAddress

`func (o *LiquidityProvider) HasRuneAddress() bool`

HasRuneAddress returns a boolean if a field has been set.

### GetAssetAddress

`func (o *LiquidityProvider) GetAssetAddress() string`

GetAssetAddress returns the AssetAddress field if non-nil, zero value otherwise.

### GetAssetAddressOk

`func (o *LiquidityProvider) GetAssetAddressOk() (*string, bool)`

GetAssetAddressOk returns a tuple with the AssetAddress field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetAssetAddress

`func (o *LiquidityProvider) SetAssetAddress(v string)`

SetAssetAddress sets AssetAddress field to given value.

### HasAssetAddress

`func (o *LiquidityProvider) HasAssetAddress() bool`

HasAssetAddress returns a boolean if a field has been set.

### GetLastAddHeight

`func (o *LiquidityProvider) GetLastAddHeight() int64`

GetLastAddHeight returns the LastAddHeight field if non-nil, zero value otherwise.

### GetLastAddHeightOk

`func (o *LiquidityProvider) GetLastAddHeightOk() (*int64, bool)`

GetLastAddHeightOk returns a tuple with the LastAddHeight field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetLastAddHeight

`func (o *LiquidityProvider) SetLastAddHeight(v int64)`

SetLastAddHeight sets LastAddHeight field to given value.

### HasLastAddHeight

`func (o *LiquidityProvider) HasLastAddHeight() bool`

HasLastAddHeight returns a boolean if a field has been set.

### GetLastWithdrawHeight

`func (o *LiquidityProvider) GetLastWithdrawHeight() int64`

GetLastWithdrawHeight returns the LastWithdrawHeight field if non-nil, zero value otherwise.

### GetLastWithdrawHeightOk

`func (o *LiquidityProvider) GetLastWithdrawHeightOk() (*int64, bool)`

GetLastWithdrawHeightOk returns a tuple with the LastWithdrawHeight field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetLastWithdrawHeight

`func (o *LiquidityProvider) SetLastWithdrawHeight(v int64)`

SetLastWithdrawHeight sets LastWithdrawHeight field to given value.

### HasLastWithdrawHeight

`func (o *LiquidityProvider) HasLastWithdrawHeight() bool`

HasLastWithdrawHeight returns a boolean if a field has been set.

### GetUnits

`func (o *LiquidityProvider) GetUnits() string`

GetUnits returns the Units field if non-nil, zero value otherwise.

### GetUnitsOk

`func (o *LiquidityProvider) GetUnitsOk() (*string, bool)`

GetUnitsOk returns a tuple with the Units field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetUnits

`func (o *LiquidityProvider) SetUnits(v string)`

SetUnits sets Units field to given value.


### GetPendingRune

`func (o *LiquidityProvider) GetPendingRune() string`

GetPendingRune returns the PendingRune field if non-nil, zero value otherwise.

### GetPendingRuneOk

`func (o *LiquidityProvider) GetPendingRuneOk() (*string, bool)`

GetPendingRuneOk returns a tuple with the PendingRune field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetPendingRune

`func (o *LiquidityProvider) SetPendingRune(v string)`

SetPendingRune sets PendingRune field to given value.


### GetPendingAsset

`func (o *LiquidityProvider) GetPendingAsset() string`

GetPendingAsset returns the PendingAsset field if non-nil, zero value otherwise.

### GetPendingAssetOk

`func (o *LiquidityProvider) GetPendingAssetOk() (*string, bool)`

GetPendingAssetOk returns a tuple with the PendingAsset field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetPendingAsset

`func (o *LiquidityProvider) SetPendingAsset(v string)`

SetPendingAsset sets PendingAsset field to given value.


### GetPendingTxId

`func (o *LiquidityProvider) GetPendingTxId() string`

GetPendingTxId returns the PendingTxId field if non-nil, zero value otherwise.

### GetPendingTxIdOk

`func (o *LiquidityProvider) GetPendingTxIdOk() (*string, bool)`

GetPendingTxIdOk returns a tuple with the PendingTxId field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetPendingTxId

`func (o *LiquidityProvider) SetPendingTxId(v string)`

SetPendingTxId sets PendingTxId field to given value.

### HasPendingTxId

`func (o *LiquidityProvider) HasPendingTxId() bool`

HasPendingTxId returns a boolean if a field has been set.

### GetRuneDepositValue

`func (o *LiquidityProvider) GetRuneDepositValue() string`

GetRuneDepositValue returns the RuneDepositValue field if non-nil, zero value otherwise.

### GetRuneDepositValueOk

`func (o *LiquidityProvider) GetRuneDepositValueOk() (*string, bool)`

GetRuneDepositValueOk returns a tuple with the RuneDepositValue field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetRuneDepositValue

`func (o *LiquidityProvider) SetRuneDepositValue(v string)`

SetRuneDepositValue sets RuneDepositValue field to given value.


### GetAssetDepositValue

`func (o *LiquidityProvider) GetAssetDepositValue() string`

GetAssetDepositValue returns the AssetDepositValue field if non-nil, zero value otherwise.

### GetAssetDepositValueOk

`func (o *LiquidityProvider) GetAssetDepositValueOk() (*string, bool)`

GetAssetDepositValueOk returns a tuple with the AssetDepositValue field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetAssetDepositValue

`func (o *LiquidityProvider) SetAssetDepositValue(v string)`

SetAssetDepositValue sets AssetDepositValue field to given value.



[[Back to Model list]](../README.md#documentation-for-models) [[Back to API list]](../README.md#documentation-for-api-endpoints) [[Back to README]](../README.md)


