package app

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"

	"github.com/cosmos/cosmos-sdk/baseapp"
	"github.com/cosmos/cosmos-sdk/codec/types"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	banktypes "github.com/cosmos/cosmos-sdk/x/bank/types"
	"github.com/cosmos/cosmos-sdk/x/genutil"
	"github.com/cosmos/cosmos-sdk/x/mint"
	"github.com/cosmos/cosmos-sdk/x/supply"
	tmjson "github.com/tendermint/tendermint/libs/json"
	"honnef.co/go/tools/version"

	abci "github.com/tendermint/tendermint/abci/types"
	"github.com/tendermint/tendermint/libs/log"
	tmos "github.com/tendermint/tendermint/libs/os"
	tmtypes "github.com/tendermint/tendermint/types"
	dbm "github.com/tendermint/tm-db"

	bam "github.com/cosmos/cosmos-sdk/baseapp"
	"github.com/cosmos/cosmos-sdk/codec"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/module"
	"github.com/cosmos/cosmos-sdk/x/auth"
	"github.com/cosmos/cosmos-sdk/x/bank"
	"github.com/cosmos/cosmos-sdk/x/params"

	authkeeper "github.com/cosmos/cosmos-sdk/x/auth/keeper"
	bankkeeper "github.com/cosmos/cosmos-sdk/x/bank/keeper"
	distrtypes "github.com/cosmos/cosmos-sdk/x/distribution/types"
	mintkeeper "github.com/cosmos/cosmos-sdk/x/mint/keeper"
	minttypes "github.com/cosmos/cosmos-sdk/x/mint/types"
	paramskeeper "github.com/cosmos/cosmos-sdk/x/params/keeper"
	paramstypes "github.com/cosmos/cosmos-sdk/x/params/types"
	"gitlab.com/thorchain/thornode/x/thorchain"
	"gitlab.com/thorchain/thornode/x/thorchain/keeper"
)

const appPoolData = "thorchain"

var (
	// default home directories for the application CLI
	DefaultCLIHome string

	// DefaultNodeHome sets the folder where the applcation data and configuration will be stored
	DefaultNodeHome string

	// ModuleBasicManager is in charge of setting up basic module elemnets
	ModuleBasics = module.NewBasicManager(
		genutil.AppModuleBasic{},
		auth.AppModuleBasic{},
		bank.AppModuleBasic{},
		params.AppModuleBasic{},
		thorchain.AppModule{},
	)

	// account permissions
	maccPerms = map[string][]string{
		authtypes.FeeCollectorName: nil,
		thorchain.ModuleName:       {authtypes.Minter, authtypes.Burner},
		thorchain.ReserveName:      {},
		thorchain.BondName:         {authtypes.Staking},
		thorchain.AsgardName:       {},
	}
	// module accounts that are allowed to receive tokens
	allowedReceivingModAcc = map[string]bool{
		distrtypes.ModuleName: true,
	}
)

func init() {
	userHomeDir, err := os.UserHomeDir()
	if err != nil {
		panic(err)
	}

	DefaultNodeHome = filepath.Join(userHomeDir, ".thord")
	DefaultCLIHome = filepath.Join(userHomeDir, ".thorcli")
}

// MakeCodec generates the necessary codecs for Amino
func MakeCodecs() (codec.Marshaler, *codec.LegacyAmino) {
	config := MakeEncodingConfig()
	return config.Marshaler, config.Amino
}

type thorChainApp struct {
	*bam.BaseApp
	legacyAmino       *codec.LegacyAmino
	appCodec          codec.Marshaler
	interfaceRegistry types.InterfaceRegistry

	invCheckPeriod uint

	keys  map[string]*sdk.KVStoreKey
	tkeys map[string]*sdk.TransientStoreKey
	// Keepers
	accountKeeper   authkeeper.AccountKeeper
	bankKeeper      bankkeeper.Keeper
	supplyKeeper    mintkeeper.Keeper
	paramsKeeper    paramskeeper.Keeper
	thorchainKeeper keeper.Keeper

	// Module Manager
	mm *module.Manager
}

