# ChainStatus

## Properties

Name | Type | Description | Notes
------------ | ------------- | ------------- | -------------
**TradingAvailable** | [**Availability**](Availability.md) |  | 
**LpAvailable** | [**Availability**](Availability.md) |  | 
**SaversAvailable** | [**Availability**](Availability.md) |  | 
**SynthsAvailable** | [**Availability**](Availability.md) |  | 
**LendingAvailable** | [**Availability**](Availability.md) |  | 

## Methods

### NewChainStatus

`func NewChainStatus(tradingAvailable Availability, lpAvailable Availability, saversAvailable Availability, synthsAvailable Availability, lendingAvailable Availability, ) *ChainStatus`

NewChainStatus instantiates a new ChainStatus object
This constructor will assign default values to properties that have it defined,
and makes sure properties required by API are set, but the set of arguments
will change when the set of required properties is changed

### NewChainStatusWithDefaults

`func NewChainStatusWithDefaults() *ChainStatus`

NewChainStatusWithDefaults instantiates a new ChainStatus object
This constructor will only assign default values to properties that have it defined,
but it doesn't guarantee that properties required by API are set

### GetTradingAvailable

`func (o *ChainStatus) GetTradingAvailable() Availability`

GetTradingAvailable returns the TradingAvailable field if non-nil, zero value otherwise.

### GetTradingAvailableOk

`func (o *ChainStatus) GetTradingAvailableOk() (*Availability, bool)`

GetTradingAvailableOk returns a tuple with the TradingAvailable field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetTradingAvailable

`func (o *ChainStatus) SetTradingAvailable(v Availability)`

SetTradingAvailable sets TradingAvailable field to given value.


### GetLpAvailable

`func (o *ChainStatus) GetLpAvailable() Availability`

GetLpAvailable returns the LpAvailable field if non-nil, zero value otherwise.

### GetLpAvailableOk

`func (o *ChainStatus) GetLpAvailableOk() (*Availability, bool)`

GetLpAvailableOk returns a tuple with the LpAvailable field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetLpAvailable

`func (o *ChainStatus) SetLpAvailable(v Availability)`

SetLpAvailable sets LpAvailable field to given value.


### GetSaversAvailable

`func (o *ChainStatus) GetSaversAvailable() Availability`

GetSaversAvailable returns the SaversAvailable field if non-nil, zero value otherwise.

### GetSaversAvailableOk

`func (o *ChainStatus) GetSaversAvailableOk() (*Availability, bool)`

GetSaversAvailableOk returns a tuple with the SaversAvailable field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetSaversAvailable

`func (o *ChainStatus) SetSaversAvailable(v Availability)`

SetSaversAvailable sets SaversAvailable field to given value.


### GetSynthsAvailable

`func (o *ChainStatus) GetSynthsAvailable() Availability`

GetSynthsAvailable returns the SynthsAvailable field if non-nil, zero value otherwise.

### GetSynthsAvailableOk

`func (o *ChainStatus) GetSynthsAvailableOk() (*Availability, bool)`

GetSynthsAvailableOk returns a tuple with the SynthsAvailable field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetSynthsAvailable

`func (o *ChainStatus) SetSynthsAvailable(v Availability)`

SetSynthsAvailable sets SynthsAvailable field to given value.


### GetLendingAvailable

`func (o *ChainStatus) GetLendingAvailable() Availability`

GetLendingAvailable returns the LendingAvailable field if non-nil, zero value otherwise.

### GetLendingAvailableOk

`func (o *ChainStatus) GetLendingAvailableOk() (*Availability, bool)`

GetLendingAvailableOk returns a tuple with the LendingAvailable field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetLendingAvailable

`func (o *ChainStatus) SetLendingAvailable(v Availability)`

SetLendingAvailable sets LendingAvailable field to given value.



[[Back to Model list]](../README.md#documentation-for-models) [[Back to API list]](../README.md#documentation-for-api-endpoints) [[Back to README]](../README.md)


