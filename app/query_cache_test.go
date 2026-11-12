package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	wasmkeeper "github.com/CosmWasm/wasmd/x/wasm/keeper"
	wasmvmtypes "github.com/CosmWasm/wasmvm/v2/types"
	abci "github.com/cometbft/cometbft/abci/types"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	dbm "github.com/cosmos/cosmos-db"
	grpctypes "github.com/cosmos/cosmos-sdk/types/grpc"
	gateway "github.com/cosmos/gogogateway"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"

	apitypes "gitlab.com/thorchain/thornode/v3/api/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	thortypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// ---------------------------------------------------------------------------
// cache unit tests
// ---------------------------------------------------------------------------

func testQueryCache(maxBytes int64) *queryCache {
	return newQueryCache(maxBytes, func() time.Duration { return time.Minute })
}

func TestQueryCachePolicy(t *testing.T) {
	for path, want := range map[string]queryCachePolicy{
		"/types.Query/Pools":                                     queryCacheAlways,
		"/types.Query/InboundAddresses":                          queryCacheAlways,
		"/cosmwasm.wasm.v1.Query/SmartContractState":             queryCacheAlways,
		"/cosmos.bank.v1beta1.Query/AllBalances":                 queryCacheAlways,
		"/types.Query/QuoteSwap":                                 queryCacheWhileFresh,
		"/types.Query/QuoteLimit":                                queryCacheWhileFresh,
		"/types.Query/Block":                                     queryCacheNever,
		"/types.Query/Export":                                    queryCacheNever,
		"/types.Query/Keysign":                                   queryCacheNever,
		"/types.Query/KeysignPubkey":                             queryCacheNever,
		"/types.Query/Keygen":                                    queryCacheNever,
		"/cosmos.tx.v1beta1.Service/Simulate":                    queryCacheNever,
		"/cosmos.tx.v1beta1.Service/BroadcastTx":                 queryCacheNever,
		"/cosmos.tx.v1beta1.Service/GetTx":                       queryCacheNever,
		"/cosmos.base.tendermint.v1beta1.Service/GetLatestBlock": queryCacheNever,
		"/cosmos.base.tendermint.v1beta1.Service/ABCIQuery":      queryCacheNever,
		"/cosmos.base.node.v1beta1.Service/Status":               queryCacheNever,
		"/app/simulate":                                          queryCacheNever,
		"/store/thorchain/key":                                   queryCacheNever,
		"/p2p/filter/id/x":                                       queryCacheNever,
		"":                                                       queryCacheNever,
		"types.Query/Pools":                                      queryCacheNever,
	} {
		require.Equal(t, want, queryCachePolicyFor(path), path)
	}
}

func TestQueryCacheUsable(t *testing.T) {
	c := testQueryCache(1 << 20)
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	c.now = func() time.Time { return now }

	// nothing committed yet
	require.False(t, c.usable(queryCacheAlways, 1))

	c.committed(10, now.Add(-30*time.Second))
	require.True(t, c.usable(queryCacheAlways, 10))
	require.True(t, c.usable(queryCacheAlways, 3), "explicit older height")
	require.False(t, c.usable(queryCacheAlways, 11), "height not committed yet")
	require.False(t, c.usable(queryCacheAlways, 0))
	require.False(t, c.usable(queryCacheNever, 10))

	// quotes: only at the latest height, and only while it is fresh
	require.True(t, c.usable(queryCacheWhileFresh, 10))
	require.False(t, c.usable(queryCacheWhileFresh, 9))
	now = now.Add(31 * time.Second) // block now 61s old, max lag 1m
	require.False(t, c.usable(queryCacheWhileFresh, 10))
	require.True(t, c.usable(queryCacheAlways, 10))
}

