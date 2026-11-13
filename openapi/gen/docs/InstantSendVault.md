# InstantSendVault

## Properties

Name | Type | Description | Notes
------------ | ------------- | ------------- | -------------
**State** | [**InstantSendState**](InstantSendState.md) |  | 
**Coins** | [**[]Coin**](Coin.md) | Gross vault balances, including credited deposits awaiting settlement. | 
**Eligible** | **bool** | Active Asgard vault supporting the chain without a compromised-vault flag. Deposit-specific checks still apply. | 

## Methods

### NewInstantSendVault

`func NewInstantSendVault(state InstantSendState, coins []Coin, eligible bool, ) *InstantSendVault`

NewInstantSendVault instantiates a new InstantSendVault object
This constructor will assign default values to properties that have it defined,
and makes sure properties required by API are set, but the set of arguments
will change when the set of required properties is changed

### NewInstantSendVaultWithDefaults

`func NewInstantSendVaultWithDefaults() *InstantSendVault`

NewInstantSendVaultWithDefaults instantiates a new InstantSendVault object
This constructor will only assign default values to properties that have it defined,
but it doesn't guarantee that properties required by API are set

### GetState

`func (o *InstantSendVault) GetState() InstantSendState`

GetState returns the State field if non-nil, zero value otherwise.

### GetStateOk

`func (o *InstantSendVault) GetStateOk() (*InstantSendState, bool)`

GetStateOk returns a tuple with the State field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetState

`func (o *InstantSendVault) SetState(v InstantSendState)`

SetState sets State field to given value.


### GetCoins

`func (o *InstantSendVault) GetCoins() []Coin`

GetCoins returns the Coins field if non-nil, zero value otherwise.

### GetCoinsOk

`func (o *InstantSendVault) GetCoinsOk() (*[]Coin, bool)`

GetCoinsOk returns a tuple with the Coins field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetCoins

`func (o *InstantSendVault) SetCoins(v []Coin)`

SetCoins sets Coins field to given value.


### GetEligible

`func (o *InstantSendVault) GetEligible() bool`

GetEligible returns the Eligible field if non-nil, zero value otherwise.

### GetEligibleOk

`func (o *InstantSendVault) GetEligibleOk() (*bool, bool)`

GetEligibleOk returns a tuple with the Eligible field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetEligible

`func (o *InstantSendVault) SetEligible(v bool)`

SetEligible sets Eligible field to given value.



[[Back to Model list]](../README.md#documentation-for-models) [[Back to API list]](../README.md#documentation-for-api-endpoints) [[Back to README]](../README.md)


