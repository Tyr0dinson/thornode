package solana

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rs/zerolog"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/bifrost/blockscanner"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/endpoints"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/solana/rpc"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/config"
)

// pagingNode answers getSignaturesForAddress like a Solana node: it has total
// signatures newest first, listed transactionQueryLimit per page; a "before" it does
// not know gives an empty page (Agave's behaviour). failBefore makes its next paged
// request fail once with HTTP 502, failPaged every paged request. Every transaction
// failed on chain, so the scan skips them without observing anything.
type pagingNode struct {
	sigs       []string
	failBefore atomic.Bool
	failPaged  atomic.Bool
	listings   atomic.Int32
}

func (n *pagingNode) handler(rw http.ResponseWriter, req *http.Request) {
	body, _ := io.ReadAll(req.Body)
	var call struct {
		Method string            `json:"method"`
		Params []json.RawMessage `json:"params"`
	}
	_ = json.Unmarshal(body, &call)
	rw.Header().Set("Content-Type", "application/json")
	switch call.Method {
	case "getSignaturesForAddress":
		var opts struct {
			Before string `json:"before"`
		}
		if len(call.Params) > 1 {
			_ = json.Unmarshal(call.Params[1], &opts)
		}
		if opts.Before != "" && (n.failBefore.CompareAndSwap(true, false) || n.failPaged.Load()) {
			rw.WriteHeader(http.StatusBadGateway)
			return
		}
		n.listings.Add(1)
		start := 0
		if opts.Before != "" {
			start = -1
			for i, s := range n.sigs {
				if s == opts.Before {
					start = i + 1
				}
			}
		}
		var page []string
		if start >= 0 {
			for i := start; i < len(n.sigs) && len(page) < transactionQueryLimit; i++ {
				page = append(page, fmt.Sprintf(`{"signature":"%s","slot":%d}`, n.sigs[i], 5000-i))
			}
		}
		_, _ = rw.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":[` + strings.Join(page, ",") + `]}`))
	case "getTransaction":
		_, _ = rw.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"slot":1,"transaction":{"signatures":["x"],"message":{"accountKeys":[],"instructions":[]}},"meta":{"err":{"InstructionError":[0,"Custom"]}}}}`))
	default:
		_, _ = rw.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":null}`))
	}
}

// A vault with more than one page of new signatures is listed by one endpoint. Here
// page 2 of the routed listing would land on a node without page 1's last signature
// (after a transient failure on the first node), which answers an empty page: the
// scan would end early and move the cursor past 5 signatures it never read.
func (s *SolTestSuite) TestVaultCatchUpListsEveryPageFromOneEndpoint(c *C) {
	sigs := make([]string, transactionQueryLimit+5)
	for i := range sigs {
		sigs[i] = fmt.Sprintf("sig%04d", i)
	}
	a := &pagingNode{sigs: sigs}
	b := &pagingNode{sigs: sigs[:transactionQueryLimit]} // never saw the older 5
	sa, sb := httptest.NewServer(http.HandlerFunc(a.handler)), httptest.NewServer(http.HandlerFunc(b.handler))
	defer sa.Close()
	defer sb.Close()
	eps := []config.ChainEndpointConfig{{Name: "a", RPCHost: sa.URL, Priority: 1}, {Name: "b", RPCHost: sb.URL, Priority: 2}}
	cfg := config.BifrostChainConfiguration{ChainID: common.SOLChain}
	mgr, err := endpoints.NewManager(common.SOLChain, eps, cfg.GetFailoverConfig(), zerolog.Nop())
	c.Assert(err, IsNil)
	for _, e := range mgr.Entries() {
		e.SetIdentity("genesis")
	}
	targets, err := endpoints.TargetsFrom(eps, endpoints.RPCHost)
	c.Assert(err, IsNil)
	router, err := endpoints.NewHTTPRouter(mgr, endpoints.HTTPRouterConfig{Base: endpoints.SyntheticBase(common.SOLChain), Targets: targets}, zerolog.Nop())
	c.Assert(err, IsNil)
	scanner := &SOLScanner{
		logger: zerolog.Nop(),
		solRpc: rpc.NewSolRPCWithClient(endpoints.SyntheticBase(common.SOLChain).String(), 2*time.Second, &http.Client{Transport: router, Timeout: 2 * time.Second}),
		pinEndpoint: func(int) string {
			return mgr.ReadCandidates()[0].Name
		},
	}
	scan := func() (ok bool, count int, cursor string) {
		txCounts, oks, cursors := make([]int, 1), make([]bool, 1), make([]string, 1)
		var wg sync.WaitGroup
		wg.Add(1)
		scanner.scanVault("vault", 0, "", 0, 0, func() {}, txCounts, oks, cursors, make([]uint64, 1), &wg)
		wg.Wait()
		return oks[0], txCounts[0], cursors[0]
	}

	// a fails its first page-2 request: the pinned listing does not move to b, the
	// scan fails and keeps the cursor
	a.failBefore.Store(true)
	ok, _, _ := scan()
	c.Assert(ok, Equals, false)
	c.Assert(b.listings.Load(), Equals, int32(0))
	// the next round lists every page from a and reads all 1005 signatures
	ok, count, cursor := scan()
	c.Assert(ok, Equals, true)
	c.Assert(count, Equals, transactionQueryLimit+5)
	c.Assert(cursor, Equals, "sig0000")
	c.Assert(b.listings.Load(), Equals, int32(0))
}

