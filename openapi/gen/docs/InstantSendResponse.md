# InstantSendResponse

## Properties

Name | Type | Description | Notes
------------ | ------------- | ------------- | -------------
**Vaults** | [**[]InstantSendVault**](InstantSendVault.md) |  | 
**ObservationHeight** | **string** | Agreed chain height. Initially zero; early admission waits for a positive anchor. | 
**MinAmount** | **string** | Minimum early deposit amount in base units. | 
**MaxAmount** | **string** | Network-wide maximum pending exposure in base units. | 
**Status** | [**InstantSendStatus**](InstantSendStatus.md) |  | 

## Methods

### NewInstantSendResponse

`func NewInstantSendResponse(vaults []InstantSendVault, observationHeight string, minAmount string, maxAmount string, status InstantSendStatus, ) *InstantSendResponse`

NewInstantSendResponse instantiates a new InstantSendResponse object
This constructor will assign default values to properties that have it defined,
and makes sure properties required by API are set, but the set of arguments
will change when the set of required properties is changed

### NewInstantSendResponseWithDefaults

`func NewInstantSendResponseWithDefaults() *InstantSendResponse`

NewInstantSendResponseWithDefaults instantiates a new InstantSendResponse object
This constructor will only assign default values to properties that have it defined,
but it doesn't guarantee that properties required by API are set

### GetVaults

`func (o *InstantSendResponse) GetVaults() []InstantSendVault`

GetVaults returns the Vaults field if non-nil, zero value otherwise.

### GetVaultsOk

`func (o *InstantSendResponse) GetVaultsOk() (*[]InstantSendVault, bool)`

GetVaultsOk returns a tuple with the Vaults field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetVaults

`func (o *InstantSendResponse) SetVaults(v []InstantSendVault)`

SetVaults sets Vaults field to given value.


### GetObservationHeight

`func (o *InstantSendResponse) GetObservationHeight() string`

GetObservationHeight returns the ObservationHeight field if non-nil, zero value otherwise.

### GetObservationHeightOk

`func (o *InstantSendResponse) GetObservationHeightOk() (*string, bool)`

GetObservationHeightOk returns a tuple with the ObservationHeight field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetObservationHeight

`func (o *InstantSendResponse) SetObservationHeight(v string)`

SetObservationHeight sets ObservationHeight field to given value.


### GetMinAmount

`func (o *InstantSendResponse) GetMinAmount() string`

GetMinAmount returns the MinAmount field if non-nil, zero value otherwise.

### GetMinAmountOk

`func (o *InstantSendResponse) GetMinAmountOk() (*string, bool)`

GetMinAmountOk returns a tuple with the MinAmount field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetMinAmount

`func (o *InstantSendResponse) SetMinAmount(v string)`

SetMinAmount sets MinAmount field to given value.


### GetMaxAmount

`func (o *InstantSendResponse) GetMaxAmount() string`

GetMaxAmount returns the MaxAmount field if non-nil, zero value otherwise.

### GetMaxAmountOk

`func (o *InstantSendResponse) GetMaxAmountOk() (*string, bool)`

GetMaxAmountOk returns a tuple with the MaxAmount field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetMaxAmount

`func (o *InstantSendResponse) SetMaxAmount(v string)`

SetMaxAmount sets MaxAmount field to given value.


### GetStatus

`func (o *InstantSendResponse) GetStatus() InstantSendStatus`

GetStatus returns the Status field if non-nil, zero value otherwise.

### GetStatusOk

`func (o *InstantSendResponse) GetStatusOk() (*InstantSendStatus, bool)`

GetStatusOk returns a tuple with the Status field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetStatus

`func (o *InstantSendResponse) SetStatus(v InstantSendStatus)`

SetStatus sets Status field to given value.



[[Back to Model list]](../README.md#documentation-for-models) [[Back to API list]](../README.md#documentation-for-api-endpoints) [[Back to README]](../README.md)


