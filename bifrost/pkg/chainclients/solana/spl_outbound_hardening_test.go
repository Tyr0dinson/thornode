package solana

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"

	"github.com/mr-tron/base58"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/solana/rpc"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/solana/types"
	stypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
)

// An SPL outbound creates the recipient's associated token account if it is missing
// (CreateIdempotent) and transfers into it. Two recipient states make it fail on chain
// every time: SPL Token lets an account's owner be reassigned, after which
// CreateIdempotent fails with InvalidOwner, and an issuer can freeze the account, after
// which the transfer fails. Broadcast preflight keeps such a transaction from landing,
// but only after a keysign, every signing period, for good. The signer refuses it
// before keysign, as a dropped outbound remembered as durable-rejected. The check only
// saves keysigns, so a failed read of the recipient's token account fails open, as the
// durable pre-sign filter's reads do (TestSPLPreSignFilter).
func (s *SolTestSuite) TestSPLOutboundToUndeliverableTokenAccountDropped(c *C) {
	destATA := ""
	destValue := "null" // getAccountInfo value of the recipient's token account
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
			case addr == destATA && failRead:
				_, _ = rw.Write([]byte(`{"jsonrpc":"2.0","id":1,"error":{"code":-32005,"message":"node is behind"}}`))
			case addr == destATA:
				_, _ = fmt.Fprintf(rw, `{"jsonrpc":"2.0","id":1,"result":{"context":{"slot":1},"value":%s}}`, destValue)
			default:
				_, _ = rw.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"context":{"slot":1},"value":null}}`))
			}
		case "getMinimumBalanceForRentExemption":
			_, _ = rw.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":1488440}`))
		default:
			_, _ = rw.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":null}`))
		}
	}))
	defer server.Close()

	e, pub, pubkeyMgr := s.newSPLSignerTestClient(c, server)
	defer func() { c.Assert(pubkeyMgr.Stop(), IsNil) }()
	e.spl = testSPLRegistry(testUSDCMint)
	destATA = ata(c, testRecipient, testUSDCMint)
	const height = int64(1)

	newOut := func(i int) (tx stypes.TxOutItem) {
		tx = splTestTxOut(c, pub, 2_500_000_000)
		tx.Memo = fmt.Sprintf("OUT:7D91ADAFA69765E7805B5FF2F3A0BA1DBE69E37A1CFCD20C48B99C528AA3EE8%d", i)
		return tx
	}

	someoneElse := ata(c, testSender, testUSDCMint) // any address other than the recipient
	for i, tc := range []struct {
		name  string
		value string
	}{
		{"owner reassigned", tokenAcct(testUSDCMint, someoneElse, "0", "initialized")},
		{"frozen", tokenAcct(testUSDCMint, testRecipient, "0", "frozen")},
	} {
		destValue = tc.value
		txOut := newOut(i)
		raw, checkpoint, obs, err := e.SignTx(txOut, height)
		c.Check(err, IsNil, Commentf(tc.name))
		c.Check(raw, IsNil, Commentf(tc.name))
		c.Check(checkpoint, IsNil, Commentf(tc.name))
		c.Check(obs, IsNil, Commentf(tc.name))
		c.Check(e.durableRejected(pendingOf(txOut, height)), Equals, true, Commentf(tc.name))
	}

	// The recipient's token account cannot be read: signed anyway (preflight still
	// keeps an undeliverable transaction from landing).
	failRead = true
	raw, _, _, err := e.SignTx(newOut(5), height)
	c.Check(err, IsNil)
	c.Check(raw, NotNil)
	failRead = false

	// A missing token account, or the recipient's own unfrozen one, is paid.
	for i, value := range []string{"null", tokenAcct(testUSDCMint, testRecipient, "0", "initialized")} {
		destValue = value
		raw, _, obs, err := e.SignTx(newOut(6+i), height)
		c.Assert(err, IsNil, Commentf("value %s", value))
		c.Check(raw, NotNil, Commentf("value %s", value))
		c.Check(obs, NotNil, Commentf("value %s", value))
	}
}

// testComputeBudgetProgram is Solana's ComputeBudget program.
const testComputeBudgetProgram = "ComputeBudget111111111111111111111111111111"

