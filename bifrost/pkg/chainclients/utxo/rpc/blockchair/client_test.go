package blockchair_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/utxo/rpc/blockchair"
)

func Test(t *testing.T) { TestingT(t) }

type BlockchairSuite struct{}

var _ = Suite(&BlockchairSuite{})

func newTestClient(srv *httptest.Server) *blockchair.Client {
	return blockchair.NewClient(srv.URL, "bitcoin", "", 1<<20)
}

func (s *BlockchairSuite) TestGetBlockCount(c *C) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.Assert(r.URL.Path, Equals, "/bitcoin/stats")
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"blocks": int64(800000)}})
	}))
	defer srv.Close()

	count, err := newTestClient(srv).GetBlockCount()
	c.Assert(err, IsNil)
	c.Assert(count, Equals, int64(800000))
}

func (s *BlockchairSuite) TestGetBlockHash(c *C) {
	const hash = "000000000000000000024bead8df69990852c202db0e0097c1a12ea637d7e96d"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.Assert(r.URL.Path, Equals, "/bitcoin/blocks")
		c.Assert(r.URL.Query().Get("q"), Equals, "id(800000)")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{{"id": 800000, "hash": hash}},
		})
	}))
	defer srv.Close()

	got, err := newTestClient(srv).GetBlockHash(800000)
	c.Assert(err, IsNil)
	c.Assert(got, Equals, hash)
}

func (s *BlockchairSuite) TestGetBlockHashNotFound(c *C) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{}})
	}))
	defer srv.Close()

	_, err := newTestClient(srv).GetBlockHash(999999999)
	c.Assert(err, NotNil)
}

func (s *BlockchairSuite) TestGetRawTransactionVerbose(c *C) {
	const txid = "abc123"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.Assert(r.URL.Path, Equals, "/bitcoin/dashboards/transaction/"+txid)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				txid: map[string]any{
					"transaction": map[string]any{
						"hash":     txid,
						"block_id": int64(800000),
						"fee":      int64(1000),
						"size":     int32(250),
					},
					"inputs": []map[string]any{
						{"transaction_hash": "prev111", "index": uint32(0), "value": int64(50000)},
					},
					"outputs": []map[string]any{
						{"index": uint32(0), "value": int64(49000), "recipient": "bc1qaddr", "type": "v0_p2wpkh"},
					},
				},
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

func (s *BlockchairSuite) TestGetRawMempool(c *C) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.Assert(r.URL.Path, Equals, "/bitcoin/mempool/transactions")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{
				{"hash": "tx1", "fee": int64(500), "size": int32(200)},
				{"hash": "tx2", "fee": int64(300), "size": int32(150)},
			},
		})
	}))
	defer srv.Close()

	txids, err := newTestClient(srv).GetRawMempool()
	c.Assert(err, IsNil)
	c.Assert(txids, DeepEquals, []string{"tx1", "tx2"})
}

func (s *BlockchairSuite) TestGetMempoolEntryConfirmed(c *C) {
	const txid = "confirmedtx"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				txid: map[string]any{
					"transaction": map[string]any{
						"hash": txid, "block_id": int64(800000), "fee": int64(500), "size": int32(200),
					},
					"inputs":  []any{},
					"outputs": []any{},
				},
			},
		})
	}))
	defer srv.Close()

	_, err := newTestClient(srv).GetMempoolEntry(txid)
	c.Assert(err, NotNil) // confirmed tx must error
}

func (s *BlockchairSuite) TestHTTPError(c *C) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "rate limited", http.StatusTooManyRequests)
	}))
	defer srv.Close()

	_, err := newTestClient(srv).GetBlockCount()
	c.Assert(err, NotNil)
}

func (s *BlockchairSuite) TestNotSupportedMethods(c *C) {
	cl := blockchair.NewClient("http://unused", "bitcoin", "", 1<<20)

	_, err := cl.GetBlockStats("hash")
	c.Assert(err, Equals, blockchair.ErrNotSupported)

	_, err = cl.GetRawTransaction("txid")
	c.Assert(err, Equals, blockchair.ErrNotSupported)

	_, err = cl.GetNetworkInfo()
	c.Assert(err, Equals, blockchair.ErrNotSupported)
}

func (s *BlockchairSuite) TestApiKeyInjected(c *C) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.Assert(r.URL.Query().Get("key"), Equals, "mykey")
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"blocks": int64(1)}})
	}))
	defer srv.Close()

	cl := blockchair.NewClient(srv.URL, "bitcoin", "mykey", 1<<20)
	_, err := cl.GetBlockCount()
	c.Assert(err, IsNil)
}
