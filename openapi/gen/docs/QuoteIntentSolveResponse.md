# QuoteIntentSolveResponse

## Properties

Name | Type | Description | Notes
------------ | ------------- | ------------- | -------------
**Valid** | **bool** | whether the solve parameters are valid | 
**ValidationError** | Pointer to **string** | error message if not valid | [optional] 
**TargetAsset** | Pointer to **string** | the target asset expected for solving | [optional] 
**MinAmount** | Pointer to **string** | minimum amount required (at least low_target) | [optional] 
**InstantWinAmount** | Pointer to **string** | amount needed to win immediately (high_target) | [optional] 
**CurrentBestAmount** | Pointer to **string** | current best solve amount (if any) | [optional] 
**AmountToBeatBest** | Pointer to **string** | amount needed to beat current best (current_best + min_increment) | [optional] 
**MinIncrement** | Pointer to **string** | minimum increment required to beat current best (1x gas fee) | [optional] 
**MinIncrementAsset** | Pointer to **string** | asset the min_increment is denominated in | [optional] 
**EstimatedMatchHeight** | Pointer to **int64** | estimated match height if this solve were submitted | [optional] 
**WouldWinImmediately** | Pointer to **bool** | whether this solve would win immediately | [optional] 
**WouldBeatCurrentBest** | Pointer to **bool** | whether this solve would beat the current best | [optional] 
**BlocksUntilMatch** | Pointer to **int64** | number of blocks until match (0 if immediate) | [optional] 
**IntentExpiryHeight** | Pointer to **int64** | intent expiry height | [optional] 
**CurrentHeight** | Pointer to **int64** | current block height | [optional] 
**ExistingSolveCount** | Pointer to **int64** | number of existing solves on this intent | [optional] 
**SourceCoin** | Pointer to [**Coin**](Coin.md) |  | [optional] 
**Expiry** | Pointer to **int64** | quote expiration timestamp (unix) | [optional] 
**Warning** | Pointer to **string** | warning message | [optional] 
**Notes** | Pointer to **string** | additional notes | [optional] 

## Methods

### NewQuoteIntentSolveResponse

`func NewQuoteIntentSolveResponse(valid bool, ) *QuoteIntentSolveResponse`

NewQuoteIntentSolveResponse instantiates a new QuoteIntentSolveResponse object
This constructor will assign default values to properties that have it defined,
and makes sure properties required by API are set, but the set of arguments
will change when the set of required properties is changed

### NewQuoteIntentSolveResponseWithDefaults

`func NewQuoteIntentSolveResponseWithDefaults() *QuoteIntentSolveResponse`

NewQuoteIntentSolveResponseWithDefaults instantiates a new QuoteIntentSolveResponse object
This constructor will only assign default values to properties that have it defined,
but it doesn't guarantee that properties required by API are set

### GetValid

`func (o *QuoteIntentSolveResponse) GetValid() bool`

GetValid returns the Valid field if non-nil, zero value otherwise.

### GetValidOk

`func (o *QuoteIntentSolveResponse) GetValidOk() (*bool, bool)`

GetValidOk returns a tuple with the Valid field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetValid

`func (o *QuoteIntentSolveResponse) SetValid(v bool)`

SetValid sets Valid field to given value.


### GetValidationError

`func (o *QuoteIntentSolveResponse) GetValidationError() string`

GetValidationError returns the ValidationError field if non-nil, zero value otherwise.

### GetValidationErrorOk

`func (o *QuoteIntentSolveResponse) GetValidationErrorOk() (*string, bool)`

GetValidationErrorOk returns a tuple with the ValidationError field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetValidationError

`func (o *QuoteIntentSolveResponse) SetValidationError(v string)`

SetValidationError sets ValidationError field to given value.

### HasValidationError

`func (o *QuoteIntentSolveResponse) HasValidationError() bool`

HasValidationError returns a boolean if a field has been set.

### GetTargetAsset

`func (o *QuoteIntentSolveResponse) GetTargetAsset() string`

GetTargetAsset returns the TargetAsset field if non-nil, zero value otherwise.

### GetTargetAssetOk

`func (o *QuoteIntentSolveResponse) GetTargetAssetOk() (*string, bool)`

GetTargetAssetOk returns a tuple with the TargetAsset field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetTargetAsset

`func (o *QuoteIntentSolveResponse) SetTargetAsset(v string)`

SetTargetAsset sets TargetAsset field to given value.

### HasTargetAsset