// agaveNode answers getSignaturesForAddress at finalized commitment as Agave does: a
// minContextSlot above its root is refused with -32016, and an "until" it does not
// know is ignored, so the whole history it holds is listed. sigs are newest first;
// "sigNNNN" is at slot 5000-NNNN on every node; it has finalized up to root.
type agaveNode struct {
	sigs     []string
	root     uint64
	listings atomic.Int32
}

func (n *agaveNode) handler(rw http.ResponseWriter, req *http.Request) {
	body, _ := io.ReadAll(req.Body)
	var call struct {
		Method string            `json:"method"`
		Params []json.RawMessage `json:"params"`
	}
	_ = json.Unmarshal(body, &call)
	rw.Header().Set("Content-Type", "application/json")
	switch call.Method {
	case "getSignaturesForAddress":
		var opts struct {
			Until          string `json:"until"`
			MinContextSlot uint64 `json:"minContextSlot"`
		}
		if len(call.Params) > 1 {
			_ = json.Unmarshal(call.Params[1], &opts)
		}
		if opts.MinContextSlot > n.root {
			_, _ = rw.Write([]byte(fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"error":{"code":-32016,"message":"Minimum context slot has not been reached","data":{"contextSlot":%d}}}`, n.root)))
			return
		}
		n.listings.Add(1)
		var page []string
		for _, s := range n.sigs {
			if s == opts.Until || len(page) == transactionQueryLimit {
				break
			}
			var num uint64
			_, _ = fmt.Sscanf(s, "sig%d", &num)
			if slot := 5000 - num; slot <= n.root {
				page = append(page, fmt.Sprintf(`{"signature":"%s","slot":%d}`, s, slot))
			}
		}
		_, _ = rw.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":[` + strings.Join(page, ",") + `]}`))
	case "getTransaction":
		_, _ = rw.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"slot":1,"transaction":{"signatures":["x"],"message":{"accountKeys":[],"instructions":[]}},"meta":{"err":{"InstructionError":[0,"Custom"]}}}}`))
	default:
		_, _ = rw.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":null}`))
	}
}

