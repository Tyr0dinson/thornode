package monero

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/rs/zerolog/log"
	"go.uber.org/atomic"

	stypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/config"
	ttypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

func boolPtr(v bool) *bool {
	return &v
}

func TestShouldReportSolvency(t *testing.T) {
	c := &Client{
		cfg:                     config.BifrostChainConfiguration{SolvencyBlocks: 10},
		scanner:                 &Scanner{lastFee: atomic.NewUint64(3)},
		lastSolvencyCheckHeight: atomic.NewInt64(0),
	}

	if c.ShouldReportSolvency(9) {
		t.Fatal("should not report on non-solvency block")
	}
	if !c.ShouldReportSolvency(10) {
		t.Fatal("should report on solvency block")
	}

	c.lastSolvencyCheckHeight.Store(10)
	if c.ShouldReportSolvency(10) {
		t.Fatal("should not report at same height twice")
	}

	c.scanner.lastFee.Store(0)
	if c.ShouldReportSolvency(20) {
		t.Fatal("should not report when fee is unknown")
	}
}

func TestShouldReportSolvencyRequiresInitializedState(t *testing.T) {
	c := &Client{
		cfg: config.BifrostChainConfiguration{SolvencyBlocks: 10},
	}
	if c.ShouldReportSolvency(10) {
		t.Fatal("should not report when scanner and solvency state are not initialized")
	}
}

func TestXMRAccountIncludesSameVaultMempoolSelfSend(t *testing.T) {
	t.Parallel()

	vaultPubKey, vaultAddress := testVaultPubKeyAndAddress(t)
	baseBalance := uint64(10 * moneroAtomicToThorchain)
	pendingSelfSend := uint64(90 * moneroAtomicToThorchain)
	c, server := newTestXMRSolvencyClient(t, 500, ttypes.Vaults{{
		PubKey:      vaultPubKey,
		PubKeyEddsa: vaultPubKey,
		PubKeyFrost: vaultPubKey,
		Coins:       common.NewCoins(common.NewCoin(common.XMRAsset, cosmos.NewUint(100))),
	}}, map[string]xmrFrostBalanceResponse{
		vaultPubKey.String(): {
			Balance:          baseBalance,
			UnlockedBalance:  baseBalance,
			SpendableBalance: uint64Ptr(baseBalance),
		},
	}, map[string][]xmrFrostOutgoingTransferStateEntry{
		vaultPubKey.String(): {{
			TxHash:        strings.Repeat("a", 64),
			Status:        xmrFrostOutgoingStatusObservedMempool,
			FromAddress:   vaultAddress.String(),
			ToAddress:     vaultAddress.String(),
			Amount:        pendingSelfSend,
			Fee:           1,
			Memo:          "consolidate",
			PlannedHeight: 500,
			CreatedAt:     1,
		}, {
			TxHash:        strings.Repeat("b", 64),
			Status:        xmrFrostOutgoingStatusObservedMempool,
			FromAddress:   vaultAddress.String(),
			ToAddress:     testOutboundXMRDestination().String(),
			Amount:        pendingSelfSend,
			Fee:           1,
			Memo:          "OUT:ignored",
			PlannedHeight: 500,
			CreatedAt:     2,
		}},
	})
	defer server.Close()

	acct, err := c.GetAccountByAddress(vaultAddress.String(), nil)
	if err != nil {
		t.Fatalf("get account: %v", err)
	}
	coin := acct.Coins.GetCoin(common.XMRAsset)
	if !coin.Amount.Equal(cosmos.NewUint(100)) {
		t.Fatalf("expected same-vault mempool self-send to count toward account balance, got %s", coin.Amount)
	}
}

