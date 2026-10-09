package main

import (
	"errors"
	"math/big"
	rand "math/rand/v2"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"

	sdkmath "cosmossdk.io/math"

	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/config"
	acommon "gitlab.com/thorchain/thornode/v3/test/simulation/actors/common"
	"gitlab.com/thorchain/thornode/v3/test/simulation/pkg/evm"
	"gitlab.com/thorchain/thornode/v3/test/simulation/pkg/monero"
	. "gitlab.com/thorchain/thornode/v3/test/simulation/pkg/types"
)

// usersCmdCov builds master + two users; the child test process has CHAIN_ID
// set so NewUser works offline.
func usersCmdCov(t *testing.T, constructors map[common.Chain]LiteChainClientConstructor) (*User, *User, *User) {
	t.Helper()
	master := NewUser(mocknetMasterMnemonic, constructors)
	u1 := NewUser(mocknetUserMnemonics[0], constructors)
	u2 := NewUser(mocknetUserMnemonics[1], constructors)
	return master, u1, u2
}

// withLedgerClientsCmdCov wires ledger-backed fake clients for chain.
func withLedgerClientsCmdCov(chain common.Chain, users ...*User) *ledgerCmdCov {
	l := newLedgerCmdCov()
	for _, u := range users {
		u.ChainClients[chain] = &ledgerClientCmdCov{addr: u.Address(chain), ledger: l}
	}
	return l
}

func TestFundUserChainAccountsTopsUpOnlyMissingCov(t *testing.T) {
	for _, chain := range []common.Chain{common.BTCChain, common.SOLChain} {
		t.Run(chain.String(), func(t *testing.T) {
			master, u1, u2 := usersCmdCov(t, nil)
			l := withLedgerClientsCmdCov(chain, master, u1, u2)
			gas := chain.GetGasAsset()
			l.credit(u1.Address(chain), common.NewCoin(gas, sdkmath.NewUint(500)))
			l.credit(u2.Address(chain), common.NewCoin(gas, sdkmath.NewUint(30)))

			fundUserChainAccounts(master, []*User{u1, u2}, chain, sdkmath.NewUint(100))

			if got := l.balance(u1.Address(chain), gas); got.Uint64() != 500 {
				t.Fatalf("already-funded user must not be topped up, got %s", got)
			}
			if got := l.balance(u2.Address(chain), gas); got.Uint64() != 100 {
				t.Fatalf("user must be topped up to target, got %s", got)
			}
			sent := l.sent()
			if len(sent) != 1 || sent[0].Memo != "SIMULATION:"+u2.Name() || sent[0].Coin.Amount.Uint64() != 70 {
				t.Fatalf("unexpected funding txs %+v", sent)
			}
		})
	}
}

func TestFundUserChainAccountsEVMFundsGasAndTokensCov(t *testing.T) {
	fake := newEVMFakeCmdCov(t)
	cons := map[common.Chain]LiteChainClientConstructor{common.ETHChain: evm.NewConstructor(fake.server.URL)}
	master, u1, u2 := usersCmdCov(t, cons)

	rich := new(big.Int).Exp(big.NewInt(10), big.NewInt(30), nil)
	fake.setBalance(master.Address(common.ETHChain).String(), rich)
	fake.setBalance(u1.Address(common.ETHChain).String(), rich) // gas and TKN already funded

	fundUserChainAccounts(master, []*User{u1, u2}, common.ETHChain, sdkmath.NewUint(100*common.One))

	// u2 needs one gas transfer and one TKN transfer; u1 needs nothing
	if fake.sentRaw != 2 {
		t.Fatalf("expected 2 funding txs, got %d", fake.sentRaw)
	}
	if fake.count("eth_getTransactionReceipt") != 2 {
		t.Fatalf("deferred receipts must be flushed once each, got %d", fake.count("eth_getTransactionReceipt"))
	}
}

