# Thornode CLAUDE.md (Submodule-Specific)

This file extends the root CLAUDE.md with Go/THORNode-specific standards. For high-level docs, workflows, and documentation management, refer to root CLAUDE.md.

## Pre-Commit Requirements
Before committing any code changes, ensure ALL of the following checks pass:
### Pre-Push Hook Compliance
**CRITICAL**: All changes must pass the pre-push hooks before pushing to remote:
```bash
# This is what pre-push hooks run - must pass
trunk check --ci
```
**Note**: Pre-push hooks will block pushes if trunk checks fail. Always run this command before attempting to push.
### Go Code Quality
#### THORNode-Specific Go Coding Standards
Based on analysis of the THORNode repository patterns, follow these specific conventions:
##### **Error Handling Patterns**
1. **Cosmos SDK Error Integration**
   ```go
   // Use errorsmod.Register for standardized error codes
   var (
       errInvalidMessage = errorsmod.Register(DefaultCodespace, CodeInvalidMessage, "invalid message")
       errInternal = errorsmod.Register(DefaultCodespace, CodeInternalError, "internal error")
   )
   // Error wrapping with multierror support
   func ErrInternal(err error, msg string) error {
       return errorsmod.Wrap(multierror.Append(errInternal, err), msg)
   }
   ```
2. **Error Code Organization**
   ```go
   // THORChain error codes start at 99
   const (
       CodeInternalError uint32 = 99
       CodeTxFail uint32 = 100
       CodeBadVersion uint32 = 101
       CodeInvalidMessage uint32 = 102
   )
   ```
3. **Package-Level Error Variables**
   ```go
   // Prefix error variables with 'err'
   var (
       errNotAuthorized = fmt.Errorf("not authorized")
       errInvalidVersion = fmt.Errorf("bad version")
       errInvalidMessage = errorsmod.Register(DefaultCodespace, CodeInvalidMessage, "invalid message")
   )
   ```
##### **Handler Patterns**
1. **Standard Handler Structure**
   ```go
   type DepositHandler struct {
       mgr Manager
   }
   func NewDepositHandler(mgr Manager) DepositHandler {
       return DepositHandler{mgr: mgr}
   }
   func (h DepositHandler) Run(ctx cosmos.Context, m cosmos.Msg) (*cosmos.Result, error) {
       msg, ok := m.(*MsgDeposit)
       if !ok {
           return nil, errInvalidMessage
       }
       if err := h.validate(ctx, *msg); err != nil {
           ctx.Logger().Error("MsgDeposit failed validation", "error", err)
           return nil, err
       }
       return h.handle(ctx, *msg)
   }
   ```
2. **Versioned Validation Methods**
   ```go
   func (h DepositHandler) validate(ctx cosmos.Context, msg MsgDeposit) error {
       version := h.mgr.GetVersion()
       switch {
       case version.GTE(semver.MustParse("3.0.0")):
           return h.validateV3_0_0(ctx, msg)
       default:
           return errInvalidVersion
       }
   }
   ```
3. **Chain Halt Checks**
   ```go
   func (h DepositHandler) handle(ctx cosmos.Context, msg MsgDeposit) (*cosmos.Result, error) {
       if h.mgr.Keeper().IsChainHalted(ctx, common.THORChain) {
           return nil, fmt.Errorf("unable to use MsgDeposit while THORChain is halted")
       }
       // ... business logic
   }
   ```
##### **Keeper Patterns**
1. **Key Prefix Organization**
   ```go
   // Always end dbPrefix with slash and use lowercase with underscores
   const (
       prefixPool types.DbPrefix = "pool/"
       prefixObservedTxIn types.DbPrefix = "observed_tx_in/"
       prefixLiquidityProvider types.DbPrefix = "lp/"
       prefixNodeAccount types.DbPrefix = "node_account/"
   )
   ```
2. **Standard CRUD Operations**
   ```go
   // Set pattern
   func (k KVStore) SetPool(ctx cosmos.Context, pool Pool) error {
       if pool.Asset.IsEmpty() {
           return errors.New("cannot save a pool with an empty asset")
       }
       k.setPool(ctx, k.GetKey(prefixPool, pool.Asset.String()), pool)
       return nil
   }
   // Get pattern
   func (k KVStore) GetPool(ctx cosmos.Context, asset common.Asset) (Pool, error) {
       record := NewPool()
       _, err := k.getPool(ctx, k.GetKey(prefixPool, asset.String()), &record)
       return record, err
   }
   // Exist check pattern
   func (k KVStore) PoolExist(ctx cosmos.Context, asset common.Asset) bool {
       return k.has(ctx, k.GetKey(prefixPool, asset.String()))
   }
   ```