func TestQueryCacheByteCapEvicts(t *testing.T) {
	c := testQueryCache(4096)
	c.committed(5, time.Now())
	key := func(i int) string { return queryCacheKey(queryCacheKindABCI, 5, "/types.Query/Pool", []byte{byte(i)}) }
	for i := 0; i < 20; i++ {
		c.add(key(i), 5, false, 300, i)
		require.LessOrEqual(t, c.bytes, c.maxBytes)
		if i == 1 {
			_, ok := c.get(key(0)) // refresh 0: it should outlive 1
			require.True(t, ok)
		}
	}
	require.Positive(t, c.evictions.Load())
	_, ok := c.get(key(19))
	require.True(t, ok, "newest entry kept")
	_, ok = c.get(key(1))
	require.False(t, ok, "least recently used entry evicted")
	require.Equal(t, int64(len(c.items)), int64(c.ll.Len()))

	// a response bigger than a quarter of the cache is never stored
	c.add("big", 5, false, 2000, "big")
	_, ok = c.get("big")
	require.False(t, ok)
}

func TestQueryCacheCommitDropsLatestEntries(t *testing.T) {
	c := testQueryCache(1 << 20)
	c.committed(5, time.Now())
	c.add("latest@5", 5, true, 10, 1)
	c.add("explicit@5", 5, false, 10, 2)
	c.committed(6, time.Now())
	_, ok := c.get("latest@5")
	require.False(t, ok)
	_, ok = c.get("explicit@5")
	require.True(t, ok)
	// a latest response computed for 5 that lands after 6 committed is dropped
	c.add("late@5", 5, true, 10, 3)
	_, ok = c.get("late@5")
	require.False(t, ok)
	require.Empty(t, c.latestEls, "the latest index never outlives its entries")
}

func TestQueryCacheLatestIndexTracksEviction(t *testing.T) {
	c := testQueryCache(1000)
	c.committed(5, time.Now())
	c.add("latest@5", 5, true, 10, 1)
	c.add("latest@5", 5, true, 10, 2) // replacing re-indexes the new element
	require.Len(t, c.latestEls, 1)
	for i := 0; i < 20; i++ { // push it out of the LRU
		c.add(fmt.Sprintf("explicit%d", i), 5, false, 10, i)
	}
	_, ok := c.get("latest@5")
	require.False(t, ok)
	require.Empty(t, c.latestEls, "eviction drops the index entry too")
	n := c.ll.Len()
	require.Positive(t, n)
	c.committed(6, time.Now())
	require.Equal(t, n, c.ll.Len(), "the sweep leaves explicit-height entries alone")
}

func TestQueryCacheSingleflight(t *testing.T) {
	c := testQueryCache(1 << 20)
	c.committed(5, time.Now())
	key := queryCacheKey(queryCacheKindGRPC, 5, "/types.Query/Pools", nil)

	var fills atomic.Int32
	release := make(chan struct{})
	const n = 32
	var wg sync.WaitGroup
	var started sync.WaitGroup
	results := make([]any, n)
	started.Add(n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			started.Done()
			v, _, err := c.do(key, 5, true, func() queryCacheResult {
				fills.Add(1)
				<-release
				return queryCacheResult{val: "pools", size: 5, store: true}
			})
			require.NoError(t, err)
			results[i] = v
		}(i)
	}
	started.Wait()
	time.Sleep(20 * time.Millisecond) // let the callers pile up on the leader
	close(release)
	wg.Wait()
	require.Equal(t, int32(1), fills.Load())
	for _, v := range results {
		require.Equal(t, "pools", v)
	}
	require.Equal(t, uint64(1), c.misses.Load())
}

func TestQueryCacheErrorsNotCachedOrShared(t *testing.T) {
	c := testQueryCache(1 << 20)
	c.committed(5, time.Now())
	key := queryCacheKey(queryCacheKindGRPC, 5, "/types.Query/Pool", []byte("x"))
	var fills atomic.Int32
	fail := func() queryCacheResult {
		fills.Add(1)
		return queryCacheResult{err: errors.New("boom")}
	}
	for i := 0; i < 3; i++ {
		_, fromCache, err := c.do(key, 5, false, fail)
		require.Error(t, err)
		require.False(t, fromCache)
	}
	require.Equal(t, int32(3), fills.Load())
	require.Empty(t, c.items)
}

