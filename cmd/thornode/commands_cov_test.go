package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"cosmossdk.io/log"
	cmtcli "github.com/cometbft/cometbft/libs/cli"
	dbm "github.com/cosmos/cosmos-db"
	"github.com/cosmos/cosmos-sdk/baseapp"
	"github.com/cosmos/cosmos-sdk/client/flags"
	"github.com/cosmos/cosmos-sdk/server"
	svrcmd "github.com/cosmos/cosmos-sdk/server/cmd"
	serverconfig "github.com/cosmos/cosmos-sdk/server/config"
	simtestutil "github.com/cosmos/cosmos-sdk/testutil/sims"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/mempool"
	"github.com/cosmos/cosmos-sdk/types/module"
	"github.com/cosmos/cosmos-sdk/version"
	"github.com/cosmos/cosmos-sdk/x/genutil"
	genutiltypes "github.com/cosmos/cosmos-sdk/x/genutil/types"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/require"

	"gitlab.com/thorchain/thornode/v3/app"
	prefix "gitlab.com/thorchain/thornode/v3/cmd"
)

var (
	rootCmdCovOnce sync.Once
	rootCmdCov     *cobra.Command
)

// getRootCmdCov builds the root command once: NewRootCmd seals the global sdk
// config, so it can only run once per process.
func getRootCmdCov() *cobra.Command {
	rootCmdCovOnce.Do(func() {
		rootCmdCov = NewRootCmd()
		// mirror svrcmd.Execute's flag setup without running the command
		rootCmdCov.PersistentFlags().String(flags.FlagLogLevel, "info", "")
		rootCmdCov.PersistentFlags().String(flags.FlagLogFormat, "plain", "")
		rootCmdCov.PersistentFlags().Bool(flags.FlagLogNoColor, false, "")
		_ = cmtcli.PrepareBaseCmd(rootCmdCov, "", app.DefaultNodeHome)
	})
	return rootCmdCov
}

// executeRootCov runs the root command in process with the given args.
func executeRootCov(args ...string) error {
	root := getRootCmdCov()
	root.SetArgs(args)
	return root.ExecuteContext(svrcmd.CreateExecuteContext(context.Background()))
}

func findSubCmdCov(t *testing.T, root *cobra.Command, args ...string) *cobra.Command {
	t.Helper()
	cmd, _, err := root.Find(args)
	require.NoError(t, err)
	return cmd
}

// captureStdoutCov captures os.Stdout while f runs.
func captureStdoutCov(t *testing.T, f func() error) (string, error) {
	t.Helper()
	r, w, err := os.Pipe()
	require.NoError(t, err)
	old := os.Stdout
	os.Stdout = w
	runErr := f()
	os.Stdout = old
	require.NoError(t, w.Close())
	buf, err := io.ReadAll(r)
	require.NoError(t, err)
	return string(buf), runErr
}

func TestInitConfigsCov(t *testing.T) {
	cmtCfg := initCometBFTConfig()
	require.NotNil(t, cmtCfg)
	require.NoError(t, cmtCfg.ValidateBasic())

	tmpl, cfg := initAppConfig()
	require.True(t, strings.HasPrefix(tmpl, serverconfig.DefaultConfigTemplate))
	require.Contains(t, tmpl, "[wasm]")
	require.Contains(t, tmpl, "[ebifrost]")
	require.Contains(t, fmt.Sprintf("%+v", cfg), "MinGasPrices:0stake")
}

func TestRootCmdStructureCov(t *testing.T) {
	root := getRootCmdCov()
	require.Equal(t, version.AppName, root.Use)
	for _, path := range [][]string{
		{"init"},
		{"start"},
		{"render-config"},
		{"ed25519"},
		{"pubkey"},
		{"validate-seed-phrase"},
		{"compact"},
		{"query", "address"},
		{"tx", "sign"},
		{"genesis"},
		{"keys"},
		{"status"},
	} {
		cmd := findSubCmdCov(t, root, path...)
		require.Equal(t, path[len(path)-1], strings.Fields(cmd.Use)[0], "path %v", path)
	}
	// bech32 prefixes were configured and sealed
	require.Equal(t, prefix.Bech32PrefixAccAddr, sdk.GetConfig().GetBech32AccountAddrPrefix())
}

func TestQueryMappedAddressCov(t *testing.T) {
	home := t.TempDir()

	evm := "0x4242424242424242424242424242424242424242"
	want := sdk.AccAddress(bytes.Repeat([]byte{0x42}, 20)).String()

	out, err := captureStdoutCov(t, func() error {
		return executeRootCov("query", "address", evm, "--home", home)
	})
	require.NoError(t, err)
	require.Equal(t, want, strings.TrimSpace(out))

	// the root pre-run wrote the client and node configs into the home dir
	require.FileExists(t, filepath.Join(home, "config", "client.toml"))
	require.FileExists(t, filepath.Join(home, "config", "app.toml"))
	require.FileExists(t, filepath.Join(home, "config", "config.toml"))

	// invalid address
	_, err = captureStdoutCov(t, func() error {
		return executeRootCov("query", "address", "bad address!", "--home", home)
	})
	require.ErrorContains(t, err, "failed to read address")

	// valid but unmappable (non bech32 BTC) address
	_, err = captureStdoutCov(t, func() error {
		return executeRootCov("query", "address", "1BvBMSEYstWetqTFn5Au4m4GFg7xJaNVN2", "--home", home)
	})
	require.ErrorContains(t, err, "failed to convert to bech32")
}

