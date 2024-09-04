package app

import (
	"io"
	"os"
	"fmt"
	"strings"
	"time"
	_ "unsafe"

	"github.com/cosmos/cosmos-sdk/baseapp"
	"github.com/cosmos/cosmos-sdk/client"
	"github.com/cosmos/cosmos-sdk/client/grpc/tmservice"
	"github.com/cosmos/cosmos-sdk/client/rpc"
	"github.com/cosmos/cosmos-sdk/codec"
	"github.com/cosmos/cosmos-sdk/codec/types"
	"github.com/cosmos/cosmos-sdk/server/api"
	"github.com/cosmos/cosmos-sdk/server/config"
	servertypes "github.com/cosmos/cosmos-sdk/server/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/module"
	"github.com/cosmos/cosmos-sdk/version"
	"github.com/cosmos/cosmos-sdk/x/auth"
	"github.com/cosmos/cosmos-sdk/x/auth/ante"
	authrest "github.com/cosmos/cosmos-sdk/x/auth/client/rest"
	authkeeper "github.com/cosmos/cosmos-sdk/x/auth/keeper"
	authtx "github.com/cosmos/cosmos-sdk/x/auth/tx"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	"github.com/cosmos/cosmos-sdk/x/bank"
	bankkeeper "github.com/cosmos/cosmos-sdk/x/bank/keeper"
	banktypes "github.com/cosmos/cosmos-sdk/x/bank/types"
	abci "github.com/tendermint/tendermint/abci/types"
	"github.com/tendermint/tendermint/libs/log"
	tmos "github.com/tendermint/tendermint/libs/os"
	dbm "github.com/tendermint/tm-db"

	"github.com/cosmos/cosmos-sdk/x/genutil"
	genutiltypes "github.com/cosmos/cosmos-sdk/x/genutil/types"
	"github.com/cosmos/cosmos-sdk/x/mint"
	mintkeeper "github.com/cosmos/cosmos-sdk/x/mint/keeper"
	minttypes "github.com/cosmos/cosmos-sdk/x/mint/types"
	"github.com/cosmos/cosmos-sdk/x/params"
	paramskeeper "github.com/cosmos/cosmos-sdk/x/params/keeper"
	paramstypes "github.com/cosmos/cosmos-sdk/x/params/types"
	stakingkeeper "github.com/cosmos/cosmos-sdk/x/staking/keeper"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"
	appparams "gitlab.com/thorchain/thornode/app/params"
	"gitlab.com/thorchain/thornode/x/thorchain"
	thorchainkeeper "gitlab.com/thorchain/thornode/x/thorchain/keeper"
	thorchainkeeperv1 "gitlab.com/thorchain/thornode/x/thorchain/keeper/v1"
	thorchaintypes "gitlab.com/thorchain/thornode/x/thorchain/types"

	"github.com/cosmos/cosmos-sdk/telemetry"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
	"github.com/cosmos/cosmos-sdk/x/auth/legacy/legacytx"
	"github.com/gogo/protobuf/proto"
	"github.com/tendermint/tendermint/crypto/tmhash"
)

const (
	runTxModeCheck    runTxMode = iota // Check a transaction
	runTxModeReCheck                   // Recheck a (pending) transaction after a commit
	runTxModeSimulate                  // Simulate a transaction
	runTxModeDeliver                   // Deliver a transaction
)

type runTxMode uint8

