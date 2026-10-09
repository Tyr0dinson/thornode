package core

// Shared hermetic fakes for the core actor coverage tests. The actors normally
// drive a live mocknet; these fakes replace the THORNode REST API (httptest
// server wired into pkg/thornode), the THORChain bridge and the L1 lite chain
// clients so the ops can run without any network.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
	_ "unsafe" // required for go:linkname

	sdk "github.com/cosmos/cosmos-sdk/types"

	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	"gitlab.com/thorchain/thornode/v3/common"
	stypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"

	simtypes "gitlab.com/thorchain/thornode/v3/test/simulation/pkg/types"
)

// pkg/thornode resolves its base URL from config at init time and exposes no
// setter, so tests rebind the unexported package variables directly.
//
//go:linkname covThornodeURL gitlab.com/thorchain/thornode/v3/test/simulation/pkg/thornode.thornodeURL
var covThornodeURL string

//go:linkname covThornodeHTTPSleep gitlab.com/thorchain/thornode/v3/test/simulation/pkg/thornode.thornodeHTTPSleep
var covThornodeHTTPSleep func(time.Duration)

// covMnemonics are the mocknet user mnemonics (valid BIP39 phrases).
var covMnemonics = []string{
	strings.Repeat("bird ", 23) + "asthma",
	strings.Repeat("deer ", 23) + "diesel",
	strings.Repeat("duck ", 23) + "face",
	strings.Repeat("fish ", 23) + "fade",
	strings.Repeat("frog ", 23) + "flat",
	strings.Repeat("goat ", 23) + "install",
	strings.Repeat("hawk ", 23) + "juice",
	strings.Repeat("lion ", 23) + "misery",
	strings.Repeat("mouse ", 23) + "option",
	strings.Repeat("mule ", 23) + "major",
	strings.Repeat("rabbit ", 23) + "rent",
	strings.Repeat("wolf ", 23) + "victory",
	strings.Repeat("dog ", 23) + "fossil",
	strings.Repeat("cat ", 23) + "crawl",
	strings.Repeat("fox ", 23) + "filter",
	strings.Repeat("pig ", 23) + "quick",
	strings.Repeat("frog ", 23) + "blossom",
	strings.Repeat("master ", 23) + "notice",
}

////////////////////////////////////////////////////////////////////////////////////////
// Fake THORNode REST API
////////////////////////////////////////////////////////////////////////////////////////

// covThornode is an httptest THORNode API. Routes are keyed by exact URL path;
// each route returns its queued responses in order and then keeps repeating the
// last one. Unknown paths return 404 (a non-retryable error in pkg/thornode).
type covThornode struct {
	t      testing.TB
	server *httptest.Server

	mu     sync.Mutex
	routes map[string][]covResponse
	hits   map[string]int
	reqs   []*http.Request
}

type covResponse struct {
	status  int
	body    string
	handler http.HandlerFunc
}

// newCovThornode starts a fake THORNode API and points pkg/thornode at it for
// the duration of the test. Tests using it must not run in parallel.
func newCovThornode(t testing.TB) *covThornode {
	t.Helper()
	f := &covThornode{
		t:      t,
		routes: map[string][]covResponse{},
		hits:   map[string]int{},
	}
	f.server = httptest.NewServer(http.HandlerFunc(f.serve))
	oldURL, oldSleep := covThornodeURL, covThornodeHTTPSleep
	covThornodeURL = f.server.URL
	covThornodeHTTPSleep = func(time.Duration) {}
	t.Cleanup(func() {
		covThornodeURL, covThornodeHTTPSleep = oldURL, oldSleep
		f.server.Close()
	})
	return f
}

func (f *covThornode) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.hits[r.URL.Path]++
	f.reqs = append(f.reqs, r)
	queue := f.routes[r.URL.Path]
	var resp covResponse
	found := len(queue) > 0
	if found {
		resp = queue[0]
		if len(queue) > 1 {
			f.routes[r.URL.Path] = queue[1:]
		}
	}
	f.mu.Unlock()

	if !found {
		http.NotFound(w, r)
		return
	}
	if resp.handler != nil {
		resp.handler(w, r)
		return
	}
	w.WriteHeader(resp.status)
	_, _ = w.Write([]byte(resp.body))
}

