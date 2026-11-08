package evm_test

import (
	"context"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"time"

	ecommon "github.com/ethereum/go-ethereum/common"
	etypes "github.com/ethereum/go-ethereum/core/types"
	"github.com/prometheus/client_golang/prometheus/testutil"
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

// testBlockJSON is an empty block every txServer returns at any height.
const testBlockJSON = `{"parentHash":"0x0000000000000000000000000000000000000000000000000000000000000000","sha3Uncles":"0x1dcc4de8dec75d7aab85b567b6ccd41ad312451b948a7413f0a142fd40d49347","miner":"0x0000000000000000000000000000000000000000","stateRoot":"0x0000000000000000000000000000000000000000000000000000000000000000","transactionsRoot":"0x56e81f171bcc55a6ff8345e692c0f86e5b48e01b996cadc001622fb5e363b421","receiptsRoot":"0x56e81f171bcc55a6ff8345e692c0f86e5b48e01b996cadc001622fb5e363b421","logsBloom":"0x00000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000","difficulty":"0x1","number":"0x63","gasLimit":"0x1","gasUsed":"0x0","timestamp":"0x1","extraData":"0x","mixHash":"0x0000000000000000000000000000000000000000000000000000000000000000","nonce":"0x0000000000000000","transactions":[],"uncles":[]}`

func ecommonHash() ecommon.Hash { return ecommon.HexToHash(lookupHash) }

// testTipJSON is block 100, the child of testBlockJSON's block: the block a scanner holds
// when it detects a reorg there, which txServer serves at height 100.
var testTipJSON = func() string {
	var parent etypes.Header
	if err := json.Unmarshal([]byte(testBlockJSON), &parent); err != nil {
		panic(err)
	}
	tip := strings.Replace(testBlockJSON, `"number":"0x63"`, `"number":"0x64"`, 1)
	return strings.Replace(tip, `"parentHash":"0x0000000000000000000000000000000000000000000000000000000000000000"`, `"parentHash":"`+parent.Hash().Hex()+`"`, 1)
}()

// testTip is testTipJSON's header.
func testTip(c *C) *etypes.Header {
	var h etypes.Header
	c.Assert(json.Unmarshal([]byte(testTipJSON), &h), IsNil)
	return &h
}

// txServer answers eth_getTransactionByHash: "known" returns a transaction,
// "absent" a null result, "txerror" HTTP 502 for the lookup only (so the endpoint
// still verifies its identity at bootstrap and counts as a witness that could not
// answer), and "broken" HTTP 502 for everything (so it never verifies at all). Blocks:
// testTipJSON at height 100, testBlockJSON at any other; "blockerror" answers block
// reads with HTTP 502 and "blocklimited" with HTTP 429, everything else as "absent".
func txServer(mode string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if mode == "broken" {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		var req struct {
			Method string            `json:"method"`
			ID     json.RawMessage   `json:"id"`
			Params []json.RawMessage `json:"params"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "application/json")
		id := string(req.ID)
		switch req.Method {
		case "eth_chainId":
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":` + id + `,"result":"0x539"}`))
		case "eth_blockNumber":
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":` + id + `,"result":"0x10"}`))
		case "eth_getBlockByNumber":
			if mode == "blockerror" {
				w.WriteHeader(http.StatusBadGateway)
				return
			}
			if mode == "blocklimited" {
				w.WriteHeader(http.StatusTooManyRequests)
				return
			}
			block := testBlockJSON
			if len(req.Params) > 0 && string(req.Params[0]) == `"0x64"` {
				block = testTipJSON
			}
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":` + id + `,"result":` + block + `}`))
		case "eth_getTransactionByHash":
			if mode == "txerror" {
				w.WriteHeader(http.StatusBadGateway)
				return
			}
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
	known, absent, txerror, broken := txServer("known"), txServer("absent"), txServer("txerror"), txServer("broken")
	defer known.Close()
	defer absent.Close()
	defer txerror.Close()
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

	// one endpoint answers, the other cannot: absence is not established
	fc3 := lookupClient(c, absent, txerror)
	defer fc3.Manager.Stop()
	defer fc3.Client.Close()
	c.Assert(evm.LookupTxEverywhere(context.Background(), fc3.Manager, fc3.Client, ecommonHash(), time.Hour), Equals, signercache.LookupAmbiguous)

	// an endpoint whose chain identity was never verified in this process (it was
	// unreachable at startup) is never asked, but it may have taken the broadcast
	// before a restart: absence is not established, the cache entry is kept
	fc5 := lookupClient(c, absent, broken)
	defer fc5.Manager.Stop()
	defer fc5.Client.Close()
	c.Assert(fc5.Manager.Entry("b").Usable(), Equals, false)
	c.Assert(evm.LookupTxEverywhere(context.Background(), fc5.Manager, fc5.Client, ecommonHash(), time.Hour), Equals, signercache.LookupAmbiguous)
	// once it is proven to be on another network it is disabled and no longer counts
	fc5.Manager.Disable("b", "identity")
	c.Assert(evm.LookupTxEverywhere(context.Background(), fc5.Manager, fc5.Client, ecommonHash(), time.Hour), Equals, signercache.LookupNotFound)

	// every endpoint disabled: nothing was queried, so nothing can prove absence
	fc4 := lookupClient(c, absent, absent)
	defer fc4.Manager.Stop()
	defer fc4.Client.Close()
	fc4.Manager.Disable("a", "identity")
	fc4.Manager.Disable("b", "identity")
	c.Assert(evm.LookupTxEverywhere(context.Background(), fc4.Manager, fc4.Client, ecommonHash(), time.Hour), Equals, signercache.LookupAmbiguous)
}

// An ambiguous verification is counted against the endpoint that kept it ambiguous
// (here one whose lookup failed, then one never verified); an answer found elsewhere,
// or absence proven everywhere, counts nothing.
func (s *LookupSuite) TestAmbiguousLookupIsCountedAgainstTheBlockingEndpoint(c *C) {
	known, absent, txerror, broken := txServer("known"), txServer("absent"), txServer("txerror"), txServer("broken")
	defer known.Close()
	defer absent.Close()
	defer txerror.Close()
	defer broken.Close()
	blocked := func(name string) float64 {
		return testutil.ToFloat64(endpoints.DefaultMetrics().VerificationBlockedCounter().WithLabelValues("AVAX", name))
	}
	lookup := func(a, b *httptest.Server) signercache.LookupResult {
		fc := lookupClient(c, a, b)
		defer fc.Manager.Stop()
		defer fc.Client.Close()
		return evm.LookupTxEverywhere(context.Background(), fc.Manager, fc.Client, ecommonHash(), time.Hour)
	}
	a0, b0 := blocked("a"), blocked("b")
	c.Assert(lookup(absent, txerror), Equals, signercache.LookupAmbiguous)
	c.Assert(blocked("a"), Equals, a0)
	c.Assert(blocked("b"), Equals, b0+1)
	c.Assert(lookup(absent, broken), Equals, signercache.LookupAmbiguous)
	c.Assert(blocked("b"), Equals, b0+2)
	// no endpoint can be asked at all (a disabled, b never verified): b still counted
	fc := lookupClient(c, absent, broken)
	fc.Manager.Disable("a", "identity")
	c.Assert(evm.LookupTxEverywhere(context.Background(), fc.Manager, fc.Client, ecommonHash(), time.Hour), Equals, signercache.LookupAmbiguous)
	fc.Manager.Stop()
	fc.Client.Close()
	c.Assert(blocked("a"), Equals, a0)
	c.Assert(blocked("b"), Equals, b0+3)
	c.Assert(lookup(txerror, known), Equals, signercache.LookupFound)
	c.Assert(lookup(absent, absent), Equals, signercache.LookupNotFound)
	c.Assert(blocked("a"), Equals, a0)
	c.Assert(blocked("b"), Equals, b0+3)
}

func (s *LookupSuite) TestLookupSkipsLongDeadEndpoints(c *C) {
	absent, txerror := txServer("absent"), txServer("txerror")
	defer absent.Close()
	defer txerror.Close()
	fc := lookupClient(c, absent, txerror)
	defer fc.Manager.Stop()
	defer fc.Client.Close()
	// b is verified but cannot answer: while it counts as a witness, absence stays
	// ambiguous and the signer cache is kept
	fc.Manager.Entry("b").MarkUnhealthy(endpoints.ErrorKindTransport)
	c.Assert(evm.LookupTxEverywhere(context.Background(), fc.Manager, fc.Client, ecommonHash(), time.Hour), Equals, signercache.LookupAmbiguous)
	// once it has been down longer than the signer-cache expiry it is skipped, so a
	// retired provider left in the list does not wedge re-signing forever
	c.Assert(evm.LookupTxEverywhere(context.Background(), fc.Manager, fc.Client, ecommonHash(), time.Nanosecond), Equals, signercache.LookupNotFound)
}

// Reorg re-confirmation has one witness: the active endpoint, once its block at the
// reorg tip is the parent of the block being scanned, it has the scanned block itself
// (same height, same fork) and it is as far as the chain height the scan was driven
// by. Another endpoint that still has the transaction (it may be on the old fork)
// cannot veto the errata, and a witness that is behind or on another fork cannot cause
// one.
func (s *LookupSuite) TestReorgWitness(c *C) {
	absent, known := txServer("absent"), txServer("known")
	defer absent.Close()
	defer known.Close()
	ctx := context.Background()
	scanned := testTip(c)

	// no manager: unpinned, the single endpoint answers as before failover existed
	w, err := evm.ReorgWitness(ctx, nil, nil, scanned, 16)
	c.Assert(err, IsNil)
	c.Assert(w, Equals, "")

	// a is active, its block 99 is the reorg parent, it has the scanned block 100 and
	// its tip (16 in this fixture) is the chain height the scan saw: it is the witness
	fc := lookupClient(c, absent, known)
	defer fc.Manager.Stop()
	defer fc.Client.Close()
	w, err = evm.ReorgWitness(ctx, fc.Manager, fc.Client, scanned, 16)
	c.Assert(err, IsNil)
	c.Assert(w, Equals, "a")

	// the witness's "not found" stands although b still has the transaction
	rpc, err := evm.NewEthRPC(fc.Client, 2*time.Second, "AVAX")
	c.Assert(err, IsNil)
	presence, err := rpc.CheckTransaction(w, lookupHash)
	c.Assert(err, IsNil)
	c.Assert(presence, Equals, evm.TxNotFound)
	// the pin is what decides who answers: b says it is still there (pending)
	presence, err = rpc.CheckTransaction("b", lookupHash)
	c.Assert(err, IsNil)
	c.Assert(presence, Equals, evm.TxOnChain)

	// a's block 99 is not the reorg parent: no witness, the step is retried
	otherParent := *scanned
	otherParent.ParentHash = ecommon.HexToHash("0x01")
	_, err = evm.ReorgWitness(ctx, fc.Manager, fc.Client, &otherParent, 16)
	c.Assert(err, ErrorMatches, ".*behind or on another fork.*")

	// a holds another block 100 on the same parent (a competing tip): its receipts
	// would describe that block, not the scanned one, so the step is retried
	competing := *scanned
	competing.Extra = []byte{1}
	_, err = evm.ReorgWitness(ctx, fc.Manager, fc.Client, &competing, 16)
	c.Assert(err, ErrorMatches, ".*not the scanned block.*")

	// a is below the chain height the scan was driven by (another endpoint served it):
	// its "not found" could miss a transaction re-mined above, so the step is retried
	_, err = evm.ReorgWitness(ctx, fc.Manager, fc.Client, scanned, 17)
	c.Assert(err, ErrorMatches, ".*behind the chain height 17.*")
	// none of these is a failure to answer: a stays active
	c.Assert(fc.Manager.Active().Name, Equals, "a")
}

// An active endpoint that answers its probes but cannot serve the witness's block
// reads would be asked again on every retry (its other requests succeed, so inline
// errors never evict it): it is sidelined and the active role moves on, so the next
// attempt's witness is the next endpoint, which has to pass the same checks.
func (s *LookupSuite) TestReorgWitnessThatCannotAnswerHandsOff(c *C) {
	failing, absent := txServer("blockerror"), txServer("absent")
	defer failing.Close()
	defer absent.Close()
	ctx := context.Background()
	scanned := testTip(c)
	fc := lookupClient(c, failing, absent)
	defer fc.Manager.Stop()
	defer fc.Client.Close()
	c.Assert(fc.Manager.Active().Name, Equals, "a")

	_, err := evm.ReorgWitness(ctx, fc.Manager, fc.Client, scanned, 16)
	c.Assert(err, ErrorMatches, ".*cannot read block.*")
	c.Assert(fc.Manager.Active().Name, Equals, "b")
	w, err := evm.ReorgWitness(ctx, fc.Manager, fc.Client, scanned, 16)
	c.Assert(err, IsNil)
	c.Assert(w, Equals, "b")
}

// A reorg plan matches stored transaction hashes against the scanned block's in one
// form, whatever case or prefix the stored one has.
func (s *LookupSuite) TestScannedTxHashes(c *C) {
	tx := etypes.NewTx(&etypes.LegacyTx{Nonce: 1, Gas: 21000, GasPrice: big.NewInt(1)})
	listed := evm.ScannedTxHashes(etypes.Transactions{nil, tx})
	c.Assert(listed, HasLen, 1)
	for _, stored := range []string{tx.Hash().Hex(), tx.Hash().Hex()[2:], strings.ToUpper(tx.Hash().Hex()[2:]), "0X" + tx.Hash().Hex()[2:]} {
		c.Assert(listed[evm.TxMetaKey(stored)], Equals, true, Commentf("%s", stored))
	}
	c.Assert(listed[evm.TxMetaKey(lookupHash)], Equals, false)
	c.Assert(evm.ScannedTxHashes(nil), HasLen, 0)
}

// The router has already charged the failed witness read (here a rate limit): sidelining
// moves the active role but neither counts the read again nor relabels it.
func (s *LookupSuite) TestReorgWitnessSidelineKeepsTheRoutersAccount(c *C) {
	limited, absent := txServer("blocklimited"), txServer("absent")
	defer limited.Close()
	defer absent.Close()
	fc := lookupClient(c, limited, absent)
	defer fc.Manager.Stop()
	defer fc.Client.Close()
	_, err := evm.ReorgWitness(context.Background(), fc.Manager, fc.Client, testTip(c), 16)
	c.Assert(err, NotNil)
	c.Assert(fc.Manager.Active().Name, Equals, "b")
	c.Assert(fc.Manager.Entry("a").Snapshot().LastErrorKind, Equals, string(endpoints.ErrorKindRateLimit))
}
