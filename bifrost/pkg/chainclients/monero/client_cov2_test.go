package monero

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/blang/semver"
	"github.com/rs/zerolog/log"
	"github.com/stretchr/testify/require"

	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/monero/sidecar"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	stypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/config"
)

func validSignTxClientCov(t *testing.T) (common.PubKey, stypes.TxOutItem) {
	t.Helper()
	vault, _ := testVaultPubKeyAndAddress(t)
	return vault, stypes.TxOutItem{
		Chain:            common.XMRChain,
		VaultPubKey:      vault,
		VaultPubKeyFrost: vault,
		ToAddress:        testOutboundXMRDestination(),
		Coins:            common.Coins{common.NewCoin(common.XMRAsset, cosmos.NewUint(10))},
		Memo:             "OUT:0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF",
	}
}

func TestValidateSignTxErrorsClientCov(t *testing.T) {
	t.Parallel()

	c := &Client{chain: common.XMRChain, logger: log.Logger}
	_, base := validSignTxClientCov(t)

	got, err := c.validateSignTx(base, 10, true)
	require.NoError(t, err)
	require.Equal(t, uint64(10*moneroAtomicToThorchain), got.amountAtomic)
	require.Equal(t, base.ToAddress.String(), got.toAddress)

	cases := map[string]struct {
		mutate func(tx *stypes.TxOutItem)
		height int64
		want   string
	}{
		"empty-to":     {func(tx *stypes.TxOutItem) { tx.ToAddress = "" }, 10, "to address is empty"},
		"invalid-to":   {func(tx *stypes.TxOutItem) { tx.ToAddress = "bc1qnotxmr" }, 10, "is not a valid XMR address"},
		"height":       {func(tx *stypes.TxOutItem) {}, 0, "height must be greater than zero"},
		"no-memo":      {func(tx *stypes.TxOutItem) { tx.Memo = "" }, 10, "requires its original memo"},
		"bad-memo":     {func(tx *stypes.TxOutItem) { tx.Memo = "NOTAMEMO:xyz" }, 10, "fail to parse outbound memo"},
		"inbound-memo": {func(tx *stypes.TxOutItem) { tx.Memo = "ADD:BTC.BTC" }, 10, "inbound memo should not be used"},
		"two-coins": {func(tx *stypes.TxOutItem) {
			tx.Coins = append(tx.Coins, common.NewCoin(common.BTCAsset, cosmos.NewUint(1)))
		}, 10, "exactly one coin"},
		"not-xmr": {func(tx *stypes.TxOutItem) {
			tx.Coins = common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(1))}
		}, 10, "does not contain XMR coin"},
		"zero": {func(tx *stypes.TxOutItem) {
			tx.Coins = common.Coins{common.NewCoin(common.XMRAsset, cosmos.ZeroUint())}
		}, 10, "does not contain XMR coin"},
		"overflow": {func(tx *stypes.TxOutItem) {
			tx.Coins = common.Coins{common.NewCoin(common.XMRAsset, cosmos.NewUintFromString("100000000000000000000"))}
		}, 10, "overflows uint64"},
		"max-gas-overflow": {func(tx *stypes.TxOutItem) {
			tx.MaxGas = common.Gas{common.NewCoin(common.XMRAsset, cosmos.NewUintFromString("100000000000000000000"))}
		}, 10, "fail to convert xmr max gas"},
		"no-vault": {func(tx *stypes.TxOutItem) { tx.VaultPubKeyFrost = common.EmptyPubKey }, 10, "vault pubkey is empty"},
		"bad-vault": {func(tx *stypes.TxOutItem) {
			tx.VaultPubKeyFrost = common.PubKey("bogus")
		}, 10, "fail to derive monero vault address"},
	}
	for name, tc := range cases {
		tx := base
		tx.Coins = append(common.Coins(nil), base.Coins...)
		tc.mutate(&tx)
		_, err = c.validateSignTx(tx, tc.height, true)
		require.ErrorContains(t, err, tc.want, name)
	}

	// Height is optional when not required.
	_, err = c.validateSignTx(base, 0, false)
	require.NoError(t, err)
}

