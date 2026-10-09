//go:build regtest
// +build regtest

package main

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"

	"cosmossdk.io/log"
	abci "github.com/cometbft/cometbft/abci/types"
	cmtcfg "github.com/cometbft/cometbft/config"
	pvm "github.com/cometbft/cometbft/privval"
	cmttypes "github.com/cometbft/cometbft/types"
	dbm "github.com/cosmos/cosmos-db"
	"github.com/cosmos/cosmos-sdk/client"
	"github.com/cosmos/cosmos-sdk/server"
	"github.com/cosmos/cosmos-sdk/server/api"
	serverconfig "github.com/cosmos/cosmos-sdk/server/config"
	servertypes "github.com/cosmos/cosmos-sdk/server/types"
	genutiltypes "github.com/cosmos/cosmos-sdk/x/genutil/types"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/require"
)

// regtestCovApp is a minimal ABCI application: just enough for an in-process
// CometBFT node to handshake, start and (with empty blocks disabled) idle.
// Any other servertypes.Application call panics via the nil embedded value.
type regtestCovApp struct {
	servertypes.Application

	mu         sync.Mutex
	chainID    string
	closed     int
	apiRoutes  bool
	txService  chan struct{}
	onceTxServ sync.Once
}

func newRegtestCovApp() *regtestCovApp {
	return &regtestCovApp{txService: make(chan struct{})}
}

func (a *regtestCovApp) Info(*abci.RequestInfo) (*abci.ResponseInfo, error) {
	return &abci.ResponseInfo{}, nil
}

func (a *regtestCovApp) Query(context.Context, *abci.RequestQuery) (*abci.ResponseQuery, error) {
	return &abci.ResponseQuery{}, nil
}

func (a *regtestCovApp) CheckTx(*abci.RequestCheckTx) (*abci.ResponseCheckTx, error) {
	return &abci.ResponseCheckTx{}, nil
}

func (a *regtestCovApp) InitChain(req *abci.RequestInitChain) (*abci.ResponseInitChain, error) {
	a.mu.Lock()
	a.chainID = req.ChainId
	a.mu.Unlock()
	return &abci.ResponseInitChain{}, nil
}

func (a *regtestCovApp) PrepareProposal(req *abci.RequestPrepareProposal) (*abci.ResponsePrepareProposal, error) {
	return &abci.ResponsePrepareProposal{Txs: req.Txs}, nil
}

func (a *regtestCovApp) ProcessProposal(*abci.RequestProcessProposal) (*abci.ResponseProcessProposal, error) {
	return &abci.ResponseProcessProposal{Status: abci.ResponseProcessProposal_ACCEPT}, nil
}

func (a *regtestCovApp) FinalizeBlock(req *abci.RequestFinalizeBlock) (*abci.ResponseFinalizeBlock, error) {
	return &abci.ResponseFinalizeBlock{
		TxResults: make([]*abci.ExecTxResult, len(req.Txs)),
		AppHash:   []byte("regtestcov"),
	}, nil
}

func (a *regtestCovApp) ExtendVote(context.Context, *abci.RequestExtendVote) (*abci.ResponseExtendVote, error) {
	return &abci.ResponseExtendVote{}, nil
}

func (a *regtestCovApp) VerifyVoteExtension(*abci.RequestVerifyVoteExtension) (*abci.ResponseVerifyVoteExtension, error) {
	return &abci.ResponseVerifyVoteExtension{Status: abci.ResponseVerifyVoteExtension_ACCEPT}, nil
}

func (a *regtestCovApp) Commit() (*abci.ResponseCommit, error) { return &abci.ResponseCommit{}, nil }

func (a *regtestCovApp) ListSnapshots(*abci.RequestListSnapshots) (*abci.ResponseListSnapshots, error) {
	return &abci.ResponseListSnapshots{}, nil
}

