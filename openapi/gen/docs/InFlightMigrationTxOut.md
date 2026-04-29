# InFlightMigrationTxOut

## Properties

Name | Type | Description | Notes
------------ | ------------- | ------------- | -------------
**InHash** | Pointer to **string** |  | [optional] 
**OutHash** | Pointer to **string** |  | [optional] 
**Chain** | **string** |  | 
**ToAddress** | **string** |  | 
**VaultPubKey** | Pointer to **string** |  | [optional] 
**VaultPubKeyEddsa** | Pointer to **string** |  | [optional] 
**Coin** | [**InFlightMigrationCoin**](InFlightMigrationCoin.md) |  | 
**MaxGas** | [**[]InFlightMigrationCoin**](InFlightMigrationCoin.md) |  | 
**GasRate** | Pointer to **string** |  | [optional] 
**Memo** | Pointer to **string** |  | [optional] 
**OriginalMemo** | Pointer to **string** |  | [optional] 
**Aggregator** | Pointer to **string** | whitelisted DEX Aggregator contract address | [optional] 
**AggregatorTargetAsset** | Pointer to **string** | target asset for the aggregator contract to attempt a swap to | [optional] 
**AggregatorTargetLimit** | Pointer to **string** | the minimum number of tokens the swapper wants to receive of the output asset | [optional] 
**CloutSpent** | Pointer to **string** | clout spent in RUNE for the outbound | [optional] 

## Methods

### NewInFlightMigrationTxOut

`func NewInFlightMigrationTxOut(chain string, toAddress string, coin InFlightMigrationCoin, maxGas []InFlightMigrationCoin, ) *InFlightMigrationTxOut`

NewInFlightMigrationTxOut instantiates a new InFlightMigrationTxOut object
This constructor will assign default values to properties that have it defined,
and makes sure properties required by API are set, but the set of arguments
will change when the set of required properties is changed

### NewInFlightMigrationTxOutWithDefaults

`func NewInFlightMigrationTxOutWithDefaults() *InFlightMigrationTxOut`

NewInFlightMigrationTxOutWithDefaults instantiates a new InFlightMigrationTxOut object
This constructor will only assign default values to properties that have it defined,
but it doesn't guarantee that properties required by API are set

### GetInHash

`func (o *InFlightMigrationTxOut) GetInHash() string`

GetInHash returns the InHash field if non-nil, zero value otherwise.

### GetInHashOk

`func (o *InFlightMigrationTxOut) GetInHashOk() (*string, bool)`

GetInHashOk returns a tuple with the InHash field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetInHash

`func (o *InFlightMigrationTxOut) SetInHash(v string)`

SetInHash sets InHash field to given value.

### HasInHash

`func (o *InFlightMigrationTxOut) HasInHash() bool`

HasInHash returns a boolean if a field has been set.

### GetOutHash

`func (o *InFlightMigrationTxOut) GetOutHash() string`

GetOutHash returns the OutHash field if non-nil, zero value otherwise.

### GetOutHashOk

`func (o *InFlightMigrationTxOut) GetOutHashOk() (*string, bool)`

GetOutHashOk returns a tuple with the OutHash field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetOutHash

`func (o *InFlightMigrationTxOut) SetOutHash(v string)`

SetOutHash sets OutHash field to given value.

### HasOutHash

`func (o *InFlightMigrationTxOut) HasOutHash() bool`

HasOutHash returns a boolean if a field has been set.

### GetChain

`func (o *InFlightMigrationTxOut) GetChain() string`

GetChain returns the Chain field if non-nil, zero value otherwise.

### GetChainOk

`func (o *InFlightMigrationTxOut) GetChainOk() (*string, bool)`

GetChainOk returns a tuple with the Chain field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetChain

`func (o *InFlightMigrationTxOut) SetChain(v string)`

SetChain sets Chain field to given value.


### GetToAddress

`func (o *InFlightMigrationTxOut) GetToAddress() string`

GetToAddress returns the ToAddress field if non-nil, zero value otherwise.

### GetToAddressOk

`func (o *InFlightMigrationTxOut) GetToAddressOk() (*string, bool)`

