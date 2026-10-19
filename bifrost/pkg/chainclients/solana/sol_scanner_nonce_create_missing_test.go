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
	stypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
)

type ScannerNonceCreateMissingSuite struct{}

var _ = Suite(&ScannerNonceCreateMissingSuite{})

// missingNonceScanRPC serves one signature whose tx is tx, and reports every account
// as missing (getAccountInfo value null), counting those reads.
type missingNonceScanRPC struct {
	tx    *rpc.TransactionResult
	reads atomic.Int32
}

func (n *missingNonceScanRPC) server(c *C) *httptest.Server {
	txJSON, err := json.Marshal(n.tx)
	c.Assert(err, IsNil)
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Method string `json:"method"`
		}
		_ = json.Unmarshal(body, &req)
		w.Header().Set("Content-Type", "application/json")
		switch req.Method {
		case "getSignaturesForAddress":
			_, _ = fmt.Fprintf(w, `{"jsonrpc":"2.0","id":1,"result":[{"signature":%q,"slot":100}]}`, testTxSig)
		case "getTransaction":
			_, _ = fmt.Fprintf(w, `{"jsonrpc":"2.0","id":1,"result":%s}`, txJSON)
		case "getAccountInfo":
			n.reads.Add(1)
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"context":{"slot":101},"value":null}}`))
		default:
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":null}`))
		}
	}))
}

// observedNothing reports whether the scan queued no observation (an empty TxIn,
// which the observer drops, is not one).
func observedNothing(scanner *SOLScanner) bool {
	for {
		select {
		case txIn := <-scanner.globalTxsQueue:
			if len(txIn.TxArray) != 0 {
				return false
			}
		default:
			return true
		}
	}
}

// scanOnce runs one scanVault pass of addr and reports whether it succeeded, whether
// the scanner stayed healthy, and the cursor it saved.
func scanOnce(scanner *SOLScanner, addr string) (ok, healthy bool, cursor string) {
	var wg sync.WaitGroup
	wg.Add(1)
	okQ, counts, sigs := make([]bool, 1), make([]int, 1), make([]string, 1)
	healthy = true
	scanner.scanVault(addr, 0, "", 0, nil, func() { healthy = false }, counts, okQ, sigs, &wg)
	wg.Wait()
	return okQ[0], healthy, sigs[0]
}

func missingNonceScanner(c *C, n *missingNonceScanRPC, vaults ...string) (*SOLScanner, *httptest.Server) {
	srv := n.server(c)
	scanner := newTestScanner()
	scanner.cfg.ChainID = common.SOLChain
	scanner.solRpc = rpc.NewSolRPC(srv.URL, time.Second)
	addrs := map[string]bool{}
	for _, v := range vaults {
		addrs[v] = true
	}
	scanner.pubKeyMgr = poolAddrs{addrs: addrs}
	scanner.globalTxsQueue = make(chan stypes.TxIn, 10)
	return scanner, srv
}

// zly (!5273): an outsider funds and initializes its OWN seed-derived nonce account in
// a tx that also lists a vault address (the System Program ignores trailing accounts,
// so the tx is in the vault's signature history), then closes the account. Scanners
// read the tx after finalization and find the account missing. A creation funded by a
// non-vault is never observed, so it must not be read at all, and the vault's scan
// must move past it.
func (s *ScannerNonceCreateMissingSuite) TestOutsiderNonceCreationDoesNotStallVaultScan(c *C) {
	vault, _ := attestFixture(c)
	outsider, outsiderNonce := attestFixture(c)
	trap := nonceCreateTx(outsider, outsiderNonce)
	trap.Transaction.Message.AccountKeys = append(trap.Transaction.Message.AccountKeys, vault)
	ins := trap.Transaction.Message.Instructions
	ins[1].Accounts = append(ins[1].Accounts, len(trap.Transaction.Message.AccountKeys)-1)

	n := &missingNonceScanRPC{tx: trap}
	scanner, srv := missingNonceScanner(c, n, vault)
	defer srv.Close()

	c.Assert(scanner.getTxInItem(trap, 100), IsNil)
	c.Check(n.reads.Load(), Equals, int32(0), Commentf("an outsider's nonce account must not be read"))

	ok, healthy, cursor := scanOnce(scanner, vault)
	c.Check(ok, Equals, true)
	c.Check(healthy, Equals, true)
	c.Check(cursor, Equals, testTxSig, Commentf("the vault's cursor must move past the outsider's tx"))
	c.Check(observedNothing(scanner), Equals, true)
}

// A vault's own nonce-account creation whose account has since been closed (a retired
// vault reclaims its rent) is in the vault's history for good. Re-reading it must not
// fail the scan every pass: the account is gone, so nothing durable can wedge on it,
// as failedDurableTxInItem already assumes for a missing account.
func (s *ScannerNonceCreateMissingSuite) TestClosedVaultNonceCreationDoesNotStallScan(c *C) {
	vault, nonceAcct := attestFixture(c)
	n := &missingNonceScanRPC{tx: nonceCreateTx(vault, nonceAcct)}
	scanner, srv := missingNonceScanner(c, n, vault)
	defer srv.Close()

	ok, healthy, cursor := scanOnce(scanner, vault)
	c.Check(ok, Equals, true)
	c.Check(healthy, Equals, true)
	c.Check(cursor, Equals, testTxSig)
	c.Check(observedNothing(scanner), Equals, true)
}
