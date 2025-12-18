# QuoteIntentResponse

## Properties

Name | Type | Description | Notes
------------ | ------------- | ------------- | -------------
**Valid** | **bool** | whether the intent parameters are valid | 
**ValidationError** | Pointer to **string** | error message if not valid | [optional] 
**Memo** | Pointer to **string** | generated intent open memo | [optional] 
**InboundAddress** | Pointer to **string** | the inbound address for the transaction on the source chain | [optional] 
**InboundConfirmationBlocks** | Pointer to **int64** | approximate blocks for inbound confirmation | [optional] 
**InboundConfirmationSeconds** | Pointer to **int64** | approximate seconds for inbound confirmation | [optional] 
**Router** | Pointer to **string** | EVM router contract address (if applicable) | [optional] 
**IntentTtlBlocks** | Pointer to **int64** | intent TTL in blocks | [optional] 
**IntentTtlSeconds** | Pointer to **int64** | intent TTL in seconds | [optional] 
**PriceSlideBlocks** | Pointer to **int64** | price slide blocks (time for price to slide from high to low) | [optional] 
**ExpiryHeight** | Pointer to **int64** | block height when intent would expire | [optional] 
**Fees** | Pointer to [**QuoteIntentFees**](QuoteIntentFees.md) |  | [optional] 
**AuctionInfo** | Pointer to [**QuoteIntentAuctionInfo**](QuoteIntentAuctionInfo.md) |  | [optional] 
**PriceContext** | Pointer to [**QuoteIntentPriceContext**](QuoteIntentPriceContext.md) |  | [optional] 
**Expiry** | Pointer to **int64** | quote expiration timestamp (unix) | [optional] 
**Warning** | Pointer to **string** | warning message | [optional] 
**Notes** | Pointer to **string** | chain-specific notes | [optional] 
**DustThreshold** | Pointer to **string** | dust threshold for source chain | [optional] 
**RecommendedMinAmountIn** | Pointer to **string** | recommended minimum amount in | [optional] 
**RecommendedGasRate** | Pointer to **string** | recommended gas rate for inbound | [optional] 
**GasRateUnits** | Pointer to **string** | gas rate units | [optional] 
**SolveMinIncrement** | Pointer to **string** | minimum amount a solve must exceed current best by to be considered \&quot;better\&quot; (1x gas fee) | [optional] 
**SolveMinIncrementAsset** | Pointer to **string** | asset the solve_min_increment is denominated in | [optional] 

## Methods

### NewQuoteIntentResponse

`func NewQuoteIntentResponse(valid bool, ) *QuoteIntentResponse`

NewQuoteIntentResponse instantiates a new QuoteIntentResponse object
This constructor will assign default values to properties that have it defined,
and makes sure properties required by API are set, but the set of arguments
will change when the set of required properties is changed

### NewQuoteIntentResponseWithDefaults

`func NewQuoteIntentResponseWithDefaults() *QuoteIntentResponse`

NewQuoteIntentResponseWithDefaults instantiates a new QuoteIntentResponse object
This constructor will only assign default values to properties that have it defined,
but it doesn't guarantee that properties required by API are set

### GetValid

`func (o *QuoteIntentResponse) GetValid() bool`

GetValid returns the Valid field if non-nil, zero value otherwise.

### GetValidOk

`func (o *QuoteIntentResponse) GetValidOk() (*bool, bool)`

GetValidOk returns a tuple with the Valid field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetValid

`func (o *QuoteIntentResponse) SetValid(v bool)`

SetValid sets Valid field to given value.


### GetValidationError

`func (o *QuoteIntentResponse) GetValidationError() string`

GetValidationError returns the ValidationError field if non-nil, zero value otherwise.

### GetValidationErrorOk

`func (o *QuoteIntentResponse) GetValidationErrorOk() (*string, bool)`

GetValidationErrorOk returns a tuple with the ValidationError field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetValidationError

`func (o *QuoteIntentResponse) SetValidationError(v string)`

