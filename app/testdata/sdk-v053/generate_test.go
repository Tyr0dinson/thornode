//go:build mocknet

// Copy into app/ in baseline 757199a809e069932aeaff1628a6483d5fdde416 and run:
// SDK_UPGRADE_FIXTURE=/absolute/output/home go test -tags mocknet ./app -run '^TestGenerateSDKUpgradeFixture$' -count=1
package app

import (
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"cosmossdk.io/log"
	"cosmossdk.io/math"
	upgrade "cosmossdk.io/x/upgrade/types"
	abci "github.com/cometbft/cometbft/abci/types"
	cmtjson "github.com/cometbft/cometbft/libs/json"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	dbm "github.com/cosmos/cosmos-db"
	"github.com/cosmos/cosmos-sdk/baseapp"
	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	sdk "github.com/cosmos/cosmos-sdk/types"
	auth "github.com/cosmos/cosmos-sdk/x/auth/types"
	"github.com/stretchr/testify/require"
	"gitlab.com/thorchain/thornode/v3/constants"
	keeperv1 "gitlab.com/thorchain/thornode/v3/x/thorchain/keeper/v1"
	types "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

func TestGenerateSDKUpgradeFixture(t *testing.T) {
	home := os.Getenv("SDK_UPGRADE_FIXTURE")
	require.NotEmpty(t, home)
	types.SetupConfigForTest()
	constants.SWVersion = types.GetCurrentVersion()
	bz, err := os.ReadFile("../test/regression/mnt/exports/wasm/gas-fee.json")
	require.NoError(t, err)
	var genesis struct {
		ChainID       string          `json:"chain_id"`
		InitialHeight int64           `json:"initial_height"`
		AppState      json.RawMessage `json:"app_state"`
		Consensus     struct {
			Params json.RawMessage `json:"params"`
		} `json:"consensus"`
	}
	require.NoError(t, json.Unmarshal(bz, &genesis))
	var params cmtproto.ConsensusParams
	require.NoError(t, cmtjson.Unmarshal(genesis.Consensus.Params, &params))
	db, err := dbm.NewGoLevelDB("application", filepath.Join(home, "data"), nil)
	require.NoError(t, err)
	app := NewChainApp(log.NewNopLogger(), db, nil, true, &appOptionsWrapper{homeDir: home}, nil, baseapp.SetChainID(genesis.ChainID))
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	_, err = app.InitChain(&abci.RequestInitChain{ChainId: genesis.ChainID, InitialHeight: genesis.InitialHeight, Time: start, ConsensusParams: &params, AppStateBytes: genesis.AppState})
	require.NoError(t, err)
	ctx := app.NewContext(false)
	// Normalized exports strip versions; set the OLD binary's version here only.
	require.NoError(t, keeperv1.UpdateActiveValidatorVersions(ctx, app.ThorchainKeeper, constants.SWVersion.String()))
	key := secp256k1.GenPrivKeyFromSecret([]byte("sdk-upgrade-existing-account"))
	account := auth.NewBaseAccount(sdk.AccAddress(key.PubKey().Address()), key.PubKey(), 123, 456)
	app.AccountKeeper.SetAccount(ctx, account)
	coins := sdk.NewCoins(sdk.NewCoin("rune", math.NewInt(100000000000)))
	require.NoError(t, app.BankKeeper.MintCoins(ctx, types.ModuleName, coins))
	require.NoError(t, app.BankKeeper.SendCoinsFromModuleToAccount(ctx, types.ModuleName, account.GetAddress(), coins))
	require.NoError(t, app.UpgradeKeeper.ScheduleUpgrade(ctx, upgrade.Plan{Name: "4.0.0", Height: 8}))
	versions, err := app.UpgradeKeeper.GetModuleVersionMap(ctx)
	require.NoError(t, err)
	require.Equal(t, uint64(5), versions[auth.ModuleName])
	_, err = app.FinalizeBlock(&abci.RequestFinalizeBlock{Height: 7, Time: start})
	require.NoError(t, err)
	_, err = app.Commit()
	require.NoError(t, err)
	require.Equal(t, int64(7), app.LastBlockHeight())
	require.NoError(t, app.Close())

	// Dump every key/value pair as one hex "key=value" line, in key order.
	db, err = dbm.NewGoLevelDB("application", filepath.Join(home, "data"), nil)
	require.NoError(t, err)
	defer db.Close()
	it, err := db.Iterator(nil, nil)
	require.NoError(t, err)
	defer it.Close()
	var dump []byte
	for ; it.Valid(); it.Next() {
		dump = append(dump, hex.EncodeToString(it.Key())+"="+hex.EncodeToString(it.Value())+"\n"...)
	}
	require.NoError(t, it.Error())
	require.NoError(t, os.WriteFile(filepath.Join(home, "application.db.hex"), dump, 0o644))
}
