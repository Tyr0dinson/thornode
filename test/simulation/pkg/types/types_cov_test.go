package types

import (
	"errors"
	"math/rand/v2"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rs/zerolog"

	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/config"
)

const dogMnemonicCov = "dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog fossil"

// withChainIDCov makes the bifrost config carry a chain id for the duration of
// the test (NewUser requires one), restoring the original config afterwards.
func withChainIDCov(t *testing.T) {
	t.Helper()
	t.Cleanup(config.Init) // runs after t.Setenv restores the environment
	t.Setenv("CHAIN_ID", "thorchain")
	config.Init()
}

func TestNewUserCov(t *testing.T) {
	withChainIDCov(t)

	fake := &testLiteChainClient{}
	var gotKeys *thorclient.Keys
	user := NewUser(dogMnemonicCov, map[common.Chain]LiteChainClientConstructor{
		common.BTCChain: func(chain common.Chain, keys *thorclient.Keys) (LiteChainClient, error) {
			if !chain.Equals(common.BTCChain) {
				t.Errorf("unexpected chain %s", chain)
			}
			gotKeys = keys
			return fake, nil
		},
	})

	if user.Name() != "dog" {
		t.Fatalf("expected name dog, got %s", user.Name())
	}
	if user.ChainClients[common.BTCChain] != fake {
		t.Fatal("constructed chain client not stored")
	}
	if gotKeys == nil || gotKeys.GetSignerInfo().Name != "dog" {
		t.Fatal("constructor must receive keys for the mnemonic signer")
	}
	if user.Thorchain == nil {
		t.Fatal("thorchain bridge must be created")
	}

	// secp256k1 vs ed25519 keys are distinct and used per chain signing algo
	secp := user.PubKey(common.BTCChain)
	ed := user.PubKey(common.SOLChain)
	if secp.IsEmpty() || ed.IsEmpty() || secp.Equals(ed) {
		t.Fatalf("expected distinct non-empty pubkeys, got %s and %s", secp, ed)
	}
	btcAddr := user.Address(common.BTCChain)
	wantBTC, err := secp.GetAddress(common.BTCChain)
	if err != nil || btcAddr != wantBTC {
		t.Fatalf("btc address %s != %s (%v)", btcAddr, wantBTC, err)
	}
	solAddr := user.Address(common.SOLChain)
	wantSOL, err := ed.GetAddress(common.SOLChain)
	if err != nil || solAddr != wantSOL {
		t.Fatalf("sol address %s != %s (%v)", solAddr, wantSOL, err)
	}

	// lock semantics
	if user.IsLocked() {
		t.Fatal("new user must be unlocked")
	}
	if !user.Acquire() {
		t.Fatal("first acquire must succeed")
	}
	if !user.IsLocked() {
		t.Fatal("user must be locked after acquire")
	}
	if user.Acquire() {
		t.Fatal("second acquire must fail")
	}
	user.Release()
	if user.IsLocked() || !user.Acquire() {
		t.Fatal("user must be acquirable after release")
	}
}

func TestNewChainClientWithRetryDefaultSleepCov(t *testing.T) {
	// permanent error returns immediately, so the real sleep is never invoked
	want := errors.New("bad config")
	got, err := newChainClientWithRetry(common.BTCChain, func(common.Chain, *thorclient.Keys) (LiteChainClient, error) {
		return nil, want
	}, nil)
	if got != nil || !errors.Is(err, want) {
		t.Fatalf("expected permanent error, got %v %v", got, err)
	}
}

func TestNewChainClientWithRetryExhaustsAttemptsCov(t *testing.T) {
	attempts, sleeps := 0, 0
	transient := errors.New("dial tcp: connect: connection refused")
	got, err := newChainClientWithRetryAndSleep(common.BTCChain, func(common.Chain, *thorclient.Keys) (LiteChainClient, error) {
		attempts++
		return nil, transient
	}, nil, func(time.Duration) { sleeps++ })
	if got != nil || !errors.Is(err, transient) {
		t.Fatalf("expected last transient error, got %v %v", got, err)
	}
	if attempts != chainClientStartupRetryAttempts || sleeps != chainClientStartupRetryAttempts-1 {
		t.Fatalf("attempts=%d sleeps=%d", attempts, sleeps)
	}
	if isRetryableChainClientStartupError(nil) {
		t.Fatal("nil error is not retryable")
	}
}

func TestActorStateTransitionsCov(t *testing.T) {
	a := NewActor("a", rand.New(rand.NewPCG(1, 1)))
	if a.Started() || a.Backgrounded() || a.Finished() {
		t.Fatal("new actor must have unknown state")
	}
	a.Start()
	if !a.Started() || a.Backgrounded() || a.Finished() {
		t.Fatal("expected started")
	}
	a.Background()
	if a.Started() || !a.Backgrounded() || a.Finished() {
		t.Fatal("expected backgrounded")
	}
	a.Ops = []Op{func(*OpConfig) OpResult { return OpResult{Continue: true} }}
	if err := a.Execute(&OpConfig{}); err != nil {
		t.Fatal(err)
	}
	if !a.Finished() {
		t.Fatal("expected finished after execute")
	}
}

