# \MemolessApi

All URIs are relative to *http://localhost*

Method | HTTP request | Description
------------- | ------------- | -------------
[**RegisteredMemo**](MemolessApi.md#RegisteredMemo) | **Get** /thorchain/registered_memo/{reference} | 



## RegisteredMemo

> RegisteredMemoResponse RegisteredMemo(ctx, reference).Height(height).Execute()





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
    reference := "12345" // string | The 5-digit reference code for the registered memo
    height := int64(789) // int64 | optional block height, defaults to current tip (optional)

    configuration := openapiclient.NewConfiguration()
    apiClient := openapiclient.NewAPIClient(configuration)
    resp, r, err := apiClient.MemolessApi.RegisteredMemo(context.Background(), reference).Height(height).Execute()
    if err != nil {
        fmt.Fprintf(os.Stderr, "Error when calling `MemolessApi.RegisteredMemo``: %v\n", err)
        fmt.Fprintf(os.Stderr, "Full HTTP response: %v\n", r)
    }
    // response from `RegisteredMemo`: RegisteredMemoResponse
    fmt.Fprintf(os.Stdout, "Response from `MemolessApi.RegisteredMemo`: %v\n", resp)
}
```

### Path Parameters


Name | Type | Description  | Notes
------------- | ------------- | ------------- | -------------
**ctx** | **context.Context** | context for authentication, logging, cancellation, deadlines, tracing, etc.
**reference** | **string** | The 5-digit reference code for the registered memo | 

### Other Parameters

Other parameters are passed through a pointer to a apiRegisteredMemoRequest struct via the builder pattern


Name | Type | Description  | Notes
------------- | ------------- | ------------- | -------------

 **height** | **int64** | optional block height, defaults to current tip | 

### Return type

[**RegisteredMemoResponse**](RegisteredMemoResponse.md)

### Authorization

No authorization required

### HTTP request headers

- **Content-Type**: Not defined
- **Accept**: application/json

[[Back to top]](#) [[Back to API list]](../README.md#documentation-for-api-endpoints)
[[Back to Model list]](../README.md#documentation-for-models)
[[Back to README]](../README.md)