SetValidationError sets ValidationError field to given value.

### HasValidationError

`func (o *QuoteIntentResponse) HasValidationError() bool`

HasValidationError returns a boolean if a field has been set.

### GetMemo

`func (o *QuoteIntentResponse) GetMemo() string`

GetMemo returns the Memo field if non-nil, zero value otherwise.

### GetMemoOk

`func (o *QuoteIntentResponse) GetMemoOk() (*string, bool)`

GetMemoOk returns a tuple with the Memo field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetMemo

`func (o *QuoteIntentResponse) SetMemo(v string)`

SetMemo sets Memo field to given value.

### HasMemo

`func (o *QuoteIntentResponse) HasMemo() bool`

HasMemo returns a boolean if a field has been set.

### GetInboundAddress

`func (o *QuoteIntentResponse) GetInboundAddress() string`

GetInboundAddress returns the InboundAddress field if non-nil, zero value otherwise.

### GetInboundAddressOk

`func (o *QuoteIntentResponse) GetInboundAddressOk() (*string, bool)`

GetInboundAddressOk returns a tuple with the InboundAddress field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetInboundAddress

`func (o *QuoteIntentResponse) SetInboundAddress(v string)`

SetInboundAddress sets InboundAddress field to given value.

### HasInboundAddress

`func (o *QuoteIntentResponse) HasInboundAddress() bool`

HasInboundAddress returns a boolean if a field has been set.

### GetInboundConfirmationBlocks

`func (o *QuoteIntentResponse) GetInboundConfirmationBlocks() int64`

GetInboundConfirmationBlocks returns the InboundConfirmationBlocks field if non-nil, zero value otherwise.

### GetInboundConfirmationBlocksOk

`func (o *QuoteIntentResponse) GetInboundConfirmationBlocksOk() (*int64, bool)`

GetInboundConfirmationBlocksOk returns a tuple with the InboundConfirmationBlocks field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetInboundConfirmationBlocks

`func (o *QuoteIntentResponse) SetInboundConfirmationBlocks(v int64)`

SetInboundConfirmationBlocks sets InboundConfirmationBlocks field to given value.

### HasInboundConfirmationBlocks

`func (o *QuoteIntentResponse) HasInboundConfirmationBlocks() bool`

HasInboundConfirmationBlocks returns a boolean if a field has been set.

### GetInboundConfirmationSeconds

`func (o *QuoteIntentResponse) GetInboundConfirmationSeconds() int64`

GetInboundConfirmationSeconds returns the InboundConfirmationSeconds field if non-nil, zero value otherwise.

### GetInboundConfirmationSecondsOk

`func (o *QuoteIntentResponse) GetInboundConfirmationSecondsOk() (*int64, bool)`

GetInboundConfirmationSecondsOk returns a tuple with the InboundConfirmationSeconds field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetInboundConfirmationSeconds

`func (o *QuoteIntentResponse) SetInboundConfirmationSeconds(v int64)`

SetInboundConfirmationSeconds sets InboundConfirmationSeconds field to given value.

### HasInboundConfirmationSeconds

`func (o *QuoteIntentResponse) HasInboundConfirmationSeconds() bool`

HasInboundConfirmationSeconds returns a boolean if a field has been set.

### GetRouter

`func (o *QuoteIntentResponse) GetRouter() string`

GetRouter returns the Router field if non-nil, zero value otherwise.

### GetRouterOk

`func (o *QuoteIntentResponse) GetRouterOk() (*string, bool)`

GetRouterOk returns a tuple with the Router field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetRouter

`func (o *QuoteIntentResponse) SetRouter(v string)`

SetRouter sets Router field to given value.

### HasRouter

`func (o *QuoteIntentResponse) HasRouter() bool`

HasRouter returns a boolean if a field has been set.

### GetIntentTtlBlocks

`func (o *QuoteIntentResponse) GetIntentTtlBlocks() int64`