var (
	// DefaultNodeHome default home directories for the application daemon
	DefaultNodeHome = func() string {
		return os.ExpandEnv("$HOME/.thornode")
	}

	// ModuleBasics defines the module BasicManager is in charge of setting up basic,
	// non-dependant module elements, such as codec registration
	// and genesis verification.
	ModuleBasics = module.NewBasicManager(
		auth.AppModuleBasic{},
		genutil.AppModuleBasic{},
		bank.AppModuleBasic{},
		mint.AppModuleBasic{},
		params.AppModuleBasic{},
		thorchain.AppModuleBasic{},
	)

	// module account permissions
	maccPerms = map[string][]string{
		authtypes.FeeCollectorName:       nil,
		minttypes.ModuleName:             {authtypes.Minter},
		stakingtypes.BondedPoolName:      {authtypes.Burner, authtypes.Staking},
		stakingtypes.NotBondedPoolName:   {authtypes.Burner, authtypes.Staking},
		thorchain.ModuleName:             {authtypes.Minter, authtypes.Burner},
		thorchain.AsgardName:             {},
		thorchain.BondName:               {},
		thorchain.ReserveName:            {},
		thorchain.LendingName:            {},
		thorchain.AffiliateCollectorName: {},
		thorchain.TreasuryName:           {},
		thorchain.RUNEPoolName:           {},
	}

	// module accounts that are allowed to receive tokens
	allowedReceivingModAcc = map[string]bool{
		thorchain.LendingName:  true,
		thorchain.TreasuryName: true,
	}
)

var (
	_ App                     = (*THORChainApp)(nil)
	_ servertypes.Application = (*THORChainApp)(nil)
)

// THORChainApp extends an ABCI application
type THORChainApp struct {
	*baseapp.BaseApp
	appName           string
	cdc               *codec.LegacyAmino
	appCodec          codec.Codec
	interfaceRegistry types.InterfaceRegistry
	invCheckPeriod    uint
	// keys to access the substores
	keys    map[string]*sdk.KVStoreKey
	tkeys   map[string]*sdk.TransientStoreKey
	memKeys map[string]*sdk.MemoryStoreKey

	// keepers
	AccountKeeper authkeeper.AccountKeeper
	BankKeeper    bankkeeper.Keeper
	StakingKeeper stakingkeeper.Keeper
	MintKeeper    mintkeeper.Keeper
	ParamsKeeper  paramskeeper.Keeper

	thorchainKeeper thorchainkeeper.Keeper

	msgServiceRouter  *MsgServiceRouter    // router for redirecting Msg service messages
	anteHandler    sdk.AnteHandler  // ante handler for fee and auth
	router            sdk.Router           // handle any kind of message
	txDecoder         sdk.TxDecoder // unmarshal []byte into sdk.Tx
	runTxRecoveryMiddleware recoveryMiddleware
	// the module manager
	mm *module.Manager
}

