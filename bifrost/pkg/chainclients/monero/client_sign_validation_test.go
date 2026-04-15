package monero

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	stypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/config"
	ttypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

const nonGuaranteedFeaturedSubaddress = "CjWdTpuDaZ69nTGxzm9YarR82YDYFECi1WaaREZTMy5yDsjaRX5bC3cbC3JpcrBPd7YYpjoWKuBMidgGaKBK5Jye2v3wfMHCy"

func TestValidateXMRSignTxRejectsInboundMemo(t *testing.T) {
	t.Parallel()

	vaultPubKey, _ := testVaultPubKeyAndAddress(t)
	c := &Client{chain: common.XMRChain}
	tx := stypes.TxOutItem{
		Chain:            common.XMRChain,
		ToAddress:        testOutboundXMRDestination(),
		VaultPubKey:      vaultPubKey,
		VaultPubKeyEddsa: vaultPubKey,
		Coins: common.Coins{
			common.NewCoin(common.XMRAsset, cosmos.NewUint(1)),
		},
		Memo: "DONATE:XMR.XMR",
	}

	_, err := c.validateXMRSignTx(tx, 100, true)
	if err == nil {
		t.Fatalf("expected inbound memo validation error")
	}
	if !strings.Contains(err.Error(), "inbound memo") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateXMRSignTxAcceptsEmptyMemo(t *testing.T) {
	t.Parallel()

	vaultPubKey, _ := testVaultPubKeyAndAddress(t)
	c := &Client{chain: common.XMRChain}
	tx := stypes.TxOutItem{
		Chain:            common.XMRChain,
		ToAddress:        testOutboundXMRDestination(),
		VaultPubKey:      vaultPubKey,
		VaultPubKeyEddsa: vaultPubKey,
		Coins: common.Coins{
			common.NewCoin(common.XMRAsset, cosmos.NewUint(1)),
		},
		Memo: "",
	}

	_, err := c.validateXMRSignTx(tx, 100, true)
	if err != nil {
		t.Fatalf("empty memo should be accepted: %v", err)
	}
}

func TestValidateOutboundObservationCallsSidecar(t *testing.T) {
	t.Parallel()

	vaultPubKey, _ := testVaultPubKeyAndAddress(t)

	var captured xmrFrostValidateOutboundRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method != http.MethodPost || r.URL.Path != "/v1/monero/outbounds/validate" {
			http.NotFound(w, r)
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(xmrFrostValidateOutboundResponse{Tracked: true, Marked: 2})
	}))
	defer server.Close()

	frostSigner, err := NewFrostSignerWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
	if err != nil {
		t.Fatalf("new frost signer: %v", err)
	}

	c := &Client{
		chain:       common.XMRChain,
		frostSigner: frostSigner,
	}

	obsTx := common.NewObservedTx(
		common.Tx{
			ID:          common.TxID(strings.Repeat("a", 64)),
			Chain:       common.XMRChain,
			FromAddress: common.Address(common.XMRChecksummedDummyAddress('4')),
			ToAddress:   testOutboundXMRDestination(),
			Coins:       common.NewCoins(common.NewCoin(common.XMRAsset, cosmos.NewUint(55))),
			Gas:         common.Gas{common.NewCoin(common.XMRAsset, cosmos.NewUint(3))},
			Memo:        "OUT:" + strings.Repeat("B", 64),
		},
		100,
		vaultPubKey,
		0,
	)
	obsTx.TxKeys = []string{strings.Repeat("1", 64), strings.Repeat("2", 64)}

	if err := c.ValidateOutboundObservation(obsTx); err != nil {
		t.Fatalf("validate outbound observation: %v", err)
	}

	if captured.VaultPubKey != vaultPubKey.String() {
		t.Fatalf("expected vault pubkey %s, got %s", vaultPubKey, captured.VaultPubKey)
	}
	if captured.TxHash != obsTx.Tx.ID.String() {
		t.Fatalf("expected tx hash %s, got %s", obsTx.Tx.ID, captured.TxHash)
	}
	if len(captured.TxKeys) != 2 || captured.TxKeys[0] != obsTx.TxKeys[0] || captured.TxKeys[1] != obsTx.TxKeys[1] {
		t.Fatalf("unexpected tx keys: %+v", captured.TxKeys)
	}
	if captured.Amount != 55*moneroAtomicToThorchain {
		t.Fatalf("expected atomic amount %d, got %d", 55*moneroAtomicToThorchain, captured.Amount)
	}
	if captured.Fee != 3*moneroAtomicToThorchain {
		t.Fatalf("expected atomic fee %d, got %d", 3*moneroAtomicToThorchain, captured.Fee)
	}
}

