package solana

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"time"

	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/bifrost/blockscanner"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/signercache"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/solana/rpc"
	"gitlab.com/thorchain/thornode/v3/bifrost/pubkeymanager"
	stypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/config"
	types2 "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// singleVaultPubKeys serves one vault to the scanner; scan() uses nothing else.
type singleVaultPubKeys struct {
	pubkeymanager.PubKeyValidator
	vault common.PubKey
}

func (p singleVaultPubKeys) GetAlgoPubKeys(common.SigningAlgo, bool) common.PubKeys {
	return common.PubKeys{p.vault}
}

// splScanRPC serves getSlot, per-address getSignaturesForAddress (honouring the
// until cursor) and getTransaction, and counts getTransaction calls per signature.
type splScanRPC struct {
	mu       sync.Mutex
	slot     uint64
	sigs     map[string][]rpc.SignaturesForAddressResult // address -> newest first
	txs      map[string]*rpc.TransactionResult
	getTxHit map[string]int
}

func (m *splScanRPC) server(c *C) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		body, _ := io.ReadAll(req.Body)
		var r struct {
			Method string            `json:"method"`
			Params []json.RawMessage `json:"params"`
		}
		_ = json.Unmarshal(body, &r)
		m.mu.Lock()
		defer m.mu.Unlock()
		write := func(v interface{}) {
			b, _ := json.Marshal(v)
			_, _ = fmt.Fprintf(rw, `{"jsonrpc":"2.0","id":1,"result":%s}`, b)
		}
		switch r.Method {
		case "getSlot":
			write(m.slot)
		case "getSignaturesForAddress":
			var addr string
			_ = json.Unmarshal(r.Params[0], &addr)
			var p rpc.GetSignaturesForAddressUntilParams
			_ = json.Unmarshal(r.Params[1], &p)
			out := []rpc.SignaturesForAddressResult{}
			for _, s := range m.sigs[addr] {
				if s.Signature == p.Until {
					break
				}
				out = append(out, s)
			}
			write(out)
		case "getTransaction":
			var sig string
			_ = json.Unmarshal(r.Params[0], &sig)
			m.getTxHit[sig]++
			write(m.txs[sig])
		default:
			write(nil)
		}
	}))
}

func (m *splScanRPC) set(slot uint64, sigs map[string][]rpc.SignaturesForAddressResult) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.slot = slot
	m.sigs = sigs
}

func drainTxIns(ch chan stypes.TxIn) []stypes.TxIn {
	var out []stypes.TxIn
	for {
		select {
		case t := <-ch:
			out = append(out, t)
		default:
			return out
		}
	}
}

// TestScanTokenAccounts drives scan() over a vault and its USDC token account:
//   - a token account seen for the first time is registered at the current slot,
//     and its pre-existing history is skipped (but the cursor moves past it);
//   - a new transfer that lists both the vault and its token account is observed
//     once, though both addresses return it.
func (s *BlockScannerTestSuite) TestScanTokenAccounts(c *C) {
	vault := types2.GetRandomEd25519PubKey()
	vaultAddr, err := vault.GetAddress(common.SOLChain)
	c.Assert(err, IsNil)
	tokenAccount := ata(c, vaultAddr.String(), testUSDCMint)

	const oldSig, newSig = "OldDeposit111111111111111111111111111111111111111111111111111111111", "NewDeposit111111111111111111111111111111111111111111111111111111111"
	old := splInboundTo(c, vaultAddr.String(), 1_000_000)
	old.Transaction.Signatures = []string{oldSig}
	fresh := splInboundTo(c, vaultAddr.String(), 2_000_000)
	fresh.Transaction.Signatures = []string{newSig}

	m := &splScanRPC{
		txs:      map[string]*rpc.TransactionResult{oldSig: old, newSig: fresh},
		getTxHit: map[string]int{},
	}
	m.set(1000, map[string][]rpc.SignaturesForAddressResult{
		tokenAccount: {{Signature: oldSig, Slot: 900}}, // predates tracking
	})
	server := m.server(c)
	defer server.Close()

	storage, err := blockscanner.NewBlockScannerStorageSolana("", config.LevelDBOptions{})
	c.Assert(err, IsNil)
	scm, err := signercache.NewSignerCacheManager(storage.GetInternalDb())
	c.Assert(err, IsNil)
	cfg := getConfigForTest("http://" + server.Listener.Addr().String())
	cfg.StartBlockHeight = 0
	cfg.WhitelistTokens = []string{testUSDCMint}
	cfg.Solana.ScanInterval = time.Millisecond
	cfg.Solana.FeeUpdateInterval = 1_000_000_000
	rpcClient := rpc.NewSolRPC("http://"+server.Listener.Addr().String(), cfg.HTTPRequestTimeout)
	scanner, err := NewSOLScanner(make(chan struct{}), cfg, storage, s.bridge, s.m, rpcClient,
		singleVaultPubKeys{vault: vault}, func(int64) error { return nil }, scm)
	c.Assert(err, IsNil)
	scanner.spl = testSPLRegistry(testUSDCMint) // independent of the build's token list
	txIns := make(chan stypes.TxIn, 10)
	scanner.globalTxsQueue = txIns
	scanner.lastMimirCheck = time.Now() // skip the halt mimir reads

	// Pass 1: the token account is registered at slot 1000; the slot-900 deposit is
	// not observed, and the cursor moves to it.
	scanner.scan()
	c.Check(drainTxIns(txIns), HasLen, 0)
	c.Check(m.getTxHit[oldSig], Equals, 0)
	c.Check(scanner.IsHealthy(), Equals, true)
	time.Sleep(50 * time.Millisecond) // scan status writes are async
	sig, slot, err := storage.GetScanStatus(tokenAccount)
	c.Assert(err, IsNil)
	c.Check(sig, Equals, oldSig)
	c.Check(slot, Equals, uint64(1000))

	// Pass 2: a deposit that lists both the vault and its token account (it created
	// the token account) comes back from both queries but is observed once.
	m.set(2000, map[string][]rpc.SignaturesForAddressResult{
		vaultAddr.String(): {{Signature: newSig, Slot: 1500}},
		tokenAccount:       {{Signature: newSig, Slot: 1500}, {Signature: oldSig, Slot: 900}},
	})
	scanner.scan()
	got := drainTxIns(txIns)
	c.Assert(got, HasLen, 1)
	c.Assert(got[0].TxArray, HasLen, 1)
	item := got[0].TxArray[0]
	c.Check(item.Tx, Equals, newSig)
	c.Check(item.To, Equals, vaultAddr.String())
	c.Check(item.Coins[0].Asset.String(), Equals, testUSDCAssetStr)
	c.Check(m.getTxHit[newSig], Equals, 1)
	time.Sleep(50 * time.Millisecond)
	for _, addr := range []string{vaultAddr.String(), tokenAccount} {
		sig, _, err = storage.GetScanStatus(addr)
		c.Assert(err, IsNil)
		c.Check(sig, Equals, newSig, Commentf(addr))
	}
}

