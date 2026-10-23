package utxo

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"time"

	"github.com/btcsuite/btcutil"
	"github.com/rs/zerolog"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/endpoints"
	chainclienttypes "gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/types"
	sutxo "gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/utxo"
	utxorpcc "gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/utxo/rpc"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/config"
)

// zecDaemon answers getblockcount with tip and getrawtransaction with "not found".
func zecDaemon(tip int64) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Method string `json:"method"`
			ID     int    `json:"id"`
		}
		_ = json.NewDecoder(r.Body).Decode(&request)
		switch request.Method {
		case "getblockcount":
			_ = json.NewEncoder(w).Encode(map[string]any{"id": request.ID, "result": tip, "error": nil})
		case "getrawtransaction":
			_ = json.NewEncoder(w).Encode(map[string]any{"id": request.ID, "result": nil, "error": map[string]any{"code": -5, "message": "No such mempool or blockchain transaction"}})
		default:
			http.Error(w, "unexpected RPC: "+request.Method, 400)
		}
	}))
}

// An expired payload leaves the retry queue only when the primary, which is asked
// whether it was mined, has itself reached the expiry height. Below it, another
// signer's copy may sit in a block the primary has not seen yet.
func (s *ZcashSignerSuite) TestZECExpiryNeedsThePrimaryAtTheExpiryHeight(c *C) {
	const txid = "500468e8f504eaeee3c53f826dac132dbff2abf824b5361d10d712a199917d65"
	lagging := zecDaemon(39)
	defer lagging.Close()
	var err error
	s.client.rpc, err = utxorpcc.NewClient(lagging.Listener.Addr().String(), s.cfg.UserName, s.cfg.Password, 0, time.Second, common.ZECChain, s.client.log)
	c.Assert(err, IsNil)
	// judged expired on a routed height of 41; the primary is at 39 and says "not found"
	_, err = s.client.checkZecBroadcastExpiry(40, txid, 41)
	c.Assert(err, ErrorMatches, ".*the primary is at 39, below the expiry height 40.*")
	c.Assert(errors.Is(err, chainclienttypes.ErrTxExpired), Equals, false)

	atExpiry := zecDaemon(40)
	defer atExpiry.Close()
	s.client.rpc, err = utxorpcc.NewClient(atExpiry.Listener.Addr().String(), s.cfg.UserName, s.cfg.Password, 0, time.Second, common.ZECChain, s.client.log)
	c.Assert(err, IsNil)
	_, err = s.client.checkZecBroadcastExpiry(40, txid, 41)
	c.Assert(errors.Is(err, chainclienttypes.ErrTxExpired), Equals, true)
}

// ZEC inputs come from the primary daemon only, so the "tip has reached the input
// reference" guard must read the primary's tip: a fallback's height must not let a
// primary that cannot show it has caught up pass the guard.
func (s *ZcashSignerSuite) TestZECInputSelectionReadsThePrimaryTip(c *C) {
	fixture := s.newZecExpiryFixture(c)
	defer fixture.rpcServer.Close()
	fixture.heightErr.Store(true) // the primary cannot report its tip
	ahead := zecDaemon(3500001)
	defer ahead.Close()
	fallback, err := utxorpcc.NewClient(ahead.Listener.Addr().String(), s.cfg.UserName, s.cfg.Password, 0, time.Second, common.ZECChain, s.client.log)
	c.Assert(err, IsNil)
	s.client.rpc, err = utxorpcc.NewClientWithFallbacks(fixture.rpcServer.Listener.Addr().String(), s.cfg.UserName, s.cfg.Password, "", 0, time.Second, common.ZECChain, s.client.log, []utxorpcc.UTXOReader{fallback}, 0)
	c.Assert(err, IsNil)
	// the routed height comes from the fallback
	routed, err := s.client.rpc.GetBlockCount()
	c.Assert(err, IsNil)
	c.Assert(routed, Equals, int64(3500001))

	_, err = s.client.selectZecUtxos(fixture.tx.VaultPubKey, btcutil.Amount(1000), 0, 3500000, false, func(int) btcutil.Amount { return 0 })
	c.Assert(err, ErrorMatches, "get ZEC tip for input selection.*")
}

// relayDaemon answers getnetworkinfo with relayFee, or fails it when relayFee < 0.
func relayDaemon(relayFee float64) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Method string `json:"method"`
			ID     int    `json:"id"`
		}
		_ = json.NewDecoder(r.Body).Decode(&request)
		if request.Method != "getnetworkinfo" || relayFee < 0 {
			_ = json.NewEncoder(w).Encode(map[string]any{"id": request.ID, "result": nil, "error": map[string]any{"code": -1, "message": "unavailable"}})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": request.ID, "result": map[string]any{"relayfee": relayFee}, "error": nil})
	}))
}

