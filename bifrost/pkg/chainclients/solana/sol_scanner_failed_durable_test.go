package solana

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"time"

	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/solana/rpc"
	"gitlab.com/thorchain/thornode/v3/bifrost/pubkeymanager"
	stypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
)

type ScannerFailedDurableSuite struct{}

var _ = Suite(&ScannerFailedDurableSuite{})

// bakedNonce is the durable nonce a failed tx carried in its recentBlockhash field;
// attestedNonce (the post-tx value the RPC serves) differs from it.
const bakedNonce = "4uQeVj5tqViQh7yWWGStvkEG1Zmhx6uasJtWCJziofM"

// poolAddrs is a pool address validator over a fixed set of SOL addresses.
type poolAddrs struct {
	pubkeymanager.PubKeyValidator
	addrs map[string]bool
}

func (p poolAddrs) IsValidPoolAddress(addr string, chain common.Chain) (bool, common.ChainPoolInfo) {
	return chain.Equals(common.SOLChain) && p.addrs[addr], common.ChainPoolInfo{}
}

// nonceAccountRPC serves getAccountInfo for a durable-nonce account: the post-tx
// nonce with the given authority, a missing account, or an RPC error.
type nonceAccountRPC struct {
	authority string
	nonce     string
	missing   bool
	fail      bool
	calls     atomic.Int32
}

func (n *nonceAccountRPC) server() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string `json:"method"`
		}
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &req)
		if req.Method != "getAccountInfo" {
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":null}`))
			return
		}
		n.calls.Add(1)
		switch {
		case n.fail:
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"error":{"code":-32005,"message":"node is behind"}}`))
		case n.missing:
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"context":{"slot":101},"value":null}}`))
		default:
			_, _ = fmt.Fprint(w, nonceAccountValueJSON(n.authority, n.nonce))
		}
	}))
}

