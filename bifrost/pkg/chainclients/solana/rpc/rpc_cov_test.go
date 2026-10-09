package rpc

import (
	"encoding/json"
	"errors"
	"net/http"

	. "gopkg.in/check.v1"
)

type RPCCovSuite struct{}

var _ = Suite(&RPCCovSuite{})

func rpcCovFixed(body string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(body))
	}
}

func rpcCovFail() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}
}

// --- presign.go ---

func (s *RPCCovSuite) TestGetAccountMeta(c *C) {
	var gotParams []interface{}
	server, r := newTestServer(func(w http.ResponseWriter, req *http.Request) {
		var payload struct {
			Method string        `json:"method"`
			Params []interface{} `json:"params"`
		}
		c.Assert(json.NewDecoder(req.Body).Decode(&payload), IsNil)
		c.Assert(payload.Method, Equals, "getAccountInfo")
		gotParams = payload.Params
		_, _ = w.Write([]byte(`{"result":{"value":{"owner":"Prog1","executable":true,"lamports":42,"space":80}}}`))
	})
	defer server.Close()

	meta, err := r.GetAccountMeta("addr1")
	c.Assert(err, IsNil)
	c.Assert(meta, DeepEquals, AccountMeta{Exists: true, Executable: true, Owner: "Prog1", Lamports: 42, Space: 80})
	c.Assert(gotParams[0], Equals, "addr1")
	cfg, ok := gotParams[1].(map[string]interface{})
	c.Assert(ok, Equals, true)
	c.Assert(cfg["commitment"], Equals, "confirmed")
	c.Assert(cfg["encoding"], Equals, "base64")
}

func (s *RPCCovSuite) TestGetAccountMetaMissingAndErrors(c *C) {
	server, r := newTestServer(rpcCovFixed(`{"result":{"value":null}}`))
	meta, err := r.GetAccountMeta("a")
	c.Assert(err, IsNil)
	c.Assert(meta.Exists, Equals, false)
	server.Close()

	server, r = newTestServer(rpcCovFixed(`{"error":{"code":-1,"message":"boom"}}`))
	_, err = r.GetAccountMeta("a")
	c.Assert(err, ErrorMatches, "getAccountInfo error: -1 boom")
	server.Close()

	server, r = newTestServer(rpcCovFixed(`{}`))
	_, err = r.GetAccountMeta("a")
	c.Assert(err, ErrorMatches, "getAccountInfo: empty result")
	server.Close()

	server, r = newTestServer(rpcCovFail())
	_, err = r.GetAccountMeta("a")
	c.Assert(err, ErrorMatches, "failed to call getAccountInfo.*")
	server.Close()
}

func (s *RPCCovSuite) TestGetMinimumBalanceForRentExemption(c *C) {
	server, r := newTestServer(func(w http.ResponseWriter, req *http.Request) {
		var payload struct {
			Params []uint64 `json:"params"`
		}
		c.Assert(json.NewDecoder(req.Body).Decode(&payload), IsNil)
		c.Assert(payload.Params, DeepEquals, []uint64{80})
		_, _ = w.Write([]byte(`{"result":1447680}`))
	})
	v, err := r.GetMinimumBalanceForRentExemption(80)
	c.Assert(err, IsNil)
	c.Assert(v, Equals, uint64(1447680))
	server.Close()

	server, r = newTestServer(rpcCovFixed(`{"error":{"code":-2,"message":"bad"}}`))
	_, err = r.GetMinimumBalanceForRentExemption(1)
	c.Assert(err, ErrorMatches, "getMinimumBalanceForRentExemption error: -2 bad")
	server.Close()

	server, r = newTestServer(rpcCovFixed(`{}`))
	_, err = r.GetMinimumBalanceForRentExemption(1)
	c.Assert(err, ErrorMatches, "getMinimumBalanceForRentExemption: empty result")
	server.Close()

	server, r = newTestServer(rpcCovFail())
	_, err = r.GetMinimumBalanceForRentExemption(1)
	c.Assert(err, ErrorMatches, "failed to call getMinimumBalanceForRentExemption.*")
	server.Close()
}

func (s *RPCCovSuite) TestSimulateTransaction(c *C) {
	server, r := newTestServer(rpcCovFixed(`{"result":{"value":{"err":null}}}`))
	execErr, err := r.SimulateTransaction("dHg=")
	c.Assert(err, IsNil)
	c.Assert(execErr, IsNil)
	server.Close()

	server, r = newTestServer(rpcCovFixed(`{"result":{"value":{}}}`))
	execErr, err = r.SimulateTransaction("dHg=")
	c.Assert(err, IsNil)
	c.Assert(execErr, IsNil)
	server.Close()

	server, r = newTestServer(rpcCovFixed(`{"result":{"value":{"err":{"InstructionError":[0,"Custom"]}}}}`))
	execErr, err = r.SimulateTransaction("dHg=")
	c.Assert(err, IsNil)
	c.Assert(string(execErr), Equals, `{"InstructionError":[0,"Custom"]}`)
	server.Close()

	server, r = newTestServer(rpcCovFixed(`{"error":{"code":-3,"message":"nope"}}`))
	_, err = r.SimulateTransaction("dHg=")
	c.Assert(err, ErrorMatches, "simulateTransaction error: -3 nope")
	server.Close()

	server, r = newTestServer(rpcCovFixed(`{}`))
	_, err = r.SimulateTransaction("dHg=")
	c.Assert(err, ErrorMatches, "simulateTransaction: empty result")
	server.Close()

	server, r = newTestServer(rpcCovFail())
	_, err = r.SimulateTransaction("dHg=")
	c.Assert(err, ErrorMatches, "failed to call simulateTransaction.*")
	server.Close()
}

