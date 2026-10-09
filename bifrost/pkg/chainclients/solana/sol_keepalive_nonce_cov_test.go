package solana

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"time"

	cosmoscryptoed25519 "github.com/cosmos/cosmos-sdk/crypto/keys/ed25519"
	"github.com/mr-tron/base58"
	"github.com/rs/zerolog/log"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/solana/types"
	"gitlab.com/thorchain/thornode/v3/bifrost/pubkeymanager"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain"
	types2 "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// -------------------------------------------------------------------------
// shared fakes
// -------------------------------------------------------------------------

// covSolRPC is a JSON-RPC fake: per-method overrides return a full response body;
// otherwise the deterministic consensus-blockhash methods are served and anything
// else answers "result": null.
type covSolRPC struct {
	mu        sync.Mutex
	calls     map[string]int
	overrides map[string]func(params []json.RawMessage) string
}

func newCovSolRPC() *covSolRPC {
	return &covSolRPC{calls: map[string]int{}, overrides: map[string]func([]json.RawMessage) string{}}
}

func (m *covSolRPC) set(method string, fn func(params []json.RawMessage) string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.overrides[method] = fn
}

func (m *covSolRPC) clear(method string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.overrides, method)
}

func (m *covSolRPC) setResult(method, result string) {
	m.set(method, func([]json.RawMessage) string {
		return fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"result":%s}`, result)
	})
}

func (m *covSolRPC) setError(method, msg string) {
	m.set(method, func([]json.RawMessage) string {
		return fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"error":{"code":-32000,"message":%q}}`, msg)
	})
}

func (m *covSolRPC) count(method string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls[method]
}

func (m *covSolRPC) ServeHTTP(rw http.ResponseWriter, req *http.Request) {
	var rpcReq struct {
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
	}
	if err := json.NewDecoder(req.Body).Decode(&rpcReq); err != nil {
		http.Error(rw, err.Error(), http.StatusBadRequest)
		return
	}
	var p []json.RawMessage
	_ = json.Unmarshal(rpcReq.Params, &p)
	m.mu.Lock()
	m.calls[rpcReq.Method]++
	fn, ok := m.overrides[rpcReq.Method]
	m.mu.Unlock()
	if ok {
		_, _ = rw.Write([]byte(fn(p)))
		return
	}
	if result, handled := consensusBlockhashRPCResponse(rpcReq.Method, rpcReq.Params); handled {
		_, _ = fmt.Fprintf(rw, `{"jsonrpc":"2.0","id":1,"result":%s}`, result)
		return
	}
	_, _ = rw.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":null}`))
}

// covBridge serves the bridge calls keep-alive and provisioning make.
type covBridge struct {
	*mockSOLThorchainBridge
	nodeStatus types2.NodeStatus
	nodeErr    error
	vaults     types2.Vaults
	asgardErr  error
	mimirs     map[string]int64
	mimirErr   map[string]error
}

func (b *covBridge) FetchNodeStatus() (types2.NodeStatus, error) { return b.nodeStatus, b.nodeErr }

func (b *covBridge) GetAsgards() (types2.Vaults, error) {
	return append(types2.Vaults(nil), b.vaults...), b.asgardErr
}

func (b *covBridge) GetMimir(key string) (int64, error) {
	if err := b.mimirErr[key]; err != nil {
		return 0, err
	}
	return b.mimirs[key], nil
}

// covPendingBridge additionally lists the pending outbound queue.
type covPendingBridge struct {
	*covBridge
	pending    []thorclient.PendingOutbound
	pendingErr error
}

func (b *covPendingBridge) GetPendingOutbounds(int64) ([]thorclient.PendingOutbound, error) {
	return b.pending, b.pendingErr
}

type KeepAliveNonceCovSuite struct {
	oldLookupDelay time.Duration
}

var _ = Suite(&KeepAliveNonceCovSuite{})

func (s *KeepAliveNonceCovSuite) SetUpSuite(c *C) {
	thorchain.SetupConfigForTest()
	// broadcast failures would otherwise wait 3s before the landed-anyway lookup
	s.oldLookupDelay = broadcastLookupDelay
	broadcastLookupDelay = 0
}

func (s *KeepAliveNonceCovSuite) TearDownSuite(c *C) {
	broadcastLookupDelay = s.oldLookupDelay
}

