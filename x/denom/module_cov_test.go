package denom_test

import (
	"encoding/json"
	"testing"

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

	"gitlab.com/thorchain/thornode/v3/x/denom"
	"gitlab.com/thorchain/thornode/v3/x/denom/keeper"
	"gitlab.com/thorchain/thornode/v3/x/denom/types"
)

var (
	adminCov1 = sdk.AccAddress([]byte("admin1______________"))
	adminCov2 = sdk.AccAddress([]byte("admin2______________"))
)

type denomFixtureCov struct {
	ctx    sdk.Context
	keeper keeper.Keeper
	module denom.AppModule
	cdc    codec.Codec
	encCfg moduletestutil.TestEncodingConfig
}

func newDenomFixtureCov(t *testing.T) denomFixtureCov {
	t.Helper()
	key := storetypes.NewKVStoreKey(types.StoreKey)
	ctx := testutil.DefaultContextWithKeys(
		map[string]*storetypes.KVStoreKey{types.StoreKey: key},
		storetypes.NewTransientStoreKeys(),
		storetypes.NewMemoryStoreKeys(),
	)
	encCfg := moduletestutil.MakeTestEncodingConfig()
	types.RegisterInterfaces(encCfg.InterfaceRegistry)
	k := keeper.NewKeeper(encCfg.Codec, runtime.NewKVStoreService(key), nil, nil, nil, adminCov1.String())
	return denomFixtureCov{
		ctx:    ctx,
		keeper: k,
		module: denom.NewAppModule(encCfg.Codec, k, nil, nil),
		cdc:    encCfg.Codec,
		encCfg: encCfg,
	}
}

func TestDenomGenesisRoundTripCov(t *testing.T) {
	f := newDenomFixtureCov(t)

	// empty store exports an empty (non-nil) list
	gs := denom.ExportGenesis(f.ctx, f.keeper)
	require.NotNil(t, gs.Admins)
	require.Empty(t, gs.Admins)

	in := types.GenesisState{Admins: []types.GenesisDenom{
		{Denom: "x/btc", Admin: adminCov1.String()},
		{Denom: "x/eth", Admin: adminCov2.String()},
	}}
	require.NoError(t, in.Validate())
	denom.InitGenesis(f.ctx, f.keeper, in)

	admin, err := f.keeper.GetAdmin(f.ctx, "x/btc")
	require.NoError(t, err)
	require.Equal(t, adminCov1, admin)

	out := denom.ExportGenesis(f.ctx, f.keeper)
	require.ElementsMatch(t, in.Admins, out.Admins)
}

func TestDenomInitGenesisPanicsCov(t *testing.T) {
	f := newDenomFixtureCov(t)
	// wrong prefix
	require.Panics(t, func() {
		denom.InitGenesis(f.ctx, f.keeper, types.GenesisState{Admins: []types.GenesisDenom{
			{Denom: "y/btc", Admin: adminCov1.String()},
		}})
	})
	// bad admin address
	require.Panics(t, func() {
		denom.InitGenesis(f.ctx, f.keeper, types.GenesisState{Admins: []types.GenesisDenom{
			{Denom: "x/btc", Admin: "bogus"},
		}})
	})
	admin, err := f.keeper.GetAdmin(f.ctx, "x/btc")
	require.NoError(t, err)
	require.Nil(t, admin)
}