// After a failover to a node that has not finalized the cursor's slot, the listing
// must not run past the cursor: Agave ignores an "until" it does not know and would
// list the vault's whole history. The cursor's slot is sent as minContextSlot, so
// the lagging node refuses (-32016, an endpoint error on Solana) and the read moves
// to a node that has the cursor, which lists only the new signatures.
func (s *SolTestSuite) TestCursorListingDoesNotRunPastTheCursorOnALaggingNode(c *C) {
	sigs := make([]string, 30)
	for i := range sigs {
		sigs[i] = fmt.Sprintf("sig%04d", i)
	}
	// the cursor is sig0010 at slot 4990; sig0000..sig0009 are new
	behind := &agaveNode{sigs: sigs[11:], root: 4985} // lags: never saw the cursor
	ahead := &agaveNode{sigs: sigs, root: 5000}
	sb, sa := httptest.NewServer(http.HandlerFunc(behind.handler)), httptest.NewServer(http.HandlerFunc(ahead.handler))
	defer sb.Close()
	defer sa.Close()
	eps := []config.ChainEndpointConfig{{Name: "behind", RPCHost: sb.URL, Priority: 1}, {Name: "ahead", RPCHost: sa.URL, Priority: 2}}
	cfg := config.BifrostChainConfiguration{ChainID: common.SOLChain}
	mgr, err := endpoints.NewManager(common.SOLChain, eps, cfg.GetFailoverConfig(), zerolog.Nop())
	c.Assert(err, IsNil)
	for _, e := range mgr.Entries() {
		e.SetIdentity("genesis")
	}
	targets, err := endpoints.TargetsFrom(eps, endpoints.RPCHost)
	c.Assert(err, IsNil)
	router, err := endpoints.NewHTTPRouter(mgr, endpoints.HTTPRouterConfig{Base: endpoints.SyntheticBase(common.SOLChain), Targets: targets, EndpointErrorCodes: solEndpointErrorCodes}, zerolog.Nop())
	c.Assert(err, IsNil)
	c.Assert(mgr.Active().Name, Equals, "behind")
	scanner := &SOLScanner{
		logger: zerolog.Nop(),
		solRpc: rpc.NewSolRPCWithClient(endpoints.SyntheticBase(common.SOLChain).String(), 2*time.Second, &http.Client{Transport: router, Timeout: 2 * time.Second}),
	}
	txCounts, oks, cursors, slots := make([]int, 1), make([]bool, 1), make([]string, 1), make([]uint64, 1)
	var wg sync.WaitGroup
	wg.Add(1)
	scanner.scanVault("vault", 0, "sig0010", 4990, 4990, func() {}, txCounts, oks, cursors, slots, &wg)
	wg.Wait()
	c.Assert(oks[0], Equals, true)
	c.Assert(txCounts[0], Equals, 10)
	c.Assert(cursors[0], Equals, "sig0000")
	c.Assert(slots[0], Equals, uint64(5000))
	c.Assert(behind.listings.Load(), Equals, int32(0))
}

// The cursor's slot is kept with the cursor, in memory and in its own storage key,
// and read back after a restart; a status stored before it existed reads as slot 0
// (no minContextSlot, as before).
func (s *SolTestSuite) TestVaultCursorSlotIsKept(c *C) {
	storage, err := blockscanner.NewBlockScannerStorageSolana("", config.LevelDBOptions{})
	c.Assert(err, IsNil)
	defer func() { _ = storage.Close() }()
	c.Assert(storage.SetScanStatus("legacy", "oldsig", 900), IsNil)
	first := &SOLScanner{logger: zerolog.Nop(), db: storage, vaultScanStatuses: map[string]vaultScanStatus{}}
	c.Assert(first.storeVaultScanStatus("vault", vaultScanStatus{lastSig: "sig", lastSlot: 1000, cursorSlot: 990}), IsNil)

	restarted := &SOLScanner{logger: zerolog.Nop(), db: storage, vaultScanStatuses: map[string]vaultScanStatus{}}
	vss, err := restarted.loadVaultScanStatus("vault")
	c.Assert(err, IsNil)
	c.Assert(vss, DeepEquals, vaultScanStatus{lastSig: "sig", lastSlot: 1000, cursorSlot: 990})
	// the cursor and its slot are one write: a later status replaces both together
	c.Assert(first.storeVaultScanStatus("vault", vaultScanStatus{lastSig: "sig2", lastSlot: 1100, cursorSlot: 1050}), IsNil)
	sig, slot, cursorSlot, err := storage.GetScanStatusWithCursor("vault")
	c.Assert(err, IsNil)
	c.Assert([]any{sig, slot, cursorSlot}, DeepEquals, []any{"sig2", uint64(1100), uint64(1050)})
	vss, err = restarted.loadVaultScanStatus("legacy")
	c.Assert(err, IsNil)
	c.Assert(vss, DeepEquals, vaultScanStatus{lastSig: "oldsig", lastSlot: 900})
}