func TestStartPreRunCov(t *testing.T) {
	root := getRootCmdCov()
	start := findSubCmdCov(t, root, "start")
	require.NotNil(t, start.PreRunE)
	require.NotNil(t, start.Flags().Lookup("ebifrost.address"))

	start.SetContext(svrcmd.CreateExecuteContext(context.Background()))
	serverCtx := server.NewDefaultContext()
	require.NoError(t, server.SetCmdServerContext(start, serverCtx))
	require.NoError(t, start.PreRunE(start, nil))

	got := server.GetServerContextFromCmd(start)
	_, ok := got.Logger.(ConsensusFailureLogger)
	require.True(t, ok, "logger should be wrapped, got %T", got.Logger)
}

func TestCompactCommandCov(t *testing.T) {
	home := t.TempDir()
	old := app.DefaultNodeHome
	app.DefaultNodeHome = home
	defer func() { app.DefaultNodeHome = old }()

	cmd := compactCommand()
	require.NoError(t, cmd.RunE(cmd, nil))
	require.DirExists(t, filepath.Join(home, "data", "application.db"))

	// a file where the data directory should be makes opening the db fail
	home2 := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(home2, "data"), []byte("x"), 0o600))
	app.DefaultNodeHome = home2
	require.Error(t, cmd.RunE(cmd, nil))
}

func TestGenesisCommandCov(t *testing.T) {
	encCfg := app.MakeEncodingConfig()
	extra := &cobra.Command{Use: "extra-cov"}
	basics := module.NewBasicManager(genutil.NewAppModuleBasic(genutiltypes.DefaultMessageValidator))
	cmd := genesisCommand(encCfg.TxConfig, basics, extra)
	sub, _, err := cmd.Find([]string{"extra-cov"})
	require.NoError(t, err)
	require.Equal(t, extra, sub)
}

func writeGenesisCov(t *testing.T, home, chainID string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Join(home, "config"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(home, "config", "genesis.json"),
		[]byte(`{"chain_id":"`+chainID+`"}`), 0o600))
}

func TestDefaultBaseappOptionsCov(t *testing.T) {
	home := t.TempDir()

	// chain id from flags
	opts := DefaultBaseappOptions(simtestutil.AppOptionsMap{
		server.FlagPruning:         "default",
		flags.FlagHome:             home,
		flags.FlagChainID:          "flag-chain",
		server.FlagMempoolMaxTxs:   -1,
		server.FlagInterBlockCache: true,
		server.FlagHaltHeight:      123,
	})
	require.Len(t, opts, 12)
	bapp := baseapp.NewBaseApp("cov", log.NewNopLogger(), dbm.NewMemDB(), nil, opts...)
	require.Equal(t, "flag-chain", bapp.ChainID())

	// chain id from genesis
	home = t.TempDir() // the snapshot store holds a lock on the previous home
	writeGenesisCov(t, home, "genesis-chain")
	opts = DefaultBaseappOptions(simtestutil.AppOptionsMap{
		server.FlagPruning:       "default",
		flags.FlagHome:           home,
		server.FlagMempoolMaxTxs: 10,
	})
	require.Len(t, opts, 12)
	bapp = baseapp.NewBaseApp("cov", log.NewNopLogger(), dbm.NewMemDB(), nil, opts...)
	require.Equal(t, "genesis-chain", bapp.ChainID())
	_, ok := bapp.Mempool().(mempool.NoOpMempool)
	require.False(t, ok, "max txs >= 0 should select the sender nonce mempool")

	// missing genesis panics
	require.Panics(t, func() {
		DefaultBaseappOptions(simtestutil.AppOptionsMap{flags.FlagHome: t.TempDir(), server.FlagPruning: "default"})
	})

	// malformed genesis panics
	bad := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(bad, "config"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(bad, "config", "genesis.json"), []byte("{"), 0o600))
	require.Panics(t, func() {
		DefaultBaseappOptions(simtestutil.AppOptionsMap{flags.FlagHome: bad, server.FlagPruning: "default"})
	})

	// invalid pruning options panic
	require.Panics(t, func() {
		DefaultBaseappOptions(simtestutil.AppOptionsMap{
			flags.FlagHome:               home,
			server.FlagPruning:           "custom",
			server.FlagPruningKeepRecent: "0",
			server.FlagPruningInterval:   "0",
		})
	})
}

func TestNewAppCov(t *testing.T) {
	getRootCmdCov() // ensure global sdk config is set up
	home := t.TempDir()
	a := newApp(log.NewNopLogger(), dbm.NewMemDB(), nil, simtestutil.AppOptionsMap{
		server.FlagPruning: "default",
		flags.FlagHome:     home,
		flags.FlagChainID:  "cov-chain",
		app.TestApp:        true,
	})
	require.NotNil(t, a)
	chainApp, ok := a.(*app.THORChainApp)
	require.True(t, ok)
	require.Equal(t, "cov-chain", chainApp.ChainID())
}

func TestAppExportCov(t *testing.T) {
	getRootCmdCov()

	// home is required
	_, err := appExport(log.NewNopLogger(), dbm.NewMemDB(), nil, -1, false, nil,
		simtestutil.AppOptionsMap{}, nil)
	require.ErrorContains(t, err, "application home is not set")

	// app options must be a viper instance
	_, err = appExport(log.NewNopLogger(), dbm.NewMemDB(), nil, -1, false, nil,
		simtestutil.AppOptionsMap{flags.FlagHome: t.TempDir()}, nil)
	require.ErrorContains(t, err, "appOpts is not viper.Viper")

	// loading a height that does not exist fails
	v := viper.New()
	v.Set(flags.FlagHome, t.TempDir())
	v.Set(app.TestApp, true)
	v.Set(server.FlagPruning, "default")
	_, err = appExport(log.NewNopLogger(), dbm.NewMemDB(), nil, 5, false, nil, v, nil)
	require.Error(t, err)
	require.Equal(t, 1, v.GetInt(server.FlagInvCheckPeriod))
}
