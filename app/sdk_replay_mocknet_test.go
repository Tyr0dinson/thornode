//go:build mocknet

package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"cosmossdk.io/log/v2"
	wasmkeeper "github.com/CosmWasm/wasmd/x/wasm/keeper"
	wasmtypes "github.com/CosmWasm/wasmd/x/wasm/types"
	wasmvm "github.com/CosmWasm/wasmvm/v3"
	abci "github.com/cometbft/cometbft/abci/types"
	cmtjson "github.com/cometbft/cometbft/libs/json"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	dbm "github.com/cosmos/cosmos-db"
	"github.com/cosmos/cosmos-sdk/baseapp"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"

	"gitlab.com/thorchain/thornode/v3/constants"
	keeperv1 "gitlab.com/thorchain/thornode/v3/x/thorchain/keeper/v1"
	thorchaintypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// This compares two local applications and a disk-backed restart. It does not
// exercise distributed consensus, validator networking, catch-up or state sync.
func TestSDKTwoAppReplayAndRestart(t *testing.T) {
	config := sdk.GetConfig()
	acc, accPub := config.GetBech32AccountAddrPrefix(), config.GetBech32AccountPubPrefix()
	val, valPub := config.GetBech32ValidatorAddrPrefix(), config.GetBech32ValidatorPubPrefix()
	cons, consPub := config.GetBech32ConsensusAddrPrefix(), config.GetBech32ConsensusPubPrefix()
	coinType, purpose := config.GetCoinType(), config.GetPurpose()
	cacheEnabled := sdk.IsAddrCacheEnabled()
	thorchaintypes.SetupConfigForTest()
	sdk.SetAddrCacheEnabled(false)
	t.Cleanup(func() {
		config.SetBech32PrefixForAccount(acc, accPub)
		config.SetBech32PrefixForValidator(val, valPub)
		config.SetBech32PrefixForConsensusNode(cons, consPub)
		config.SetCoinType(coinType)
		config.SetPurpose(purpose)
		sdk.SetAddrCacheEnabled(cacheEnabled)
	})
	oldVersion := constants.SWVersion
	constants.SWVersion = thorchaintypes.GetCurrentVersion()
	t.Cleanup(func() { constants.SWVersion = oldVersion })
	bz, err := os.ReadFile(filepath.Join("..", "test", "regression", "mnt", "exports", "wasm", "gas-fee.json"))
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
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	engines := map[string]*wasmvm.VM{}
	newApp := func(home string) *THORChainApp {
		db, err := dbm.NewGoLevelDB("application", filepath.Join(home, "data"), nil)
		require.NoError(t, err)
		captureVM := wasmkeeper.WithWasmEngineDecorator(func(engine wasmtypes.WasmEngine) wasmtypes.WasmEngine {
			engines[home] = engine.(*wasmvm.VM)
			return engine
		})
		return NewChainApp(log.NewNopLogger(), db, nil, true, &appOptionsWrapper{homeDir: home}, []wasmkeeper.Option{captureVM}, baseapp.SetChainID(genesis.ChainID))
	}
	homes := []string{t.TempDir(), t.TempDir()}
	apps := []*THORChainApp{newApp(homes[0]), newApp(homes[1])}
	t.Cleanup(func() {
		for i, app := range apps {
			if app != nil {
				require.NoError(t, app.Close())
				engines[homes[i]].Cleanup()
			}
		}
	})
	for _, app := range apps {
		_, err := app.InitChain(&abci.RequestInitChain{
			ChainId: genesis.ChainID, InitialHeight: genesis.InitialHeight,
			Time: start, ConsensusParams: &params, AppStateBytes: genesis.AppState,
		})
		require.NoError(t, err)
		// Regression exports strip node versions. Restore the fixture's active
		// nodes to this binary's version before running its real block handlers.
		require.NoError(t, keeperv1.UpdateActiveValidatorVersions(app.NewContext(false), app.ThorchainKeeper, constants.SWVersion.String()))
	}

	for offset := int64(0); offset < 4; offset++ {
		height := genesis.InitialHeight + offset
		request := &abci.RequestFinalizeBlock{Height: height, Time: start.Add(time.Duration(offset) * time.Second)}
		first, err := apps[0].FinalizeBlock(request)
		require.NoError(t, err)
		second, err := apps[1].FinalizeBlock(request)
		require.NoError(t, err)
		require.NotEmpty(t, first.AppHash)
		t.Logf("height=%d app_hash=%X", height, first.AppHash)
		require.Equal(t, first.AppHash, second.AppHash, "finalize app hash at height %d", height)
		require.Equal(t, first.ValidatorUpdates, second.ValidatorUpdates)
		for _, app := range apps {
			_, err := app.Commit()
			require.NoError(t, err)
		}
		require.Equal(t, apps[0].LastCommitID(), apps[1].LastCommitID())
		if offset == 1 {
			beforeRestart := apps[1].LastCommitID()
			require.NoError(t, apps[1].Close())
			apps[1] = nil
			// Release the native VM lock, as a real process exit would.
			engines[homes[1]].Cleanup()
			apps[1] = newApp(homes[1])
			require.Equal(t, beforeRestart, apps[1].LastCommitID(), "restart must load committed state")
		}
	}
	firstState, err := apps[0].ExportAppStateAndValidators(false, nil, nil)
	require.NoError(t, err)
	secondState, err := apps[1].ExportAppStateAndValidators(false, nil, nil)
	require.NoError(t, err)
	require.JSONEq(t, string(firstState.AppState), string(secondState.AppState))
	require.Equal(t, firstState.ConsensusParams, secondState.ConsensusParams)
	require.Equal(t, firstState.Height, secondState.Height)
}
