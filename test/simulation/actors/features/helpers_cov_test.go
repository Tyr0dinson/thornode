package features

import (
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"

	sdk "github.com/cosmos/cosmos-sdk/types"

	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"

	. "gitlab.com/thorchain/thornode/v3/test/simulation/pkg/types"
)

// routesCov is a mutable fake thornode: request URI (or path) -> JSON body.
// Missing routes answer 404 so thornode.Get fails without retrying.
type routesCov struct {
	mu     sync.Mutex
	bodies map[string]string
}

func newRoutesCov(t *testing.T, bodies map[string]string) *routesCov {
	t.Helper()
	r := &routesCov{bodies: map[string]string{}}
	for k, v := range bodies {
		r.bodies[k] = v
	}
	setThornodeCov(t, func(w http.ResponseWriter, req *http.Request) {
		r.mu.Lock()
		defer r.mu.Unlock()
		body, ok := r.bodies[req.URL.RequestURI()]
		if !ok && req.URL.RawQuery == "" {
			body, ok = r.bodies[req.URL.Path]
		}
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte("not found"))
			return
		}
		_, _ = w.Write([]byte(body))
	})
	return r
}

func (r *routesCov) set(key, body string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.bodies[key] = body
}

func (r *routesCov) del(key string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.bodies, key)
}

// fakeBridgeCov records broadcast messages instead of talking to THORChain.
type fakeBridgeCov struct {
	thorclient.ThorchainBridge

	mu     sync.Mutex
	msgs   []sdk.Msg
	err    error
	txid   common.TxID
	height int64
	policy thorclient.KeysignPolicy
	polErr error
}

func (b *fakeBridgeCov) Broadcast(msgs ...sdk.Msg) (common.TxID, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.err != nil {
		return "", b.err
	}
	b.msgs = append(b.msgs, msgs...)
	if b.txid != "" {
		return b.txid, nil
	}
	return common.TxID("ABCDEF"), nil
}

func (b *fakeBridgeCov) BroadcastWithBlocking(msgs ...sdk.Msg) (common.TxID, error) {
	return b.Broadcast(msgs...)
}

func (b *fakeBridgeCov) GetBlockHeight() (int64, error) { return b.height, nil }

func (b *fakeBridgeCov) GetKeysignPolicy(common.PubKey, int64) (thorclient.KeysignPolicy, error) {
	return b.policy, b.polErr
}

func (b *fakeBridgeCov) sent() []sdk.Msg {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]sdk.Msg(nil), b.msgs...)
}

// fakeChainCov is an in-memory LiteChainClient.
type fakeChainCov struct {
	coins    common.Coins
	accErr   error
	signErr  error
	bcastErr error
	txid     string
	signed   []SimTx
}

func (c *fakeChainCov) GetAccount(*common.PubKey) (*common.Account, error) {
	if c.accErr != nil {
		return nil, c.accErr
	}
	return &common.Account{Coins: c.coins}, nil
}

func (c *fakeChainCov) SignTx(tx SimTx) ([]byte, error) {
	if c.signErr != nil {
		return nil, c.signErr
	}
	c.signed = append(c.signed, tx)
	return []byte("signed"), nil
}

func (c *fakeChainCov) BroadcastTx([]byte) (string, error) {
	if c.bcastErr != nil {
		return "", c.bcastErr
	}
	if c.txid == "" {
		return "TXID1", nil
	}
	return c.txid, nil
}

var errCov = errors.New("injected failure")

var userMnemonicsCov = map[string]string{
	"dog": strings.Repeat("dog ", 23) + "fossil",
	"cat": strings.Repeat("cat ", 23) + "crawl",
	"pig": strings.Repeat("pig ", 23) + "quick",
}

// newUserCov creates an offline User (CHAIN_ID is set by TestMain) with a fake
// bridge and an optional fake chain client funded with the given coins.
func newUserCov(t *testing.T, name string, chain common.Chain, coins ...common.Coin) (*User, *fakeBridgeCov, *fakeChainCov) {
	t.Helper()
	m, ok := userMnemonicsCov[name]
	if !ok {
		t.Fatalf("unknown user %s", name)
	}
	u := NewUser(m, nil)
	bridge := &fakeBridgeCov{}
	u.Thorchain = bridge
	client := &fakeChainCov{coins: common.NewCoins(coins...)}
	if !chain.IsEmpty() {
		u.ChainClients[chain] = client
	}
	return u, bridge, client
}

func thorAddrCov(t *testing.T, u *User) cosmos.AccAddress {
	t.Helper()
	acc, err := u.PubKey(common.THORChain).GetThorAddress()
	if err != nil {
		t.Fatal(err)
	}
	return acc
}

func lastMemoCov(t *testing.T, b *fakeBridgeCov) string {
	t.Helper()
	msgs := b.sent()
	if len(msgs) == 0 {
		t.Fatal("no messages broadcast")
	}
	type memoer interface{ GetMemo() string }
	m, ok := msgs[len(msgs)-1].(memoer)
	if !ok {
		t.Fatalf("last message %T has no memo", msgs[len(msgs)-1])
	}
	return m.GetMemo()
}