// New returns a reference to an initialized thornode application.
func New(
	appName string, logger log.Logger, db dbm.DB, traceStore io.Writer, loadLatest bool, skipUpgradeHeights map[int64]bool,
	homePath string, invCheckPeriod uint, encodingConfig appparams.EncodingConfig, telemetryEnabled bool, baseAppOptions ...func(*baseapp.BaseApp),
) *THORChainApp {
	appCodec := encodingConfig.Marshaler
	cdc := encodingConfig.Amino
	interfaceRegistry := encodingConfig.InterfaceRegistry
	
	fmt.Println("NewBaseApp")
	txDecoder := encodingConfig.TxConfig.TxDecoder()
	bApp := baseapp.NewBaseApp(appName, logger, db, txDecoder, baseAppOptions...)
	fmt.Println("Finished NewBaseApp")
	//bApp.MsgServiceRouter()
	bApp.SetCommitMultiStoreTracer(traceStore)
	fmt.Println("Finished NewBaseApp2")
	bApp.SetVersion(version.Version)
	fmt.Println("Finished NewBaseApp3")
	bApp.SetInterfaceRegistry(interfaceRegistry)
	fmt.Println("Finished NewBaseApp4")
	// bApp.GRPCQueryRouter().RegisterSimulateService(bApp.Simulate, interfaceRegistry)

	keys := sdk.NewKVStoreKeys(
		authtypes.StoreKey, banktypes.StoreKey, stakingtypes.StoreKey,
		minttypes.StoreKey,
		paramstypes.StoreKey,
		thorchaintypes.StoreKey,
	)
	tkeys := sdk.NewTransientStoreKeys(paramstypes.TStoreKey)
	memKeys := sdk.NewMemoryStoreKeys()

	app := &THORChainApp{
		BaseApp:           bApp,
		appName:           appName,
		cdc:               cdc,
		appCodec:          appCodec,
		interfaceRegistry: interfaceRegistry,
		invCheckPeriod:    invCheckPeriod,
		keys:              keys,
		tkeys:             tkeys,
		memKeys:           memKeys,
		txDecoder:         txDecoder,
	}
	app.runTxRecoveryMiddleware = newDefaultRecoveryMiddleware()

	app.ParamsKeeper = initParamsKeeper(appCodec, cdc, keys[paramstypes.StoreKey], tkeys[paramstypes.TStoreKey])

	// set the BaseApp's parameter store
	bApp.SetParamStore(app.ParamsKeeper.Subspace(baseapp.Paramspace).WithKeyTable(paramskeeper.ConsensusParamsKeyTable()))

	// add keepers
	app.AccountKeeper = authkeeper.NewAccountKeeper(
		appCodec, keys[authtypes.StoreKey], app.GetSubspace(authtypes.ModuleName), authtypes.ProtoBaseAccount, maccPerms,
	)
	app.BankKeeper = bankkeeper.NewBaseKeeper(
		appCodec, keys[banktypes.StoreKey], app.AccountKeeper, app.GetSubspace(banktypes.ModuleName), app.BlockedAddrs(),
	)

	stakingKeeper := stakingkeeper.NewKeeper(
		appCodec, keys[stakingtypes.StoreKey], app.AccountKeeper, app.BankKeeper, app.GetSubspace(stakingtypes.ModuleName),
	)

	app.MintKeeper = mintkeeper.NewKeeper(
		appCodec, keys[minttypes.StoreKey], app.GetSubspace(minttypes.ModuleName), &stakingKeeper,
		app.AccountKeeper, app.BankKeeper, authtypes.FeeCollectorName,
	)

	app.thorchainKeeper = thorchainkeeperv1.NewKeeper(
		appCodec, app.BankKeeper, app.AccountKeeper, keys[thorchaintypes.StoreKey],
	)
	app.msgServiceRouter = NewMsgServiceRouter(app.thorchainKeeper)
	app.SetInterfaceRegistry(interfaceRegistry)

	// NOTE: Any module instantiated in the module manager that is later modified
	// must be passed by reference here.

	app.mm = module.NewManager(
		genutil.NewAppModule(
			app.AccountKeeper, app.StakingKeeper, app.BaseApp.DeliverTx,
			encodingConfig.TxConfig,
		),
		auth.NewAppModule(appCodec, app.AccountKeeper, nil),
		bank.NewAppModule(appCodec, app.BankKeeper, app.AccountKeeper),
		params.NewAppModule(app.ParamsKeeper),
		thorchain.NewAppModule(app.thorchainKeeper, appCodec, app.BankKeeper, app.AccountKeeper, keys[thorchaintypes.StoreKey], telemetryEnabled),
	)

	// NOTE: The genutils module must occur after staking so that pools are
	// properly initialized with tokens from genesis accounts.
	app.mm.SetOrderInitGenesis(
		authtypes.ModuleName,
		banktypes.ModuleName,
		paramstypes.ModuleName,
		genutiltypes.ModuleName,
		thorchain.ModuleName,
	)

	app.mm.RegisterRoutes(app.Router(), app.QueryRouter(), encodingConfig.Amino)
	app.router = app.Router()
	app.mm.RegisterServices(module.NewConfigurator(app.appCodec, app.MsgServiceRouter(), app.GRPCQueryRouter()))
	//bappMsgServiceRouter := bApp.MsgServiceRouter()
	//bappMsgServiceRouter = app.MsgServiceRouter().MsgServiceRouter

	// initialize stores
	app.MountKVStores(keys)
	app.MountTransientStores(tkeys)
	app.MountMemoryStores(memKeys)

	// initialize BaseApp
	app.SetInitChainer(app.InitChainer)
	app.SetBeginBlocker(app.BeginBlocker)

	anteHandler, err := NewAnteHandler(AnteHandlerOptions{
		AccountKeeper:   app.AccountKeeper,
		BankKeeper:      app.BankKeeper,
		SigGasConsumer:  ante.DefaultSigVerificationGasConsumer,
		SignModeHandler: encodingConfig.TxConfig.SignModeHandler(),
		THORChainKeeper: app.thorchainKeeper,
	})
	if err != nil {
		tmos.Exit(err.Error())
	}

	app.anteHandler = anteHandler
	app.SetAnteHandler(anteHandler)
	// app.SetPostHandler(postHandler) // TOOD needs v46
	app.SetEndBlocker(app.EndBlocker)

	if loadLatest {
		if err = app.LoadLatestVersion(); err != nil {
			tmos.Exit(err.Error())
		}
	}
	fmt.Println("Returning app")

	return app
}