// ---------------------------------------------------------------------------
// app fixtures
// ---------------------------------------------------------------------------

func queryCacheApp(t testing.TB) *THORChainApp {
	app := replayApp(t, dbm.NewMemDB())
	app.EnableQueryCache(64<<20, func() time.Duration { return time.Hour })
	commitReplayGenesis(t, app, replayGenesis(t, app, 1, false))
	return app
}

// nextBlock moves BTC pool depth by delta and commits the next height.
func nextBlock(t require.TestingT, app *THORChainApp, delta uint64) {
	height := app.LastBlockHeight() + 1
	ctx := app.NewUncachedContext(false, cmtproto.Header{ChainID: replayChainID, Height: height})
	pool, err := app.ThorchainKeeper.GetPool(ctx, common.BTCAsset)
	require.NoError(t, err)
	pool.BalanceRune = pool.BalanceRune.Add(cosmos.NewUint(delta))
	require.NoError(t, app.ThorchainKeeper.SetPool(ctx, pool))
	_, err = app.FinalizeBlock(&abci.RequestFinalizeBlock{
		Height: height, Time: replayTime.Add(time.Duration(height) * time.Second),
		Hash: bytes.Repeat([]byte{byte(height)}, 32), NextValidatorsHash: bytes.Repeat([]byte{2}, 32),
	})
	require.NoError(t, err)
	_, err = app.Commit()
	require.NoError(t, err)
}

func abciQuery(t require.TestingT, path string, msg interface{ Marshal() ([]byte, error) }, height int64) *abci.RequestQuery {
	bz, err := msg.Marshal()
	require.NoError(t, err)
	return &abci.RequestQuery{Path: path, Data: bz, Height: height}
}

// uncached runs a query through BaseApp only, bypassing the cache.
func uncached(t require.TestingT, app *THORChainApp, req *abci.RequestQuery) *abci.ResponseQuery {
	r := *req
	resp, err := app.BaseApp.Query(context.Background(), &r)
	require.NoError(t, err)
	require.Zero(t, resp.Code, resp.Log)
	return resp
}

// ---------------------------------------------------------------------------
// ABCI entrypoint
// ---------------------------------------------------------------------------

func TestQueryCacheABCIHitIsIdentical(t *testing.T) {
	app := queryCacheApp(t)
	c := app.queryCache
	req := abciQuery(t, "/types.Query/Pools", &thortypes.QueryPoolsRequest{}, 0)

	first, err := app.Query(context.Background(), req)
	require.NoError(t, err)
	require.Zero(t, first.Code, first.Log)
	require.Equal(t, uint64(1), c.misses.Load())
	second, err := app.Query(context.Background(), abciQuery(t, "/types.Query/Pools", &thortypes.QueryPoolsRequest{}, 0))
	require.NoError(t, err)
	require.Equal(t, uint64(1), c.hits.Load())

	want := uncached(t, app, req)
	require.Equal(t, want, first)
	require.Equal(t, want, second)
	require.Equal(t, app.LastBlockHeight(), second.Height)
	require.NotEmpty(t, second.Value)

	// callers get a copy: mutating one response does not touch the cache
	second.Height = -1
	third, err := app.Query(context.Background(), abciQuery(t, "/types.Query/Pools", &thortypes.QueryPoolsRequest{}, 0))
	require.NoError(t, err)
	require.Equal(t, want, third)
}