func newFrostSolvencyStateServer(
	t *testing.T,
	chainHeight int64,
	balanceByPubKey map[string]xmrFrostBalanceResponse,
	outgoingByPubKey map[string][]xmrFrostOutgoingTransferStateEntry,
) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/monero/vaults/register":
			_ = json.NewEncoder(w).Encode(xmrFrostRegisterVaultResponse{Registered: true})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/monero/chain/height":
			_ = json.NewEncoder(w).Encode(xmrFrostChainHeightResponse{Height: chainHeight})
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/monero/balance/"):
			pk := strings.TrimPrefix(r.URL.Path, "/v1/monero/balance/")
			resp, ok := balanceByPubKey[pk]
			if !ok {
				http.Error(w, fmt.Sprintf("unknown vault pubkey: %s", pk), http.StatusNotFound)
				return
			}
			_ = json.NewEncoder(w).Encode(resp)
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/monero/outbounds/"):
			pk, err := url.PathUnescape(strings.TrimPrefix(r.URL.Path, "/v1/monero/outbounds/"))
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			_ = json.NewEncoder(w).Encode(xmrFrostOutgoingTransfersStateResponse{
				VaultPubKey: pk,
				Outgoing:    append([]xmrFrostOutgoingTransferStateEntry(nil), outgoingByPubKey[pk]...),
			})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/monero/transfers" && r.URL.Query().Get("mempool") == "true":
			resp := xmrFrostTransfersResponse{}
			for pk, outgoing := range outgoingByPubKey {
				for _, tx := range outgoing {
					if !strings.EqualFold(tx.Status, xmrFrostOutgoingStatusObservedMempool) {
						continue
					}
					resp.Outgoing = append(resp.Outgoing, xmrFrostTransferEntry{
						TxID:        tx.TxHash,
						Address:     tx.ToAddress,
						FromAddress: tx.FromAddress,
						VaultPubKey: pk,
						TxKeys:      append([]string(nil), tx.TxKeys...),
						Amount:      tx.Amount,
						Fee:         tx.Fee,
						Height:      tx.PlannedHeight,
						Note:        tx.Memo,
						Locked:      true,
					})
				}
			}
			_ = json.NewEncoder(w).Encode(resp)
		default:
			http.NotFound(w, r)
		}
	}))
}

func newTestXMRSolvencyClient(
	t *testing.T,
	chainHeight int64,
	asgards ttypes.Vaults,
	balanceByPubKey map[string]xmrFrostBalanceResponse,
	outgoingByPubKey map[string][]xmrFrostOutgoingTransferStateEntry,
) (*Client, *httptest.Server) {
	t.Helper()

	server := newFrostSolvencyStateServer(t, chainHeight, balanceByPubKey, outgoingByPubKey)
	frostSigner := newTestFrostSignerWithServer(t, server)

	bridge := &solvencyBridge{asgards: asgards}
	scanner := NewScanner(log.Logger, frostSigner, bridge, common.XMRChain, common.EmptyPubKey, 0)
	scanner.currentBlockHeight.Store(chainHeight)
	scanner.lastFee.Store(10)

	client := &Client{
		logger:                  log.Logger,
		cfg:                     config.BifrostChainConfiguration{SolvencyBlocks: 10},
		chain:                   common.XMRChain,
		thorchainBridge:         bridge,
		scanner:                 scanner,
		frostSigner:             frostSigner,
		vaultAddressMap:         make(map[string]common.Address),
		lastSolvencyCheckHeight: atomic.NewInt64(0),
	}
	for _, vault := range asgards {
		address, err := vault.PubKey.GetAddress(common.XMRChain)
		if err != nil || address.IsEmpty() {
			continue
		}
		client.setVaultAddressMapping(vault.PubKey, address)
		client.setVaultAddressMapping(vault.PubKeyEddsa, address)
		client.setVaultAddressMapping(vault.PubKeyFrost, address)
	}

	return client, server
}

