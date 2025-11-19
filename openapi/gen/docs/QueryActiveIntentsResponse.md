# QueryActiveIntentsResponse

## Properties

Name | Type | Description | Notes
------------ | ------------- | ------------- | -------------
**Intents** | [**[]IntentWithSolves**](IntentWithSolves.md) |  | 

## Methods

### NewQueryActiveIntentsResponse

`func NewQueryActiveIntentsResponse(intents []IntentWithSolves, ) *QueryActiveIntentsResponse`

NewQueryActiveIntentsResponse instantiates a new QueryActiveIntentsResponse object
This constructor will assign default values to properties that have it defined,
and makes sure properties required by API are set, but the set of arguments
will change when the set of required properties is changed

### NewQueryActiveIntentsResponseWithDefaults

`func NewQueryActiveIntentsResponseWithDefaults() *QueryActiveIntentsResponse`

NewQueryActiveIntentsResponseWithDefaults instantiates a new QueryActiveIntentsResponse object
This constructor will only assign default values to properties that have it defined,
but it doesn't guarantee that properties required by API are set

### GetIntents

`func (o *QueryActiveIntentsResponse) GetIntents() []IntentWithSolves`

GetIntents returns the Intents field if non-nil, zero value otherwise.

### GetIntentsOk

`func (o *QueryActiveIntentsResponse) GetIntentsOk() (*[]IntentWithSolves, bool)`

GetIntentsOk returns a tuple with the Intents field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetIntents

`func (o *QueryActiveIntentsResponse) SetIntents(v []IntentWithSolves)`

SetIntents sets Intents field to given value.



[[Back to Model list]](../README.md#documentation-for-models) [[Back to API list]](../README.md#documentation-for-api-endpoints) [[Back to README]](../README.md)