func TestValidateOutboundObservationWithoutTxKeysSkipsTxKeyAudit(t *testing.T) {
	t.Parallel()

	vaultPubKey, _ := testVaultPubKeyAndAddress(t)

	var outboundValidateCalls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1/monero/outbounds/validate":
			outboundValidateCalls++
			t.Fatal("outbound tx-key validation should be skipped when tx keys are missing")
		case strings.HasPrefix(r.URL.Path, "/v1/monero/outputs/"):
			_ = json.NewEncoder(w).Encode(xmrFrostOutputsResponse{Outputs: nil})
		default:
			http.Error(w, "not found", http.StatusNotFound)
		}
	}))
	defer server.Close()

	frostSigner, err := NewFrostSignerWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
	if err != nil {
		t.Fatalf("new frost signer: %v", err)
	}

	c := &Client{
		chain:       common.XMRChain,
		frostSigner: frostSigner,
		logger:      testLogger(),
	}

	obsTx := common.NewObservedTx(
		common.Tx{
			ID:          common.TxID(strings.Repeat("b", 64)),
			Chain:       common.XMRChain,
			FromAddress: common.Address(common.XMRChecksummedDummyAddress('4')),
			ToAddress:   testOutboundXMRDestination(),
			Coins:       common.NewCoins(common.NewCoin(common.XMRAsset, cosmos.NewUint(55))),
			Gas:         common.Gas{common.NewCoin(common.XMRAsset, cosmos.NewUint(3))},
			Memo:        "OUT:" + strings.Repeat("C", 64),
		},
		100,
		vaultPubKey,
		0,
	)

	if err := c.ValidateOutboundObservation(obsTx); err != nil {
		t.Fatalf("validate outbound observation without tx keys: %v", err)
	}
	if outboundValidateCalls != 0 {
		t.Fatalf("unexpected outbound validate calls: %d", outboundValidateCalls)
	}
}

func TestValidateXMRSignTxAcceptsCheckpoint(t *testing.T) {
	t.Parallel()

	vaultPubKey, _ := testVaultPubKeyAndAddress(t)
	c := &Client{chain: common.XMRChain}
	ckpt, _ := json.Marshal(signedTxPayload{
		TxHash:       strings.Repeat("c", 64),
		TxMetadata:   "deadbeef",
		AmountAtomic: 1,
		FeeAtomic:    1,
		MsgID:        "msg-id",
		TxKeys:       []string{strings.Repeat("1", 64)},
	})
	tx := stypes.TxOutItem{
		Chain:            common.XMRChain,
		ToAddress:        testOutboundXMRDestination(),
		VaultPubKey:      vaultPubKey,
		VaultPubKeyEddsa: vaultPubKey,
		Coins: common.Coins{
			common.NewCoin(common.XMRAsset, cosmos.NewUint(1)),
		},
		Memo:       "OUT:0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF",
		Checkpoint: ckpt,
	}

	_, err := c.validateXMRSignTx(tx, 100, true)
	if err != nil {
		t.Fatalf("checkpoint should be accepted: %v", err)
	}
}

func TestValidateXMRSignTxAcceptsStandardDestination(t *testing.T) {
	t.Parallel()

	vaultPubKey, _ := testVaultPubKeyAndAddress(t)
	c := &Client{chain: common.XMRChain}
	tx := stypes.TxOutItem{
		Chain:            common.XMRChain,
		ToAddress:        common.Address(common.XMRChecksummedDummyAddress('8')),
		VaultPubKey:      vaultPubKey,
		VaultPubKeyEddsa: vaultPubKey,
		Coins: common.Coins{
			common.NewCoin(common.XMRAsset, cosmos.NewUint(1)),
		},
		Memo: "OUT:0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF",
	}

	_, err := c.validateXMRSignTx(tx, 100, true)
	if err != nil {
		t.Fatalf("standard destination should be accepted: %v", err)
	}
}

