package solana

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/solana/rpc"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	types2 "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// testHaltSOLTokens is the HaltSOLTokens value SolTestSuite's mock thornode serves (0:
// SPL live), and testHaltSOLTokensFail makes it answer with a malformed body.
var (
	testHaltSOLTokens     atomic.Int64
	testHaltSOLTokensFail atomic.Bool
)

// HaltSOLTokens pauses SPL: set to a THORChain height, it is in force once that height
// is reached, as Halt<Chain>Chain is read. 0 or unset (-1) means SPL is live.
func (s *SPLScannerSuite) TestSPLHaltActive(c *C) {
	for _, tc := range []struct {
		halt, height int64
		want         bool
	}{
		{-1, 100, false}, // unset
		{0, 100, false},
		{150, 100, false}, // scheduled, not reached yet
		{100, 100, true},
		{50, 100, true},
	} {
		c.Check(splHaltActive(tc.halt, tc.height), Equals, tc.want, Commentf("halt %d at height %d", tc.halt, tc.height))
	}
}

// While SPL is halted, a token deposit from outside the vaults is not observed: the
// vault token account's scan, paused meanwhile, observes it once the halt is cleared.
// A vault's own token transfer (an outbound signed before the halt, a migration) is
// observed regardless.
func (s *SPLScannerSuite) TestHaltedDepositNotObservedVaultTransferIs(c *C) {
	scanner := newSPLTestScanner()
	scanner.pubKeyMgr = splVaultAddrs{addrs: map[string]bool{testVault: true}}
	scanner.spl.halted.Store(true)
	c.Check(scanner.getTxInItem(splInbound(c, 12_345_678), 100), IsNil)
	out := scanner.getTxInItem(splVaultOutbound(c, testVault, 25_000_000), 200)
	c.Assert(out, NotNil)
	c.Check(out.Sender, Equals, testVault)

	scanner.spl.halted.Store(false)
	c.Check(scanner.getTxInItem(splInbound(c, 12_345_678), 100), NotNil)
}

// haltMimirBridge serves a THORChain height and the HaltSOLTokens mimir (every other
// mimir reads 0); scan()'s pause and SPL halt checks use nothing else.
type haltMimirBridge struct {
	thorclient.ThorchainBridge
	mu           sync.Mutex
	height, halt int64
}

func (b *haltMimirBridge) GetBlockHeight() (int64, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.height, nil
}

func (b *haltMimirBridge) GetMimir(key string) (int64, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if key == constants.MimirKeyHaltSOLTokens {
		return b.halt, nil
	}
	return 0, nil
}

func (b *haltMimirBridge) setHalt(v int64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.halt = v
}

// TestScanPausesSPLDepositsWhileHalted drives scan() through a halt: while it is in
// force the vault's token account is not scanned, and a deposit the vault wallet's
// scan returns (one that created the token account) is not observed. Once the halt is
// cleared, the token account's scan observes both deposits made meanwhile, each once.
func (s *BlockScannerTestSuite) TestScanPausesSPLDepositsWhileHalted(c *C) {
	vault := types2.GetRandomEd25519PubKey()
	vaultAddr, err := vault.GetAddress(common.SOLChain)
	c.Assert(err, IsNil)
	tokenAccount := ata(c, vaultAddr.String(), testUSDCMint)

	const plainSig = "HaltedPlainDeposit1111111111111111111111111111111111111111111111111"
	const creatingSig = "HaltedCreatingDeposit1111111111111111111111111111111111111111111111"
	plain := splInboundTo(c, vaultAddr.String(), 6_000_000)
	plain.Transaction.Signatures = []string{plainSig}
	creating := splInboundTo(c, vaultAddr.String(), 7_000_000)
	creating.Transaction.Signatures = []string{creatingSig}
	m := &splScanRPC{txs: map[string]*rpc.TransactionResult{plainSig: plain, creatingSig: creating}, getTxHit: map[string]int{}}
	sigs := map[string][]rpc.SignaturesForAddressResult{
		vaultAddr.String(): {{Signature: creatingSig, Slot: 950}},
		tokenAccount:       {{Signature: creatingSig, Slot: 950}, {Signature: plainSig, Slot: 900}},
	}
	m.set(1000, sigs)
	server := m.server(c)
	defer server.Close()

	scanner, _, txIns := s.newTokenScanTestScanner(c, server, vault)
	scanner.pubKeyMgr = singleVaultPubKeys{
		vault:           vault,
		PubKeyValidator: splVaultAddrs{addrs: map[string]bool{vaultAddr.String(): true}},
	}
	bridge := &haltMimirBridge{height: 100, halt: 50}
	scanner.bridge = bridge
	scanner.lastMimirCheck = time.Time{} // read the mimirs this pass

	// observed lists the transactions a pass reported (a transaction read but not
	// observed is queued as an empty batch, which the observer ignores).
	observed := func() []string {
		var out []string
		for _, txIn := range drainTxIns(txIns) {
			for _, item := range txIn.TxArray {
				out = append(out, item.Tx)
			}
		}
		sort.Strings(out)
		return out
	}

	scanner.scan()
	c.Check(observed(), HasLen, 0)
	c.Check(m.getTxHit[plainSig], Equals, 0) // the token account was not scanned
	c.Check(scanner.IsHealthy(), Equals, true)

	bridge.setHalt(0)
	scanner.lastMimirCheck = time.Time{}
	m.set(1100, sigs)
	time.Sleep(50 * time.Millisecond) // scan status writes are async
	scanner.scan()
	c.Check(observed(), DeepEquals, []string{creatingSig, plainSig})
}

