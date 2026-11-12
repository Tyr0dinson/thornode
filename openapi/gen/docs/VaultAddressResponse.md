# VaultAddressResponse

## Properties

Name | Type | Description | Notes
------------ | ------------- | ------------- | -------------
**Pubkey** | **string** | The vault pubkey associated with the address (empty if not found) | 
**Found** | **bool** | Whether the address belongs to a vault | 

## Methods

### NewVaultAddressResponse

`func NewVaultAddressResponse(pubkey string, found bool, ) *VaultAddressResponse`

NewVaultAddressResponse instantiates a new VaultAddressResponse object
This constructor will assign default values to properties that have it defined,
and makes sure properties required by API are set, but the set of arguments
will change when the set of required properties is changed

### NewVaultAddressResponseWithDefaults

`func NewVaultAddressResponseWithDefaults() *VaultAddressResponse`

NewVaultAddressResponseWithDefaults instantiates a new VaultAddressResponse object
This constructor will only assign default values to properties that have it defined,
but it doesn't guarantee that properties required by API are set

### GetPubkey

`func (o *VaultAddressResponse) GetPubkey() string`

GetPubkey returns the Pubkey field if non-nil, zero value otherwise.

### GetPubkeyOk

`func (o *VaultAddressResponse) GetPubkeyOk() (*string, bool)`

GetPubkeyOk returns a tuple with the Pubkey field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetPubkey

`func (o *VaultAddressResponse) SetPubkey(v string)`

SetPubkey sets Pubkey field to given value.


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