func nonceAccountValueJSON(authority, nonce string) string {
	return fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"result":{"context":{"slot":101},"value":{"owner":"11111111111111111111111111111111","data":{"program":"nonce","parsed":{"type":"initialized","info":{"authority":%q,"blockhash":%q}}}}}}`, authority, nonce)
}

// failedDurableOutbound is a vault's durable outbound (durableOutboundTx) that landed
// and failed in its transfer: the fee was charged and the nonce advanced.
func failedDurableOutbound(c *C, vault, nonceAcct string) *rpc.TransactionResult {
	dest, _ := attestFixture(c)
	tx := durableOutboundTx(vault, dest, nonceAcct)
	tx.Meta.Err = map[string]interface{}{"InstructionError": []interface{}{2, map[string]interface{}{"Custom": 1}}}
	tx.Meta.PreBalances = []uint64{1_000_000_000, 0, 1_500_000, 1, 1, 1}
	tx.Meta.PostBalances = []uint64{1_000_000_000 - 5000, 0, 1_500_000, 1, 1, 1}
	tx.Transaction.Message.RecentBlockhash = bakedNonce
	return tx
}

func failedDurableScanner(c *C, n *nonceAccountRPC, pools ...string) (*SOLScanner, *httptest.Server) {
	srv := n.server()
	scanner := newTestScanner()
	scanner.solRpc = rpc.NewSolRPC(srv.URL, time.Second)
	addrs := map[string]bool{}
	for _, p := range pools {
		addrs[p] = true
	}
	scanner.pubKeyMgr = poolAddrs{addrs: addrs}
	return scanner, srv
}

// TestFailedDurableVaultTxObserved: a vault's durable tx that landed and FAILED still
// paid its fee and advanced the vault's nonce. It is observed in THORChain's
// nonce-maintenance shape (isSolNonceCreateTx: from the vault to its own nonce
// account, no memo, one SOL coin, the post-tx nonce attested), so THORChain commits
// the new nonce, books the fee as gas and re-issues the failed outbound at once,
// instead of the vault's durable outbounds staying wedged until re-attestation.
func (s *ScannerFailedDurableSuite) TestFailedDurableVaultTxObserved(c *C) {
	vault, nonceAcct := attestFixture(c)
	n := &nonceAccountRPC{authority: vault, nonce: attestedNonce}
	scanner, srv := failedDurableScanner(c, n, vault)
	defer srv.Close()

	txIn, err := scanner.getTxIn(100, []*rpc.TransactionResult{failedDurableOutbound(c, vault, nonceAcct)})
	c.Assert(err, IsNil)
	c.Assert(txIn.TxArray, HasLen, 1)
	item := txIn.TxArray[0]
	c.Check(item.Tx, Equals, testTxSig)
	c.Check(item.BlockHeight, Equals, int64(100))
	c.Check(item.Sender, Equals, vault)
	c.Check(item.To, Equals, nonceAcct)
	c.Check(item.Memo, Equals, "")
	// The fee (5000 lamports = 500 in 1e8) is booked exactly: 1 unit as the coin
	// (an observation needs a non-zero coin), the rest as gas.
	c.Assert(item.Coins, HasLen, 1)
	c.Check(item.Coins[0].Asset.Equals(common.SOLAsset), Equals, true)
	c.Check(item.Coins[0].Amount.Uint64(), Equals, uint64(1))
	c.Assert(item.Gas, HasLen, 1)
	c.Check(item.Gas[0].Asset.Equals(common.SOLAsset), Equals, true)
	c.Check(item.Gas[0].Amount.Uint64(), Equals, uint64(499))
	c.Assert(item.SolNonce, NotNil)
	c.Check(item.SolNonce.NonceAccount, Equals, nonceAcct)
	c.Check(item.SolNonce.Nonce, Equals, attestedNonce)
	c.Check(item.SolNonce.Slot, Equals, int64(100))
	c.Check(n.calls.Load(), Equals, int32(1))
}

// TestFailedTxNotObservedOtherwise: a failed tx is observed only when it is provably
// a vault durable tx that consumed the vault's own nonce. Anything else stays
// unobserved, as before; none of these shapes needs a nonce read except where the
// nonce itself decides.
func (s *ScannerFailedDurableSuite) TestFailedTxNotObservedOtherwise(c *C) {
	vault, nonceAcct := attestFixture(c)
	check := func(desc string, n *nonceAccountRPC, pools []string, mutate func(*rpc.TransactionResult), wantReads int32) {
		scanner, srv := failedDurableScanner(c, n, pools...)
		defer srv.Close()
		tx := failedDurableOutbound(c, vault, nonceAcct)
		if mutate != nil {
			mutate(tx)
		}
		txIn, err := scanner.getTxIn(100, []*rpc.TransactionResult{tx})
		c.Check(err, IsNil, Commentf(desc))
		c.Check(txIn.TxArray, HasLen, 0, Commentf(desc))
		c.Check(n.calls.Load(), Equals, wantReads, Commentf(desc))
	}
	good := func() *nonceAccountRPC { return &nonceAccountRPC{authority: vault, nonce: attestedNonce} }

	check("fee payer is not a vault", good(), nil, nil, 0)
	check("no AdvanceNonceAccount (a recent-blockhash tx)", good(), []string{vault}, func(tx *rpc.TransactionResult) {
		tx.Transaction.Message.Instructions = tx.Transaction.Message.Instructions[1:]
	}, 0)
	check("AdvanceNonceAccount is not instruction 0", good(), []string{vault}, func(tx *rpc.TransactionResult) {
		ins := tx.Transaction.Message.Instructions
		ins[0], ins[1] = ins[1], ins[0]
	}, 0)
	check("nonce authority is not the fee payer", good(), []string{vault}, func(tx *rpc.TransactionResult) {
		tx.Transaction.Message.Instructions[0].Accounts = []int{2, 4, 1}
	}, 0)
	foreign, _ := attestFixture(c)
	check("not the vault's own seed-derived nonce account", good(), []string{vault}, func(tx *rpc.TransactionResult) {
		tx.Transaction.Message.AccountKeys[2] = foreign
	}, 0)
	check("on-chain authority is not the vault", &nonceAccountRPC{authority: foreign, nonce: attestedNonce}, []string{vault}, nil, 1)
	check("nonce did not advance (re-issuing copies of a live nonce could pay twice)",
		&nonceAccountRPC{authority: vault, nonce: bakedNonce}, []string{vault}, nil, 1)
	check("nonce account no longer exists (left to re-attestation)", &nonceAccountRPC{missing: true}, []string{vault}, nil, 1)
}

// TestFailedDurableNonceReadErrorFailsScan: a transient read failure must not drop the
// observation (the vault would stay wedged); the scan fails so the tx is re-read.
func (s *ScannerFailedDurableSuite) TestFailedDurableNonceReadErrorFailsScan(c *C) {
	vault, nonceAcct := attestFixture(c)
	scanner, srv := failedDurableScanner(c, &nonceAccountRPC{fail: true}, vault)
	defer srv.Close()
	_, err := scanner.getTxIn(100, []*rpc.TransactionResult{failedDurableOutbound(c, vault, nonceAcct)})
	c.Check(err, NotNil)
}

// TestScanVaultObservesFailedDurableVaultTx drives scanVault: the vault's failed
// durable tx comes back from the vault's signature query and is queued as the
// nonce-maintenance observation, where it used to be skipped.
func (s *ScannerFailedDurableSuite) TestScanVaultObservesFailedDurableVaultTx(c *C) {
	vault, nonceAcct := attestFixture(c)
	n := &scanRPC{nonceRPC: nonceRPC{authority: vault}, tx: failedDurableOutbound(c, vault, nonceAcct)}
	srv := n.server(c)
	defer srv.Close()
	scanner := newTestScanner()
	scanner.cfg.ChainID = common.SOLChain
	scanner.solRpc = rpc.NewSolRPC(srv.URL, time.Second)
	scanner.pubKeyMgr = poolAddrs{addrs: map[string]bool{vault: true}}
	scanner.globalTxsQueue = make(chan stypes.TxIn, 10)

	var wg sync.WaitGroup
	wg.Add(1)
	okQ, counts, sigs := make([]bool, 1), make([]int, 1), make([]string, 1)
	healthy := true
	scanner.scanVault(vault, 0, "", 0, func() { healthy = false }, counts, okQ, sigs, &wg)
	wg.Wait()
	c.Assert(okQ[0], Equals, true)
	c.Assert(healthy, Equals, true)
	c.Assert(sigs[0], Equals, testTxSig)
	c.Assert(scanner.globalTxsQueue, HasLen, 1)
	txIn := <-scanner.globalTxsQueue
	c.Assert(txIn.TxArray, HasLen, 1)
	item := txIn.TxArray[0]
	c.Check(item.To, Equals, nonceAcct)
	c.Check(item.Memo, Equals, "")
	c.Assert(item.SolNonce, NotNil)
	c.Check(item.SolNonce.Nonce, Equals, attestedNonce)
}