func TestDenomAppModuleBasicsCov(t *testing.T) {
	f := newDenomFixtureCov(t)
	am := f.module

	require.Equal(t, types.ModuleName, am.Name())
	require.Equal(t, types.ModuleName, denom.NewAppModuleBasic(f.cdc).Name())
	require.Equal(t, types.QuerierRoute, am.QuerierRoute())
	require.Equal(t, uint64(denom.ConsensusVersion), am.ConsensusVersion())
	require.NotPanics(t, am.IsOnePerModuleType)
	require.NotPanics(t, am.IsAppModule)

	// no tx commands: msgs are only dispatched from contracts
	require.Nil(t, am.GetTxCmd())
	qcmd := am.GetQueryCmd()
	require.NotNil(t, qcmd)
	require.Equal(t, types.ModuleName, qcmd.Use)

	require.Nil(t, am.ProposalContents(module.SimulationState{}))
	require.Nil(t, am.WeightedOperations(module.SimulationState{}))
	require.NotPanics(t, func() {
		am.RegisterStoreDecoder(nil)
		am.RegisterInvariants(nil)
		am.RegisterRESTRoutes(client.Context{}, nil)
		am.RegisterLegacyAminoCodec(codec.NewLegacyAmino())
	})

	// default genesis is valid and empty
	def := am.DefaultGenesis(f.cdc)
	require.NoError(t, am.ValidateGenesis(f.cdc, nil, def))
	var gs types.GenesisState
	require.NoError(t, f.cdc.UnmarshalJSON(def, &gs))
	require.Empty(t, gs.Admins)

	// malformed json
	err := am.ValidateGenesis(f.cdc, nil, json.RawMessage(`{"admins":`))
	require.ErrorContains(t, err, "failed to unmarshal denom genesis state")
	// duplicate denoms
	dup := types.GenesisState{Admins: []types.GenesisDenom{
		{Denom: "x/btc", Admin: adminCov1.String()},
		{Denom: "x/btc", Admin: adminCov2.String()},
	}}
	err = am.ValidateGenesis(f.cdc, nil, f.cdc.MustMarshalJSON(&dup))
	require.ErrorIs(t, err, types.ErrInvalidGenesis)

	// simulation genesis is the default genesis
	simState := &module.SimulationState{Cdc: f.cdc, GenState: map[string]json.RawMessage{}}
	am.GenerateGenesisState(simState)
	require.JSONEq(t, string(def), string(simState.GenState[types.ModuleName]))
}

func TestDenomAppModuleGenesisCov(t *testing.T) {
	f := newDenomFixtureCov(t)
	am := f.module

	in := types.GenesisState{Admins: []types.GenesisDenom{{Denom: "x/btc", Admin: adminCov2.String()}}}
	updates := am.InitGenesis(f.ctx, f.cdc, f.cdc.MustMarshalJSON(&in))
	require.Empty(t, updates)

	var out types.GenesisState
	require.NoError(t, f.cdc.UnmarshalJSON(am.ExportGenesis(f.ctx, f.cdc), &out))
	require.Equal(t, in.Admins, out.Admins)

	require.Panics(t, func() { am.InitGenesis(f.ctx, f.cdc, json.RawMessage(`not json`)) })
}

func TestDenomAppModuleRegisterServicesCov(t *testing.T) {
	f := newDenomFixtureCov(t)

	msgRouter := baseapp.NewMsgServiceRouter()
	msgRouter.SetInterfaceRegistry(f.encCfg.InterfaceRegistry)
	queryRouter := baseapp.NewGRPCQueryRouter()
	queryRouter.SetInterfaceRegistry(f.encCfg.InterfaceRegistry)

	f.module.RegisterServices(module.NewConfigurator(f.cdc, msgRouter, queryRouter))

	require.NotNil(t, msgRouter.HandlerByTypeURL(sdk.MsgTypeURL(&types.MsgCreateDenom{})))
	require.NotNil(t, msgRouter.HandlerByTypeURL(sdk.MsgTypeURL(&types.MsgMintTokens{})))
	require.NotNil(t, queryRouter.Route("/thorchain.denom.v1.Query/DenomAdmin"))

	// registering interfaces makes the msgs resolvable
	reg := moduletestutil.MakeTestEncodingConfig().InterfaceRegistry
	f.module.RegisterInterfaces(reg)
	_, err := reg.Resolve(sdk.MsgTypeURL(&types.MsgCreateDenom{}))
	require.NoError(t, err)

	// gateway routes register without error
	mux := gwruntime.NewServeMux()
	require.NotPanics(t, func() { f.module.RegisterGRPCGatewayRoutes(client.Context{}, mux) })
}
