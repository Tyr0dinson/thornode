package app

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"cosmossdk.io/log"
	upgradetypes "cosmossdk.io/x/upgrade/types"
	wasmkeeper "github.com/CosmWasm/wasmd/x/wasm/keeper"
	abci "github.com/cometbft/cometbft/abci/types"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	dbm "github.com/cosmos/cosmos-db"
	"github.com/cosmos/cosmos-sdk/baseapp"
	"github.com/cosmos/cosmos-sdk/client"
	"github.com/cosmos/cosmos-sdk/client/flags"
	"github.com/cosmos/cosmos-sdk/crypto/keys/ed25519"
	"github.com/cosmos/cosmos-sdk/server/api"
	serverconfig "github.com/cosmos/cosmos-sdk/server/config"
	simtestutil "github.com/cosmos/cosmos-sdk/testutil/sims"
	sdk "github.com/cosmos/cosmos-sdk/types"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"
	"github.com/gorilla/mux"
	"github.com/stretchr/testify/require"

	"gitlab.com/thorchain/thornode/v3/app/upgrades"
	"gitlab.com/thorchain/thornode/v3/app/upgrades/standard"
	"gitlab.com/thorchain/thornode/v3/x/thorchain"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

const appCovChainID = "thorchain-cov"

func appCovOptions(home string) simtestutil.AppOptionsMap {
	return simtestutil.AppOptionsMap{
		flags.FlagHome:     home,
		TestApp:            true,
		"ebifrost.address": "127.0.0.1:0", // never bind the default port
	}
}

func newAppCov(t *testing.T, db dbm.DB, home string) *THORChainApp {
	t.Helper()
	if regtestBuildCov {
		// The regtest build drives blocks itself (app_regtest.go): it seals the
		// BaseApp in NewChainApp and blocks Commit, so these tests don't apply.
		t.Skip("app construction tests do not apply to the regtest build")
	}
	return NewChainApp(
		log.NewNopLogger(), db, nil, true, appCovOptions(home),
		[]wasmkeeper.Option{}, baseapp.SetChainID(appCovChainID),
	)
}

// appCovGenesis returns the default genesis with one active thorchain node so
// the chain starts with a non-empty validator set.
func appCovGenesis(t *testing.T, a *THORChainApp) []byte {
	t.Helper()
	genesis := a.DefaultGenesis()
	var tcGen thorchain.GenesisState
	require.NoError(t, a.AppCodec().UnmarshalJSON(genesis[thorchain.ModuleName], &tcGen))
	tcGen.NodeAccounts = append(tcGen.NodeAccounts, types.GetRandomValidatorNode(types.NodeStatus_Active))
	// InitGenesis decodes the thorchain section with the amino codec
	bz, err := thorchain.ModuleCdc.MarshalJSON(&tcGen)
	require.NoError(t, err)
	genesis[thorchain.ModuleName] = bz
	state, err := json.Marshal(genesis)
	require.NoError(t, err)
	return state
}

// startAppCov creates an app, runs InitChain and commits one block.
func startAppCov(t *testing.T) (*THORChainApp, dbm.DB, string) {
	t.Helper()
	db := dbm.NewMemDB()
	home := t.TempDir()
	a := newAppCov(t, db, home)

	res, err := a.InitChain(&abci.RequestInitChain{
		ChainId:         appCovChainID,
		Time:            time.Unix(1_700_000_000, 0).UTC(),
		ConsensusParams: simtestutil.DefaultConsensusParams,
		AppStateBytes:   appCovGenesis(t, a),
		InitialHeight:   1,
	})
	require.NoError(t, err)
	require.Len(t, res.Validators, 1)

	_, err = a.FinalizeBlock(&abci.RequestFinalizeBlock{
		Height: 1,
		Time:   time.Unix(1_700_000_005, 0).UTC(),
	})
	require.NoError(t, err)
	_, err = a.Commit()
	require.NoError(t, err)
	require.Equal(t, int64(1), a.LastBlockHeight())
	return a, db, home
}