func TestQueryCacheABCINewHeightNotStale(t *testing.T) {
	app := queryCacheApp(t)
	h1 := app.LastBlockHeight()
	pool := &thortypes.QueryPoolRequest{Asset: "BTC.BTC"}

	atH1, err := app.Query(context.Background(), abciQuery(t, "/types.Query/Pool", pool, 0))
	require.NoError(t, err)
	require.Equal(t, h1, atH1.Height)

	nextBlock(t, app, 12345)
	h2 := app.LastBlockHeight()
	require.Equal(t, h1+1, h2)

	atH2, err := app.Query(context.Background(), abciQuery(t, "/types.Query/Pool", pool, 0))
	require.NoError(t, err)
	require.Equal(t, h2, atH2.Height)
	require.NotEqual(t, atH1.Value, atH2.Value, "pool changed at the new height")
	require.Equal(t, uncached(t, app, abciQuery(t, "/types.Query/Pool", pool, h2)).Value, atH2.Value)
	require.Equal(t, uint64(0), app.queryCache.hits.Load())

	// explicit heights hit, and the old height still answers with old data
	for i := 0; i < 2; i++ {
		old, qerr := app.Query(context.Background(), abciQuery(t, "/types.Query/Pool", pool, h1))
		require.NoError(t, qerr)
		require.Equal(t, atH1.Value, old.Value)
		require.Equal(t, h1, old.Height)
	}
	require.Equal(t, uint64(1), app.queryCache.hits.Load())
	explicit, err := app.Query(context.Background(), abciQuery(t, "/types.Query/Pool", pool, h2))
	require.NoError(t, err)
	require.Equal(t, atH2.Value, explicit.Value)
	require.Equal(t, uint64(2), app.queryCache.hits.Load(), "explicit latest height shares the latest entry")
}

func TestQueryCacheABCIExcluded(t *testing.T) {
	app := queryCacheApp(t)
	c := app.queryCache
	for _, req := range []*abci.RequestQuery{
		// excluded service
		abciQuery(t, "/cosmos.base.node.v1beta1.Service/Config", &thortypes.QueryPoolsRequest{}, 0),
		// excluded method (errors outside regtest builds)
		abciQuery(t, "/types.Query/Export", &thortypes.QueryExportRequest{}, 0),
		// proofs
		{Path: "/types.Query/Pools", Prove: true},
		// non-gRPC routes
		{Path: "/app/version"},
		{Path: "/store/thorchain/key", Data: []byte("x")},
		// errors from a cacheable method
		abciQuery(t, "/types.Query/Pool", &thortypes.QueryPoolRequest{Asset: "NOPE.NOPE"}, 0),
		// heights not committed yet
		abciQuery(t, "/types.Query/Pools", &thortypes.QueryPoolsRequest{}, app.LastBlockHeight()+1),
	} {
		for i := 0; i < 2; i++ {
			_, err := app.Query(context.Background(), req)
			require.NoError(t, err)
		}
	}
	require.Empty(t, c.items)
	require.Zero(t, c.hits.Load())
}

// TestQueryCacheBypassedInsideStateMachine shows CosmWasm's in-tx query path
// never reads the cache: with a poisoned entry live for the ABCI path, the
// querier the app hands wasmd still reads the store, and consumes the same gas
// as with no cache at all.
func TestQueryCacheBypassedInsideStateMachine(t *testing.T) {
	app := queryCacheApp(t)
	c := app.queryCache
	height := app.LastBlockHeight()
	poolReq := &thortypes.QueryPoolRequest{Asset: "BTC.BTC"}
	abciReq := abciQuery(t, "/types.Query/Pool", poolReq, height)

	poison := &abci.ResponseQuery{Height: height, Value: []byte("poison")}
	c.add(queryCacheKey(queryCacheKindABCI, height, abciReq.Path, abciReq.Data), height, false, 10, poison)
	got, err := app.Query(context.Background(), abciReq)
	require.NoError(t, err)
	require.Equal(t, []byte("poison"), got.Value, "the external path is served from the cache")

	// the querier wasmd gets (app.go: AcceptListGrpcQuerier over GRPCQueryRouter)
	querier := wasmkeeper.AcceptListGrpcQuerier(wasmAcceptedQueries, app.GRPCQueryRouter(), app.appCodec)
	run := func() (*apitypes.QueryPoolResponse, uint64) {
		ctx := app.NewUncachedContext(false, cmtproto.Header{ChainID: replayChainID, Height: height + 1}).
			WithValue(constants.CtxWASMQuery, true)
		before := ctx.GasMeter().GasConsumed()
		resp, err := querier(ctx, &wasmvmtypes.GrpcQuery{Path: abciReq.Path, Data: abciReq.Data})
		require.NoError(t, err)
		out, ok := resp.(*apitypes.QueryPoolResponse)
		require.True(t, ok)
		return out, ctx.GasMeter().GasConsumed() - before
	}
	hits, misses := c.hits.Load(), c.misses.Load()
	withCache, gasWithCache := run()
	require.Equal(t, "BTC.BTC", withCache.Asset)
	require.Equal(t, hits, c.hits.Load())
	require.Equal(t, misses, c.misses.Load())

	app.queryCache = nil
	withoutCache, gasWithoutCache := run()
	app.queryCache = c
	require.Equal(t, withoutCache.String(), withCache.String())
	require.Positive(t, gasWithCache)
	require.Equal(t, gasWithoutCache, gasWithCache)
}