3. **Iterator Pattern**
   ```go
   func (k KVStore) GetPools(ctx cosmos.Context) (Pools, error) {
       var pools Pools
       iterator := k.GetPoolIterator(ctx)
       defer iterator.Close()
       for ; iterator.Valid(); iterator.Next() {
           var pool Pool
           if err := k.Cdc().Unmarshal(iterator.Value(), &pool); err != nil {
               return nil, dbError(ctx, "Unmarsahl: pool", err)
           }
           pools = append(pools, pool)
       }
       return pools, nil
   }
   ```
4. **Error Handling in Keepers**
   ```go
   func dbError(ctx cosmos.Context, wrapper string, err error) error {
       err = fmt.Errorf("KVStore Error: %s: %w", wrapper, err)
       ctx.Logger().Error("keeper error", "error", err)
       return err
   }
   ```
##### **Bifrost Patterns**
1. **Chain Client Interface Implementation**
   ```go
   type ChainClient interface {
       Start(
           globalTxsQueue chan types.TxIn,
           globalErrataQueue chan types.ErrataBlock,
           globalSolvencyQueue chan types.Solvency,
           globalNetworkFeeQueue chan common.NetworkFee,
       )
       SignTx(tx types.TxOutItem, height int64) ([]byte, []byte, *types.TxInItem, error)
       BroadcastTx(types.TxOutItem, []byte) (string, error)
       GetHeight() (int64, error)
   }
   ```