// An SPL outbound pays no priority fee, so under congestion the leader's scheduler ranks
// it by its fee per requested compute unit. Without an explicit limit it requests the
// default 200,000 units per non-builtin instruction (memo, CreateIdempotent,
// TransferChecked: about 600,000), while it uses well under 50,000 (CreateIdempotent
// 13,400 to 15,000 units when it creates the account, p-token transfers a few hundred,
// measured on mainnet). Both builders set a 200,000-unit limit, once; the durable one
// keeps AdvanceNonceAccount as instruction 0.
func (s *SPLScannerSuite) TestSPLOutboundSetsComputeUnitLimit(c *C) {
	vault := types.MustPublicKeyFromString(testVault)
	recipient := types.MustPublicKeyFromString(testRecipient)
	mint := types.MustPublicKeyFromString(testUSDCMint)
	params := types.SPLTransferParams{
		Vault:     vault,
		Recipient: recipient,
		Mint:      mint,
		SourceATA: types.MustPublicKeyFromString(ata(c, testVault, testUSDCMint)),
		DestATA:   types.MustPublicKeyFromString(ata(c, testRecipient, testUSDCMint)),
		Amount:    25_000_000,
		Decimals:  6,
		Memo:      "OUT:7D91ADAFA69765E7805B5FF2F3A0BA1DBE69E37A1CFCD20C48B99C528AA3EE87",
	}
	nonceAcct := types.MustPublicKeyFromString(ata(c, testVault, testUSDTMint)) // any address
	for name, tx := range map[string]types.Transaction{
		"recent blockhash": types.NewSPLTransferTransaction(params, "9rAtxuhtKn8qagc3UtZFyhLrw5zkh6etv43TibaXuSKo"),
		"durable nonce": types.NewNonceSPLTransferTransaction(params, types.NonceParams{
			Value: "9rAtxuhtKn8qagc3UtZFyhLrw5zkh6etv43TibaXuSKo", Account: nonceAcct, Authority: vault,
		}),
	} {
		m := tx.Message
		limits := 0
		for _, ins := range m.Instructions {
			if m.Accounts[ins.ProgramIDIndex].String() != testComputeBudgetProgram {
				continue
			}
			limits++
			c.Assert(ins.Data, HasLen, 5, Commentf(name))
			c.Check(ins.Data[0], Equals, byte(2), Commentf("%s: SetComputeUnitLimit", name))
			c.Check(binary.LittleEndian.Uint32(ins.Data[1:5]), Equals, uint32(200_000), Commentf(name))
			c.Check(ins.Accounts, HasLen, 0, Commentf(name))
		}
		c.Check(limits, Equals, 1, Commentf(name))
		if name == "durable nonce" {
			c.Check(m.Accounts[m.Instructions[0].ProgramIDIndex].String(), Equals, testSystemProgram)
		}
	}
}

// The scanner reads a vault's SPL outbound that carries the compute-unit limit as before.
func (s *SPLScannerSuite) TestVaultOutboundWithComputeUnitLimitObserved(c *C) {
	txn := splVaultOutbound(c, testVault, 25_000_000)
	limit := make([]byte, 5)
	limit[0] = 2
	binary.LittleEndian.PutUint32(limit[1:], 200_000)
	txn.Transaction.Message.AccountKeys = append(txn.Transaction.Message.AccountKeys, testComputeBudgetProgram)
	txn.Meta.PreBalances = append(txn.Meta.PreBalances, 1)
	txn.Meta.PostBalances = append(txn.Meta.PostBalances, 1)
	txn.Transaction.Message.Instructions = append([]rpc.RPCInstruction{
		{ProgramIdIndex: len(txn.Transaction.Message.AccountKeys) - 1, Accounts: []int{}, Data: base58.Encode(limit)},
	}, txn.Transaction.Message.Instructions...)

	item := newSPLTestScanner().getTxInItem(txn, 200)
	c.Assert(item, NotNil)
	c.Check(item.Sender, Equals, testVault)
	c.Check(item.Coins[0].Amount.String(), Equals, "2500000000")
}