// ---------------------------------------------------------------------------
// gRPC entrypoint
// ---------------------------------------------------------------------------

type capturedService struct {
	desc *grpc.ServiceDesc
	impl any
}

type captureServer struct{ services map[string]capturedService }

func (s *captureServer) RegisterService(sd *grpc.ServiceDesc, impl any) {
	s.services[sd.ServiceName] = capturedService{sd, impl}
}

type headerStream struct {
	mu     sync.Mutex
	header metadata.MD
}

func (s *headerStream) Method() string { return "" }
func (s *headerStream) SetHeader(md metadata.MD) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.header = metadata.Join(s.header, md)
	return nil
}
func (s *headerStream) SendHeader(md metadata.MD) error { return s.SetHeader(md) }
func (s *headerStream) SetTrailer(metadata.MD) error    { return nil }

type grpcCaller func(method string, req interface{ Marshal() ([]byte, error) }, md metadata.MD) (any, metadata.MD, error)

func grpcQueryCaller(t testing.TB, app *THORChainApp) grpcCaller {
	srv := &captureServer{services: map[string]capturedService{}}
	app.RegisterGRPCServerWithSkipCheckHeader(srv, true)
	return func(method string, req interface{ Marshal() ([]byte, error) }, md metadata.MD) (any, metadata.MD, error) {
		svc := srv.services["types.Query"]
		bz, err := req.Marshal()
		require.NoError(t, err)
		for _, m := range svc.desc.Methods {
			if m.MethodName != method {
				continue
			}
			stream := &headerStream{}
			ctx := grpc.NewContextWithServerTransportStream(metadata.NewIncomingContext(context.Background(), md), stream)
			resp, err := m.Handler(svc.impl, ctx, func(v any) error {
				um, ok := v.(interface{ Unmarshal([]byte) error })
				if !ok {
					return fmt.Errorf("unexpected request type %T", v)
				}
				return um.Unmarshal(bz)
			}, nil)
			return resp, stream.header, err
		}
		t.Fatalf("method %s not registered", method)
		return nil, nil, nil
	}
}

