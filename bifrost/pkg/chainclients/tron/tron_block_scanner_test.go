package tron

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rs/zerolog"

	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/tron/api"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/tokenlist"
	"gitlab.com/thorchain/thornode/v3/config"

	. "gopkg.in/check.v1"
)

type BlockScannerTestSuite struct {
	api     *httptest.Server
	scanner *TronBlockScanner
}

var _ = Suite(&BlockScannerTestSuite{})

func (s *BlockScannerTestSuite) SetUpSuite(c *C) {
	s.api = api.NewMockServer()
	c.Assert(s.api, NotNil)

	clientConfig := config.BifrostClientConfiguration{
		ChainID:      "thorchain",
		ChainHost:    "localhost",
		SignerName:   "bob",
		SignerPasswd: "password",
	}

	chainConfig := config.BifrostChainConfiguration{
		ChainID: common.TRONChain,
		APIHost: s.api.URL,
		BlockScanner: config.BifrostBlockScannerConfiguration{
			ChainID:            common.TRONChain,
			HTTPRequestTimeout: time.Second * 2,
		},
	}

	bridge, err := thorclient.NewThorchainBridge(
		clientConfig, nil, nil,
	)
	c.Assert(err, IsNil)

	vaultAddress, err := api.ConvertAddress("412867f90e345015cd2234f562c5767296b717157b")
	c.Assert(err, IsNil)
	pubkeyMgr := receiptRetryVaultValidator{address: vaultAddress}

	s.scanner, err = NewTronBlockScanner(
		chainConfig,
		bridge,
		pubkeyMgr,
		func(h int64) error { return nil },
	)
	c.Assert(err, IsNil)

	s.scanner.whitelist = map[string]tokenlist.ERC20Token{
		"TG3XXyExBkPp9nzdajDZsozEu4BkaSJozs": {
			Name:     "USDT",
			Symbol:   "USDT",
			Address:  "TG3XXyExBkPp9nzdajDZsozEu4BkaSJozs",
			Decimals: 6,
		},
	}
}

func (s *BlockScannerTestSuite) TestGetHeight(c *C) {
	height, err := s.scanner.GetHeight()
	c.Assert(err, IsNil)
	// scanning 1 blocks behind the tip
	c.Assert(height, Equals, 55088560-ConfirmationBlocks)
}

func (s *BlockScannerTestSuite) TestFetchTxs(c *C) {
	txIn, err := s.scanner.FetchTxs(69351239, 0)
	c.Assert(err, IsNil)
	c.Assert(len(txIn.TxArray), Equals, 1)
	c.Assert(txIn.TxArray[0].Tx, Equals, "e1cd4454d71d8973e89155bc2c2a91aa56fd470c8ce64547ce8c69c789c21f0c")
}

func (s *BlockScannerTestSuite) TestGetMaxEnergy(c *C) {
	// fail because there is no refAddress set
	s.scanner.refAddress = ""
	maxEnergy, err := s.scanner.getMaxEnergy()
	c.Assert(err, NotNil)
	c.Assert(maxEnergy, Equals, int64(0))

	// set any address, using mock return
	s.scanner.refAddress = "TXhG4Vhrxyu8htL8tBdCmndF66D1jyKgJW"
	maxEnergy, err = s.scanner.getMaxEnergy()
	c.Assert(err, IsNil)
	// mock data returns 900k (2x)
	c.Assert(maxEnergy, Equals, int64(1800000))
}

// headReplies is a TRON node whose two head queries answer as scripted:
// ok, error (an HTTP-200 Error object), empty ({}), or an HTTP status code.
type headReplies struct {
	head                         int64
	header, full                 atomic.Pointer[string]
	headerRequests, fullRequests atomic.Int64
}

func (n *headReplies) serve(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var reply string
		switch r.URL.Path {
		case "/wallet/getblock":
			n.headerRequests.Add(1)
			reply = *n.header.Load()
		case "/wallet/getnowblock":
			n.fullRequests.Add(1)
			reply = *n.full.Load()
		default:
			http.NotFound(w, r)
			return
		}
		switch reply {
		case "ok":
			_, _ = fmt.Fprintf(w, `{"blockID":"%064x","block_header":{"raw_data":{"number":%d}}}`, n.head, n.head)
		case "error":
			_, _ = fmt.Fprint(w, `{"Error":"class java.lang.IllegalAccessException : lack of computing resources"}`)
		case "empty":
			_, _ = fmt.Fprint(w, `{}`)
		default:
			code, _ := strconv.Atoi(reply)
			http.Error(w, "unavailable", code)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func TestTRONGetHeightHeadQuery(t *testing.T) {
	type step struct {
		header, full         string
		wantErr              bool
		wantHeader, wantFull int64 // cumulative requests
	}
	for _, tc := range []struct {
		name     string
		head     int64
		steps    []step
		wantWarn int
	}{
		{"header served", 500, []step{
			{"ok", "ok", false, 1, 0},
			// once the header query has answered, failures are not retried with a full block
			{"503", "ok", true, 2, 0},
			{"error", "ok", true, 3, 0},
			{"404", "ok", true, 4, 0},
			{"ok", "ok", false, 5, 0},
		}, 0},
		{"header not served", 500, []step{
			// both queries down: undecided, the next poll probes the header again
			{"404", "503", true, 1, 1},
			// a full block that carries no block does not decide either
			{"404", "error", true, 2, 2},
			{"404", "empty", true, 3, 3},
			{"404", "ok", false, 4, 4},
			// decided: full blocks only, until restart
			{"ok", "ok", false, 4, 5},
			{"ok", "503", true, 4, 6},
		}, 1},
		{"header answers with an error at startup", 500, []step{
			{"error", "ok", false, 1, 1},
			{"ok", "ok", false, 1, 2},
		}, 1},
		{"recovers after a startup outage", 500, []step{
			{"503", "503", true, 1, 1},
			{"ok", "ok", false, 2, 1},
			{"503", "ok", true, 3, 1},
		}, 0},
		{"head below confirmations", ConfirmationBlocks - 1, []step{
			{"ok", "ok", false, 1, 0},
		}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			node := &headReplies{head: tc.head}
			server := node.serve(t)
			var logs bytes.Buffer
			scanner := &TronBlockScanner{
				logger: zerolog.New(&logs).Level(zerolog.WarnLevel),
				api:    api.NewTronApi(server.URL, time.Second),
			}
			for i, step := range tc.steps {
				node.header.Store(&step.header)
				node.full.Store(&step.full)
				height, err := scanner.GetHeight()
				if (err != nil) != step.wantErr {
					t.Fatalf("step %d: err = %v, want error %v", i, err, step.wantErr)
				}
				if want := max(tc.head-ConfirmationBlocks, 0); !step.wantErr && height != want {
					t.Fatalf("step %d: height = %d, want %d", i, height, want)
				}
				if got := node.headerRequests.Load(); got != step.wantHeader {
					t.Fatalf("step %d: header requests = %d, want %d", i, got, step.wantHeader)
				}
				if got := node.fullRequests.Load(); got != step.wantFull {
					t.Fatalf("step %d: full block requests = %d, want %d", i, got, step.wantFull)
				}
			}
			if got := strings.Count(logs.String(), "header-only head query unavailable"); got != tc.wantWarn {
				t.Fatalf("fallback warnings = %d, want %d: %s", got, tc.wantWarn, logs.String())
			}
		})
	}
}