func (a *regtestCovApp) RegisterAPIRoutes(*api.Server, serverconfig.APIConfig) {
	a.mu.Lock()
	a.apiRoutes = true
	a.mu.Unlock()
}

func (a *regtestCovApp) RegisterTxService(client.Context) {
	a.onceTxServ.Do(func() { close(a.txService) })
}
func (a *regtestCovApp) RegisterTendermintService(client.Context)                {}
func (a *regtestCovApp) RegisterNodeService(client.Context, serverconfig.Config) {}

func (a *regtestCovApp) Close() error {
	a.mu.Lock()
	a.closed++
	a.mu.Unlock()
	return nil
}

func (a *regtestCovApp) creator() servertypes.AppCreator {
	return func(log.Logger, dbm.DB, io.Writer, servertypes.AppOptions) servertypes.Application { return a }
}

func regtestCovMemDB(string, dbm.BackendType) (dbm.DB, error) { return dbm.NewMemDB(), nil }

// regtestCovServerCtx returns a server context rooted at a fresh home with a
// valid app config (min gas price set, API and gRPC disabled).
func regtestCovServerCtx(t *testing.T) *server.Context {
	t.Helper()
	home := t.TempDir()
	cfg := cmtcfg.DefaultConfig()
	cfg.SetRoot(home)
	cmtcfg.EnsureRoot(home)
	cfg.P2P.ListenAddress = "tcp://127.0.0.1:0"
	cfg.RPC.ListenAddress = "tcp://127.0.0.1:0"
	cfg.Consensus.CreateEmptyBlocks = false
	v := viper.New()
	v.Set("minimum-gas-prices", "0rune")
	v.Set("api.enable", false)
	v.Set("grpc.enable", false)
	return server.NewContext(v, cfg, log.NewNopLogger())
}

// regtestCovWriteGenesis writes an app genesis whose only validator is the
// node's own private validator.
func regtestCovWriteGenesis(t *testing.T, cfg *cmtcfg.Config, chainID string) {
	t.Helper()
	pv := pvm.LoadOrGenFilePV(cfg.PrivValidatorKeyFile(), cfg.PrivValidatorStateFile())
	pub, err := pv.GetPubKey()
	require.NoError(t, err)
	gen := genutiltypes.NewAppGenesisWithVersion(chainID, []byte("{}"))
	gen.GenesisTime = time.Now().UTC()
	gen.InitialHeight = 1
	gen.Consensus = &genutiltypes.ConsensusGenesis{
		Validators: []cmttypes.GenesisValidator{{Address: pub.Address(), PubKey: pub, Power: 10, Name: "regtestcov"}},
		Params:     cmttypes.DefaultConsensusParams(),
	}
	require.NoError(t, gen.SaveAs(cfg.GenesisFile()))
}

func TestStartCmdOptionsRegtestCov(t *testing.T) {
	called := false
	opts := startCmdOptions(func(*cobra.Command) { called = true })
	require.NotNil(t, opts.StartCommandHandler)
	require.NotNil(t, opts.AddFlags)
	opts.AddFlags(nil)
	require.True(t, called)
}

