# QueryIntentResponse

## Properties

Name | Type | Description | Notes
------------ | ------------- | ------------- | -------------
**Intent** | [**Intent**](Intent.md) |  | 
**Solves** | [**[]IntentSolve**](IntentSolve.md) |  | 

## Methods

### NewQueryIntentResponse

`func NewQueryIntentResponse(intent Intent, solves []IntentSolve, ) *QueryIntentResponse`

NewQueryIntentResponse instantiates a new QueryIntentResponse object
This constructor will assign default values to properties that have it defined,
and makes sure properties required by API are set, but the set of arguments
will change when the set of required properties is changed

### NewQueryIntentResponseWithDefaults

`func NewQueryIntentResponseWithDefaults() *QueryIntentResponse`

NewQueryIntentResponseWithDefaults instantiates a new QueryIntentResponse object
This constructor will only assign default values to properties that have it defined,
but it doesn't guarantee that properties required by API are set

### GetIntent

`func (o *QueryIntentResponse) GetIntent() Intent`

GetIntent returns the Intent field if non-nil, zero value otherwise.

### GetIntentOk

`func (o *QueryIntentResponse) GetIntentOk() (*Intent, bool)`

GetIntentOk returns a tuple with the Intent field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetIntent

`func (o *QueryIntentResponse) SetIntent(v Intent)`

SetIntent sets Intent field to given value.


### GetSolves

`func (o *QueryIntentResponse) GetSolves() []IntentSolve`

GetSolves returns the Solves field if non-nil, zero value otherwise.

### GetSolvesOk

`func (o *QueryIntentResponse) GetSolvesOk() (*[]IntentSolve, bool)`

GetSolvesOk returns a tuple with the Solves field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetSolves

`func (o *QueryIntentResponse) SetSolves(v []IntentSolve)`

SetSolves sets Solves field to given value.



[[Back to Model list]](../README.md#documentation-for-models) [[Back to API list]](../README.md#documentation-for-api-endpoints) [[Back to README]](../README.md)


