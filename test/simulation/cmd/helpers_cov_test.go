package main

import (
	"errors"
	"strings"
	"testing"
	"time"

	sdkmath "cosmossdk.io/math"

	"gitlab.com/thorchain/thornode/v3/common"
	. "gitlab.com/thorchain/thornode/v3/test/simulation/pkg/types"
)

func TestWaitForMasterAccountTerminalStatesCov(t *testing.T) {
	sleeps := 0
	sleep := func(time.Duration) { sleeps++ }

	// zero balance on a chain that does not need funding is ready immediately
	zero := &stubLiteChainClient{getAccount: func(*common.PubKey) (*common.Account, error) {
		return &common.Account{Coins: common.Coins{common.NewCoin(common.BTCAsset, sdkmath.ZeroUint())}}, nil
	}}
	if acct, err := waitForMasterAccountWithSleep(common.BTCChain, zero, sleep); err != nil || acct == nil {
		t.Fatalf("BTC master must be ready without balance, got %v %v", acct, err)
	}

	// nil account is never ready and is not a retryable zero-balance state
	nilAcct := &stubLiteChainClient{getAccount: func(*common.PubKey) (*common.Account, error) { return nil, nil }}
	if _, err := waitForMasterAccountWithSleep(common.XRPChain, nilAcct, sleep); err == nil ||
		!strings.Contains(err.Error(), "master account balance is still zero") {
		t.Fatalf("expected zero balance error, got %v", err)
	}

	// permanent RPC errors are returned without retry
	perm := errors.New("unauthorized")
	permClient := &stubLiteChainClient{getAccount: func(*common.PubKey) (*common.Account, error) { return nil, perm }}
	if _, err := waitForMasterAccountWithSleep(common.BTCChain, permClient, sleep); !errors.Is(err, perm) {
		t.Fatalf("expected permanent error, got %v", err)
	}

	// a transient error that never clears gives up after the last attempt
	calls := 0
	transient := &stubLiteChainClient{getAccount: func(*common.PubKey) (*common.Account, error) {
		calls++
		return nil, errors.New("dial tcp: i/o timeout")
	}}
	if _, err := waitForMasterAccountWithSleep(common.BTCChain, transient, sleep); err == nil || calls != masterAccountRetryAttempts {
		t.Fatalf("expected %d attempts then error, got %d %v", masterAccountRetryAttempts, calls, err)
	}
	if sleeps != masterAccountRetryAttempts-1 {
		t.Fatalf("expected %d sleeps, got %d", masterAccountRetryAttempts-1, sleeps)
	}

	// zero XRP balance retries until the last attempt and then fails
	calls, sleeps = 0, 0
	zeroXRP := &stubLiteChainClient{getAccount: func(*common.PubKey) (*common.Account, error) {
		calls++
		return &common.Account{Coins: common.Coins{common.NewCoin(common.XRPAsset, sdkmath.ZeroUint())}}, nil
	}}
	if _, err := waitForMasterAccountWithSleep(common.XRPChain, zeroXRP, sleep); err == nil || calls != masterAccountRetryAttempts {
		t.Fatalf("expected zero-balance retries to exhaust, got %d %v", calls, err)
	}
}

func TestSmallMocknetHelpersCov(t *testing.T) {
	t.Setenv("SIMCOV_ENV_OR_DEFAULT", "  value  ")
	if got := envOrDefault("SIMCOV_ENV_OR_DEFAULT", "fallback"); got != "value" {
		t.Fatalf("expected trimmed env value, got %q", got)
	}
	if got := envOrDefault("SIMCOV_ENV_OR_DEFAULT_UNSET", "fallback"); got != "fallback" {
		t.Fatalf("expected fallback, got %q", got)
	}

	if seedThor, seedChains := thorUserFundingPlan(nil); !seedThor || !seedChains {
		t.Fatal("users without rune must be seeded on THORChain and chains")
	}

	if isRetryableTransportStartupError(nil) {
		t.Fatal("nil error is not retryable")
	}
	if isRetryableTransportStartupError(errors.New("bad request")) {
		t.Fatal("permanent error is not retryable")
	}

	wantErr := errors.New("height unavailable")
	clients := map[common.Chain]LiteChainClient{
		common.XRPChain: &heightErrClientCmdCov{err: wantErr},
	}
	if _, err := getHeight(common.XRPChain, clients); !errors.Is(err, wantErr) {
		t.Fatalf("expected provider error, got %v", err)
	}
}

type heightErrClientCmdCov struct {
	stubLiteChainClient
	err error
}

func (c *heightErrClientCmdCov) GetHeight() (int64, error) { return 0, c.err }
