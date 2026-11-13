package app

import (
	"container/list"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	abci "github.com/cometbft/cometbft/abci/types"
	"github.com/cosmos/cosmos-sdk/telemetry"
	grpctypes "github.com/cosmos/cosmos-sdk/types/grpc"
	"golang.org/x/sync/singleflight"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

// Node-local response cache for external read queries.
//
// It wraps exactly two entrypoints, both of which only serve clients outside
// the state machine:
//
//   - THORChainApp.Query, the ABCI query handler (CometBFT RPC abci_query, and
//     REST when api.query-via-grpc is off or the gRPC server is disabled);
//   - the gRPC server registration (abciParityServer), which serves external
//     gRPC and REST via the in-process grpc-gateway loopback.
//
// Queries made from inside the state machine (CosmWasm contracts querying
// through wasmkeeper.AcceptListGrpcQuerier / the Stargate plugin, module code
// calling keepers) resolve handlers from BaseApp.GRPCQueryRouter() directly and
// never pass through either entrypoint, so their store reads, and the gas they
// meter, are untouched.
//
// Entries are keyed on (entrypoint, height, method, request bytes). A request
// without a height resolves to the latest committed height before lookup, so a
// new block always selects new keys and can never be served the previous
// block's response. The cache only serves or stores heights marked ready by the
// end-of-Commit hook (PrepareCheckStater), by which point the query context for
// that height (store version, commit-info block time, check-state header) is
// settled; explicit heights above it go to the handler uncached, as before.
type queryCache struct {
	maxBytes int64
	// maxLag returns the quote stale-state lag (thor.api.quote.max_lag)
	maxLag func() time.Duration
	now    func() time.Time

	mu    sync.Mutex
	ll    *list.List // front = most recently used
	items map[string]*list.Element
	bytes int64
	// latestEls indexes the entries stored for a request without a height, so
	// committed() sweeps only those instead of walking the whole LRU list
	latestEls map[*list.Element]struct{}

	sf    singleflight.Group
	ready atomic.Pointer[queryCacheReady]

	hits, misses, evictions atomic.Uint64
}

type queryCacheReady struct {
	height    int64
	blockTime time.Time
}

type queryCacheEntry struct {
	key    string
	height int64
	latest bool // stored by a request that resolved "latest"
	size   int64
	val    any
}

// per-entry bookkeeping charged against the byte cap on top of key and value
const queryCacheEntryOverhead = 160

const (
	queryCacheKindABCI byte = 'a'
	queryCacheKindGRPC byte = 'g'
)

type queryCachePolicy int

const (
	queryCacheNever queryCachePolicy = iota
	queryCacheAlways
	// responses that also depend on wall-clock time: the quote endpoints refuse
	// to answer when the latest block is older than thor.api.quote.max_lag, so
	// they are only served from cache at the latest height while that block is
	// fresh by the same rule
	queryCacheWhileFresh
)

// Services whose query handlers are a pure function of (height, request):
// store reads at the query height plus process-static config.
var queryCacheServices = map[string]bool{
	"types.Query":                  true,
	"cosmwasm.wasm.v1.Query":       true,
	"cosmos.bank.v1beta1.Query":    true,
	"cosmos.auth.v1beta1.Query":    true,
	"thorchain.denom.v1.Query":     true,
	"thorchain.scheduler.v1.Query": true,
}

// Methods of the services above that are not a pure function of (height,
// request).
var queryCacheExcludedMethods = map[string]bool{
	// reads CometBFT block data in-process, not app state at the height
	"/types.Query/Block": true,
	// regtest state dump; large and not polled
	"/types.Query/Export": true,
	// sign the response with the node keyring
	"/types.Query/Keysign":       true,
	"/types.Query/KeysignPubkey": true,
	"/types.Query/Keygen":        true,
}

var queryCacheWallClockMethods = map[string]bool{
	"/types.Query/QuoteSwap":  true,
	"/types.Query/QuoteLimit": true,
}

// queryCachePolicyFor returns the policy for a gRPC full method name
// ("/package.Service/Method"), which is also the ABCI query path for gRPC
// routes.
func queryCachePolicyFor(fullMethod string) queryCachePolicy {
	if len(fullMethod) < 2 || fullMethod[0] != '/' {
		return queryCacheNever
	}
	i := strings.IndexByte(fullMethod[1:], '/')
	if i < 0 {
		return queryCacheNever
	}
	if !queryCacheServices[fullMethod[1:1+i]] || queryCacheExcludedMethods[fullMethod] {
		return queryCacheNever
	}
	if queryCacheWallClockMethods[fullMethod] {
		return queryCacheWhileFresh
	}
	return queryCacheAlways
}

func newQueryCache(maxBytes int64, maxLag func() time.Duration) *queryCache {
	return &queryCache{
		maxBytes: maxBytes,
		maxLag:   maxLag,
		now:      time.Now,
		ll:       list.New(),
		items:    make(map[string]*list.Element),

		latestEls: make(map[*list.Element]struct{}),
	}
}

// committed marks height as served-from-cache eligible and drops entries that
// were stored for an older "latest" height.
func (c *queryCache) committed(height int64, blockTime time.Time) {
	c.ready.Store(&queryCacheReady{height: height, blockTime: blockTime})
	c.mu.Lock()
	defer c.mu.Unlock()
	// removing the current key while ranging is safe; order does not matter
	for e := range c.latestEls {
		if entryOf(e).height < height {
			c.removeLocked(e)
		}
	}
}

// latest is the height a request without one resolves to: the last block
// whose Commit has finished (0 before the first). Between the store committing
// a block and Commit returning, this is still the previous height, consistent
// with what CometBFT reports until it has processed the commit.
func (c *queryCache) latest() int64 {
	if r := c.ready.Load(); r != nil {
		return r.height
	}
	return 0
}

// usable reports whether a response at height may be read from or written to
// the cache now.
func (c *queryCache) usable(p queryCachePolicy, height int64) bool {
	if p == queryCacheNever || height <= 0 {
		return false
	}
	r := c.ready.Load()
	if r == nil || height > r.height {
		return false
	}
	if p == queryCacheWhileFresh {
		// mirrors the quote guard: stale iff blockTime < now - maxLag
		if height != r.height || r.blockTime.Before(c.now().Add(-c.maxLag())) {
			return false
		}
	}
	return true
}

func queryCacheKey(kind byte, height int64, method string, req []byte) string {
	var b strings.Builder
	b.Grow(1 + 8 + len(method) + 1 + len(req))
	b.WriteByte(kind)
	var h [8]byte
	binary.BigEndian.PutUint64(h[:], uint64(height))
	b.Write(h[:])
	b.WriteString(method)
	b.WriteByte(0)
	b.Write(req)
	return b.String()
}

func (c *queryCache) get(key string) (any, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.items[key]
	if !ok {
		return nil, false
	}
	c.ll.MoveToFront(e)
	return entryOf(e).val, true
}

func (c *queryCache) add(key string, height int64, latest bool, size int64, val any) {
	size += int64(len(key)) + queryCacheEntryOverhead
	// one response may not take more than a quarter of the cache
	if size > c.maxBytes/4 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if r := c.ready.Load(); latest && (r == nil || height < r.height) {
		// a block committed while this response was computed; committed()
		// already swept, so don't leave an orphan behind
		return
	}
	if e, ok := c.items[key]; ok {
		c.removeLocked(e)
	}
	e := c.ll.PushFront(&queryCacheEntry{key: key, height: height, latest: latest, size: size, val: val})
	c.items[key] = e
	if latest {
		c.latestEls[e] = struct{}{}
	}
	c.bytes += size
	for c.bytes > c.maxBytes {
		c.removeLocked(c.ll.Back())
		c.evictions.Add(1)
	}
}

func entryOf(e *list.Element) *queryCacheEntry {
	ent, _ := e.Value.(*queryCacheEntry) // only *queryCacheEntry is stored
	return ent
}

func (c *queryCache) removeLocked(e *list.Element) {
	ent := entryOf(e)
	c.ll.Remove(e)
	delete(c.items, ent.key)
	delete(c.latestEls, e)
	c.bytes -= ent.size
}

// queryCacheResult is what a fill function produces: the response, its size
// in bytes, and whether it may be stored (successful responses only).
type queryCacheResult struct {
	val    any
	size   int64
	store  bool
	cached bool // found in the cache rather than computed
	err    error
}

// do returns the cached response for key, or runs fill once across concurrent
// callers of the same key and stores a storable result. fromCache is true when
// the response was not computed by this caller (a hit, or a shared result).
// A caller that waited on a result that could not be stored (an error) runs
// fill itself, so errors are never shared and never cached.
func (c *queryCache) do(key string, height int64, latest bool, fill func() queryCacheResult) (val any, fromCache bool, err error) {
	if v, ok := c.get(key); ok {
		c.hits.Add(1)
		telemetry.IncrCounter(1, "query_cache", "hit")
		return v, true, nil
	}
	led := false
	v, _, _ := c.sf.Do(key, func() (any, error) {
		led = true
		// a previous leader may have stored it between get and Do
		if v, ok := c.get(key); ok {
			return queryCacheResult{val: v, store: true, cached: true}, nil
		}
		c.misses.Add(1)
		telemetry.IncrCounter(1, "query_cache", "miss")
		res := fill()
		if res.store && res.err == nil {
			c.add(key, height, latest, res.size, res.val)
		}
		return res, nil
	})
	res, _ := v.(queryCacheResult) // the fill wrapper always returns one
	if led {
		if res.cached {
			c.hits.Add(1)
			telemetry.IncrCounter(1, "query_cache", "hit")
		}
		return res.val, res.cached, res.err
	}
	if !res.store || res.err != nil {
		res = fill()
		return res.val, false, res.err
	}
	c.hits.Add(1)
	telemetry.IncrCounter(1, "query_cache", "hit")
	return res.val, true, nil
}

// ---------------------------------------------------------------------------
// ABCI entrypoint
// ---------------------------------------------------------------------------

func abciResponseSize(r *abci.ResponseQuery) int64 {
	n := len(r.Log) + len(r.Info) + len(r.Key) + len(r.Value) + len(r.Codespace) + 64
	if r.ProofOps != nil {
		n += r.ProofOps.Size()
	}
	return int64(n)
}

// Query overrides BaseApp.Query (the ABCI query handler) to serve repeated
// gRPC-route queries for one height from the query cache. Every query, cached
// or not, is first counted for the divergence overlap counters.
func (app *THORChainApp) Query(ctx context.Context, req *abci.RequestQuery) (*abci.ResponseQuery, error) {
	app.divergence.Tracker().QueryStart()
	c := app.queryCache
	if c == nil || req.Prove || req.Height < 0 {
		return app.BaseApp.Query(ctx, req)
	}
	p := queryCachePolicyFor(req.Path)
	if p == queryCacheNever || app.GRPCQueryRouter().Route(req.Path) == nil {
		return app.BaseApp.Query(ctx, req)
	}
	latest := req.Height == 0
	height := req.Height
	if latest {
		height = c.latest()
	}
	if !c.usable(p, height) {
		return app.BaseApp.Query(ctx, req)
	}
	key := queryCacheKey(queryCacheKindABCI, height, req.Path, req.Data)
	val, _, err := c.do(key, height, latest, func() queryCacheResult {
		r := *req
		r.Height = height
		resp, err := app.BaseApp.Query(ctx, &r)
		if err != nil || resp == nil || resp.Code != 0 {
			return queryCacheResult{val: resp, err: err}
		}
		return queryCacheResult{val: resp, size: abciResponseSize(resp), store: true}
	})
	resp, _ := val.(*abci.ResponseQuery)
	if resp != nil {
		// every caller gets its own struct; a cached one stays untouched
		cp := *resp
		resp = &cp
	}
	return resp, err
}

// ---------------------------------------------------------------------------
// gRPC entrypoint
// ---------------------------------------------------------------------------

// errQueryCacheCaptured stops a gRPC method handler right after it decoded its
// request, before the BaseApp interceptor builds a query context.
var errQueryCacheCaptured = errors.New("query cache: request captured")

// resolveGRPCQueryHeight mirrors the BaseApp gRPC interceptor's reading of the
// height header: exactly one value is an explicit height, anything else means
// latest. ok is false for a header BaseApp would reject.
func resolveGRPCQueryHeight(md metadata.MD, lastHeight int64) (height int64, latest, ok bool) {
	if hs := md.Get(grpctypes.GRPCBlockHeightHeader); len(hs) == 1 {
		h, err := strconv.ParseInt(hs[0], 10, 64)
		if err != nil || h < 0 {
			return 0, false, false
		}
		if h > 0 {
			return h, false, true
		}
	}
	return lastHeight, true, true
}

// serveGRPC serves one gRPC query through the cache. inner is the BaseApp
// wrapped method handler.
func (c *queryCache) serveGRPC(
	ctx context.Context, p queryCachePolicy, fullMethod string,
	inner grpc.MethodHandler, srv any, dec func(any) error, interceptor grpc.UnaryServerInterceptor,
) (any, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return inner(srv, ctx, dec, interceptor)
	}
	height, latest, ok := resolveGRPCQueryHeight(md, c.latest())
	if !ok || !c.usable(p, height) {
		return inner(srv, ctx, dec, interceptor)
	}

	// Run the handler only as far as decoding the request.
	var in any
	resp, err := inner(srv, ctx, func(v any) error {
		if err := dec(v); err != nil {
			return err
		}
		in = v
		return errQueryCacheCaptured
	}, interceptor)
	if !errors.Is(err, errQueryCacheCaptured) {
		return resp, err // decode error (or a handler that didn't decode)
	}
	replay := func(v any) error {
		dst, src := reflect.ValueOf(v), reflect.ValueOf(in)
		if dst.Type() != src.Type() || dst.Kind() != reflect.Pointer {
			return fmt.Errorf("query cache: request type %s, handler wants %s", src.Type(), dst.Type())
		}
		dst.Elem().Set(src.Elem())
		return nil
	}
	m, ok := in.(interface{ Marshal() ([]byte, error) })
	if !ok {
		return inner(srv, ctx, replay, interceptor)
	}
	reqBz, err := m.Marshal()
	if err != nil {
		return inner(srv, ctx, replay, interceptor)
	}

	ictx := ctx
	if latest {
		// pin the handler to the height the key was built for
		md = md.Copy()
		md.Set(grpctypes.GRPCBlockHeightHeader, strconv.FormatInt(height, 10))
		ictx = metadata.NewIncomingContext(ctx, md)
	}
	key := queryCacheKey(queryCacheKindGRPC, height, fullMethod, reqBz)
	val, fromCache, err := c.do(key, height, latest, func() queryCacheResult {
		resp, herr := inner(srv, ictx, replay, interceptor)
		if herr != nil {
			return queryCacheResult{val: resp, err: herr}
		}
		enc, ok := encodeGRPCResponse(resp)
		if !ok {
			return queryCacheResult{val: resp}
		}
		return queryCacheResult{val: enc, size: int64(len(enc.bz)), store: true}
	})
	if err != nil {
		return val, err
	}
	if fromCache {
		// the BaseApp interceptor sets this on every query it runs
		_ = grpc.SetHeader(ctx, metadata.Pairs(grpctypes.GRPCBlockHeightHeader, strconv.FormatInt(height, 10)))
	}
	if enc, ok := val.(*grpcCachedResponse); ok {
		return enc.decode()
	}
	return val, nil
}