func TestFundUserZecAccountsSplitsAndWaitsCov(t *testing.T) {
	master, u1, u2 := usersCmdCov(t, nil)
	l := withLedgerClientsCmdCov(common.ZECChain, master, u1, u2)
	l.credit(u1.Address(common.ZECChain), common.NewCoin(common.ZECAsset, sdkmath.NewUint(1000)))
	l.credit(u2.Address(common.ZECChain), common.NewCoin(common.ZECAsset, sdkmath.NewUint(150)))

	fundUserZecAccounts(master, []*User{u1, u2}, sdkmath.NewUint(100), 3)

	if got := l.balance(u2.Address(common.ZECChain), common.ZECAsset); got.Uint64() != 300 {
		t.Fatalf("expected u2 topped up to 300, got %s", got)
	}
	sent := l.sent()
	if len(sent) != 2 || sent[0].Coin.Amount.Uint64() != 100 || sent[1].Coin.Amount.Uint64() != 50 {
		t.Fatalf("expected transfers of 100 and 50, got %+v", sent)
	}
	if got := l.balance(u1.Address(common.ZECChain), common.ZECAsset); got.Uint64() != 1000 {
		t.Fatalf("funded user must be untouched, got %s", got)
	}
}

func TestFundUserXMRAccountsFaucetsEveryUserCov(t *testing.T) {
	helper := newMoneroHelperFakeCmdCov(t, 77, 5_000*1e12)
	cons := map[common.Chain]LiteChainClientConstructor{common.XMRChain: monero.NewConstructor(helper.server.URL)}
	master, u1, u2 := usersCmdCov(t, cons)

	fundUserXMRAccounts(master, []*User{u1, u2}, sdkmath.NewUint(1000*common.One))

	helper.mu.Lock()
	defer helper.mu.Unlock()
	if len(helper.faucets) != 2 ||
		helper.faucets[0] != u1.Address(common.XMRChain).String() ||
		helper.faucets[1] != u2.Address(common.XMRChain).String() {
		t.Fatalf("unexpected faucet recipients %v", helper.faucets)
	}
	if helper.balances != 2 {
		t.Fatalf("expected one balance check per user, got %d", helper.balances)
	}
	for _, h := range helper.birthday {
		if h != 77 {
			t.Fatalf("user clients must use the master birthday height, got %v", helper.birthday)
		}
	}
}

func TestFundUserThorAccountSkipsUsersWithRuneCov(t *testing.T) {
	var queried string
	setThornodeCmdCov(t, func(w http.ResponseWriter, r *http.Request) bool {
		if strings.HasPrefix(r.URL.Path, "/cosmos/bank/v1beta1/balances/") {
			queried = strings.TrimPrefix(r.URL.Path, "/cosmos/bank/v1beta1/balances/")
		}
		return false
	})
	master, u1, _ := usersCmdCov(t, nil)
	if !fundUserThorAccount(master, u1) {
		t.Fatal("chain seeding must continue for users with rune")
	}
	if queried != u1.Address(common.THORChain).String() {
		t.Fatalf("expected balance lookup for %s, got %s", u1.Address(common.THORChain), queried)
	}
}

func TestInitConfigWithoutSeedCapsUsersCov(t *testing.T) {
	restoreSimEnvCmdCov(t)
	fakes := map[common.Chain]*ledgerCmdCov{}
	orig := liteClientConstructors
	liteClientConstructors = map[common.Chain]LiteChainClientConstructor{}
	for _, chain := range []common.Chain{common.BTCChain, common.XRPChain} {
		l := newLedgerCmdCov()
		fakes[chain] = l
		liteClientConstructors[chain] = func(chain common.Chain, _ *thorclient.Keys) (LiteChainClient, error) {
			return &ledgerClientCmdCov{ledger: l}, nil
		}
		// XRP requires a funded master account before it is considered ready
		l.credit("", common.NewCoin(chain.GetGasAsset(), sdkmath.NewUint(1)))
	}
	t.Cleanup(func() { liteClientConstructors = orig })

	cfg := InitConfig(100, false, rand.New(rand.NewPCG(1, 2)))
	if len(cfg.NodeUsers) != len(mocknetValidatorMnemonics) {
		t.Fatalf("expected %d node users, got %d", len(mocknetValidatorMnemonics), len(cfg.NodeUsers))
	}
	if len(cfg.Users) != len(mocknetUserMnemonics) {
		t.Fatalf("parallelism must be capped to %d users, got %d", len(mocknetUserMnemonics), len(cfg.Users))
	}
	seen := map[string]bool{}
	for _, u := range cfg.Users {
		if seen[u.Name()] {
			t.Fatalf("duplicate user %s", u.Name())
		}
		seen[u.Name()] = true
		if u.ChainClients[common.BTCChain] == nil || u.ChainClients[common.XRPChain] == nil {
			t.Fatal("users must get a client per configured constructor")
		}
	}
	if len(fakes[common.BTCChain].sent()) != 0 {
		t.Fatal("non-seed init must not fund accounts")
	}
}