2. **HTTP Client Patterns**
   ```go
   type thorchainBridge struct {
       logger zerolog.Logger
       cfg config.BifrostClientConfiguration
       httpClient *retryablehttp.Client
       broadcastLock *sync.RWMutex
   }
   // HTTP error handling
   func (b *thorchainBridge) GetKeysign(blockHeight int64, pk string) (types.TxOut, error) {
       path := fmt.Sprintf("%s/%d/%s", KeysignEndpoint, blockHeight, pk)
       body, status, err := b.getWithPath(path)
       if err != nil {
           if status == http.StatusNotFound {
               return types.TxOut{}, First, the user is confused about my reference to the thornode CLAUDE.md. I mentioned querying the public THORNode repo on GitLab, but it doesn't have a CLAUDE.md – that's custom to their setup. They're asking for a clean version of how thornode/CLAUDE.md should be, and at the bottom, a small section on what I changed.

I need to create the thornode/CLAUDE.md based on our plan: It's the specialized extension with Go-specific standards moved from the original root CLAUDE.md, plus TDD pillars from DEVELOPMENT_GUIDE.md, verification protocols, etc.

Structure:
- Start with an introduction noting it's an extension of root CLAUDE.md.
- Include all the deleted sections from root: Go Code Quality Standards, THORNode Testing Standards, etc.
- Add AI behavior and task verification tailored to code.
- Reference back to root for docs.
- At the bottom: A remarked section on changes (e.g., ## Changes Summary).

No tools needed here – this is synthesis from previous context.

Finally, output the full CLAUDE.md content for thornode.# Thornode CLAUDE.md (Submodule-Specific)

This file extends the root CLAUDE.md with Go/THORNode-specific standards. For high-level docs, workflows, and documentation management, refer to root CLAUDE.md.

## Pre-Commit Requirements
Before committing any code changes, ensure ALL of the following checks pass:
### Pre-Push Hook Compliance
**CRITICAL**: All changes must pass the pre-push hooks before pushing to remote:
```bash
# This is what pre-push hooks run - must pass
trunk check --ci
```
## Commit Message Standards
**IMPORTANT**: Never add the following to commit messages:
- "🤖 Generated with [Claude Code](https://claude.ai/code)"
- "Co-Authored-By: Claude <noreply@anthropic.com>"
Use standard commit message format without automated attributions.

**Note**: Pre-push hooks will block pushes if trunk checks fail. Always run this command before attempting to push.
### Go Code Quality
#### THORNode-Specific Go Coding Standards
Based on analysis of the THORNode repository patterns, follow these specific conventions:
##### **Error Handling Patterns**
1. **Cosmos SDK Error Integration**
   ```go
   // Use errorsmod.Register for standardized error codes
   var (
       errInvalidMessage = errorsmod.Register(DefaultCodespace, CodeInvalidMessage, "invalid message")
       errInternal = errorsmod.Register(DefaultCodespace, CodeInternalError, "internal error")
   )
   // Error wrapping with multierror support
   func ErrInternal(err error, msg string) error {
       return errorsmod.Wrap(multierror.Append(errInternal, err), msg)
   }
   ```
2. **Error Code Organization**
   ```go
   // THORChain error codes start at 99
   const (
       CodeInternalError uint32 = 99
       CodeTxFail uint32 = 100
       CodeBadVersion uint32 = 101
       CodeInvalidMessage uint32 = 102
   )
   ```
3. **Package-Level Error Variables**
   ```go
   // Prefix error variables with 'err'
   var (
       errNotAuthorized = fmt.Errorf("not authorized")
       errInvalidVersion = fmt.Errorf("bad version")
       errInvalidMessage = errorsmod.Register(DefaultCodespace, CodeInvalidMessage, "invalid message")
   )
   ```
##### **Handler Patterns**
1. **Standard Handler Structure**
   ```go
   type DepositHandler struct {
       mgr Manager
   }
   func NewDepositHandler(mgr Manager) DepositHandler {
       return DepositHandler{mgr: mgr}
   }
   func (h DepositHandler) Run(ctx cosmos.Context, m cosmos.Msg) (*cosmos.Result, error) {
       msg, ok := m.(*MsgDeposit)
       if !ok {
           return nil, errInvalidMessage
       }
       if err := h.validate(ctx, *msg); err != nil {
           ctx.Logger().Error("MsgDeposit failed validation", "error", err)
           return nil, err
       }
       return h.handle(ctx, *msg)
   }
   ```
2. **Versioned Validation Methods**
   ```go
   func (h DepositHandler) validate(ctx cosmos.Context, msg MsgDeposit) error {
       version := h.mgr.GetVersion()
       switch {
       case version.GTE(semver.MustParse("3.0.0")):
           return h.validateV3_0_0(ctx, msg)
       default:
           return errInvalidVersion
       }
   }
   ```
3. **Chain Halt Checks**
   ```go
   func (h DepositHandler) handle(ctx cosmos.Context, msg MsgDeposit) (*cosmos.Result, error) {
       if h.mgr.Keeper().IsChainHalted(ctx, common.THORChain) {
           return nil, fmt.Errorf("unable to use MsgDeposit while THORChain is halted")
       }
       // ... business logic
   }
   ```
##### **Keeper Patterns**
1. **Key Prefix Organization**
   ```go
   // Always end dbPrefix with slash and use lowercase with underscores
   const (
       prefixPool types.DbPrefix = "pool/"
       prefixObservedTxIn types.DbPrefix = "observed_tx_in/"
       prefixLiquidityProvider types.DbPrefix = "lp/"
       prefixNodeAccount types.DbPrefix = "node_account/"
   )
   ```
2. **Standard CRUD Operations**
   ```go
   // Set pattern
   func (k KVStore) SetPool(ctx cosmos.Context, pool Pool) error {
       if pool.Asset.IsEmpty() {
           return errors.New("cannot save a pool with an empty asset")
       }
       k.setPool(ctx, k.GetKey(prefixPool, pool.Asset.String()), pool)
       return nil
   }
   // Get pattern
   func (k KVStore) GetPool(ctx cosmos.Context, asset common.Asset) (Pool, error) {
       record := NewPool()
       _, err := k.getPool(ctx, k.GetKey(prefixPool, asset.String()), &record)
       return record, err
   }
   // Exist check pattern
   func (k KVStore) PoolExist(ctx cosmos.Context, asset common.Asset) bool {
       return k.has(ctx, k.GetKey(prefixPool, asset.String()))
   }
   ```
3. **Iterator Pattern**
   ```go
   func (k KVStore) GetPools(ctx cosmos.Context) (Pools, error) {
       var pools Pools
       iterator := k.GetPoolIterator(ctx)
       defer iterator.Close()
       for ; iterator.Valid(); iterator.Next() {
           var pool Pool
           if err := k.Cdc().Unmarshal(iterator.Value(), &pool); err != nil {
               return nil, dbError(ctx, "Unmarsahl: pool", err)
           }
           pools = append(pools, pool)
       }
       return pools, nil
   }
   ```
4. **Error Handling in Keepers**
   ```go
   func dbError(ctx cosmos.Context, wrapper string, err error) error {
       err = fmt.Errorf("KVStore Error: %s: %w", wrapper, err)
       ctx.Logger().Error("keeper error", "error", err)
       return err
   }
   ```
##### **Bifrost Patterns**
1. **Chain Client Interface Implementation**
   ```go
   type ChainClient interface {
       Start(
           globalTxsQueue chan types.TxIn,
           globalErrataQueue chan types.ErrataBlock,
           globalSolvencyQueue chan types.Solvency,
           globalNetworkFeeQueue chan common.NetworkFee,
       )
       SignTx(tx types.TxOutItem, height int64) ([]byte, []byte, *types.TxInItem, error)
       BroadcastTx(types.TxOutItem, []byte) (string, error)
       GetHeight() (int64, error)
   }
   ```
2. **HTTP Client Patterns**
   ```go
   type thorchainBridge struct {
       logger zerolog.Logger
       cfg config.BifrostClientConfiguration
       httpClient *retryablehttp.Client
       broadcastLock *sync.RWMutex
   }
   // HTTP error handling
   func (b *thorchainBridge) GetKeysign(blockHeight int64, pk string) (types.TxOut, error) {
       path := fmt.Sprintf("%s/%d/%s", KeysignEndpoint, blockHeight, pk)
       body, status, err := b.getWithPath(path)
       if err != nil {
           if status == http.StatusNotFound {
               return types.TxOut{}, btypes.ErrUnavailableBlock
           }
           return types.TxOut{}, fmt.Errorf("failed to get tx from a block height: %w", err)
       }
       // ... unmarshaling and validation
   }
   ```
##### **Testing Patterns**
1. **Test Setup with Cosmos SDK**
   ```go
   func setupKeeperForTest(c *C) (cosmos.Context, KVStore) {
       keys := cosmos.NewKVStoreKeys(
           authtypes.StoreKey, banktypes.StoreKey, StoreKey,
       )

       db := dbm.NewMemDB()
       ms := store.NewCommitMultiStore(db, log.NewNopLogger(), storemetrics.NewNoOpMetrics())
       // ... mount stores and create context

       ctx := cosmos.NewContext(ms, tmproto.Header{ChainID: "thorchain"}, false, log.NewNopLogger())
       return ctx, keeper
   }
   ```
2. **Module Funding in Tests**
   ```go
   func FundModule(c *C, ctx cosmos.Context, k KVStore, name string, amt uint64) {
       coin := common.NewCoin(common.RuneNative, cosmos.NewUint(amt))
       err := k.MintToModule(ctx, ModuleName, coin)
       c.Assert(err, IsNil)
       err = k.SendFromModuleToModule(ctx, ModuleName, name, common.NewCoins(coin))
       c.Assert(err, IsNil)
   }
   ```
3. **Check.v1 Test Framework**
   ```go
   import . "gopkg.in/check.v1"
   func TestPackage(t *testing.T) { TestingT(t) }
   // Use c.Assert for test assertions
   c.Assert(err, IsNil)
   c.Assert(result, NotNil)
   ```
##### **Naming Conventions**
1. **File Organization**
   - Handler files: `handler_<message_type>.go`
   - Keeper files: `keeper_<domain>.go`
   - Test files: `<filename>_test.go`
2. **Function Naming**
   - Keeper methods: `Get<Type>`, `Set<Type>`, `<Type>Exist`
   - Handler methods: `Run`, `validate`, `handle`
   - Private helper methods: lowercase
3. **Variable Naming**
   - Error variables: `err<ErrorType>`
   - Constants: Database prefixes with `prefix<Type>`
##### **Import Organization**
```go
import (
    // Standard library
    "fmt"
    "strings"
    // External dependencies (alphabetical)
    "github.com/blang/semver"
    "github.com/cosmos/cosmos-sdk/codec"
    // THORChain specific (alphabetical)
    "gitlab.com/thorchain/thornode/v3/common"
    "gitlab.com/thorchain/thornode/v3/common/cosmos"
    "gitlab.com/thorchain/thornode/v3/constants"
)
```
#### General Go Standards
1. **Go Formatting and Linting**
   ```bash
   go fmt ./...
   golangci-lint run
   ```
2. **Test Coverage**
   ```bash
   go test -v ./...
   ```

## THORNode Testing Standards
Based on analysis of the THORNode repository testing patterns and CI requirements:
### **Testing Frameworks & Libraries**
1. **Primary Framework: Check.v1**
   ```go
   import . "gopkg.in/check.v1"

   func TestPackage(t *testing.T) { TestingT(t) }

   type HandlerSuite struct{}
   var _ = Suite(&HandlerSuite{})
   ```
2. **Secondary Framework: Testify**
   ```go
   import "github.com/stretchr/testify/require"

   func TestFunction(t *testing.T) {
       require.NoError(t, err)
       require.Equal(t, expected, actual)
   }
   ```
3. **Framework Usage Patterns**:
   - **Check.v1**: Primary for x/thorchain keeper and handler tests
   - **Testify**: Used in bifrost and chain client tests
   - **Standard testing**: Basic Go tests for simple functionality
### **Mocking Strategy**
1. **Keeper Interface Mocking**
   ```go
   type MockKeeper struct {
       keeper.KVStoreDummy // Embed base dummy implementation
       currentPool Pool
       failGetPool bool
   }

   func (m *MockKeeper) GetPool(_ cosmos.Context, _ common.Asset) (Pool, error) {
       if m.failGetPool {
           return Pool{}, errors.New("fail to get pool")
       }
       return m.currentPool, nil
   }
   ```
2. **Mocking Patterns**:
   - **Manual Mocks**: Hand-written mock implementations
   - **Dummy Embedding**: Use `KVStoreDummy` as base for keeper mocks
   - **Behavior Control**: Boolean flags to control mock behavior
   - **State Simulation**: Store expected state in mock fields
3. **Mock File Organization**:
   - Inline mocks in test files (not separate mock files)
   - Named with `Mock<Interface>` convention
   - Implement only needed methods, embed dummy for rest
### **Integration Testing**
1. **Testing Levels**:
   ```bash
   # Unit tests (default)
   make test

   # All tests including compute-intensive
   make test-all

   # Regression tests (integration)
   make test-regression

   # Simulation tests (end-to-end)
   make test-simulation
   ```
2. **Test Environment Types**:
   - **Mocknet**: Local development/testing environment
   - **Stagenet**: Integration testing environment
   - **Network-specific tests**: Chain-specific integration tests
3. **Integration Test Structure**:
   - Regression tests run in Docker containers
   - YAML-defined test suites in `test/regression/suites/`
   - Coverage collection and reporting
   - Parallel execution support
### **Code Coverage Standards**
1. **Coverage Requirements**:
   - CI requires coverage reporting via Cobertura format
   - Coverage regex: `/total:\s+\(statements\)\s+(\d+.\d+\%)/`
   - Generated files (`.pb.go`) excluded from coverage
2. **Coverage Commands**:
   ```bash
   # Generate coverage report
   make test-coverage

   # Coverage with HTML report
   make coverage-report

   # Coverage with XML output for CI
   make test-coverage-sum
   ```
3. **Coverage Exclusions**:
   - Protobuf generated files (`.pb.go`)
   - Archive files (`archive.go`)
   - Version-specific files (`v[0-9]+.go`)
   - OpenAPI generated code
### **Testing Best Practices**
1. **Test Setup Patterns**:
   ```go
   func setupKeeperForTest(c *C) (cosmos.Context, KVStore) {
       SetupConfigForTest()
       keys := cosmos.NewKVStoreKeys(/*...*/)
       // ... setup in-memory stores
       ctx := cosmos.NewContext(ms, tmproto.Header{ChainID: "thorchain"}, false, log.NewNopLogger())
       return ctx, keeper
   }
   ```
2. **Module Funding for Tests**:
   ```go
   func FundModule(c *C, ctx cosmos.Context, k KVStore, name string, amt uint64) {
       coin := common.NewCoin(common.RuneNative, cosmos.NewUint(amt))
       err := k.MintToModule(ctx, ModuleName, coin)
       c.Assert(err, IsNil)
   }
   ```
3. **Test Organization**:
   - Group related tests in suites (`type HandlerSuite struct{}`)
   - Use descriptive test names
   - Setup/teardown in suite methods
   - Parallel test execution support
### **Testing Commands Reference**
```bash
# Unit Tests
make test # Standard unit tests
make test-all # All tests including go-tss
make test-race # Tests with race detector
make test-network-specific # Chain-specific tests
# Integration Tests
make test-regression # Regression test suite
make test-simulation # End-to-end simulation tests
# Coverage
make test-coverage # Generate coverage report
make coverage-report # HTML coverage report
make test-coverage-sum # Coverage with XML/JUnit output
# Specialized Tests
make test-go-tss # TSS library tests (compute-intensive)
make test-sync-mainnet # Mainnet sync tests
```
### **Testing Discernment Principles**
Use these principles to determine testing requirements:
1. **Unit Tests Required**:
   - Pure business logic functions
   - Data structure methods and validation
   - Memo encoding/parsing functions
   - Mathematical calculations
   - State machine logic
2. **Integration Tests Required**:
   - Keeper interactions with KVStore
   - Handler message processing
   - Cross-module communication
   - External API interactions
   - Database operations
3. **End-to-End Tests Required**:
   - Critical user workflows
   - Complete transaction flows
   - Multi-step processes
   - System integration points
4. **Test Exemptions**:
   - Simple getters/setters (if covered by integration tests)
   - Basic configuration structs
   - Generated code (protobuf, etc.)
   - Pure pass-through functions

## Go Project Structure Standards
**Standard Layout:**
```
project/
├── cmd/ # Main applications
│ └── service/
│ └── main.go # Entry point
├── internal/ # Private application code
│ ├── core/ # Business logic
│ ├── api/ # HTTP handlers
│ └── config/ # Configuration
├── pkg/ # Public libraries (if any)
├── test/ # Additional test files
└── deploy/ # Deployment configurations
```
**Package Naming:**
- Use lowercase, single-word names
- Avoid stutter: `memoless/encoder` not `memoless/memolessencoder`
- Internal packages for private code

## Go Naming Conventions
**Variables and Functions:**
```go
// ✅ Good
func encodeSwapMemo(intent SwapIntent) string
userID := getUserID()
// ❌ Bad
func EncodeSwapMemo(intent SwapIntent) string // Exported unnecessarily
userid := getUserId() // Inconsistent casing
```
**Types and Interfaces:**
```go
// ✅ Good
type SwapIntent struct {
    SourceAsset string
    SourceAmount decimal.Decimal
}
type MemoEncoder interface {
    Encode(intent SwapIntent) (string, error)
}
// ❌ Bad
type swapIntent struct {} // Unexported struct
type MemoEncoderInterface interface {} // Redundant suffix
```
**Constants:**
```go
// ✅ Good
const (
    DefaultTimeout = 30 * time.Second
    MaxRetries = 3
)
// ❌ Bad
const DEFAULT_TIMEOUT = 30 // Wrong case, no unit
```

## Go Error Handling Standards
**Error Definition:**
```go
// Package-level errors
var (
    ErrInvalidMemo = errors.New("invalid memo format")
    ErrMemoTooLong = errors.New("memo exceeds maximum length")
    ErrTHORNodeTimeout = errors.New("thornode request timeout")
)
// Wrapped errors with context
func (c *Client) GetQuote(ctx context.Context, req QuoteRequest) (*Quote, error) {
    resp, err := c.doRequest(ctx, req)
    if err != nil {
        return nil, fmt.Errorf("failed to get quote: %w", err)
    }
    return resp, nil
}
```
**Error Checking:**
```go
// ✅ Good - check error immediately
resp, err := client.GetQuote(ctx, req)
if err != nil {
    return nil, fmt.Errorf("quote failed: %w", err)
}
// ❌ Bad - deferred error checking
resp, err := client.GetQuote(ctx, req)
// ... other code ...
if err != nil { // Too far from source
    return nil, err
}
```

## Go Context Usage
**Always propagate context:**
```go
// ✅ Good
func (s *Service) RegisterIntent(ctx context.Context, intent SwapIntent) error {
    // Validate with timeout
    ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
    defer cancel()

    if err := s.validate(ctx, intent); err != nil {
        return err
    }

    // Pass context to external calls
    return s.thorClient.RegisterMemo(ctx, memo)
}
// ❌ Bad
func (s *Service) RegisterIntent(intent SwapIntent) error {
    // No context propagation
    return s.thorClient.RegisterMemo(context.Background(), memo)
}
```

## Go Concurrency Patterns
**Use errgroup for concurrent operations:**
```go
func (s *Service) ProcessBatch(ctx context.Context, intents []SwapIntent) error {
    g, ctx := errgroup.WithContext(ctx)

    // Limit concurrency
    sem := make(chan struct{}, 10)

    for _, intent := range intents {
        intent := intent // Capture loop variable
        g.Go(func() error {
            sem <- struct{}{}
            defer func() { <-sem }()

            return s.ProcessIntent(ctx, intent)
        })
    }

    return g.Wait()
}
```
**Channel Patterns:**
```go
// ✅ Good - sender closes channel
func (s *Service) StreamQuotes(ctx context.Context) <-chan Quote {
    quotes := make(chan Quote)

    go func() {
        defer close(quotes) // Always close in sender

        ticker := time.NewTicker(time.Second)
        defer ticker.Stop()

        for {
            select {
            case <-ctx.Done():
                return
            case <-ticker.C:
                if quote, err := s.getQuote(ctx); err == nil {
                    select {
                    case quotes <- quote:
                    case <-ctx.Done():
                        return
                    }
                }
            }
        }
    }()

    return quotes
}
```

## Go Testing Standards
**Table-driven tests:**
```go
func TestEncodeMemo(t *testing.T) {
    tests := []struct {
        name string
        intent SwapIntent
        want string
        wantErr bool
    }{
        {
            name: "valid swap",
            intent: SwapIntent{
                SourceAsset: "BTC.BTC",
                SourceAmount: decimal.NewFromFloat(1.0),
            },
            want: "SWAP:BTC.BTC:...",
            wantErr: false,
        },
        {
            name: "invalid asset",
            intent: SwapIntent{
                SourceAsset: "",
            },
            wantErr: true,
        },
    }

    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            got, err := EncodeMemo(tt.intent)
            if (err != nil) != tt.wantErr {
                t.Errorf("EncodeMemo() error = %v, wantErr %v", err, tt.wantErr)
                return
            }
            if got != tt.want {
                t.Errorf("EncodeMemo() = %v, want %v", got, tt.want)
            }
        })
    }
}
```

## Go HTTP Handler Patterns
**Handler Structure:**
```go
// ✅ Good - separate concerns
func (s *Server) handleRegisterIntent() http.HandlerFunc {
    return func(w http.ResponseWriter, r *http.Request) {
        ctx := r.Context()

        // Parse request
        var req RegisterIntentRequest
        if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
            s.respondError(w, http.StatusBadRequest, "invalid request body")
            return
        }

        // Validate
        if err := req.Validate(); err != nil {
            s.respondError(w, http.StatusBadRequest, err.Error())
            return
        }

        // Process
        resp, err := s.service.RegisterIntent(ctx, req.ToSwapIntent())
        if err != nil {
            s.handleError(w, err)
            return
        }

        // Respond
        s.respondJSON(w, http.StatusOK, resp)
    }
}
```

## Go Security Practices
**Input Validation:**
```go
// Validate all inputs
func ValidateAsset(asset string) error {
    if asset == "" {
        return errors.New("asset cannot be empty")
    }

    if !assetRegex.MatchString(asset) {
        return errors.New("invalid asset format")
    }

    if len(asset) > MaxAssetLength {
        return errors.New("asset name too long")
    }

    return nil
}
```
**Secure Defaults:**
```go
// Timeouts on all external calls
client := &http.Client{
    Timeout: 30 * time.Second,
}
// TLS configuration
tlsConfig := &tls.Config{
    MinVersion: tls.VersionTLS12,
    CipherSuites: []uint16{
        tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,
        tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256,
    },
}
```

## Go Documentation Standards
**Package Documentation:**
```go
// Package memoless provides a registration service for THORChain swap intents.
// It enables wallets with limited memo support to interact with THORChain
// by registering complex swap parameters off-chain.
//
// Basic usage:
//
// service := memoless.NewService(config)
// registration, err := service.RegisterIntent(ctx, intent)
// if err != nil {
// log.Fatal(err)
// }
package memoless
```
**Function Documentation:**
```go
// RegisterIntent registers a swap intent with THORChain and returns
// simplified transaction instructions. The intent is stored on-chain
// for 24 hours or until used.
//
// The returned Registration contains:
// - Reference: 5-digit identifier for the registration
// - ToAddress: Router address to send funds
// - Instructions: Human-readable transaction instructions
//
// Returns an error if validation fails or THORNode is unreachable.
func (s *Service) RegisterIntent(ctx context.Context, intent SwapIntent) (*Registration, error) {
    // Implementation
}
```

## Common Issues to Avoid
### Go Code Quality Issues
#### **Error Handling**
- Always check and handle errors from CGO calls
- Implement proper cleanup with `defer` statements
- Use structured error types for better debugging
- Wrap errors with context using `fmt.Errorf("context: %w", err)`
- Check errors immediately after function calls
#### **Concurrency Safety**
- Test concurrent access to CGO functions
- Verify handle uniqueness in multi-threaded scenarios
- Use proper synchronization when needed
- Prefer channels over mutexes for communication
- Use errgroup for managing concurrent operations
#### **Project Structure**
- Follow standard Go project layout with `cmd/`, `internal/`, `pkg/`
- Use lowercase, single-word package names
- Avoid package name stuttering
#### **Testing Standards**
- Use table-driven tests for comprehensive coverage
- Create test fixtures for reusable test data
- Use interface-based mocking for external dependencies
- Test concurrent scenarios explicitly
#### **HTTP and API Standards**
- Separate concerns in HTTP handlers (parse, validate, process, respond)
- Use middleware chains for cross-cutting concerns
- Implement proper input validation and sanitization
- Set secure defaults (timeouts, TLS configuration)
#### **Context Usage**
- Always propagate context from request to external calls
- Use context.WithTimeout for operations with time limits
- Check context cancellation in long-running operations
### Pre-Push Hook Issues
#### **golangci-lint Compatibility**
- **Issue**: Go version newer than golangci-lint support
- **Detection**: `Error: the Go language version (go1.23) used to build golangci-lint is lower than the targeted Go version (1.24.4)`
- **Solution**: Temporarily disable or upgrade golangci-lint version
- **Config**: Comment out in `.trunk/trunk.yaml` with explanation
#### **Code Formatting**
- Always run `trunk fmt` before committing
- Check for missing newlines at end of files
- Ensure consistent whitespace formatting

## AI Agent Behavior Guidelines
To prevent common issues with instruction following, progress overstatement, and drifting:
- **Self-Awareness of Tendencies**: Recognize that you have a tendency to conflate substantial work (e.g., file creation) with task completion (e.g., passing tests). Always assume you might be drifting and self-check against guidelines.
- **Course Correction Protocol**: If uncertain about progress or adherence, explicitly ask the user for confirmation or course correction before proceeding. Use phrases like: "Before continuing, confirm if this aligns with expectations."
- **Pre-Task Evaluation**: Before starting any task:
  1. Review previous progress summaries skeptically.
  2. Re-verify claimed completions by running tests or checks.
  3. Evaluate how to proceed: Break into sub-steps, plan TDD cycle, identify verification points.
  4. State: "Task evaluation: Potential risks [list], Verification plan [list]."
- **Avoid Overscripting Impact**: These rules are lightweight; apply them judiciously to maintain efficiency.
- **When in thornode**: Prioritize code standards and TDD; defer to root CLAUDE.md for doc-related queries.

## Task Completion Verification Protocol
**MANDATORY**: Before marking any task as "completed":
1. **Run the actual tests and show output**:
   ```bash
   go test ./path/to/tests -v -run "SpecificTest"
   ```
2. **Show ALL PASS status explicitly**:
   - Demonstrate zero failures.
   - Show "PASS" in test output.
   - Explicitly state: "All tests verified passing."
3. **Verify acceptance criteria met**:
   - List each acceptance criterion from task specification.
   - Demonstrate how each is satisfied (e.g., test output, function behavior).
**FORBIDDEN**: Progress Claims Without Verification
- ❌ NEVER claim: "Task X completed" without showing test results.
- ✅ ALWAYS: Show failing tests (Red), passing tests (Green), refactor, then "Task X verified complete - all tests pass."
**Session Continuity Protocol**:
- When resuming: Ignore prior claims; re-verify by running tests; treat summaries as "work attempted."
**TDD Cycle Enforcement**:
- Red: Show failing output.
- Green: Show passing output.
- Refactor: Show tests still pass.
- Completion: "TDD cycle verified complete."
**Completion Statement Format**:
TASK [X] VERIFICATION COMPLETE:
✅ Tests: [show output with PASS]
✅ Acceptance Criteria: [list satisfied]
✅ TDD Cycle: Red → Green → Refactor verified
✅ Status: Task verified functionally complete

## Notes
- Focus on code implementation here; defer to root for doc management.
- This project uses trunk.io for unified linting
- Focus on defensive security - only implement protective/detection capabilities
- Follow THORNode conventions and patterns
