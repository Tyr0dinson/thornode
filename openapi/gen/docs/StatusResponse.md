# StatusResponse

## Properties

Name | Type | Description | Notes
------------ | ------------- | ------------- | -------------
**Global** | [**StatusResponseGlobal**](StatusResponseGlobal.md) |  | 
**Chain** | [**map[string]ChainStatus**](ChainStatus.md) |  | 
**Pool** | [**map[string]PoolStatus**](PoolStatus.md) |  | 

## Methods

### NewStatusResponse

`func NewStatusResponse(global StatusResponseGlobal, chain map[string]ChainStatus, pool map[string]PoolStatus, ) *StatusResponse`

NewStatusResponse instantiates a new StatusResponse object
This constructor will assign default values to properties that have it defined,
and makes sure properties required by API are set, but the set of arguments
will change when the set of required properties is changed

### NewStatusResponseWithDefaults

`func NewStatusResponseWithDefaults() *StatusResponse`

NewStatusResponseWithDefaults instantiates a new StatusResponse object
This constructor will only assign default values to properties that have it defined,
but it doesn't guarantee that properties required by API are set

### GetGlobal

`func (o *StatusResponse) GetGlobal() StatusResponseGlobal`

GetGlobal returns the Global field if non-nil, zero value otherwise.

### GetGlobalOk

`func (o *StatusResponse) GetGlobalOk() (*StatusResponseGlobal, bool)`

GetGlobalOk returns a tuple with the Global field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetGlobal

`func (o *StatusResponse) SetGlobal(v StatusResponseGlobal)`

SetGlobal sets Global field to given value.


### GetChain

`func (o *StatusResponse) GetChain() map[string]ChainStatus`

GetChain returns the Chain field if non-nil, zero value otherwise.

### GetChainOk

`func (o *StatusResponse) GetChainOk() (*map[string]ChainStatus, bool)`

GetChainOk returns a tuple with the Chain field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetChain

`func (o *StatusResponse) SetChain(v map[string]ChainStatus)`

SetChain sets Chain field to given value.


### GetPool

`func (o *StatusResponse) GetPool() map[string]PoolStatus`

GetPool returns the Pool field if non-nil, zero value otherwise.

### GetPoolOk

`func (o *StatusResponse) GetPoolOk() (*map[string]PoolStatus, bool)`

GetPoolOk returns a tuple with the Pool field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetPool

`func (o *StatusResponse) SetPool(v map[string]PoolStatus)`

SetPool sets Pool field to given value.



[[Back to Model list]](../README.md#documentation-for-models) [[Back to API list]](../README.md#documentation-for-api-endpoints) [[Back to README]](../README.md)