// Name returns the name of the App
func (app *THORChainApp) Name() string { return app.BaseApp.Name() }

// InitChainer application update at chain initialization
func (app *THORChainApp) InitChainer(ctx sdk.Context, req abci.RequestInitChain) abci.ResponseInitChain {
	var genesisState GenesisState
	app.cdc.MustUnmarshalJSON(req.AppStateBytes, &genesisState)
	return app.mm.InitGenesis(ctx, app.appCodec, genesisState)
}

// LoadHeight loads a particular height
func (app *THORChainApp) LoadHeight(height int64) error {
	return app.LoadVersion(height)
}

// ModuleAccountAddrs returns all the app's module account addresses.
func (app *THORChainApp) ModuleAccountAddrs() map[string]bool {
	modAccAddrs := make(map[string]bool)
	for acc := range maccPerms {
		modAccAddrs[authtypes.NewModuleAddress(acc).String()] = true
	}

	return modAccAddrs
}

// BlockedAddrs returns all the app's module account addresses that are not
// allowed to receive external tokens.
func (app *THORChainApp) BlockedAddrs() map[string]bool {
	blockedAddrs := make(map[string]bool)
	for acc := range maccPerms {
		blockedAddrs[authtypes.NewModuleAddress(acc).String()] = !allowedReceivingModAcc[acc]
	}

	return blockedAddrs
}

// LegacyAmino returns the app's amino codec.
//
// NOTE: This is solely to be used for testing purposes as it may be desirable
// for modules to register their own custom testing types.
func (app *THORChainApp) LegacyAmino() *codec.LegacyAmino {
	return app.cdc
}

// AppCodec returns app codec.
//
// NOTE: This is solely to be used for testing purposes as it may be desirable
// for modules to register their own custom testing types.
func (app *THORChainApp) AppCodec() codec.Codec {
	return app.appCodec
}

// InterfaceRegistry returns InterfaceRegistry
func (app *THORChainApp) InterfaceRegistry() types.InterfaceRegistry {
	return app.interfaceRegistry
}

// GetKey returns the KVStoreKey for the provided store key.
//
// NOTE: This is solely to be used for testing purposes.
func (app *THORChainApp) GetKey(storeKey string) *sdk.KVStoreKey {
	return app.keys[storeKey]
}

// GetTKey returns the TransientStoreKey for the provided store key.
//
// NOTE: This is solely to be used for testing purposes.
func (app *THORChainApp) GetTKey(storeKey string) *sdk.TransientStoreKey {
	return app.tkeys[storeKey]
}

// GetMemKey returns the MemStoreKey for the provided mem key.
//
// NOTE: This is solely used for testing purposes.
func (app *THORChainApp) GetMemKey(storeKey string) *sdk.MemoryStoreKey {
	return app.memKeys[storeKey]
}