GetToAddressOk returns a tuple with the ToAddress field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetToAddress

`func (o *InFlightMigrationTxOut) SetToAddress(v string)`

SetToAddress sets ToAddress field to given value.


### GetVaultPubKey

`func (o *InFlightMigrationTxOut) GetVaultPubKey() string`

GetVaultPubKey returns the VaultPubKey field if non-nil, zero value otherwise.

### GetVaultPubKeyOk

`func (o *InFlightMigrationTxOut) GetVaultPubKeyOk() (*string, bool)`

GetVaultPubKeyOk returns a tuple with the VaultPubKey field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetVaultPubKey

`func (o *InFlightMigrationTxOut) SetVaultPubKey(v string)`

SetVaultPubKey sets VaultPubKey field to given value.

### HasVaultPubKey

`func (o *InFlightMigrationTxOut) HasVaultPubKey() bool`

HasVaultPubKey returns a boolean if a field has been set.

### GetVaultPubKeyEddsa

`func (o *InFlightMigrationTxOut) GetVaultPubKeyEddsa() string`

GetVaultPubKeyEddsa returns the VaultPubKeyEddsa field if non-nil, zero value otherwise.

### GetVaultPubKeyEddsaOk

`func (o *InFlightMigrationTxOut) GetVaultPubKeyEddsaOk() (*string, bool)`

GetVaultPubKeyEddsaOk returns a tuple with the VaultPubKeyEddsa field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetVaultPubKeyEddsa

`func (o *InFlightMigrationTxOut) SetVaultPubKeyEddsa(v string)`

SetVaultPubKeyEddsa sets VaultPubKeyEddsa field to given value.

### HasVaultPubKeyEddsa

`func (o *InFlightMigrationTxOut) HasVaultPubKeyEddsa() bool`

HasVaultPubKeyEddsa returns a boolean if a field has been set.

### GetCoin

`func (o *InFlightMigrationTxOut) GetCoin() InFlightMigrationCoin`

GetCoin returns the Coin field if non-nil, zero value otherwise.

### GetCoinOk

`func (o *InFlightMigrationTxOut) GetCoinOk() (*InFlightMigrationCoin, bool)`

GetCoinOk returns a tuple with the Coin field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetCoin

`func (o *InFlightMigrationTxOut) SetCoin(v InFlightMigrationCoin)`

SetCoin sets Coin field to given value.


### GetMaxGas

`func (o *InFlightMigrationTxOut) GetMaxGas() []InFlightMigrationCoin`

GetMaxGas returns the MaxGas field if non-nil, zero value otherwise.

### GetMaxGasOk

`func (o *InFlightMigrationTxOut) GetMaxGasOk() (*[]InFlightMigrationCoin, bool)`

GetMaxGasOk returns a tuple with the MaxGas field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetMaxGas

`func (o *InFlightMigrationTxOut) SetMaxGas(v []InFlightMigrationCoin)`

SetMaxGas sets MaxGas field to given value.


### GetGasRate

`func (o *InFlightMigrationTxOut) GetGasRate() string`

GetGasRate returns the GasRate field if non-nil, zero value otherwise.

### GetGasRateOk

`func (o *InFlightMigrationTxOut) GetGasRateOk() (*string, bool)`

GetGasRateOk returns a tuple with the GasRate field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetGasRate

`func (o *InFlightMigrationTxOut) SetGasRate(v string)`

SetGasRate sets GasRate field to given value.

### HasGasRate

`func (o *InFlightMigrationTxOut) HasGasRate() bool`

HasGasRate returns a boolean if a field has been set.

### GetMemo

`func (o *InFlightMigrationTxOut) GetMemo() string`

GetMemo returns the Memo field if non-nil, zero value otherwise.

### GetMemoOk

`func (o *InFlightMigrationTxOut) GetMemoOk() (*string, bool)`

GetMemoOk returns a tuple with the Memo field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetMemo

`func (o *InFlightMigrationTxOut) SetMemo(v string)`

SetMemo sets Memo field to given value.

### HasMemo