func TestDecodeStoredSignedXMRTxPayloadClientCov(t *testing.T) {
	t.Parallel()

	good := signedTxPayload{
		TxHash: strings.ToUpper(testXMRHash("tx")), TxMetadata: "ABCD", MsgID: "m",
		TxKey: testXMRHash("key"),
	}
	encode := func(p signedTxPayload) []byte {
		raw, err := json.Marshal(p)
		require.NoError(t, err)
		return raw
	}
	got, err := decodeStoredSignedXMRTxPayload(encode(good))
	require.NoError(t, err)
	require.Equal(t, testXMRHash("tx"), got.TxHash)
	require.Equal(t, "abcd", got.TxMetadata)

	_, err = decodeStoredSignedXMRTxPayload([]byte("{"))
	require.ErrorContains(t, err, "fail to decode cached xmr signed tx payload")
	cases := map[string]struct {
		mutate func(p *signedTxPayload)
		want   string
	}{
		"no-hash":    {func(p *signedTxPayload) { p.TxHash = " " }, "missing tx hash"},
		"short-hash": {func(p *signedTxPayload) { p.TxHash = "abcd" }, "cached xmr tx hash must be 32-byte hex"},
		"hex-hash":   {func(p *signedTxPayload) { p.TxHash = strings.Repeat("z", 64) }, "cached xmr tx hash must be hex"},
		"no-meta":    {func(p *signedTxPayload) { p.TxMetadata = "" }, "missing tx_hex"},
		"bad-meta":   {func(p *signedTxPayload) { p.TxMetadata = "zz" }, "not valid hex"},
		"no-msg":     {func(p *signedTxPayload) { p.MsgID = "" }, "missing msg_id"},
		"bad-key":    {func(p *signedTxPayload) { p.TxKey = "00" }, "cached xmr tx key"},
	}
	for name, tc := range cases {
		p := good
		tc.mutate(&p)
		_, err = decodeStoredSignedXMRTxPayload(encode(p))
		require.ErrorContains(t, err, tc.want, name)
	}
}

func TestCloneXMRObservationClientCov(t *testing.T) {
	t.Parallel()

	require.Nil(t, cloneXMRObservation(nil))
	in := &stypes.TxInItem{
		Tx:              "tx",
		Coins:           common.Coins{common.NewCoin(common.XMRAsset, cosmos.NewUint(1))},
		Gas:             common.Gas{common.NewCoin(common.XMRAsset, cosmos.NewUint(2))},
		SpentOutputRefs: []*common.OutputRef{nil, testXMROutputRef("clone", 1, "")},
	}
	out := cloneXMRObservation(in)
	require.Equal(t, "tx", out.Tx)
	require.Len(t, out.SpentOutputRefs, 1, "nil refs are dropped")
	out.SpentOutputRefs[0].OutputIndex = 99
	out.Coins[0].Amount = cosmos.NewUint(5)
	require.Equal(t, uint32(1), in.SpentOutputRefs[1].OutputIndex, "deep copy")
	require.Equal(t, cosmos.NewUint(1), in.Coins[0].Amount)
}

type versionErrBridgeClientCov struct {
	*keysignFailureBridge
	versionErr error
	policyErr  error
	policy     *thorclient.KeysignPolicy
}

func (b *versionErrBridgeClientCov) GetThorchainVersion() (semver.Version, error) {
	if b.versionErr != nil {
		return semver.Version{}, b.versionErr
	}
	return b.keysignFailureBridge.GetThorchainVersion()
}

func (b *versionErrBridgeClientCov) GetKeysignPolicy(pk common.PubKey, h int64) (thorclient.KeysignPolicy, error) {
	if b.policyErr != nil {
		return thorclient.KeysignPolicy{}, b.policyErr
	}
	if b.policy != nil {
		return *b.policy, nil
	}
	return b.keysignFailureBridge.GetKeysignPolicy(pk, h)
}

func keysSidecarClientCov(t *testing.T, status int, vaults []sidecar.VaultKey) *sidecar.Client {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/v1/monero/keys":
			if status != 0 {
				http.Error(w, "keys unavailable", status)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"vaults": vaults})
		case r.URL.Path == "/v1/monero/vaults/register":
			_ = json.NewEncoder(w).Encode(testRegisterVaultResponse(1))
		case strings.HasPrefix(r.URL.Path, "/v1/monero/balance/"):
			bal := uint64(100 * moneroAtomicToThorchain)
			_ = json.NewEncoder(w).Encode(sidecar.Balance{Balance: bal, UnlockedBalance: bal, SpendableBalance: uint64Ptr(bal)})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	client, err := sidecar.NewClientWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
	require.NoError(t, err)
	return client
}