// GetSubspace returns a param subspace for a given module name.
//
// NOTE: This is solely to be used for testing purposes.
func (app *THORChainApp) GetSubspace(moduleName string) paramstypes.Subspace {
	subspace, _ := app.ParamsKeeper.GetSubspace(moduleName)
	return subspace
}

// RegisterAPIRoutes registers all application module routes with the provided
// API server.
func (app *THORChainApp) RegisterAPIRoutes(apiSvr *api.Server, apiConfig config.APIConfig) {
	clientCtx := apiSvr.ClientCtx
	rpc.RegisterRoutes(clientCtx, apiSvr.Router)
	authrest.RegisterTxRoutes(clientCtx, apiSvr.Router)

	// Register new tx routes from grpc-gateway.
	authtx.RegisterGRPCGatewayRoutes(clientCtx, apiSvr.GRPCGatewayRouter)
	// Register new tendermint queries routes from grpc-gateway.
	tmservice.RegisterGRPCGatewayRoutes(clientCtx, apiSvr.GRPCGatewayRouter)

	// Register legacy and grpc-gateway routes for all modules.
	ModuleBasics.RegisterRESTRoutes(clientCtx, apiSvr.Router)
	ModuleBasics.RegisterGRPCGatewayRoutes(clientCtx, apiSvr.GRPCGatewayRouter)
}

// RegisterTendermintService implements the Application.RegisterTendermintService method.
func (app *THORChainApp) RegisterTendermintService(ctx client.Context) {
	tmservice.RegisterTendermintService(app.BaseApp.GRPCQueryRouter(), ctx, app.interfaceRegistry)
}

// RegisterTxService implements the Application.RegisterTxService method.
func (app *THORChainApp) RegisterTxService(ctx client.Context) {
	authtx.RegisterTxService(app.BaseApp.GRPCQueryRouter(), ctx, app.BaseApp.Simulate, app.interfaceRegistry)
}

// MsgServiceRouter returns the MsgServiceRouter of a BaseApp.
func (app *THORChainApp) MsgServiceRouter() *MsgServiceRouter { return app.msgServiceRouter }

// SetInterfaceRegistry sets the InterfaceRegistry.
func (app *THORChainApp) SetInterfaceRegistry(registry types.InterfaceRegistry) {
	app.interfaceRegistry = registry
	app.msgServiceRouter.SetInterfaceRegistry(registry)
}

// initParamsKeeper init params keeper and its subspaces
func initParamsKeeper(appCodec codec.BinaryCodec, legacyAmino *codec.LegacyAmino, key, tkey sdk.StoreKey) paramskeeper.Keeper {
	paramsKeeper := paramskeeper.NewKeeper(appCodec, legacyAmino, key, tkey)

	paramsKeeper.Subspace(authtypes.ModuleName)
	paramsKeeper.Subspace(banktypes.ModuleName)
	paramsKeeper.Subspace(stakingtypes.ModuleName)
	paramsKeeper.Subspace(minttypes.ModuleName)

	return paramsKeeper
}

// DeliverTx implements the ABCI interface and executes a tx in DeliverTx mode.
// State only gets persisted if all messages are valid and get executed successfully.
// Otherwise, the ResponseDeliverTx will contain releveant error information.
// Regardless of tx execution outcome, the ResponseDeliverTx will contain relevant
// gas execution context.
func (app *THORChainApp) DeliverTx(req abci.RequestDeliverTx) abci.ResponseDeliverTx {
	defer telemetry.MeasureSince(time.Now(), "abci", "deliver_tx")

	gInfo := sdk.GasInfo{}
	resultStr := "successful"

	defer func() {
		telemetry.IncrCounter(1, "tx", "count")
		telemetry.IncrCounter(1, "tx", resultStr)
		telemetry.SetGauge(float32(gInfo.GasUsed), "tx", "gas", "used")
		telemetry.SetGauge(float32(gInfo.GasWanted), "tx", "gas", "wanted")
	}()

	gInfo, result, anteEvents, err := app.runTx(runTxModeDeliver, req.Tx)
	if err != nil {
		resultStr = "failed"
		return sdkerrors.ResponseDeliverTxWithEvents(err, gInfo.GasWanted, gInfo.GasUsed, anteEvents, app.Trace())
	}

	return abci.ResponseDeliverTx{
		GasWanted: int64(gInfo.GasWanted), // TODO: Should type accept unsigned ints?
		GasUsed:   int64(gInfo.GasUsed),   // TODO: Should type accept unsigned ints?
		Log:       result.Log,
		Data:      result.Data,
		Events:    sdk.MarkEventsToIndex(result.Events, nil),
		//Events:    sdk.MarkEventsToIndex(result.Events, app.indexEvents),
	}
}

