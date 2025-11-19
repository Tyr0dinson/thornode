# IntentSolve

## Properties

Name | Type | Description | Notes
------------ | ------------- | ------------- | -------------
**SolveTxId** | **string** | the transaction ID of the solve bid | 
**IntentTxId** | **string** | the transaction ID of the intent being solved | 
**SolverAddress** | **string** | the solver address | 
**TargetCoin** | [**Coin**](Coin.md) |  | 
**VaultPubKey** | **string** | the vault public key | 
**CreateHeight** | **int64** | the block height when the solve was created | 
**MatchHeight** | **int64** | the block height when the solve should be matched | 

## Methods

### NewIntentSolve

`func NewIntentSolve(solveTxId string, intentTxId string, solverAddress string, targetCoin Coin, vaultPubKey string, createHeight int64, matchHeight int64, ) *IntentSolve`

NewIntentSolve instantiates a new IntentSolve object
This constructor will assign default values to properties that have it defined,
and makes sure properties required by API are set, but the set of arguments
will change when the set of required properties is changed

### NewIntentSolveWithDefaults

`func NewIntentSolveWithDefaults() *IntentSolve`

NewIntentSolveWithDefaults instantiates a new IntentSolve object
This constructor will only assign default values to properties that have it defined,
but it doesn't guarantee that properties required by API are set

### GetSolveTxId

`func (o *IntentSolve) GetSolveTxId() string`

GetSolveTxId returns the SolveTxId field if non-nil, zero value otherwise.

### GetSolveTxIdOk

`func (o *IntentSolve) GetSolveTxIdOk() (*string, bool)`

GetSolveTxIdOk returns a tuple with the SolveTxId field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetSolveTxId

`func (o *IntentSolve) SetSolveTxId(v string)`

SetSolveTxId sets SolveTxId field to given value.


### GetIntentTxId

`func (o *IntentSolve) GetIntentTxId() string`

GetIntentTxId returns the IntentTxId field if non-nil, zero value otherwise.

### GetIntentTxIdOk

`func (o *IntentSolve) GetIntentTxIdOk() (*string, bool)`

GetIntentTxIdOk returns a tuple with the IntentTxId field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetIntentTxId

`func (o *IntentSolve) SetIntentTxId(v string)`

SetIntentTxId sets IntentTxId field to given value.


### GetSolverAddress

`func (o *IntentSolve) GetSolverAddress() string`

GetSolverAddress returns the SolverAddress field if non-nil, zero value otherwise.

### GetSolverAddressOk

`func (o *IntentSolve) GetSolverAddressOk() (*string, bool)`

GetSolverAddressOk returns a tuple with the SolverAddress field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetSolverAddress

`func (o *IntentSolve) SetSolverAddress(v string)`

SetSolverAddress sets SolverAddress field to given value.


### GetTargetCoin

`func (o *IntentSolve) GetTargetCoin() Coin`

GetTargetCoin returns the TargetCoin field if non-nil, zero value otherwise.

### GetTargetCoinOk

`func (o *IntentSolve) GetTargetCoinOk() (*Coin, bool)`

GetTargetCoinOk returns a tuple with the TargetCoin field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetTargetCoin

`func (o *IntentSolve) SetTargetCoin(v Coin)`

SetTargetCoin sets TargetCoin field to given value.


### GetVaultPubKey

`func (o *IntentSolve) GetVaultPubKey() string`

GetVaultPubKey returns the VaultPubKey field if non-nil, zero value otherwise.

### GetVaultPubKeyOk

`func (o *IntentSolve) GetVaultPubKeyOk() (*string, bool)`

GetVaultPubKeyOk returns a tuple with the VaultPubKey field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetVaultPubKey

`func (o *IntentSolve) SetVaultPubKey(v string)`

SetVaultPubKey sets VaultPubKey field to given value.


### GetCreateHeight

`func (o *IntentSolve) GetCreateHeight() int64`

GetCreateHeight returns the CreateHeight field if non-nil, zero value otherwise.

### GetCreateHeightOk

`func (o *IntentSolve) GetCreateHeightOk() (*int64, bool)`

GetCreateHeightOk returns a tuple with the CreateHeight field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetCreateHeight

`func (o *IntentSolve) SetCreateHeight(v int64)`

SetCreateHeight sets CreateHeight field to given value.


### GetMatchHeight

`func (o *IntentSolve) GetMatchHeight() int64`

GetMatchHeight returns the MatchHeight field if non-nil, zero value otherwise.

### GetMatchHeightOk

`func (o *IntentSolve) GetMatchHeightOk() (*int64, bool)`

GetMatchHeightOk returns a tuple with the MatchHeight field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetMatchHeight

`func (o *IntentSolve) SetMatchHeight(v int64)`

SetMatchHeight sets MatchHeight field to given value.



[[Back to Model list]](../README.md#documentation-for-models) [[Back to API list]](../README.md#documentation-for-api-endpoints) [[Back to README]](../README.md)


