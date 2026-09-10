//go:build mocknet

package app

import (
	"archive/zip"
	"math/rand"
	"os"
	"path/filepath"
	"testing"
	"time"

	"cosmossdk.io/log/v2"
	"cosmossdk.io/math"
	wasmkeeper "github.com/CosmWasm/wasmd/x/wasm/keeper"
	wasmtypes "github.com/CosmWasm/wasmd/x/wasm/types"
	wasmvm "github.com/CosmWasm/wasmvm/v3"
	abci "github.com/cometbft/cometbft/abci/types"
	dbm "github.com/cosmos/cosmos-db"
	"github.com/cosmos/cosmos-sdk/baseapp"
	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	simtestutil "github.com/cosmos/cosmos-sdk/testutil/sims"
	sdk "github.com/cosmos/cosmos-sdk/types"
	auth "github.com/cosmos/cosmos-sdk/x/auth/types"
	bank "github.com/cosmos/cosmos-sdk/x/bank/types"
	"github.com/stretchr/testify/require"
)

// Open a Store v1 database committed by the previous application; never call
// InitChain or RunMigrations here, or replace its version map or upgrade plan.
func TestSDKScheduledUpgradeFromOldDatabase(t *testing.T) {
	setupSDKReplay(t)
	home := t.TempDir()
	fixture, err := zip.OpenReader("testdata/sdk-v053/home.zip")
	require.NoError(t, err)
	require.NoError(t, os.CopyFS(home, fixture))
	require.NoError(t, fixture.Close())
	var engine *wasmvm.VM
	newApp := func() *THORChainApp {
		db, err := dbm.NewGoLevelDB("application", filepath.Join(home, "data"), nil)
		require.NoError(t, err)
		capture := wasmkeeper.WithWasmEngineDecorator(func(vm wasmtypes.WasmEngine) wasmtypes.WasmEngine { engine = vm.(*wasmvm.VM); return vm })
		return NewChainApp(log.NewNopLogger(), db, nil, true, &appOptionsWrapper{homeDir: home}, []wasmkeeper.Option{capture}, baseapp.SetChainID("thorchain"))
	}
	app := newApp()
	t.Cleanup(func() { require.NoError(t, app.Close()); engine.Cleanup() })
	require.Equal(t, int64(7), app.LastBlockHeight())
	ctx := app.NewContext(true)
	plan, err := app.UpgradeKeeper.GetUpgradePlan(ctx)
	require.NoError(t, err)
	require.Equal(t, "4.0.0", plan.Name)
	require.Equal(t, int64(8), plan.Height)
	versions, err := app.UpgradeKeeper.GetModuleVersionMap(ctx)
	require.NoError(t, err)
	require.Equal(t, uint64(5), versions[auth.ModuleName])
	require.Equal(t, uint64(4), versions[wasmtypes.ModuleName])
	nodes, err := app.ThorchainKeeper.ListActiveValidators(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, nodes)
	for _, node := range nodes {
		require.Equal(t, "3.20.0", node.Version)
	}
	key := secp256k1.GenPrivKeyFromSecret([]byte("sdk-upgrade-existing-account"))
	address := sdk.AccAddress(key.PubKey().Address())
	account := app.AccountKeeper.GetAccount(ctx, address)
	require.NotNil(t, account)
	require.Equal(t, uint64(123), account.GetAccountNumber())
	require.Equal(t, uint64(456), account.GetSequence())
	contract := sdk.MustAccAddressFromBech32("tthor14hj2tavq8fpesdwxxcu44rty3hh90vhujrvcmstl4zr3txmfvw9sw58u9f")
	code := app.WasmKeeper.GetCodeInfo(ctx, 1)
	info := app.WasmKeeper.GetContractInfo(ctx, contract)
	require.NotNil(t, code)
	require.NotNil(t, info)
	require.Equal(t, []byte(`"1"`), app.WasmKeeper.QueryRaw(ctx, contract, []byte("counter")))
	// auth 5 stores this counter; auth 6 must remove it through the handler.
	//nolint:staticcheck // Verify the persisted legacy key is actually migrated.
	require.True(t, ctx.KVStore(app.GetKey(auth.StoreKey)).Has(auth.GlobalAccountNumberKey.Bytes()))

	assertUpgraded := func(sequence uint64) {
		ctx := app.NewContext(true)
		versions, err := app.UpgradeKeeper.GetModuleVersionMap(ctx)
		require.NoError(t, err)
		require.Equal(t, app.ModuleManager.GetVersionMap(), versions)
		done, err := app.UpgradeKeeper.GetDoneHeight(ctx, "4.0.0")
		require.NoError(t, err)
		require.Equal(t, int64(8), done)
		_, err = app.UpgradeKeeper.GetUpgradePlan(ctx)
		require.Error(t, err)
		nodes, err := app.ThorchainKeeper.ListActiveValidators(ctx)
		require.NoError(t, err)
		require.NotEmpty(t, nodes)
		for _, node := range nodes {
			require.Equal(t, "4.0.0", node.Version)
		}
		got := app.AccountKeeper.GetAccount(ctx, address)
		require.Equal(t, account.GetAccountNumber(), got.GetAccountNumber())
		require.Equal(t, sequence, got.GetSequence())
		require.True(t, account.GetPubKey().Equals(got.GetPubKey()))
		//nolint:staticcheck // The legacy counter must stay deleted after restart.
		require.False(t, ctx.KVStore(app.GetKey(auth.StoreKey)).Has(auth.GlobalAccountNumberKey.Bytes()))
		require.Equal(t, code, app.WasmKeeper.GetCodeInfo(ctx, 1))
		require.Equal(t, info, app.WasmKeeper.GetContractInfo(ctx, contract))
	}
	recipient := sdk.AccAddress(secp256k1.GenPrivKeyFromSecret([]byte("sdk-upgrade-recipient")).PubKey().Address())
	send := &bank.MsgSend{FromAddress: address.String(), ToAddress: recipient.String(), Amount: sdk.NewCoins(sdk.NewCoin("rune", math.NewInt(100000000)))}
	execute := &wasmtypes.MsgExecuteContract{Sender: address.String(), Contract: contract.String(), Msg: []byte(`{"increment":{}}`)}
	for i, msg := range []sdk.Msg{send, execute} {
		tx, err := simtestutil.GenSignedMockTx(rand.New(rand.NewSource(1)), app.TxConfig(), []sdk.Msg{msg}, sdk.NewCoins(sdk.NewCoin("rune", math.NewInt(2000000))), 2000000, "thorchain", []uint64{123}, []uint64{456 + uint64(i)}, key)
		require.NoError(t, err)
		bz, err := app.TxConfig().TxEncoder()(tx)
		require.NoError(t, err)
		result, err := app.FinalizeBlock(&abci.RequestFinalizeBlock{Height: 8 + int64(i), Time: time.Date(2026, 1, 1, 0, 0, i+1, 0, time.UTC), Txs: [][]byte{bz}})
		require.NoError(t, err)
		require.Len(t, result.TxResults, 1)
		require.Zero(t, result.TxResults[0].Code, result.TxResults[0].Log)
		_, err = app.Commit()
		require.NoError(t, err)
		assertUpgraded(457 + uint64(i))
		committed := app.LastCommitID()
		require.NoError(t, app.Close())
		engine.Cleanup()
		app = newApp()
		require.Equal(t, committed, app.LastCommitID())
		assertUpgraded(457 + uint64(i))
		require.Equal(t, "100000000", app.BankKeeper.GetBalance(app.NewContext(true), recipient, "rune").Amount.String())
	}
	require.Equal(t, []byte(`"2"`), app.WasmKeeper.QueryRaw(app.NewContext(true), contract, []byte("counter")))
}
