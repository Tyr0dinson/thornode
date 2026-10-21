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
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
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
//   - the token account's history before the SPL activation slot is skipped (but the
//     cursor moves past it);
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
		tokenAccount: {{Signature: oldSig, Slot: 900}}, // predates activation
	})
	server := m.server(c)
	defer server.Close()

	scanner, storage, txIns := s.newTokenScanTestScanner(c, server, vault, 950)

	// Pass 1: the slot-900 deposit is not observed, and the cursor moves to it.
	scanner.scan()
	c.Check(drainTxIns(txIns), HasLen, 0)
	c.Check(m.getTxHit[oldSig], Equals, 0)
	c.Check(scanner.IsHealthy(), Equals, true)
	time.Sleep(50 * time.Millisecond) // scan status writes are async
	sig, _, err := storage.GetScanStatus(tokenAccount)
	c.Assert(err, IsNil)
	c.Check(sig, Equals, oldSig)

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

// TestScanTokenAccountObservesDepositsBeforeFirstScan: the floor is the activation
// slot, not when this node first scans the token account. Nodes that are not members
// learn of a churned-in vault only once it is active and published in
// inbound_addresses, and a node that was offline over a churn learns of it later
// still. A deposit to such a vault's token account made before this node's first
// scan lists only the token account, so it must still be observed, or it could never
// reach consensus.
func (s *BlockScannerTestSuite) TestScanTokenAccountObservesDepositsBeforeFirstScan(c *C) {
	vault := types2.GetRandomEd25519PubKey()
	vaultAddr, err := vault.GetAddress(common.SOLChain)
	c.Assert(err, IsNil)
	tokenAccount := ata(c, vaultAddr.String(), testUSDCMint)

	const earlySig = "EarlyDeposit11111111111111111111111111111111111111111111111111111111"
	early := splInboundTo(c, vaultAddr.String(), 3_000_000)
	early.Transaction.Signatures = []string{earlySig}
	m := &splScanRPC{txs: map[string]*rpc.TransactionResult{earlySig: early}, getTxHit: map[string]int{}}
	// The deposit landed at slot 900, before this node's first scan at slot 1000; it
	// references the token account only, so only the token account query returns it.
	m.set(1000, map[string][]rpc.SignaturesForAddressResult{
		tokenAccount: {{Signature: earlySig, Slot: 900}},
	})
	server := m.server(c)
	defer server.Close()

	scanner, storage, txIns := s.newTokenScanTestScanner(c, server, vault, 800)
	// This node scanned the vault before; it makes no difference to its token account.
	c.Assert(storage.SetScanStatus(vaultAddr.String(), "", 850), IsNil)
	scanner.scan()
	got := drainTxIns(txIns)
	c.Assert(got, HasLen, 1)
	c.Assert(got[0].TxArray, HasLen, 1)
	c.Check(got[0].TxArray[0].Tx, Equals, earlySig)
	c.Check(got[0].TxArray[0].To, Equals, vaultAddr.String())
	c.Check(scanner.IsHealthy(), Equals, true)
	time.Sleep(50 * time.Millisecond) // scan status writes are async
	sig, _, err := storage.GetScanStatus(tokenAccount)
	c.Assert(err, IsNil)
	c.Check(sig, Equals, earlySig)
}

// TestScanTokenAccountObservesLateIndexedDeposits: a token account with no
// signatures yet keeps the activation slot as its floor as the scan advances, across
// a restart, so a deposit an RPC indexes late is still observed.
func (s *BlockScannerTestSuite) TestScanTokenAccountObservesLateIndexedDeposits(c *C) {
	vault := types2.GetRandomEd25519PubKey()
	vaultAddr, err := vault.GetAddress(common.SOLChain)
	c.Assert(err, IsNil)
	tokenAccount := ata(c, vaultAddr.String(), testUSDCMint)

	const lateSig = "LateIndexedDeposit111111111111111111111111111111111111111111111111111"
	late := splInboundTo(c, vaultAddr.String(), 4_000_000)
	late.Transaction.Signatures = []string{lateSig}
	m := &splScanRPC{txs: map[string]*rpc.TransactionResult{lateSig: late}, getTxHit: map[string]int{}}
	m.set(1000, nil)
	server := m.server(c)
	defer server.Close()

	scanner, _, txIns := s.newTokenScanTestScanner(c, server, vault, 1000)
	scanner.scan()
	m.set(3000, nil)
	scanner.scan()
	c.Check(drainTxIns(txIns), HasLen, 0)

	// The scanner restarts (in-memory state lost); the deposit at slot 2000 (below the
	// latest scan slot, above the activation slot) is indexed only now.
	time.Sleep(50 * time.Millisecond) // scan status writes are async
	scanner.vaultScanStatuses = map[string]vaultScanStatus{}
	m.set(4000, map[string][]rpc.SignaturesForAddressResult{tokenAccount: {{Signature: lateSig, Slot: 2000}}})
	scanner.scan()
	got := drainTxIns(txIns)
	c.Assert(got, HasLen, 1)
	c.Check(got[0].TxArray[0].Tx, Equals, lateSig)
}

