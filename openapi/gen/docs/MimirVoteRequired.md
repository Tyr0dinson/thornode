# MimirVoteRequired

## Properties

Name | Type | Description | Notes
------------ | ------------- | ------------- | -------------
**Key** | **string** |  | 
**Votes** | **int64** | active nodes that have voted on this key | 

## Methods

### NewMimirVoteRequired

`func NewMimirVoteRequired(key string, votes int64, ) *MimirVoteRequired`

NewMimirVoteRequired instantiates a new MimirVoteRequired object
This constructor will assign default values to properties that have it defined,
and makes sure properties required by API are set, but the set of arguments
will change when the set of required properties is changed

### NewMimirVoteRequiredWithDefaults

`func NewMimirVoteRequiredWithDefaults() *MimirVoteRequired`

NewMimirVoteRequiredWithDefaults instantiates a new MimirVoteRequired object
This constructor will only assign default values to properties that have it defined,
but it doesn't guarantee that properties required by API are set

### GetKey

`func (o *MimirVoteRequired) GetKey() string`

GetKey returns the Key field if non-nil, zero value otherwise.

### GetKeyOk

`func (o *MimirVoteRequired) GetKeyOk() (*string, bool)`

GetKeyOk returns a tuple with the Key field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetKey

`func (o *MimirVoteRequired) SetKey(v string)`

SetKey sets Key field to given value.


### GetVotes

`func (o *MimirVoteRequired) GetVotes() int64`

GetVotes returns the Votes field if non-nil, zero value otherwise.

### GetVotesOk

`func (o *MimirVoteRequired) GetVotesOk() (*int64, bool)`

GetVotesOk returns a tuple with the Votes field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetVotes

`func (o *MimirVoteRequired) SetVotes(v int64)`

SetVotes sets Votes field to given value.



[[Back to Model list]](../README.md#documentation-for-models) [[Back to API list]](../README.md#documentation-for-api-endpoints) [[Back to README]](../README.md)


