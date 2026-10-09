package core

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	sdkmath "cosmossdk.io/math"

	"gitlab.com/thorchain/thornode/v3/common"
	simmonero "gitlab.com/thorchain/thornode/v3/test/simulation/pkg/monero"
	simtypes "gitlab.com/thorchain/thornode/v3/test/simulation/pkg/types"
)

// covBFrost is a fake XMR FROST balance endpoint.
type covBFrost struct {
	server *httptest.Server

	mu     sync.Mutex
	status int
	body   string
	paths  []string
}

func newCovBFrost(t *testing.T) *covBFrost {
	t.Helper()
	f := &covBFrost{status: http.StatusOK, body: `{"balance":0,"unlocked_balance":0}`}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.paths = append(f.paths, r.URL.Path)
		w.WriteHeader(f.status)
		_, _ = w.Write([]byte(f.body))
	}))
	t.Cleanup(f.server.Close)
	return f
}

// set configures the balances returned (in 1e8 simulation units).
func (f *covBFrost) set(status int, total, spendable uint64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.status = status
	// 1e8 simulation units == 1e12 atomic units
	f.body = fmt.Sprintf(`{"balance":%d,"unlocked_balance":%d,"spendable_balance":%d}`, total*10_000, spendable*10_000, spendable*10_000)
}

// newCovBMoneroClient builds a real *simmonero.Client (keys from mnemonic idx)
// whose FROST balance endpoint is the fake.
func newCovBMoneroClient(t *testing.T, idx int, frost *covBFrost) *simmonero.Client {
	t.Helper()
	t.Setenv("XMR_FROST_ENDPOINT", frost.server.URL)
	user := simtypes.NewUser(covMnemonics[idx], map[common.Chain]simtypes.LiteChainClientConstructor{
		common.XMRChain: simmonero.NewConstructor(frost.server.URL),
	})
	client, ok := user.ChainClients[common.XMRChain].(*simmonero.Client)
	if !ok {
		t.Fatal("expected a monero client")
	}
	return client
}

// covBXMRInbound serves an XMR inbound address, optionally with the vault
// pubkey (an empty pubKey omits it).
func covBXMRInbound(api *covThornode, vaultPubKey common.PubKey) {
	pubKey := ""
	if !vaultPubKey.IsEmpty() {
		pubKey = `,"pub_key":"` + vaultPubKey.String() + `"`
	}
	api.JSON("/thorchain/inbound_addresses", `[{"chain":"XMR","address":"4xmrvault"`+pubKey+`}]`)
}

func TestGetXMRVaultBalanceClientCovB(t *testing.T) {
	frost := newCovBFrost(t)
	xmr := newCovBMoneroClient(t, 0, frost)
	plain, _ := newCovUser(t, 1, map[common.Chain]simtypes.LiteChainClient{common.XMRChain: newCovChainClient()})
	none, _ := newCovUser(t, 2, nil)
	withXMR, _ := newCovUser(t, 3, map[common.Chain]simtypes.LiteChainClient{common.XMRChain: xmr})

	if got, err := getXMRVaultBalanceClient(nil, withXMR); err != nil || got != xmr {
		t.Fatalf("preferred user client must win: %v %v", got, err)
	}
	cfg := &simtypes.OpConfig{Users: []*simtypes.User{none, plain, withXMR}}
	if got, err := getXMRVaultBalanceClient(cfg, plain); err != nil || got != xmr {
		t.Fatalf("must fall back to another user: %v", err)
	}
	cfg = &simtypes.OpConfig{Users: []*simtypes.User{plain}, NodeUsers: []*simtypes.User{plain, withXMR}}
	if got, err := getXMRVaultBalanceClient(cfg, plain); err != nil || got != xmr {
		t.Fatalf("must fall back to node users: %v", err)
	}
	if _, err := getXMRVaultBalanceClient(&simtypes.OpConfig{Users: []*simtypes.User{none, plain}}, nil); err == nil ||
		!strings.Contains(err.Error(), "no simulation Monero client") {
		t.Fatalf("expected missing client error, got %v", err)
	}
	if _, err := getXMRVaultBalanceClient(nil, nil); err == nil {
		t.Fatal("expected missing client error without config")
	}
}