GetIntentTtlBlocks returns the IntentTtlBlocks field if non-nil, zero value otherwise.

### GetIntentTtlBlocksOk

`func (o *QuoteIntentResponse) GetIntentTtlBlocksOk() (*int64, bool)`

GetIntentTtlBlocksOk returns a tuple with the IntentTtlBlocks field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetIntentTtlBlocks

`func (o *QuoteIntentResponse) SetIntentTtlBlocks(v int64)`

SetIntentTtlBlocks sets IntentTtlBlocks field to given value.

### HasIntentTtlBlocks

`func (o *QuoteIntentResponse) HasIntentTtlBlocks() bool`

HasIntentTtlBlocks returns a boolean if a field has been set.

### GetIntentTtlSeconds

`func (o *QuoteIntentResponse) GetIntentTtlSeconds() int64`

GetIntentTtlSeconds returns the IntentTtlSeconds field if non-nil, zero value otherwise.

### GetIntentTtlSecondsOk

`func (o *QuoteIntentResponse) GetIntentTtlSecondsOk() (*int64, bool)`

GetIntentTtlSecondsOk returns a tuple with the IntentTtlSeconds field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetIntentTtlSeconds

`func (o *QuoteIntentResponse) SetIntentTtlSeconds(v int64)`

SetIntentTtlSeconds sets IntentTtlSeconds field to given value.

### HasIntentTtlSeconds

`func (o *QuoteIntentResponse) HasIntentTtlSeconds() bool`

HasIntentTtlSeconds returns a boolean if a field has been set.

### GetPriceSlideBlocks

`func (o *QuoteIntentResponse) GetPriceSlideBlocks() int64`

GetPriceSlideBlocks returns the PriceSlideBlocks field if non-nil, zero value otherwise.

### GetPriceSlideBlocksOk

`func (o *QuoteIntentResponse) GetPriceSlideBlocksOk() (*int64, bool)`

GetPriceSlideBlocksOk returns a tuple with the PriceSlideBlocks field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetPriceSlideBlocks

`func (o *QuoteIntentResponse) SetPriceSlideBlocks(v int64)`

SetPriceSlideBlocks sets PriceSlideBlocks field to given value.

### HasPriceSlideBlocks

`func (o *QuoteIntentResponse) HasPriceSlideBlocks() bool`

HasPriceSlideBlocks returns a boolean if a field has been set.

### GetExpiryHeight

`func (o *QuoteIntentResponse) GetExpiryHeight() int64`

GetExpiryHeight returns the ExpiryHeight field if non-nil, zero value otherwise.

### GetExpiryHeightOk

`func (o *QuoteIntentResponse) GetExpiryHeightOk() (*int64, bool)`

GetExpiryHeightOk returns a tuple with the ExpiryHeight field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetExpiryHeight

`func (o *QuoteIntentResponse) SetExpiryHeight(v int64)`

SetExpiryHeight sets ExpiryHeight field to given value.

### HasExpiryHeight

`func (o *QuoteIntentResponse) HasExpiryHeight() bool`

HasExpiryHeight returns a boolean if a field has been set.

### GetFees

`func (o *QuoteIntentResponse) GetFees() QuoteIntentFees`

GetFees returns the Fees field if non-nil, zero value otherwise.

### GetFeesOk

`func (o *QuoteIntentResponse) GetFeesOk() (*QuoteIntentFees, bool)`

GetFeesOk returns a tuple with the Fees field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetFees

`func (o *QuoteIntentResponse) SetFees(v QuoteIntentFees)`

SetFees sets Fees field to given value.

### HasFees

`func (o *QuoteIntentResponse) HasFees() bool`

HasFees returns a boolean if a field has been set.

### GetAuctionInfo

`func (o *QuoteIntentResponse) GetAuctionInfo() QuoteIntentAuctionInfo`

GetAuctionInfo returns the AuctionInfo field if non-nil, zero value otherwise.

### GetAuctionInfoOk

