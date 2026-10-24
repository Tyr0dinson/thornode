package solana

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"

	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	stypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

// TestSignTxDropsDoNotHoldDurableSiblings: an outbound that SignTx drops before the
// durable-nonce path (an asset this node cannot send, an SPL self-send, an SPL
// recipient that is a token account) is never signed by this node, so it must not hold
// the vault's later durable siblings behind it in the serialize gate. THORChain keeps
// such an item OutHash-empty and, while the vault's nonce has not moved, reschedules it
// on the same vault ahead of its siblings: if the gate waited on it, every later SOL
// outbound of the vault would wait forever.
func (s *SolTestSuite) TestSignTxDropsDoNotHoldDurableSiblings(c *C) {
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
			if addr == testRecipient {
				// The recipient address is itself a token account.
				_, _ = fmt.Fprintf(rw, `{"jsonrpc":"2.0","id":1,"result":{"context":{"slot":1},"value":{"owner":%q,"executable":false,"lamports":2039280,"space":165,"data":["","base64"]}}}`, testTokenProgram)
				return
			}
			_, _ = rw.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"context":{"slot":1},"value":null}}`))
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
	vaultAddr, err := pub.GetAddress(common.SOLChain)
	c.Assert(err, IsNil)
	usdt, err := common.NewAsset("SOL.USDT-" + strings.ToUpper(testUSDTMint))
	c.Assert(err, IsNil)
	const wallet = "9xQeWvG816bUx9EPjHmaT23yvVM2ZWbrrpZb9PusVFin"

	toTokenAccount := splTestTxOut(c, pub, 2_500_000_000)
	selfSend := splTestTxOut(c, pub, 2_500_000_000)
	selfSend.ToAddress = vaultAddr
	selfSend.Memo = "OUT:5D91ADAFA69765E7805B5FF2F3A0BA1DBE69E37A1CFCD20C48B99C528AA3EE87"
	unsendable := splTestTxOut(c, pub, 2_500_000_000)
	unsendable.ToAddress = wallet
	unsendable.Coins = common.Coins{common.NewCoin(usdt, cosmos.NewUint(2_500_000_000))}
	unsendable.Memo = "OUT:6D91ADAFA69765E7805B5FF2F3A0BA1DBE69E37A1CFCD20C48B99C528AA3EE87"

	const height = int64(100)
	for _, tc := range []struct {
		name string
		bad  stypes.TxOutItem
	}{
		{"recipient is a token account", toTokenAccount},
		{"SPL self-send", selfSend},
		{"asset this node cannot send", unsendable},
	} {
		raw, checkpoint, obs, err := e.SignTx(tc.bad, height)
		c.Assert(err, IsNil, Commentf(tc.name))
		c.Assert(raw, IsNil, Commentf(tc.name))
		c.Assert(checkpoint, IsNil, Commentf(tc.name))
		c.Assert(obs, IsNil, Commentf(tc.name)) // dropped for reschedule
		c.Check(e.durableRejected(pendingOf(tc.bad, height)), Equals, true, Commentf(tc.name))

		// A later SOL outbound of the same vault must not wait behind the dropped item.
		next := stypes.TxOutItem{
			Chain: common.SOLChain, ToAddress: wallet, VaultPubKey: pub, VaultPubKeyEddsa: pub,
			Coins:  common.Coins{common.NewCoin(common.SOLAsset, cosmos.NewUint(11047250))},
			Memo:   "OUT:7D91ADAFA69765E7805B5FF2F3A0BA1DBE69E37A1CFCD20C48B99C528AA3EE87",
			InHash: "7D91ADAFA69765E7805B5FF2F3A0BA1DBE69E37A1CFCD20C48B99C528AA3EE87",
		}
		bridge := e.bridge
		e.bridge = &pendingBridge{items: []thorclient.PendingOutbound{pendingOf(tc.bad, height), pendingOf(next, height+1)}}
		c.Check(e.durableSerializeAllows(next, height+1), Equals, true, Commentf(tc.name))
		e.bridge = bridge
	}
}
