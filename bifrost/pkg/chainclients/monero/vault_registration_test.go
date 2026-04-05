package monero

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	cometed25519 "github.com/cometbft/cometbft/crypto/ed25519"
	"github.com/rs/zerolog/log"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/config"
)

func TestGroupKeyHexFromVaultPubKey(t *testing.T) {
	t.Parallel()

	raw := sha256.Sum256([]byte("vault-registration-group-key"))
	vaultPubKey, err := common.NewPubKeyFromCrypto(cometed25519.PubKey(raw[:]))
	if err != nil {
		t.Fatalf("new pub key: %v", err)
	}

	groupKeyHex, err := groupKeyHexFromVaultPubKey(vaultPubKey)
	if err != nil {
		t.Fatalf("groupKeyHexFromVaultPubKey failed: %v", err)
	}

	expected := hex.EncodeToString(raw[:])
	if groupKeyHex != expected {
		t.Fatalf("expected group key %s, got %s", expected, groupKeyHex)
	}
}

func TestVaultPubKeyFromGroupKeyHex(t *testing.T) {
	t.Parallel()

	raw := sha256.Sum256([]byte("vault-registration-pubkey-from-group-key"))
	expectedPubKey, err := common.NewPubKeyFromCrypto(cometed25519.PubKey(raw[:]))
	if err != nil {
		t.Fatalf("new pub key: %v", err)
	}

	pubKey, err := vaultPubKeyFromGroupKeyHex(hex.EncodeToString(raw[:]))
	if err != nil {
		t.Fatalf("vaultPubKeyFromGroupKeyHex failed: %v", err)
	}
	if !pubKey.Equals(expectedPubKey) {
		t.Fatalf("expected pubkey %s, got %s", expectedPubKey, pubKey)
	}
}

func TestClientEnsureXMRVaultBalanceRegistersVault(t *testing.T) {
	t.Parallel()

	vaultPubKey := testVaultPubKey(t, "ensure-xmr-vault-balance")
	address, deriveErr := vaultPubKey.GetAddress(common.XMRChain)
	if deriveErr != nil {
		t.Fatalf("derive address: %v", deriveErr)
	}
	expectedGroupKey, err := groupKeyHexFromVaultPubKey(vaultPubKey)
	if err != nil {
		t.Fatalf("group key: %v", err)
	}

	var mu sync.Mutex
	registered := false

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/monero/vaults/register":
			var req xmrFrostRegisterVaultRequest
			if decodeErr := json.NewDecoder(r.Body).Decode(&req); decodeErr != nil {
				http.Error(w, decodeErr.Error(), http.StatusBadRequest)
				return
			}
			if req.VaultPubKey != vaultPubKey.String() {
				http.Error(w, "unexpected vault pubkey", http.StatusBadRequest)
				return
			}
			if req.GroupKey != expectedGroupKey {
				http.Error(w, "unexpected group key", http.StatusBadRequest)
				return
			}
			if req.Address != address.String() {
				http.Error(w, "unexpected address", http.StatusBadRequest)
				return
			}
			if req.Source != "bifrost" {
				http.Error(w, "unexpected source", http.StatusBadRequest)
				return
			}
			mu.Lock()
			registered = true
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(xmrFrostRegisterVaultResponse{Registered: true})
		case "/v1/monero/balance/" + vaultPubKey.String():
			mu.Lock()
			isRegistered := registered
			mu.Unlock()
			if !isRegistered {
				http.Error(w, "vault not registered", http.StatusPreconditionFailed)
				return
			}
			_ = json.NewEncoder(w).Encode(xmrFrostBalanceResponse{
				Balance:         5000,
				UnlockedBalance: 5000,
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	signer, err := NewFrostSignerWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
	if err != nil {
		t.Fatalf("new frost signer: %v", err)
	}

	client := &Client{
		logger:          log.Logger,
		chain:           common.XMRChain,
		frostSigner:     signer,
		vaultAddressMap: make(map[string]common.Address),
	}

	if err := client.ensureXMRVaultBalance(vaultPubKey, 1000, nil); err != nil {
		t.Fatalf("ensureXMRVaultBalance failed: %v", err)
	}

	mapped, ok := client.getMappedVaultAddress(vaultPubKey)
	if !ok {
		t.Fatal("expected vault address mapping to be cached")
	}
	if !mapped.Equals(address) {
		t.Fatalf("expected mapped address %s, got %s", address, mapped)
	}
}

func TestEnsureFrostVaultRegisteredIncludesBootstrapSpendKeyForMatchingVault(t *testing.T) {
	t.Parallel()

	vaultPubKey := testVaultPubKey(t, "matching-bootstrap-spend-key")
	address, deriveErr := vaultPubKey.GetAddress(common.XMRChain)
	if deriveErr != nil {
		t.Fatalf("derive address: %v", deriveErr)
	}
	expectedGroupKey, err := groupKeyHexFromVaultPubKey(vaultPubKey)
	if err != nil {
		t.Fatalf("group key: %v", err)
	}
	expectedSpendKeyHex := strings.Repeat("ab", 32)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/monero/vaults/register" {
			http.NotFound(w, r)
			return
		}

		var req xmrFrostRegisterVaultRequest
		if decodeErr := json.NewDecoder(r.Body).Decode(&req); decodeErr != nil {
			http.Error(w, decodeErr.Error(), http.StatusBadRequest)
			return
		}
		if req.VaultPubKey != vaultPubKey.String() {
			http.Error(w, "unexpected vault pubkey", http.StatusBadRequest)
			return
		}
		if req.GroupKey != expectedGroupKey {
			http.Error(w, "unexpected group key", http.StatusBadRequest)
			return
		}
		if req.Address != address.String() {
			http.Error(w, "unexpected address", http.StatusBadRequest)
			return
		}
		if req.SpendKeyHex != expectedSpendKeyHex {
			http.Error(w, "unexpected spend key", http.StatusBadRequest)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(xmrFrostRegisterVaultResponse{Registered: true})
	}))
	defer server.Close()

	signer, err := NewFrostSignerWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
	if err != nil {
		t.Fatalf("new frost signer: %v", err)
	}

	bootstrap := &frostVaultBootstrap{
		vaultPubKey: vaultPubKey,
		spendKeyHex: expectedSpendKeyHex,
	}
	registeredAddress, err := ensureFrostVaultRegistered(log.Logger, signer, common.XMRChain, vaultPubKey, bootstrap, "test", 0)
	if err != nil {
		t.Fatalf("ensureFrostVaultRegistered failed: %v", err)
	}
	if !registeredAddress.Equals(address) {
		t.Fatalf("expected address %s, got %s", address, registeredAddress)
	}
}