`func (o *QuoteIntentResponse) GetAuctionInfoOk() (*QuoteIntentAuctionInfo, bool)`

GetAuctionInfoOk returns a tuple with the AuctionInfo field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetAuctionInfo

`func (o *QuoteIntentResponse) SetAuctionInfo(v QuoteIntentAuctionInfo)`

SetAuctionInfo sets AuctionInfo field to given value.

### HasAuctionInfo

`func (o *QuoteIntentResponse) HasAuctionInfo() bool`

HasAuctionInfo returns a boolean if a field has been set.

### GetPriceContext

`func (o *QuoteIntentResponse) GetPriceContext() QuoteIntentPriceContext`

GetPriceContext returns the PriceContext field if non-nil, zero value otherwise.

### GetPriceContextOk

`func (o *QuoteIntentResponse) GetPriceContextOk() (*QuoteIntentPriceContext, bool)`

GetPriceContextOk returns a tuple with the PriceContext field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetPriceContext

`func (o *QuoteIntentResponse) SetPriceContext(v QuoteIntentPriceContext)`

SetPriceContext sets PriceContext field to given value.

### HasPriceContext

`func (o *QuoteIntentResponse) HasPriceContext() bool`

HasPriceContext returns a boolean if a field has been set.

### GetExpiry

`func (o *QuoteIntentResponse) GetExpiry() int64`

GetExpiry returns the Expiry field if non-nil, zero value otherwise.

### GetExpiryOk

`func (o *QuoteIntentResponse) GetExpiryOk() (*int64, bool)`

GetExpiryOk returns a tuple with the Expiry field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetExpiry

`func (o *QuoteIntentResponse) SetExpiry(v int64)`

SetExpiry sets Expiry field to given value.

### HasExpiry

`func (o *QuoteIntentResponse) HasExpiry() bool`

HasExpiry returns a boolean if a field has been set.

### GetWarning

`func (o *QuoteIntentResponse) GetWarning() string`

GetWarning returns the Warning field if non-nil, zero value otherwise.

### GetWarningOk

`func (o *QuoteIntentResponse) GetWarningOk() (*string, bool)`

GetWarningOk returns a tuple with the Warning field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetWarning

`func (o *QuoteIntentResponse) SetWarning(v string)`

SetWarning sets Warning field to given value.

### HasWarning

`func (o *QuoteIntentResponse) HasWarning() bool`

HasWarning returns a boolean if a field has been set.

### GetNotes

`func (o *QuoteIntentResponse) GetNotes() string`

GetNotes returns the Notes field if non-nil, zero value otherwise.

### GetNotesOk

`func (o *QuoteIntentResponse) GetNotesOk() (*string, bool)`

GetNotesOk returns a tuple with the Notes field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetNotes

`func (o *QuoteIntentResponse) SetNotes(v string)`

SetNotes sets Notes field to given value.

### HasNotes

`func (o *QuoteIntentResponse) HasNotes() bool`

HasNotes returns a boolean if a field has been set.

### GetDustThreshold

`func (o *QuoteIntentResponse) GetDustThreshold() string`

GetDustThreshold returns the DustThreshold field if non-nil, zero value otherwise.

### GetDustThresholdOk

`func (o *QuoteIntentResponse) GetDustThresholdOk() (*string, bool)`

GetDustThresholdOk returns a tuple with the DustThreshold field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetDustThreshold

`func (o *QuoteIntentResponse) SetDustThreshold(v string)`

SetDustThreshold sets DustThreshold field to given value.

### HasDustThreshold

`func (o *QuoteIntentResponse) HasDustThreshold() bool`

HasDustThreshold returns a boolean if a field has been set.

### GetRecommendedMinAmountIn

`func (o *QuoteIntentResponse) GetRecommendedMinAmountIn() string`

GetRecommendedMinAmountIn returns the RecommendedMinAmountIn field if non-nil, zero value otherwise.

### GetRecommendedMinAmountInOk