// covFixture is a client whose local ed25519 key owns the vault, so signing is local
// (no TSS) and the broadcast goes to the fake RPC.
type covFixture struct {
	client    *SOLClient
	rpc       *covSolRPC
	server    *httptest.Server
	bridge    *covBridge
	vault     types2.Vault
	vaultAddr string
	nodePK    common.PubKey
}

func newCovFixture(c *C) *covFixture {
	priv := cosmoscryptoed25519.GenPrivKey()
	bech, err := cosmos.Bech32ifyPubKey(cosmos.Bech32PubKeyTypeAccPub, priv.PubKey())
	c.Assert(err, IsNil)
	eddsa, err := common.NewPubKey(bech)
	c.Assert(err, IsNil)
	addr, err := eddsa.GetAddress(common.SOLChain)
	c.Assert(err, IsNil)
	c.Assert(addr.String(), Equals, base58.Encode(priv.PubKey().Bytes()))

	nodePK := types2.GetRandomPubKey()
	vault := types2.Vault{
		PubKey:      types2.GetRandomPubKey(),
		PubKeyEddsa: eddsa,
		Status:      types2.VaultStatus_ActiveVault,
		Membership:  []string{nodePK.String()},
	}

	m := newCovSolRPC()
	server := httptest.NewServer(m)
	bridge := &covBridge{
		mockSOLThorchainBridge: &mockSOLThorchainBridge{},
		nodeStatus:             types2.NodeStatus_Active,
		vaults:                 types2.Vaults{vault},
		mimirs:                 map[string]int64{constants.MimirKeySignatureRefreshBlocks: 1},
		mimirErr:               map[string]error{},
	}
	pkm, err := pubkeymanager.NewPubKeyManager(bridge, nil)
	c.Assert(err, IsNil)
	pkm.AddNodePubKey(nodePK, common.SigningAlgoSecp256k1)

	healthy := &atomic.Bool{}
	healthy.Store(true)
	client := &SOLClient{
		logger:             log.Logger,
		localPubKey:        addr.String(),
		k:                  priv,
		bridge:             bridge,
		pubkeyMgr:          pkm,
		rpcClient:          newSolRPCForServer(server),
		solScanner:         &SOLScanner{healthy: healthy},
		signerCacheManager: newTestSignerCacheManager(c),
	}
	return &covFixture{client: client, rpc: m, server: server, bridge: bridge, vault: vault, vaultAddr: addr.String(), nodePK: nodePK}
}

// -------------------------------------------------------------------------
// keep-alive
// -------------------------------------------------------------------------

const covAnchorHeight = int64(500)

// staleSigs makes the vault's newest signature far older than the refresh threshold.
func (f *covFixture) staleSigs() {
	f.rpc.setResult("getSignaturesForAddress", `[{"signature":"oldsig","slot":1}]`)
}

func (s *KeepAliveNonceCovSuite) TestKeepAliveVaultBroadcastsSelfSend(c *C) {
	if notMocknetCov() {
		c.Skip("mocknet only: keep-alive cadence uses mocknet settings")
	}
	f := newCovFixture(c)
	defer f.server.Close()
	f.staleSigs()
	f.rpc.setResult("getBalance", `{"context":{"slot":1},"value":100000000000}`)
	f.rpc.setResult("sendTransaction", `"keepalive-sig"`)

	f.client.keepAliveCheck(1_000_000, covAnchorHeight)
	c.Assert(f.rpc.count("sendTransaction"), Equals, 1)

	// A second round for the same anchor height is served from the signer cache
	// (same TxOutItem hash), so nothing is broadcast again.
	f.client.keepAliveCheck(1_000_000, covAnchorHeight)
	c.Assert(f.rpc.count("sendTransaction"), Equals, 1)
}

func (s *KeepAliveNonceCovSuite) TestKeepAliveVaultSkipsWhenNotDue(c *C) {
	f := newCovFixture(c)
	defer f.server.Close()

	// no history: nothing to keep fresh
	f.rpc.setResult("getSignaturesForAddress", `[]`)
	c.Assert(f.client.keepAliveVault(f.vault, 1_000_000, 1, covAnchorHeight), IsNil)
	c.Assert(f.rpc.count("getBalance"), Equals, 0)

	// fresh signature: not due
	f.rpc.setResult("getSignaturesForAddress", `[{"signature":"new","slot":999990}]`)
	c.Assert(f.client.keepAliveVault(f.vault, 1_000_000, 1, covAnchorHeight), IsNil)
	c.Assert(f.rpc.count("getBalance"), Equals, 0)
	c.Assert(f.rpc.count("sendTransaction"), Equals, 0)
}