// --- token_accounts.go ---

func (s *RPCCovSuite) TestGetOwnedTokenAccountLamports(c *C) {
	var programs []string
	server, r := newTestServer(func(w http.ResponseWriter, req *http.Request) {
		var payload struct {
			Params []json.RawMessage `json:"params"`
		}
		c.Assert(json.NewDecoder(req.Body).Decode(&payload), IsNil)
		var filter map[string]string
		c.Assert(json.Unmarshal(payload.Params[1], &filter), IsNil)
		programs = append(programs, filter["programId"])
		if len(programs) == 1 {
			_, _ = w.Write([]byte(`{"result":{"value":[{"account":{"lamports":2039280}},{"account":{"lamports":10}}]}}`))
			return
		}
		_, _ = w.Write([]byte(`{"result":{"value":[{"account":{"lamports":5}}]}}`))
	})
	defer server.Close()

	total, err := r.GetOwnedTokenAccountLamports("owner", "finalized")
	c.Assert(err, IsNil)
	c.Assert(total.Uint64(), Equals, uint64(2039295))
	c.Assert(programs, DeepEquals, solanaTokenProgramIDs)
}

func (s *RPCCovSuite) TestGetOwnedTokenAccountLamportsErrors(c *C) {
	server, r := newTestServer(rpcCovFixed(`{"error":{"code":-32602,"message":"bad owner"}}`))
	_, err := r.GetOwnedTokenAccountLamports("owner", "finalized")
	c.Assert(err, ErrorMatches, "RPC Error -32602: bad owner")
	server.Close()

	server, r = newTestServer(rpcCovFixed(`{"result":{"value":[{"account":{"lamports":1.5}}]}}`))
	_, err = r.GetOwnedTokenAccountLamports("owner", "finalized")
	c.Assert(err, ErrorMatches, "failed to convert token account lamports.*")
	server.Close()

	server, r = newTestServer(rpcCovFail())
	_, err = r.GetOwnedTokenAccountLamports("owner", "finalized")
	c.Assert(err, ErrorMatches, "failed to call getTokenAccountsByOwner for Tokenkeg.*")
	server.Close()
}

// --- transactions.go ---

func (s *RPCCovSuite) TestGetTransactionConfirmedCommitment(c *C) {
	server, r := newTestServer(func(w http.ResponseWriter, req *http.Request) {
		var payload struct {
			Params []json.RawMessage `json:"params"`
		}
		c.Assert(json.NewDecoder(req.Body).Decode(&payload), IsNil)
		var cfg map[string]interface{}
		c.Assert(json.Unmarshal(payload.Params[1], &cfg), IsNil)
		c.Assert(cfg["commitment"], Equals, "confirmed")
		_, _ = w.Write([]byte(`{"result":{"slot":77}}`))
	})
	defer server.Close()
	tx, err := r.GetTransactionConfirmed("sig")
	c.Assert(err, IsNil)
	c.Assert(tx.Slot, Equals, uint64(77))
}

func (s *RPCCovSuite) TestGetSignatureStatus(c *C) {
	server, r := newTestServer(rpcCovFixed(`{"result":{"value":[{"slot":9,"confirmations":3,"err":null,"confirmationStatus":"confirmed"}]}}`))
	st, err := r.GetSignatureStatus("sig")
	c.Assert(err, IsNil)
	c.Assert(st, NotNil)
	c.Assert(st.Slot, Equals, uint64(9))
	c.Assert(*st.Confirmations, Equals, uint64(3))
	c.Assert(st.ConfirmationStatus, Equals, "confirmed")
	server.Close()

	server, r = newTestServer(rpcCovFixed(`{"result":{"value":[null]}}`))
	st, err = r.GetSignatureStatus("sig")
	c.Assert(err, IsNil)
	c.Assert(st, IsNil)
	server.Close()

	server, r = newTestServer(rpcCovFixed(`{"result":{"value":[]}}`))
	st, err = r.GetSignatureStatus("sig")
	c.Assert(err, IsNil)
	c.Assert(st, IsNil)
	server.Close()

	server, r = newTestServer(rpcCovFixed(`{"error":{"code":-5,"message":"x"}}`))
	_, err = r.GetSignatureStatus("sig")
	c.Assert(err, ErrorMatches, "RPC Error -5: x")
	server.Close()

	server, r = newTestServer(rpcCovFail())
	_, err = r.GetSignatureStatus("sig")
	c.Assert(err, ErrorMatches, "failed to call getSignatureStatuses.*")
	server.Close()
}

// --- get_account_info.go ---

func (s *RPCCovSuite) TestGetNonceAccountPostError(c *C) {
	server, r := newTestServer(rpcCovFail())
	defer server.Close()
	_, _, err := r.GetNonceAccount("pk", 0)
	c.Assert(err, ErrorMatches, "failed to call getAccountInfo.*")
	c.Assert(errors.Is(err, ErrNonceAccountNotProvisioned), Equals, false)
}
