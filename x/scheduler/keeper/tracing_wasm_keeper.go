package keeper

import (
	"time"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"gitlab.com/thorchain/thornode/v3/common/telemetry"
	"gitlab.com/thorchain/thornode/v3/x/scheduler/types"
)

// TracingWasmKeeper optionally traces scheduled Wasm calls.
type TracingWasmKeeper struct {
	keeper types.WasmKeeper
	tracer types.ContractTracer
}

var _ types.WasmKeeper = TracingWasmKeeper{}

func NewTracingWasmKeeper(
	keeper types.WasmKeeper,
	tracer types.ContractTracer,
) TracingWasmKeeper {
	return TracingWasmKeeper{
		keeper: keeper,
		tracer: tracer,
	}
}

func (k TracingWasmKeeper) Execute(
	ctx sdk.Context,
	contract, caller sdk.AccAddress,
	payload []byte,
	coins sdk.Coins,
) (result []byte, err error) {
	if !tracingEnabled(k.tracer) {
		return k.keeper.Execute(ctx, contract, caller, payload, coins)
	}

	start := time.Now()
	outcome := telemetry.OutcomePanic
	defer func() {
		recordContract(
			k.tracer,
			ctx,
			telemetry.ContractExecute,
			contract,
			payload,
			outcome,
			time.Since(start),
		)
	}()

	result, err = k.keeper.Execute(ctx, contract, caller, payload, coins)
	outcome = telemetry.Outcome(err)
	return result, err
}

func tracingEnabled(tracer types.ContractTracer) (enabled bool) {
	if tracer == nil {
		return false
	}
	defer func() {
		if recover() != nil {
			enabled = false
		}
	}()
	return tracer.ContractTracingEnabled()
}

func recordContract(
	tracer types.ContractTracer,
	ctx sdk.Context,
	operation string,
	contract sdk.AccAddress,
	payload []byte,
	outcome string,
	duration time.Duration,
) {
	// Telemetry failures must not replace the contract result or panic.
	defer func() {
		_ = recover()
	}()
	// Leave code ID empty to avoid a telemetry-only store read.
	const codeID uint64 = 0
	tracer.RecordContract(
		ctx,
		operation,
		contract.String(),
		codeID,
		payload,
		outcome,
		duration,
	)
}
