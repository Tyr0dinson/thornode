# TxSignersResponse

## Properties

Name | Type | Description | Notes
------------ | ------------- | ------------- | -------------
**TxId** | Pointer to **string** |  | [optional] 
**Tx** | [**ObservedTx**](ObservedTx.md) |  | 
**Height** | Pointer to **int64** |  | [optional] 
**Txs** | [**[]ObservedTx**](ObservedTx.md) |  | 
**Actions** | [**[]ObservedTx**](ObservedTx.md) |  | 
**OutTxs** | **[]string** |  | 
**FinalisedHeight** | Pointer to **int64** | the thorchain height at which the outbound was finalised | [optional] 
**UpdatedVault** | Pointer to **bool** |  | [optional] 
**Reverted** | Pointer to **bool** |  | [optional] 
**OutboundHeight** | Pointer to **int64** |  | [optional] 

## Methods

### NewTxSignersResponse

`func NewTxSignersResponse(tx ObservedTx, txs []ObservedTx, actions []ObservedTx, outTxs []string, ) *TxSignersResponse`

NewTxSignersResponse instantiates a new TxSignersResponse object
This constructor will assign default values to properties that have it defined,
and makes sure properties required by API are set, but the set of arguments
will change when the set of required properties is changed

### NewTxSignersResponseWithDefaults

`func NewTxSignersResponseWithDefaults() *TxSignersResponse`

NewTxSignersResponseWithDefaults instantiates a new TxSignersResponse object
This constructor will only assign default values to properties that have it defined,
but it doesn't guarantee that properties required by API are set

### GetTxId

`func (o *TxSignersResponse) GetTxId() string`

GetTxId returns the TxId field if non-nil, zero value otherwise.

### GetTxIdOk

`func (o *TxSignersResponse) GetTxIdOk() (*string, bool)`

GetTxIdOk returns a tuple with the TxId field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetTxId

`func (o *TxSignersResponse) SetTxId(v string)`

SetTxId sets TxId field to given value.

### HasTxId

`func (o *TxSignersResponse) HasTxId() bool`

HasTxId returns a boolean if a field has been set.

### GetTx

`func (o *TxSignersResponse) GetTx() ObservedTx`

GetTx returns the Tx field if non-nil, zero value otherwise.

### GetTxOk

`func (o *TxSignersResponse) GetTxOk() (*ObservedTx, bool)`

GetTxOk returns a tuple with the Tx field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetTx

`func (o *TxSignersResponse) SetTx(v ObservedTx)`

SetTx sets Tx field to given value.


### GetHeight

`func (o *TxSignersResponse) GetHeight() int64`

GetHeight returns the Height field if non-nil, zero value otherwise.

### GetHeightOk

`func (o *TxSignersResponse) GetHeightOk() (*int64, bool)`

GetHeightOk returns a tuple with the Height field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetHeight

`func (o *TxSignersResponse) SetHeight(v int64)`

SetHeight sets Height field to given value.

### HasHeight

`func (o *TxSignersResponse) HasHeight() bool`

HasHeight returns a boolean if a field has been set.

### GetTxs

`func (o *TxSignersResponse) GetTxs() []ObservedTx`

GetTxs returns the Txs field if non-nil, zero value otherwise.

### GetTxsOk

`func (o *TxSignersResponse) GetTxsOk() (*[]ObservedTx, bool)`

GetTxsOk returns a tuple with the Txs field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetTxs

`func (o *TxSignersResponse) SetTxs(v []ObservedTx)`

SetTxs sets Txs field to given value.


### GetActions

`func (o *TxSignersResponse) GetActions() []ObservedTx`

GetActions returns the Actions field if non-nil, zero value otherwise.

### GetActionsOk

`func (o *TxSignersResponse) GetActionsOk() (*[]ObservedTx, bool)`

GetActionsOk returns a tuple with the Actions field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetActions

`func (o *TxSignersResponse) SetActions(v []ObservedTx)`

SetActions sets Actions field to given value.


### GetOutTxs

`func (o *TxSignersResponse) GetOutTxs() []string`

GetOutTxs returns the OutTxs field if non-nil, zero value otherwise.

### GetOutTxsOk

`func (o *TxSignersResponse) GetOutTxsOk() (*[]string, bool)`

GetOutTxsOk returns a tuple with the OutTxs field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetOutTxs

`func (o *TxSignersResponse) SetOutTxs(v []string)`

SetOutTxs sets OutTxs field to given value.


### GetFinalisedHeight

`func (o *TxSignersResponse) GetFinalisedHeight() int64`

GetFinalisedHeight returns the FinalisedHeight field if non-nil, zero value otherwise.

### GetFinalisedHeightOk

`func (o *TxSignersResponse) GetFinalisedHeightOk() (*int64, bool)`

GetFinalisedHeightOk returns a tuple with the FinalisedHeight field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetFinalisedHeight

`func (o *TxSignersResponse) SetFinalisedHeight(v int64)`

SetFinalisedHeight sets FinalisedHeight field to given value.

### HasFinalisedHeight

`func (o *TxSignersResponse) HasFinalisedHeight() bool`

HasFinalisedHeight returns a boolean if a field has been set.

### GetUpdatedVault

`func (o *TxSignersResponse) GetUpdatedVault() bool`

GetUpdatedVault returns the UpdatedVault field if non-nil, zero value otherwise.

### GetUpdatedVaultOk

`func (o *TxSignersResponse) GetUpdatedVaultOk() (*bool, bool)`

GetUpdatedVaultOk returns a tuple with the UpdatedVault field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetUpdatedVault

`func (o *TxSignersResponse) SetUpdatedVault(v bool)`

SetUpdatedVault sets UpdatedVault field to given value.

### HasUpdatedVault

`func (o *TxSignersResponse) HasUpdatedVault() bool`

HasUpdatedVault returns a boolean if a field has been set.

### GetReverted

`func (o *TxSignersResponse) GetReverted() bool`

GetReverted returns the Reverted field if non-nil, zero value otherwise.

### GetRevertedOk

`func (o *TxSignersResponse) GetRevertedOk() (*bool, bool)`

GetRevertedOk returns a tuple with the Reverted field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetReverted

`func (o *TxSignersResponse) SetReverted(v bool)`

SetReverted sets Reverted field to given value.

### HasReverted

`func (o *TxSignersResponse) HasReverted() bool`

HasReverted returns a boolean if a field has been set.

### GetOutboundHeight

`func (o *TxSignersResponse) GetOutboundHeight() int64`

GetOutboundHeight returns the OutboundHeight field if non-nil, zero value otherwise.

### GetOutboundHeightOk

`func (o *TxSignersResponse) GetOutboundHeightOk() (*int64, bool)`

GetOutboundHeightOk returns a tuple with the OutboundHeight field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetOutboundHeight

`func (o *TxSignersResponse) SetOutboundHeight(v int64)`

SetOutboundHeight sets OutboundHeight field to given value.

### HasOutboundHeight

`func (o *TxSignersResponse) HasOutboundHeight() bool`

HasOutboundHeight returns a boolean if a field has been set.


[[Back to Model list]](../README.md#documentation-for-models) [[Back to API list]](../README.md#documentation-for-api-endpoints) [[Back to README]](../README.md)


