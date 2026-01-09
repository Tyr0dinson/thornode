# StagedOutboundItem

## Properties

Name | Type | Description | Notes
------------ | ------------- | ------------- | -------------
**TxOutItem** | Pointer to [**TxOutItem**](TxOutItem.md) |  | [optional] 
**ScheduledHeight** | Pointer to **int64** | Pre-computed target height (delay countdown starts at staging time) | [optional] 
**StagedHeight** | Pointer to **int64** | Block height when this outbound was staged | [optional] 

## Methods

### NewStagedOutboundItem

`func NewStagedOutboundItem() *StagedOutboundItem`

NewStagedOutboundItem instantiates a new StagedOutboundItem object
This constructor will assign default values to properties that have it defined,
and makes sure properties required by API are set, but the set of arguments
will change when the set of required properties is changed

### NewStagedOutboundItemWithDefaults

`func NewStagedOutboundItemWithDefaults() *StagedOutboundItem`

NewStagedOutboundItemWithDefaults instantiates a new StagedOutboundItem object
This constructor will only assign default values to properties that have it defined,
but it doesn't guarantee that properties required by API are set

### GetTxOutItem

`func (o *StagedOutboundItem) GetTxOutItem() TxOutItem`

GetTxOutItem returns the TxOutItem field if non-nil, zero value otherwise.

### GetTxOutItemOk

`func (o *StagedOutboundItem) GetTxOutItemOk() (*TxOutItem, bool)`

GetTxOutItemOk returns a tuple with the TxOutItem field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetTxOutItem

`func (o *StagedOutboundItem) SetTxOutItem(v TxOutItem)`

SetTxOutItem sets TxOutItem field to given value.

### HasTxOutItem

`func (o *StagedOutboundItem) HasTxOutItem() bool`

HasTxOutItem returns a boolean if a field has been set.

### GetScheduledHeight

`func (o *StagedOutboundItem) GetScheduledHeight() int64`

GetScheduledHeight returns the ScheduledHeight field if non-nil, zero value otherwise.

### GetScheduledHeightOk

`func (o *StagedOutboundItem) GetScheduledHeightOk() (*int64, bool)`

GetScheduledHeightOk returns a tuple with the ScheduledHeight field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetScheduledHeight

`func (o *StagedOutboundItem) SetScheduledHeight(v int64)`

SetScheduledHeight sets ScheduledHeight field to given value.

### HasScheduledHeight

`func (o *StagedOutboundItem) HasScheduledHeight() bool`

HasScheduledHeight returns a boolean if a field has been set.

### GetStagedHeight

`func (o *StagedOutboundItem) GetStagedHeight() int64`

GetStagedHeight returns the StagedHeight field if non-nil, zero value otherwise.

### GetStagedHeightOk

`func (o *StagedOutboundItem) GetStagedHeightOk() (*int64, bool)`

GetStagedHeightOk returns a tuple with the StagedHeight field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetStagedHeight

`func (o *StagedOutboundItem) SetStagedHeight(v int64)`

SetStagedHeight sets StagedHeight field to given value.

### HasStagedHeight

`func (o *StagedOutboundItem) HasStagedHeight() bool`

HasStagedHeight returns a boolean if a field has been set.


[[Back to Model list]](../README.md#documentation-for-models) [[Back to API list]](../README.md#documentation-for-api-endpoints) [[Back to README]](../README.md)


