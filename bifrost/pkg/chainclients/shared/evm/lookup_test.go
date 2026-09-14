package evm_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"time"

	ecommon "github.com/ethereum/go-ethereum/common"
	"github.com/rs/zerolog"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/endpoints"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/evm"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/signercache"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/config"
)

// LookupSuite is run by the package's existing TestingT call. Do NOT add a func Test.
type LookupSuite struct{}

var _ = Suite(&LookupSuite{})

const lookupHash = "0x00000000000000000000000000000000000000000000000000000000000000ab"

func ecommonHash() ecommon.Hash { return ecommon.HexToHash(lookupHash) }

// txServer answers eth_getTransactionByHash: known returns a transaction, absent
// returns a null result, broken returns HTTP 502.
func txServer(mode string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if mode == "broken" {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		var req struct {
			Method string          `json:"method"`
			ID     json.RawMessage `json:"id"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "application/json")
		id := string(req.ID)
		switch req.Method {
		case "eth_chainId":
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":` + id + `,"result":"0x539"}`))
		case "eth_blockNumber":
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":` + id + `,"result":"0x10"}`))
		case "eth_getTransactionByHash":
			if mode == "known" {
				_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":` + id + `,"result":{"hash":"` + lookupHash +
					`","nonce":"0x1","blockHash":null,"blockNumber":null,"transactionIndex":null,` +
					`"from":"0x1111111111111111111111111111111111111111","to":"0x2222222222222222222222222222222222222222",` +
					`"value":"0x1","gas":"0x5208","gasPrice":"0x1","input":"0x",` +
					`"v":"0x1b","r":"0x1","s":"0x1"}}`))
				return
			}
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":` + id + `,"result":null}`))
		default:
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":` + id + `,"result":"0x0"}`))
		}
	}))
}

// lookupClient builds a two-endpoint managed EVM client over the given servers.
func lookupClient(c *C, a, b *httptest.Server) *evm.FailoverClient {
	cfg := config.BifrostChainConfiguration{ChainID: common.AVAXChain, Endpoints: []config.ChainEndpointConfig{
		{Name: "a", RPCHost: a.URL, Priority: 1},
		{Name: "b", RPCHost: b.URL, Priority: 2},
	}}
	cfg.Failover.ErrorThreshold = 100 // health accounting is not what this suite pins
	cfg.Failover.HealthCheckTimeout = 500 * time.Millisecond
	fc, err := evm.NewFailoverClient(context.Background(), cfg, 2*time.Second, zerolog.Nop())
	c.Assert(err, IsNil)
	return fc
}

func (s *LookupSuite) TestLookupTxEverywhere(c *C) {
	known, absent, broken := txServer("known"), txServer("absent"), txServer("broken")
	defer known.Close()
	defer absent.Close()
	defer broken.Close()

	// found only on the secondary: the active endpoint says absent, b knows it
	fc := lookupClient(c, absent, known)
	defer fc.Manager.Stop()
	defer fc.Client.Close()
	c.Assert(evm.LookupTxEverywhere(context.Background(), fc.Manager, fc.Client, ecommonHash(), time.Hour), Equals, signercache.LookupFound)

	// absent on every endpoint, both answering: the only case that may clear the cache
	fc2 := lookupClient(c, absent, absent)
	defer fc2.Manager.Stop()
	defer fc2.Client.Close()
	c.Assert(evm.LookupTxEverywhere(context.Background(), fc2.Manager, fc2.Client, ecommonHash(), time.Hour), Equals, signercache.LookupNotFound)

	// one endpoint erroring: absence is not established
	fc3 := lookupClient(c, absent, broken)
	defer fc3.Manager.Stop()
	defer fc3.Client.Close()
	c.Assert(evm.LookupTxEverywhere(context.Background(), fc3.Manager, fc3.Client, ecommonHash(), time.Hour), Equals, signercache.LookupAmbiguous)

	// every endpoint disabled: nothing was queried, so nothing can prove absence
	fc4 := lookupClient(c, absent, absent)
	defer fc4.Manager.Stop()
	defer fc4.Client.Close()
	fc4.Manager.Disable("a", "identity")
	fc4.Manager.Disable("b", "identity")
	c.Assert(evm.LookupTxEverywhere(context.Background(), fc4.Manager, fc4.Client, ecommonHash(), time.Hour), Equals, signercache.LookupAmbiguous)
}

func (s *LookupSuite) TestLookupSkipsLongDeadEndpoints(c *C) {
	absent, broken := txServer("absent"), txServer("broken")
	defer absent.Close()
	defer broken.Close()
	fc := lookupClient(c, absent, broken)
	defer fc.Manager.Stop()
	defer fc.Client.Close()
	// b is unreachable: while it counts, absence stays ambiguous
	c.Assert(evm.LookupTxEverywhere(context.Background(), fc.Manager, fc.Client, ecommonHash(), time.Hour), Equals, signercache.LookupAmbiguous)
	// mark b down and treat anything down at all as too long: it is skipped, and the
	// remaining endpoint establishes absence instead of wedging re-signing forever
	fc.Manager.Entry("b").MarkUnhealthy(endpoints.ErrorKindTransport)
	c.Assert(evm.LookupTxEverywhere(context.Background(), fc.Manager, fc.Client, ecommonHash(), time.Nanosecond), Equals, signercache.LookupNotFound)
}
