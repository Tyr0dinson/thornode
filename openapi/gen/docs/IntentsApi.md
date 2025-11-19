# \IntentsApi

All URIs are relative to *http://localhost*

Method | HTTP request | Description
------------- | ------------- | -------------
[**ActiveIntents**](IntentsApi.md#ActiveIntents) | **Get** /thorchain/intents | 
[**Intent**](IntentsApi.md#Intent) | **Get** /thorchain/intent/{tx_id} | 



## ActiveIntents

> QueryActiveIntentsResponse ActiveIntents(ctx).Height(height).Execute()





### Example

```go
package main

import (
    "context"
    "fmt"
    "os"
    openapiclient "./openapi"
)

func main() {
    height := int64(789) // int64 | optional block height, defaults to current tip (optional)

    configuration := openapiclient.NewConfiguration()
    apiClient := openapiclient.NewAPIClient(configuration)
    resp, r, err := apiClient.IntentsApi.ActiveIntents(context.Background()).Height(height).Execute()
    if err != nil {
        fmt.Fprintf(os.Stderr, "Error when calling `IntentsApi.ActiveIntents``: %v\n", err)
        fmt.Fprintf(os.Stderr, "Full HTTP response: %v\n", r)
    }
    // response from `ActiveIntents`: QueryActiveIntentsResponse
    fmt.Fprintf(os.Stdout, "Response from `IntentsApi.ActiveIntents`: %v\n", resp)
}
```

### Path Parameters



### Other Parameters

Other parameters are passed through a pointer to a apiActiveIntentsRequest struct via the builder pattern


Name | Type | Description  | Notes
------------- | ------------- | ------------- | -------------
 **height** | **int64** | optional block height, defaults to current tip | 

### Return type

[**QueryActiveIntentsResponse**](QueryActiveIntentsResponse.md)

### Authorization

No authorization required

### HTTP request headers

- **Content-Type**: Not defined
- **Accept**: application/json

[[Back to top]](#) [[Back to API list]](../README.md#documentation-for-api-endpoints)
[[Back to Model list]](../README.md#documentation-for-models)
[[Back to README]](../README.md)


## Intent

> QueryIntentResponse Intent(ctx, txId).Height(height).Execute()





### Example

```go
package main

import (
    "context"
    "fmt"
    "os"
    openapiclient "./openapi"
)

func main() {
    txId := "1234567890ABCDEF1234567890ABCDEF1234567890ABCDEF1234567890ABCDEF" // string | The transaction ID of the intent
    height := int64(789) // int64 | optional block height, defaults to current tip (optional)

    configuration := openapiclient.NewConfiguration()
    apiClient := openapiclient.NewAPIClient(configuration)
    resp, r, err := apiClient.IntentsApi.Intent(context.Background(), txId).Height(height).Execute()
    if err != nil {
        fmt.Fprintf(os.Stderr, "Error when calling `IntentsApi.Intent``: %v\n", err)
        fmt.Fprintf(os.Stderr, "Full HTTP response: %v\n", r)
    }
    // response from `Intent`: QueryIntentResponse
    fmt.Fprintf(os.Stdout, "Response from `IntentsApi.Intent`: %v\n", resp)
}
```

### Path Parameters


Name | Type | Description  | Notes
------------- | ------------- | ------------- | -------------
**ctx** | **context.Context** | context for authentication, logging, cancellation, deadlines, tracing, etc.
**txId** | **string** | The transaction ID of the intent | 

### Other Parameters

Other parameters are passed through a pointer to a apiIntentRequest struct via the builder pattern


Name | Type | Description  | Notes
------------- | ------------- | ------------- | -------------

 **height** | **int64** | optional block height, defaults to current tip | 

### Return type

[**QueryIntentResponse**](QueryIntentResponse.md)

### Authorization

No authorization required

### HTTP request headers

- **Content-Type**: Not defined
- **Accept**: application/json

[[Back to top]](#) [[Back to API list]](../README.md#documentation-for-api-endpoints)
[[Back to Model list]](../README.md#documentation-for-models)
[[Back to README]](../README.md)

