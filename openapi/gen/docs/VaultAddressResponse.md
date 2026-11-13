# VaultAddressResponse

## Properties

Name | Type | Description | Notes
------------ | ------------- | ------------- | -------------
**PubKey** | **string** | The vault pubkey associated with the address (empty if not found) | 
**Found** | **bool** | Whether the address belongs to a vault | 

## Methods

### NewVaultAddressResponse

`func NewVaultAddressResponse(pubKey string, found bool, ) *VaultAddressResponse`

NewVaultAddressResponse instantiates a new VaultAddressResponse object
This constructor will assign default values to properties that have it defined,
and makes sure properties required by API are set, but the set of arguments
will change when the set of required properties is changed

### NewVaultAddressResponseWithDefaults

`func NewVaultAddressResponseWithDefaults() *VaultAddressResponse`

NewVaultAddressResponseWithDefaults instantiates a new VaultAddressResponse object
This constructor will only assign default values to properties that have it defined,
but it doesn't guarantee that properties required by API are set

### GetPubKey

`func (o *VaultAddressResponse) GetPubKey() string`

GetPubKey returns the PubKey field if non-nil, zero value otherwise.

### GetPubKeyOk

`func (o *VaultAddressResponse) GetPubKeyOk() (*string, bool)`

GetPubKeyOk returns a tuple with the PubKey field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetPubKey

`func (o *VaultAddressResponse) SetPubKey(v string)`

SetPubKey sets PubKey field to given value.


### GetFound

`func (o *VaultAddressResponse) GetFound() bool`

GetFound returns the Found field if non-nil, zero value otherwise.

### GetFoundOk

`func (o *VaultAddressResponse) GetFoundOk() (*bool, bool)`

GetFoundOk returns a tuple with the Found field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetFound

`func (o *VaultAddressResponse) SetFound(v bool)`

SetFound sets Found field to given value.



[[Back to Model list]](../README.md#documentation-for-models) [[Back to API list]](../README.md#documentation-for-api-endpoints) [[Back to README]](../README.md)


