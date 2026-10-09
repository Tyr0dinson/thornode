package core

import (
	"errors"
	"math/big"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	. "gitlab.com/thorchain/thornode/v3/test/simulation/pkg/types"
)

func TestNewSolvencyCheckActorCovD(t *testing.T) {
	a := NewSolvencyCheckActor(covDRNG())
	if a.Name != "SolvencyCheck" || len(a.Ops) != 1 || !strings.Contains(a.Ops[0].String(), "checkSolvency") {
		t.Fatalf("unexpected actor %+v", a)
	}
}

func TestSolvencyHelpersCovD(t *testing.T) {
	items := appendUniqueString(nil, "")
	items = appendUniqueString(items, "a")
	items = appendUniqueString(items, "a")
	items = appendUniqueString(items, "b")
	if strings.Join(items, ",") != "a,b" {
		t.Fatalf("unexpected unique strings %v", items)
	}
	m := map[string]cosmos.Uint{}
	addToUintMap(m, "x", cosmos.NewUint(2))
	addToUintMap(m, "x", cosmos.NewUint(3))
	if !m["x"].Equal(cosmos.NewUint(5)) {
		t.Fatalf("unexpected sum %s", m["x"])
	}
	if solvencyMismatchRetryable(nil) {
		t.Fatal("no mismatches is not retryable")
	}
}

// covDSolvencyClients holds the per-scenario L1 behaviour.
type covDSolvencyClients struct {
	btc, sol, xmr *covChainClient
	ethAllowance  *big.Int
	ethErr        bool
}

