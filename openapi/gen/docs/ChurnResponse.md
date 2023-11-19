# ChurnResponse

## Properties

Name | Type | Description | Notes
------------ | ------------- | ------------- | -------------
**NextChurnHeight** | **int64** | First keygen block of next churn | 
**NodesIn** | **[]string** | Non-Active nodes which would currently be chosen as keygen members to potentially churn in. | 
**NodesOut** | **[]string** | Active nodes which would currently be marked to churn out. | 

## Methods

### NewChurnResponse

`func NewChurnResponse(nextChurnHeight int64, nodesIn []string, nodesOut []string, ) *ChurnResponse`

NewChurnResponse instantiates a new ChurnResponse object
This constructor will assign default values to properties that have it defined,
and makes sure properties required by API are set, but the set of arguments
will change when the set of required properties is changed

### NewChurnResponseWithDefaults

`func NewChurnResponseWithDefaults() *ChurnResponse`

NewChurnResponseWithDefaults instantiates a new ChurnResponse object
This constructor will only assign default values to properties that have it defined,
but it doesn't guarantee that properties required by API are set

### GetNextChurnHeight

`func (o *ChurnResponse) GetNextChurnHeight() int64`

GetNextChurnHeight returns the NextChurnHeight field if non-nil, zero value otherwise.

### GetNextChurnHeightOk

`func (o *ChurnResponse) GetNextChurnHeightOk() (*int64, bool)`

GetNextChurnHeightOk returns a tuple with the NextChurnHeight field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetNextChurnHeight

`func (o *ChurnResponse) SetNextChurnHeight(v int64)`

SetNextChurnHeight sets NextChurnHeight field to given value.


### GetNodesIn

`func (o *ChurnResponse) GetNodesIn() []string`

GetNodesIn returns the NodesIn field if non-nil, zero value otherwise.

### GetNodesInOk

`func (o *ChurnResponse) GetNodesInOk() (*[]string, bool)`

GetNodesInOk returns a tuple with the NodesIn field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetNodesIn

`func (o *ChurnResponse) SetNodesIn(v []string)`

SetNodesIn sets NodesIn field to given value.


### GetNodesOut

`func (o *ChurnResponse) GetNodesOut() []string`

GetNodesOut returns the NodesOut field if non-nil, zero value otherwise.

### GetNodesOutOk

`func (o *ChurnResponse) GetNodesOutOk() (*[]string, bool)`

GetNodesOutOk returns a tuple with the NodesOut field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetNodesOut

`func (o *ChurnResponse) SetNodesOut(v []string)`

SetNodesOut sets NodesOut field to given value.



[[Back to Model list]](../README.md#documentation-for-models) [[Back to API list]](../README.md#documentation-for-api-endpoints) [[Back to README]](../README.md)


