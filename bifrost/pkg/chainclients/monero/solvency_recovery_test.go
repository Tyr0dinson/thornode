package monero

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"go.uber.org/atomic"

	"gitlab.com/thorchain/thornode/v3/bifrost/blockscanner"
	"gitlab.com/thorchain/thornode/v3/bifrost/metrics"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/monero/sidecar"
	"gitlab.com/thorchain/thornode/v3/bifrost/reporter"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	stypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/config"
	ttypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

type recoverySolvencyBridge struct {
	thorclient.ThorchainBridge
	asgards ttypes.Vaults
}

func (*recoverySolvencyBridge) GetMimir(key string) (int64, error) {
	if key == "HaltScannerXMR" {
		return 0, nil
	}
	return -1, nil
}

func (*recoverySolvencyBridge) GetBlockHeight() (int64, error)                     { return 1000, nil }
func (*recoverySolvencyBridge) GetNetworkFee(common.Chain) (uint64, uint64, error) { return 1, 1, nil }

func (b *recoverySolvencyBridge) GetAsgards() (ttypes.Vaults, error) { return b.asgards, nil }
func (*recoverySolvencyBridge) FetchNodeStatus() (ttypes.NodeStatus, error) {
	return ttypes.NodeStatus_Active, nil
}

func (*recoverySolvencyBridge) GetSolvencyMsg(height int64, chain common.Chain, pubKey common.PubKey, coins common.Coins) *ttypes.MsgSolvency {
	return &ttypes.MsgSolvency{Height: height, Chain: chain, PubKey: pubKey, Coins: coins}
}

type recoverySolvencyAttester chan common.Solvency

func (a recoverySolvencyAttester) AttestSolvency(_ context.Context, solvency common.Solvency) error {
	a <- solvency
	return nil
}

func TestXMRSolvencyAfterAvailabilityRecovery(t *testing.T) {
	vaultKey, vaultAddress := testVaultPubKeyAndAddress(t)
	reconciled := true
	balanceServer := newFrostBalanceServer(t, map[string]sidecar.Balance{vaultKey.String(): {
		Balance: moneroAtomicToThorchain, UnlockedBalance: moneroAtomicToThorchain, SpendableBalance: uint64Ptr(moneroAtomicToThorchain),
		Reconciled: &reconciled, ReconciliationState: "reconciled", PendingOutgoingCount: intPtr(0),
	}})
	defer balanceServer.Close()
	chainServer := newFrostTransfersServer(t, nil, nil, nil)
	defer chainServer.Close()
	tip := atomic.NewInt64(99)
	ready := atomic.NewBool(false)
	failures := atomic.NewInt64(0)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1/monero/chain/height":
			_ = json.NewEncoder(w).Encode(sidecar.ChainHeightResp{Height: tip.Load()})
		case r.URL.Path == fmt.Sprintf("/v1/monero/chain/block/%d", tip.Load()) && tip.Load()%10 == 0 && !ready.Load():
			failures.Inc()
			http.Error(w, "block unavailable", http.StatusConflict)
		case strings.HasPrefix(r.URL.Path, "/v1/monero/balance/") || r.URL.Path == "/v1/monero/vaults/register":
			balanceServer.Config.Handler.ServeHTTP(w, r)
		default:
			chainServer.Config.Handler.ServeHTTP(w, r)
		}
	}))
	defer server.Close()
	signer, err := sidecar.NewClientWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
	if err != nil {
		t.Fatal(err)
	}
	scanner := NewScanner(zerolog.Nop(), signer, nil, common.XMRChain, common.EmptyPubKey, 72)
	scanner.lastFee.Store(moneroAtomicToThorchain)
	vault := ttypes.Vault{PubKey: vaultKey, PubKeyFrost: vaultKey, Coins: common.NewCoins(common.NewCoin(common.XMRAsset, cosmos.NewUint(1000)))}
	bridge := &recoverySolvencyBridge{asgards: ttypes.Vaults{vault}}
	reports := make(recoverySolvencyAttester, 4)
	client := &Client{
		logger: zerolog.Nop(), cfg: config.BifrostChainConfiguration{ChainID: common.XMRChain, SolvencyBlocks: 10}, chain: common.XMRChain,
		scanner: scanner, sidecar: signer, thorchainBridge: bridge,
		vaultAddressMap: map[string]common.Address{vaultKey.String(): vaultAddress},
	}
	shared, err := blockscanner.NewBlockScanner(config.BifrostBlockScannerConfiguration{
		ChainID: common.XMRChain, StartBlockHeight: 98, BlockHeightDiscoverBackoff: time.Millisecond,
	}, blockscanner.NewMockScannerStorage(), &metrics.Metrics{}, bridge, scanner)
	if err != nil {
		t.Fatal(err)
	}
	client.blockScanner = shared
	stopper := make(chan struct{})
	var wg sync.WaitGroup
	dispatcher := reporter.NewDispatcher(map[common.Chain]reporter.ChainClient{common.XMRChain: client}, nil, bridge, reports, nil, stopper, &wg)
	shared.Start(make(chan stypes.TxIn, 4), make(chan common.NetworkFee, 4), dispatcher)
	defer func() { shared.Stop(); close(stopper); wg.Wait() }()
	wait := func(condition func() bool) {
		t.Helper()
		deadline := time.Now().Add(3 * time.Second)
		for !condition() && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
		}
		if !condition() {
			t.Fatal("scanner did not reach expected state")
		}
	}
	wait(func() bool { return shared.PreviousHeight() == 99 && shared.IsHealthy() })
	// Complete the initial canonical report before changing the chain tip.
	// The dispatcher is asynchronous, so scanner progress alone is not a barrier.
	select {
	case baseline := <-reports:
		if baseline.Height != 90 {
			t.Fatalf("unexpected initial report height: %d", baseline.Height)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("initial solvency report did not complete")
	}
	height := int64(100)
	ready.Store(false)
	failures.Store(0)
	tip.Store(height)
	wait(func() bool { return failures.Load() >= 3 && !shared.IsHealthy() })
	if shared.PreviousHeight() != height-1 || len(reports) != 0 {
		t.Fatal("failed fetch advanced scanner or emitted solvency")
	}
	ready.Store(true)
	wait(func() bool { return shared.PreviousHeight() == height && shared.IsHealthy() })
	select {
	case report := <-reports:
		if report.Height != height || !report.PubKey.Equals(vaultKey) || !report.Chain.Equals(common.XMRChain) {
			t.Fatalf("unexpected insolvency report: %+v", report)
		}
		if !report.Coins.GetCoin(common.XMRAsset).Amount.LT(vault.Coins.GetCoin(common.XMRAsset).Amount) {
			t.Fatal("fixture must be insolvent")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("successful availability retry skipped insolvency report")
	}
	// Reports arrive asynchronously, so allow time for a duplicate to reach the
	// attester instead of checking the channel length immediately after receipt.
	select {
	case duplicate := <-reports:
		t.Fatalf("duplicate solvency report at height %d", duplicate.Height)
	case <-time.After(200 * time.Millisecond):
	}
}