// splHaltSignServer is a SOL RPC that serves the consensus blockhash, a recipient with
// no account yet and the token account rent, as an SPL outbound to a wallet needs.
func splHaltSignServer() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
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
}

// TestSPLSigningPausedWhileHalted: HaltSOLTokens, read at the keysign height so every
// party decides alike, pauses SPL signing. A halted SPL outbound is dropped for
// reschedule and remembered, so it holds no later durable sibling behind it; SOL
// outbounds keep signing; a halt scheduled above the keysign height is not in force
// yet; and an unreadable mimir retains the outbound rather than sign through.
func (s *SolTestSuite) TestSPLSigningPausedWhileHalted(c *C) {
	server := splHaltSignServer()
	defer server.Close()
	defer testHaltSOLTokens.Store(0)
	defer testHaltSOLTokensFail.Store(false)

	e, pub, pubkeyMgr := s.newSPLSignerTestClient(c, server)
	defer func() { c.Assert(pubkeyMgr.Stop(), IsNil) }()
	e.spl = testSPLRegistry(testUSDCMint)
	memo := func(i int) string {
		return fmt.Sprintf("OUT:%d000000000000000000000000000000000000000000000000000000000000000", i)
	}

	testHaltSOLTokens.Store(1) // in force from height 1
	halted := splTestTxOut(c, pub, 2_500_000_000)
	halted.Memo = memo(1)
	raw, checkpoint, obs, err := e.SignTx(halted, 1)
	c.Assert(err, IsNil)
	c.Check(raw, IsNil)
	c.Check(checkpoint, IsNil)
	c.Check(obs, IsNil)
	c.Check(e.durableRejected(pendingOf(halted, 1)), Equals, true)

	sol := splTestTxOut(c, pub, 2_500_000_000)
	sol.Coins = common.Coins{common.NewCoin(common.SOLAsset, cosmos.NewUint(11047250))}
	sol.Memo = memo(2)
	raw, _, obs, err = e.SignTx(sol, 1)
	c.Assert(err, IsNil)
	c.Check(raw, NotNil)
	c.Check(obs, NotNil)

	testHaltSOLTokens.Store(2) // scheduled above the keysign height
	scheduled := splTestTxOut(c, pub, 2_500_000_000)
	scheduled.Memo = memo(3)
	raw, _, _, err = e.SignTx(scheduled, 1)
	c.Assert(err, IsNil)
	c.Check(raw, NotNil)

	testHaltSOLTokensFail.Store(true)
	unreadable := splTestTxOut(c, pub, 2_500_000_000)
	unreadable.Memo = memo(4)
	raw, _, _, err = e.SignTx(unreadable, 1)
	c.Check(err, NotNil)
	c.Check(raw, IsNil)

	testHaltSOLTokensFail.Store(false)
	testHaltSOLTokens.Store(0) // cleared
	cleared := splTestTxOut(c, pub, 2_500_000_000)
	cleared.Memo = memo(5)
	raw, _, _, err = e.SignTx(cleared, 1)
	c.Assert(err, IsNil)
	c.Check(raw, NotNil)
}