func TestSignTxEarlyReturnsClientCov(t *testing.T) {
	t.Parallel()

	tssServer := &mockFrostTssServer{err: errors.New("must not be reached")}
	c, tx, server, _ := newFrostSignTxCleanupFixture(t, tssServer, 0)
	t.Cleanup(server.Close)

	// Non-XMR chains are rejected by the public wrapper.
	btcTx := tx
	btcTx.Chain = common.BTCChain
	_, _, _, err := c.SignTx(btcTx, 10)
	require.ErrorContains(t, err, "cannot sign non-xmr tx")

	// Empty coins is a no-op.
	empty := tx
	empty.Coins = nil
	signed, ckpt, obs, err := c.signTx(empty, 10)
	require.NoError(t, err)
	require.Nil(t, signed)
	require.Nil(t, ckpt)
	require.Nil(t, obs)

	// Consolidation without a signing id cannot form an identity.
	consolidate := tx
	consolidate.Memo = "CONSOLIDATE"
	_, _, _, err = c.signTx(consolidate, 10)
	require.ErrorContains(t, err, "stable signing id")

	// Already signed in the signer cache.
	identity, err := xmrSigningCacheIdentity(tx)
	require.NoError(t, err)
	require.NoError(t, c.signerCacheManager.SetSigned(identity.CacheHash, "vault", "tx-hash"))
	signed, _, _, err = c.signTx(tx, 10)
	require.NoError(t, err)
	require.Nil(t, signed)

	// Missing sidecar / tss server fail closed.
	noSidecar := &Client{chain: common.XMRChain}
	_, _, _, err = noSidecar.signTx(tx, 10)
	require.ErrorIs(t, err, ErrSidecarNotConfigured)
	noTSS := &Client{chain: common.XMRChain, sidecar: c.sidecar}
	_, _, _, err = noTSS.signTx(tx, 10)
	require.ErrorContains(t, err, "tss server is not initialized")
}

func TestSignTxMembershipAndSignabilityClientCov(t *testing.T) {
	t.Parallel()

	newFixture := func(t *testing.T) (*Client, stypes.TxOutItem, *versionErrBridgeClientCov) {
		tssServer := &mockFrostTssServer{err: errors.New("must not be reached")}
		c, tx, server, _ := newFrostSignTxCleanupFixture(t, tssServer, 0)
		t.Cleanup(server.Close)
		base, ok := c.thorchainBridge.(*keysignFailureBridge)
		require.True(t, ok)
		bridge := &versionErrBridgeClientCov{keysignFailureBridge: base}
		c.thorchainBridge = bridge
		c.logger = log.Logger
		return c, tx, bridge
	}

	t.Run("version-error", func(t *testing.T) {
		c, tx, bridge := newFixture(t)
		bridge.versionErr = errors.New("version down")
		_, _, _, err := c.signTx(tx, 10)
		require.ErrorContains(t, err, "fail to resolve thorchain version")
	})
	t.Run("policy-error", func(t *testing.T) {
		c, tx, bridge := newFixture(t)
		bridge.policyErr = errors.New("policy down")
		_, _, _, err := c.signTx(tx, 10)
		require.ErrorContains(t, err, "fail to resolve vault signing members")
	})
	t.Run("empty-active", func(t *testing.T) {
		c, tx, bridge := newFixture(t)
		bridge.policy = &thorclient.KeysignPolicy{}
		_, _, err := c.vaultSigningMembers(tx.VaultPubKeyFrost, 10)
		require.ErrorContains(t, err, "empty active membership")
	})
	t.Run("foreign-signer", func(t *testing.T) {
		c, tx, bridge := newFixture(t)
		bridge.policy = &thorclient.KeysignPolicy{Signers: common.PubKeys{"stranger"}}
		_, _, err := c.vaultSigningMembers(tx.VaultPubKeyFrost, 10)
		require.ErrorContains(t, err, "is not in vault")
	})
	t.Run("empty-membership", func(t *testing.T) {
		c, tx, bridge := newFixture(t)
		vault := bridge.vaults[tx.VaultPubKeyFrost.String()]
		vault.Membership = []string{" "}
		bridge.vaults[tx.VaultPubKeyFrost.String()] = vault
		_, _, err := c.vaultSigningMembers(tx.VaultPubKeyFrost, 10)
		require.ErrorContains(t, err, "empty membership")
		_, _, err = c.vaultSigningMembers(common.EmptyPubKey, 10)
		require.ErrorContains(t, err, "vault pubkey is empty")
	})
	t.Run("vault-lookup-fallback", func(t *testing.T) {
		c, tx, bridge := newFixture(t)
		vault := bridge.vaults[tx.VaultPubKeyFrost.String()]
		// GetVault fails for the frost key but the asgard scan finds it.
		delete(bridge.vaults, tx.VaultPubKeyFrost.String())
		bridge.vaults["other-key"] = vault
		got, err := c.getVaultByPubKey(tx.VaultPubKeyFrost)
		require.NoError(t, err)
		require.True(t, got.PubKeyFrost.Equals(tx.VaultPubKeyFrost))
		_, err = c.getVaultByPubKey(testVaultPubKey(t, "absent-vault"))
		require.ErrorContains(t, err, "vault not found")
		bridge.vaults = nil
		_, err = c.getVaultByPubKey(tx.VaultPubKeyFrost)
		require.ErrorContains(t, err, "asgards lookup is unavailable")
		_, err = c.getVaultByPubKey(common.EmptyPubKey)
		require.ErrorContains(t, err, "vault pubkey is empty")
	})
	t.Run("keys-unavailable", func(t *testing.T) {
		c, tx, _ := newFixture(t)
		c.sidecar = keysSidecarClientCov(t, http.StatusServiceUnavailable, nil)
		_, _, _, err := c.signTx(tx, 10)
		require.ErrorContains(t, err, "local vault signability is unknown")
	})
	t.Run("no-local-share", func(t *testing.T) {
		c, tx, _ := newFixture(t)
		c.sidecar = keysSidecarClientCov(t, 0, nil)
		signed, _, _, err := c.signTx(tx, 10)
		require.NoError(t, err, "node stands down when it has no keyshare")
		require.Nil(t, signed)
	})
	t.Run("group-key-mismatch", func(t *testing.T) {
		c, tx, _ := newFixture(t)
		c.sidecar = keysSidecarClientCov(t, 0, []sidecar.VaultKey{{
			VaultPubKey: tx.VaultPubKeyFrost.String(), GroupKey: strings.Repeat("00", 32), HasFrostKeys: true, Signable: true,
		}})
		_, err := c.sidecarHasVaultKeys(tx.VaultPubKeyFrost)
		require.ErrorContains(t, err, "does not match requested vault group key")
		_, err = c.sidecarHasVaultKeys(common.EmptyPubKey)
		require.ErrorContains(t, err, "vault pubkey is empty")
		_, err = (&Client{}).sidecarHasVaultKeys(tx.VaultPubKeyFrost)
		require.ErrorContains(t, err, "sidecar not configured")
	})
}

