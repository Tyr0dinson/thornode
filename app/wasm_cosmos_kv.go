package app

import (
	"bytes"
	"encoding/json"
	"fmt"

	errorsmod "cosmossdk.io/errors"
	wasmtypes "github.com/CosmWasm/wasmd/x/wasm/types"
	wasmvmtypes "github.com/CosmWasm/wasmvm/v2/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
)

const (
	cosmosKVMaxRequestBytes = 8192
	cosmosKVMaxKeyBytes     = 4096
	cosmosKVMaxValueBytes   = 1 << 20
)

// wasmd redacts query errors to their codespace and code unless the outermost
// error is a system error or marked deterministic. These fixed texts and codes
// reach contracts, so they are consensus-visible and must not carry decoder text.
var (
	errCosmosKVInvalidRequest  = newCosmosKVError(2, "invalid cosmos_kv request")
	errCosmosKVRequestTooLarge = newCosmosKVError(3, fmt.Sprintf("cosmos_kv request exceeds %d bytes", cosmosKVMaxRequestBytes))
	errCosmosKVInvalidKey      = newCosmosKVError(4, fmt.Sprintf("cosmos_kv requires a 1..%d byte key", cosmosKVMaxKeyBytes))
	errCosmosKVUnknownStore    = newCosmosKVError(5, "unknown persistent cosmos store")
	errCosmosKVValueTooLarge   = newCosmosKVError(6, "cosmos_kv value exceeds 1 MiB")
)

func newCosmosKVError(code uint32, description string) error {
	return wasmtypes.MarkErrorDeterministic(errorsmod.Register("cosmos_kv", code, description))
}

type cosmosKVRequest struct {
	Store string `json:"store"`
	Key   []byte `json:"key"`
}

// cosmosKVQuery exposes read-only point queries over mounted persistent stores.
// Keys and values use JSON base64 encoding; a missing key returns {"value":null},
// while a stored empty value returns {"value":""}. Reads use the caller's context
// so that contract executions see current state and pay the SDK's normal KV gas.
func (app *THORChainApp) cosmosKVQuery(ctx sdk.Context, raw json.RawMessage) ([]byte, error) {
	if len(raw) > cosmosKVMaxRequestBytes {
		return nil, errCosmosKVRequestTooLarge
	}
	var variants map[string]json.RawMessage
	if err := json.Unmarshal(raw, &variants); err != nil {
		return nil, errCosmosKVInvalidRequest
	}
	payload, ok := variants["cosmos_kv"]
	if !ok || len(variants) != 1 {
		return nil, wasmvmtypes.UnsupportedRequest{Kind: "unknown custom query variant"}
	}
	var query cosmosKVRequest
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&query); err != nil {
		return nil, errCosmosKVInvalidRequest
	}
	if len(query.Key) == 0 || len(query.Key) > cosmosKVMaxKeyBytes {
		return nil, errCosmosKVInvalidKey
	}
	key := app.keys[query.Store]
	if key == nil {
		return nil, errCosmosKVUnknownStore
	}
	value := ctx.KVStore(key).Get(query.Key)
	if len(value) > cosmosKVMaxValueBytes {
		return nil, errCosmosKVValueTooLarge
	}
	return json.Marshal(struct {
		Value []byte `json:"value"`
	}{Value: value})
}