func TestValidateXMRSignTxAcceptsNonGuaranteedFeaturedDestination(t *testing.T) {
	t.Parallel()

	vaultPubKey, _ := testVaultPubKeyAndAddress(t)
	c := &Client{chain: common.XMRChain}
	tx := stypes.TxOutItem{
		Chain:            common.XMRChain,
		ToAddress:        common.Address(nonGuaranteedFeaturedSubaddress),
		VaultPubKey:      vaultPubKey,
		VaultPubKeyEddsa: vaultPubKey,
		Coins: common.Coins{
			common.NewCoin(common.XMRAsset, cosmos.NewUint(1)),
		},
		Memo: "OUT:0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF",
	}

	_, err := c.validateXMRSignTx(tx, 100, true)
	if err != nil {
		t.Fatalf("non-guaranteed featured destination should be accepted: %v", err)
	}
}

func TestValidateXMRSignTxRejectsInsufficientBalance(t *testing.T) {
	t.Parallel()

	vaultPubKey, vaultAddr := testVaultPubKeyAndAddress(t)

	// Mock frost signer server that returns low balance
	balanceServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost && r.URL.Path == "/v1/monero/vaults/register" {
			_ = json.NewEncoder(w).Encode(xmrFrostRegisterVaultResponse{Registered: true})
			return
		}
		if strings.Contains(r.URL.Path, "/v1/monero/balance/") {
			_ = json.NewEncoder(w).Encode(xmrFrostBalanceResponse{
				Balance:         2 * moneroAtomicToThorchain,
				UnlockedBalance: 2 * moneroAtomicToThorchain,
			})
			return
		}
		http.NotFound(w, r)
	}))
	defer balanceServer.Close()

	cfg := config.BifrostChainConfiguration{}
	frostSigner, err := NewFrostSignerWithEndpoint(cfg, balanceServer.URL)
	if err != nil {
		t.Fatalf("new frost signer: %v", err)
	}

	c := &Client{
		chain:       common.XMRChain,
		frostSigner: frostSigner,
		vaultAddressMap: map[string]common.Address{
			vaultPubKey.String(): vaultAddr,
		},
	}
	tx := stypes.TxOutItem{
		Chain:            common.XMRChain,
		ToAddress:        testOutboundXMRDestination(),
		VaultPubKey:      vaultPubKey,
		VaultPubKeyEddsa: vaultPubKey,
		Coins: common.Coins{
			common.NewCoin(common.XMRAsset, cosmos.NewUint(2)),
		},
		MaxGas: common.Gas{
			common.NewCoin(common.XMRAsset, cosmos.NewUint(1)),
		},
		Memo: "OUT:0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF",
	}

	_, err = c.validateXMRSignTx(tx, 100, true)
	if err == nil {
		t.Fatalf("expected insufficient balance error")
	}
	if !strings.Contains(err.Error(), "insufficient xmr balance") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateXMRSignTxRejectsLockedOnlyBalance(t *testing.T) {
	t.Parallel()

	vaultPubKey, vaultAddr := testVaultPubKeyAndAddress(t)

	balanceServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost && r.URL.Path == "/v1/monero/vaults/register" {
			_ = json.NewEncoder(w).Encode(xmrFrostRegisterVaultResponse{Registered: true})
			return
		}
		if strings.Contains(r.URL.Path, "/v1/monero/balance/") {
			_ = json.NewEncoder(w).Encode(xmrFrostBalanceResponse{
				Balance:         10 * moneroAtomicToThorchain,
				UnlockedBalance: 0,
			})
			return
		}
		http.NotFound(w, r)
	}))
	defer balanceServer.Close()

	cfg := config.BifrostChainConfiguration{}
	frostSigner, err := NewFrostSignerWithEndpoint(cfg, balanceServer.URL)
	if err != nil {
		t.Fatalf("new frost signer: %v", err)
	}

	c := &Client{
		chain:       common.XMRChain,
		frostSigner: frostSigner,
		vaultAddressMap: map[string]common.Address{
			vaultPubKey.String(): vaultAddr,
		},
	}
	tx := stypes.TxOutItem{
		Chain:            common.XMRChain,
		ToAddress:        testOutboundXMRDestination(),
		VaultPubKey:      vaultPubKey,
		VaultPubKeyEddsa: vaultPubKey,
		Coins: common.Coins{
			common.NewCoin(common.XMRAsset, cosmos.NewUint(1)),
		},
		Memo: "OUT:0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF",
	}

	_, err = c.validateXMRSignTx(tx, 100, true)
	if err == nil {
		t.Fatalf("expected insufficient balance error when funds are fully locked")
	}
	if !strings.Contains(err.Error(), "insufficient xmr balance") {
		t.Fatalf("unexpected error: %v", err)
	}
	var retryErr interface {
		error
		RetryAfterNextBlock() bool
	}
	if !errors.As(err, &retryErr) {
		t.Fatalf("expected locked balance error to request retry after next block: %v", err)
	}
	if !retryErr.RetryAfterNextBlock() {
		t.Fatalf("expected locked balance error to request retry after next block")
	}
}

