package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"time"

	. "gopkg.in/check.v1"
)

type ApiCovSuite struct{}

var _ = Suite(&ApiCovSuite{})

// staticServerCov answers every request with the same status and body.
func staticServerCov(status int, body string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
}

func (s *ApiCovSuite) TestGetBaseEnergy(c *C) {
	info := TransactionInfo{}
	info.Receipt.EnergyUsageTotal = 1000
	info.Receipt.EnergyPenaltyTotal = 300
	c.Check(info.GetBaseEnergy(), Equals, int64(700))

	// A penalty larger than the total usage is clamped to zero.
	info.Receipt.EnergyPenaltyTotal = 1500
	c.Check(info.GetBaseEnergy(), Equals, int64(0))

	c.Check((&TransactionInfo{}).GetBaseEnergy(), Equals, int64(0))
}

func (s *ApiCovSuite) TestGetTransaction(c *C) {
	server := NewMockServer()
	defer server.Close()
	api := NewTronApi(server.URL, time.Second)

	tx, err := api.GetTransaction("deadbeef9034d95770d5704c9898e3414780c5fcbe282350080db8c4cef3e729")
	c.Assert(err, IsNil)
	c.Check(tx.TxId, Equals, "deadbeef9034d95770d5704c9898e3414780c5fcbe282350080db8c4cef3e729")
	c.Check(tx.RawData.Timestamp, Equals, int64(1))
	c.Check(tx.RawData.Expiration, Equals, int64(2))

	// Unknown transactions come back as an empty object.
	tx, err = api.GetTransaction("0123456789abcdef")
	c.Assert(err, IsNil)
	c.Check(tx.TxId, Equals, "")
}

func (s *ApiCovSuite) TestMalformedResponses(c *C) {
	server := staticServerCov(http.StatusOK, "not json")
	defer server.Close()
	api := NewTronApi(server.URL, time.Second)

	_, err := api.GetLatestBlock()
	c.Check(err, ErrorMatches, "failed to unmarshal response.*")
	_, err = api.GetBlock(1)
	c.Check(err, ErrorMatches, "failed to unmarshal response.*")
	_, err = api.GetTransactionInfo("ab")
	c.Check(err, ErrorMatches, "failed to unmarshal response.*")
	_, err = api.GetTransaction("ab")
	c.Check(err, ErrorMatches, "failed to unmarshal response.*")
	_, err = api.GetAccount("addr")
	c.Check(err, NotNil)
	_, err = api.CreateTransaction("a", "b", 1, "")
	c.Check(err, ErrorMatches, "failed to unmarshal response.*")
	_, err = api.TriggerSmartContract("a", "b", "sel", "in", 1)
	c.Check(err, ErrorMatches, "failed to unmarshal response.*")
	_, err = api.BroadcastTransaction([]byte(`{}`))
	c.Check(err, ErrorMatches, "failed to unmarshal response.*")
	_, err = api.GetChainParameters()
	c.Check(err, NotNil)
	_, err = api.EstimateEnergy("a", "b", "sel", "in")
	c.Check(err, NotNil)

	// The tx passed to broadcast must itself be JSON.
	_, err = api.BroadcastTransaction([]byte("nope"))
	c.Check(err, ErrorMatches, "failed to unmarshal tx.*")
}

func (s *ApiCovSuite) TestErrorStatus(c *C) {
	server := staticServerCov(http.StatusInternalServerError, "")
	defer server.Close()
	api := NewTronApi(server.URL, time.Second)

	_, err := api.GetLatestBlock()
	c.Check(err, ErrorMatches, "failed to call getnowblock: response status: 500")
	_, err = api.GetTransaction("ab")
	c.Check(err, ErrorMatches, "failed to call gettransactionbyid.*")
	_, err = api.CreateTransaction("a", "b", 1, "")
	c.Check(err, ErrorMatches, "failed to call createtransaction.*")
	_, err = api.TriggerSmartContract("a", "b", "sel", "in", 1)
	c.Check(err, ErrorMatches, "failed to call triggersmartcontract.*")
	_, err = api.BroadcastTransaction([]byte(`{}`))
	c.Check(err, ErrorMatches, "failed to call broadcasttransaction.*")
	_, err = api.GetChainParameters()
	c.Check(err, ErrorMatches, "failed to call getchainparameters.*")
	_, err = api.EstimateEnergy("a", "b", "sel", "in")
	c.Check(err, ErrorMatches, "failed to call estimateenergy.*")

	// Unreachable host.
	server.Close()
	_, err = api.GetBlock(1)
	c.Check(err, ErrorMatches, "failed to call getblockbynum: failed to perform request.*")

	// Invalid base URL.
	bad := NewTronApi("://bad", time.Second)
	_, err = bad.GetLatestBlock()
	c.Check(err, ErrorMatches, ".*failed to create url")

	// Params that can't be JSON encoded.
	_, err = bad.post("x", map[string]any{"ch": make(chan int)})
	c.Check(err, ErrorMatches, ".*failed to marshal params.*")
}