// TestScanObservesVaultTokenTransferWhenInactive drives scan() with SPL off (the
// SOLTokens mimir turned back off after activation): the vault's token account is
// not scanned, but the vault's own token outbound lists the vault wallet, so the
// wallet's scan returns it, and it is observed.
func (s *BlockScannerTestSuite) TestScanObservesVaultTokenTransferWhenInactive(c *C) {
	vault := types2.GetRandomEd25519PubKey()
	vaultAddr, err := vault.GetAddress(common.SOLChain)
	c.Assert(err, IsNil)

	const outSig = "VaultTokenOutbound11111111111111111111111111111111111111111111111111"
	out := splVaultOutbound(c, vaultAddr.String(), 25_000_000)
	out.Transaction.Signatures = []string{outSig}
	m := &splScanRPC{txs: map[string]*rpc.TransactionResult{outSig: out}, getTxHit: map[string]int{}}
	m.set(1000, map[string][]rpc.SignaturesForAddressResult{
		vaultAddr.String(): {{Signature: outSig, Slot: 900}},
	})
	server := m.server(c)
	defer server.Close()

	scanner, _, txIns := s.newTokenScanTestScanner(c, server, vault, 0)
	scanner.pubKeyMgr = singleVaultPubKeys{
		vault:           vault,
		PubKeyValidator: splVaultAddrs{addrs: map[string]bool{vaultAddr.String(): true}},
	}
	scanner.scan()
	got := drainTxIns(txIns)
	c.Assert(got, HasLen, 1)
	c.Assert(got[0].TxArray, HasLen, 1)
	item := got[0].TxArray[0]
	c.Check(item.Tx, Equals, outSig)
	c.Check(item.Sender, Equals, vaultAddr.String())
	c.Check(item.To, Equals, testSender)
	c.Check(scanner.IsHealthy(), Equals, true)
}

// newTokenScanTestScanner builds a scanner over one vault with USDC whitelisted and
// SPL active from activationSlot (as by the SOLTokens mimir), served by m, and
// returns it with its storage and observation queue.
func (s *BlockScannerTestSuite) newTokenScanTestScanner(c *C, server *httptest.Server, vault common.PubKey, activationSlot uint64) (*SOLScanner, *blockscanner.BlockScannerStorageSolana, chan stypes.TxIn) {
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
	scanner.spl.activeFrom.Store(activationSlot)
	txIns := make(chan stypes.TxIn, 10)
	scanner.globalTxsQueue = txIns
	scanner.lastMimirCheck = time.Now() // skip the mimir reads
	return scanner, storage, txIns
}

// mimirBridge serves THORChain mimirs (unset reads 0) and a block height; scan()'s
// pause and SPL gate checks use nothing else.
type mimirBridge struct {
	thorclient.ThorchainBridge
	mu     sync.Mutex
	values map[string]int64
}

func (b *mimirBridge) GetBlockHeight() (int64, error) { return 100, nil }

func (b *mimirBridge) GetMimir(key string) (int64, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.values[key], nil
}

func (b *mimirBridge) set(key string, v int64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.values[key] = v
}

// TestScanSPLGatedBySOLTokensMimir: until THORChain's SOLTokens mimir is set, the
// scanner neither scans vault token accounts nor reports token deposits from outside
// the vaults (TestScanObservesVaultTokenTransferWhenInactive covers the vaults' own
// transfers, which are observed regardless). A thornode
// without SPL support has no such mimir, so a Bifrost upgraded early never feeds it
// SPL deposits it would credit (and later pay out through signers that cannot sign
// SPL). Once the mimir is set, its value is the activation slot: deposits before it
// are never observed, on every node alike.
func (s *BlockScannerTestSuite) TestScanSPLGatedBySOLTokensMimir(c *C) {
	vault := types2.GetRandomEd25519PubKey()
	vaultAddr, err := vault.GetAddress(common.SOLChain)
	c.Assert(err, IsNil)
	tokenAccount := ata(c, vaultAddr.String(), testUSDCMint)

	const depositSig = "GatedDeposit11111111111111111111111111111111111111111111111111111111"
	const laterSig = "EnabledDeposit111111111111111111111111111111111111111111111111111111"
	deposit := splInboundTo(c, vaultAddr.String(), 6_000_000)
	deposit.Transaction.Signatures = []string{depositSig}
	later := splInboundTo(c, vaultAddr.String(), 7_000_000)
	later.Transaction.Signatures = []string{laterSig}
	m := &splScanRPC{txs: map[string]*rpc.TransactionResult{depositSig: deposit, laterSig: later}, getTxHit: map[string]int{}}
	m.set(1000, map[string][]rpc.SignaturesForAddressResult{
		tokenAccount: {{Signature: depositSig, Slot: 1500}},
	})
	server := m.server(c)
	defer server.Close()

	scanner, storage, txIns := s.newTokenScanTestScanner(c, server, vault, 0)
	bridge := &mimirBridge{values: map[string]int64{}}
	scanner.bridge = bridge
	scanner.lastMimirCheck = time.Time{} // read the mimirs this pass

	scanner.scan()
	c.Check(drainTxIns(txIns), HasLen, 0)
	c.Check(m.getTxHit[depositSig], Equals, 0)
	_, _, err = storage.GetScanStatus(tokenAccount)
	c.Check(err, NotNil) // never tracked

	bridge.set("SOLTokens", 2000)
	scanner.lastMimirCheck = time.Time{}
	m.set(2200, map[string][]rpc.SignaturesForAddressResult{
		tokenAccount: {{Signature: depositSig, Slot: 1500}},
	})
	scanner.scan()
	c.Check(scanner.spl.activationSlot(), Equals, uint64(2000))
	c.Check(drainTxIns(txIns), HasLen, 0) // before the activation slot
	c.Check(m.getTxHit[depositSig], Equals, 0)

	m.set(3000, map[string][]rpc.SignaturesForAddressResult{
		tokenAccount: {{Signature: laterSig, Slot: 2500}, {Signature: depositSig, Slot: 1500}},
	})
	scanner.scan()
	got := drainTxIns(txIns)
	c.Assert(got, HasLen, 1)
	c.Check(got[0].TxArray[0].Tx, Equals, laterSig)
	c.Check(m.getTxHit[depositSig], Equals, 0)
}
