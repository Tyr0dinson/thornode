# QuoteIntentAuctionInfo

## Properties

Name | Type | Description | Notes
------------ | ------------- | ------------- | -------------
**WillAuction** | Pointer to **bool** | whether this intent would trigger auction on expiry | [optional] 
**AuctionPercentageBps** | Pointer to **int64** | percentage of source auctioned (basis points) | [optional] 
**RemainingToOwnerBps** | Pointer to **int64** | percentage returned to owner (basis points) | [optional] 
**BidAsset** | Pointer to **string** | asset used for auction bidding | [optional] 
**AuctionDurationBlocks** | Pointer to **int64** | auction duration in blocks | [optional] 

## Methods

### NewQuoteIntentAuctionInfo

`func NewQuoteIntentAuctionInfo() *QuoteIntentAuctionInfo`

NewQuoteIntentAuctionInfo instantiates a new QuoteIntentAuctionInfo object
This constructor will assign default values to properties that have it defined,
and makes sure properties required by API are set, but the set of arguments
will change when the set of required properties is changed

### NewQuoteIntentAuctionInfoWithDefaults

`func NewQuoteIntentAuctionInfoWithDefaults() *QuoteIntentAuctionInfo`

NewQuoteIntentAuctionInfoWithDefaults instantiates a new QuoteIntentAuctionInfo object
This constructor will only assign default values to properties that have it defined,
but it doesn't guarantee that properties required by API are set

### GetWillAuction

`func (o *QuoteIntentAuctionInfo) GetWillAuction() bool`

GetWillAuction returns the WillAuction field if non-nil, zero value otherwise.

### GetWillAuctionOk

`func (o *QuoteIntentAuctionInfo) GetWillAuctionOk() (*bool, bool)`

GetWillAuctionOk returns a tuple with the WillAuction field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetWillAuction

`func (o *QuoteIntentAuctionInfo) SetWillAuction(v bool)`

SetWillAuction sets WillAuction field to given value.

### HasWillAuction

`func (o *QuoteIntentAuctionInfo) HasWillAuction() bool`

HasWillAuction returns a boolean if a field has been set.

### GetAuctionPercentageBps

`func (o *QuoteIntentAuctionInfo) GetAuctionPercentageBps() int64`

GetAuctionPercentageBps returns the AuctionPercentageBps field if non-nil, zero value otherwise.

### GetAuctionPercentageBpsOk

`func (o *QuoteIntentAuctionInfo) GetAuctionPercentageBpsOk() (*int64, bool)`

GetAuctionPercentageBpsOk returns a tuple with the AuctionPercentageBps field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetAuctionPercentageBps

`func (o *QuoteIntentAuctionInfo) SetAuctionPercentageBps(v int64)`

SetAuctionPercentageBps sets AuctionPercentageBps field to given value.

### HasAuctionPercentageBps

`func (o *QuoteIntentAuctionInfo) HasAuctionPercentageBps() bool`

HasAuctionPercentageBps returns a boolean if a field has been set.

### GetRemainingToOwnerBps

`func (o *QuoteIntentAuctionInfo) GetRemainingToOwnerBps() int64`

GetRemainingToOwnerBps returns the RemainingToOwnerBps field if non-nil, zero value otherwise.

### GetRemainingToOwnerBpsOk

`func (o *QuoteIntentAuctionInfo) GetRemainingToOwnerBpsOk() (*int64, bool)`

GetRemainingToOwnerBpsOk returns a tuple with the RemainingToOwnerBps field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetRemainingToOwnerBps

`func (o *QuoteIntentAuctionInfo) SetRemainingToOwnerBps(v int64)`

SetRemainingToOwnerBps sets RemainingToOwnerBps field to given value.

### HasRemainingToOwnerBps

`func (o *QuoteIntentAuctionInfo) HasRemainingToOwnerBps() bool`

HasRemainingToOwnerBps returns a boolean if a field has been set.

### GetBidAsset

`func (o *QuoteIntentAuctionInfo) GetBidAsset() string`

GetBidAsset returns the BidAsset field if non-nil, zero value otherwise.

### GetBidAssetOk

`func (o *QuoteIntentAuctionInfo) GetBidAssetOk() (*string, bool)`

GetBidAssetOk returns a tuple with the BidAsset field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetBidAsset

`func (o *QuoteIntentAuctionInfo) SetBidAsset(v string)`

SetBidAsset sets BidAsset field to given value.

### HasBidAsset

`func (o *QuoteIntentAuctionInfo) HasBidAsset() bool`

HasBidAsset returns a boolean if a field has been set.

### GetAuctionDurationBlocks

`func (o *QuoteIntentAuctionInfo) GetAuctionDurationBlocks() int64`

GetAuctionDurationBlocks returns the AuctionDurationBlocks field if non-nil, zero value otherwise.

### GetAuctionDurationBlocksOk

`func (o *QuoteIntentAuctionInfo) GetAuctionDurationBlocksOk() (*int64, bool)`

GetAuctionDurationBlocksOk returns a tuple with the AuctionDurationBlocks field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetAuctionDurationBlocks

`func (o *QuoteIntentAuctionInfo) SetAuctionDurationBlocks(v int64)`

SetAuctionDurationBlocks sets AuctionDurationBlocks field to given value.

### HasAuctionDurationBlocks

`func (o *QuoteIntentAuctionInfo) HasAuctionDurationBlocks() bool`

HasAuctionDurationBlocks returns a boolean if a field has been set.


[[Back to Model list]](../README.md#documentation-for-models) [[Back to API list]](../README.md#documentation-for-api-endpoints) [[Back to README]](../README.md)