`func (o *QuoteIntentSolveResponse) HasTargetAsset() bool`

HasTargetAsset returns a boolean if a field has been set.

### GetMinAmount

`func (o *QuoteIntentSolveResponse) GetMinAmount() string`

GetMinAmount returns the MinAmount field if non-nil, zero value otherwise.

### GetMinAmountOk

`func (o *QuoteIntentSolveResponse) GetMinAmountOk() (*string, bool)`

GetMinAmountOk returns a tuple with the MinAmount field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetMinAmount

`func (o *QuoteIntentSolveResponse) SetMinAmount(v string)`

SetMinAmount sets MinAmount field to given value.

### HasMinAmount

`func (o *QuoteIntentSolveResponse) HasMinAmount() bool`

HasMinAmount returns a boolean if a field has been set.

### GetInstantWinAmount

`func (o *QuoteIntentSolveResponse) GetInstantWinAmount() string`

GetInstantWinAmount returns the InstantWinAmount field if non-nil, zero value otherwise.

### GetInstantWinAmountOk

`func (o *QuoteIntentSolveResponse) GetInstantWinAmountOk() (*string, bool)`

GetInstantWinAmountOk returns a tuple with the InstantWinAmount field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetInstantWinAmount

`func (o *QuoteIntentSolveResponse) SetInstantWinAmount(v string)`

SetInstantWinAmount sets InstantWinAmount field to given value.

### HasInstantWinAmount

`func (o *QuoteIntentSolveResponse) HasInstantWinAmount() bool`

HasInstantWinAmount returns a boolean if a field has been set.

### GetCurrentBestAmount

`func (o *QuoteIntentSolveResponse) GetCurrentBestAmount() string`

GetCurrentBestAmount returns the CurrentBestAmount field if non-nil, zero value otherwise.

### GetCurrentBestAmountOk

`func (o *QuoteIntentSolveResponse) GetCurrentBestAmountOk() (*string, bool)`

GetCurrentBestAmountOk returns a tuple with the CurrentBestAmount field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetCurrentBestAmount

`func (o *QuoteIntentSolveResponse) SetCurrentBestAmount(v string)`

SetCurrentBestAmount sets CurrentBestAmount field to given value.

### HasCurrentBestAmount

`func (o *QuoteIntentSolveResponse) HasCurrentBestAmount() bool`

HasCurrentBestAmount returns a boolean if a field has been set.

### GetAmountToBeatBest

`func (o *QuoteIntentSolveResponse) GetAmountToBeatBest() string`

GetAmountToBeatBest returns the AmountToBeatBest field if non-nil, zero value otherwise.

### GetAmountToBeatBestOk

`func (o *QuoteIntentSolveResponse) GetAmountToBeatBestOk() (*string, bool)`

GetAmountToBeatBestOk returns a tuple with the AmountToBeatBest field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetAmountToBeatBest

`func (o *QuoteIntentSolveResponse) SetAmountToBeatBest(v string)`

SetAmountToBeatBest sets AmountToBeatBest field to given value.

### HasAmountToBeatBest

`func (o *QuoteIntentSolveResponse) HasAmountToBeatBest() bool`

HasAmountToBeatBest returns a boolean if a field has been set.

### GetMinIncrement

`func (o *QuoteIntentSolveResponse) GetMinIncrement() string`

GetMinIncrement returns the MinIncrement field if non-nil, zero value otherwise.

### GetMinIncrementOk

`func (o *QuoteIntentSolveResponse) GetMinIncrementOk() (*string, bool)`

GetMinIncrementOk returns a tuple with the MinIncrement field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetMinIncrement

`func (o *QuoteIntentSolveResponse) SetMinIncrement(v string)`

SetMinIncrement sets MinIncrement field to given value.

### HasMinIncrement

`func (o *QuoteIntentSolveResponse) HasMinIncrement() bool`

HasMinIncrement returns a boolean if a field has been set.

### GetMinIncrementAsset

`func (o *QuoteIntentSolveResponse) GetMinIncrementAsset() string`

GetMinIncrementAsset returns the MinIncrementAsset field if non-nil, zero value otherwise.

### GetMinIncrementAssetOk

`func (o *QuoteIntentSolveResponse) GetMinIncrementAssetOk() (*string, bool)`

GetMinIncrementAssetOk returns a tuple with the MinIncrementAsset field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetMinIncrementAsset

`func (o *QuoteIntentSolveResponse) SetMinIncrementAsset(v string)`