func covMarshal(t testing.TB, v interface{}) string {
	t.Helper()
	if s, ok := v.(string); ok {
		return s
	}
	buf, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal fake response: %v", err)
	}
	return string(buf)
}

// JSON replaces the route with a single 200 response (a string is sent raw,
// anything else is JSON encoded).
func (f *covThornode) JSON(path string, v interface{}) *covThornode {
	return f.Seq(path, v)
}

// Seq replaces the route with a sequence of 200 responses.
func (f *covThornode) Seq(path string, vs ...interface{}) *covThornode {
	f.t.Helper()
	queue := make([]covResponse, 0, len(vs))
	for _, v := range vs {
		queue = append(queue, covResponse{status: http.StatusOK, body: covMarshal(f.t, v)})
	}
	f.mu.Lock()
	f.routes[path] = queue
	f.mu.Unlock()
	return f
}

// Status replaces the route with a single non-200 response. Prefer 4xx codes:
// 429/502/503/504 trigger the pkg/thornode retry.
func (f *covThornode) Status(path string, status int, body string) *covThornode {
	f.mu.Lock()
	f.routes[path] = []covResponse{{status: status, body: body}}
	f.mu.Unlock()
	return f
}

// Handle replaces the route with a custom handler.
func (f *covThornode) Handle(path string, h http.HandlerFunc) *covThornode {
	f.mu.Lock()
	f.routes[path] = []covResponse{{handler: h}}
	f.mu.Unlock()
	return f
}

// Remove deletes a route so it 404s.
func (f *covThornode) Remove(path string) *covThornode {
	f.mu.Lock()
	delete(f.routes, path)
	f.mu.Unlock()
	return f
}

// Hits returns how many times the path was requested.
func (f *covThornode) Hits(path string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.hits[path]
}

// URL returns the fake server base URL.
func (f *covThornode) URL() string { return f.server.URL }

////////////////////////////////////////////////////////////////////////////////////////
// Fake THORChain bridge
////////////////////////////////////////////////////////////////////////////////////////

// covBridge implements the subset of thorclient.ThorchainBridge used by the
// actors; any other method panics via the nil embedded interface.
type covBridge struct {
	thorclient.ThorchainBridge

	mu sync.Mutex

	BroadcastFn             func(msgs ...sdk.Msg) (common.TxID, error)
	BroadcastWithBlockingFn func(msgs ...sdk.Msg) (common.TxID, error)
	HeightFn                func() (int64, error)
	KeysignPolicyFn         func(vault common.PubKey, height int64) (thorclient.KeysignPolicy, error)
	PostKeysignFailureFn    func(blame stypes.Blame, height int64, memo string, coins common.Coins, pubkey common.PubKey) (common.TxID, error)
	NetworkFeeFn            func(chain common.Chain) (uint64, uint64, error)

	Msgs         []sdk.Msg
	BlockingMsgs []sdk.Msg
	Failures     []stypes.Blame
	txCount      int
}

func (b *covBridge) nextTxID() common.TxID {
	b.txCount++
	return common.TxID(fmt.Sprintf("%064X", b.txCount))
}

func (b *covBridge) Broadcast(msgs ...sdk.Msg) (common.TxID, error) {
	b.mu.Lock()
	b.Msgs = append(b.Msgs, msgs...)
	fn := b.BroadcastFn
	id := b.nextTxID()
	b.mu.Unlock()
	if fn != nil {
		return fn(msgs...)
	}
	return id, nil
}

func (b *covBridge) BroadcastWithBlocking(msgs ...sdk.Msg) (common.TxID, error) {
	b.mu.Lock()
	b.BlockingMsgs = append(b.BlockingMsgs, msgs...)
	fn := b.BroadcastWithBlockingFn
	id := b.nextTxID()
	b.mu.Unlock()
	if fn != nil {
		return fn(msgs...)
	}
	return id, nil
}

func (b *covBridge) GetBlockHeight() (int64, error) {
	if b.HeightFn != nil {
		return b.HeightFn()
	}
	return 100, nil
}

