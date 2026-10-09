package monero

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/cometbft/cometbft/crypto/secp256k1"
	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	cryptocodec "github.com/cosmos/cosmos-sdk/crypto/codec"
	cKeys "github.com/cosmos/cosmos-sdk/crypto/keyring"
	"github.com/rs/zerolog/log"
	"github.com/stretchr/testify/require"
	"github.com/syndtr/goleveldb/leveldb"
	"github.com/syndtr/goleveldb/leveldb/storage"

	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/monero/sidecar"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/crypto/ed25519"
	"gitlab.com/thorchain/thornode/v3/config"
)

func newCovThorKeys(t *testing.T, withEDDSA bool) *thorclient.Keys {
	t.Helper()
	registry := codectypes.NewInterfaceRegistry()
	cdc := codec.NewProtoCodec(registry)
	cryptocodec.RegisterInterfaces(cdc.InterfaceRegistry())
	kb := cKeys.NewInMemory(cdc, func(options *cKeys.Options) {
		options.SupportedAlgos = append(options.SupportedAlgos, ed25519.Ed25519)
	})
	if withEDDSA {
		_, _, err := kb.NewMnemonic(ed25519.SignerNameEDDSA("cov-signer"), cKeys.English, "", "password", ed25519.Ed25519)
		require.NoError(t, err)
	}
	return thorclient.NewKeysWithKeybase(kb, "cov-signer", "password")
}

func TestDeriveVaultBootstrapCov(t *testing.T) {
	t.Parallel()

	got, err := deriveVaultBootstrap(nil)
	require.NoError(t, err)
	require.Nil(t, got)

	_, err = deriveVaultBootstrap(newCovThorKeys(t, false))
	require.ErrorContains(t, err, "fail to get local ed25519 private key")

	keys := newCovThorKeys(t, true)
	boot, err := deriveVaultBootstrap(keys)
	require.NoError(t, err)
	require.NotNil(t, boot)
	require.False(t, boot.vaultPubKey.IsEmpty())
	require.Len(t, boot.spendKeyHex, 64)

	priv, err := keys.GetPrivateKeyEDDSA()
	require.NoError(t, err)
	require.Equal(t, hex.EncodeToString(priv.Bytes()[:32]), boot.spendKeyHex)

	// spendKeyHexFor only releases the key for the matching vault.
	spend, ok := boot.spendKeyHexFor(boot.vaultPubKey)
	require.True(t, ok)
	require.Equal(t, boot.spendKeyHex, spend)

	_, ok = boot.spendKeyHexFor(testVaultPubKey(t, "other-vault"))
	require.False(t, ok)
	_, ok = boot.spendKeyHexFor(common.EmptyPubKey)
	require.False(t, ok)

	var nilBoot *vaultBootstrap
	_, ok = nilBoot.spendKeyHexFor(boot.vaultPubKey)
	require.False(t, ok)

	blank := &vaultBootstrap{vaultPubKey: boot.vaultPubKey, spendKeyHex: "   "}
	_, ok = blank.spendKeyHexFor(boot.vaultPubKey)
	require.False(t, ok)
}

func TestGroupKeyHexConversionErrorsCov(t *testing.T) {
	t.Parallel()

	_, err := groupKeyHexFromVaultPubKey(common.EmptyPubKey)
	require.ErrorContains(t, err, "vault pubkey is empty")

	_, err = groupKeyHexFromVaultPubKey(common.PubKey("not-a-bech32-pubkey"))
	require.ErrorContains(t, err, "fail to decode vault pubkey")

	// A secp256k1 pubkey decodes but has 33 bytes.
	secp, err := common.NewPubKeyFromCrypto(secp256k1.GenPrivKey().PubKey())
	require.NoError(t, err)
	_, err = groupKeyHexFromVaultPubKey(secp)
	require.ErrorContains(t, err, "unexpected Ed25519 vault pubkey length")

	_, err = vaultPubKeyFromGroupKeyHex("   ")
	require.ErrorContains(t, err, "group key is empty")
	_, err = vaultPubKeyFromGroupKeyHex("zz")
	require.ErrorContains(t, err, "fail to decode group key")
	_, err = vaultPubKeyFromGroupKeyHex("abcd")
	require.ErrorContains(t, err, "unexpected group key length 2")

	// Round trip.
	vault := testVaultPubKey(t, "group-key-roundtrip")
	gk, err := groupKeyHexFromVaultPubKey(vault)
	require.NoError(t, err)
	back, err := vaultPubKeyFromGroupKeyHex(" " + gk + " ")
	require.NoError(t, err)
	require.True(t, back.Equals(vault))
}