func TestActorAppendFansInCov(t *testing.T) {
	rng := rand.New(rand.NewPCG(2, 2))
	root := NewActor("root", rng)
	c1 := NewActor("c1", rng)
	c2 := NewActor("c2", rng)
	g1 := NewActor("g1", rng)
	root.Children[c1] = true
	root.Children[c2] = true
	c1.Children[g1] = true

	tail := NewActor("tail", rng)
	root.Append(tail)

	parents := tail.Parents()
	if len(parents) != 2 || !parents[g1] || !parents[c2] {
		t.Fatalf("expected leaf descendants g1 and c2 as parents, got %v", parents)
	}
	if !g1.Children[tail] || !c2.Children[tail] || c1.Children[tail] {
		t.Fatal("tail must be a child of every leaf only")
	}
}

func TestActorExecuteFinishWithErrorCov(t *testing.T) {
	a := NewActor("a", rand.New(rand.NewPCG(3, 3)))
	a.SetLogger(zerolog.Nop())
	want := errors.New("boom")
	calls := 0
	a.Ops = []Op{
		func(*OpConfig) OpResult { calls++; return OpResult{Finish: true, Error: want} },
		func(*OpConfig) OpResult { t.Fatal("second op must not run"); return OpResult{} },
	}
	if err := a.Execute(&OpConfig{}); !errors.Is(err, want) || calls != 1 {
		t.Fatalf("expected op error after 1 call, got %v (%d calls)", err, calls)
	}
}

func TestWatcherExecuteCov(t *testing.T) {
	var calls atomic.Int32
	want := errors.New("stop")
	w := &Watcher{Name: "w", Interval: time.Millisecond, Fn: func(*OpConfig) error {
		if calls.Add(1) == 3 {
			return want
		}
		return nil
	}}
	if err := w.Execute(&OpConfig{}, zerolog.Nop()); !errors.Is(err, want) || calls.Load() != 3 {
		t.Fatalf("expected stop error after 3 calls, got %v (%d)", err, calls.Load())
	}

	p := &Watcher{Name: "p", Fn: func(*OpConfig) error { panic("kaboom") }}
	err := p.Execute(&OpConfig{}, zerolog.Nop())
	if err == nil || err.Error() != "panic: kaboom" {
		t.Fatalf("expected recovered panic error, got %v", err)
	}
}

func TestSimTxRetryClassifiersCov(t *testing.T) {
	cases := []struct {
		name string
		fn   func() bool
		want bool
	}{
		{"sign sol blockhash", func() bool {
			return shouldRetrySimTxSign(common.SOLChain, errors.New("fail to get latest blockhash"))
		}, true},
		{"sign sol nil", func() bool { return shouldRetrySimTxSign(common.SOLChain, nil) }, false},
		{"sign btc", func() bool { return shouldRetrySimTxSign(common.BTCChain, errors.New("eof")) }, false},
		{"broadcast btc", func() bool { return shouldRetrySimTxBroadcast(common.BTCChain, errors.New("eof")) }, false},
		{"broadcast sol nil", func() bool { return shouldRetrySimTxBroadcast(common.SOLChain, nil) }, false},
		{"broadcast sol io timeout", func() bool {
			return shouldRetrySimTxBroadcast(common.SOLChain, errors.New("read: i/o timeout"))
		}, true},
		{"broadcast tron nil", func() bool { return shouldRetrySimTxBroadcast(common.TRONChain, nil) }, false},
		{"broadcast zec nil", func() bool { return shouldRetrySimTxBroadcast(common.ZECChain, nil) }, false},
		{"unconfirmed nil", func() bool { return isSubmittedButUnconfirmedSimTxBroadcast(common.SOLChain, nil) }, false},
		{"unconfirmed btc", func() bool {
			return isSubmittedButUnconfirmedSimTxBroadcast(common.BTCChain, errors.New("not confirmed on solana node"))
		}, false},
		{"unconfirmed sol", func() bool {
			return isSubmittedButUnconfirmedSimTxBroadcast(common.SOLChain, errors.New("TX NOT CONFIRMED ON SOLANA NODE"))
		}, true},
	}
	for _, tc := range cases {
		if got := tc.fn(); got != tc.want {
			t.Errorf("%s: got %v want %v", tc.name, got, tc.want)
		}
	}
	if !strings.Contains(Op(func(*OpConfig) OpResult { return OpResult{} }).String(), "types.") {
		t.Fatal("op name should include package")
	}
}