SetMinIncrementAsset sets MinIncrementAsset field to given value.

### HasMinIncrementAsset

`func (o *QuoteIntentSolveResponse) HasMinIncrementAsset() bool`

HasMinIncrementAsset returns a boolean if a field has been set.

### GetEstimatedMatchHeight

`func (o *QuoteIntentSolveResponse) GetEstimatedMatchHeight() int64`

GetEstimatedMatchHeight returns the EstimatedMatchHeight field if non-nil, zero value otherwise.

### GetEstimatedMatchHeightOk

`func (o *QuoteIntentSolveResponse) GetEstimatedMatchHeightOk() (*int64, bool)`

GetEstimatedMatchHeightOk returns a tuple with the EstimatedMatchHeight field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetEstimatedMatchHeight

`func (o *QuoteIntentSolveResponse) SetEstimatedMatchHeight(v int64)`

SetEstimatedMatchHeight sets EstimatedMatchHeight field to given value.

### HasEstimatedMatchHeight

`func (o *QuoteIntentSolveResponse) HasEstimatedMatchHeight() bool`

HasEstimatedMatchHeight returns a boolean if a field has been set.

### GetWouldWinImmediately

`func (o *QuoteIntentSolveResponse) GetWouldWinImmediately() bool`

GetWouldWinImmediately returns the WouldWinImmediately field if non-nil, zero value otherwise.

### GetWouldWinImmediatelyOk

`func (o *QuoteIntentSolveResponse) GetWouldWinImmediatelyOk() (*bool, bool)`

GetWouldWinImmediatelyOk returns a tuple with the WouldWinImmediately field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetWouldWinImmediately

`func (o *QuoteIntentSolveResponse) SetWouldWinImmediately(v bool)`

SetWouldWinImmediately sets WouldWinImmediately field to given value.

### HasWouldWinImmediately

`func (o *QuoteIntentSolveResponse) HasWouldWinImmediately() bool`

HasWouldWinImmediately returns a boolean if a field has been set.

### GetWouldBeatCurrentBest

`func (o *QuoteIntentSolveResponse) GetWouldBeatCurrentBest() bool`

GetWouldBeatCurrentBest returns the WouldBeatCurrentBest field if non-nil, zero value otherwise.

### GetWouldBeatCurrentBestOk

`func (o *QuoteIntentSolveResponse) GetWouldBeatCurrentBestOk() (*bool, bool)`

GetWouldBeatCurrentBestOk returns a tuple with the WouldBeatCurrentBest field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetWouldBeatCurrentBest

`func (o *QuoteIntentSolveResponse) SetWouldBeatCurrentBest(v bool)`

SetWouldBeatCurrentBest sets WouldBeatCurrentBest field to given value.

### HasWouldBeatCurrentBest

`func (o *QuoteIntentSolveResponse) HasWouldBeatCurrentBest() bool`

HasWouldBeatCurrentBest returns a boolean if a field has been set.

### GetBlocksUntilMatch

`func (o *QuoteIntentSolveResponse) GetBlocksUntilMatch() int64`

GetBlocksUntilMatch returns the BlocksUntilMatch field if non-nil, zero value otherwise.

### GetBlocksUntilMatchOk

`func (o *QuoteIntentSolveResponse) GetBlocksUntilMatchOk() (*int64, bool)`

GetBlocksUntilMatchOk returns a tuple with the BlocksUntilMatch field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetBlocksUntilMatch

`func (o *QuoteIntentSolveResponse) SetBlocksUntilMatch(v int64)`

SetBlocksUntilMatch sets BlocksUntilMatch field to given value.

### HasBlocksUntilMatch

`func (o *QuoteIntentSolveResponse) HasBlocksUntilMatch() bool`

HasBlocksUntilMatch returns a boolean if a field has been set.

### GetIntentExpiryHeight

`func (o *QuoteIntentSolveResponse) GetIntentExpiryHeight() int64`

GetIntentExpiryHeight returns the IntentExpiryHeight field if non-nil, zero value otherwise.

### GetIntentExpiryHeightOk

`func (o *QuoteIntentSolveResponse) GetIntentExpiryHeightOk() (*int64, bool)`

GetIntentExpiryHeightOk returns a tuple with the IntentExpiryHeight field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetIntentExpiryHeight

`func (o *QuoteIntentSolveResponse) SetIntentExpiryHeight(v int64)`

SetIntentExpiryHeight sets IntentExpiryHeight field to given value.

