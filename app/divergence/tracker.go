package divergence

import (
	"context"
	"sync"
	"sync/atomic"

	gogogrpc "github.com/cosmos/gogoproto/grpc"
	"google.golang.org/grpc"

	sdk "github.com/cosmos/cosmos-sdk/types"
)

// Overlap counts Simulate calls and queries, and how many of them started while
// a block was executing (FinalizeBlock start through Commit end), so a
// divergence can be correlated with the node-local traffic around it.
type Overlap struct {
	Simulate        uint64 `json:"simulate"`
	SimulateOverlap uint64 `json:"simulate_overlap"`
	// SimulateInflightAtBegin counts Simulate calls already running when a
	// block's FinalizeBlock started.
	SimulateInflightAtBegin uint64 `json:"simulate_inflight_at_begin"`
	Query                   uint64 `json:"query"`
	QueryOverlap            uint64 `json:"query_overlap"`
}

func (o *Overlap) add(other Overlap) {
	o.Simulate += other.Simulate
	o.SimulateOverlap += other.SimulateOverlap
	o.SimulateInflightAtBegin += other.SimulateInflightAtBegin
	o.Query += other.Query
	o.QueryOverlap += other.QueryOverlap
}

// Tracker counts Simulate calls and queries against block execution. All
// methods are safe on a nil *Tracker (they do nothing), so callers need no
// enabled check. Counters are approximate under contention, by design: they
// must never add a lock to the block-execution path.
type Tracker struct {
	inBlock     atomic.Bool
	simInflight atomic.Int64

	simulate        atomic.Uint64
	simulateOverlap atomic.Uint64
	simInflightAtBg atomic.Uint64
	query           atomic.Uint64
	queryOverlap    atomic.Uint64

	mu    sync.Mutex
	total Overlap
}

// NewTracker returns an idle tracker.
func NewTracker() *Tracker { return &Tracker{} }

// BeginBlock marks the start of block execution (FinalizeBlock entry).
func (t *Tracker) BeginBlock() {
	if t == nil {
		return
	}
	t.inBlock.Store(true)
	if n := t.simInflight.Load(); n > 0 {
		t.simInflightAtBg.Add(uint64(n))
	}
}

// EndBlock marks the end of block execution (Commit return).
func (t *Tracker) EndBlock() {
	if t == nil {
		return
	}
	t.inBlock.Store(false)
}

// InBlock reports whether a block is executing.
func (t *Tracker) InBlock() bool {
	return t != nil && t.inBlock.Load()
}

// simulateStart records a Simulate call and returns its completion func.
func (t *Tracker) simulateStart() func() {
	if t == nil {
		return func() {}
	}
	t.simInflight.Add(1)
	t.simulate.Add(1)
	if t.inBlock.Load() {
		t.simulateOverlap.Add(1)
	}
	return func() { t.simInflight.Add(-1) }
}

// QueryStart records the start of a query.
func (t *Tracker) QueryStart() {
	if t == nil {
		return
	}
	t.query.Add(1)
	if t.inBlock.Load() {
		t.queryOverlap.Add(1)
	}
}

// TakeBlock returns the counts since the previous TakeBlock, resets them, and
// adds them to the process totals. The recorder calls it from ListenCommit, so
// a call starting in the short window between ListenCommit and Commit's return
// is attributed to the next block.
func (t *Tracker) TakeBlock() Overlap {
	if t == nil {
		return Overlap{}
	}
	o := Overlap{
		Simulate:                t.simulate.Swap(0),
		SimulateOverlap:         t.simulateOverlap.Swap(0),
		SimulateInflightAtBegin: t.simInflightAtBg.Swap(0),
		Query:                   t.query.Swap(0),
		QueryOverlap:            t.queryOverlap.Swap(0),
	}
	t.mu.Lock()
	t.total.add(o)
	t.mu.Unlock()
	return o
}

// Totals returns the process-lifetime counts, including the not yet taken
// counts of the current block.
func (t *Tracker) Totals() Overlap {
	if t == nil {
		return Overlap{}
	}
	t.mu.Lock()
	o := t.total
	t.mu.Unlock()
	o.add(Overlap{
		Simulate:                t.simulate.Load(),
		SimulateOverlap:         t.simulateOverlap.Load(),
		SimulateInflightAtBegin: t.simInflightAtBg.Load(),
		Query:                   t.query.Load(),
		QueryOverlap:            t.queryOverlap.Load(),
	})
	return o
}

// SimulateFunc is the BaseApp.Simulate signature authtx.RegisterTxService takes.
type SimulateFunc = func(txBytes []byte) (sdk.GasInfo, *sdk.Result, error)

// WrapSimulate counts calls to f. With a nil tracker it returns f unchanged.
func (t *Tracker) WrapSimulate(f SimulateFunc) SimulateFunc {
	if t == nil {
		return f
	}
	return func(txBytes []byte) (sdk.GasInfo, *sdk.Result, error) {
		defer t.simulateStart()()
		return f(txBytes)
	}
}

// WrapGRPCServer counts every unary gRPC query registered on s. With a nil
// tracker it returns s unchanged.
func (t *Tracker) WrapGRPCServer(s gogogrpc.Server) gogogrpc.Server {
	if t == nil {
		return s
	}
	return countingServer{Server: s, t: t}
}

type countingServer struct {
	gogogrpc.Server
	t *Tracker
}

func (s countingServer) RegisterService(sd *grpc.ServiceDesc, handler interface{}) {
	wrapped := *sd
	wrapped.Methods = make([]grpc.MethodDesc, len(sd.Methods))
	for i, method := range sd.Methods {
		inner := method.Handler
		wrapped.Methods[i] = grpc.MethodDesc{
			MethodName: method.MethodName,
			Handler: func(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
				s.t.QueryStart()
				return inner(srv, ctx, dec, interceptor)
			},
		}
	}
	s.Server.RegisterService(&wrapped, handler)
}
