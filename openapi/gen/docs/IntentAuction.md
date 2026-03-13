# IntentAuction

## Properties

Name | Type | Description | Notes
------------ | ------------- | ------------- | -------------
**IntentTxId** | **string** | the transaction ID of the intent | 
**AuctionAmount** | [**Coin**](Coin.md) |  | 
**RemainingAmount** | [**Coin**](Coin.md) |  | 
**BidAsset** | **string** | the asset used for bidding (gas asset of source chain) | 
**MinBid** | **string** | the minimum bid amount | 
**StartHeight** | **int64** | the block height when the auction started | 
**ExpiryHeight** | **int64** | the block height when the auction expires | 
**Status** | **string** | the status of the auction | 
**OwnerAddress** | Pointer to **string** | the owner address (original intent creator) | [optional] 
**WinningBidTxId** | Pointer to **string** | the transaction ID of the winning bid | [optional] 

## Methods

### NewIntentAuction

`func NewIntentAuction(intentTxId string, auctionAmount Coin, remainingAmount Coin, bidAsset string, minBid string, startHeight int64, expiryHeight int64, status string, ) *IntentAuction`

NewIntentAuction instantiates a new IntentAuction object
This constructor will assign default values to properties that have it defined,
and makes sure properties required by API are set, but the set of arguments
will change when the set of required properties is changed

### NewIntentAuctionWithDefaults

`func NewIntentAuctionWithDefaults() *IntentAuction`

NewIntentAuctionWithDefaults instantiates a new IntentAuction object
This constructor will only assign default values to properties that have it defined,
but it doesn't guarantee that properties required by API are set

### GetIntentTxId

`func (o *IntentAuction) GetIntentTxId() string`

GetIntentTxId returns the IntentTxId field if non-nil, zero value otherwise.

### GetIntentTxIdOk

`func (o *IntentAuction) GetIntentTxIdOk() (*string, bool)`

GetIntentTxIdOk returns a tuple with the IntentTxId field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetIntentTxId

`func (o *IntentAuction) SetIntentTxId(v string)`

SetIntentTxId sets IntentTxId field to given value.


### GetAuctionAmount

`func (o *IntentAuction) GetAuctionAmount() Coin`

GetAuctionAmount returns the AuctionAmount field if non-nil, zero value otherwise.

### GetAuctionAmountOk

`func (o *IntentAuction) GetAuctionAmountOk() (*Coin, bool)`

GetAuctionAmountOk returns a tuple with the AuctionAmount field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetAuctionAmount

`func (o *IntentAuction) SetAuctionAmount(v Coin)`

SetAuctionAmount sets AuctionAmount field to given value.


### GetRemainingAmount

`func (o *IntentAuction) GetRemainingAmount() Coin`

GetRemainingAmount returns the RemainingAmount field if non-nil, zero value otherwise.

### GetRemainingAmountOk

`func (o *IntentAuction) GetRemainingAmountOk() (*Coin, bool)`

GetRemainingAmountOk returns a tuple with the RemainingAmount field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetRemainingAmount

`func (o *IntentAuction) SetRemainingAmount(v Coin)`

SetRemainingAmount sets RemainingAmount field to given value.


### GetBidAsset

`func (o *IntentAuction) GetBidAsset() string`

GetBidAsset returns the BidAsset field if non-nil, zero value otherwise.

### GetBidAssetOk

`func (o *IntentAuction) GetBidAssetOk() (*string, bool)`

GetBidAssetOk returns a tuple with the BidAsset field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetBidAsset

`func (o *IntentAuction) SetBidAsset(v string)`

SetBidAsset sets BidAsset field to given value.


### GetMinBid

`func (o *IntentAuction) GetMinBid() string`

GetMinBid returns the MinBid field if non-nil, zero value otherwise.

### GetMinBidOk

`func (o *IntentAuction) GetMinBidOk() (*string, bool)`

GetMinBidOk returns a tuple with the MinBid field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetMinBid

`func (o *IntentAuction) SetMinBid(v string)`

SetMinBid sets MinBid field to given value.


### GetStartHeight

`func (o *IntentAuction) GetStartHeight() int64`

GetStartHeight returns the StartHeight field if non-nil, zero value otherwise.

### GetStartHeightOk

`func (o *IntentAuction) GetStartHeightOk() (*int64, bool)`

GetStartHeightOk returns a tuple with the StartHeight field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetStartHeight

`func (o *IntentAuction) SetStartHeight(v int64)`

SetStartHeight sets StartHeight field to given value.


### GetExpiryHeight

`func (o *IntentAuction) GetExpiryHeight() int64`

GetExpiryHeight returns the ExpiryHeight field if non-nil, zero value otherwise.

### GetExpiryHeightOk

`func (o *IntentAuction) GetExpiryHeightOk() (*int64, bool)`

GetExpiryHeightOk returns a tuple with the ExpiryHeight field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetExpiryHeight

`func (o *IntentAuction) SetExpiryHeight(v int64)`

SetExpiryHeight sets ExpiryHeight field to given value.


### GetStatus

`func (o *IntentAuction) GetStatus() string`

GetStatus returns the Status field if non-nil, zero value otherwise.

### GetStatusOk

`func (o *IntentAuction) GetStatusOk() (*string, bool)`

GetStatusOk returns a tuple with the Status field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetStatus

`func (o *IntentAuction) SetStatus(v string)`

SetStatus sets Status field to given value.


### GetOwnerAddress

`func (o *IntentAuction) GetOwnerAddress() string`

GetOwnerAddress returns the OwnerAddress field if non-nil, zero value otherwise.

### GetOwnerAddressOk

`func (o *IntentAuction) GetOwnerAddressOk() (*string, bool)`

GetOwnerAddressOk returns a tuple with the OwnerAddress field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetOwnerAddress

`func (o *IntentAuction) SetOwnerAddress(v string)`

SetOwnerAddress sets OwnerAddress field to given value.

### HasOwnerAddress

`func (o *IntentAuction) HasOwnerAddress() bool`

HasOwnerAddress returns a boolean if a field has been set.

### GetWinningBidTxId

`func (o *IntentAuction) GetWinningBidTxId() string`

GetWinningBidTxId returns the WinningBidTxId field if non-nil, zero value otherwise.

### GetWinningBidTxIdOk

`func (o *IntentAuction) GetWinningBidTxIdOk() (*string, bool)`

GetWinningBidTxIdOk returns a tuple with the WinningBidTxId field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetWinningBidTxId

`func (o *IntentAuction) SetWinningBidTxId(v string)`

SetWinningBidTxId sets WinningBidTxId field to given value.

### HasWinningBidTxId

`func (o *IntentAuction) HasWinningBidTxId() bool`

HasWinningBidTxId returns a boolean if a field has been set.


[[Back to Model list]](../README.md#documentation-for-models) [[Back to API list]](../README.md#documentation-for-api-endpoints) [[Back to README]](../README.md)