// grpcCachedResponse is a gRPC response as stored: its wire bytes, so every
// caller decodes its own message (nothing it changes reaches another caller)
// and the entry's size is what it really holds.
type grpcCachedResponse struct {
	typ reflect.Type // the response's pointer type
	bz  []byte
}

type gogoMessage interface {
	Marshal() ([]byte, error)
	Unmarshal([]byte) error
}

func encodeGRPCResponse(resp any) (*grpcCachedResponse, bool) {
	m, ok := resp.(gogoMessage)
	if !ok || reflect.TypeOf(resp).Kind() != reflect.Pointer {
		return nil, false
	}
	bz, err := m.Marshal()
	if err != nil {
		return nil, false
	}
	return &grpcCachedResponse{typ: reflect.TypeOf(resp), bz: bz}, true
}

func (r *grpcCachedResponse) decode() (any, error) {
	v := reflect.New(r.typ.Elem()).Interface()
	m, _ := v.(gogoMessage) // r.typ came from a value implementing it
	if err := m.Unmarshal(r.bz); err != nil {
		return nil, fmt.Errorf("query cache: decode %s: %w", r.typ, err)
	}
	return v, nil
}

// ---------------------------------------------------------------------------
// wiring
// ---------------------------------------------------------------------------

// EnableQueryCache turns on the node-local query response cache, bounded to
// maxBytes. maxLag is the quote stale-state lag (thor.api.quote.max_lag). It
// must be called before the app serves queries.
func (app *THORChainApp) EnableQueryCache(maxBytes int64, maxLag func() time.Duration) {
	if maxBytes <= 0 {
		return
	}
	app.queryCache = newQueryCache(maxBytes, maxLag)
}