func (b *covBridge) GetKeysignPolicy(vault common.PubKey, height int64) (thorclient.KeysignPolicy, error) {
	if b.KeysignPolicyFn != nil {
		return b.KeysignPolicyFn(vault, height)
	}
	return thorclient.KeysignPolicy{}, fmt.Errorf("no keysign policy configured")
}

func (b *covBridge) PostKeysignFailure(blame stypes.Blame, height int64, memo string, coins common.Coins, pubkey common.PubKey) (common.TxID, error) {
	b.mu.Lock()
	b.Failures = append(b.Failures, blame)
	fn := b.PostKeysignFailureFn
	id := b.nextTxID()
	b.mu.Unlock()
	if fn != nil {
		return fn(blame, height, memo, coins, pubkey)
	}
	return id, nil
}

func (b *covBridge) GetNetworkFee(chain common.Chain) (uint64, uint64, error) {
	if b.NetworkFeeFn != nil {
		return b.NetworkFeeFn(chain)
	}
	return 10000, 1200000, nil
}

// MsgCount returns the number of messages broadcast (blocking or not).
func (b *covBridge) MsgCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.Msgs) + len(b.BlockingMsgs)
}

////////////////////////////////////////////////////////////////////////////////////////
// Fake lite chain client
////////////////////////////////////////////////////////////////////////////////////////

// covChainClient is a configurable LiteChainClient that records signed and
// broadcast transactions.
type covChainClient struct {
	mu sync.Mutex

	Account      common.Account
	AccountErr   error
	SignErr      error
	BroadcastErr error
	TxID         string

	AccountFn   func(pk *common.PubKey) (*common.Account, error)
	SignFn      func(tx simtypes.SimTx) ([]byte, error)
	BroadcastFn func(payload []byte) (string, error)

	Signed    []simtypes.SimTx
	Broadcast [][]byte
}

func newCovChainClient(coins ...common.Coin) *covChainClient {
	return &covChainClient{Account: common.NewAccount(0, 0, coins, true), TxID: "COVTXID"}
}

func (c *covChainClient) GetAccount(pk *common.PubKey) (*common.Account, error) {
	if c.AccountFn != nil {
		return c.AccountFn(pk)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.AccountErr != nil {
		return nil, c.AccountErr
	}
	account := c.Account
	return &account, nil
}

func (c *covChainClient) SignTx(tx simtypes.SimTx) ([]byte, error) {
	c.mu.Lock()
	c.Signed = append(c.Signed, tx)
	fn, err := c.SignFn, c.SignErr
	c.mu.Unlock()
	if fn != nil {
		return fn(tx)
	}
	if err != nil {
		return nil, err
	}
	return []byte("signed:" + tx.Memo), nil
}

func (c *covChainClient) BroadcastTx(payload []byte) (string, error) {
	c.mu.Lock()
	c.Broadcast = append(c.Broadcast, payload)
	fn, err, id := c.BroadcastFn, c.BroadcastErr, c.TxID
	c.mu.Unlock()
	if fn != nil {
		return fn(payload)
	}
	if err != nil {
		return "", err
	}
	return id, nil
}

////////////////////////////////////////////////////////////////////////////////////////
// Users
////////////////////////////////////////////////////////////////////////////////////////

var (
	covUserCacheMu sync.Mutex
	covUserCache   = map[int]*simtypes.User{}
)

// newCovUser builds a real simulation user (deterministic keys from the mocknet
// mnemonic at idx) with a fresh fake bridge and the given chain clients. Key
// derivation is cached per mnemonic; each call returns a new, unlocked User.
func newCovUser(t *testing.T, idx int, clients map[common.Chain]simtypes.LiteChainClient) (*simtypes.User, *covBridge) {
	t.Helper()
	covUserCacheMu.Lock()
	base, ok := covUserCache[idx]
	if !ok {
		base = simtypes.NewUser(covMnemonics[idx], nil)
		covUserCache[idx] = base
	}
	covUserCacheMu.Unlock()

	user := *base // copies the private keys/mnemonic; lock is replaced below
	setTestSwapUserField(t, &user, "lock", make(chan struct{}, 1))
	bridge := &covBridge{}
	user.Thorchain = bridge
	user.ChainClients = map[common.Chain]simtypes.LiteChainClient{}
	for chain, client := range clients {
		user.ChainClients[chain] = client
	}
	return &user, bridge
}
