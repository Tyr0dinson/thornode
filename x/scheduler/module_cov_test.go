package scheduler_test

import (
	"encoding/json"
	"testing"

	"cosmossdk.io/collections"
	storetypes "cosmossdk.io/store/types"
	"github.com/cosmos/cosmos-sdk/baseapp"
	"github.com/cosmos/cosmos-sdk/client"
	"github.com/cosmos/cosmos-sdk/codec"
	"github.com/cosmos/cosmos-sdk/runtime"
	"github.com/cosmos/cosmos-sdk/testutil"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/module"
	moduletestutil "github.com/cosmos/cosmos-sdk/types/module/testutil"
	gwruntime "github.com/grpc-ecosystem/grpc-gateway/runtime"
	"github.com/stretchr/testify/require"

	"gitlab.com/thorchain/thornode/v3/x/scheduler"
	"gitlab.com/thorchain/thornode/v3/x/scheduler/keeper"
	"gitlab.com/thorchain/thornode/v3/x/scheduler/types"
)

var (
	senderCov1 = sdk.AccAddress([]byte("sender1_____________"))
	senderCov2 = sdk.AccAddress([]byte("sender2_____________"))
)

type execCallCov struct {
	contract sdk.AccAddress
	caller   sdk.AccAddress
	msg      string
}

type recordingWasmCov struct {
	calls []execCallCov
}

func (r *recordingWasmCov) Execute(_ sdk.Context, contract, caller sdk.AccAddress, msg []byte, _ sdk.Coins) ([]byte, error) {
	r.calls = append(r.calls, execCallCov{contract: contract, caller: caller, msg: string(msg)})
	return nil, nil
}

type schedFixtureCov struct {
	ctx    sdk.Context
	key    *storetypes.KVStoreKey
	keeper keeper.Keeper
	module scheduler.AppModule
	cdc    codec.Codec
	encCfg moduletestutil.TestEncodingConfig
	wasm   *recordingWasmCov
}

func newSchedFixtureCov(t *testing.T) schedFixtureCov {
	t.Helper()
	key := storetypes.NewKVStoreKey(types.StoreKey)
	ctx := testutil.DefaultContextWithKeys(
		map[string]*storetypes.KVStoreKey{types.StoreKey: key},
		storetypes.NewTransientStoreKeys(),
		storetypes.NewMemoryStoreKeys(),
	)
	encCfg := moduletestutil.MakeTestEncodingConfig()
	types.RegisterInterfaces(encCfg.InterfaceRegistry)
	wasm := &recordingWasmCov{}
	k := keeper.NewKeeper(encCfg.Codec, runtime.NewKVStoreService(key), wasm, senderCov1.String())
	return schedFixtureCov{
		ctx:    ctx,
		key:    key,
		keeper: k,
		module: scheduler.NewAppModule(encCfg.Codec, k),
		cdc:    encCfg.Codec,
		encCfg: encCfg,
		wasm:   wasm,
	}
}

func sampleSchedulesCov() []types.Schedule {
	return []types.Schedule{
		{Height: 5, Msgs: []types.MsgScheduleExecuteContract{
			{Sender: senderCov1.String(), After: 1, Msg: []byte(`{"a":1}`)},
			{Sender: senderCov2.String(), After: 2, Msg: []byte(`{"b":2}`)},
		}},
		{Height: 9, Msgs: []types.MsgScheduleExecuteContract{
			{Sender: senderCov2.String(), After: 3, Msg: []byte(`{"c":3}`)},
		}},
	}
}

func TestSchedulerGenesisRoundTripCov(t *testing.T) {
	f := newSchedFixtureCov(t)

	gs := scheduler.ExportGenesis(f.ctx, f.keeper)
	require.NotNil(t, gs.Schedules)
	require.Empty(t, gs.Schedules)

	in := types.GenesisState{Schedules: sampleSchedulesCov()}
	require.NoError(t, in.Validate())
	scheduler.InitGenesis(f.ctx, f.keeper, in)

	sched, err := f.keeper.GetSchedule(f.ctx, 5)
	require.NoError(t, err)
	require.Len(t, sched.Msgs, 2)

	// sender index was populated
	has, err := f.keeper.SenderIndex().Has(f.ctx, collections.Join(senderCov2.String(), uint64(9)))
	require.NoError(t, err)
	require.True(t, has)

	out := scheduler.ExportGenesis(f.ctx, f.keeper)
	require.Equal(t, in.Schedules, out.Schedules)
}

func TestSchedulerExportGenesisCorruptPanicsCov(t *testing.T) {
	f := newSchedFixtureCov(t)
	// write an undecodable value directly under the schedule prefix
	keyBz, err := collections.EncodeKeyWithPrefix(types.SchedulePrefix, collections.Uint64Key, uint64(3))
	require.NoError(t, err)
	f.ctx.KVStore(f.key).Set(keyBz, []byte{0xff, 0xff, 0xff})
	require.Panics(t, func() { scheduler.ExportGenesis(f.ctx, f.keeper) })
}

