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

	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/endpoints"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/solana/rpc"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/config"
)

// pagingNode answers getSignaturesForAddress like a Solana node: it has total
// signatures newest first, listed transactionQueryLimit per page; a "before" it does
// not know gives an empty page (Agave's behaviour). failBefore makes its next paged
// request fail once with HTTP 502. Every transaction failed on chain, so the scan
// skips them without observing anything.
type pagingNode struct {
	sigs       []string
	failBefore atomic.Bool
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
		if opts.Before != "" && n.failBefore.CompareAndSwap(true, false) {
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
		pinEndpoint: func() string {
			return mgr.ReadCandidates()[0].Name
		},
	}
	scan := func() (ok bool, count int, cursor string) {
		txCounts, oks, cursors := make([]int, 1), make([]bool, 1), make([]string, 1)
		var wg sync.WaitGroup
		wg.Add(1)
		scanner.scanVault("vault", 0, "", 0, func() {}, txCounts, oks, cursors, &wg)
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