// TestCheckSolvencyScenariosCovD runs every checkSolvency scenario concurrently
// against one shared fake THORNode, because each call starts with an
// unconditional 5s sleep (check_solvency.go). Scenarios differ only by the L1
// clients of the user that acquires them. No scenario produces a retryable
// (XMR/SOL-only) mismatch, so the 5s retry delay is never hit.
func TestCheckSolvencyScenariosCovD(t *testing.T) {
	if testing.Short() {
		t.Skip("checkSolvency sleeps 5s before querying")
	}
	api := newCovThornode(t)
	token := covDTokenAsset(t)

	secpOwner, _ := newCovUser(t, 15, nil)
	edOwner, _ := newCovUser(t, 16, nil)
	frostOwner, _ := newCovUser(t, 17, nil)
	vaultPub := secpOwner.PubKey(common.THORChain)
	vaultEd := edOwner.PubKey(common.SOLChain)
	vaultFrost := frostOwner.PubKey(common.XMRChain)

	api.JSON(covDPathInbound, []interface{}{covDInbound(common.ETHChain, covDETHVault, covDRouter, "")})
	api.JSON(covDPathVaults, []map[string]interface{}{
		{
			"status": covDActiveVault, "pub_key": vaultPub.String(), "pub_key_eddsa": vaultEd.String(),
			"coins": []map[string]string{
				{"asset": "BTC.BTC", "amount": "1000"},
				{"asset": "SOL.SOL", "amount": "500"},
				{"asset": token.String(), "amount": "300"},
				// XMR without a FROST key is skipped
				{"asset": "XMR.XMR", "amount": "999"},
			},
		},
		{
			// same keys again: L1 balances must only be counted once
			"status": covDRetiringVlt, "pub_key": vaultPub.String(), "pub_key_eddsa": vaultEd.String(),
			"coins": []map[string]string{
				{"asset": "BTC.BTC", "amount": "200"},
				{"asset": "SOL.SOL", "amount": "0"},
				{"asset": token.String(), "amount": "0"},
			},
		},
		{
			"status": covDActiveVault, "pub_key_frost": vaultFrost.String(),
			"coins": []map[string]string{{"asset": "XMR.XMR", "amount": "100"}},
		},
	})

	good := func() covDSolvencyClients {
		return covDSolvencyClients{
			btc:          newCovChainClient(common.NewCoin(common.BTCAsset, cosmos.NewUint(1300))), // excess
			sol:          newCovChainClient(common.NewCoin(common.SOLAsset, cosmos.NewUint(500))),
			xmr:          newCovChainClient(common.NewCoin(common.XMRAsset, cosmos.NewUint(90))), // within tolerance
			ethAllowance: big.NewInt(3_000_000_000_000),                                          // 300 * 1e10
		}
	}

	type scenario struct {
		name    string
		clients covDSolvencyClients
		noUser  bool
		check   func(t *testing.T, res OpResult)
	}
	retry := func(t *testing.T, res OpResult) {
		t.Helper()
		if res.Continue || res.Finish || res.Error != nil {
			t.Fatalf("expected retry, got %+v", res)
		}
	}
	var btcQueries atomic.Int32
	var btcQueriedKey atomic.Value
	solvent := good()
	solventAcct := solvent.btc.Account
	solvent.btc.AccountFn = func(pk *common.PubKey) (*common.Account, error) {
		btcQueries.Add(1)
		btcQueriedKey.Store(pk.String())
		acct := solventAcct
		return &acct, nil
	}
	scenarios := []scenario{
		{name: "solvent", clients: solvent, check: func(t *testing.T, res OpResult) {
			if !res.Continue || res.Finish || res.Error != nil {
				t.Fatalf("expected solvent, got %+v", res)
			}
			// two vaults share the key: the L1 balance is queried once
			if btcQueries.Load() != 1 || btcQueriedKey.Load() != vaultPub.String() {
				t.Fatalf("expected one BTC query for the vault key, got %d (%v)", btcQueries.Load(), btcQueriedKey.Load())
			}
		}},
		{name: "btc account error", clients: func() covDSolvencyClients {
			c := good()
			c.btc.AccountErr = errors.New("daemon down")
			return c
		}(), check: retry},
		{name: "sol account error", clients: func() covDSolvencyClients {
			c := good()
			c.sol.AccountErr = errors.New("daemon down")
			return c
		}(), check: retry},
		{name: "xmr account error", clients: func() covDSolvencyClients {
			c := good()
			c.xmr.AccountErr = errors.New("daemon down")
			return c
		}(), check: retry},
		{name: "evm allowance error", clients: func() covDSolvencyClients {
			c := good()
			c.ethErr = true
			return c
		}(), check: retry},
		{name: "btc shortfall", clients: func() covDSolvencyClients {
			c := good()
			c.btc = newCovChainClient(common.NewCoin(common.BTCAsset, cosmos.NewUint(1199)))
			return c
		}(), check: func(t *testing.T, res OpResult) {
			if !res.Finish || res.Error == nil || !strings.Contains(res.Error.Error(), "do not match") {
				t.Fatalf("expected non-retryable mismatch, got %+v", res)
			}
		}},
		{name: "no user", noUser: true, check: retry},
	}

	type run struct {
		sc     scenario
		user   *User
		locked *User
		res    OpResult
	}
	runs := make([]*run, 0, len(scenarios))
	for i, sc := range scenarios {
		evmClient, rpc := newCovEVMClient(t, common.ETHChain, i%12)
		if sc.clients.ethErr {
			rpc.CallFn = func(string, []byte) (*big.Int, error) { return nil, errors.New("reverted") }
		} else {
			rpc.CallResult = sc.clients.ethAllowance
		}
		r := &run{sc: sc}
		if sc.clients.btc != nil {
			r.user, _ = newCovUser(t, i%12, map[common.Chain]LiteChainClient{
				common.BTCChain: sc.clients.btc,
				common.SOLChain: sc.clients.sol,
				common.XMRChain: sc.clients.xmr,
				common.ETHChain: evmClient,
			})
		}
		// a locked user is skipped in favour of the next one
		r.locked, _ = newCovUser(t, (i+1)%12, nil)
		if !r.locked.Acquire() {
			t.Fatal("acquire")
		}
		runs = append(runs, r)
	}

	var wg sync.WaitGroup
	for _, r := range runs {
		wg.Add(1)
		go func(r *run) {
			defer wg.Done()
			users := []*User{r.locked}
			if r.user != nil {
				users = append(users, r.user)
			}
			a := &SolvencyCheckActor{Actor: *NewActor("SolvencyCheck", covDRNG())}
			r.res = a.checkSolvency(&OpConfig{Users: users})
		}(r)
	}
	wg.Wait()

	for _, r := range runs {
		t.Run(r.sc.name, func(t *testing.T) {
			r.sc.check(t, r.res)
			if r.user != nil && r.user.IsLocked() {
				t.Fatal("user lock leaked")
			}
		})
	}
}
