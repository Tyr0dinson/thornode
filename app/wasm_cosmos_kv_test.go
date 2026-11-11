package app

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	errorsmod "cosmossdk.io/errors"
	storetypes "cosmossdk.io/store/types"
	wasmkeeper "github.com/CosmWasm/wasmd/x/wasm/keeper"
	wasmvmtypes "github.com/CosmWasm/wasmvm/v2/types"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	dbm "github.com/cosmos/cosmos-db"
	"github.com/cosmos/cosmos-sdk/testutil"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"

	"gitlab.com/thorchain/thornode/v3/common"
	thortypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

func cosmosKVTestRequest(t *testing.T, store string, key []byte) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"cosmos_kv": map[string]any{"store": store, "key": key}})
	require.NoError(t, err)
	return raw
}

func cosmosKVTestValue(t *testing.T, raw []byte) []byte {
	t.Helper()
	var result struct {
		Value []byte `json:"value"`
	}
	require.NoError(t, json.Unmarshal(raw, &result))
	return result.Value
}

func cosmosKVFixture() (*THORChainApp, sdk.Context) {
	keys := storetypes.NewKVStoreKeys("thorchain", "bank")
	return &THORChainApp{keys: keys}, testutil.DefaultContextWithKeys(keys, nil, nil)
}

// cosmosKVHandlerQuery sends a request through wasmd's redacting query handler.
func cosmosKVHandlerQuery(app *THORChainApp, ctx sdk.Context, raw json.RawMessage, gasLimit uint64) ([]byte, error) {
	plugins := &wasmkeeper.QueryPlugins{Custom: app.cosmosKVQuery}
	handler := wasmkeeper.NewQueryHandler(ctx, plugins, nil, WasmGasRegister)
	return handler.Query(wasmvmtypes.QueryRequest{Custom: raw}, WasmGasRegister.ToWasmVMGas(gasLimit))
}

// cosmosKVRejectedQuery returns the error for a request that must fail before
// any store access.
func cosmosKVRejectedQuery(t *testing.T, app *THORChainApp, parent sdk.Context, raw string) error {
	t.Helper()
	ctx := parent.WithGasMeter(storetypes.NewGasMeter(1_000_000))
	result, err := cosmosKVHandlerQuery(app, ctx, json.RawMessage(raw), 1_000_000)
	require.Nil(t, result)
	require.Zero(t, ctx.GasMeter().GasConsumed(), "invalid requests must not access stores")
	return err
}

// requireCosmosKVError pins the registered error and the result a contract sees.
func requireCosmosKVError(t *testing.T, err error, code uint32, text string) {
	t.Helper()
	codespace, abciCode, _ := errorsmod.ABCIInfo(err, false)
	require.Equal(t, "cosmos_kv", codespace)
	require.Equal(t, code, abciCode)
	require.Equal(t, wasmvmtypes.QuerierResult{Ok: &wasmvmtypes.QueryResult{Err: text}}, wasmvmtypes.ToQuerierResult(nil, err))
}

func TestCosmosKVPointReadsAndMetering(t *testing.T) {
	app, parent := cosmosKVFixture()
	key := []byte{0, 255, 1}
	for _, store := range []string{"thorchain", "bank"} {
		parent.KVStore(app.keys[store]).Set(key, []byte(store))
	}
	parent.KVStore(app.keys["thorchain"]).Set([]byte("empty"), []byte{})
	for _, tc := range []struct {
		name  string
		store string
		key   []byte
		value []byte
	}{
		{"binary key", "thorchain", key, []byte("thorchain")},
		{"different store", "bank", key, []byte("bank")},
		{"missing", "thorchain", []byte("missing"), nil},
		{"empty", "thorchain", []byte("empty"), []byte{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := parent.WithGasMeter(storetypes.NewGasMeter(1_000_000))
			raw, err := app.cosmosKVQuery(ctx, cosmosKVTestRequest(t, tc.store, tc.key))
			require.NoError(t, err)
			require.Equal(t, tc.value, cosmosKVTestValue(t, raw))
			gas := ctx.GasMeter().GasConsumed()
			require.Positive(t, gas)
			direct := parent.WithGasMeter(storetypes.NewGasMeter(1_000_000))
			require.Equal(t, tc.value, direct.KVStore(app.keys[tc.store]).Get(tc.key))
			require.Equal(t, direct.GasMeter().GasConsumed(), gas)
		})
	}
	// A query sees uncommitted execution state without writing into the parent.
	ctx, _ := parent.CacheContext()
	ctx.KVStore(app.keys["thorchain"]).Set(key, []byte("cached"))
	raw, err := app.cosmosKVQuery(ctx, cosmosKVTestRequest(t, "thorchain", key))
	require.NoError(t, err)
	require.JSONEq(t, `{"value":"Y2FjaGVk"}`, string(raw))
	require.Equal(t, []byte("thorchain"), parent.KVStore(app.keys["thorchain"]).Get(key))
}