func TestXMRSolvencySystemIdleRejectsNonIdleStates(t *testing.T) {
	t.Parallel()

	vaultPubKey, _ := testVaultPubKeyAndAddress(t)
	asgards := ttypes.Vaults{{
		PubKey:      vaultPubKey,
		PubKeyEddsa: vaultPubKey,
		PubKeyFrost: vaultPubKey,
		Coins:       common.NewCoins(common.NewCoin(common.XMRAsset, cosmos.NewUint(100))),
	}}
	balanceByPK := map[string]xmrFrostBalanceResponse{
		vaultPubKey.String(): {
			Balance:          100 * moneroAtomicToThorchain,
			UnlockedBalance:  100 * moneroAtomicToThorchain,
			SpendableBalance: uint64Ptr(100 * moneroAtomicToThorchain),
		},
	}

	t.Run("scanner behind tip", func(t *testing.T) {
		c, server := newTestXMRSolvencyClient(t, 500, asgards, balanceByPK, nil)
		defer server.Close()
		c.scanner.currentBlockHeight.Store(490)

		idle, reason, err := c.xmrSolvencySystemIdle()
		if err != nil {
			t.Fatalf("xmrSolvencySystemIdle: %v", err)
		}
		if idle {
			t.Fatal("expected idle check to fail while scanner is behind tip")
		}
		if !strings.Contains(reason, "not at tip") {
			t.Fatalf("expected tip reason, got %q", reason)
		}
	})

	t.Run("key-image queue not empty", func(t *testing.T) {
		c, server := newTestXMRSolvencyClient(t, 500, asgards, balanceByPK, nil)
		defer server.Close()
		c.keyImageCeremonyGate = newXMRKeyImageCeremonyGate()
		c.keyImageCeremonyGate.queued = 1

		idle, reason, err := c.xmrSolvencySystemIdle()
		if err != nil {
			t.Fatalf("xmrSolvencySystemIdle: %v", err)
		}
		if idle {
			t.Fatal("expected idle check to fail while key-image queue is non-empty")
		}
		if !strings.Contains(reason, "key-image queue not empty") {
			t.Fatalf("expected key-image queue reason, got %q", reason)
		}
	})

	t.Run("pending thornode outbound queue", func(t *testing.T) {
		pendingAsgards := ttypes.Vaults{{
			PubKey:                vaultPubKey,
			PubKeyEddsa:           vaultPubKey,
			PubKeyFrost:           vaultPubKey,
			Coins:                 common.NewCoins(common.NewCoin(common.XMRAsset, cosmos.NewUint(100))),
			PendingTxBlockHeights: []int64{123},
		}}
		c, server := newTestXMRSolvencyClient(t, 500, pendingAsgards, balanceByPK, nil)
		defer server.Close()

		idle, reason, err := c.xmrSolvencySystemIdle()
		if err != nil {
			t.Fatalf("xmrSolvencySystemIdle: %v", err)
		}
		if idle {
			t.Fatal("expected idle check to fail while thornode outbound queue is non-empty")
		}
		if !strings.Contains(reason, "thornode outbound queue not empty") {
			t.Fatalf("expected outbound queue reason, got %q", reason)
		}
	})

	t.Run("outbound still awaiting final observation", func(t *testing.T) {
		c, server := newTestXMRSolvencyClient(t, 500, asgards, balanceByPK, map[string][]xmrFrostOutgoingTransferStateEntry{
			vaultPubKey.String(): {{
				TxHash:    strings.Repeat("a", 64),
				Status:    "observed_mined",
				TxKeys:    []string{"tx-key"},
				Amount:    1,
				Fee:       1,
				Memo:      "OUT:abc",
				CreatedAt: 1,
			}},
		})
		defer server.Close()

		idle, reason, err := c.xmrSolvencySystemIdle()
		if err != nil {
			t.Fatalf("xmrSolvencySystemIdle: %v", err)
		}
		if idle {
			t.Fatal("expected idle check to fail while finalized outbound propagation is incomplete")
		}
		if !strings.Contains(reason, "awaiting finalized observation") {
			t.Fatalf("expected finalized observation reason, got %q", reason)
		}
	})

	t.Run("outbound still awaiting change materialization", func(t *testing.T) {
		localChangeMaterialized := false
		c, server := newTestXMRSolvencyClient(t, 500, asgards, balanceByPK, map[string][]xmrFrostOutgoingTransferStateEntry{
			vaultPubKey.String(): {{
				TxHash:                  strings.Repeat("b", 64),
				Status:                  "observed_mined",
				TxKeys:                  []string{"tx-key"},
				HasOwnedChange:          boolPtr(true),
				LocalChangeMaterialized: &localChangeMaterialized,
				PendingReason:           "awaiting_change_materialization",
				Amount:                  1,
				Fee:                     1,
				Memo:                    "OUT:def",
				CreatedAt:               1,
			}},
		})
		defer server.Close()

		idle, reason, err := c.xmrSolvencySystemIdle()
		if err != nil {
			t.Fatalf("xmrSolvencySystemIdle: %v", err)
		}
		if idle {
			t.Fatal("expected idle check to fail while tracked outbound change has not materialized")
		}
		if !strings.Contains(reason, "awaiting change materialization") {
			t.Fatalf("expected change materialization reason, got %q", reason)
		}
	})
}

func TestReportSolvencySkipsWhenXMRSystemIsNotIdle(t *testing.T) {
	t.Parallel()

	vaultPubKey, vaultAddress := testVaultPubKeyAndAddress(t)
	asgards := ttypes.Vaults{{
		PubKey:      vaultPubKey,
		PubKeyEddsa: vaultPubKey,
		PubKeyFrost: vaultPubKey,
		Coins:       common.NewCoins(common.NewCoin(common.XMRAsset, cosmos.NewUint(100))),
	}}
	c, server := newTestXMRSolvencyClient(t, 500, asgards, map[string]xmrFrostBalanceResponse{
		vaultPubKey.String(): {
			Balance:          100 * moneroAtomicToThorchain,
			UnlockedBalance:  100 * moneroAtomicToThorchain,
			SpendableBalance: uint64Ptr(100 * moneroAtomicToThorchain),
		},
	}, nil)
	defer server.Close()

	queue := make(chan stypes.Solvency, 2)
	c.globalSolvencyQueue = queue
	c.vaultAddressMap[vaultPubKey.String()] = vaultAddress
	c.keyImageCeremonyGate = newXMRKeyImageCeremonyGate()
	c.keyImageCeremonyGate.active = true

	if err := c.ReportSolvency(10); err != nil {
		t.Fatalf("report solvency: %v", err)
	}
	if len(queue) != 0 {
		t.Fatalf("expected no solvency message while xmr system is not idle, got %d", len(queue))
	}
	if c.lastSolvencyCheckHeight.Load() != 0 {
		t.Fatalf("expected idle-gated solvency skip to leave last height unchanged, got %d", c.lastSolvencyCheckHeight.Load())
	}
}