func TestGetActiveXMRVaultBalanceCovB(t *testing.T) {
	api := newCovThornode(t)
	frost := newCovBFrost(t)
	xmr := newCovBMoneroClient(t, 0, frost)
	user, _ := newCovUser(t, 0, map[common.Chain]simtypes.LiteChainClient{common.XMRChain: xmr})

	if _, err := getActiveXMRVaultBalance(&simtypes.OpConfig{}, nil); err == nil {
		t.Fatal("missing client must fail")
	}
	api.Status("/thorchain/inbound_addresses", 400, "bad")
	if _, err := getActiveXMRVaultBalance(nil, user); err == nil || !strings.Contains(err.Error(), "inbound address") {
		t.Fatalf("inbound failure must be wrapped, got %v", err)
	}
	covBXMRInbound(api, common.EmptyPubKey)
	if _, err := getActiveXMRVaultBalance(nil, user); err == nil || !strings.Contains(err.Error(), "inbound pubkey") {
		t.Fatalf("missing pubkey must fail, got %v", err)
	}
	covBXMRInbound(api, user.PubKey(common.XMRChain))
	frost.set(http.StatusBadGateway, 0, 0)
	if _, err := getActiveXMRVaultBalance(nil, user); err == nil || !strings.Contains(err.Error(), "vault balance") {
		t.Fatalf("balance failure must be wrapped, got %v", err)
	}
	frost.set(http.StatusOK, 500, 300)
	snap, err := getActiveXMRVaultBalance(nil, user)
	if err != nil {
		t.Fatal(err)
	}
	if snap.inbound != "4xmrvault" || !snap.pubKey.Equals(user.PubKey(common.XMRChain)) || !snap.total.Equal(sdkmath.NewUint(500)) || !snap.spendable.Equal(sdkmath.NewUint(300)) {
		t.Fatalf("unexpected snapshot %+v", snap)
	}
	if last := frost.paths[len(frost.paths)-1]; last != "/v1/monero/balance/"+user.PubKey(common.XMRChain).String() {
		t.Fatalf("unexpected FROST path %s", last)
	}
}

func TestEnsureXMRVaultSpendableCovB(t *testing.T) {
	api := newCovThornode(t)
	frost := newCovBFrost(t)
	xmr := newCovBMoneroClient(t, 0, frost)
	user, _ := newCovUser(t, 0, map[common.Chain]simtypes.LiteChainClient{common.XMRChain: xmr})
	a := newSwapBalanceTestActor(common.BTCAsset, common.XMRAsset, 1, 2)
	a.user = user

	if res := (newSwapBalanceTestActor(common.BTCAsset, common.ETHAsset, 1, 2)).ensureXMRVaultSpendable(nil, sdkmath.ZeroUint()); !res.Continue {
		t.Fatal("non-XMR destination must not wait")
	}
	api.Status("/thorchain/inbound_addresses", 400, "bad")
	if res := a.ensureXMRVaultSpendable(nil, sdkmath.NewUint(10)); res.Continue || res.Finish {
		t.Fatalf("balance lookup failure must wait: %+v", res)
	}
	covBXMRInbound(api, user.PubKey(common.XMRChain))
	frost.set(http.StatusOK, 100, 9)
	if res := a.ensureXMRVaultSpendable(nil, sdkmath.NewUint(10)); res.Continue || res.Finish {
		t.Fatalf("insufficient spendable must wait: %+v", res)
	}
	frost.set(http.StatusOK, 100, 10)
	if res := a.ensureXMRVaultSpendable(nil, sdkmath.NewUint(10)); !res.Continue {
		t.Fatalf("sufficient spendable must continue: %+v", res)
	}
	// zero requirement still needs at least one unit
	frost.set(http.StatusOK, 100, 0)
	if res := a.ensureXMRVaultSpendable(nil, sdkmath.ZeroUint()); res.Continue {
		t.Fatal("empty vault must wait even with zero requirement")
	}
	frost.set(http.StatusOK, 100, 1)
	if res := a.ensureXMRVaultSpendable(nil, sdkmath.ZeroUint()); !res.Continue {
		t.Fatal("one spendable unit satisfies a zero requirement")
	}
}
