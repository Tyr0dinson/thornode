package solana

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"

	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

// testToken2022Program is the Token-2022 program, which also owns token accounts and
// mints.
const testToken2022Program = "TokenzQdBNbLqP5VEhdkAS6EPFLC1PHnBqCXEpPxuEb" //nolint:gosec // G101: a public Solana address, not a credential

// TestSPLOutboundToTokenAccountNeverSigned: an SPL outbound pays the recipient's
// associated token account, derived from the recipient address as a wallet. If that
// address is itself a token account or a mint (owned by a token program), the derived
// account belongs to an address nobody can sign for, and the tokens can never be
// moved again. Bifrost refuses to sign such an outbound, so the tokens stay in the
// vault and the outbound stays pending instead of being lost. A failed read of the
// recipient account is retried, never signed through. SOL outbounds are unaffected:
// lamports sent to a token account go back to its owner when it is closed.
func (s *SolTestSuite) TestSPLOutboundToTokenAccountNeverSigned(c *C) {
	recipientOwner := "" // program owning the account at the recipient address; "" = none
	failRead := false
	server := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		body, _ := io.ReadAll(req.Body)
		var r struct {
			Method string            `json:"method"`
			Params []json.RawMessage `json:"params"`
		}
		_ = json.Unmarshal(body, &r)
		raw, _ := json.Marshal(r.Params)
		if res, ok := consensusBlockhashRPCResponse(r.Method, raw); ok {
			_, _ = fmt.Fprintf(rw, `{"jsonrpc":"2.0","id":1,"result":%s}`, res)
			return
		}
		switch r.Method {
		case "getAccountInfo":
			var addr string
			if len(r.Params) > 0 {
				_ = json.Unmarshal(r.Params[0], &addr)
			}
			switch {
			case addr == testRecipient && failRead:
				_, _ = rw.Write([]byte(`{"jsonrpc":"2.0","id":1,"error":{"code":-32005,"message":"node is behind"}}`))
			case addr == testRecipient && recipientOwner != "":
				_, _ = fmt.Fprintf(rw, `{"jsonrpc":"2.0","id":1,"result":{"context":{"slot":1},"value":{"owner":%q,"executable":false,"lamports":2039280,"space":165,"data":["","base64"]}}}`, recipientOwner)
			default:
				_, _ = rw.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"context":{"slot":1},"value":null}}`))
			}
		case "getMinimumBalanceForRentExemption":
			_, _ = rw.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":2039280}`))
		default:
			_, _ = rw.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":null}`))
		}
	}))
	defer server.Close()

	e, pub, pubkeyMgr := s.newSPLSignerTestClient(c, server)
	defer func() { c.Assert(pubkeyMgr.Stop(), IsNil) }()
	e.spl = testSPLRegistry(testUSDCMint)
	txOut := splTestTxOut(c, pub, 2_500_000_000)

	// The recipient address is a token account or a mint: never signed, dropped without
	// an error so this node does not retry it every pass.
	for _, owner := range []string{testTokenProgram, testToken2022Program} {
		recipientOwner = owner
		raw, checkpoint, obs, err := e.SignTx(txOut, 1)
		c.Check(err, IsNil, Commentf("owner %s", owner))
		c.Check(raw, IsNil, Commentf("owner %s", owner))
		c.Check(checkpoint, IsNil, Commentf("owner %s", owner))
		c.Check(obs, IsNil, Commentf("owner %s", owner))
	}

	// SOL to the same address is still sent.
	sol := txOut
	sol.Coins = common.Coins{common.NewCoin(common.SOLAsset, cosmos.NewUint(11047250))}
	sol.Memo = "OUT:6D91ADAFA69765E7805B5FF2F3A0BA1DBE69E37A1CFCD20C48B99C528AA3EE87"
	raw, _, obs, err := e.SignTx(sol, 1)
	c.Assert(err, IsNil)
	c.Check(raw, NotNil)
	c.Check(obs, NotNil)

	// The recipient account cannot be read: retained with an error and retried, not
	// signed.
	recipientOwner = ""
	failRead = true
	raw, _, _, err = e.SignTx(txOut, 1)
	c.Check(err, NotNil)
	c.Check(raw, IsNil)

	// An ordinary wallet, with or without an account yet, is paid.
	failRead = false
	for _, owner := range []string{"", testSystemProgram} {
		recipientOwner = owner
		txOut.Memo = fmt.Sprintf("OUT:7D91ADAFA69765E7805B5FF2F3A0BA1DBE69E37A1CFCD20C48B99C528AA3EE8%d", len(owner)%10)
		raw, _, obs, err = e.SignTx(txOut, 1)
		c.Assert(err, IsNil, Commentf("owner %q", owner))
		c.Check(raw, NotNil, Commentf("owner %q", owner))
		c.Check(obs, NotNil, Commentf("owner %q", owner))
	}
}
