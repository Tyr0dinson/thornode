# Modify Limit Swaps

## Overview

THORChain's limit swap modification feature allows traders to dynamically adjust or cancel their pending limit swaps without waiting for execution or expiration. This functionality provides flexibility for managing trading positions as market conditions change, enabling users to modify target amounts or completely cancel swaps that are no longer desired.

The modify limit swap feature integrates seamlessly with THORChain's advanced swap queue system, maintaining the security and integrity of the original limit swap while allowing controlled modifications.

## Core Concepts

### What is a Modify Limit Swap?

A modify limit swap operation allows the original creator of a limit swap to:
- **Modify**: Change the target amount (minimum output) of an existing limit swap
- **Cancel**: Remove the limit swap entirely from the queue and refund remaining assets

This is accomplished through a special transaction with a modify memo that references the original swap parameters.

### Security Model

The modify limit swap system implements several security measures:

1. **Address Authorization**: Only the original swap creator can modify their swaps
2. **Asset Matching**: Source and target assets must exactly match the original swap
3. **Fund Donation**: Any assets sent with the modification transaction are donated to THORChain pools
4. **Index Integrity**: Swap queue indexes are properly updated to maintain consistency

## Memo Format

### Basic Structure

```
m=<:SOURCE:TARGET:MODIFIEDTARGETAMOUNT
```

### Parameters

| Parameter | Description | Example | Notes |
|-----------|-------------|---------|-------|
| `m=<` | Modify limit swap handler | `m=<` | Fixed prefix |
| `SOURCE` | Original source coin amount and asset | `1000000000THOR.RUNE` | Must match original swap exactly |
| `TARGET` | Original target coin amount and asset | `5000000BTC.BTC` | Must match original swap exactly |
| `MODIFIEDTARGETAMOUNT` | New target amount | `7500000` | Set to `0` to cancel swap |

### Example Memos

#### Modify Target Amount
```bash
# Increase BTC target from 0.05 to 0.075 BTC
m=<:1000000000THOR.RUNE:5000000BTC.BTC:7500000
```

#### Cancel Limit Swap
```bash
# Cancel the limit swap entirely
m=<:1000000000THOR.RUNE:5000000BTC.BTC:0
```

#### Decrease Target Amount
```bash
# Reduce ETH target from 2.0 to 1.5 ETH
m=<:500000000THOR.RUNE:2000000000000000000ETH.ETH:1500000000000000000
```

## Implementation Architecture

### Handler Flow

The modify limit swap functionality follows THORChain's standard transaction processing flow:

```
Transaction → Memo Parser → Message Validation → Handler Execution
```

#### Key Components

1. **Memo Parser** (`x/thorchain/memo/memo_modify_limit_swap.go:28`)
   - Parses the `m=<` memo format
   - Extracts source, target, and modified target amount parameters
   - Creates `ModifyLimitSwapMemo` struct

2. **Message Type** (`x/thorchain/types/msg_modify_limit_swap.go:34`)
   - Validates memo parameters
   - Ensures source and target assets are different
   - Verifies address authorization

3. **Handler Logic** (`x/thorchain/handler_modify_limit_swap.go:48`)
   - Locates matching limit swaps by source/target assets
   - Validates user authorization
   - Executes modification or cancellation
   - Handles fund donation

### Swap Identification Process

The system identifies target swaps using a sophisticated indexing mechanism:

1. **Index Lookup**: Query swap queue index using source asset, target asset, and swap type
2. **Address Filtering**: Filter results to only include swaps from the requesting address
3. **First Match Selection**: Modify only the first matching swap if multiple exist

```go
// Simplified handler logic flow
items, err := h.mgr.Keeper().GetAdvSwapQueueIndex(ctx, MsgSwap{
    Tx: common.Tx{
        Coins: common.NewCoins(msg.Source),
    },
    TargetAsset: msg.Target.Asset,
    TradeTarget: msg.Target.Amount,
    SwapType:    LimitSwap,
})
```

## Modification Process

### Step-by-Step Workflow

#### 1. Transaction Submission
User submits a transaction with:
- **Recipient**: THORChain inbound address
- **Memo**: Properly formatted modify memo
- **Amount**: Any amount (will be donated to pools)

#### 2. Memo Parsing
THORChain parses the memo to extract:
- Source coin specification
- Target coin specification  
- New target amount

#### 3. Validation
System validates:
- Memo format correctness
- Asset validity
- Address authorization
- Business logic constraints

#### 4. Swap Location
Handler locates the target swap by:
- Querying advanced swap queue index
- Filtering by source and target assets
- Matching requester address

#### 5. Modification Execution
For modifications (non-zero target):
- Remove existing swap from index
- Update trade target amount
- Re-add swap to queue with new index

For cancellations (zero target):
- Remove swap from queue entirely
- Settle and refund remaining assets

#### 6. Fund Handling
Any assets sent with the modification:
- Are donated to the appropriate pool
- Generate donation events
- Help maintain protocol liquidity

## Security Considerations

### Authorization Controls

The system enforces strict authorization:

```go
// Only original swap creator can modify
if !msgSwap.Tx.FromAddress.Equals(msg.From) {
    continue // Skip swaps from other addresses
}
```

### Asset Validation

Both source and target assets undergo validation:
- Must be valid THORChain assets
- Must match original swap exactly
- Cannot be the same asset (source == target)

### Chain Consistency

For THORChain native assets:
```go
// Native asset validation
if m.Source.Asset.GetChain().IsTHORChain() {
    frm, err := m.From.AccAddress()
    if err != nil {
        return cosmos.ErrUnknownRequest(err.Error())
    }
    if !frm.Equals(m.Signer) {
        return cosmos.ErrUnknownRequest("from and signer address must match when source asset is native")
    }
}
```