func TestEnsureVaultBalanceClientCov(t *testing.T) {
	t.Parallel()

	vault, _ := testVaultPubKeyAndAddress(t)
	locked := uint64(10)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/v1/monero/vaults/register":
			_ = json.NewEncoder(w).Encode(testRegisterVaultResponse(1))
		case strings.HasPrefix(r.URL.Path, "/v1/monero/balance/"):
			_ = json.NewEncoder(w).Encode(sidecar.Balance{Balance: 100 + locked, UnlockedBalance: 100})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	sc, err := sidecar.NewClientWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
	require.NoError(t, err)
	c := &Client{chain: common.XMRChain, logger: log.Logger, sidecar: sc}

	require.NoError(t, c.ensureVaultBalance(vault, 50, 50))
	require.ErrorContains(t, c.ensureVaultBalance(vault, 100, 1), "outputs may need 10 confirmations")
	require.ErrorContains(t, c.ensureVaultBalance(vault, ^uint64(0), 1), "overflows uint64")
	require.NoError(t, (&Client{}).ensureVaultBalance(vault, 1, 1))

	// Unreachable balance endpoint.
	broken, err := sidecar.NewClientWithEndpoint(config.BifrostChainConfiguration{}, "http://127.0.0.1:1")
	require.NoError(t, err)
	c.sidecar = broken
	require.Error(t, c.ensureVaultBalance(vault, 1, 0))

	// Spendable balance is capped at total.
	require.Equal(t, uint64(5), spendableBalanceAtomic(sidecar.Balance{Balance: 5, UnlockedBalance: 9}))
	require.Equal(t, uint64(5), spendableBalanceAtomic(sidecar.Balance{Balance: 5, UnlockedBalance: 1, SpendableBalance: uint64Ptr(9)}))
	require.Equal(t, uint64(3), spendableBalanceAtomic(sidecar.Balance{Balance: 5, UnlockedBalance: 1, SpendableBalance: uint64Ptr(3)}))
}

func TestBroadcastTxValidationClientCov(t *testing.T) {
	t.Parallel()

	c := &Client{chain: common.XMRChain, logger: log.Logger}
	_, tx := validSignTxClientCov(t)
	_, err := c.BroadcastTx(tx, []byte("{"))
	require.ErrorContains(t, err, "fail to decode monero signed tx payload")
	_, err = c.broadcastTx(tx, signedTxPayload{})
	require.ErrorContains(t, err, "missing tx hash")
	_, err = c.broadcastTx(tx, signedTxPayload{TxHash: "abc"})
	require.ErrorContains(t, err, "missing tx_hex")
	_, err = c.broadcastTx(tx, signedTxPayload{TxHash: "abc", TxMetadata: "00", TxKey: "zz"})
	require.ErrorContains(t, err, "invalid tx_key")
}