// An endpoint that keeps failing a later page of a vault's listing gives way to the
// next candidate after solPinRetries failed listings. Its other requests succeed and
// its probes pass, so it is never evicted, and pinning it again every round would
// leave the vault's older signatures unread for as long as it fails. The listing
// resumes from a cursor (pagingNode lists everything regardless of it): a catch-up
// listing is never moved (TestCatchUpListingIsNotMovedToAnotherEndpoint).
func (s *SolTestSuite) TestVaultListingThatKeepsFailingMovesToTheNextEndpoint(c *C) {
	sigs := make([]string, transactionQueryLimit+5)
	for i := range sigs {
		sigs[i] = fmt.Sprintf("sig%04d", i)
	}
	a, b := &pagingNode{sigs: sigs}, &pagingNode{sigs: sigs}
	a.failPaged.Store(true)
	sa, sb := httptest.NewServer(http.HandlerFunc(a.handler)), httptest.NewServer(http.HandlerFunc(b.handler))
	defer sa.Close()
	defer sb.Close()
	eps := []config.ChainEndpointConfig{{Name: "a", RPCHost: sa.URL, Priority: 1}, {Name: "b", RPCHost: sb.URL, Priority: 2}}
	cfg := config.BifrostChainConfiguration{ChainID: common.SOLChain}
	mgr, err := endpoints.NewManager(common.SOLChain, eps, cfg.GetFailoverConfig(), zerolog.Nop())
	c.Assert(err, IsNil)
	for _, e := range mgr.Entries() {
		e.SetIdentity("genesis")
	}
	targets, err := endpoints.TargetsFrom(eps, endpoints.RPCHost)
	c.Assert(err, IsNil)
	router, err := endpoints.NewHTTPRouter(mgr, endpoints.HTTPRouterConfig{Base: endpoints.SyntheticBase(common.SOLChain), Targets: targets}, zerolog.Nop())
	c.Assert(err, IsNil)
	scanner := &SOLScanner{
		logger:      zerolog.Nop(),
		solRpc:      rpc.NewSolRPCWithClient(endpoints.SyntheticBase(common.SOLChain).String(), 2*time.Second, &http.Client{Transport: router, Timeout: 2 * time.Second}),
		pinEndpoint: func(failures int) string { return pinCandidate(mgr, failures) },
	}
	scan := func() (ok bool, count int, cursor string) {
		txCounts, oks, cursors := make([]int, 1), make([]bool, 1), make([]string, 1)
		var wg sync.WaitGroup
		wg.Add(1)
		scanner.scanVault("vault", 0, "sigcursor", 0, 0, func() {}, txCounts, oks, cursors, make([]uint64, 1), &wg)
		wg.Wait()
		return oks[0], txCounts[0], cursors[0]
	}

	for round := 0; round < solPinRetries; round++ {
		ok, _, _ := scan()
		c.Assert(ok, Equals, false, Commentf("round %d", round))
	}
	c.Assert(b.listings.Load(), Equals, int32(0))
	ok, count, cursor := scan()
	c.Assert(ok, Equals, true)
	c.Assert(count, Equals, transactionQueryLimit+5)
	c.Assert(cursor, Equals, "sig0000")
	c.Assert(b.listings.Load() > 0, Equals, true)
	c.Assert(mgr.Entry("a").State(), Equals, endpoints.StateHealthy) // never evicted: it answers everything else
}