func TestValidateXMRSignTxFallsBackFromLegacyEddsaToFrostBalanceLookup(t *testing.T) {
	t.Parallel()

	eddsaPubKey, vaultAddr := testVaultPubKeyAndAddress(t)
	frostPubKey, _ := testVaultPubKeyAndAddress(t)
	ecdsaPubKey := testVaultPubKey(t, "ecdsa-legacy-migrate")

	var (
		mu          sync.Mutex
		balanceHits = make(map[string]int)
	)
	balanceServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost && r.URL.Path == "/v1/monero/vaults/register" {
			_ = json.NewEncoder(w).Encode(xmrFrostRegisterVaultResponse{Registered: true})
			return
		}
		if strings.HasPrefix(r.URL.Path, "/v1/monero/balance/") {
			pk := strings.TrimPrefix(r.URL.Path, "/v1/monero/balance/")
			mu.Lock()
			balanceHits[pk]++
			mu.Unlock()
			switch pk {
			case eddsaPubKey.String():
				_ = json.NewEncoder(w).Encode(xmrFrostBalanceResponse{})
				return
			case frostPubKey.String():
				_ = json.NewEncoder(w).Encode(xmrFrostBalanceResponse{
					Balance:         5 * moneroAtomicToThorchain,
					UnlockedBalance: 5 * moneroAtomicToThorchain,
				})
				return
			default:
				http.Error(w, fmt.Sprintf("unknown vault pubkey: %s", pk), http.StatusNotFound)
				return
			}
		}
		http.NotFound(w, r)
	}))
	defer balanceServer.Close()

	frostSigner, err := NewFrostSignerWithEndpoint(config.BifrostChainConfiguration{}, balanceServer.URL)
	if err != nil {
		t.Fatalf("new frost signer: %v", err)
	}

	c := &Client{
		chain:       common.XMRChain,
		frostSigner: frostSigner,
		thorchainBridge: &keysignFailureBridge{
			vaults: map[string]ttypes.Vault{
				frostPubKey.String(): {
					PubKey:      ecdsaPubKey,
					PubKeyEddsa: eddsaPubKey,
					PubKeyFrost: frostPubKey,
				},
			},
		},
		vaultAddressMap: map[string]common.Address{
			eddsaPubKey.String(): vaultAddr,
		},
	}
	tx := stypes.TxOutItem{
		Chain:            common.XMRChain,
		ToAddress:        testOutboundXMRDestination(),
		VaultPubKey:      ecdsaPubKey,
		VaultPubKeyEddsa: eddsaPubKey,
		Coins: common.Coins{
			common.NewCoin(common.XMRAsset, cosmos.NewUint(1)),
		},
		Memo: "OUT:3333333333333333333333333333333333333333333333333333333333333333",
	}

	req, err := c.validateXMRSignTx(tx, 100, true)
	if err != nil {
		t.Fatalf("expected fallback to frost balance lookup to succeed: %v", err)
	}
	if !req.vaultPubKey.Equals(eddsaPubKey) {
		t.Fatalf("expected validated request to preserve legacy outbound vault pubkey %s, got %s", eddsaPubKey, req.vaultPubKey)
	}
	if req.fromAddress != vaultAddr.String() {
		t.Fatalf("expected validated request from address %s, got %s", vaultAddr, req.fromAddress)
	}

	mu.Lock()
	eddsaHits := balanceHits[eddsaPubKey.String()]
	frostHits := balanceHits[frostPubKey.String()]
	mu.Unlock()
	if eddsaHits == 0 {
		t.Fatalf("expected balance check against legacy eddsa pubkey")
	}
	if frostHits == 0 {
		t.Fatalf("expected fallback balance check against frost pubkey")
	}
}