### HasIntentExpiryHeight

`func (o *QuoteIntentSolveResponse) HasIntentExpiryHeight() bool`

HasIntentExpiryHeight returns a boolean if a field has been set.

### GetCurrentHeight

`func (o *QuoteIntentSolveResponse) GetCurrentHeight() int64`

GetCurrentHeight returns the CurrentHeight field if non-nil, zero value otherwise.

### GetCurrentHeightOk

`func (o *QuoteIntentSolveResponse) GetCurrentHeightOk() (*int64, bool)`

GetCurrentHeightOk returns a tuple with the CurrentHeight field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetCurrentHeight

`func (o *QuoteIntentSolveResponse) SetCurrentHeight(v int64)`

SetCurrentHeight sets CurrentHeight field to given value.

### HasCurrentHeight

`func (o *QuoteIntentSolveResponse) HasCurrentHeight() bool`

HasCurrentHeight returns a boolean if a field has been set.

### GetExistingSolveCount

`func (o *QuoteIntentSolveResponse) GetExistingSolveCount() int64`

GetExistingSolveCount returns the ExistingSolveCount field if non-nil, zero value otherwise.

### GetExistingSolveCountOk

`func (o *QuoteIntentSolveResponse) GetExistingSolveCountOk() (*int64, bool)`

GetExistingSolveCountOk returns a tuple with the ExistingSolveCount field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetExistingSolveCount

`func (o *QuoteIntentSolveResponse) SetExistingSolveCount(v int64)`

SetExistingSolveCount sets ExistingSolveCount field to given value.

### HasExistingSolveCount

`func (o *QuoteIntentSolveResponse) HasExistingSolveCount() bool`

HasExistingSolveCount returns a boolean if a field has been set.

### GetSourceCoin

`func (o *QuoteIntentSolveResponse) GetSourceCoin() Coin`

GetSourceCoin returns the SourceCoin field if non-nil, zero value otherwise.

### GetSourceCoinOk

`func (o *QuoteIntentSolveResponse) GetSourceCoinOk() (*Coin, bool)`

GetSourceCoinOk returns a tuple with the SourceCoin field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetSourceCoin

`func (o *QuoteIntentSolveResponse) SetSourceCoin(v Coin)`

SetSourceCoin sets SourceCoin field to given value.

### HasSourceCoin

`func (o *QuoteIntentSolveResponse) HasSourceCoin() bool`

HasSourceCoin returns a boolean if a field has been set.

### GetExpiry

`func (o *QuoteIntentSolveResponse) GetExpiry() int64`

GetExpiry returns the Expiry field if non-nil, zero value otherwise.

### GetExpiryOk

`func (o *QuoteIntentSolveResponse) GetExpiryOk() (*int64, bool)`

GetExpiryOk returns a tuple with the Expiry field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetExpiry

`func (o *QuoteIntentSolveResponse) SetExpiry(v int64)`

SetExpiry sets Expiry field to given value.

### HasExpiry

`func (o *QuoteIntentSolveResponse) HasExpiry() bool`

HasExpiry returns a boolean if a field has been set.

### GetWarning

`func (o *QuoteIntentSolveResponse) GetWarning() string`

GetWarning returns the Warning field if non-nil, zero value otherwise.

### GetWarningOk

`func (o *QuoteIntentSolveResponse) GetWarningOk() (*string, bool)`

GetWarningOk returns a tuple with the Warning field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetWarning

`func (o *QuoteIntentSolveResponse) SetWarning(v string)`

SetWarning sets Warning field to given value.

### HasWarning

`func (o *QuoteIntentSolveResponse) HasWarning() bool`

HasWarning returns a boolean if a field has been set.

### GetNotes

`func (o *QuoteIntentSolveResponse) GetNotes() string`

GetNotes returns the Notes field if non-nil, zero value otherwise.

### GetNotesOk

`func (o *QuoteIntentSolveResponse) GetNotesOk() (*string, bool)`

GetNotesOk returns a tuple with the Notes field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetNotes

`func (o *QuoteIntentSolveResponse) SetNotes(v string)`

SetNotes sets Notes field to given value.

### HasNotes

`func (o *QuoteIntentSolveResponse) HasNotes() bool`

HasNotes returns a boolean if a field has been set.


[[Back to Model list]](../README.md#documentation-for-models) [[Back to API list]](../README.md#documentation-for-api-endpoints) [[Back to README]](../README.md)


