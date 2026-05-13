package blockcypher_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/utxo/rpc/blockcypher"
)

func Test(t *testing.T) { TestingT(t) }

type BlockcypherSuite struct{}

var _ = Suite(&BlockcypherSuite{})

func newTestClient(srv *httptest.Server) *blockcypher.Client {
	return blockcypher.NewClient(srv.URL+"/v1", "btc", "main", "", 1<<20)
}

func (s *BlockcypherSuite) TestGetBlockCount(c *C) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.Assert(r.URL.Path, Equals, "/v1/btc/main")
		_ = json.NewEncoder(w).Encode(map[string]any{"height": int64(800000), "hash": "abc"})
	}))
	defer srv.Close()

	count, err := newTestClient(srv).GetBlockCount()
	c.Assert(err, IsNil)
	c.Assert(count, Equals, int64(800000))
}

func (s *BlockcypherSuite) TestGetBlockHash(c *C) {
	const hash = "000000000000000000024bead8df69990852c202db0e0097c1a12ea637d7e96d"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.Assert(r.URL.Path, Equals, "/v1/btc/main/blocks/800000")
		_ = json.NewEncoder(w).Encode(map[string]any{"hash": hash, "height": int64(800000)})
	}))
	defer srv.Close()

	got, err := newTestClient(srv).GetBlockHash(800000)
	c.Assert(err, IsNil)
	c.Assert(got, Equals, hash)
}

func (s *BlockcypherSuite) TestGetBlockHashNotFound(c *C) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"hash": "", "height": int64(0)})
	}))
	defer srv.Close()

	_, err := newTestClient(srv).GetBlockHash(999999999)
	c.Assert(err, NotNil)
}

func (s *BlockcypherSuite) TestGetBlockVerboseTxs(c *C) {
	const hash = "blockhash1"
	// BlockCypher returns txid-only stubs that would be silently dropped by ignoreTx.
	// The adapter returns ErrNotSupported so withFallback falls through to a real node.
	cl := blockcypher.NewClient("http://unused", "btc", "main", "", 1<<20)
	result, err := cl.GetBlockVerboseTxs(hash)
	c.Assert(err, Equals, blockcypher.ErrNotSupported)
	c.Assert(result, IsNil)
}

func (s *BlockcypherSuite) TestGetRawTransactionVerbose(c *C) {
	const txid = "abc123"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.Assert(r.URL.Path, Equals, "/v1/btc/main/txs/"+txid)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"hash":         txid,
			"block_height": int64(800000),
			"fees":         int64(1000),
			"size":         250,
			"inputs": []map[string]any{
				{"prev_hash": "prev111", "output_index": uint32(0), "output_value": int64(50000)},
			},
			"outputs": []map[string]any{
				{"value": int64(49000), "addresses": []string{"bc1qaddr"}, "script_type": "pay-to-witness-pubkey-hash"},
			},
		})
	}))
	defer srv.Close()

	tx, err := newTestClient(srv).GetRawTransactionVerbose(txid)
	c.Assert(err, IsNil)
	c.Assert(tx.Txid, Equals, txid)
	c.Assert(len(tx.Vin), Equals, 1)
	c.Assert(tx.Vin[0].Txid, Equals, "prev111")
	c.Assert(len(tx.Vout), Equals, 1)
	c.Assert(tx.Vout[0].Value, Equals, float64(49000)/1e8)
	c.Assert(tx.Vout[0].ScriptPubKey.Addresses[0], Equals, "bc1qaddr")
}

func (s *BlockcypherSuite) TestGetMempoolEntryUnconfirmed(c *C) {
	const txid = "mempooltx"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"hash": txid, "block_height": int64(-1), "fees": int64(500), "size": 200,
			"inputs": []any{}, "outputs": []any{},
		})
	}))
	defer srv.Close()

	entry, err := newTestClient(srv).GetMempoolEntry(txid)
	c.Assert(err, IsNil)
	c.Assert(entry.Fees.Base, Equals, float64(500)/1e8)
}

func (s *BlockcypherSuite) TestGetMempoolEntryConfirmed(c *C) {
	const txid = "confirmedtx"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"hash": txid, "block_height": int64(800000), "fees": int64(500), "size": 200,
			"inputs": []any{}, "outputs": []any{},
		})
	}))
	defer srv.Close()

	_, err := newTestClient(srv).GetMempoolEntry(txid)
	c.Assert(err, NotNil) // confirmed tx must error
}

func (s *BlockcypherSuite) TestHTTPError(c *C) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "rate limited", http.StatusTooManyRequests)
	}))
	defer srv.Close()

	_, err := newTestClient(srv).GetBlockCount()
	c.Assert(err, NotNil)
}

func (s *BlockcypherSuite) TestNotSupportedMethods(c *C) {
	cl := blockcypher.NewClient("http://unused/v1", "btc", "main", "", 1<<20)

	_, err := cl.GetBlockStats("hash")
	c.Assert(err, Equals, blockcypher.ErrNotSupported)

	_, err = cl.GetRawTransaction("txid")
	c.Assert(err, Equals, blockcypher.ErrNotSupported)

	_, err = cl.GetRawMempool()
	c.Assert(err, Equals, blockcypher.ErrNotSupported)

	_, err = cl.GetNetworkInfo()
	c.Assert(err, Equals, blockcypher.ErrNotSupported)
}

func (s *BlockcypherSuite) TestTokenInjected(c *C) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.Assert(r.URL.Query().Get("token"), Equals, "mytoken")
		_ = json.NewEncoder(w).Encode(map[string]any{"height": int64(1), "hash": "abc"})
	}))
	defer srv.Close()

	cl := blockcypher.NewClient(srv.URL+"/v1", "btc", "main", "mytoken", 1<<20)
	_, err := cl.GetBlockCount()
	c.Assert(err, IsNil)
}
