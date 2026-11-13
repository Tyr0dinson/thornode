package utxo

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"time"

	"github.com/rs/zerolog"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/endpoints"
	utxorpcc "gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/utxo/rpc"
	stypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/config"
	ttypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// walletDaemon answers getblockcount with its tip (raised by afterCount after each
// answer, if set) and listunspent with an empty wallet, counting getblockcount calls.
func walletDaemon(tip *atomic.Int64, counts *atomic.Int32, afterCount func()) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Method string `json:"method"`
			ID     int    `json:"id"`
		}
		_ = json.NewDecoder(r.Body).Decode(&request)
		switch request.Method {
		case "getblockcount":
			counts.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]any{"id": request.ID, "result": tip.Load(), "error": nil})
			if afterCount != nil {
				afterCount()
			}
		case "listunspent":
			_ = json.NewEncoder(w).Encode(map[string]any{"id": request.ID, "result": []any{}, "error": nil})
		default:
			http.Error(w, "unexpected RPC: "+request.Method, 400)
		}
	}))
}

// With a fallback, the scanned height can come from a fallback ahead of the primary,
// whose wallet the balances are read from: a report waits briefly for the primary to
// reach the height and is skipped (the next due report retries) if it does not, so
// balances are never labelled with a height the primary has not seen. Without a
// fallback the primary is not asked its height.
func (s *CoverageBackfillSuite) TestSolvencyWaitsForThePrimaryToReachTheHeight(c *C) {
	savedRPC, savedBridge, savedWait := s.client.rpc, s.client.bridge, solvencyPrimaryWait
	defer func() { s.client.rpc, s.client.bridge, solvencyPrimaryWait = savedRPC, savedBridge, savedWait }()
	solvencyPrimaryWait = time.Millisecond
	var tip atomic.Int64
	var counts atomic.Int32
	var raise atomic.Bool
	primary := walletDaemon(&tip, &counts, func() {
		if raise.Load() {
			tip.Store(100)
		}
	})
	defer primary.Close()
	other := relayDaemon(0.0001)
	defer other.Close()
	fallback, err := utxorpcc.NewClient(other.Listener.Addr().String(), "", "", 0, time.Second, common.BTCChain, s.client.log)
	c.Assert(err, IsNil)
	eps := []config.ChainEndpointConfig{{Name: "primary", RPCHost: primary.URL, Priority: 1}, {Name: "fallback", RPCHost: other.URL, Priority: 2}}
	mgr, err := endpoints.NewManager(common.BTCChain, eps, config.BifrostChainConfiguration{ChainID: common.BTCChain}.GetFailoverConfig(), zerolog.Nop())
	c.Assert(err, IsNil)
	for _, e := range mgr.Entries() {
		e.SetIdentity("btc")
	}
	rpcClient, err := utxorpcc.NewClientWithFallbacks(primary.Listener.Addr().String(), "", "", "", 0, time.Second, common.BTCChain, s.client.log, []utxorpcc.UTXOReader{fallback}, 0)
	c.Assert(err, IsNil)
	s.client.rpc = rpcClient.WithEndpointManager(mgr, eps)
	c.Assert(s.client.rpc.PrimaryOutWithFallback(), Equals, false) // the primary is in rotation

	// an insolvent vault (THORChain holds 1 BTC, the wallet is empty) is always reported
	vault := ttypes.Vault{PubKey: ttypes.GetRandomPubKey(), Coins: common.NewCoins(common.NewCoin(common.BTCAsset, cosmos.NewUint(100_000_000)))}
	s.client.bridge = &solvencyGateBridge{ThorchainBridge: s.bridge, asgards: ttypes.Vaults{vault}}

	// the primary stays at 98: the report for 100 is skipped, and the height is not
	// recorded, so the next block reports
	tip.Store(98)
	queue := make(chan stypes.Solvency, 10)
	s.client.globalSolvencyQueue = queue
	s.client.lastSolvencyCheckHeight.Store(0)
	c.Assert(s.client.ReportSolvency(100), IsNil)
	c.Assert(queue, HasLen, 0)
	c.Assert(counts.Load() > 0, Equals, true)
	c.Assert(s.client.lastSolvencyCheckHeight.Load(), Equals, int64(0))

	// the primary reaches 100 while the report waits: it is reported at 100
	raise.Store(true)
	c.Assert(s.client.ReportSolvency(100), IsNil)
	c.Assert(queue, HasLen, 1)
	msg := <-queue
	c.Assert(msg.Height, Equals, int64(100))
	c.Assert(s.client.lastSolvencyCheckHeight.Load(), Equals, int64(100))

	// without a fallback the scan and the wallet are the same daemon: no height read
	single, err := utxorpcc.NewClient(primary.Listener.Addr().String(), "", "", 0, time.Second, common.BTCChain, s.client.log)
	c.Assert(err, IsNil)
	s.client.rpc = single
	tip.Store(1)
	raise.Store(false)
	counts.Store(0)
	s.client.lastSolvencyCheckHeight.Store(0)
	c.Assert(s.client.ReportSolvency(100), IsNil)
	c.Assert(counts.Load(), Equals, int32(0))
	c.Assert(queue, HasLen, 1)
}
