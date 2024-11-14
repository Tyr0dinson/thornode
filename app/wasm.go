package app

import (
	wasmkeeper "github.com/CosmWasm/wasmd/x/wasm/keeper"
	apitypes "gitlab.com/thorchain/thornode/api/types"
)

var wasmAcceptedQueries = wasmkeeper.AcceptedQueries{
	"/types.Query/Pool": &apitypes.QueryPoolResponse{},
}
