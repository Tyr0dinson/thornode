# Intent

## Properties

Name | Type | Description | Notes
------------ | ------------- | ------------- | -------------
**TxId** | **string** | the transaction ID of the intent | 
**Source** | [**Coin**](Coin.md) |  | 
**SourceAddress** | **string** | the source address | 
**TargetAsset** | **string** | the target asset | 
**TargetAddress** | **string** | the target address | 
**HighTarget** | **string** | the high target amount | 
**LowTarget** | **string** | the low target amount | 
**CreateHeight** | **int64** | the block height when the intent was created | 
**ExpiryHeight** | **int64** | the block height when the intent expires | 
**Status** | **string** | the status of the intent | 
**MinFeeBasisPoints** | **int64** | minimum fee basis points | 
**AffiliateAddress** | Pointer to **string** | affiliate address | [optional] 
**AffiliateBasisPoints** | Pointer to **int64** | affiliate fee basis points | [optional] 

## Methods

### NewIntent

`func NewIntent(txId string, source Coin, sourceAddress string, targetAsset string, targetAddress string, highTarget string, lowTarget string, createHeight int64, expiryHeight int64, status string, minFeeBasisPoints int64, ) *Intent`

NewIntent instantiates a new Intent object
This constructor will assign default values to properties that have it defined,
and makes sure properties required by API are set, but the set of arguments
will change when the set of required properties is changed

### NewIntentWithDefaults

`func NewIntentWithDefaults() *Intent`

NewIntentWithDefaults instantiates a new Intent object
This constructor will only assign default values to properties that have it defined,
but it doesn't guarantee that properties required by API are set

### GetTxId

`func (o *Intent) GetTxId() string`

GetTxId returns the TxId field if non-nil, zero value otherwise.

### GetTxIdOk

`func (o *Intent) GetTxIdOk() (*string, bool)`

GetTxIdOk returns a tuple with the TxId field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetTxId

`func (o *Intent) SetTxId(v string)`

SetTxId sets TxId field to given value.


### GetSource

`func (o *Intent) GetSource() Coin`

GetSource returns the Source field if non-nil, zero value otherwise.

### GetSourceOk

`func (o *Intent) GetSourceOk() (*Coin, bool)`

GetSourceOk returns a tuple with the Source field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetSource

`func (o *Intent) SetSource(v Coin)`

SetSource sets Source field to given value.


### GetSourceAddress

`func (o *Intent) GetSourceAddress() string`

GetSourceAddress returns the SourceAddress field if non-nil, zero value otherwise.

### GetSourceAddressOk

`func (o *Intent) GetSourceAddressOk() (*string, bool)`

GetSourceAddressOk returns a tuple with the SourceAddress field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetSourceAddress

`func (o *Intent) SetSourceAddress(v string)`

SetSourceAddress sets SourceAddress field to given value.


### GetTargetAsset

`func (o *Intent) GetTargetAsset() string`

GetTargetAsset returns the TargetAsset field if non-nil, zero value otherwise.

### GetTargetAssetOk

`func (o *Intent) GetTargetAssetOk() (*string, bool)`

GetTargetAssetOk returns a tuple with the TargetAsset field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetTargetAsset

`func (o *Intent) SetTargetAsset(v string)`

SetTargetAsset sets TargetAsset field to given value.


### GetTargetAddress

`func (o *Intent) GetTargetAddress() string`

GetTargetAddress returns the TargetAddress field if non-nil, zero value otherwise.

### GetTargetAddressOk

`func (o *Intent) GetTargetAddressOk() (*string, bool)`

GetTargetAddressOk returns a tuple with the TargetAddress field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetTargetAddress

`func (o *Intent) SetTargetAddress(v string)`

SetTargetAddress sets TargetAddress field to given value.


### GetHighTarget

`func (o *Intent) GetHighTarget() string`

GetHighTarget returns the HighTarget field if non-nil, zero value otherwise.

### GetHighTargetOk

`func (o *Intent) GetHighTargetOk() (*string, bool)`

GetHighTargetOk returns a tuple with the HighTarget field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetHighTarget

`func (o *Intent) SetHighTarget(v string)`