// A catch-up listing (no cursor) lists the whole history the node holds, and
// endpoints keep different amounts of it, so it is never moved to another endpoint:
// here b holds only the newest 1,000 signatures, and listing the vault there would end
// early and store a cursor past the 5 older signatures for good. It keeps failing on a
// (no cursor is stored) until a can list it.
func (s *SolTestSuite) TestCatchUpListingIsNotMovedToAnotherEndpoint(c *C) {
	sigs := make([]string, transactionQueryLimit+5)
	for i := range sigs {
		sigs[i] = fmt.Sprintf("sig%04d", i)
	}
	a := &pagingNode{sigs: sigs}
	b := &pagingNode{sigs: sigs[:transactionQueryLimit]} // never saw the older 5
	a.failPaged.Store(true)
	sa, sb := httptest.NewServer(http.HandlerFunc(a.handler)), httptest.NewServer(http.HandlerFunc(b.handler))
	defer sa.Close()
	defer sb.Close()
	eps := []config.ChainEndpointConfig{{Name: "a", RPCHost: sa.URL, Priority: 1}, {Name: "b", RPCHost: sb.URL, Priority: 2}}
	cfg := config.BifrostChainConfiguration{ChainID: common.SOLChain}
	mgr, err := endpoints.NewManager(common.SOLChain, eps, cfg.GetFailoverConfig(), zerolog.Nop())
	c.Assert(err, IsNil)
	for _, e := range mgr.Entries() {
		e.SetIdentity("genesis")
	}
	targets, err := endpoints.TargetsFrom(eps, endpoints.RPCHost)
	c.Assert(err, IsNil)
	router, err := endpoints.NewHTTPRouter(mgr, endpoints.HTTPRouterConfig{Base: endpoints.SyntheticBase(common.SOLChain), Targets: targets}, zerolog.Nop())
	c.Assert(err, IsNil)
	logs := &lockedBuffer{}
	scanner := &SOLScanner{
		logger:      zerolog.New(logs),
		solRpc:      rpc.NewSolRPCWithClient(endpoints.SyntheticBase(common.SOLChain).String(), 2*time.Second, &http.Client{Transport: router, Timeout: 2 * time.Second}),
		pinEndpoint: func(failures int) string { return pinCandidate(mgr, failures) },
	}
	scan := func() (ok bool, cursor string) {
		txCounts, oks, cursors := make([]int, 1), make([]bool, 1), make([]string, 1)
		var wg sync.WaitGroup
		wg.Add(1)
		scanner.scanVault("vault", 0, "", 0, 0, func() {}, txCounts, oks, cursors, make([]uint64, 1), &wg)
		wg.Wait()
		return oks[0], cursors[0]
	}

	for round := 0; round <= 2*solPinRetries; round++ {
		ok, cursor := scan()
		c.Assert(ok, Equals, false, Commentf("round %d", round))
		c.Assert(cursor, Equals, "", Commentf("round %d", round))
	}
	c.Assert(b.listings.Load(), Equals, int32(0))
	// the operator is told which endpoint holds the catch-up back
	c.Assert(strings.Contains(logs.String(), `"endpoint":"a"`), Equals, true)
	c.Assert(strings.Contains(logs.String(), "vault catch-up listing keeps failing on its endpoint"), Equals, true)

	// once a lists its pages again, the vault completes from a
	a.failPaged.Store(false)
	ok, cursor := scan()
	c.Assert(ok, Equals, true)
	c.Assert(cursor, Equals, "sig0000")
	c.Assert(b.listings.Load(), Equals, int32(0))
}

// lockedBuffer is a log sink safe for the scan's concurrent writers.
type lockedBuffer struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
