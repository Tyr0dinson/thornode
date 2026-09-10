package app

import (
	"fmt"
	"testing"

	"cosmossdk.io/log/v2"
	wasmkeeper "github.com/CosmWasm/wasmd/x/wasm/keeper"
	wasmv2 "github.com/CosmWasm/wasmd/x/wasm/migrations/v2"
	wasmv3 "github.com/CosmWasm/wasmd/x/wasm/migrations/v3"
	wasmtypes "github.com/CosmWasm/wasmd/x/wasm/types"
	wasmvm "github.com/CosmWasm/wasmvm/v3"
	abci "github.com/cometbft/cometbft/abci/types"
	tmproto "github.com/cometbft/cometbft/proto/tendermint/types"
	dbm "github.com/cosmos/cosmos-db"
	"github.com/cosmos/cosmos-sdk/baseapp"
	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	storetypes "github.com/cosmos/cosmos-sdk/store/v2/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/module"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	"github.com/stretchr/testify/require"
)

// The embedded nil Configurator makes any service registration fail: THORNode
// must register migrations without exposing upstream wasm message/query routes.
type wasmMigrationRecorder struct {
	module.Configurator
	versions []uint64
}

func (c *wasmMigrationRecorder) RegisterMigration(name string, version uint64, handler module.MigrationHandler) error {
	if name != wasmtypes.ModuleName || handler == nil {
		panic("invalid wasm migration")
	}
	c.versions = append(c.versions, version)
	return nil
}

func TestCustomWasmRegistersOnlyMigrations(t *testing.T) {
	cfg := &wasmMigrationRecorder{}
	NewCustomWasmModule(nil, wasmkeeper.Migrator{}).RegisterServices(cfg)
	require.Equal(t, []uint64{1, 2, 3}, cfg.versions)
}

func TestSDKUpgradeBranchPreservesFinalizeState(t *testing.T) {
	logger := log.NewTestLogger(t)
	app := baseapp.NewBaseApp("upgrade-context", logger, dbm.NewMemDB(), nil)
	key := storetypes.NewKVStoreKey("test")
	app.MountKVStores(map[string]*storetypes.KVStoreKey{"test": key})
	require.NoError(t, app.LoadLatestVersion())

	finalize := app.NewNextBlockContext(tmproto.Header{})
	finalize.KVStore(key).Set([]byte("in-flight"), []byte("preserved"))

	// This is the legacy consensus-parameter migration's branch lifecycle.
	branch := app.CommitMultiStore().CacheMultiStore()
	ctx := sdk.NewContext(branch, tmproto.Header{}, false, logger)
	ctx.KVStore(key).Set([]byte("migrated"), []byte("value"))
	branch.Write()

	require.Equal(t, []byte("preserved"), app.NewContext(false).KVStore(key).Get([]byte("in-flight")))
	require.Equal(t, []byte("value"), app.NewContext(false).KVStore(key).Get([]byte("migrated")))
	_, err := app.FinalizeBlock(&abci.RequestFinalizeBlock{Height: 1})
	require.NoError(t, err)
	_, err = app.Commit()
	require.NoError(t, err)
	require.Equal(t, []byte("preserved"), app.NewContext(true).KVStore(key).Get([]byte("in-flight")))
	require.Equal(t, []byte("value"), app.NewContext(true).KVStore(key).Get([]byte("migrated")))
}

func TestWasmPinningGasRemainsUnchanged(t *testing.T) {
	require.Zero(t, WasmGasRegister.PinCodeCost())
	require.Zero(t, WasmGasRegister.UnpinCodeCost())
}

func TestWasmNativeLibraryVersion(t *testing.T) {
	version, err := wasmvm.LibwasmvmVersion()
	require.NoError(t, err)
	require.Equal(t, "3.0.7", version)
}

//nolint:staticcheck // Seed historical Wasm permissions and auth storage to verify their migration.
func TestRegisteredWasmMigrationPreservesCodeIdentity(t *testing.T) {
	for _, fromVersion := range []uint64{1, 2, 3} {
		t.Run(fmt.Sprint(fromVersion), func(t *testing.T) {
			app := NewChainApp(log.NewTestLogger(t), dbm.NewMemDB(), nil, true, &appOptionsWrapper{homeDir: t.TempDir()}, nil)
			ctx := app.NewNextBlockContext(tmproto.Header{})
			creator := sdk.AccAddress(secp256k1.GenPrivKeyFromSecret([]byte("wasm-migration-creator")).PubKey().Address()).String()
			access := wasmv3.AccessConfig{Permission: wasmv3.AccessTypeOnlyAddress, Address: creator}
			params := wasmv3.Params{CodeUploadAccess: access, InstantiateDefaultPermission: wasmv3.AccessTypeOnlyAddress}
			code := wasmv3.CodeInfo{CodeHash: []byte("existing-checksum"), Creator: creator, InstantiateConfig: access}
			store := ctx.KVStore(app.GetKey(wasmtypes.StoreKey))
			if fromVersion < 3 {
				legacyParams := wasmv2.Params{
					CodeUploadAccess:             wasmv2.AccessConfig{Permission: wasmv2.AccessTypeOnlyAddress, Address: creator},
					InstantiateDefaultPermission: wasmv2.AccessTypeOnlyAddress,
				}
				app.GetSubspace(wasmtypes.ModuleName).SetParamSet(ctx, &legacyParams)
			} else {
				store.Set(wasmtypes.ParamsKey, app.appCodec.MustMarshal(&params))
			}
			store.Set(wasmtypes.GetCodeKey(7), app.appCodec.MustMarshal(&code))

			accountKey := secp256k1.GenPrivKeyFromSecret([]byte("existing-account"))
			account := authtypes.NewBaseAccount(sdk.AccAddress(accountKey.PubKey().Address()), accountKey.PubKey(), 123, 456)
			app.AccountKeeper.SetAccount(ctx, account)
			require.NoError(t, app.AccountKeeper.AccountNumber.Set(ctx, 999))

			versions := app.ModuleManager.GetVersionMap()
			versions[authtypes.ModuleName] = 5
			versions[wasmtypes.ModuleName] = fromVersion
			updated, err := app.ModuleManager.RunMigrations(ctx, app.configurator, versions)
			require.NoError(t, err)
			require.Equal(t, uint64(4), updated[wasmtypes.ModuleName])
			require.Equal(t, uint64(6), updated[authtypes.ModuleName])
			migratedAccount := app.AccountKeeper.GetAccount(ctx, account.GetAddress())
			require.Equal(t, account.GetAccountNumber(), migratedAccount.GetAccountNumber())
			require.Equal(t, account.GetSequence(), migratedAccount.GetSequence())
			require.True(t, account.GetPubKey().Equals(migratedAccount.GetPubKey()))
			require.False(t, ctx.KVStore(app.GetKey(authtypes.StoreKey)).Has(authtypes.GlobalAccountNumberKey.Bytes()))
			got := app.WasmKeeper.GetCodeInfo(ctx, 7)
			require.NotNil(t, got)
			require.Equal(t, code.CodeHash, got.CodeHash)
			require.Equal(t, creator, got.Creator)
			require.Equal(t, wasmtypes.AccessTypeAnyOfAddresses, got.InstantiateConfig.Permission)
			require.Equal(t, []string{creator}, got.InstantiateConfig.Addresses)
			require.Equal(t, []string{creator}, app.WasmKeeper.GetParams(ctx).CodeUploadAccess.Addresses)
		})
	}
}