func TestAppLifecycleAndExportCov(t *testing.T) {
	a, db, _ := startAppCov(t)

	// export at the next height
	exported, err := a.ExportAppStateAndValidators(false, nil, nil)
	require.NoError(t, err)
	require.Equal(t, int64(2), exported.Height)
	var state map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(exported.AppState, &state))
	require.Contains(t, state, thorchain.ModuleName)
	require.NotNil(t, exported.ConsensusParams.Block)

	// export a subset of modules
	exported, err = a.ExportAppStateAndValidators(false, nil, []string{thorchain.ModuleName})
	require.NoError(t, err)
	state = nil
	require.NoError(t, json.Unmarshal(exported.AppState, &state))
	require.Len(t, state, 1)
	require.Contains(t, state, thorchain.ModuleName)

	// seed two staking validators so zero height prep has something to reset
	sctx := a.NewUncachedContext(false, cmtproto.Header{Height: a.LastBlockHeight(), ChainID: appCovChainID})
	allowed := sdk.ValAddress(bytes.Repeat([]byte{1}, 20))
	other := sdk.ValAddress(bytes.Repeat([]byte{2}, 20))
	for _, va := range []sdk.ValAddress{allowed, other} {
		v, verr := stakingtypes.NewValidator(va.String(), ed25519.GenPrivKey().PubKey(), stakingtypes.Description{})
		require.NoError(t, verr)
		v.UnbondingHeight = 42
		require.NoError(t, a.StakingKeeper.SetValidator(sctx, v))
	}

	// export writes into the check state, read back from there
	cctx := a.NewContextLegacy(true, cmtproto.Header{Height: a.LastBlockHeight()})

	// zero height export without an allow list resets unbonding heights only
	exported, err = a.ExportAppStateAndValidators(true, nil, nil)
	require.NoError(t, err)
	require.Equal(t, int64(0), exported.Height)
	for _, va := range []sdk.ValAddress{allowed, other} {
		v, verr := a.StakingKeeper.GetValidator(cctx, va)
		require.NoError(t, verr)
		require.Equal(t, int64(0), v.UnbondingHeight)
		require.False(t, v.Jailed)
	}

	// with an allow list every other validator is jailed
	exported, err = a.ExportAppStateAndValidators(true, []string{allowed.String()}, nil)
	require.NoError(t, err)
	require.Equal(t, int64(0), exported.Height)
	v, err := a.StakingKeeper.GetValidator(cctx, allowed)
	require.NoError(t, err)
	require.False(t, v.Jailed)
	v, err = a.StakingKeeper.GetValidator(cctx, other)
	require.NoError(t, err)
	require.True(t, v.Jailed)

	// a fresh app on the same db can reload the committed height
	// (separate home: the wasm VM holds an exclusive lock on its directory)
	newUnloaded := func() *THORChainApp {
		return NewChainApp(log.NewNopLogger(), db, nil, false, appCovOptions(t.TempDir()),
			[]wasmkeeper.Option{}, baseapp.SetChainID(appCovChainID))
	}
	b := newUnloaded()
	require.NoError(t, b.LoadHeight(1))
	require.Equal(t, int64(1), b.LastBlockHeight())
	require.Error(t, newUnloaded().LoadHeight(5))
}

func TestAppAccessorsCov(t *testing.T) {
	a := newAppCov(t, dbm.NewMemDB(), t.TempDir())

	require.Equal(t, a.BaseApp.Name(), a.Name())
	require.NotNil(t, a.LegacyAmino())
	require.NotNil(t, a.AppCodec())
	require.NotNil(t, a.InterfaceRegistry())
	require.NotNil(t, a.TxConfig())
	require.NotNil(t, a.Configurator())
	require.NotNil(t, a.SimulationManager())

	genesis := a.DefaultGenesis()
	require.Contains(t, genesis, thorchain.ModuleName)
	require.Contains(t, genesis, "auth")

	key := a.GetKey(stakingtypes.StoreKey)
	require.NotNil(t, key)
	require.Equal(t, stakingtypes.StoreKey, key.Name())
	require.Nil(t, a.GetKey("does-not-exist"))
	require.Nil(t, a.GetTKey("does-not-exist"))

	keys := a.GetStoreKeys()
	require.NotEmpty(t, keys)
	for i := 1; i < len(keys); i++ {
		require.Less(t, keys[i-1].Name(), keys[i].Name(), "store keys must be sorted")
	}

	opts := a.AutoCliOpts()
	require.Contains(t, opts.Modules, thorchain.ModuleName)
	require.NotNil(t, opts.AddressCodec)
	require.NotNil(t, opts.ValidatorAddressCodec)
	require.NotNil(t, opts.ConsensusAddressCodec)

	enc := makeEncodingConfig(a)
	require.Equal(t, a.AppCodec(), enc.Codec)
	require.Equal(t, a.TxConfig(), enc.TxConfig)
	require.Equal(t, a.InterfaceRegistry(), enc.InterfaceRegistry)
	require.Equal(t, a.LegacyAmino(), enc.Amino)

	// cached encoding config is built once and shared
	cached := MakeEncodingConfig()
	require.NotNil(t, cached.Codec)
	again := MakeEncodingConfig()
	require.Equal(t, cached.Codec, again.Codec)

	// no-op hooks
	setPrepareCheckStater(a.BaseApp)
	NewCustomWasmModule(nil).RegisterServices(nil)
}