//go:linkname validateBasicTxMsgs github.com/cosmos/cosmos-sdk/baseapp.validateBasicTxMsgs
func validateBasicTxMsgs([]sdk.Msg) error

//go:linkname getContextForTx github.com/cosmos/cosmos-sdk/baseapp.(*BaseApp).getContextForTx
func getContextForTx(*baseapp.BaseApp, runTxMode, []byte) sdk.Context

// runTx processes a transaction within a given execution mode, encoded transaction
// bytes, and the decoded transaction itself. All state transitions occur through
// a cached Context depending on the mode provided. State only gets persisted
// if all messages get executed successfully and the execution mode is DeliverTx.
// Note, gas execution info is always returned. A reference to a Result is
// returned if the tx does not run out of gas and if all the messages are valid
// and execute successfully. An error is returned otherwise.
func (app *THORChainApp) runTx(mode runTxMode, txBytes []byte) (gInfo sdk.GasInfo, result *sdk.Result, anteEvents []abci.Event, err error) {
	fmt.Println("thor runTx")
	// NOTE: GasWanted should be returned by the AnteHandler. GasUsed is
	// determined by the GasMeter. We need access to the context to get the gas
	// meter so we initialize upfront.
	var gasWanted uint64

	ctx := getContextForTx(app.BaseApp, mode, txBytes)
	ms := ctx.MultiStore()

	// only run the tx if there is block gas remaining
	if mode == runTxModeDeliver && ctx.BlockGasMeter().IsOutOfGas() {
		return gInfo, nil, nil, sdkerrors.Wrap(sdkerrors.ErrOutOfGas, "no block gas left to run tx")
	}

	defer func() {
		if r := recover(); r != nil {
			recoveryMW := newOutOfGasRecoveryMiddleware(gasWanted, ctx, app.runTxRecoveryMiddleware)
			err, result = processRecovery(r, recoveryMW), nil
		}

		gInfo = sdk.GasInfo{GasWanted: gasWanted, GasUsed: ctx.GasMeter().GasConsumed()}
	}()

	blockGasConsumed := false
	// consumeBlockGas makes sure block gas is consumed at most once. It must happen after
	// tx processing, and must be execute even if tx processing fails. Hence we use trick with `defer`
	consumeBlockGas := func() {
		if !blockGasConsumed {
			blockGasConsumed = true
			ctx.BlockGasMeter().ConsumeGas(
				ctx.GasMeter().GasConsumedToLimit(), "block gas meter",
			)
		}
	}

	// If BlockGasMeter() panics it will be caught by the above recover and will
	// return an error - in any case BlockGasMeter will consume gas past the limit.
	//
	// NOTE: This must exist in a separate defer function for the above recovery
	// to recover from this one.
	if mode == runTxModeDeliver {
		defer consumeBlockGas()
	}

	tx, err := app.txDecoder(txBytes)
	if err != nil {
		return sdk.GasInfo{}, nil, nil, err
	}

	msgs := tx.GetMsgs()
	if err := validateBasicTxMsgs(msgs); err != nil {
		return sdk.GasInfo{}, nil, nil, err
	}

	if app.anteHandler != nil {
		var (
			anteCtx sdk.Context
			msCache sdk.CacheMultiStore
		)

		// Branch context before AnteHandler call in case it aborts.
		// This is required for both CheckTx and DeliverTx.
		// Ref: https://github.com/cosmos/cosmos-sdk/issues/2772
		//
		// NOTE: Alternatively, we could require that AnteHandler ensures that
		// writes do not happen if aborted/failed.  This may have some
		// performance benefits, but it'll be more difficult to get right.
		anteCtx, msCache = app.cacheTxContext(ctx, txBytes)
		anteCtx = anteCtx.WithEventManager(sdk.NewEventManager())
		newCtx, err := app.anteHandler(anteCtx, tx, mode == runTxModeSimulate)

		if !newCtx.IsZero() {
			// At this point, newCtx.MultiStore() is a store branch, or something else
			// replaced by the AnteHandler. We want the original multistore.
			//
			// Also, in the case of the tx aborting, we need to track gas consumed via
			// the instantiated gas meter in the AnteHandler, so we update the context
			// prior to returning.
			ctx = newCtx.WithMultiStore(ms)
		}

		events := ctx.EventManager().Events()

		// GasMeter expected to be set in AnteHandler
		gasWanted = ctx.GasMeter().Limit()

		if err != nil {
			return gInfo, nil, nil, err
		}

		msCache.Write()
		anteEvents = events.ToABCIEvents()
	}

	// Create a new Context based off of the existing Context with a MultiStore branch
	// in case message processing fails. At this point, the MultiStore
	// is a branch of a branch.
	runMsgCtx, msCache := app.cacheTxContext(ctx, txBytes)

	// Attempt to execute all messages and only update state if all messages pass
	// and we're in DeliverTx. Note, runMsgs will never return a reference to a
	// Result if any single message fails or does not have a registered Handler.
	result, err = app.runMsgs(runMsgCtx, msgs, mode)
	if err == nil && mode == runTxModeDeliver {
		// When block gas exceeds, it'll panic and won't commit the cached store.
		consumeBlockGas()

		msCache.Write()

		if len(anteEvents) > 0 {
			// append the events in the order of occurrence
			result.Events = append(anteEvents, result.Events...)
		}
	}

	return gInfo, result, anteEvents, err
}