SetHighTarget sets HighTarget field to given value.


### GetLowTarget

`func (o *Intent) GetLowTarget() string`

GetLowTarget returns the LowTarget field if non-nil, zero value otherwise.

### GetLowTargetOk

`func (o *Intent) GetLowTargetOk() (*string, bool)`

GetLowTargetOk returns a tuple with the LowTarget field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetLowTarget

`func (o *Intent) SetLowTarget(v string)`

SetLowTarget sets LowTarget field to given value.


### GetCreateHeight

`func (o *Intent) GetCreateHeight() int64`

GetCreateHeight returns the CreateHeight field if non-nil, zero value otherwise.

### GetCreateHeightOk

`func (o *Intent) GetCreateHeightOk() (*int64, bool)`

GetCreateHeightOk returns a tuple with the CreateHeight field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetCreateHeight

`func (o *Intent) SetCreateHeight(v int64)`

SetCreateHeight sets CreateHeight field to given value.


### GetExpiryHeight

`func (o *Intent) GetExpiryHeight() int64`

GetExpiryHeight returns the ExpiryHeight field if non-nil, zero value otherwise.

### GetExpiryHeightOk

`func (o *Intent) GetExpiryHeightOk() (*int64, bool)`

GetExpiryHeightOk returns a tuple with the ExpiryHeight field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetExpiryHeight

`func (o *Intent) SetExpiryHeight(v int64)`

SetExpiryHeight sets ExpiryHeight field to given value.


### GetStatus

`func (o *Intent) GetStatus() string`

GetStatus returns the Status field if non-nil, zero value otherwise.

### GetStatusOk

`func (o *Intent) GetStatusOk() (*string, bool)`

GetStatusOk returns a tuple with the Status field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetStatus

`func (o *Intent) SetStatus(v string)`

SetStatus sets Status field to given value.


### GetMinFeeBasisPoints

`func (o *Intent) GetMinFeeBasisPoints() int64`

GetMinFeeBasisPoints returns the MinFeeBasisPoints field if non-nil, zero value otherwise.

### GetMinFeeBasisPointsOk

`func (o *Intent) GetMinFeeBasisPointsOk() (*int64, bool)`

GetMinFeeBasisPointsOk returns a tuple with the MinFeeBasisPoints field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetMinFeeBasisPoints

`func (o *Intent) SetMinFeeBasisPoints(v int64)`

SetMinFeeBasisPoints sets MinFeeBasisPoints field to given value.


### GetAffiliateAddress

`func (o *Intent) GetAffiliateAddress() string`

GetAffiliateAddress returns the AffiliateAddress field if non-nil, zero value otherwise.

### GetAffiliateAddressOk

`func (o *Intent) GetAffiliateAddressOk() (*string, bool)`

GetAffiliateAddressOk returns a tuple with the AffiliateAddress field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetAffiliateAddress

`func (o *Intent) SetAffiliateAddress(v string)`

SetAffiliateAddress sets AffiliateAddress field to given value.

### HasAffiliateAddress

`func (o *Intent) HasAffiliateAddress() bool`

HasAffiliateAddress returns a boolean if a field has been set.

### GetAffiliateBasisPoints

`func (o *Intent) GetAffiliateBasisPoints() int64`

GetAffiliateBasisPoints returns the AffiliateBasisPoints field if non-nil, zero value otherwise.

### GetAffiliateBasisPointsOk

`func (o *Intent) GetAffiliateBasisPointsOk() (*int64, bool)`

GetAffiliateBasisPointsOk returns a tuple with the AffiliateBasisPoints field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetAffiliateBasisPoints

`func (o *Intent) SetAffiliateBasisPoints(v int64)`

SetAffiliateBasisPoints sets AffiliateBasisPoints field to given value.

### HasAffiliateBasisPoints

`func (o *Intent) HasAffiliateBasisPoints() bool`

HasAffiliateBasisPoints returns a boolean if a field has been set.


[[Back to Model list]](../README.md#documentation-for-models) [[Back to API list]](../README.md#documentation-for-api-endpoints) [[Back to README]](../README.md)