func TestCosmosKVRejectsInvalidRequests(t *testing.T) {
	app, parent := cosmosKVFixture()
	const (
		invalidRequest = "invalid cosmos_kv request"
		requestTooBig  = "cosmos_kv request exceeds 8192 bytes"
		invalidKey     = "cosmos_kv requires a 1..4096 byte key"
		unknownStore   = "unknown persistent cosmos store"
	)
	for _, tc := range []struct {
		name string
		raw  string
		code uint32
		text string
	}{
		{"malformed", `{`, 2, invalidRequest},
		{"not an object", `[]`, 2, invalidRequest},
		{"invalid base64", `{"cosmos_kv":{"store":"thorchain","key":"!"}}`, 2, invalidRequest},
		{"wrong type", `{"cosmos_kv":{"store":"thorchain","key":true}}`, 2, invalidRequest},
		{"unknown field", `{"cosmos_kv":{"store":"thorchain","key":"YQ==","write":true}}`, 2, invalidRequest},
		{"trailing JSON", `{"cosmos_kv":{"store":"thorchain","key":"YQ=="}}{}`, 2, invalidRequest},
		{"trailing garbage", `{"cosmos_kv":{"store":"thorchain","key":"YQ=="}}x`, 2, invalidRequest},
		{"long request", strings.Repeat(" ", cosmosKVMaxRequestBytes+1), 3, requestTooBig},
		{"null query", `{"cosmos_kv":null}`, 4, invalidKey},
		{"empty key", `{"cosmos_kv":{"store":"thorchain","key":""}}`, 4, invalidKey},
		{"missing key", `{"cosmos_kv":{"store":"thorchain"}}`, 4, invalidKey},
		{"long key", string(cosmosKVTestRequest(t, "thorchain", bytes.Repeat([]byte{1}, cosmosKVMaxKeyBytes+1))), 4, invalidKey},
		{"unknown store", `{"cosmos_kv":{"store":"unknown","key":"YQ=="}}`, 5, unknownStore},
		{"missing store", `{"cosmos_kv":{"key":"YQ=="}}`, 5, unknownStore},
	} {
		t.Run(tc.name, func(t *testing.T) {
			requireCosmosKVError(t, cosmosKVRejectedQuery(t, app, parent, tc.raw), tc.code, tc.text)
		})
	}
}

func TestCosmosKVUnsupportedVariants(t *testing.T) {
	app, parent := cosmosKVFixture()
	for _, tc := range []struct {
		name string
		raw  string
	}{
		{"unknown query", `{"cosmos_kv_range":{"store":"thorchain","prefix":"YQ==","limit":1}}`},
		{"no query", `{}`},
		{"null request", `null`},
		{"extra query", `{"cosmos_kv":{"store":"thorchain","key":"YQ=="},"other":{}}`},
		{"case variant", `{"COSMOS_KV":{"store":"thorchain","key":"YQ=="}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := cosmosKVRejectedQuery(t, app, parent, tc.raw)
			want := wasmvmtypes.UnsupportedRequest{Kind: "unknown custom query variant"}
			require.Equal(t, want, err)
			require.Equal(t, &want, wasmvmtypes.ToQuerierResult(nil, err).Err.UnsupportedRequest)
		})
	}
}

func TestCosmosKVLimits(t *testing.T) {
	app, parent := cosmosKVFixture()
	key := bytes.Repeat([]byte{255}, cosmosKVMaxKeyBytes)
	value := bytes.Repeat([]byte{255}, cosmosKVMaxValueBytes)
	parent.KVStore(app.keys["thorchain"]).Set(key, value)
	raw := cosmosKVTestRequest(t, "thorchain", key)
	// Whitespace pads the request to exactly the accepted JSON byte limit.
	raw = append(raw, bytes.Repeat([]byte(" "), cosmosKVMaxRequestBytes-len(raw))...)
	response, err := app.cosmosKVQuery(parent, raw)
	require.NoError(t, err)
	require.Equal(t, value, cosmosKVTestValue(t, response))

	parent.KVStore(app.keys["thorchain"]).Set(key, append(value, 0))
	ctx := parent.WithGasMeter(storetypes.NewGasMeter(10_000_000))
	response, err = cosmosKVHandlerQuery(app, ctx, raw, 10_000_000)
	requireCosmosKVError(t, err, 6, "cosmos_kv value exceeds 1 MiB")
	require.Nil(t, response)
	require.Positive(t, ctx.GasMeter().GasConsumed(), "rejected values must still pay read gas")
}

func TestCosmosKVWasmQueryGasLimit(t *testing.T) {
	app, parent := cosmosKVFixture()
	parent.KVStore(app.keys["thorchain"]).Set([]byte("key"), []byte("value"))
	raw := cosmosKVTestRequest(t, "thorchain", []byte("key"))
	ctx := parent.WithGasMeter(storetypes.NewGasMeter(1_000_000))
	response, err := cosmosKVHandlerQuery(app, ctx, raw, 100_000)
	require.NoError(t, err)
	require.JSONEq(t, `{"value":"dmFsdWU="}`, string(response))
	require.Positive(t, ctx.GasMeter().GasConsumed())

	ctx = parent.WithGasMeter(storetypes.NewGasMeter(1_000_000))
	require.PanicsWithValue(t, storetypes.ErrorOutOfGas{Descriptor: storetypes.GasReadCostFlatDesc}, func() {
		_, _ = cosmosKVHandlerQuery(app, ctx, raw, 1)
	})
	require.Positive(t, ctx.GasMeter().GasConsumed(), "failed subqueries must charge the parent")
}

func TestCosmosKVNativePoolParity(t *testing.T) {
	app := replayApp(t, dbm.NewMemDB())
	ctx := app.NewUncachedContext(false, cmtproto.Header{Height: 1})
	pool := thortypes.NewPool()
	pool.Asset = common.BTCAsset
	require.NoError(t, app.ThorchainKeeper.SetPool(ctx, pool))
	// The literal request is the example in app-layer/README.md.
	response, err := app.cosmosKVQuery(ctx, json.RawMessage(`{"cosmos_kv":{"store":"thorchain","key":"cG9vbC8vQlRDLkJUQw=="}}`))
	require.NoError(t, err)
	require.Equal(t, app.appCodec.MustMarshal(&pool), cosmosKVTestValue(t, response))
}
