//go:build regtest
// +build regtest

package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"cosmossdk.io/core/appmodule"
	"cosmossdk.io/log"
	abci "github.com/cometbft/cometbft/abci/types"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	dbm "github.com/cosmos/cosmos-db"
	"github.com/cosmos/cosmos-sdk/baseapp"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/module"
	"github.com/stretchr/testify/require"
)

// These tests cover the regtest block-production hooks (app_regtest.go). Run
// them selectively with: go test -tags=regtest ./app -run RegtestCov

// regtestCovTimeout bounds every channel handshake so a broken hook fails the
// test instead of hanging the package run.
const regtestCovTimeout = 5 * time.Second

func regtestCovSend(t *testing.T, ch chan struct{}) {
	t.Helper()
	select {
	case ch <- struct{}{}:
	case <-time.After(regtestCovTimeout):
		t.Fatal("timed out sending on regtest channel")
	}
}

func regtestCovRecv(t *testing.T, ch chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(regtestCovTimeout):
		t.Fatal("timed out receiving on regtest channel")
	}
}

func TestWrapPrepareProposalRegtestCov(t *testing.T) {
	var gotReq *abci.RequestPrepareProposal
	inner := func(_ sdk.Context, req *abci.RequestPrepareProposal) (*abci.ResponsePrepareProposal, error) {
		gotReq = req
		return &abci.ResponsePrepareProposal{Txs: req.Txs}, nil
	}
	h := wrapPrepareProposal(inner)
	baseCtx := sdk.Context{}.WithLogger(log.NewNopLogger())

	run := func(ctx sdk.Context, req *abci.RequestPrepareProposal) *abci.ResponsePrepareProposal {
		type result struct {
			resp *abci.ResponsePrepareProposal
			err  error
		}
		done := make(chan result, 1)
		go func() {
			resp, err := h(ctx, req)
			done <- result{resp, err}
		}()
		// the proposal is parked until the harness releases it
		select {
		case <-done:
			t.Fatal("PrepareProposal returned before the harness released it")
		case <-time.After(10 * time.Millisecond):
		}
		regtestCovSend(t, begin)
		select {
		case r := <-done:
			require.NoError(t, r.err)
			return r.resp
		case <-time.After(regtestCovTimeout):
			t.Fatal("PrepareProposal did not return")
			return nil
		}
	}

	// no reaper: the txs CometBFT reaped are passed through
	SetRegtestMempoolReaper(nil)
	resp := run(baseCtx, &abci.RequestPrepareProposal{Txs: [][]byte{{1}}, MaxTxBytes: 100})
	require.Equal(t, [][]byte{{1}}, resp.Txs)

	type reapCall struct{ maxBytes, maxGas int64 }
	var calls []reapCall
	SetRegtestMempoolReaper(func(maxBytes, maxGas int64) [][]byte {
		calls = append(calls, reapCall{maxBytes, maxGas})
		return [][]byte{{9}, {8}}
	})
	defer SetRegtestMempoolReaper(nil)

	// unlimited block bytes -> reap without a byte limit, with the block gas limit
	ctx := baseCtx.WithConsensusParams(cmtproto.ConsensusParams{Block: &cmtproto.BlockParams{MaxBytes: -1, MaxGas: 500}})
	resp = run(ctx, &abci.RequestPrepareProposal{Txs: [][]byte{{1}}, MaxTxBytes: 100})
	require.Equal(t, [][]byte{{9}, {8}}, resp.Txs)
	require.Equal(t, [][]byte{{9}, {8}}, gotReq.Txs)
	require.Equal(t, reapCall{-1, 500}, calls[0])

	// bounded block bytes -> the request's MaxTxBytes is used
	ctx = baseCtx.WithConsensusParams(cmtproto.ConsensusParams{Block: &cmtproto.BlockParams{MaxBytes: 2000, MaxGas: 7}})
	run(ctx, &abci.RequestPrepareProposal{MaxTxBytes: 1500})
	require.Equal(t, reapCall{1500, 7}, calls[1])

	// no block params -> no gas limit
	run(baseCtx, &abci.RequestPrepareProposal{MaxTxBytes: 42})
	require.Equal(t, reapCall{42, -1}, calls[2])
}

func TestNewBlockHandlerRegtestCov(t *testing.T) {
	serve := func() string {
		rec := httptest.NewRecorder()
		done := make(chan struct{})
		go func() {
			http.DefaultServeMux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/newBlock", nil))
			close(done)
		}()
		// the request starts block production, then waits for the commit
		regtestCovRecv(t, begin)
		select {
		case <-done:
			t.Fatal("/newBlock returned before the block was committed")
		case <-time.After(10 * time.Millisecond):
		}
		regtestCovSend(t, end)
		regtestCovRecv(t, done)
		require.Equal(t, http.StatusOK, rec.Code)
		return rec.Body.String()
	}
	first := serve()
	second := serve()
	require.NotEmpty(t, first)
	require.NotEqual(t, first, second, "each block gets a new count")
}

func TestSetPrepareCheckStaterRegtestCov(t *testing.T) {
	bApp := baseapp.NewBaseApp("regtestcov", log.NewNopLogger(), dbm.NewMemDB(), nil, baseapp.SetChainID("regtestcov"))
	setPrepareCheckStater(bApp)
	require.NoError(t, bApp.LoadLatestVersion())
	_, err := bApp.InitChain(&abci.RequestInitChain{ChainId: "regtestcov", InitialHeight: 1})
	require.NoError(t, err)
	_, err = bApp.FinalizeBlock(&abci.RequestFinalizeBlock{Height: 1})
	require.NoError(t, err)

	committed := make(chan error, 1)
	go func() {
		_, err := bApp.Commit()
		committed <- err
	}()
	// Commit signals /newBlock once the block is persisted
	regtestCovRecv(t, end)
	select {
	case err := <-committed:
		require.NoError(t, err)
	case <-time.After(regtestCovTimeout):
		t.Fatal("Commit did not return")
	}
	require.Equal(t, int64(1), bApp.LastBlockHeight())
}

// regtestCovModule records the block time each block hook sees.
type regtestCovModule struct {
	begin, end time.Time
}

func (m *regtestCovModule) IsOnePerModuleType() {}
func (m *regtestCovModule) IsAppModule()        {}

func (m *regtestCovModule) BeginBlock(ctx context.Context) error {
	m.begin = sdk.UnwrapSDKContext(ctx).BlockTime()
	return nil
}

func (m *regtestCovModule) EndBlock(ctx context.Context) error {
	m.end = sdk.UnwrapSDKContext(ctx).BlockTime()
	return nil
}

var (
	_ appmodule.HasBeginBlocker = (*regtestCovModule)(nil)
	_ appmodule.HasEndBlocker   = (*regtestCovModule)(nil)
)

func TestBlockersUseHeightTimestampRegtestCov(t *testing.T) {
	mod := &regtestCovModule{}
	app := &THORChainApp{ModuleManager: module.NewManagerFromMap(map[string]appmodule.AppModule{"regtestcov": mod})}
	ctx := sdk.Context{}.
		WithLogger(log.NewNopLogger()).
		WithEventManager(sdk.NewEventManager()).
		WithBlockHeight(1234).
		WithBlockTime(time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC))

	_, err := app.BeginBlocker(ctx)
	require.NoError(t, err)
	require.Equal(t, time.Unix(1234, 0).UTC(), mod.begin)

	_, err = app.EndBlocker(ctx.WithBlockHeight(1235))
	require.NoError(t, err)
	require.Equal(t, time.Unix(1235, 0).UTC(), mod.end)
}
