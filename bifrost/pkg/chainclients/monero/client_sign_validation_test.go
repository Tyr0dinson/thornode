package monero

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
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
	txHash := strings.Repeat("a", 64)
	noOwnedChange := false

	var capturedPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/monero/outbounds/"+url.PathEscape(vaultPubKey.String()):
			capturedPath = r.URL.Path
			_ = json.NewEncoder(w).Encode(xmrFrostOutgoingTransfersStateResponse{
				VaultPubKey: vaultPubKey.String(),
				Outgoing: []xmrFrostOutgoingTransferStateEntry{{
					TxHash:              txHash,
					HasOwnedChange:      &noOwnedChange,
					FromAddress:         common.Address(common.XMRChecksummedDummyAddress('4')).String(),
					ToAddress:           testOutboundXMRDestination().String(),
					Amount:              55 * moneroAtomicToThorchain,
					Fee:                 3 * moneroAtomicToThorchain,
					Memo:                "OUT:" + strings.Repeat("B", 64),
					PlannedHeight:       100,
					Status:              "observed_mined",
					TxKeys:              []string{strings.Repeat("1", 64), strings.Repeat("2", 64)},
					RegisteredInventory: true,
				}},
			})
			return
		case r.URL.Path == "/v1/monero/outbounds/validate":
			t.Fatal("validate_outbound fallback should not be called")
		default:
			http.NotFound(w, r)
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
	}

	obsTx := common.NewObservedTx(
		common.Tx{
			ID:          common.TxID(txHash),
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

	if capturedPath != "/v1/monero/outbounds/"+url.PathEscape(vaultPubKey.String()) {
		t.Fatalf("expected signer outgoing state lookup path for %s, got %s", vaultPubKey, capturedPath)
	}
}

func TestValidateOutboundObservationCanonicalizesVaultPubKeyForSidecarTxKeyAudit(t *testing.T) {
	t.Parallel()

	observedVaultPubKey := common.PubKey("cthorpub1observedvault")
	canonicalVaultPubKey := common.PubKey("cthorpub1canonicalvault")
	txHash := strings.Repeat("c", 64)
	noOwnedChange := false

	var capturedPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/monero/outbounds/"+url.PathEscape(canonicalVaultPubKey.String()):
			capturedPath = r.URL.Path
			_ = json.NewEncoder(w).Encode(xmrFrostOutgoingTransfersStateResponse{
				VaultPubKey: canonicalVaultPubKey.String(),
				Outgoing: []xmrFrostOutgoingTransferStateEntry{{
					TxHash:              txHash,
					HasOwnedChange:      &noOwnedChange,
					FromAddress:         common.Address(common.XMRChecksummedDummyAddress('4')).String(),
					ToAddress:           testOutboundXMRDestination().String(),
					Amount:              55 * moneroAtomicToThorchain,
					Fee:                 3 * moneroAtomicToThorchain,
					Memo:                "OUT:" + strings.Repeat("D", 64),
					PlannedHeight:       100,
					Status:              "observed_mined",
					TxKeys:              []string{strings.Repeat("1", 64)},
					RegisteredInventory: true,
				}},
			})
		case r.URL.Path == "/v1/monero/outbounds/validate":
			t.Fatal("validate_outbound fallback should not be called")
		default:
			http.NotFound(w, r)
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
		thorchainBridge: &keyImageTestBridge{
			mockMoneroBridge: mockMoneroBridge{
				vaultByPubKey: map[string]ttypes.Vault{
					observedVaultPubKey.String(): {
						PubKey:      observedVaultPubKey,
						PubKeyEddsa: observedVaultPubKey,
						PubKeyFrost: canonicalVaultPubKey,
						Membership:  []string{"member-a", "member-b", "member-c"},
					},
				},
			},
		},
		logger: testLogger(),
	}

	obsTx := common.NewObservedTx(
		common.Tx{
			ID:          common.TxID(txHash),
			Chain:       common.XMRChain,
			FromAddress: common.Address(common.XMRChecksummedDummyAddress('4')),
			ToAddress:   testOutboundXMRDestination(),
			Coins:       common.NewCoins(common.NewCoin(common.XMRAsset, cosmos.NewUint(55))),
			Gas:         common.Gas{common.NewCoin(common.XMRAsset, cosmos.NewUint(3))},
			Memo:        "OUT:" + strings.Repeat("D", 64),
		},
		100,
		observedVaultPubKey,
		0,
	)
	obsTx.TxKeys = []string{strings.Repeat("1", 64)}

	if err := c.ValidateOutboundObservation(obsTx); err != nil {
		t.Fatalf("validate outbound observation: %v", err)
	}
	if capturedPath != "/v1/monero/outbounds/"+url.PathEscape(canonicalVaultPubKey.String()) {
		t.Fatalf("expected canonical vault lookup path %s, got %s", canonicalVaultPubKey, capturedPath)
	}
}

