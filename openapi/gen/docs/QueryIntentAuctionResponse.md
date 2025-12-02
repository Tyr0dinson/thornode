# QueryIntentAuctionResponse

## Properties

Name | Type | Description | Notes
------------ | ------------- | ------------- | -------------
**Auction** | [**IntentAuction**](IntentAuction.md) |  | 
**Bids** | [**[]IntentAuctionBid**](IntentAuctionBid.md) |  | 

## Methods

### NewQueryIntentAuctionResponse

`func NewQueryIntentAuctionResponse(auction IntentAuction, bids []IntentAuctionBid, ) *QueryIntentAuctionResponse`

NewQueryIntentAuctionResponse instantiates a new QueryIntentAuctionResponse object
This constructor will assign default values to properties that have it defined,
and makes sure properties required by API are set, but the set of arguments
will change when the set of required properties is changed

### NewQueryIntentAuctionResponseWithDefaults

`func NewQueryIntentAuctionResponseWithDefaults() *QueryIntentAuctionResponse`

NewQueryIntentAuctionResponseWithDefaults instantiates a new QueryIntentAuctionResponse object
This constructor will only assign default values to properties that have it defined,
but it doesn't guarantee that properties required by API are set

### GetAuction

`func (o *QueryIntentAuctionResponse) GetAuction() IntentAuction`

GetAuction returns the Auction field if non-nil, zero value otherwise.

### GetAuctionOk

`func (o *QueryIntentAuctionResponse) GetAuctionOk() (*IntentAuction, bool)`

GetAuctionOk returns a tuple with the Auction field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetAuction

`func (o *QueryIntentAuctionResponse) SetAuction(v IntentAuction)`

SetAuction sets Auction field to given value.


### GetBids

`func (o *QueryIntentAuctionResponse) GetBids() []IntentAuctionBid`

GetBids returns the Bids field if non-nil, zero value otherwise.

### GetBidsOk

`func (o *QueryIntentAuctionResponse) GetBidsOk() (*[]IntentAuctionBid, bool)`

GetBidsOk returns a tuple with the Bids field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetBids

`func (o *QueryIntentAuctionResponse) SetBids(v []IntentAuctionBid)`

SetBids sets Bids field to given value.



[[Back to Model list]](../README.md#documentation-for-models) [[Back to API list]](../README.md#documentation-for-api-endpoints) [[Back to README]](../README.md)