func TestEnsureFrostVaultRegisteredOmitsBootstrapSpendKeyForOtherVault(t *testing.T) {
	t.Parallel()

	vaultPubKey := testVaultPubKey(t, "non-matching-bootstrap-spend-key")
	address, deriveErr := vaultPubKey.GetAddress(common.XMRChain)
	if deriveErr != nil {
		t.Fatalf("derive address: %v", deriveErr)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/monero/vaults/register" {
			http.NotFound(w, r)
			return
		}

		var req xmrFrostRegisterVaultRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if req.SpendKeyHex != "" {
			http.Error(w, "expected empty spend key", http.StatusBadRequest)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(xmrFrostRegisterVaultResponse{Registered: true})
	}))
	defer server.Close()

	signer, err := NewFrostSignerWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
	if err != nil {
		t.Fatalf("new frost signer: %v", err)
	}

	bootstrap := &frostVaultBootstrap{
		vaultPubKey: testVaultPubKey(t, "different-bootstrap-vault"),
		spendKeyHex: strings.Repeat("cd", 32),
	}
	registeredAddress, err := ensureFrostVaultRegistered(log.Logger, signer, common.XMRChain, vaultPubKey, bootstrap, "test", 0)
	if err != nil {
		t.Fatalf("ensureFrostVaultRegistered failed: %v", err)
	}
	if !registeredAddress.Equals(address) {
		t.Fatalf("expected address %s, got %s", address, registeredAddress)
	}
}

func TestGetAccountByAddressRegistersVaultBeforeBalanceQuery(t *testing.T) {
	t.Parallel()

	vaultPubKey := testVaultPubKey(t, "account-query-registers-vault")
	address, err := vaultPubKey.GetAddress(common.XMRChain)
	if err != nil {
		t.Fatalf("derive address: %v", err)
	}

	var mu sync.Mutex
	registered := false

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/v1/monero/vaults/register":
			mu.Lock()
			registered = true
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(xmrFrostRegisterVaultResponse{Registered: true})
		case strings.HasPrefix(r.URL.Path, "/v1/monero/balance/"):
			mu.Lock()
			isRegistered := registered
			mu.Unlock()
			if !isRegistered {
				http.Error(w, "vault not registered", http.StatusPreconditionFailed)
				return
			}
			_ = json.NewEncoder(w).Encode(xmrFrostBalanceResponse{
				Balance:         7 * moneroAtomicToThorchain,
				UnlockedBalance: 7 * moneroAtomicToThorchain,
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	signer, err := NewFrostSignerWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
	if err != nil {
		t.Fatalf("new frost signer: %v", err)
	}

	client := &Client{
		logger:          log.Logger,
		chain:           common.XMRChain,
		frostSigner:     signer,
		vaultAddressMap: make(map[string]common.Address),
	}
	client.setVaultAddressMapping(vaultPubKey, address)

	account, err := client.GetAccountByAddress(address.String(), nil)
	if err != nil {
		t.Fatalf("GetAccountByAddress failed: %v", err)
	}

	coin := account.Coins.GetCoin(common.XMRAsset)
	if coin.Amount.Uint64() != 7 {
		t.Fatalf("expected 7 XMR units, got %s", coin.Amount.String())
	}
}

func TestGetAccountByAddressUsesSpendableBalanceWhenProvided(t *testing.T) {
	t.Parallel()

	vaultPubKey := testVaultPubKey(t, "account-query-uses-spendable-balance")
	address, err := vaultPubKey.GetAddress(common.XMRChain)
	if err != nil {
		t.Fatalf("derive address: %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/v1/monero/vaults/register":
			_ = json.NewEncoder(w).Encode(xmrFrostRegisterVaultResponse{Registered: true})
		case strings.HasPrefix(r.URL.Path, "/v1/monero/balance/"):
			_ = json.NewEncoder(w).Encode(xmrFrostBalanceResponse{
				Balance:          9 * moneroAtomicToThorchain,
				UnlockedBalance:  9 * moneroAtomicToThorchain,
				SpendableBalance: uint64Ptr(3 * moneroAtomicToThorchain),
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	signer, err := NewFrostSignerWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
	if err != nil {
		t.Fatalf("new frost signer: %v", err)
	}

	client := &Client{
		logger:          log.Logger,
		chain:           common.XMRChain,
		frostSigner:     signer,
		vaultAddressMap: make(map[string]common.Address),
	}
	client.setVaultAddressMapping(vaultPubKey, address)

	account, err := client.GetAccountByAddress(address.String(), nil)
	if err != nil {
		t.Fatalf("GetAccountByAddress failed: %v", err)
	}

	coin := account.Coins.GetCoin(common.XMRAsset)
	if coin.Amount.Uint64() != 3 {
		t.Fatalf("expected spendable 3 XMR units, got %s", coin.Amount.String())
	}
}