func (s *KeepAliveNonceCovSuite) TestKeepAliveVaultErrors(c *C) {
	f := newCovFixture(c)
	defer f.server.Close()

	f.rpc.setError("getSignaturesForAddress", "sigs down")
	err := f.client.keepAliveVault(f.vault, 1_000_000, 1, covAnchorHeight)
	c.Assert(err, ErrorMatches, "fail to get newest signature.*sigs down.*")

	f.staleSigs()
	f.rpc.setError("getBalance", "balance down")
	err = f.client.keepAliveVault(f.vault, 1_000_000, 1, covAnchorHeight)
	c.Assert(err, ErrorMatches, "fail to get vault balance.*")

	// balance below amount + fee margin: skipped without signing
	f.rpc.setResult("getBalance", `{"context":{"slot":1},"value":1}`)
	c.Assert(f.client.keepAliveVault(f.vault, 1_000_000, 1, covAnchorHeight), IsNil)
	c.Assert(f.rpc.count("sendTransaction"), Equals, 0)

	// signing fails: consensus blockhash cannot be derived
	f.rpc.setResult("getBalance", `{"context":{"slot":1},"value":100000000000}`)
	f.rpc.setError("getSlot", "slot down")
	err = f.client.keepAliveVault(f.vault, 1_000_000, 1, covAnchorHeight)
	c.Assert(err, ErrorMatches, "fail to sign keep-alive tx.*")

	// broadcast fails
	f.rpc.clear("getSlot")
	f.rpc.setError("sendTransaction", "rejected")
	f.rpc.setError("getTransaction", "missing")
	err = f.client.keepAliveVault(f.vault, 1_000_000, 1, covAnchorHeight+1)
	c.Assert(err, ErrorMatches, "fail to broadcast keep-alive tx.*")

	// a vault whose eddsa key cannot form an address
	bad := f.vault
	bad.PubKeyEddsa = common.PubKey("bogus")
	err = f.client.keepAliveVault(bad, 1_000_000, 1, covAnchorHeight)
	c.Assert(err, ErrorMatches, "fail to get vault address.*")
}

func (s *KeepAliveNonceCovSuite) TestKeepAliveVaultNotSelected(c *C) {
	f := newCovFixture(c)
	defer f.server.Close()
	f.staleSigs()
	f.rpc.setResult("getBalance", `{"context":{"slot":1},"value":100000000000}`)
	// remote signing that is not selected returns no signature and no error
	f.client.localPubKey = "not-local"
	f.client.tssKeyManager = &mockSOLThorchainKeyManager{}
	c.Assert(f.client.keepAliveVault(f.vault, 1_000_000, 1, covAnchorHeight), IsNil)
	c.Assert(f.rpc.count("sendTransaction"), Equals, 0)
}

func (s *KeepAliveNonceCovSuite) TestKeepAliveCheckGates(c *C) {
	cases := map[string]func(f *covFixture){
		"mimir error":       func(f *covFixture) { f.bridge.mimirErr[constants.MimirKeySignatureRefreshBlocks] = errors.New("x") },
		"disabled":          func(f *covFixture) { f.bridge.mimirs[constants.MimirKeySignatureRefreshBlocks] = 0 },
		"node status err":   func(f *covFixture) { f.bridge.nodeErr = errors.New("x") },
		"node not active":   func(f *covFixture) { f.bridge.nodeStatus = types2.NodeStatus_Standby },
		"signing halted":    func(f *covFixture) { f.bridge.thorHeight = 100; f.bridge.mimirs["HALTSIGNING"] = 1 },
		"scanner unhealthy": func(f *covFixture) { f.client.solScanner.healthy.Store(false) },
		"no node pubkey": func(f *covFixture) {
			pkm, err := pubkeymanager.NewPubKeyManager(f.bridge, nil)
			if err == nil {
				f.client.pubkeyMgr = pkm
			}
		},
		"asgards error": func(f *covFixture) { f.bridge.asgardErr = errors.New("x") },
		"not a member":  func(f *covFixture) { f.bridge.vaults[0].Membership = nil },
		"no eddsa key":  func(f *covFixture) { f.bridge.vaults[0].PubKeyEddsa = "" },
	}
	for name, mutate := range cases {
		f := newCovFixture(c)
		f.staleSigs()
		mutate(f)
		f.client.keepAliveCheck(1_000_000, covAnchorHeight)
		c.Check(f.rpc.count("getSignaturesForAddress"), Equals, 0, Commentf(name))
		f.server.Close()
	}

	// a per-vault failure is logged and does not abort the check
	f := newCovFixture(c)
	defer f.server.Close()
	f.rpc.setError("getSignaturesForAddress", "down")
	f.client.keepAliveCheck(1_000_000, covAnchorHeight)
	c.Assert(f.rpc.count("getSignaturesForAddress"), Equals, 1)
}