func TestShouldSweepXMRMigration(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		tx           stypes.TxOutItem
		maxGasAtomic uint64
		expected     bool
	}{
		{
			name: "migrate with gas",
			tx: stypes.TxOutItem{
				Memo: "MIGRATE:123",
			},
			maxGasAtomic: 1,
			expected:     true,
		},
		{
			name: "migrate without gas",
			tx: stypes.TxOutItem{
				Memo: "MIGRATE:123",
			},
			maxGasAtomic: 0,
			expected:     false,
		},
		{
			name: "non-migrate",
			tx: stypes.TxOutItem{
				Memo: "OUT:1234567890ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF",
			},
			maxGasAtomic: 1,
			expected:     false,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := shouldSweepXMRMigration(tc.tx, tc.maxGasAtomic); got != tc.expected {
				t.Fatalf("expected %t, got %t", tc.expected, got)
			}
		})
	}
}

func TestValidateXMRSignTxRejectsInsufficientSpendableBalance(t *testing.T) {
	t.Parallel()

	vaultPubKey, vaultAddr := testVaultPubKeyAndAddress(t)

	balanceServer := newFrostBalanceServer(t, map[string]xmrFrostBalanceResponse{
		vaultPubKey.String(): {
			Balance:          10 * moneroAtomicToThorchain,
			UnlockedBalance:  10 * moneroAtomicToThorchain,
			SpendableBalance: uint64Ptr(moneroAtomicToThorchain),
		},
	})
	defer balanceServer.Close()

	cfg := config.BifrostChainConfiguration{}
	frostSigner, err := NewFrostSignerWithEndpoint(cfg, balanceServer.URL)
	if err != nil {
		t.Fatalf("new frost signer: %v", err)
	}

	c := &Client{
		chain:       common.XMRChain,
		frostSigner: frostSigner,
		vaultAddressMap: map[string]common.Address{
			vaultPubKey.String(): vaultAddr,
		},
	}
	tx := stypes.TxOutItem{
		Chain:            common.XMRChain,
		ToAddress:        testOutboundXMRDestination(),
		VaultPubKey:      vaultPubKey,
		VaultPubKeyEddsa: vaultPubKey,
		Coins: common.Coins{
			common.NewCoin(common.XMRAsset, cosmos.NewUint(2)),
		},
		Memo: "OUT:0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF",
	}

	_, err = c.validateXMRSignTx(tx, 100, true)
	if err == nil {
		t.Fatalf("expected insufficient balance error when spendable balance is below amount")
	}
	if !strings.Contains(err.Error(), "insufficient xmr balance") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateXMRSignTxAcceptsValidRequest(t *testing.T) {
	t.Parallel()

	vaultPubKey, _ := testVaultPubKeyAndAddress(t)
	c := &Client{chain: common.XMRChain}
	tx := stypes.TxOutItem{
		Chain:            common.XMRChain,
		ToAddress:        testOutboundXMRDestination(),
		VaultPubKey:      vaultPubKey,
		VaultPubKeyEddsa: vaultPubKey,
		Coins: common.Coins{
			common.NewCoin(common.XMRAsset, cosmos.NewUint(1)),
		},
		Memo: "OUT:0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF",
	}

	req, err := c.validateXMRSignTx(tx, 100, true)
	if err != nil {
		t.Fatalf("expected valid sign request, got error: %v", err)
	}
	if req.amountAtomic == 0 || req.fromAddress == "" || req.toAddress == "" || req.vaultPubKey.IsEmpty() {
		t.Fatalf("expected fully populated validated sign request")
	}
}

// newFrostBalanceServer creates a mock sidecar server that serves balance requests.
func newFrostBalanceServer(t *testing.T, balanceByPubKey map[string]xmrFrostBalanceResponse) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost && r.URL.Path == "/v1/monero/vaults/register" {
			_ = json.NewEncoder(w).Encode(xmrFrostRegisterVaultResponse{Registered: true})
			return
		}
		// Path format: /v1/monero/balance/{vault_pub_key}
		if strings.HasPrefix(r.URL.Path, "/v1/monero/balance/") {
			pk := strings.TrimPrefix(r.URL.Path, "/v1/monero/balance/")
			resp, ok := balanceByPubKey[pk]
			if !ok {
				http.Error(w, fmt.Sprintf("unknown vault pubkey: %s", pk), http.StatusNotFound)
				return
			}
			_ = json.NewEncoder(w).Encode(resp)
			return
		}
		http.NotFound(w, r)
	}))
}

func uint64Ptr(v uint64) *uint64 {
	return &v
}
