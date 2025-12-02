# IntentAuctionBid

## Properties

Name | Type | Description | Notes
------------ | ------------- | ------------- | -------------
**BidTxId** | **string** | the transaction ID of the bid | 
**IntentTxId** | **string** | the transaction ID of the intent | 
**BidderAddress** | **string** | the bidder address | 
**BidAmount** | [**Coin**](Coin.md) |  | 
**SubmitHeight** | **int64** | the block height when the bid was submitted | 

## Methods

### NewIntentAuctionBid

`func NewIntentAuctionBid(bidTxId string, intentTxId string, bidderAddress string, bidAmount Coin, submitHeight int64, ) *IntentAuctionBid`

NewIntentAuctionBid instantiates a new IntentAuctionBid object
This constructor will assign default values to properties that have it defined,
and makes sure properties required by API are set, but the set of arguments
will change when the set of required properties is changed

### NewIntentAuctionBidWithDefaults

`func NewIntentAuctionBidWithDefaults() *IntentAuctionBid`

NewIntentAuctionBidWithDefaults instantiates a new IntentAuctionBid object
This constructor will only assign default values to properties that have it defined,
but it doesn't guarantee that properties required by API are set

### GetBidTxId

`func (o *IntentAuctionBid) GetBidTxId() string`

GetBidTxId returns the BidTxId field if non-nil, zero value otherwise.

### GetBidTxIdOk

`func (o *IntentAuctionBid) GetBidTxIdOk() (*string, bool)`

GetBidTxIdOk returns a tuple with the BidTxId field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetBidTxId

`func (o *IntentAuctionBid) SetBidTxId(v string)`

SetBidTxId sets BidTxId field to given value.


### GetIntentTxId

`func (o *IntentAuctionBid) GetIntentTxId() string`

GetIntentTxId returns the IntentTxId field if non-nil, zero value otherwise.

### GetIntentTxIdOk

`func (o *IntentAuctionBid) GetIntentTxIdOk() (*string, bool)`

GetIntentTxIdOk returns a tuple with the IntentTxId field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetIntentTxId

`func (o *IntentAuctionBid) SetIntentTxId(v string)`

SetIntentTxId sets IntentTxId field to given value.


### GetBidderAddress

`func (o *IntentAuctionBid) GetBidderAddress() string`

GetBidderAddress returns the BidderAddress field if non-nil, zero value otherwise.

### GetBidderAddressOk

`func (o *IntentAuctionBid) GetBidderAddressOk() (*string, bool)`

GetBidderAddressOk returns a tuple with the BidderAddress field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetBidderAddress

`func (o *IntentAuctionBid) SetBidderAddress(v string)`

SetBidderAddress sets BidderAddress field to given value.


### GetBidAmount

`func (o *IntentAuctionBid) GetBidAmount() Coin`

GetBidAmount returns the BidAmount field if non-nil, zero value otherwise.

### GetBidAmountOk

`func (o *IntentAuctionBid) GetBidAmountOk() (*Coin, bool)`

GetBidAmountOk returns a tuple with the BidAmount field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetBidAmount

`func (o *IntentAuctionBid) SetBidAmount(v Coin)`

SetBidAmount sets BidAmount field to given value.


### GetSubmitHeight

`func (o *IntentAuctionBid) GetSubmitHeight() int64`

GetSubmitHeight returns the SubmitHeight field if non-nil, zero value otherwise.

### GetSubmitHeightOk

`func (o *IntentAuctionBid) GetSubmitHeightOk() (*int64, bool)`

GetSubmitHeightOk returns a tuple with the SubmitHeight field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetSubmitHeight

`func (o *IntentAuctionBid) SetSubmitHeight(v int64)`

SetSubmitHeight sets SubmitHeight field to given value.



[[Back to Model list]](../README.md#documentation-for-models) [[Back to API list]](../README.md#documentation-for-api-endpoints) [[Back to README]](../README.md)