func (s *KeepAliveNonceCovSuite) TestKeepAliveLoopStops(c *C) {
	f := newCovFixture(c)
	defer f.server.Close()
	f.client.wg = &sync.WaitGroup{}
	f.client.stopchan = make(chan struct{})
	f.client.wg.Add(1)
	go f.client.keepAliveLoop()
	close(f.client.stopchan)
	f.client.wg.Wait() // returns only once the loop observed the stop signal
	c.Assert(f.client.getKeepAliveThorMultiple(), Equals, int64(0))
}

// -------------------------------------------------------------------------
// durable-nonce provisioning
// -------------------------------------------------------------------------

func covNonceAccountJSON(authority string) string {
	return fmt.Sprintf(`{"context":{"slot":1000},"value":{"owner":"11111111111111111111111111111111","data":{"program":"nonce","parsed":{"type":"initialized","info":{"authority":%q,"blockhash":"abc"}}}}}`, authority)
}

// setAccountInfo routes getAccountInfo by encoding: jsonParsed is the nonce read,
// base64 is the pre-sign account-meta read.
func (f *covFixture) setAccountInfo(nonceResp, metaResp string) {
	f.rpc.set("getAccountInfo", func(p []json.RawMessage) string {
		var cfg map[string]interface{}
		if len(p) > 1 {
			_ = json.Unmarshal(p[1], &cfg)
		}
		if cfg["encoding"] == "jsonParsed" {
			return nonceResp
		}
		return metaResp
	})
}