`func (o *InFlightMigrationTxOut) HasMemo() bool`

HasMemo returns a boolean if a field has been set.

### GetOriginalMemo

`func (o *InFlightMigrationTxOut) GetOriginalMemo() string`

GetOriginalMemo returns the OriginalMemo field if non-nil, zero value otherwise.

### GetOriginalMemoOk

`func (o *InFlightMigrationTxOut) GetOriginalMemoOk() (*string, bool)`

GetOriginalMemoOk returns a tuple with the OriginalMemo field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetOriginalMemo

`func (o *InFlightMigrationTxOut) SetOriginalMemo(v string)`

SetOriginalMemo sets OriginalMemo field to given value.

### HasOriginalMemo

`func (o *InFlightMigrationTxOut) HasOriginalMemo() bool`

HasOriginalMemo returns a boolean if a field has been set.

### GetAggregator

`func (o *InFlightMigrationTxOut) GetAggregator() string`

GetAggregator returns the Aggregator field if non-nil, zero value otherwise.

### GetAggregatorOk

`func (o *InFlightMigrationTxOut) GetAggregatorOk() (*string, bool)`

GetAggregatorOk returns a tuple with the Aggregator field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetAggregator

`func (o *InFlightMigrationTxOut) SetAggregator(v string)`

SetAggregator sets Aggregator field to given value.

### HasAggregator

`func (o *InFlightMigrationTxOut) HasAggregator() bool`

HasAggregator returns a boolean if a field has been set.

### GetAggregatorTargetAsset

`func (o *InFlightMigrationTxOut) GetAggregatorTargetAsset() string`

GetAggregatorTargetAsset returns the AggregatorTargetAsset field if non-nil, zero value otherwise.

### GetAggregatorTargetAssetOk

`func (o *InFlightMigrationTxOut) GetAggregatorTargetAssetOk() (*string, bool)`

GetAggregatorTargetAssetOk returns a tuple with the AggregatorTargetAsset field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetAggregatorTargetAsset

`func (o *InFlightMigrationTxOut) SetAggregatorTargetAsset(v string)`

SetAggregatorTargetAsset sets AggregatorTargetAsset field to given value.

### HasAggregatorTargetAsset

`func (o *InFlightMigrationTxOut) HasAggregatorTargetAsset() bool`

HasAggregatorTargetAsset returns a boolean if a field has been set.

### GetAggregatorTargetLimit

`func (o *InFlightMigrationTxOut) GetAggregatorTargetLimit() string`

GetAggregatorTargetLimit returns the AggregatorTargetLimit field if non-nil, zero value otherwise.

### GetAggregatorTargetLimitOk

`func (o *InFlightMigrationTxOut) GetAggregatorTargetLimitOk() (*string, bool)`

GetAggregatorTargetLimitOk returns a tuple with the AggregatorTargetLimit field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetAggregatorTargetLimit

`func (o *InFlightMigrationTxOut) SetAggregatorTargetLimit(v string)`

SetAggregatorTargetLimit sets AggregatorTargetLimit field to given value.

### HasAggregatorTargetLimit

`func (o *InFlightMigrationTxOut) HasAggregatorTargetLimit() bool`

HasAggregatorTargetLimit returns a boolean if a field has been set.

### GetCloutSpent

`func (o *InFlightMigrationTxOut) GetCloutSpent() string`

GetCloutSpent returns the CloutSpent field if non-nil, zero value otherwise.

### GetCloutSpentOk

`func (o *InFlightMigrationTxOut) GetCloutSpentOk() (*string, bool)`

GetCloutSpentOk returns a tuple with the CloutSpent field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetCloutSpent

`func (o *InFlightMigrationTxOut) SetCloutSpent(v string)`

SetCloutSpent sets CloutSpent field to given value.

### HasCloutSpent

`func (o *InFlightMigrationTxOut) HasCloutSpent() bool`

HasCloutSpent returns a boolean if a field has been set.


[[Back to Model list]](../README.md#documentation-for-models) [[Back to API list]](../README.md#documentation-for-api-endpoints) [[Back to README]](../README.md)