func TestValidateOutboundObservationRejectsMissingTxKeys(t *testing.T) {
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

	err = c.ValidateOutboundObservation(obsTx)
	if err == nil {
		t.Fatalf("expected missing tx keys error")
	}
	if !strings.Contains(err.Error(), "missing tx keys") {
		t.Fatalf("unexpected error: %v", err)
	}
	if outboundValidateCalls != 0 {
		t.Fatalf("unexpected outbound validate calls: %d", outboundValidateCalls)
	}
}

func TestValidateOutboundObservationAllowsUnauthorizedSpendWithoutTxKeys(t *testing.T) {
	t.Parallel()

	vaultPubKey, _ := testVaultPubKeyAndAddress(t)
	obsTx := common.NewObservedTx(
		common.Tx{
			ID:          common.TxID(strings.Repeat("d", 64)),
			Chain:       common.XMRChain,
			FromAddress: common.Address(common.XMRChecksummedDummyAddress('4')),
			ToAddress:   common.Address(common.XMRUnknownSenderAddress("spend", "vault")),
			Coins:       common.NewCoins(common.NewCoin(common.XMRAsset, cosmos.NewUint(55))),
			Gas:         common.Gas{common.NewCoin(common.XMRAsset, cosmos.NewUint(3))},
			Memo:        "NOOP:UNAUTHORIZED",
		},
		100,
		vaultPubKey,
		0,
	)

	c := &Client{chain: common.XMRChain}
	if err := c.ValidateOutboundObservation(obsTx); err != nil {
		t.Fatalf("unauthorized spend should not require sidecar tx-key validation: %v", err)
	}
}

func TestPrepareObservedTxInItemAllowsUnauthorizedSpendWithoutTxKeys(t *testing.T) {
	t.Parallel()

	vaultPubKey, vaultAddr := testVaultPubKeyAndAddress(t)
	var called bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		http.NotFound(w, r)
	}))
	defer server.Close()

	frostSigner, err := NewFrostSignerWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
	if err != nil {
		t.Fatalf("new frost signer: %v", err)
	}

	c := &Client{
		chain:       common.XMRChain,
		frostSigner: frostSigner,
		tssServer:   &mockKeyImageTssServer{},
		logger:      testLogger(),
	}
	item := &stypes.TxInItem{
		BlockHeight:         100,
		Tx:                  strings.Repeat("e", 64),
		Sender:              vaultAddr.String(),
		To:                  common.XMRUnknownSenderAddress("spend", vaultAddr.String()),
		Coins:               common.NewCoins(common.NewCoin(common.XMRAsset, cosmos.NewUint(55))),
		Gas:                 common.Gas{common.NewCoin(common.XMRAsset, cosmos.NewUint(3))},
		ObservedVaultPubKey: vaultPubKey,
		Memo:                "NOOP:UNAUTHORIZED",
	}

	prepared, include, err := c.PrepareObservedTxInItem(item, true, 110)
	if err != nil {
		t.Fatalf("prepare unauthorized spend: %v", err)
	}
	if !include || prepared != item {
		t.Fatalf("expected unauthorized spend to be included unchanged")
	}
	if called {
		t.Fatalf("unauthorized spend should not call sidecar validation")
	}
}

