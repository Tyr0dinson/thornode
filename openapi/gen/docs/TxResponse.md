# TxResponse

## Properties

Name | Type | Description | Notes
------------ | ------------- | ------------- | -------------
**ObservedTx** | Pointer to [**ObservedTx**](ObservedTx.md) |  | [optional] 
**KeysignMetric** | Pointer to [**TssKeysignMetric**](TssKeysignMetric.md) |  | [optional] 

## Methods

### NewTxResponse

`func NewTxResponse() *TxResponse`

NewTxResponse instantiates a new TxResponse object
This constructor will assign default values to properties that have it defined,
and makes sure properties required by API are set, but the set of arguments
will change when the set of required properties is changed

### NewTxResponseWithDefaults

`func NewTxResponseWithDefaults() *TxResponse`

NewTxResponseWithDefaults instantiates a new TxResponse object
This constructor will only assign default values to properties that have it defined,
but it doesn't guarantee that properties required by API are set

### GetObservedTx

`func (o *TxResponse) GetObservedTx() ObservedTx`

GetObservedTx returns the ObservedTx field if non-nil, zero value otherwise.

### GetObservedTxOk

`func (o *TxResponse) GetObservedTxOk() (*ObservedTx, bool)`

GetObservedTxOk returns a tuple with the ObservedTx field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetObservedTx

`func (o *TxResponse) SetObservedTx(v ObservedTx)`

SetObservedTx sets ObservedTx field to given value.

### HasObservedTx

`func (o *TxResponse) HasObservedTx() bool`

HasObservedTx returns a boolean if a field has been set.

### GetKeysignMetric

`func (o *TxResponse) GetKeysignMetric() TssKeysignMetric`

GetKeysignMetric returns the KeysignMetric field if non-nil, zero value otherwise.

### GetKeysignMetricOk

`func (o *TxResponse) GetKeysignMetricOk() (*TssKeysignMetric, bool)`

GetKeysignMetricOk returns a tuple with the KeysignMetric field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetKeysignMetric

`func (o *TxResponse) SetKeysignMetric(v TssKeysignMetric)`

SetKeysignMetric sets KeysignMetric field to given value.

### HasKeysignMetric

`func (o *TxResponse) HasKeysignMetric() bool`

HasKeysignMetric returns a boolean if a field has been set.


[[Back to Model list]](../README.md#documentation-for-models) [[Back to API list]](../README.md#documentation-for-api-endpoints) [[Back to README]](../README.md)