type staticSpendKeyProviderCov struct {
	key string
	ok  bool
}

func (p staticSpendKeyProviderCov) spendKeyHexFor(common.PubKey) (string, bool) {
	return p.key, p.ok
}

func TestEnsureVaultRegisteredCov(t *testing.T) {
	t.Parallel()

	vault, address := testVaultPubKeyAndAddress(t)

	_, err := ensureVaultRegistered(log.Logger, nil, common.XMRChain, vault, nil, "test", 1)
	require.ErrorContains(t, err, "sidecar client is not initialized")

	var (
		mu   sync.Mutex
		reqs []sidecar.RegisterVaultReq
		fail bool
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/monero/vaults/register" {
			http.NotFound(w, r)
			return
		}
		var req sidecar.RegisterVaultReq
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		mu.Lock()
		reqs = append(reqs, req)
		shouldFail := fail
		mu.Unlock()
		if shouldFail {
			http.Error(w, "register rejected", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(testRegisterVaultResponse(5))
	}))
	t.Cleanup(server.Close)
	client, err := sidecar.NewClientWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
	require.NoError(t, err)

	_, err = ensureVaultRegistered(log.Logger, client, common.XMRChain, common.EmptyPubKey, nil, "test", 1)
	require.ErrorContains(t, err, "vault pubkey is empty")

	_, err = ensureVaultRegisteredContext(context.Background(), log.Logger, client, common.XMRChain,
		common.PubKey("bogus"), nil, "test", 1)
	require.Error(t, err)

	// Spend-key path.
	got, err := ensureVaultRegistered(log.Logger, client, common.XMRChain, vault,
		staticSpendKeyProviderCov{key: "  " + strings.Repeat("ab", 32) + " ", ok: true}, "keygen", 7)
	require.NoError(t, err)
	require.True(t, got.Equals(address))

	// View-key fallback when the provider has no key for the vault.
	got, err = ensureVaultRegistered(log.Logger, client, common.XMRChain, vault,
		staticSpendKeyProviderCov{ok: false}, "observe", 9)
	require.NoError(t, err)
	require.True(t, got.Equals(address))

	mu.Lock()
	require.Len(t, reqs, 2)
	require.Equal(t, strings.Repeat("ab", 32), reqs[0].SpendKeyHex)
	require.Empty(t, reqs[0].ViewKeyHex)
	require.Equal(t, "keygen", reqs[0].Source)
	require.Equal(t, int64(7), reqs[0].VaultCreationHeight)
	require.Equal(t, address.String(), reqs[0].Address)
	require.Empty(t, reqs[1].SpendKeyHex)
	require.Equal(t, int64(9), reqs[1].VaultCreationHeight)
	fail = true
	mu.Unlock()

	_, err = ensureVaultRegistered(log.Logger, client, common.XMRChain, vault, nil, "observe", 9)
	require.Error(t, err)
}

func TestVaultAddressStoreErrorsCov(t *testing.T) {
	t.Parallel()

	var nilStore *VaultAddressStore
	require.ErrorContains(t, nilStore.Set("a", "b"), "not initialized")
	_, err := nilStore.Get("a")
	require.ErrorContains(t, err, "not initialized")
	_, err = nilStore.List()
	require.ErrorContains(t, err, "not initialized")

	db, err := leveldb.Open(storage.NewMemStorage(), nil)
	require.NoError(t, err)
	store := NewVaultAddressStore(db)

	require.ErrorContains(t, store.Set("", "addr"), "cannot be empty")
	require.ErrorContains(t, store.Set("pk", ""), "cannot be empty")
	_, err = store.Get("")
	require.ErrorContains(t, err, "cannot be empty")
	_, err = store.Get("missing")
	require.ErrorIs(t, err, leveldb.ErrNotFound)

	// A bare prefix key (no pubkey suffix) is ignored by List.
	require.NoError(t, db.Put([]byte(vaultAddressPrefix), []byte("ignored"), nil))
	require.NoError(t, store.Set("pk1", "addr1"))
	all, err := store.List()
	require.NoError(t, err)
	require.Equal(t, map[string]string{"pk1": "addr1"}, all)

	require.NoError(t, db.Close())
	_, err = store.List()
	require.Error(t, err)
}