// The relay fee feeds the outbound fee floor every signer must compute alike, so it
// is this node's own daemon's policy: a fallback provider's (another version, another
// default) is never taken, and while the primary cannot answer the last value stays.
func (s *CoverageBackfillSuite) TestRelayFeeComesFromThePrimaryOnly(c *C) {
	savedChain, savedRPC := s.client.cfg.ChainID, s.client.rpc
	defer func() { s.client.cfg.ChainID, s.client.rpc = savedChain, savedRPC }()
	s.client.cfg.ChainID = common.LTCChain
	s.client.minRelayFeeSats.Store(1000)

	down := relayDaemon(-1)
	defer down.Close()
	other := relayDaemon(0.0001) // 10000 sats: another daemon's default
	defer other.Close()
	fallback, err := utxorpcc.NewClient(other.Listener.Addr().String(), "", "", 0, time.Second, common.LTCChain, s.client.log)
	c.Assert(err, IsNil)
	s.client.rpc, err = utxorpcc.NewClientWithFallbacks(down.Listener.Addr().String(), "", "", "", 0, time.Second, common.LTCChain, s.client.log, []utxorpcc.UTXOReader{fallback}, 0)
	c.Assert(err, IsNil)
	s.client.updateNetworkInfo()
	c.Assert(s.client.minRelayFeeSats.Load(), Equals, uint64(1000))

	own := relayDaemon(0.00002) // 2000 sats
	defer own.Close()
	s.client.rpc, err = utxorpcc.NewClientWithFallbacks(own.Listener.Addr().String(), "", "", "", 0, time.Second, common.LTCChain, s.client.log, []utxorpcc.UTXOReader{fallback}, 0)
	c.Assert(err, IsNil)
	s.client.updateNetworkInfo()
	c.Assert(s.client.minRelayFeeSats.Load(), Equals, uint64(2000))
}

// While a fallback serves reads because the health checks took the primary out,
// pruning must not run the primary's full retry loop for every block that holds our
// own transactions: it keeps the block meta (the safe answer) without asking.
func (s *CoverageBackfillSuite) TestPruningSkipsAPrimaryOutOfRotation(c *C) {
	var asked atomic.Int32
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Method string `json:"method"`
			ID     int    `json:"id"`
		}
		_ = json.NewDecoder(r.Body).Decode(&request)
		asked.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{"id": request.ID, "result": nil, "error": map[string]any{"code": -5, "message": "Transaction not in mempool"}})
	}))
	defer primary.Close()
	other := relayDaemon(-1)
	defer other.Close()
	fallback, err := utxorpcc.NewClient(other.Listener.Addr().String(), "", "", 0, time.Second, common.LTCChain, s.client.log)
	c.Assert(err, IsNil)
	eps := []config.ChainEndpointConfig{{Name: "primary", RPCHost: primary.URL, Priority: 1}, {Name: "fallback", RPCHost: other.URL, Priority: 2}}
	mgr, err := endpoints.NewManager(common.LTCChain, eps, config.BifrostChainConfiguration{ChainID: common.LTCChain}.GetFailoverConfig(), zerolog.Nop())
	c.Assert(err, IsNil)
	for _, e := range mgr.Entries() {
		e.SetIdentity("ltc")
	}
	rpcClient, err := utxorpcc.NewClientWithFallbacks(primary.Listener.Addr().String(), "", "", "", 0, time.Second, common.LTCChain, s.client.log, []utxorpcc.UTXOReader{fallback}, 0)
	c.Assert(err, IsNil)
	savedRPC := s.client.rpc
	defer func() { s.client.rpc = savedRPC }()
	s.client.rpc = rpcClient.WithEndpointManager(mgr, eps)

	meta := sutxo.NewBlockMeta("prev", 10, "hash10")
	meta.AddSelfTransaction("mine")

	// in rotation: the primary is asked, and "not in mempool" lets the meta go
	c.Assert(s.client.canDeleteBlock(meta), Equals, true)
	c.Assert(asked.Load(), Equals, int32(1))

	// out of rotation: kept, and the primary is not asked
	mgr.Entry("primary").MarkUnhealthy(endpoints.ErrorKindTransport)
	c.Assert(s.client.canDeleteBlock(meta), Equals, false)
	c.Assert(asked.Load(), Equals, int32(1))
}

// ZEC UTXOs (and the heights they were mined at) come from the primary only, so
// their confirmations are counted against the primary's own tip, not a fallback's.
func (s *ZcashSignerSuite) TestZECListUnspentCountsConfirmationsOnThePrimary(c *C) {
	fixture := s.newZecExpiryFixture(c)
	defer fixture.rpcServer.Close()
	ahead := zecDaemon(3600000) // a fallback far ahead of the primary
	defer ahead.Close()
	fallback, err := utxorpcc.NewClient(ahead.Listener.Addr().String(), s.cfg.UserName, s.cfg.Password, 0, time.Second, common.ZECChain, s.client.log)
	c.Assert(err, IsNil)
	rpcClient, err := utxorpcc.NewClientWithFallbacks(fixture.rpcServer.Listener.Addr().String(), s.cfg.UserName, s.cfg.Password, "", 0, time.Second, common.ZECChain, s.client.log, []utxorpcc.UTXOReader{fallback}, 0)
	c.Assert(err, IsNil)
	address, err := fixture.tx.VaultPubKey.GetAddress(common.ZECChain)
	c.Assert(err, IsNil)

	// the primary is at 3500001 and the fixture UTXO was mined at 3499900
	unspent, err := rpcClient.ListUnspent(address.String())
	c.Assert(err, IsNil)
	c.Assert(unspent, HasLen, 1)
	c.Assert(unspent[0].Confirmations, Equals, int64(101))

	// a primary that cannot report its tip gives no answer, not a fallback's count
	fixture.heightErr.Store(true)
	_, err = rpcClient.ListUnspent(address.String())
	c.Assert(err, NotNil)
}