func (s *ApiCovSuite) TestApplicationErrors(c *C) {
	server := staticServerCov(http.StatusOK, `{"Error":"account not activated"}`)
	defer server.Close()
	api := NewTronApi(server.URL, time.Second)

	tx, err := api.CreateTransaction("a", "b", 1, "memo")
	c.Check(err, ErrorMatches, "create transaction failed: account not activated")
	c.Check(tx.Err, Equals, "account not activated")

	_, err = api.GetTransactionInfo("ab")
	c.Check(err, ErrorMatches, "transaction info lookup failed.*account not activated.*")

	// Chain parameters missing required keys.
	partial := staticServerCov(http.StatusOK, `{"chainParameter":[{"key":"getEnergyFee","value":420}]}`)
	defer partial.Close()
	params, err := NewTronApi(partial.URL, time.Second).GetChainParameters()
	c.Check(err, ErrorMatches, "failed to get necessary parameters")
	c.Check(params.EnergyFee, Equals, int64(420))

	// Literal null transaction info response.
	null := staticServerCov(http.StatusOK, `null`)
	defer null.Close()
	_, err = NewTronApi(null.URL, time.Second).GetTransactionInfo("ab")
	c.Check(err, ErrorMatches, "empty transaction info response")
}

func (s *ApiCovSuite) TestRehashErrors(c *C) {
	tx := Transaction{RawDataHex: "zz"}
	c.Check(tx.Rehash(), ErrorMatches, "failed to decode raw_data_hex.*")

	// Valid hex that is not a TransactionRaw protobuf.
	tx = Transaction{RawDataHex: "ff"}
	c.Check(tx.Rehash(), ErrorMatches, "failed to unmarshal raw_data_hex.*")

	tx = Transaction{RawDataHex: "", RawData: RawData{RefBlockBytes: "zz"}}
	c.Check(tx.Rehash(), ErrorMatches, "failed to decode ref_block_bytes.*")

	tx = Transaction{RawData: RawData{RefBlockBytes: "3746", RefBlockHash: "zz"}}
	c.Check(tx.Rehash(), ErrorMatches, "failed to decode ref_block_hash.*")

	tx = Transaction{RawData: RawData{RefBlockBytes: "3746", RefBlockHash: "00", Data: "zz"}}
	c.Check(tx.Rehash(), ErrorMatches, "failed to decode raw_data_hex.*")

	// An empty raw transaction rehashes fine and the id is recomputed.
	tx = Transaction{TxId: "stale", RawData: RawData{RefBlockBytes: "3746", RefBlockHash: "00", Timestamp: 5}}
	c.Assert(tx.Rehash(), IsNil)
	c.Check(tx.TxId, Not(Equals), "stale")
	c.Check(len(tx.TxId), Equals, 64)
	c.Check(tx.RawDataHex, Not(Equals), "")
}

func (s *ApiCovSuite) TestConvertAddressInvalidHex(c *C) {
	_, err := ConvertAddress("41" + string(bytes.Repeat([]byte("z"), 40)))
	c.Check(err, ErrorMatches, "address not valid")
}

func (s *ApiCovSuite) TestMockServerEdgeCases(c *C) {
	server := NewMockServer()
	defer server.Close()

	post := func(path string, body any) int {
		var buf bytes.Buffer
		if s, ok := body.(string); ok {
			buf.WriteString(s)
		} else {
			c.Assert(json.NewEncoder(&buf).Encode(body), IsNil)
		}
		resp, err := http.Post(server.URL+path, "application/json", &buf)
		c.Assert(err, IsNil)
		defer resp.Body.Close()
		return resp.StatusCode
	}

	c.Check(post("/wallet/getnowblock", "not json"), Equals, http.StatusBadRequest)
	c.Check(post("/wallet/getblockbynum", map[string]any{}), Equals, http.StatusBadRequest)
	c.Check(post("/wallet/getblockbynum", map[string]any{"num": 69351239}), Equals, http.StatusOK)
	c.Check(post("/wallet/gettransactioninfobyid", map[string]any{"value": "e1cd4454abcdef"}), Equals, http.StatusOK)
	c.Check(post("/wallet/gettransactioninfobyid", map[string]any{"value": "00000000abcdef"}), Equals, http.StatusNotFound)
	c.Check(post("/wallet/gettransactionbyid", map[string]any{"value": "77ddfa70abcdef"}), Equals, http.StatusOK)
	c.Check(post("/wallet/getaccount", map[string]any{}), Equals, http.StatusNotFound)
	c.Check(post("/wallet/unknownmethod", map[string]any{}), Equals, http.StatusNotFound)
}