func TestAppServicesCov(t *testing.T) {
	// The enshrined bifrost is disabled here: Start serves in a goroutine that
	// panics if Close stops the server before Serve begins, so starting and
	// immediately closing it in a test is racy.
	opts := appCovOptions(t.TempDir())
	opts["ebifrost.enable"] = false
	a := NewChainApp(log.NewNopLogger(), dbm.NewMemDB(), nil, true, opts,
		[]wasmkeeper.Option{}, baseapp.SetChainID(appCovChainID))
	require.Nil(t, a.EnshrinedBifrost)
	clientCtx := client.Context{}.
		WithCodec(a.AppCodec()).
		WithInterfaceRegistry(a.InterfaceRegistry()).
		WithTxConfig(a.TxConfig())

	a.RegisterTxService(clientCtx)
	a.RegisterTendermintService(clientCtx)
	a.RegisterNodeService(clientCtx, serverconfig.Config{})
	require.NotNil(t, a.GRPCQueryRouter().Route("/cosmos.tx.v1beta1.Service/GetTx"))
	require.NotNil(t, a.GRPCQueryRouter().Route("/cosmos.base.tendermint.v1beta1.Service/GetNodeInfo"))
	require.NotNil(t, a.GRPCQueryRouter().Route("/cosmos.base.node.v1beta1.Service/Config"))

	apiSvr := api.New(clientCtx, log.NewNopLogger(), nil)
	a.RegisterAPIRoutes(apiSvr, serverconfig.APIConfig{Swagger: false})
	var paths []string
	require.NoError(t, apiSvr.Router.Walk(func(route *mux.Route, _ *mux.Router, _ []*mux.Route) error {
		if p, err := route.GetPathTemplate(); err == nil {
			paths = append(paths, p)
		}
		return nil
	}))
	require.Contains(t, paths, "/thorchain/ping")

	require.NoError(t, a.Close())
}

// upgradeInfoHomeCov returns a new home dir holding an upgrade-info file.
func upgradeInfoHomeCov(t *testing.T, name string, height int64) string {
	t.Helper()
	home := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(home, "data"), 0o755))
	info, err := json.Marshal(upgradetypes.Plan{Name: name, Height: height})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(home, "data", upgradetypes.UpgradeInfoFilename), info, 0o600))
	return home
}

func TestRegisterUpgradeHandlersCov(t *testing.T) {
	// with no upgrades configured, one for the current version is registered
	saved := Upgrades
	Upgrades = nil
	defer func() { Upgrades = saved }()

	a := newAppCov(t, dbm.NewMemDB(), t.TempDir())
	require.Len(t, Upgrades, 1)
	require.Equal(t, a.Version(), Upgrades[0].UpgradeName)
	require.True(t, a.UpgradeKeeper.HasHandler(a.Version()))

	// an upgrade-info file on disk for a known upgrade selects its store loader
	Upgrades = []upgrades.Upgrade{standard.NewUpgrade("9.9.9")}
	b := newAppCov(t, dbm.NewMemDB(), upgradeInfoHomeCov(t, "9.9.9", 100))
	require.True(t, b.UpgradeKeeper.HasHandler("9.9.9"))
	require.Equal(t, int64(0), b.LastBlockHeight())

	// a skipped upgrade height returns early (each app gets its own home: the
	// wasm VM holds an exclusive lock on its directory)
	opts := appCovOptions(upgradeInfoHomeCov(t, "9.9.9", 100))
	opts["unsafe-skip-upgrades"] = []int{100}
	c := NewChainApp(log.NewNopLogger(), dbm.NewMemDB(), nil, true, opts, []wasmkeeper.Option{})
	require.True(t, c.UpgradeKeeper.IsSkipHeight(100))

	// a corrupt upgrade-info file panics
	bad := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(bad, "data"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(bad, "data", upgradetypes.UpgradeInfoFilename), []byte("{"), 0o600))
	require.Panics(t, func() { newAppCov(t, dbm.NewMemDB(), bad) })
}

func TestWasmAcceptedQueriesCov(t *testing.T) {
	require.Len(t, wasmAcceptedQueries, 10)
	for path, factory := range wasmAcceptedQueries {
		msg := factory()
		require.NotNil(t, msg, path)
		// the response type name matches the query method name
		method := path[len("/types.Query/"):]
		require.Contains(t, fmt.Sprintf("%T", msg), "Query"+method+"Response", path)
	}
}