// runMsgs iterates through a list of messages and executes them with the provided
// Context and execution mode. Messages will only be executed during simulation
// and DeliverTx. An error is returned if any single message fails or if a
// Handler does not exist for a given message route. Otherwise, a reference to a
// Result is returned. The caller must not commit state if an error is returned.
func (app *THORChainApp) runMsgs(ctx sdk.Context, msgs []sdk.Msg, mode runTxMode) (*sdk.Result, error) {
	fmt.Println("thor runMsgs() start")
	msgLogs := make(sdk.ABCIMessageLogs, 0, len(msgs))
	events := sdk.EmptyEvents()
	txMsgData := &sdk.TxMsgData{
		Data: make([]*sdk.MsgData, 0, len(msgs)),
	}

	// NOTE: GasWanted is determined by the AnteHandler and GasUsed by the GasMeter.
	for i, msg := range msgs {
		// skip actual execution for (Re)CheckTx mode
		if mode == runTxModeCheck || mode == runTxModeReCheck {
			break
		}

		var (
			msgResult    *sdk.Result
			eventMsgName string // name to use as value in event `message.action`
			err          error
		)

		fmt.Println("thor runMsgs() about to look up handler", sdk.MsgTypeURL(msg))
		if handler := app.msgServiceRouter.Handler(msg); handler != nil {
			fmt.Println("runMsgs() msgServiceRouter:", sdk.MsgTypeURL(msg))
			// ADR 031 request type routing
			msgResult, err = handler(ctx, msg)
			fmt.Println("runMsgs() handler executed")
			eventMsgName = sdk.MsgTypeURL(msg)
		} else if legacyMsg, ok := msg.(legacytx.LegacyMsg); ok {
			fmt.Println("runMsgs() legacyMsg:", sdk.MsgTypeURL(msg))
			// legacy sdk.Msg routing
			// Assuming that the app developer has migrated all their Msgs to
			// proto messages and has registered all `Msg services`, then this
			// path should never be called, because all those Msgs should be
			// registered within the `msgServiceRouter` already.
			msgRoute := legacyMsg.Route()
			eventMsgName = legacyMsg.Type()
			handler := app.router.Route(ctx, msgRoute)
			if handler == nil {
				return nil, sdkerrors.Wrapf(sdkerrors.ErrUnknownRequest, "unrecognized message route: %s; message index: %d", msgRoute, i)
			}

			msgResult, err = handler(ctx, msg)
		} else {
			return nil, sdkerrors.Wrapf(sdkerrors.ErrUnknownRequest, "can't route message %+v", msg)
		}

		if err != nil {
			return nil, sdkerrors.Wrapf(err, "failed to execute message; message index: %d", i)
		}

		msgEvents := sdk.Events{
			sdk.NewEvent(sdk.EventTypeMessage, sdk.NewAttribute(sdk.AttributeKeyAction, eventMsgName)),
		}
		msgEvents = msgEvents.AppendEvents(msgResult.GetEvents())

		// append message events, data and logs
		//
		// Note: Each message result's data must be length-prefixed in order to
		// separate each result.
		events = events.AppendEvents(msgEvents)

		txMsgData.Data = append(txMsgData.Data, &sdk.MsgData{MsgType: sdk.MsgTypeURL(msg), Data: msgResult.Data})
		msgLogs = append(msgLogs, sdk.NewABCIMessageLog(uint32(i), msgResult.Log, msgEvents))
	}

	data, err := proto.Marshal(txMsgData)
	if err != nil {
		return nil, sdkerrors.Wrap(err, "failed to marshal tx data")
	}

	return &sdk.Result{
		Data:   data,
		Log:    strings.TrimSpace(msgLogs.String()),
		Events: events.ToABCIEvents(),
	}, nil
}