func TestPrepareObservedTxInItemSkipsAlreadyObservedUnauthorizedSpend(t *testing.T) {
	t.Parallel()

	vaultPubKey, vaultAddr := testVaultPubKeyAndAddress(t)
	txHash := strings.Repeat("f", 64)
	bridge := &mockMoneroBridge{
		observedTxVoterByID: map[string]*ttypes.QueryObservedTxVoter{
			txHash: {
				TxID:            common.TxID(txHash),
				Height:          123,
				FinalisedHeight: 123,
			},
		},
	}

	c := &Client{
		chain:           common.XMRChain,
		frostSigner:     &FrostSigner{},
		tssServer:       &mockKeyImageTssServer{},
		thorchainBridge: bridge,
		logger:          testLogger(),
	}
	item := &stypes.TxInItem{
		BlockHeight:         100,
		Tx:                  txHash,
		Sender:              vaultAddr.String(),
		To:                  common.XMRUnknownSenderAddress("spend", vaultAddr.String()),
		Coins:               common.NewCoins(common.NewCoin(common.XMRAsset, cosmos.NewUint(55))),
		Gas:                 common.Gas{common.NewCoin(common.XMRAsset, cosmos.NewUint(3))},
		ObservedVaultPubKey: vaultPubKey,
		Memo:                "NOOP:UNAUTHORIZED",
	}

	prepared, include, err := c.PrepareObservedTxInItem(item, true, 110)
	if err != nil {
		t.Fatalf("prepare unauthorized spend: %v", err)
	}
	if include || prepared != nil {
		t.Fatalf("expected already observed unauthorized spend to be skipped")
	}
}