// NewThorchainApp is a constructor function for thorChainApp
func NewThorchainApp(logger log.Logger, db dbm.DB, traceStore io.Writer, loadLatest bool, skipUpgradeHeights map[int64]bool,
	homePath string, invCheckPeriod uint, encodingConfig EncodingConfig, baseAppOptions ...func(*baseapp.BaseApp)) *thorChainApp {
	// First define the top level codec that will be shared by the different modules
	appCodec := encodingConfig.Marshaler
	legacyAmino := encodingConfig.Amino
	interfaceRegistry := encodingConfig.InterfaceRegistry
	// BaseApp handles interactions with Tendermint through the ABCI protocol
	bApp := bam.NewBaseApp(appPoolData, logger, db, encodingConfig.TxConfig.TxDecoder(), baseAppOptions...)
	bApp.SetCommitMultiStoreTracer(traceStore)
	bApp.SetAppVersion(version.Version)
	bApp.SetInterfaceRegistry(interfaceRegistry)
	bApp.GRPCQueryRouter().RegisterSimulateService(bApp.Simulate, interfaceRegistry)

	keys := sdk.NewKVStoreKeys(authtypes.StoreKey, minttypes.StoreKey, thorchain.StoreKey, paramstypes.StoreKey)
	tkeys := sdk.NewTransientStoreKeys(paramstypes.TStoreKey)

	// Here you initialize your application with the store keys it requires
	app := &thorChainApp{
		BaseApp:           bApp,
		legacyAmino:       legacyAmino,
		appCodec:          appCodec,
		interfaceRegistry: interfaceRegistry,
		invCheckPeriod:    invCheckPeriod,
		keys:              keys,
		tkeys:             tkeys,
	}

	// The ParamsKeeper handles parameter storage for the application
	app.paramsKeeper = initParamsKeeper(appCodec, legacyAmino, keys[paramstypes.StoreKey], tkeys[paramstypes.TStoreKey])

	// set the BaseApp's parameter store
	bApp.SetParamStore(app.paramsKeeper.Subspace(baseapp.Paramspace).WithKeyTable(paramskeeper.ConsensusParamsKeyTable()))
	// Set specific supspaces
	authSubspace, _ := app.paramsKeeper.GetSubspace(authtypes.ModuleName)
	bankSupspace, _ := app.paramsKeeper.GetSubspace(banktypes.ModuleName)
	mintSubspace, _ := app.paramsKeeper.GetSubspace(minttypes.ModuleName)
	// add keepers
	app.accountKeeper = authkeeper.NewAccountKeeper(
		appCodec, keys[authtypes.StoreKey], authSubspace, authtypes.ProtoBaseAccount, maccPerms,
	)
	app.bankKeeper = bankkeeper.NewBaseKeeper(
		appCodec, keys[banktypes.StoreKey], app.accountKeeper, bankSupspace, app.BlockedAddrs(),
	)

	app.supplyKeeper = mintkeeper.NewKeeper(
		appCodec, keys[minttypes.StoreKey], mintSubspace, nil,
		app.accountKeeper, app.bankKeeper, authtypes.FeeCollectorName,
	)

	// thorchain keeper holds thorchain data/kvstore
	app.thorchainKeeper = keeper.NewKVStore(
		app.bankKeeper,
		app.supplyKeeper,
		keys[thorchain.StoreKey],
		app.cdc,
	)

	app.mm = module.NewManager(
		auth.NewAppModule(app.accountKeeper),
		bank.NewAppModule(app.bankKeeper, app.accountKeeper),
		mint.NewAppModule(app.supplyKeeper, app.accountKeeper),
		thorchain.NewAppModule(app.thorchainKeeper, app.bankKeeper, app.supplyKeeper),
	)

	app.mm.SetOrderBeginBlockers(thorchain.ModuleName)
	app.mm.SetOrderEndBlockers(thorchain.ModuleName)

	// Sets the order of Genesis - Order matters, genutil is to always come last
	app.mm.SetOrderInitGenesis(
		auth.ModuleName,
		bank.ModuleName,
		supply.ModuleName,
		thorchain.ModuleName,
	)

	// register all module routes and module queriers
	app.mm.RegisterRoutes(app.Router(), app.QueryRouter())

	// The initChainer handles translating the genesis.json file into initial state for the network
	app.SetInitChainer(app.InitChainer)
	app.SetBeginBlocker(app.BeginBlocker)
	app.SetEndBlocker(app.EndBlocker)

	// The AnteHandler handles signature verification and transaction pre-processing
	app.SetAnteHandler(
		auth.NewAnteHandler(
			app.accountKeeper,
			app.supplyKeeper,
			auth.DefaultSigVerificationGasConsumer,
		),
	)

	app.MountKVStores(keys)
	app.MountTransientStores(tkeys)

	err := app.LoadLatestVersion()
	if err != nil {
		tmos.Exit(err.Error())
	}

	return app
}

