# \QuotesApi

All URIs are relative to *http://localhost*

Method | HTTP request | Description
------------- | ------------- | -------------
[**QuoteIntentSolve**](QuotesApi.md#QuoteIntentSolve) | **Get** /thorchain/quote/intent/solve | 



## QuoteIntentSolve

> QuoteIntentSolveResponse QuoteIntentSolve(ctx).IntentTxId(intentTxId).Amount(amount).Execute()





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
    intentTxId := "intentTxId_example" // string | the transaction ID of the intent to solve
    amount := "amount_example" // string | the amount to offer (in target asset) - optional for checking requirements (optional)

    configuration := openapiclient.NewConfiguration()
    apiClient := openapiclient.NewAPIClient(configuration)
    resp, r, err := apiClient.QuotesApi.QuoteIntentSolve(context.Background()).IntentTxId(intentTxId).Amount(amount).Execute()
    if err != nil {
        fmt.Fprintf(os.Stderr, "Error when calling `QuotesApi.QuoteIntentSolve``: %v\n", err)
        fmt.Fprintf(os.Stderr, "Full HTTP response: %v\n", r)
    }
    // response from `QuoteIntentSolve`: QuoteIntentSolveResponse
    fmt.Fprintf(os.Stdout, "Response from `QuotesApi.QuoteIntentSolve`: %v\n", resp)
}
```

### Path Parameters



### Other Parameters

Other parameters are passed through a pointer to a apiQuoteIntentSolveRequest struct via the builder pattern


Name | Type | Description  | Notes
------------- | ------------- | ------------- | -------------
 **intentTxId** | **string** | the transaction ID of the intent to solve | 
 **amount** | **string** | the amount to offer (in target asset) - optional for checking requirements | 

### Return type

[**QuoteIntentSolveResponse**](QuoteIntentSolveResponse.md)

### Authorization

No authorization required

### HTTP request headers

- **Content-Type**: Not defined
- **Accept**: application/json

[[Back to top]](#) [[Back to API list]](../README.md#documentation-for-api-endpoints)
[[Back to Model list]](../README.md#documentation-for-models)
[[Back to README]](../README.md)

