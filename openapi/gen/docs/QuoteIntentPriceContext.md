# QuoteIntentPriceContext

## Properties

Name | Type | Description | Notes
------------ | ------------- | ------------- | -------------
**PooledSide** | Pointer to **string** | which asset has a pool (source, target, or both) | [optional] 
**PooledAsset** | Pointer to **string** | the pooled asset | [optional] 
**PooledAmount** | Pointer to **string** | the amount in pooled asset terms | [optional] 
**RuneEquivalent** | Pointer to **string** | RUNE equivalent of the pooled amount | [optional] 

## Methods

### NewQuoteIntentPriceContext

`func NewQuoteIntentPriceContext() *QuoteIntentPriceContext`

NewQuoteIntentPriceContext instantiates a new QuoteIntentPriceContext object
This constructor will assign default values to properties that have it defined,
and makes sure properties required by API are set, but the set of arguments
will change when the set of required properties is changed

### NewQuoteIntentPriceContextWithDefaults

`func NewQuoteIntentPriceContextWithDefaults() *QuoteIntentPriceContext`

NewQuoteIntentPriceContextWithDefaults instantiates a new QuoteIntentPriceContext object
This constructor will only assign default values to properties that have it defined,
but it doesn't guarantee that properties required by API are set

### GetPooledSide

`func (o *QuoteIntentPriceContext) GetPooledSide() string`

GetPooledSide returns the PooledSide field if non-nil, zero value otherwise.

### GetPooledSideOk

`func (o *QuoteIntentPriceContext) GetPooledSideOk() (*string, bool)`

GetPooledSideOk returns a tuple with the PooledSide field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetPooledSide

`func (o *QuoteIntentPriceContext) SetPooledSide(v string)`

SetPooledSide sets PooledSide field to given value.

### HasPooledSide

`func (o *QuoteIntentPriceContext) HasPooledSide() bool`

HasPooledSide returns a boolean if a field has been set.

### GetPooledAsset

`func (o *QuoteIntentPriceContext) GetPooledAsset() string`

GetPooledAsset returns the PooledAsset field if non-nil, zero value otherwise.

### GetPooledAssetOk

`func (o *QuoteIntentPriceContext) GetPooledAssetOk() (*string, bool)`

GetPooledAssetOk returns a tuple with the PooledAsset field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetPooledAsset

`func (o *QuoteIntentPriceContext) SetPooledAsset(v string)`

SetPooledAsset sets PooledAsset field to given value.

### HasPooledAsset

`func (o *QuoteIntentPriceContext) HasPooledAsset() bool`

HasPooledAsset returns a boolean if a field has been set.

### GetPooledAmount

`func (o *QuoteIntentPriceContext) GetPooledAmount() string`

GetPooledAmount returns the PooledAmount field if non-nil, zero value otherwise.

### GetPooledAmountOk

`func (o *QuoteIntentPriceContext) GetPooledAmountOk() (*string, bool)`

GetPooledAmountOk returns a tuple with the PooledAmount field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetPooledAmount

`func (o *QuoteIntentPriceContext) SetPooledAmount(v string)`

SetPooledAmount sets PooledAmount field to given value.

### HasPooledAmount

`func (o *QuoteIntentPriceContext) HasPooledAmount() bool`

HasPooledAmount returns a boolean if a field has been set.

### GetRuneEquivalent

`func (o *QuoteIntentPriceContext) GetRuneEquivalent() string`

GetRuneEquivalent returns the RuneEquivalent field if non-nil, zero value otherwise.

### GetRuneEquivalentOk

`func (o *QuoteIntentPriceContext) GetRuneEquivalentOk() (*string, bool)`

GetRuneEquivalentOk returns a tuple with the RuneEquivalent field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetRuneEquivalent

`func (o *QuoteIntentPriceContext) SetRuneEquivalent(v string)`

SetRuneEquivalent sets RuneEquivalent field to given value.

### HasRuneEquivalent

`func (o *QuoteIntentPriceContext) HasRuneEquivalent() bool`

HasRuneEquivalent returns a boolean if a field has been set.


[[Back to Model list]](../README.md#documentation-for-models) [[Back to API list]](../README.md#documentation-for-api-endpoints) [[Back to README]](../README.md)


