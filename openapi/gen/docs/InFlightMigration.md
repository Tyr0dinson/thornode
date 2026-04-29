# InFlightMigration

## Properties

Name | Type | Description | Notes
------------ | ------------- | ------------- | -------------
**TxId** | Pointer to **string** | outbound tx hash used as the in-flight migration index key | [optional] 
**SourceVaultPubKey** | Pointer to **string** | source vault pubkey stored in the in-flight migration index | [optional] 
**SourceVaultStatus** | Pointer to **string** |  | [optional] 
**DestinationVaultPubKey** | Pointer to **string** | destination vault pubkey resolved from the outbound destination address | [optional] 
**TxOut** | Pointer to [**TxOutItem**](TxOutItem.md) |  | [optional] 
**UpdatedVault** | Pointer to **bool** | true when the matching inbound migration already updated the destination vault | [optional] 

## Methods

### NewInFlightMigration

`func NewInFlightMigration() *InFlightMigration`

NewInFlightMigration instantiates a new InFlightMigration object
This constructor will assign default values to properties that have it defined,
and makes sure properties required by API are set, but the set of arguments
will change when the set of required properties is changed

### NewInFlightMigrationWithDefaults

`func NewInFlightMigrationWithDefaults() *InFlightMigration`

NewInFlightMigrationWithDefaults instantiates a new InFlightMigration object
This constructor will only assign default values to properties that have it defined,
but it doesn't guarantee that properties required by API are set

### GetTxId

`func (o *InFlightMigration) GetTxId() string`

GetTxId returns the TxId field if non-nil, zero value otherwise.

### GetTxIdOk

`func (o *InFlightMigration) GetTxIdOk() (*string, bool)`

GetTxIdOk returns a tuple with the TxId field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetTxId

`func (o *InFlightMigration) SetTxId(v string)`

SetTxId sets TxId field to given value.

### HasTxId

`func (o *InFlightMigration) HasTxId() bool`

HasTxId returns a boolean if a field has been set.

### GetSourceVaultPubKey

`func (o *InFlightMigration) GetSourceVaultPubKey() string`

GetSourceVaultPubKey returns the SourceVaultPubKey field if non-nil, zero value otherwise.

### GetSourceVaultPubKeyOk

`func (o *InFlightMigration) GetSourceVaultPubKeyOk() (*string, bool)`

GetSourceVaultPubKeyOk returns a tuple with the SourceVaultPubKey field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetSourceVaultPubKey

`func (o *InFlightMigration) SetSourceVaultPubKey(v string)`

SetSourceVaultPubKey sets SourceVaultPubKey field to given value.

### HasSourceVaultPubKey

`func (o *InFlightMigration) HasSourceVaultPubKey() bool`

HasSourceVaultPubKey returns a boolean if a field has been set.

### GetSourceVaultStatus

`func (o *InFlightMigration) GetSourceVaultStatus() string`

GetSourceVaultStatus returns the SourceVaultStatus field if non-nil, zero value otherwise.

### GetSourceVaultStatusOk

`func (o *InFlightMigration) GetSourceVaultStatusOk() (*string, bool)`

GetSourceVaultStatusOk returns a tuple with the SourceVaultStatus field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetSourceVaultStatus

`func (o *InFlightMigration) SetSourceVaultStatus(v string)`

SetSourceVaultStatus sets SourceVaultStatus field to given value.

### HasSourceVaultStatus

`func (o *InFlightMigration) HasSourceVaultStatus() bool`

HasSourceVaultStatus returns a boolean if a field has been set.

### GetDestinationVaultPubKey

`func (o *InFlightMigration) GetDestinationVaultPubKey() string`

GetDestinationVaultPubKey returns the DestinationVaultPubKey field if non-nil, zero value otherwise.

### GetDestinationVaultPubKeyOk

`func (o *InFlightMigration) GetDestinationVaultPubKeyOk() (*string, bool)`

GetDestinationVaultPubKeyOk returns a tuple with the DestinationVaultPubKey field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetDestinationVaultPubKey

`func (o *InFlightMigration) SetDestinationVaultPubKey(v string)`

SetDestinationVaultPubKey sets DestinationVaultPubKey field to given value.

### HasDestinationVaultPubKey

`func (o *InFlightMigration) HasDestinationVaultPubKey() bool`

HasDestinationVaultPubKey returns a boolean if a field has been set.

### GetTxOut

`func (o *InFlightMigration) GetTxOut() TxOutItem`

GetTxOut returns the TxOut field if non-nil, zero value otherwise.

### GetTxOutOk

`func (o *InFlightMigration) GetTxOutOk() (*TxOutItem, bool)`

GetTxOutOk returns a tuple with the TxOut field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetTxOut

`func (o *InFlightMigration) SetTxOut(v TxOutItem)`

SetTxOut sets TxOut field to given value.

### HasTxOut

`func (o *InFlightMigration) HasTxOut() bool`

HasTxOut returns a boolean if a field has been set.

### GetUpdatedVault

`func (o *InFlightMigration) GetUpdatedVault() bool`

GetUpdatedVault returns the UpdatedVault field if non-nil, zero value otherwise.

### GetUpdatedVaultOk

`func (o *InFlightMigration) GetUpdatedVaultOk() (*bool, bool)`

GetUpdatedVaultOk returns a tuple with the UpdatedVault field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetUpdatedVault

`func (o *InFlightMigration) SetUpdatedVault(v bool)`

SetUpdatedVault sets UpdatedVault field to given value.

### HasUpdatedVault

`func (o *InFlightMigration) HasUpdatedVault() bool`

HasUpdatedVault returns a boolean if a field has been set.


[[Back to Model list]](../README.md#documentation-for-models) [[Back to API list]](../README.md#documentation-for-api-endpoints) [[Back to README]](../README.md)