func TestPrepareObservedTxInItemSkipsSignerOwnedConsolidateReportedUnauthorized(t *testing.T) {
	t.Parallel()

	vaultPubKey, vaultAddr := testVaultPubKeyAndAddress(t)
	txHash := strings.Repeat("9", 64)
	hasOwnedChange := true
	localChangeMaterialized := true
	keyImagesReady := true

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/monero/outbounds/"+url.PathEscape(vaultPubKey.String()):
			_ = json.NewEncoder(w).Encode(xmrFrostOutgoingTransfersStateResponse{
				VaultPubKey: vaultPubKey.String(),
				Outgoing: []xmrFrostOutgoingTransferStateEntry{{
					TxHash:                  txHash,
					TxKeys:                  testFrostTxKeys(),
					RegisteredInventory:     true,
					HasOwnedChange:          &hasOwnedChange,
					LocalChangeMaterialized: &localChangeMaterialized,
					KeyImagesReady:          &keyImagesReady,
					FromAddress:             vaultAddr.String(),
					ToAddress:               vaultAddr.String(),
					Amount:                  55 * moneroAtomicToThorchain,
					Fee:                     3 * moneroAtomicToThorchain,
					Memo:                    "consolidate",
					PlannedHeight:           100,
					Status:                  xmrFrostOutgoingStatusObservedMined,
				}},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	frostSigner, err := NewFrostSignerWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
	if err != nil {
		t.Fatalf("new frost signer: %v", err)
	}

	c := &Client{
		chain:           common.XMRChain,
		frostSigner:     frostSigner,
		tssServer:       &mockKeyImageTssServer{},
		thorchainBridge: &mockMoneroBridge{observedTxVoterByID: map[string]*ttypes.QueryObservedTxVoter{}},
		logger:          testLogger(),
	}
	item := &stypes.TxInItem{
		BlockHeight:         100,
		Tx:                  txHash,
		Sender:              vaultAddr.String(),
		To:                  common.XMRUnknownSenderAddress(txHash, vaultAddr.String()),
		Coins:               common.NewCoins(common.NewCoin(common.XMRAsset, cosmos.NewUint(55))),
		Gas:                 common.Gas{common.NewCoin(common.XMRAsset, cosmos.NewUint(3))},
		ObservedVaultPubKey: vaultPubKey,
		Memo:                "NOOP:UNAUTHORIZED",
	}

	prepared, include, err := c.PrepareObservedTxInItem(item, true, 110)
	if err != nil {
		t.Fatalf("prepare signer-owned unauthorized-shaped consolidate: %v", err)
	}
	if include || prepared != nil {
		t.Fatalf("expected signer-owned consolidate reported as unauthorized to be skipped")
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
		if strings.Contains(r.URL.Path, "/v1/monero/outputs/") {
			_ = json.NewEncoder(w).Encode(xmrFrostOutputsResponse{
				Outputs: []xmrFrostOutputEntry{
					{
						TxHash:      strings.Repeat("a", 64),
						OutputIndex: 0,
						Amount:      10 * moneroAtomicToThorchain,
						Height:      96,
						State:       "unspent",
					},
				},
			})
			return
		}
		if r.URL.Path == "/v1/monero/chain/height" {
			_ = json.NewEncoder(w).Encode(xmrFrostChainHeightResponse{Height: 100})
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
	if !strings.Contains(err.Error(), "confirmations_remaining=6") {
		t.Fatalf("expected remaining confirmation detail, got: %v", err)
	}
	var retryErr interface {
		error
		RetryAfterNextBlock() bool
		RetryAfterBlocks() uint64
	}
	if !errors.As(err, &retryErr) {
		t.Fatalf("expected locked balance error to request retry after next block: %v", err)
	}
	if !retryErr.RetryAfterNextBlock() {
		t.Fatalf("expected locked balance error to request retry after next block")
	}
	if retryErr.RetryAfterBlocks() != 6 {
		t.Fatalf("expected locked balance error to retry after 6 blocks, got %d", retryErr.RetryAfterBlocks())
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

func TestShouldSweepXMRFinalIncludesInactiveVaultReroute(t *testing.T) {
	t.Parallel()

	vaultPubKey, vaultAddr := testVaultPubKeyAndAddress(t)
	c := &Client{
		chain: common.XMRChain,
		vaultAddressMap: map[string]common.Address{
			vaultPubKey.String(): vaultAddr,
		},
	}
	tx := stypes.TxOutItem{
		Chain:     common.XMRChain,
		ToAddress: vaultAddr,
		Memo:      "",
	}

	if !c.shouldSweepXMRFinal(tx, 1, ttypes.VaultStatus_InactiveVault) {
		t.Fatalf("expected inactive vault reroute to known vault to sweep final amount")
	}
	if c.shouldSweepXMRFinal(tx, 0, ttypes.VaultStatus_InactiveVault) {
		t.Fatalf("expected zero gas to disable final sweep")
	}
	if c.shouldSweepXMRFinal(tx, 1, ttypes.VaultStatus_ActiveVault) {
		t.Fatalf("expected active vault reroute to use normal change handling")
	}
}

func TestValidateXMRSignTxAllowsShortFinalMigrateBalanceForExactSignerAttempt(t *testing.T) {
	t.Parallel()

	vaultPubKey, vaultAddr := testVaultPubKeyAndAddress(t)
	amountAtomic := uint64(2 * moneroAtomicToThorchain)
	maxGasAtomic := uint64(moneroAtomicToThorchain)
	spendableAtomic := amountAtomic + (maxGasAtomic / 2)

	balanceServer := newFrostBalanceServer(t, map[string]xmrFrostBalanceResponse{
		vaultPubKey.String(): {
			Balance:          spendableAtomic,
			UnlockedBalance:  spendableAtomic,
			SpendableBalance: uint64Ptr(spendableAtomic),
		},
	})
	defer balanceServer.Close()

	frostSigner, err := NewFrostSignerWithEndpoint(config.BifrostChainConfiguration{}, balanceServer.URL)
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
		Memo: "MIGRATE:123",
	}

	_, err = c.validateXMRSignTx(tx, 100, true)
	if err != nil {
		t.Fatalf("expected short final migration balance to reach exact signer attempt: %v", err)
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