func TestRegtestStartRejectsBadSetupCov(t *testing.T) {
	opts := server.StartCmdOptions{DBOpener: regtestCovMemDB}

	// CometBFT must run in-process
	app := newRegtestCovApp()
	err := regtestStart(regtestCovServerCtx(t), client.Context{}, app.creator(), false, opts)
	require.ErrorContains(t, err, "regtest start requires in-process CometBFT")

	// app config without a min gas price fails validation
	svrCtx := regtestCovServerCtx(t)
	svrCtx.Viper.Set("minimum-gas-prices", "")
	err = regtestStart(svrCtx, client.Context{}, app.creator(), true, opts)
	require.ErrorContains(t, err, "set min gas price")

	// the application DB cannot be opened
	badDB := server.StartCmdOptions{DBOpener: func(string, dbm.BackendType) (dbm.DB, error) {
		return nil, errors.New("db unavailable")
	}}
	err = regtestStart(regtestCovServerCtx(t), client.Context{}, app.creator(), true, badDB)
	require.ErrorContains(t, err, "db unavailable")
	require.Equal(t, 0, app.closed, "no app is created without a DB")

	// malformed REGTEST_TIMEOUT_COMMIT; the app is still closed
	t.Setenv("REGTEST_TIMEOUT_COMMIT", "soon")
	err = regtestStart(regtestCovServerCtx(t), client.Context{}, app.creator(), true, opts)
	require.ErrorContains(t, err, "bad REGTEST_TIMEOUT_COMMIT")
	require.Equal(t, 1, app.closed)
	t.Setenv("REGTEST_TIMEOUT_COMMIT", "")

	// node key cannot be written (config dir is a regular file)
	svrCtx = regtestCovServerCtx(t)
	home := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(home, "config"), []byte("x"), 0o600))
	svrCtx.Config.SetRoot(home)
	err = regtestStart(svrCtx, client.Context{}, app.creator(), true, opts)
	require.Error(t, err)
	require.Equal(t, 2, app.closed)

	// no genesis file -> node construction fails
	err = regtestStart(regtestCovServerCtx(t), client.Context{}, app.creator(), true, opts)
	require.ErrorContains(t, err, "genesis")
	require.Equal(t, 3, app.closed)
}

func TestRegtestGenDocProviderCov(t *testing.T) {
	svrCtx := regtestCovServerCtx(t)
	_, err := regtestGenDocProvider(svrCtx.Config)()
	require.Error(t, err)

	regtestCovWriteGenesis(t, svrCtx.Config, "regtestcov-1")
	doc, err := regtestGenDocProvider(svrCtx.Config)()
	require.NoError(t, err)
	require.Equal(t, "regtestcov-1", doc.ChainID)
	require.Len(t, doc.Validators, 1)
}

// TestRegtestStartRunsNodeCov boots the in-process node with the regtest
// consensus overrides and the REST API, then stops it with SIGTERM (which
// regtestStart's quit-signal listener intercepts).
func TestRegtestStartRunsNodeCov(t *testing.T) {
	svrCtx := regtestCovServerCtx(t)
	svrCtx.Viper.Set("api.enable", true)
	svrCtx.Viper.Set("api.address", "tcp://127.0.0.1:0")
	regtestCovWriteGenesis(t, svrCtx.Config, "regtestcov-2")
	t.Setenv("REGTEST_TIMEOUT_COMMIT", "25ms")

	app := newRegtestCovApp()
	done := make(chan error, 1)
	go func() {
		done <- regtestStart(svrCtx, client.Context{}, app.creator(), true, server.StartCmdOptions{DBOpener: regtestCovMemDB})
	}()

	select {
	case <-app.txService:
	case err := <-done:
		t.Fatalf("regtestStart returned early: %v", err)
	case <-time.After(30 * time.Second):
		t.Fatal("node did not start")
	}
	// The quit-signal handler is registered before the node starts, so the
	// signal is consumed by regtestStart rather than terminating the test.
	require.NoError(t, syscall.Kill(os.Getpid(), syscall.SIGTERM))

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(30 * time.Second):
		t.Fatal("regtestStart did not stop after SIGTERM")
	}

	app.mu.Lock()
	defer app.mu.Unlock()
	require.Equal(t, "regtestcov-2", app.chainID)
	require.True(t, app.apiRoutes)
	require.Equal(t, 1, app.closed)
	require.Equal(t, 25*time.Millisecond, svrCtx.Config.Consensus.TimeoutCommit)
	require.Equal(t, 24*time.Hour, svrCtx.Config.Consensus.TimeoutPropose)
	require.Equal(t, time.Duration(0), svrCtx.Config.Consensus.TimeoutProposeDelta)
}
