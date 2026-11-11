# NodeBlameResponse

## Properties

Name | Type | Description | Notes
------------ | ------------- | ------------- | -------------
**NodeAddress** | **string** | Node address (bech32-encoded thor1... validator address). | 
**NodeOperatorAddress** | **string** | Operator (bond) address associated with the node. | 
**KeygenBlameCountTotal** | **int64** | Lifetime count of keygen blame attributions for this node. | 
**KeysignBlameCountTotal** | **int64** | Lifetime count of keysign blame attributions for this node. | 
**KeygenBlameCountSinceChurn** | **int64** | Keygen blame attributions since this node&#39;s counters were last reset. Both since-churn counters reset for a node when a regular Asgard keygen it is a member of succeeds, and for all nodes when a regular churn completes; XMR monovault keygens reset neither. During a multi-Asgard churn, members of an already-succeeded vault can therefore read 0 before the churn completes. | 
**KeysignBlameCountSinceChurn** | **int64** | Keysign blame attributions since this node&#39;s counters were last reset. Both since-churn counters reset for a node when a regular Asgard keygen it is a member of succeeds, and for all nodes when a regular churn completes; XMR monovault keygens reset neither. During a multi-Asgard churn, members of an already-succeeded vault can therefore read 0 before the churn completes. | 

## Methods

### NewNodeBlameResponse

`func NewNodeBlameResponse(nodeAddress string, nodeOperatorAddress string, keygenBlameCountTotal int64, keysignBlameCountTotal int64, keygenBlameCountSinceChurn int64, keysignBlameCountSinceChurn int64, ) *NodeBlameResponse`

NewNodeBlameResponse instantiates a new NodeBlameResponse object
This constructor will assign default values to properties that have it defined,
and makes sure properties required by API are set, but the set of arguments
will change when the set of required properties is changed

### NewNodeBlameResponseWithDefaults

`func NewNodeBlameResponseWithDefaults() *NodeBlameResponse`

NewNodeBlameResponseWithDefaults instantiates a new NodeBlameResponse object
This constructor will only assign default values to properties that have it defined,
but it doesn't guarantee that properties required by API are set

### GetNodeAddress

`func (o *NodeBlameResponse) GetNodeAddress() string`

GetNodeAddress returns the NodeAddress field if non-nil, zero value otherwise.

### GetNodeAddressOk

`func (o *NodeBlameResponse) GetNodeAddressOk() (*string, bool)`

GetNodeAddressOk returns a tuple with the NodeAddress field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetNodeAddress

`func (o *NodeBlameResponse) SetNodeAddress(v string)`

SetNodeAddress sets NodeAddress field to given value.


### GetNodeOperatorAddress

`func (o *NodeBlameResponse) GetNodeOperatorAddress() string`

GetNodeOperatorAddress returns the NodeOperatorAddress field if non-nil, zero value otherwise.

### GetNodeOperatorAddressOk

`func (o *NodeBlameResponse) GetNodeOperatorAddressOk() (*string, bool)`

GetNodeOperatorAddressOk returns a tuple with the NodeOperatorAddress field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetNodeOperatorAddress

`func (o *NodeBlameResponse) SetNodeOperatorAddress(v string)`

SetNodeOperatorAddress sets NodeOperatorAddress field to given value.


### GetKeygenBlameCountTotal

`func (o *NodeBlameResponse) GetKeygenBlameCountTotal() int64`

GetKeygenBlameCountTotal returns the KeygenBlameCountTotal field if non-nil, zero value otherwise.

### GetKeygenBlameCountTotalOk

`func (o *NodeBlameResponse) GetKeygenBlameCountTotalOk() (*int64, bool)`

GetKeygenBlameCountTotalOk returns a tuple with the KeygenBlameCountTotal field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetKeygenBlameCountTotal

`func (o *NodeBlameResponse) SetKeygenBlameCountTotal(v int64)`

SetKeygenBlameCountTotal sets KeygenBlameCountTotal field to given value.


### GetKeysignBlameCountTotal

`func (o *NodeBlameResponse) GetKeysignBlameCountTotal() int64`

GetKeysignBlameCountTotal returns the KeysignBlameCountTotal field if non-nil, zero value otherwise.

### GetKeysignBlameCountTotalOk

`func (o *NodeBlameResponse) GetKeysignBlameCountTotalOk() (*int64, bool)`

GetKeysignBlameCountTotalOk returns a tuple with the KeysignBlameCountTotal field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetKeysignBlameCountTotal

`func (o *NodeBlameResponse) SetKeysignBlameCountTotal(v int64)`

SetKeysignBlameCountTotal sets KeysignBlameCountTotal field to given value.


### GetKeygenBlameCountSinceChurn

`func (o *NodeBlameResponse) GetKeygenBlameCountSinceChurn() int64`

GetKeygenBlameCountSinceChurn returns the KeygenBlameCountSinceChurn field if non-nil, zero value otherwise.

### GetKeygenBlameCountSinceChurnOk

`func (o *NodeBlameResponse) GetKeygenBlameCountSinceChurnOk() (*int64, bool)`

GetKeygenBlameCountSinceChurnOk returns a tuple with the KeygenBlameCountSinceChurn field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetKeygenBlameCountSinceChurn

`func (o *NodeBlameResponse) SetKeygenBlameCountSinceChurn(v int64)`

SetKeygenBlameCountSinceChurn sets KeygenBlameCountSinceChurn field to given value.


### GetKeysignBlameCountSinceChurn

`func (o *NodeBlameResponse) GetKeysignBlameCountSinceChurn() int64`

GetKeysignBlameCountSinceChurn returns the KeysignBlameCountSinceChurn field if non-nil, zero value otherwise.

### GetKeysignBlameCountSinceChurnOk

`func (o *NodeBlameResponse) GetKeysignBlameCountSinceChurnOk() (*int64, bool)`

GetKeysignBlameCountSinceChurnOk returns a tuple with the KeysignBlameCountSinceChurn field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetKeysignBlameCountSinceChurn

`func (o *NodeBlameResponse) SetKeysignBlameCountSinceChurn(v int64)`

SetKeysignBlameCountSinceChurn sets KeysignBlameCountSinceChurn field to given value.



[[Back to Model list]](../README.md#documentation-for-models) [[Back to API list]](../README.md#documentation-for-api-endpoints) [[Back to README]](../README.md)


