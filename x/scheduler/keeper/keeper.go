package keeper

import (
	"fmt"

	storetypes "cosmossdk.io/core/store"
	"cosmossdk.io/log"
	sdkstore "cosmossdk.io/store/types"
	"github.com/cosmos/cosmos-sdk/codec"
	sdk "github.com/cosmos/cosmos-sdk/types"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	"gitlab.com/thorchain/thornode/v3/common/wasmgas"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/scheduler/types"
)

type (
	Keeper struct {
		cdc          codec.Codec
		storeService storetypes.KVStoreService
		wasmKeeper   types.WasmKeeper
		gasConfig    types.WasmGasConfigKeeper

		authority string
	}
)

func NewKeeper(
	cdc codec.Codec,
	storeService storetypes.KVStoreService,
	wasmKeeper types.WasmKeeper,
	gasConfig types.WasmGasConfigKeeper,
	authority string,
) Keeper {
	return Keeper{
		cdc:          cdc,
		storeService: storeService,
		wasmKeeper:   wasmKeeper,
		gasConfig:    gasConfig,
		authority:    authority,
	}
}

func (k Keeper) Logger(ctx sdk.Context) log.Logger {
	return ctx.Logger().With("module", "x/"+types.ModuleName)
}

func (k Keeper) ExecuteSchedule(ctx sdk.Context) error {
	schedule, err := k.GetSchedule(ctx, uint64(ctx.BlockHeight()))
	if err != nil {
		// Data-dependent store/decode failure must not halt the chain. Skip this
		// height; block height only increases, so it will not be revisited.
		k.Logger(ctx).Error(
			"failed to read schedule, skipping height",
			"height", ctx.BlockHeight(),
			"error", err,
		)
		return nil
	}
	for _, msg := range schedule.Msgs {
		// Already validated in msg server
		sender, _ := sdk.AccAddressFromBech32(msg.Sender)
		execCtx, commit := ctx.CacheContext()
		// EndBlock runs under an infinite gas meter; bound each scheduled
		// execution so a single contract cannot exhaust the block-time budget.
		// The activatable wasm meter additionally applies the WasmMaxGasPerTx and
		// WasmMaxGasPerBlock mimir limits on top of that hard bound.
		execCtx = execCtx.WithGasMeter(wasmgas.NewMeter(sdkstore.NewGasMeter(types.MaxScheduleExecGas)))
		if k.gasConfig != nil {
			execCtx = wasmgas.Activate(
				execCtx,
				k.gasConfig.GetConfigInt64(ctx, constants.WasmMaxGasPerTx),
				k.gasConfig.GetConfigInt64(ctx, constants.WasmMaxGasPerBlock),
			)
		}
		execCtx.EventManager().EmitEvent(sdk.NewEvent(
			types.EventExecuteStart,
			sdk.NewAttribute(types.AttributeSender, msg.Sender),
			sdk.NewAttribute(types.AttributeMsg, string(msg.Msg)),
		))
		func() {
			defer func() {
				if r := recover(); r != nil {
					k.Logger(ctx).Error(
						"panic in scheduled contract execution",
						"sender", msg.Sender,
						"error", fmt.Sprintf("%v", r),
					)
				}
			}()
			_, execErr := k.wasmKeeper.Execute(
				execCtx,
				sender,
				authtypes.NewModuleAddress(types.ModuleName),
				msg.Msg,
				sdk.Coins{},
			)
			if execErr != nil {
				k.Logger(ctx).Error(
					"failed to execute scheduled contract",
					"sender", msg.Sender,
					"msg", string(msg.Msg),
					"error", execErr,
				)
				return
			}
			execCtx.EventManager().EmitEvent(sdk.NewEvent(
				types.EventExecuteEnd,
			))

			commit()
		}()
		k.consumeScheduledGas(ctx, msg.Sender, execCtx.GasMeter().GasConsumedToLimit())
	}
	err = k.RemoveSchedule(ctx, schedule.Height)
	if err != nil {
		// Non-fatal: a data-dependent removal failure leaves a stale entry at a
		// past height (never revisited) but must not halt the chain.
		k.Logger(ctx).Error(
			"failed to remove schedule",
			"height", schedule.Height,
			"error", err,
		)
		return nil
	}
	return nil
}

// consumeScheduledGas adds EndBlock Wasm work to the shared block meter so a
// burst of scheduled callbacks cannot reset the aggregate allowance per call.
// A finite SDK consensus meter can panic when crossed; keep that normal gas
// signal scoped to this scheduled call, as ExecuteSchedule already does for VM
// out-of-gas panics.
func (k Keeper) consumeScheduledGas(ctx sdk.Context, sender string, gas sdkstore.Gas) {
	if gas == 0 || ctx.BlockGasMeter() == nil {
		return
	}
	defer func() {
		if r := recover(); r != nil {
			k.Logger(ctx).Error(
				"block gas exhausted by scheduled contract execution",
				"sender", sender,
				"error", fmt.Sprintf("%v", r),
			)
		}
	}()
	ctx.BlockGasMeter().ConsumeGas(gas, "scheduled wasm execution")
}
