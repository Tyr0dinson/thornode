package monero

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	stypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	tssp "gitlab.com/thorchain/thornode/v3/bifrost/tss/go-tss/tss"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/config"
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

func TestValidateXMRSignTxAcceptsCheckpoint(t *testing.T) {
	t.Parallel()

	vaultPubKey, _ := testVaultPubKeyAndAddress(t)
	c := &Client{chain: common.XMRChain}
	ckpt, _ := json.Marshal(xmrSignCheckpoint{
		SpentOutputRefs: []xmrFrostOutputRef{{TxHash: "aabb", OutputIndex: 0}},
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

func TestFilterAffordableXMRBatchEntriesKeepsCurrentAndTrimsOversizedSiblings(t *testing.T) {
	t.Parallel()

	siblingA := stypes.TxArrayItem{InHash: common.TxID("A")}
	siblingB := stypes.TxArrayItem{InHash: common.TxID("B")}
	entries := []xmrBatchEntry{
		{
			inHashUpper: "A",
			recipient: tssp.FrostSidecarRecipient{
				ToAddress:    "48a",
				AmountAtomic: 2,
			},
			raw: &siblingA,
		},
		{
			inHashUpper: "C",
			recipient: tssp.FrostSidecarRecipient{
				ToAddress:    "48current",
				AmountAtomic: 3,
			},
			isCurrent: true,
		},
		{
			inHashUpper: "B",
			recipient: tssp.FrostSidecarRecipient{
				ToAddress:    "48b",
				AmountAtomic: 4,
			},
			raw: &siblingB,
		},
	}

	filtered := filterAffordableXMRBatchEntries(entries, 5)
	if len(filtered) != 2 {
		t.Fatalf("expected current plus one affordable sibling, got %d entries", len(filtered))
	}
	if filtered[0].recipient.ToAddress != "48a" {
		t.Fatalf("expected earliest affordable sibling to survive trimming, got %s", filtered[0].recipient.ToAddress)
	}
	if !filtered[1].isCurrent {
		t.Fatalf("expected current entry to remain in filtered batch")
	}
}

func TestFilterAffordableXMRBatchEntriesFallsBackToHeadOnly(t *testing.T) {
	t.Parallel()

	entries := []xmrBatchEntry{
		{
			inHashUpper: "A",
			recipient: tssp.FrostSidecarRecipient{
				ToAddress:    "48sibling",
				AmountAtomic: 4,
			},
			raw: &stypes.TxArrayItem{InHash: common.TxID("A")},
		},
		{
			inHashUpper: "B",
			recipient: tssp.FrostSidecarRecipient{
				ToAddress:    "48current",
				AmountAtomic: 3,
			},
			isCurrent: true,
		},
	}

	filtered := filterAffordableXMRBatchEntries(entries, 3)
	if len(filtered) != 1 {
		t.Fatalf("expected head-only fallback, got %d entries", len(filtered))
	}
	if !filtered[0].isCurrent {
		t.Fatalf("expected only current entry to remain after fallback")
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