`func (o *QuoteIntentResponse) GetRecommendedMinAmountInOk() (*string, bool)`

GetRecommendedMinAmountInOk returns a tuple with the RecommendedMinAmountIn field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetRecommendedMinAmountIn

`func (o *QuoteIntentResponse) SetRecommendedMinAmountIn(v string)`

SetRecommendedMinAmountIn sets RecommendedMinAmountIn field to given value.

### HasRecommendedMinAmountIn

`func (o *QuoteIntentResponse) HasRecommendedMinAmountIn() bool`

HasRecommendedMinAmountIn returns a boolean if a field has been set.

### GetRecommendedGasRate

`func (o *QuoteIntentResponse) GetRecommendedGasRate() string`

GetRecommendedGasRate returns the RecommendedGasRate field if non-nil, zero value otherwise.

### GetRecommendedGasRateOk

`func (o *QuoteIntentResponse) GetRecommendedGasRateOk() (*string, bool)`

GetRecommendedGasRateOk returns a tuple with the RecommendedGasRate field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetRecommendedGasRate

`func (o *QuoteIntentResponse) SetRecommendedGasRate(v string)`

SetRecommendedGasRate sets RecommendedGasRate field to given value.

### HasRecommendedGasRate

`func (o *QuoteIntentResponse) HasRecommendedGasRate() bool`

HasRecommendedGasRate returns a boolean if a field has been set.

### GetGasRateUnits

`func (o *QuoteIntentResponse) GetGasRateUnits() string`

GetGasRateUnits returns the GasRateUnits field if non-nil, zero value otherwise.

### GetGasRateUnitsOk

`func (o *QuoteIntentResponse) GetGasRateUnitsOk() (*string, bool)`

GetGasRateUnitsOk returns a tuple with the GasRateUnits field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetGasRateUnits

`func (o *QuoteIntentResponse) SetGasRateUnits(v string)`

SetGasRateUnits sets GasRateUnits field to given value.

### HasGasRateUnits

`func (o *QuoteIntentResponse) HasGasRateUnits() bool`

HasGasRateUnits returns a boolean if a field has been set.

### GetSolveMinIncrement

`func (o *QuoteIntentResponse) GetSolveMinIncrement() string`

GetSolveMinIncrement returns the SolveMinIncrement field if non-nil, zero value otherwise.

### GetSolveMinIncrementOk

`func (o *QuoteIntentResponse) GetSolveMinIncrementOk() (*string, bool)`

GetSolveMinIncrementOk returns a tuple with the SolveMinIncrement field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetSolveMinIncrement

`func (o *QuoteIntentResponse) SetSolveMinIncrement(v string)`

SetSolveMinIncrement sets SolveMinIncrement field to given value.

### HasSolveMinIncrement

`func (o *QuoteIntentResponse) HasSolveMinIncrement() bool`

HasSolveMinIncrement returns a boolean if a field has been set.

### GetSolveMinIncrementAsset

`func (o *QuoteIntentResponse) GetSolveMinIncrementAsset() string`

GetSolveMinIncrementAsset returns the SolveMinIncrementAsset field if non-nil, zero value otherwise.

### GetSolveMinIncrementAssetOk

`func (o *QuoteIntentResponse) GetSolveMinIncrementAssetOk() (*string, bool)`

GetSolveMinIncrementAssetOk returns a tuple with the SolveMinIncrementAsset field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetSolveMinIncrementAsset

`func (o *QuoteIntentResponse) SetSolveMinIncrementAsset(v string)`

SetSolveMinIncrementAsset sets SolveMinIncrementAsset field to given value.

### HasSolveMinIncrementAsset

`func (o *QuoteIntentResponse) HasSolveMinIncrementAsset() bool`

HasSolveMinIncrementAsset returns a boolean if a field has been set.


[[Back to Model list]](../README.md#documentation-for-models) [[Back to API list]](../README.md#documentation-for-api-endpoints) [[Back to README]](../README.md)