func TestSchedulerAppModuleBasicsCov(t *testing.T) {
	f := newSchedFixtureCov(t)
	am := f.module

	require.Equal(t, types.ModuleName, am.Name())
	require.Equal(t, types.ModuleName, scheduler.NewAppModuleBasic(f.cdc).Name())
	require.Equal(t, types.QuerierRoute, am.QuerierRoute())
	require.Equal(t, uint64(scheduler.ConsensusVersion), am.ConsensusVersion())
	require.NotPanics(t, am.IsOnePerModuleType)
	require.NotPanics(t, am.IsAppModule)
	require.Nil(t, am.GetTxCmd())

	// NOTE: the scheduler module currently returns the x/denom query command
	// tree (it imports x/denom/client/cli). This documents current behaviour.
	qcmd := am.GetQueryCmd()
	require.NotNil(t, qcmd)
	require.Equal(t, "denom", qcmd.Use)

	require.Nil(t, am.ProposalContents(module.SimulationState{}))
	require.Nil(t, am.WeightedOperations(module.SimulationState{}))
	require.NotPanics(t, func() {
		am.RegisterStoreDecoder(nil)
		am.RegisterInvariants(nil)
		am.RegisterLegacyAminoCodec(codec.NewLegacyAmino())
	})

	def := am.DefaultGenesis(f.cdc)
	require.NoError(t, am.ValidateGenesis(f.cdc, nil, def))
	var gs types.GenesisState
	require.NoError(t, f.cdc.UnmarshalJSON(def, &gs))
	require.Empty(t, gs.Schedules)

	err := am.ValidateGenesis(f.cdc, nil, json.RawMessage(`{"schedules":`))
	require.ErrorContains(t, err, "failed to unmarshal scheduler genesis state")

	bad := types.GenesisState{Schedules: []types.Schedule{{Height: 0}}}
	err = am.ValidateGenesis(f.cdc, nil, f.cdc.MustMarshalJSON(&bad))
	require.ErrorIs(t, err, types.ErrInvalidGenesis)

	simState := &module.SimulationState{Cdc: f.cdc, GenState: map[string]json.RawMessage{}}
	am.GenerateGenesisState(simState)
	require.JSONEq(t, string(def), string(simState.GenState[types.ModuleName]))
}

func TestSchedulerAppModuleGenesisAndEndBlockCov(t *testing.T) {
	f := newSchedFixtureCov(t)
	am := f.module

	in := types.GenesisState{Schedules: sampleSchedulesCov()}
	require.Empty(t, am.InitGenesis(f.ctx, f.cdc, f.cdc.MustMarshalJSON(&in)))

	var out types.GenesisState
	require.NoError(t, f.cdc.UnmarshalJSON(am.ExportGenesis(f.ctx, f.cdc), &out))
	require.Equal(t, in.Schedules, out.Schedules)

	// EndBlock at a height with no schedule is a no-op
	updates, err := am.EndBlock(f.ctx.WithBlockHeight(4))
	require.NoError(t, err)
	require.Empty(t, updates)
	require.Empty(t, f.wasm.calls)

	// EndBlock at height 5 executes both messages, as the scheduler module
	// account, and removes the schedule.
	updates, err = am.EndBlock(f.ctx.WithBlockHeight(5))
	require.NoError(t, err)
	require.Empty(t, updates)
	require.Len(t, f.wasm.calls, 2)
	require.Equal(t, senderCov1, f.wasm.calls[0].contract)
	require.Equal(t, `{"a":1}`, f.wasm.calls[0].msg)
	require.Equal(t, senderCov2, f.wasm.calls[1].contract)
	has, err := f.keeper.Store().Has(f.ctx, 5)
	require.NoError(t, err)
	require.False(t, has)

	require.Panics(t, func() { am.InitGenesis(f.ctx, f.cdc, json.RawMessage(`nope`)) })
}

func TestSchedulerAppModuleRegisterServicesCov(t *testing.T) {
	f := newSchedFixtureCov(t)

	msgRouter := baseapp.NewMsgServiceRouter()
	msgRouter.SetInterfaceRegistry(f.encCfg.InterfaceRegistry)
	queryRouter := baseapp.NewGRPCQueryRouter()
	queryRouter.SetInterfaceRegistry(f.encCfg.InterfaceRegistry)

	f.module.RegisterServices(module.NewConfigurator(f.cdc, msgRouter, queryRouter))
	require.NotNil(t, msgRouter.HandlerByTypeURL(sdk.MsgTypeURL(&types.MsgScheduleExecuteContract{})))

	reg := moduletestutil.MakeTestEncodingConfig().InterfaceRegistry
	f.module.RegisterInterfaces(reg)
	_, err := reg.Resolve(sdk.MsgTypeURL(&types.MsgScheduleExecuteContract{}))
	require.NoError(t, err)

	mux := gwruntime.NewServeMux()
	require.NotPanics(t, func() { f.module.RegisterGRPCGatewayRoutes(client.Context{}, mux) })
}