func covResult(result string) string {
	return fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"result":%s}`, result)
}

const covRPCErr = `{"jsonrpc":"2.0","id":1,"error":{"code":-32000,"message":"down"}}`

func (s *KeepAliveNonceCovSuite) TestProvisionNonceForVaultCreates(c *C) {
	f := newCovFixture(c)
	defer f.server.Close()
	f.setAccountInfo(covResult(`{"context":{"slot":1000},"value":null}`), covResult(`{"context":{"slot":1},"value":null}`))
	f.rpc.setResult("getBalance", `{"context":{"slot":1},"value":100000000000}`)
	f.rpc.setResult("sendTransaction", `"create-sig"`)

	c.Assert(f.client.provisionNonceForVault(f.vault, covAnchorHeight), IsNil)
	c.Assert(f.rpc.count("sendTransaction"), Equals, 1)
}

func (s *KeepAliveNonceCovSuite) TestProvisionNonceForVaultSkips(c *C) {
	f := newCovFixture(c)
	defer f.server.Close()

	// already provisioned: idempotent no-op
	f.setAccountInfo(covResult(covNonceAccountJSON(f.vaultAddr)), "")
	c.Assert(f.client.provisionNonceForVault(f.vault, covAnchorHeight), IsNil)
	c.Assert(f.rpc.count("getBalance"), Equals, 0)

	// transport error on the existence check
	f.setAccountInfo(covRPCErr, "")
	err := f.client.provisionNonceForVault(f.vault, covAnchorHeight)
	c.Assert(err, ErrorMatches, "fail to check nonce account.*")

	missing := covResult(`{"context":{"slot":1000},"value":null}`)

	// balance error
	f.setAccountInfo(missing, "")
	f.rpc.setError("getBalance", "down")
	err = f.client.provisionNonceForVault(f.vault, covAnchorHeight)
	c.Assert(err, ErrorMatches, "fail to get vault balance.*")

	// too poor to fund rent + fee
	f.rpc.setResult("getBalance", `{"context":{"slot":1},"value":10}`)
	c.Assert(f.client.provisionNonceForVault(f.vault, covAnchorHeight), IsNil)
	c.Assert(f.rpc.count("sendTransaction"), Equals, 0)

	// account-meta read fails
	f.rpc.setResult("getBalance", `{"context":{"slot":1},"value":100000000000}`)
	f.setAccountInfo(missing, covRPCErr)
	err = f.client.provisionNonceForVault(f.vault, covAnchorHeight)
	c.Assert(err, ErrorMatches, "fail to read nonce address.*")

	// address squatted by a non-System account: cannot provision, skipped without keysign
	f.setAccountInfo(missing, covResult(`{"context":{"slot":1},"value":{"owner":"Other1111111111111111111111111111111111111","executable":false,"lamports":5,"space":10}}`))
	c.Assert(f.client.provisionNonceForVault(f.vault, covAnchorHeight), IsNil)
	c.Assert(f.rpc.count("sendTransaction"), Equals, 0)

	// sign/broadcast failure propagates
	f.setAccountInfo(missing, covResult(`{"context":{"slot":1},"value":null}`))
	f.rpc.setError("sendTransaction", "rejected")
	f.rpc.setError("getTransaction", "missing")
	err = f.client.provisionNonceForVault(f.vault, covAnchorHeight)
	c.Assert(err, ErrorMatches, "fail to broadcast nonce tx.*")

	// consensus blockhash derivation fails
	f.rpc.setError("getSlot", "down")
	err = f.client.provisionNonceForVault(f.vault, covAnchorHeight+7)
	c.Assert(err, ErrorMatches, "fail to derive consensus blockhash.*")

	// bad vault key
	bad := f.vault
	bad.PubKeyEddsa = common.PubKey("bogus")
	err = f.client.provisionNonceForVault(bad, covAnchorHeight)
	c.Assert(err, ErrorMatches, "fail to get vault address.*")
}

func (s *KeepAliveNonceCovSuite) TestSignAndBroadcastNonceTx(c *C) {
	f := newCovFixture(c)
	defer f.server.Close()
	vaultKey, err := types.PublicKeyFromString(f.vaultAddr)
	c.Assert(err, IsNil)
	nonceAddr, err := f.client.nonceAddressForVault(vaultKey)
	c.Assert(err, IsNil)
	tx := types.NewCreateNonceAccountTransaction(vaultKey, nonceAddr, solNonceSeed, nonceAccountRentLamports, mockBlockhashForSlot(950))

	f.rpc.setResult("sendTransaction", `"nonce-sig"`)
	txid, err := f.client.signAndBroadcastNonceTx(tx, f.vault, covAnchorHeight, "nonce-provision")
	c.Assert(err, IsNil)
	c.Assert(txid, Equals, "nonce-sig")

	// remote keysign not selected: empty txid, no error, no broadcast
	f.client.localPubKey = "not-local"
	f.client.tssKeyManager = &mockSOLThorchainKeyManager{}
	txid, err = f.client.signAndBroadcastNonceTx(tx, f.vault, covAnchorHeight, "nonce-provision")
	c.Assert(err, IsNil)
	c.Assert(txid, Equals, "")
	c.Assert(f.rpc.count("sendTransaction"), Equals, 1)

	// remote keysign fails
	f.client.tssKeyManager = &mockSOLThorchainKeyManager{err: errors.New("tss down")}
	_, err = f.client.signAndBroadcastNonceTx(tx, f.vault, covAnchorHeight, "nonce-provision")
	c.Assert(err, ErrorMatches, "fail to sign nonce tx.*tss down.*")
}

func (s *KeepAliveNonceCovSuite) TestNonceProvisionCheckProvisionsActiveVault(c *C) {
	f := newCovFixture(c)
	defer f.server.Close()
	f.bridge.mimirs[constants.MimirKeySOLDurableNonce] = 1
	pb := &covPendingBridge{covBridge: f.bridge}
	f.client.bridge = pb
	f.setAccountInfo(covResult(`{"context":{"slot":1000},"value":null}`), covResult(`{"context":{"slot":1},"value":null}`))
	f.rpc.setResult("getBalance", `{"context":{"slot":1},"value":100000000000}`)
	f.rpc.setResult("sendTransaction", `"create-sig"`)

	// a pending SOL outbound for the vault defers the create
	pb.pending = []thorclient.PendingOutbound{{Chain: common.SOLChain.String(), VaultPubKey: f.vault.PubKey.String()}}
	f.client.nonceProvisionCheck(covAnchorHeight)
	c.Assert(f.rpc.count("sendTransaction"), Equals, 0)

	// once the queue is clear the create is broadcast
	pb.pending = nil
	f.client.nonceProvisionCheck(covAnchorHeight)
	c.Assert(f.rpc.count("sendTransaction"), Equals, 1)

	// a provisioning failure is logged, not fatal
	f.rpc.setError("getBalance", "down")
	f.client.nonceProvisionCheck(covAnchorHeight)
	c.Assert(f.rpc.count("sendTransaction"), Equals, 1)
}

func (s *KeepAliveNonceCovSuite) TestNonceProvisionCheckGates(c *C) {
	cases := map[string]func(f *covFixture){
		"node status err":   func(f *covFixture) { f.bridge.nodeErr = errors.New("x") },
		"node not active":   func(f *covFixture) { f.bridge.nodeStatus = types2.NodeStatus_Standby },
		"mimir error":       func(f *covFixture) { f.bridge.mimirErr[constants.MimirKeySOLDurableNonce] = errors.New("x") },
		"signing halted":    func(f *covFixture) { f.bridge.thorHeight = 100; f.bridge.mimirs["HALTSIGNING"] = 1 },
		"scanner unhealthy": func(f *covFixture) { f.client.solScanner.healthy.Store(false) },
		"no node pubkey": func(f *covFixture) {
			pkm, err := pubkeymanager.NewPubKeyManager(f.bridge, nil)
			if err == nil {
				f.client.pubkeyMgr = pkm
			}
		},
		"asgards error":     func(f *covFixture) { f.bridge.asgardErr = errors.New("x") },
		"no pending reader": func(f *covFixture) { f.client.bridge = f.bridge },
		"pending read error": func(f *covFixture) {
			if pb, ok := f.client.bridge.(*covPendingBridge); ok {
				pb.pendingErr = errors.New("x")
			}
		},
		"not a member":   func(f *covFixture) { f.bridge.vaults[0].Membership = nil },
		"no eddsa key":   func(f *covFixture) { f.bridge.vaults[0].PubKeyEddsa = "" },
		"retiring vault": func(f *covFixture) { f.bridge.vaults[0].Status = types2.VaultStatus_RetiringVault },
		"feature off":    func(f *covFixture) { f.bridge.mimirs[constants.MimirKeySOLDurableNonce] = 0 },
	}
	for name, mutate := range cases {
		f := newCovFixture(c)
		f.bridge.mimirs[constants.MimirKeySOLDurableNonce] = 1
		f.client.bridge = &covPendingBridge{covBridge: f.bridge}
		mutate(f)
		f.client.nonceProvisionCheck(covAnchorHeight)
		c.Check(f.rpc.count("getAccountInfo"), Equals, 0, Commentf(name))
		c.Check(f.rpc.count("getSlot"), Equals, 0, Commentf(name))
		f.server.Close()
	}
}

func (s *KeepAliveNonceCovSuite) TestNonceProvisionThorMultiple(c *C) {
	cl := &SOLClient{}
	c.Assert(cl.nextNonceProvisionThorMultiple(5), Equals, int64(0))
	c.Assert(cl.nextNonceProvisionThorMultiple(int64(nonceProvisionThorBlockCadence)*7+3), Equals, int64(7))
	cl.setNonceProvisionThorMultiple(7)
	c.Assert(cl.getNonceProvisionThorMultiple(), Equals, int64(7))
	// exact multiple has not yet been crossed
	c.Assert(cl.nextNonceProvisionThorMultiple(int64(nonceProvisionThorBlockCadence)*8), Equals, int64(7))
	c.Assert(cl.nextNonceProvisionThorMultiple(int64(nonceProvisionThorBlockCadence)*30), Equals, int64(8))
}
