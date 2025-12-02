# IntentAuction

## Properties

Name | Type | Description | Notes
------------ | ------------- | ------------- | -------------
**IntentTxId** | **string** | the transaction ID of the intent | 
**AuctionAmount** | [**Coin**](Coin.md) |  | 
**RemainingAmount** | [**Coin**](Coin.md) |  | 
**BidAsset** | **string** | the asset used for bidding (gas asset of source chain) | 
**MinBid** | **string** | the minimum bid amount | 
**CreateHeight** | **int64** | the block height when the auction was created | 
**ExpiryHeight** | **int64** | the block height when the auction expires | 
**Status** | **string** | the status of the auction | 

## Methods

### NewIntentAuction

`func NewIntentAuction(intentTxId string, auctionAmount Coin, remainingAmount Coin, bidAsset string, minBid string, createHeight int64, expiryHeight int64, status string, ) *IntentAuction`

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


### GetCreateHeight

`func (o *IntentAuction) GetCreateHeight() int64`

GetCreateHeight returns the CreateHeight field if non-nil, zero value otherwise.

### GetCreateHeightOk

`func (o *IntentAuction) GetCreateHeightOk() (*int64, bool)`

GetCreateHeightOk returns a tuple with the CreateHeight field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetCreateHeight

`func (o *IntentAuction) SetCreateHeight(v int64)`

SetCreateHeight sets CreateHeight field to given value.


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



[[Back to Model list]](../README.md#documentation-for-models) [[Back to API list]](../README.md#documentation-for-api-endpoints) [[Back to README]](../README.md)


