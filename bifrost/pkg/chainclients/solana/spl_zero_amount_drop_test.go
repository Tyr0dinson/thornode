package solana

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"

	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	stypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

// zly (!5273): an SPL outbound whose amount is zero in the token's base units (zero, or
// below one base unit once converted from THORChain's 1e8 units) can never be signed.
// SignTx returned an error for it, so the signer retained it and retried it every pass,
// and on a durable vault the serialize gate held the vault's later outbounds behind it.
// It must be dropped and remembered as durable-rejected, like the other SPL drops. So
// must an amount too large for the mint's base units.
func (s *SolTestSuite) TestSPLZeroAmountDroppedWithoutHoldingDurableSiblings(c *C) {
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
	const wallet = "9xQeWvG816bUx9EPjHmaT23yvVM2ZWbrrpZb9PusVFin"

	const height = int64(100)
	// More base units than the u64 a token transfer carries: no node can sign it either.
	overflow, err := cosmos.ParseUint("1844674407370955161600") // (2^64-1)*100+100 in 1e8 units at 6 decimals
	c.Assert(err, IsNil)
	for _, tc := range []struct {
		name   string
		amount cosmos.Uint
	}{
		{"zero", cosmos.ZeroUint()},
		{"below one base unit", cosmos.NewUint(50)}, // 0.0000005 USDC: zero at 6 decimals
		{"overflows the base units", overflow},
	} {
		bad := splTestTxOut(c, pub, 1)
		bad.Coins[0].Amount = tc.amount
		bad.ToAddress = wallet
		raw, checkpoint, obs, err := e.SignTx(bad, height)
		c.Assert(err, IsNil, Commentf(tc.name))
		c.Assert(raw, IsNil, Commentf(tc.name))
		c.Assert(checkpoint, IsNil, Commentf(tc.name))
		c.Assert(obs, IsNil, Commentf(tc.name)) // dropped for reschedule
		c.Check(e.durableRejected(pendingOf(bad, height)), Equals, true, Commentf(tc.name))

		// A later SOL outbound of the same vault must not wait behind the dropped item.
		next := stypes.TxOutItem{
			Chain: common.SOLChain, ToAddress: wallet, VaultPubKey: pub, VaultPubKeyEddsa: pub,
			Coins:  common.Coins{common.NewCoin(common.SOLAsset, cosmos.NewUint(11047250))},
			Memo:   "OUT:7D91ADAFA69765E7805B5FF2F3A0BA1DBE69E37A1CFCD20C48B99C528AA3EE87",
			InHash: "7D91ADAFA69765E7805B5FF2F3A0BA1DBE69E37A1CFCD20C48B99C528AA3EE87",
		}
		bridge := e.bridge
		e.bridge = &pendingBridge{items: []thorclient.PendingOutbound{pendingOf(bad, height), pendingOf(next, height+1)}}
		c.Check(e.durableSerializeAllows(next, height+1), Equals, true, Commentf(tc.name))
		e.bridge = bridge
	}
}
