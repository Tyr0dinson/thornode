# MimirVotesRequiredResponse

## Properties

Name | Type | Description | Notes
------------ | ------------- | ------------- | -------------
**Mimirs** | [**[]MimirVoteRequired**](MimirVoteRequired.md) |  | 
**ActiveNodeCount** | **int64** | number of active nodes the vote counts are measured against | 

## Methods

### NewMimirVotesRequiredResponse

`func NewMimirVotesRequiredResponse(mimirs []MimirVoteRequired, activeNodeCount int64, ) *MimirVotesRequiredResponse`

NewMimirVotesRequiredResponse instantiates a new MimirVotesRequiredResponse object
This constructor will assign default values to properties that have it defined,
and makes sure properties required by API are set, but the set of arguments
will change when the set of required properties is changed

### NewMimirVotesRequiredResponseWithDefaults

`func NewMimirVotesRequiredResponseWithDefaults() *MimirVotesRequiredResponse`

NewMimirVotesRequiredResponseWithDefaults instantiates a new MimirVotesRequiredResponse object
This constructor will only assign default values to properties that have it defined,
but it doesn't guarantee that properties required by API are set

### GetMimirs

`func (o *MimirVotesRequiredResponse) GetMimirs() []MimirVoteRequired`

GetMimirs returns the Mimirs field if non-nil, zero value otherwise.

### GetMimirsOk

`func (o *MimirVotesRequiredResponse) GetMimirsOk() (*[]MimirVoteRequired, bool)`

GetMimirsOk returns a tuple with the Mimirs field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetMimirs

`func (o *MimirVotesRequiredResponse) SetMimirs(v []MimirVoteRequired)`

SetMimirs sets Mimirs field to given value.


### GetActiveNodeCount

`func (o *MimirVotesRequiredResponse) GetActiveNodeCount() int64`

GetActiveNodeCount returns the ActiveNodeCount field if non-nil, zero value otherwise.

### GetActiveNodeCountOk

`func (o *MimirVotesRequiredResponse) GetActiveNodeCountOk() (*int64, bool)`

GetActiveNodeCountOk returns a tuple with the ActiveNodeCount field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetActiveNodeCount

`func (o *MimirVotesRequiredResponse) SetActiveNodeCount(v int64)`

SetActiveNodeCount sets ActiveNodeCount field to given value.



[[Back to Model list]](../README.md#documentation-for-models) [[Back to API list]](../README.md#documentation-for-api-endpoints) [[Back to README]](../README.md)