## Error Handling

### Common Error Scenarios

#### No Matching Swap Found
```
Error: "could not find matching limit swap"
```
**Causes:**
- Incorrect source or target asset specification
- Swap already executed or expired
- Address mismatch

#### Validation Failures
```
Error: "source asset and target asset cannot be the same"
```
**Causes:**
- Identical source and target assets in memo

#### Address Mismatch
**Causes:**
- Attempting to modify someone else's swap
- Incorrect from address specification

### Debugging Tips

1. **Verify Original Swap**: Ensure the limit swap exists and matches exactly
2. **Check Asset Format**: Use precise asset notation from original swap
3. **Confirm Address**: Transaction must originate from original swap creator
4. **Validate Amounts**: Ensure amounts use correct decimal precision

## API Integration

### Querying Existing Limit Swaps

Before modification, query existing swaps:

```bash
# Get all limit swaps for an address
curl "https://thornode.ninerealms.com/thorchain/queue/limit_swaps?sender=thor1abc123"

# Get swaps by source asset
curl "https://thornode.ninerealms.com/thorchain/queue/limit_swaps?source_asset=BTC.BTC"

# Get swaps by target asset  
curl "https://thornode.ninerealms.com/thorchain/queue/limit_swaps?target_asset=ETH.ETH"
```

### Transaction Construction

#### Using THORChain CLI
```bash
# Modify limit swap target amount
thornode tx thorchain deposit \
  --amount 1rune \
  --memo "m=<:1000000000THOR.RUNE:5000000BTC.BTC:7500000" \
  --from user_key \
  --chain-id thorchain
```

#### Programmatic Integration
```javascript
// Example JavaScript integration
const modifyMemo = `m=<:${sourceAmount}${sourceAsset}:${targetAmount}${targetAsset}:${newTargetAmount}`;

const tx = {
    to: thorchainInboundAddress,
    value: "1", // Minimal amount, will be donated
    data: modifyMemo
};
```

## Best Practices

### For Users

1. **Query Before Modification**: Always verify the existing swap details before attempting modification
2. **Use Minimal Amounts**: Send only the minimum required amount with modification transactions
3. **Double-Check Parameters**: Ensure source and target parameters exactly match the original swap
4. **Monitor Execution**: Watch for modification events to confirm successful processing

### For Developers

1. **Error Handling**: Implement robust error handling for all modification scenarios
2. **Asset Precision**: Maintain proper decimal precision for all asset amounts
3. **Address Validation**: Verify address formats match the source chain requirements
4. **Event Monitoring**: Track modification events for user feedback

### For Integrators

1. **API Rate Limiting**: Implement appropriate rate limiting for swap queries
2. **Retry Logic**: Build retry mechanisms for transient failures
3. **User Experience**: Provide clear feedback about modification status
4. **Security Audits**: Regularly audit modification transaction flows

## Testing Examples

### Unit Test Patterns
Based on the test suite in `handler_modify_limit_swap_test.go:15`:

```go
// Test successful modification
msg := types.NewMsgModifyLimitSwap(
    fromAddr,
    sourceCoin,
    targetCoin,
    modifiedTargetAmount,
    signer,
    common.EmptyAsset,
    cosmos.ZeroUint(),
)

result, err := handler.Run(ctx, msg)
// Verify success and modified target amount
```

### Regression Test Scenarios
The test suite covers:
- Successful target amount modification
- Limit swap cancellation (target = 0)
- Address authorization validation
- Invalid asset combination handling
- Multiple swap modification behavior

## Troubleshooting Guide

### Issue: Modification Transaction Ignored

**Symptoms**: Transaction processed but swap remains unchanged

**Potential Causes**:
1. Asset parameter mismatch
2. Incorrect decimal precision
3. Address authorization failure

**Resolution**:
1. Query existing swap details via API
2. Compare parameters exactly
3. Verify transaction origin address

### Issue: "No Matching Swap" Error

**Symptoms**: Error message indicating no swap found

**Potential Causes**:
1. Swap already executed or expired
2. Incorrect source/target specification
3. Wrong address

**Resolution**:
1. Check swap queue via API
2. Verify asset notation format
3. Confirm transaction sender

### Issue: Validation Error

**Symptoms**: Transaction rejected during validation

**Potential Causes**:
1. Same source and target assets
2. Invalid asset format
3. Empty or malformed parameters

**Resolution**:
1. Review memo format carefully
2. Validate asset notation
3. Ensure different source/target assets

## Related Documentation

- [Advanced Swap Queue Guide](../swap-guide/advanced-swap-queue.md)
- [Memo Format Guide](memos.md)
- [Asset Notation](asset-notation.md)
- [Swap Guide](../swap-guide/quickstart-guide.md)

## Code References

| Component | File | Key Functions |
|-----------|------|---------------|
| Handler | `x/thorchain/handler_modify_limit_swap.go:48` | `handle()`, `modifyLimitSwap()`, `cancelLimitSwap()` |
| Memo Parser | `x/thorchain/memo/memo_modify_limit_swap.go:28` | `ParseModifyLimitSwap()` |
| Message Type | `x/thorchain/types/msg_modify_limit_swap.go:34` | `ValidateBasic()` |
| Tests | `x/thorchain/handler_modify_limit_swap_test.go:15` | Test scenarios |
| Regression | `test/regression/suites/adv-swap-queue/mod-limit-swaps.yaml:52` | Integration tests |