func TestQueryCacheGRPC(t *testing.T) {
	app := queryCacheApp(t)
	c := app.queryCache
	call := grpcQueryCaller(t, app)
	h1 := app.LastBlockHeight()
	heightOf := func(md metadata.MD) string {
		v := md.Get(grpctypes.GRPCBlockHeightHeader)
		require.Len(t, v, 1)
		return v[0]
	}

	first, hdr1, err := call("Pools", &thortypes.QueryPoolsRequest{}, metadata.MD{})
	require.NoError(t, err)
	require.Equal(t, fmt.Sprint(h1), heightOf(hdr1))
	second, hdr2, err := call("Pools", &thortypes.QueryPoolsRequest{}, metadata.MD{})
	require.NoError(t, err)
	require.Equal(t, uint64(1), c.hits.Load())
	require.Equal(t, fmt.Sprint(h1), heightOf(hdr2), "hits carry the height header too")

	// byte-identical to the uncached path
	bz1, err := app.appCodec.Marshal(first.(*thortypes.QueryPoolsResponse))
	require.NoError(t, err)
	bz2, err := app.appCodec.Marshal(second.(*thortypes.QueryPoolsResponse))
	require.NoError(t, err)
	require.Equal(t, bz1, bz2)
	require.Equal(t, uncached(t, app, abciQuery(t, "/types.Query/Pools", &thortypes.QueryPoolsRequest{}, h1)).Value, bz1)

	// explicit height header shares the entry
	_, _, err = call("Pools", &thortypes.QueryPoolsRequest{}, metadata.Pairs(grpctypes.GRPCBlockHeightHeader, fmt.Sprint(h1)))
	require.NoError(t, err)
	require.Equal(t, uint64(2), c.hits.Load())

	// the next block is not served the old response
	nextBlock(t, app, 999)
	third, hdr3, err := call("Pools", &thortypes.QueryPoolsRequest{}, metadata.MD{})
	require.NoError(t, err)
	require.Equal(t, fmt.Sprint(h1+1), heightOf(hdr3))
	bz3, err := app.appCodec.Marshal(third.(*thortypes.QueryPoolsResponse))
	require.NoError(t, err)
	require.NotEqual(t, bz1, bz3)
	require.Equal(t, uint64(2), c.hits.Load())

	// excluded methods and errors pass through and are not stored
	n := len(c.items)
	_, _, err = call("Export", &thortypes.QueryExportRequest{}, metadata.MD{})
	require.Error(t, err)
	_, _, err = call("Pool", &thortypes.QueryPoolRequest{Asset: "NOPE.NOPE"}, metadata.MD{})
	require.Error(t, err)
	require.Len(t, c.items, n)

	// a burst of pollers right after a block (run with -race)
	nextBlock(t, app, 1)
	misses := c.misses.Load()
	var wg sync.WaitGroup
	out := make([][]byte, 16)
	for i := range out {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			resp, _, err := call("Pools", &thortypes.QueryPoolsRequest{}, metadata.MD{})
			require.NoError(t, err)
			out[i], err = app.appCodec.Marshal(resp.(*thortypes.QueryPoolsResponse))
			require.NoError(t, err)
		}(i)
	}
	wg.Wait()
	for _, bz := range out {
		require.Equal(t, out[0], bz)
	}
	require.Equal(t, misses+1, c.misses.Load(), "one handler run for the whole burst")
}