// GenesisState represents chain state at the start of the chain. Any initial state (account balances) are stored here.
type GenesisState map[string]json.RawMessage

func NewDefaultGenesisState() GenesisState {
	encCfg := MakeEncodingConfig()
	return ModuleBasics.DefaultGenesis(encCfg.Marshaler)

}

func (app *thorChainApp) InitChainer(ctx sdk.Context, req abci.RequestInitChain) abci.ResponseInitChain {
	var genesisState GenesisState
	if err := tmjson.Unmarshal(req.AppStateBytes, &genesisState); err != nil {
		panic(err)
	}
	return app.mm.InitGenesis(ctx, app.appCodec, genesisState)
}

func (app *thorChainApp) BeginBlocker(ctx sdk.Context, req abci.RequestBeginBlock) abci.ResponseBeginBlock {
	return app.mm.BeginBlock(ctx, req)
}

func (app *thorChainApp) EndBlocker(ctx sdk.Context, req abci.RequestEndBlock) abci.ResponseEndBlock {
	return app.mm.EndBlock(ctx, req)
}

func (app *thorChainApp) LoadHeight(height int64) error {
	return app.LoadVersion(height)
}

// BlockedAddrs returns all the app's module account addresses that are not
// allowed to receive external tokens.
func (app *thorChainApp) BlockedAddrs() map[string]bool {
	blockedAddrs := make(map[string]bool)
	for acc := range maccPerms {
		blockedAddrs[authtypes.NewModuleAddress(acc).String()] = !allowedReceivingModAcc[acc]
	}

	return blockedAddrs
}

// initParamsKeeper init params keeper and its subspaces
func initParamsKeeper(appCodec codec.BinaryMarshaler, legacyAmino *codec.LegacyAmino, key, tkey sdk.StoreKey) paramskeeper.Keeper {
	paramsKeeper := paramskeeper.NewKeeper(appCodec, legacyAmino, key, tkey)

	paramsKeeper.Subspace(authtypes.ModuleName)
	paramsKeeper.Subspace(banktypes.ModuleName)
	paramsKeeper.Subspace(minttypes.ModuleName)

	return paramsKeeper
}

// ModuleAccountAddrs returns all the app's module account addresses.
func (app *thorChainApp) ModuleAccountAddrs() map[string]bool {
	modAccAddrs := make(map[string]bool)
	for acc := range maccPerms {
		modAccAddrs[authtypes.NewModuleAddress(acc).String()] = true
	}

	return modAccAddrs
}

// _________________________________________________________

func (app *thorChainApp) ExportAppStateAndValidators(forZeroHeight bool, jailWhiteList []string,
) (appState json.RawMessage, validators []tmtypes.GenesisValidator, err error) {
	// as if they could withdraw from the start of the next block
	ctx := app.NewContext(true, abci.Header{Height: app.LastBlockHeight()})

	genState := app.mm.ExportGenesis(ctx)
	appState, err = codec.MarshalJSONIndent(app.cdc, genState)
	if err != nil {
		return nil, nil, err
	}

	// validators = staking.WriteValidators(ctx, app.stakingKeeper)
	return appState, validators, nil
}
