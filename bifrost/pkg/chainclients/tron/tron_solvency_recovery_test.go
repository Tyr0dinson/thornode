package tron

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rs/zerolog"

	"gitlab.com/thorchain/thornode/v3/bifrost/blockscanner"
	"gitlab.com/thorchain/thornode/v3/bifrost/metrics"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/tron/api"
	"gitlab.com/thorchain/thornode/v3/bifrost/pubkeymanager"
	stypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/config"
	ttypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

type recoverySolvencyBridge struct{ mockTronSolvencyBridge }

func (*recoverySolvencyBridge) GetMimir(string) (int64, error)                     { return -1, nil }
func (*recoverySolvencyBridge) GetBlockHeight() (int64, error)                     { return 1000, nil }
func (*recoverySolvencyBridge) GetNetworkFee(common.Chain) (uint64, uint64, error) { return 1, 1, nil }

func TestTRONSolvencyAfterReceiptRecovery(t *testing.T) {
	const height int64 = 100
	const address = "41b8c702bc7c186ad437b798dae8c61727d3c69a85"
	vaultAddress, err := api.ConvertAddress(address)
	if err != nil {
		t.Fatal(err)
	}
	txID := strings.Repeat("a", 64)
	var tip, failedReceipts atomic.Int64
	var receiptReady atomic.Bool
	tip.Store(height - 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/wallet/getnowblock":
			_, _ = fmt.Fprintf(w, `{"block_header":{"raw_data":{"number":%d}}}`, tip.Load()+ConfirmationBlocks)
		case "/wallet/getblockbynum":
			var request struct {
				Num int64 `json:"num"`
			}
			if decodeErr := json.NewDecoder(r.Body).Decode(&request); decodeErr != nil {
				http.Error(w, decodeErr.Error(), 400)
				return
			}
			if request.Num != height {
				_, _ = fmt.Fprintf(w, `{"block_header":{"raw_data":{"number":%d}}}`, request.Num)
				return
			}
			_, _ = fmt.Fprintf(w, `{"block_header":{"raw_data":{"number":100,"timestamp":1000}},"transactions":[{"txID":%q,"raw_data":{"contract":[{"type":"TransferContract","parameter":{"value":{"owner_address":%q,"to_address":%q,"amount":5000000}}}]},"ret":[{"contractRet":"SUCCESS"}]}]}`, txID, address, address)
		case "/wallet/gettransactioninfobyblocknum":
			if !receiptReady.Load() {
				failedReceipts.Add(1)
				_, _ = fmt.Fprint(w, `{"Error":"receipt unavailable"}`)
				return
			}
			_, _ = fmt.Fprintf(w, `[{"id":%q,"blockNumber":100,"fee":700}]`, txID)
		case "/wallet/getaccount":
			_, _ = fmt.Fprint(w, `{"balance":1}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	vault := ttypes.Vault{PubKey: ttypes.GetRandomPubKey(), Coins: common.NewCoins(common.NewCoin(common.TRXAsset, cosmos.NewUint(common.One)))}
	bridge := &recoverySolvencyBridge{mockTronSolvencyBridge{asgards: ttypes.Vaults{vault}}}
	scanner := &TronBlockScanner{
		config: config.BifrostBlockScannerConfiguration{ChainID: common.TRONChain, ObservationFlexibilityBlocks: 3},
		logger: zerolog.Nop(), api: api.NewTronApi(server.URL, time.Second),
		pubkeyMgr: receiptRetryVaultValidator{MockPoolAddressValidator: pubkeymanager.NewMockPoolAddressValidator(), address: vaultAddress},
	}
	reports := make(chan stypes.Solvency, 4)
	client := &TronClient{
		logger: zerolog.Nop(), config: config.BifrostChainConfiguration{ChainID: common.TRONChain}, chainId: common.TRONChain.String(),
		tronScanner: scanner, bridge: bridge, api: scanner.api, globalSolvencyQueue: reports,
	}
	scanner.reportSolvency = client.ReportSolvency
	shared, err := blockscanner.NewBlockScanner(config.BifrostBlockScannerConfiguration{
		ChainID: common.TRONChain, StartBlockHeight: height - 2, BlockHeightDiscoverBackoff: time.Millisecond,
	}, blockscanner.NewMockScannerStorage(), &metrics.Metrics{}, bridge, scanner)
	if err != nil {
		t.Fatal(err)
	}
	client.blockScanner = shared
	shared.Start(make(chan stypes.TxIn, 4), make(chan common.NetworkFee, 4))
	defer shared.Stop()
	wait := func(ready func() bool) {
		t.Helper()
		deadline := time.Now().Add(3 * time.Second)
		for !ready() && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
		}
		if !ready() {
			t.Fatal("scanner did not reach expected state")
		}
	}
	wait(func() bool { return shared.PreviousHeight() == height-1 && shared.IsHealthy() })
	// The first healthy scan checks the cycle current at startup, labelled with
	// its boundary, before the block that completed it is persisted.
	select {
	case report := <-reports:
		if report.Height != height-solvencyReportInterval-FinalityBlocks {
			t.Fatalf("unexpected startup report height %d", report.Height)
		}
	default:
		t.Fatal("startup cycle was not checked")
	}
	tip.Store(height)
	wait(func() bool { return failedReceipts.Load() >= 3 && !shared.IsHealthy() })
	if shared.PreviousHeight() != height-1 || len(reports) != 0 {
		t.Fatal("failed receipt advanced scanner or emitted solvency")
	}
	receiptReady.Store(true)
	wait(func() bool { return shared.PreviousHeight() == height && shared.IsHealthy() })
	select {
	case report := <-reports:
		if report.Height != height-FinalityBlocks || !report.PubKey.Equals(vault.PubKey) || !report.Chain.Equals(common.TRONChain) {
			t.Fatalf("unexpected insolvency report: %+v", report)
		}
		if !report.Coins.GetCoin(common.TRXAsset).Amount.LT(vault.Coins.GetCoin(common.TRXAsset).Amount) {
			t.Fatal("fixture must be insolvent")
		}
	default:
		t.Fatal("successful receipt retry skipped insolvency report")
	}
	if len(reports) != 0 {
		t.Fatal("duplicate solvency report")
	}
	// FetchTxs can also run in a prefetch worker. It must not report on its own.
	if _, err := scanner.FetchTxs(height, height); err != nil {
		t.Fatal(err)
	}
	if len(reports) != 0 {
		t.Fatal("FetchTxs still reports outside the ordered loop")
	}
}

func TestTRONSolvencyHookReportsEachCycleOnce(t *testing.T) {
	client := &TronClient{}
	// tip is the head GetHeight returns when the hook refreshes it.
	var tip, headRequests atomic.Int64
	var failHead atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/wallet/getnowblock" {
			http.NotFound(w, r)
			return
		}
		headRequests.Add(1)
		if failHead.Load() {
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		_, _ = fmt.Fprintf(w, `{"block_header":{"raw_data":{"number":%d}}}`, tip.Load()+ConfirmationBlocks)
	}))
	defer server.Close()

	var reported []int64
	var failReport bool
	scanner := &TronBlockScanner{
		config: config.BifrostBlockScannerConfiguration{ObservationFlexibilityBlocks: 2},
		logger: zerolog.Nop(),
		api:    api.NewTronApi(server.URL, time.Second),
		reportSolvency: func(height int64) error {
			if !client.ShouldReportSolvency(height) {
				t.Errorf("hook reported off-cadence height %d", height)
			}
			if failReport {
				return errors.New("asgards unavailable")
			}
			reported = append(reported, height)
			return nil
		},
	}
	for _, step := range []struct {
		name                     string
		fetchHeight, chainHeight int64
		tip                      int64
		failHead, failReport     bool
		wantErr                  bool
		wantHeadRequests         int64
		want                     []int64
	}{
		{"behind the tip", 97, 100, 100, false, false, false, 0, nil},
		{"startup cycle", 98, 99, 99, false, false, false, 1, []int64{90}},
		{"same cycle", 99, 99, 99, false, false, false, 1, []int64{90}},
		// The loop's head skipped 100; the cycle is still checked once.
		{"crossed boundary", 100, 101, 101, false, false, false, 2, []int64{90, 100}},
		{"same cycle again", 101, 101, 102, false, false, false, 2, []int64{90, 100}},
		// The loop's head is a block stale; the label follows the fresh head.
		{"head moved during fetch", 109, 110, 120, false, false, false, 3, []int64{90, 100, 120}},
		{"coalesced cycle stays done", 120, 120, 120, false, false, false, 3, []int64{90, 100, 120}},
		// A lagging load-balanced endpoint must not move the cycle backwards.
		{"stale endpoint", 130, 131, 128, false, false, false, 4, []int64{90, 100, 120, 130}},
		{"head unavailable", 140, 140, 140, true, false, true, 5, []int64{90, 100, 120, 130}},
		{"report failed", 140, 140, 140, false, true, true, 6, []int64{90, 100, 120, 130}},
		{"retry", 141, 141, 141, false, false, false, 7, []int64{90, 100, 120, 130, 140}},
	} {
		tip.Store(step.tip)
		failHead.Store(step.failHead)
		failReport = step.failReport
		err := scanner.ReportSolvency(step.fetchHeight, step.chainHeight)
		if (err != nil) != step.wantErr {
			t.Fatalf("%s: err = %v, want error %v", step.name, err, step.wantErr)
		}
		if got := headRequests.Load(); got != step.wantHeadRequests {
			t.Fatalf("%s: head requests = %d, want %d", step.name, got, step.wantHeadRequests)
		}
		if fmt.Sprint(reported) != fmt.Sprint(step.want) {
			t.Fatalf("%s: reported %v, want %v", step.name, reported, step.want)
		}
	}
}