// restoreSimEnvCmdCov snapshots env vars InitConfig overwrites and reloads the
// config package afterwards.
func restoreSimEnvCmdCov(t *testing.T) {
	t.Helper()
	t.Cleanup(config.Init)
	for _, k := range []string{
		"BIFROST_THORCHAIN_CHAIN_HOST",
		"BIFROST_THORCHAIN_CHAIN_RPC",
		"BIFROST_THORCHAIN_CHAIN_EBIFROST",
	} {
		k := k
		if v, ok := os.LookupEnv(k); ok {
			t.Setenv(k, v)
		} else {
			t.Setenv(k, "x")
			_ = os.Unsetenv(k)
		}
	}
	xmr := acommon.XMRChainEnabled()
	t.Cleanup(func() { acommon.SetXMRChainEnabled(xmr) })
}

// richClientCmdCov reports a large balance of the chain gas asset (and, for
// non-master accounts on EVM chains, every simulation token) so funding is a
// no-op. Transactions are never expected.
type richClientCmdCov struct {
	chain  common.Chain
	master bool
}

func (c *richClientCmdCov) GetAccount(*common.PubKey) (*common.Account, error) {
	rich := sdkmath.NewUint(1_000_000_000 * common.One)
	coins := common.Coins{common.NewCoin(c.chain.GetGasAsset(), rich)}
	if c.chain.IsEVM() && !c.master {
		for asset := range evm.Tokens(c.chain) {
			coins = append(coins, common.NewCoin(asset, rich))
		}
	}
	return &common.Account{Coins: coins}, nil
}

func (c *richClientCmdCov) SignTx(SimTx) ([]byte, error) {
	return nil, errors.New("unexpected sign")
}

func (c *richClientCmdCov) BroadcastTx([]byte) (string, error) {
	return "", errors.New("unexpected broadcast")
}

// TestInitConfigSeedPostsFeesAndFundsCov runs the seeding path. InitConfig
// hard-codes the THORChain API to 127.0.0.1:1317 (it overwrites
// BIFROST_THORCHAIN_CHAIN_HOST before building users), so the network fee
// posts can only be served by a fake bound to that port; the test skips when
// the port is in use (for example by a running mocknet).
func TestInitConfigSeedPostsFeesAndFundsCov(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:1317")
	if err != nil {
		t.Skipf("127.0.0.1:1317 unavailable: %v", err)
	}
	var mu sync.Mutex
	nodeQueries := 0
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/thorchain/node/") {
			mu.Lock()
			nodeQueries++
			mu.Unlock()
			// a non-active node makes PostNetworkFee a successful no-op
			_, _ = w.Write([]byte(`{}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	})}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })

	restoreSimEnvCmdCov(t)
	acommon.SetXMRChainEnabled(true)
	helper := newMoneroHelperFakeCmdCov(t, 5, 5_000*1e12)

	orig := liteClientConstructors
	liteClientConstructors = map[common.Chain]LiteChainClientConstructor{
		common.XMRChain: monero.NewConstructor(helper.server.URL),
	}
	for _, chain := range acommon.SimChains {
		if chain.Equals(common.XMRChain) {
			continue
		}
		liteClientConstructors[chain] = func(chain common.Chain, keys *thorclient.Keys) (LiteChainClient, error) {
			return &richClientCmdCov{chain: chain, master: keys.GetSignerInfo().Name == "master"}, nil
		}
	}
	t.Cleanup(func() { liteClientConstructors = orig })

	cfg := InitConfig(2, true, rand.New(rand.NewPCG(3, 4)))
	if len(cfg.Users) != 2 || len(cfg.NodeUsers) != len(mocknetValidatorMnemonics) {
		t.Fatalf("unexpected users %d/%d", len(cfg.Users), len(cfg.NodeUsers))
	}
	mu.Lock()
	defer mu.Unlock()
	if nodeQueries == 0 {
		t.Fatal("seeding must post network fees through the first validator")
	}
	helper.mu.Lock()
	defer helper.mu.Unlock()
	if len(helper.faucets) != 2 {
		t.Fatalf("expected an XMR faucet per user, got %v", helper.faucets)
	}
}
