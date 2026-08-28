package types

import (
	"context"
	"time"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"gitlab.com/thorchain/thornode/v3/constants"
)

type AccountKeeper interface {
	SetModuleAccount(ctx context.Context, macc sdk.ModuleAccountI)
}

type WasmKeeper interface {
	Execute(ctx sdk.Context, contractAddress, caller sdk.AccAddress, msg []byte, coins sdk.Coins) ([]byte, error)
}

type ContractTracer interface {
	ContractTracingEnabled() bool
	RecordContract(
		ctx sdk.Context,
		operation, contractAddress string,
		codeID uint64,
		payload []byte,
		outcome string,
		duration time.Duration,
	)
}

type WasmGasConfigKeeper interface {
	GetConfigInt64(ctx sdk.Context, key constants.ConstantName) int64
}