// Responses are cached as wire bytes and decoded per caller. Every external
// client already receives a response decoded from the wire: gRPC clients, and
// REST, whose gateway calls the node's own gRPC server through a client
// connection (or ABCI). So a miss and a hit must render the same gateway JSON
// as the uncached response after a wire round trip. (The handler's in-memory
// object can differ: a non-nil empty list renders [] there and null after
// decoding, a difference no client can see.)
func TestQueryCacheGRPCJSONMatchesUncached(t *testing.T) {
	app := queryCacheApp(t)
	call := grpcQueryCaller(t, app)
	jsonpb := &gateway.JSONPb{EmitDefaults: true, OrigName: true, AnyResolver: app.interfaceRegistry}
	toJSON := func(v any) string {
		bz, err := jsonpb.Marshal(v)
		require.NoError(t, err)
		return string(bz)
	}
	for _, q := range []struct {
		method string
		req    interface{ Marshal() ([]byte, error) }
	}{
		{"Pools", &thortypes.QueryPoolsRequest{}},
		{"Pool", &thortypes.QueryPoolRequest{Asset: "BTC.BTC"}},
		{"Network", &thortypes.QueryNetworkRequest{}},
		{"Nodes", &thortypes.QueryNodesRequest{}},
		{"AsgardVaults", &thortypes.QueryAsgardVaultsRequest{}},
		{"LiquidityProviders", &thortypes.QueryLiquidityProvidersRequest{Asset: "BTC.BTC"}},
	} {
		miss, _, err := call(q.method, q.req, metadata.MD{})
		require.NoError(t, err, q.method)
		hits := app.queryCache.hits.Load()
		hit, _, err := call(q.method, q.req, metadata.MD{})
		require.NoError(t, err, q.method)
		require.Equal(t, hits+1, app.queryCache.hits.Load(), q.method)
		require.NotSame(t, miss, hit, "%s: each caller decodes its own message", q.method)

		c := app.queryCache
		app.queryCache = nil
		want, _, err := call(q.method, q.req, metadata.MD{})
		app.queryCache = c
		require.NoError(t, err, q.method)

		wantMsg, ok := want.(interface{ Marshal() ([]byte, error) })
		require.True(t, ok, q.method)
		wantBz, err := wantMsg.Marshal()
		require.NoError(t, err, q.method)
		wire, ok := reflect.New(reflect.TypeOf(want).Elem()).Interface().(interface{ Unmarshal([]byte) error })
		require.True(t, ok, q.method)
		require.NoError(t, wire.Unmarshal(wantBz), q.method)

		require.Equal(t, toJSON(wire), toJSON(miss), q.method)
		require.Equal(t, toJSON(wire), toJSON(hit), q.method)
	}
}

// A caller changing its response does not change what later callers get, and
// an entry is charged its wire size.
func TestQueryCacheGRPCHitsAreIndependent(t *testing.T) {
	app := queryCacheApp(t)
	call := grpcQueryCaller(t, app)
	first, _, err := call("Pools", &thortypes.QueryPoolsRequest{}, metadata.MD{})
	require.NoError(t, err)
	pools, ok := first.(*thortypes.QueryPoolsResponse)
	require.True(t, ok)
	require.NotEmpty(t, pools.Pools)
	want, err := pools.Marshal()
	require.NoError(t, err)
	pools.Pools[0].Asset = "MUTATED.MUTATED"

	second, _, err := call("Pools", &thortypes.QueryPoolsRequest{}, metadata.MD{})
	require.NoError(t, err)
	secondPools, ok := second.(*thortypes.QueryPoolsResponse)
	require.True(t, ok)
	got, err := secondPools.Marshal()
	require.NoError(t, err)
	require.Equal(t, want, got)

	c := app.queryCache
	c.mu.Lock()
	defer c.mu.Unlock()
	require.Len(t, c.items, 1)
	for _, e := range c.items {
		ent := entryOf(e)
		require.Equal(t, int64(len(want)+len(ent.key)+queryCacheEntryOverhead), ent.size)
	}
}

// ---------------------------------------------------------------------------
// benchmark
// ---------------------------------------------------------------------------

// BenchmarkQueryCache compares a cache miss (BaseApp only) with a hit for the
// pools, network and nodes ABCI queries on the replay fixture (four pools, one
// node; inbound_addresses errors there for want of vaults on every chain).
func BenchmarkQueryCache(b *testing.B) {
	app := queryCacheApp(b)
	for _, q := range []struct {
		name string
		req  *abci.RequestQuery
	}{
		{"pools", abciQuery(b, "/types.Query/Pools", &thortypes.QueryPoolsRequest{}, 0)},
		{"network", abciQuery(b, "/types.Query/Network", &thortypes.QueryNetworkRequest{}, 0)},
		{"nodes", abciQuery(b, "/types.Query/Nodes", &thortypes.QueryNodesRequest{}, 0)},
	} {
		b.Run(q.name+"/miss", func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				r := *q.req
				if resp, err := app.BaseApp.Query(context.Background(), &r); err != nil || resp.Code != 0 {
					b.Fatal(err, resp.Log)
				}
			}
		})
		b.Run(q.name+"/hit", func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				r := *q.req
				if _, err := app.Query(context.Background(), &r); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
