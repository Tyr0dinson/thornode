package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"

	sdkmath "cosmossdk.io/math"

	"gitlab.com/thorchain/thornode/v3/common"
	. "gitlab.com/thorchain/thornode/v3/test/simulation/pkg/types"
)

// The thornode and watchers packages capture their REST base URL from
// CHAIN_API in init() funcs, before any test code can run. To point them at an
// in-process fake without touching production code, TestMain re-executes the
// test binary with CHAIN_API (and CHAIN_ID, needed by types.NewUser) set to a
// loopback listener that the child process serves. Tests may install an
// override handler via setThornodeCmdCov; anything it does not handle falls
// back to healthy defaults so background watchers started by main() never see
// an error for the rest of the test binary.

const childAddrEnvCmdCov = "SIMCOV_CMD_THORNODE_ADDR"

var (
	thornodeMuCmdCov       sync.Mutex
	thornodeOverrideCmdCov func(http.ResponseWriter, *http.Request) bool
)

// setThornodeCmdCov installs h for the duration of t; h returns false to fall
// back to the default fake routes.
func setThornodeCmdCov(t *testing.T, h func(http.ResponseWriter, *http.Request) bool) {
	t.Helper()
	thornodeMuCmdCov.Lock()
	thornodeOverrideCmdCov = h
	thornodeMuCmdCov.Unlock()
	t.Cleanup(func() {
		thornodeMuCmdCov.Lock()
		thornodeOverrideCmdCov = nil
		thornodeMuCmdCov.Unlock()
	})
}

func defaultThornodeCmdCov(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Path
	switch {
	case strings.HasPrefix(p, "/thorchain/invariant/"):
		name := strings.TrimPrefix(p, "/thorchain/invariant/")
		_, _ = fmt.Fprintf(w, `{"invariant":%q,"broken":false,"msg":[]}`, name)
	case p == "/thorchain/mimir":
		_, _ = w.Write([]byte(`{}`))
	case p == "/thorchain/queue/outbound":
		_, _ = w.Write([]byte(`[]`))
	case p == "/thorchain/ragnarok":
		_, _ = w.Write([]byte(`false`))
	case p == "/thorchain/inbound_addresses":
		_, _ = w.Write([]byte(`[{"chain":"XMR","address":"xmr-inbound-cov"}]`))
	case strings.HasPrefix(p, "/cosmos/bank/v1beta1/balances/"):
		_, _ = w.Write([]byte(`{"balances":[{"denom":"rune","amount":"100"}]}`))
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func TestMain(m *testing.M) {
	if addr := os.Getenv(childAddrEnvCmdCov); addr != "" {
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			fmt.Fprintln(os.Stderr, "fake thornode listen:", err)
			os.Exit(97)
		}
		srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			thornodeMuCmdCov.Lock()
			h := thornodeOverrideCmdCov
			thornodeMuCmdCov.Unlock()
			if h != nil && h(w, r) {
				return
			}
			defaultThornodeCmdCov(w, r)
		})}
		go func() { _ = srv.Serve(ln) }()
		os.Exit(m.Run())
	}

	// parent: pick a free loopback port and re-exec with it configured
	for attempt := 0; attempt < 5; attempt++ {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			fmt.Fprintln(os.Stderr, "reserve port:", err)
			os.Exit(1)
		}
		addr := ln.Addr().String()
		_ = ln.Close()

		cmd := exec.Command(os.Args[0], os.Args[1:]...)
		cmd.Env = append(os.Environ(),
			childAddrEnvCmdCov+"="+addr,
			"CHAIN_API="+addr,
			"CHAIN_ID=thorchain",
		)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		err = cmd.Run()
		if err == nil {
			os.Exit(0)
		}
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			if exitErr.ExitCode() == 97 {
				continue // port was taken in between, retry
			}
			os.Exit(exitErr.ExitCode())
		}
		fmt.Fprintln(os.Stderr, "re-exec test binary:", err)
		os.Exit(1)
	}
	os.Exit(1)
}

////////////////////////////////////////////////////////////////////////////////////////
// Ledger-backed fake chain client
////////////////////////////////////////////////////////////////////////////////////////

// ledgerCmdCov is an in-memory balance sheet shared by fake clients of one chain.
type ledgerCmdCov struct {
	mu         sync.Mutex
	balances   map[common.Address]common.Coins
	broadcasts []SimTx
}

func newLedgerCmdCov() *ledgerCmdCov {
	return &ledgerCmdCov{balances: map[common.Address]common.Coins{}}
}

func (l *ledgerCmdCov) credit(addr common.Address, coin common.Coin) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.balances[addr] = l.balances[addr].Add(coin)
}

func (l *ledgerCmdCov) balance(addr common.Address, asset common.Asset) sdkmath.Uint {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.balances[addr].GetCoin(asset).Amount
}

func (l *ledgerCmdCov) sent() []SimTx {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]SimTx(nil), l.broadcasts...)
}

type ledgerClientCmdCov struct {
	addr   common.Address
	ledger *ledgerCmdCov
}

func (c *ledgerClientCmdCov) GetAccount(*common.PubKey) (*common.Account, error) {
	c.ledger.mu.Lock()
	defer c.ledger.mu.Unlock()
	return &common.Account{Coins: append(common.Coins(nil), c.ledger.balances[c.addr]...)}, nil
}

func (c *ledgerClientCmdCov) SignTx(tx SimTx) ([]byte, error) {
	return json.Marshal(tx)
}

func (c *ledgerClientCmdCov) BroadcastTx(b []byte) (string, error) {
	var tx SimTx
	if err := json.Unmarshal(b, &tx); err != nil {
		return "", err
	}
	c.ledger.credit(tx.ToAddress, tx.Coin)
	c.ledger.mu.Lock()
	c.ledger.broadcasts = append(c.ledger.broadcasts, tx)
	n := len(c.ledger.broadcasts)
	c.ledger.mu.Unlock()
	return fmt.Sprintf("tx%d", n), nil
}
