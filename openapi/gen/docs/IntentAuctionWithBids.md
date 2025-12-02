# IntentAuctionWithBids

## Properties

Name | Type | Description | Notes
------------ | ------------- | ------------- | -------------
**Auction** | [**IntentAuction**](IntentAuction.md) |  | 
**Bids** | [**[]IntentAuctionBid**](IntentAuctionBid.md) |  | 

## Methods

### NewIntentAuctionWithBids

`func NewIntentAuctionWithBids(auction IntentAuction, bids []IntentAuctionBid, ) *IntentAuctionWithBids`

NewIntentAuctionWithBids instantiates a new IntentAuctionWithBids object
This constructor will assign default values to properties that have it defined,
and makes sure properties required by API are set, but the set of arguments
will change when the set of required properties is changed

### NewIntentAuctionWithBidsWithDefaults

`func NewIntentAuctionWithBidsWithDefaults() *IntentAuctionWithBids`

NewIntentAuctionWithBidsWithDefaults instantiates a new IntentAuctionWithBids object
This constructor will only assign default values to properties that have it defined,
but it doesn't guarantee that properties required by API are set

### GetAuction

`func (o *IntentAuctionWithBids) GetAuction() IntentAuction`

GetAuction returns the Auction field if non-nil, zero value otherwise.

### GetAuctionOk

`func (o *IntentAuctionWithBids) GetAuctionOk() (*IntentAuction, bool)`

GetAuctionOk returns a tuple with the Auction field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetAuction

`func (o *IntentAuctionWithBids) SetAuction(v IntentAuction)`

SetAuction sets Auction field to given value.


### GetBids

`func (o *IntentAuctionWithBids) GetBids() []IntentAuctionBid`

GetBids returns the Bids field if non-nil, zero value otherwise.

### GetBidsOk

`func (o *IntentAuctionWithBids) GetBidsOk() (*[]IntentAuctionBid, bool)`

GetBidsOk returns a tuple with the Bids field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetBids

`func (o *IntentAuctionWithBids) SetBids(v []IntentAuctionBid)`

SetBids sets Bids field to given value.



[[Back to Model list]](../README.md#documentation-for-models) [[Back to API list]](../README.md#documentation-for-api-endpoints) [[Back to README]](../README.md)