// cacheTxContext returns a new context based off of the provided context with
// a branched multi-store.
func (app *THORChainApp) cacheTxContext(ctx sdk.Context, txBytes []byte) (sdk.Context, sdk.CacheMultiStore) {
	ms := ctx.MultiStore()
	// TODO: https://github.com/cosmos/cosmos-sdk/issues/2824
	msCache := ms.CacheMultiStore()
	if msCache.TracingEnabled() {
		msCache = msCache.SetTracingContext(
			sdk.TraceContext(
				map[string]interface{}{
					"txHash": fmt.Sprintf("%X", tmhash.Sum(txBytes)),
				},
			),
		).(sdk.CacheMultiStore)
	}

	return ctx.WithMultiStore(msCache), msCache
}

// // retrieve the context for the tx w/ txBytes and other memoized values.
// func (app *THORChainApp) getContextForTx(mode runTxMode, txBytes []byte) sdk.Context {
// 	ctx := app.getState(mode).ctx.
// 		WithTxBytes(txBytes).
// 		WithVoteInfos(app.voteInfos)

// 	ctx = ctx.WithConsensusParams(app.GetConsensusParams(ctx))

// 	if mode == runTxModeReCheck {
// 		ctx = ctx.WithIsReCheckTx(true)
// 	}

// 	if mode == runTxModeSimulate {
// 		ctx, _ = ctx.CacheContext()
// 	}

// 	return ctx
// }

// // Returns the applications's deliverState if app is in runTxModeDeliver,
// // otherwise it returns the application's checkstate.
// func (app *BaseApp) getState(mode runTxMode) *state {
// 	if mode == runTxModeDeliver {
// 		return app.deliverState
// 	}

// 	return app.checkState
// }