// TestScanTokenAccountFloorHoldsUntilFirstSignature: a registered token account with
// no signatures keeps its registration slot as its floor while the scan advances, so
// a transfer that lands later is observed even if an RPC indexes it late.
func (s *BlockScannerTestSuite) TestScanTokenAccountFloorHoldsUntilFirstSignature(c *C) {
	vault := types2.GetRandomEd25519PubKey()
	vaultAddr, err := vault.GetAddress(common.SOLChain)
	c.Assert(err, IsNil)
	tokenAccount := ata(c, vaultAddr.String(), testUSDCMint)

	const sig1 = "FirstDeposit11111111111111111111111111111111111111111111111111111111"
	tx := splInboundTo(c, vaultAddr.String(), 5_000_000)
	tx.Transaction.Signatures = []string{sig1}
	m := &splScanRPC{txs: map[string]*rpc.TransactionResult{sig1: tx}, getTxHit: map[string]int{}}
	m.set(1000, nil)
	server := m.server(c)
	defer server.Close()

	storage, err := blockscanner.NewBlockScannerStorageSolana("", config.LevelDBOptions{})
	c.Assert(err, IsNil)
	scm, err := signercache.NewSignerCacheManager(storage.GetInternalDb())
	c.Assert(err, IsNil)
	cfg := getConfigForTest("http://" + server.Listener.Addr().String())
	cfg.StartBlockHeight = 0
	cfg.WhitelistTokens = []string{testUSDCMint}
	cfg.Solana.ScanInterval = time.Millisecond
	cfg.Solana.FeeUpdateInterval = 1_000_000_000
	rpcClient := rpc.NewSolRPC("http://"+server.Listener.Addr().String(), cfg.HTTPRequestTimeout)
	scanner, err := NewSOLScanner(make(chan struct{}), cfg, storage, s.bridge, s.m, rpcClient,
		singleVaultPubKeys{vault: vault}, func(int64) error { return nil }, scm)
	c.Assert(err, IsNil)
	scanner.spl = testSPLRegistry(testUSDCMint) // independent of the build's token list
	txIns := make(chan stypes.TxIn, 10)
	scanner.globalTxsQueue = txIns
	scanner.lastMimirCheck = time.Now()

	scanner.scan() // registers at 1000
	m.set(3000, nil)
	scanner.scan() // nothing yet; floor stays at 1000
	time.Sleep(50 * time.Millisecond)
	sig, slot, err := storage.GetScanStatus(tokenAccount)
	c.Assert(err, IsNil)
	c.Check(sig, Equals, "")
	c.Check(slot, Equals, uint64(1000))

	// A transfer at slot 2000 (below the latest scan slot, above the floor) shows up
	// only now, as from a lagging RPC: it is still observed.
	m.set(4000, map[string][]rpc.SignaturesForAddressResult{tokenAccount: {{Signature: sig1, Slot: 2000}}})
	scanner.scan()
	got := drainTxIns(txIns)
	c.Assert(got, HasLen, 1)
	c.Check(got[0].TxArray[0].Tx, Equals, sig1)
}
