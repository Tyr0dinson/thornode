# QuotePairResponse

## Properties

Name | Type | Description | Notes
------------ | ------------- | ------------- | -------------
**Available** | **bool** | returns true if the pair is available for trading | 
**DustThreshold** | **string** | defines the minimum observable transaction size for the chain in base units (sats, wei, uatom). Transactions with asset amounts lower than the dust_threshold are ignored. | 
**ExchangeRate** | **string** | the flat exchange rate of the pair. Note the actual exchange rate will change based on the size of the swap. | 
**Notes** | **string** | chain specific quote notes | 
**RecommendedMinAmountIn** | **string** | The recommended minimum inbound amount for this transaction type &amp; inbound asset. Sending less than this amount could result in failed refunds. | 
**RecommendedGasRate** | **string** | the recommended gas rate to use for the inbound to ensure timely confirmation | 

## Methods

### NewQuotePairResponse

`func NewQuotePairResponse(available bool, dustThreshold string, exchangeRate string, notes string, recommendedMinAmountIn string, recommendedGasRate string, ) *QuotePairResponse`

NewQuotePairResponse instantiates a new QuotePairResponse object
This constructor will assign default values to properties that have it defined,
and makes sure properties required by API are set, but the set of arguments
will change when the set of required properties is changed

### NewQuotePairResponseWithDefaults

`func NewQuotePairResponseWithDefaults() *QuotePairResponse`

NewQuotePairResponseWithDefaults instantiates a new QuotePairResponse object
This constructor will only assign default values to properties that have it defined,
but it doesn't guarantee that properties required by API are set

### GetAvailable

`func (o *QuotePairResponse) GetAvailable() bool`

GetAvailable returns the Available field if non-nil, zero value otherwise.

### GetAvailableOk

`func (o *QuotePairResponse) GetAvailableOk() (*bool, bool)`

GetAvailableOk returns a tuple with the Available field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetAvailable

`func (o *QuotePairResponse) SetAvailable(v bool)`

SetAvailable sets Available field to given value.


### GetDustThreshold

`func (o *QuotePairResponse) GetDustThreshold() string`

GetDustThreshold returns the DustThreshold field if non-nil, zero value otherwise.

### GetDustThresholdOk

`func (o *QuotePairResponse) GetDustThresholdOk() (*string, bool)`

GetDustThresholdOk returns a tuple with the DustThreshold field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetDustThreshold

`func (o *QuotePairResponse) SetDustThreshold(v string)`

SetDustThreshold sets DustThreshold field to given value.


### GetExchangeRate

`func (o *QuotePairResponse) GetExchangeRate() string`

GetExchangeRate returns the ExchangeRate field if non-nil, zero value otherwise.

### GetExchangeRateOk

`func (o *QuotePairResponse) GetExchangeRateOk() (*string, bool)`

GetExchangeRateOk returns a tuple with the ExchangeRate field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetExchangeRate

`func (o *QuotePairResponse) SetExchangeRate(v string)`

SetExchangeRate sets ExchangeRate field to given value.


### GetNotes

`func (o *QuotePairResponse) GetNotes() string`

GetNotes returns the Notes field if non-nil, zero value otherwise.

### GetNotesOk

`func (o *QuotePairResponse) GetNotesOk() (*string, bool)`

GetNotesOk returns a tuple with the Notes field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetNotes

`func (o *QuotePairResponse) SetNotes(v string)`

SetNotes sets Notes field to given value.


### GetRecommendedMinAmountIn

`func (o *QuotePairResponse) GetRecommendedMinAmountIn() string`

GetRecommendedMinAmountIn returns the RecommendedMinAmountIn field if non-nil, zero value otherwise.

### GetRecommendedMinAmountInOk

`func (o *QuotePairResponse) GetRecommendedMinAmountInOk() (*string, bool)`

GetRecommendedMinAmountInOk returns a tuple with the RecommendedMinAmountIn field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetRecommendedMinAmountIn

`func (o *QuotePairResponse) SetRecommendedMinAmountIn(v string)`

SetRecommendedMinAmountIn sets RecommendedMinAmountIn field to given value.


### GetRecommendedGasRate

`func (o *QuotePairResponse) GetRecommendedGasRate() string`

GetRecommendedGasRate returns the RecommendedGasRate field if non-nil, zero value otherwise.

### GetRecommendedGasRateOk

`func (o *QuotePairResponse) GetRecommendedGasRateOk() (*string, bool)`

GetRecommendedGasRateOk returns a tuple with the RecommendedGasRate field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetRecommendedGasRate

`func (o *QuotePairResponse) SetRecommendedGasRate(v string)`

SetRecommendedGasRate sets RecommendedGasRate field to given value.



[[Back to Model list]](../README.md#documentation-for-models) [[Back to API list]](../README.md#documentation-for-api-endpoints) [[Back to README]](../README.md)