func TestReportSolvencyChecksImmediatelyAfterXMRBecomesIdle(t *testing.T) {
	t.Parallel()

	vaultPubKey, vaultAddress := testVaultPubKeyAndAddress(t)
	asgards := ttypes.Vaults{{
		PubKey:      vaultPubKey,
		PubKeyEddsa: vaultPubKey,
		PubKeyFrost: vaultPubKey,
		Coins:       common.NewCoins(common.NewCoin(common.XMRAsset, cosmos.NewUint(100))),
	}}
	c, server := newTestXMRSolvencyClient(t, 500, asgards, map[string]xmrFrostBalanceResponse{
		vaultPubKey.String(): {
			Balance:          10 * moneroAtomicToThorchain,
			UnlockedBalance:  10 * moneroAtomicToThorchain,
			SpendableBalance: uint64Ptr(10 * moneroAtomicToThorchain),
		},
	}, nil)
	defer server.Close()

	queue := make(chan stypes.Solvency, 1)
	c.globalSolvencyQueue = queue
	c.vaultAddressMap[vaultPubKey.String()] = vaultAddress
	c.scanner.currentBlockHeight.Store(490)

	if err := c.ReportSolvency(10); err != nil {
		t.Fatalf("report solvency while syncing: %v", err)
	}
	if len(queue) != 0 {
		t.Fatalf("expected no solvency message while syncing, got %d", len(queue))
	}
	if c.lastSolvencyCheckHeight.Load() != 0 {
		t.Fatalf("expected non-idle skip to leave last height unchanged, got %d", c.lastSolvencyCheckHeight.Load())
	}
	if c.solvencyCheckAfterIdle == nil || !c.solvencyCheckAfterIdle.Load() {
		t.Fatal("expected next idle block to force a solvency check")
	}

	c.scanner.currentBlockHeight.Store(500)
	if err := c.ReportSolvency(11); err != nil {
		t.Fatalf("report solvency after idle: %v", err)
	}
	if len(queue) != 1 {
		t.Fatalf("expected immediate insolvency message after idle, got %d", len(queue))
	}
	if c.lastSolvencyCheckHeight.Load() != 11 {
		t.Fatalf("expected last solvency check height 11, got %d", c.lastSolvencyCheckHeight.Load())
	}
	if c.solvencyCheckAfterIdle.Load() {
		t.Fatal("expected forced solvency check flag to clear after reporting")
	}
}

func TestReportSolvencyReportsOnlyInsolventVaultsWhenXMRSystemIsIdle(t *testing.T) {
	t.Parallel()

	vaultPubKey, vaultAddress := testVaultPubKeyAndAddress(t)
	asgards := ttypes.Vaults{{
		PubKey:      vaultPubKey,
		PubKeyEddsa: vaultPubKey,
		PubKeyFrost: vaultPubKey,
		Coins:       common.NewCoins(common.NewCoin(common.XMRAsset, cosmos.NewUint(100))),
	}}
	c, server := newTestXMRSolvencyClient(t, 500, asgards, map[string]xmrFrostBalanceResponse{
		vaultPubKey.String(): {
			Balance:          10 * moneroAtomicToThorchain,
			UnlockedBalance:  10 * moneroAtomicToThorchain,
			SpendableBalance: uint64Ptr(10 * moneroAtomicToThorchain),
		},
	}, nil)
	defer server.Close()

	queue := make(chan stypes.Solvency, 1)
	c.globalSolvencyQueue = queue
	c.vaultAddressMap[vaultPubKey.String()] = vaultAddress

	if err := c.ReportSolvency(10); err != nil {
		t.Fatalf("report solvency: %v", err)
	}
	if len(queue) != 1 {
		t.Fatalf("expected one insolvency message, got %d", len(queue))
	}
	msg := <-queue
	if !msg.PubKey.Equals(vaultPubKey) {
		t.Fatalf("expected insolvency message for vault %s, got %s", vaultPubKey, msg.PubKey)
	}
	if c.lastSolvencyCheckHeight.Load() != 10 {
		t.Fatalf("expected last solvency check height 10, got %d", c.lastSolvencyCheckHeight.Load())
	}
}
