package main

import (
	"fmt"
	"os"
	"path/filepath"

	"cosmossdk.io/log"
	wasmkeeper "github.com/CosmWasm/wasmd/x/wasm/keeper"
	tmproto "github.com/cometbft/cometbft/proto/tendermint/types"
	dbm "github.com/cosmos/cosmos-db"
	"github.com/cosmos/cosmos-sdk/client/flags"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"gitlab.com/thorchain/thornode/v3/app"
	"gitlab.com/thorchain/thornode/v3/app/upgrades"
	prefix "gitlab.com/thorchain/thornode/v3/cmd"
)

type appOptions map[string]interface{}

func (o appOptions) Get(key string) interface{} {
	return o[key]
}

func main() {
	cfg := sdk.GetConfig()
	cfg.SetBech32PrefixForAccount(prefix.Bech32PrefixAccAddr, prefix.Bech32PrefixAccPub)
	cfg.Seal()

	home := os.Getenv("THORNODE_HOME")
	if home == "" {
		userHome, err := os.UserHomeDir()
		if err != nil {
			_, _ = fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		home = filepath.Join(userHome, ".thornode")
	}

	backend := dbm.GoLevelDBBackend
	if configured := os.Getenv("THORNODE_DB_BACKEND"); configured != "" {
		backend = dbm.BackendType(configured)
	}

	db, err := dbm.NewDB("application", backend, filepath.Join(home, "data"))
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer db.Close()

	opts := appOptions{
		flags.FlagHome:    home,
		app.TestApp:       true,
		"ebifrost.enable": false,
	}
	chainApp := app.NewChainApp(log.NewNopLogger(), db, nil, true, opts, []wasmkeeper.Option{})
	ctx := chainApp.BaseApp.NewUncachedContext(true, tmproto.Header{})

	if err := upgrades.Execute(ctx, chainApp.ThorchainKeeper, chainApp.BankKeeper); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
