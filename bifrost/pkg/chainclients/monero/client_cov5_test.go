package monero

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/rs/zerolog/log"
	"github.com/stretchr/testify/require"

	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/monero/sidecar"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	stypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	bifrosttss "gitlab.com/thorchain/thornode/v3/bifrost/tss"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/config"
	ttypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

func countingSidecarClientCov(t *testing.T) (*sidecar.Client, *int32) {
	t.Helper()
	var hits int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		http.NotFound(w, r)
	}))
	t.Cleanup(server.Close)
	client, err := sidecar.NewClientWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
	require.NoError(t, err)
	return client, &hits
}

func TestCatchupSpentOutputRefsSkipsClientCov(t *testing.T) {
	t.Parallel()

	// Unconfigured clients are no-ops.
	(&Client{}).catchupSpentOutputRefs()
	_, err := (&Client{}).getSpentOutputCatchupVaultKeys()
	require.ErrorContains(t, err, "bridge is nil")

	sc, hits := countingSidecarClientCov(t)
	vaultA := testVaultPubKey(t, "catchup-a")
	vaultB := testVaultPubKey(t, "catchup-b")
	var mu sync.Mutex
	queried := map[string]int{}
	bridge := &mockMoneroBridge{
		pubKeys: []thorclient.PubKeyContractAddressPair{
			{PubKey: vaultA, Algo: common.SigningAlgoEd25519},
			{PubKey: vaultA, Algo: common.SigningAlgoEd25519},
			{PubKey: vaultB, Algo: common.SigningAlgoEd25519},
			{PubKey: testVaultPubKey(t, "secp"), Algo: common.SigningAlgoSecp256k1},
			{PubKey: common.EmptyPubKey, Algo: common.SigningAlgoEd25519},
		},
		spentRefsFunc: func(pk common.PubKey) ([]*common.OutputRef, error) {
			mu.Lock()
			queried[pk.String()]++
			mu.Unlock()
			if pk.Equals(vaultA) {
				return nil, errors.New("refs down")
			}
			// Nil refs and refs without a spend hash are skipped.
			return []*common.OutputRef{nil, testXMROutputRef("no-spend", 0, " ")}, nil
		},
	}
	c := &Client{
		chain: common.XMRChain, logger: log.Logger, thorchainBridge: bridge, sidecar: sc,
		wg: &sync.WaitGroup{}, stopchan: make(chan struct{}),
	}

	keys, err := c.getSpentOutputCatchupVaultKeys()
	require.NoError(t, err)
	require.Equal(t, common.PubKeys{vaultA, vaultB}, keys, "only unique, non-empty ed25519 keys")

	// The runner performs one catch-up then exits on stop.
	close(c.stopchan)
	c.wg.Add(1)
	c.spentOutputRefsSyncRunner()
	mu.Lock()
	require.Equal(t, map[string]int{vaultA.String(): 1, vaultB.String(): 1}, queried)
	mu.Unlock()
	require.Zero(t, atomic.LoadInt32(hits), "nothing importable reached the sidecar")

	bridge.pubKeysErr = errors.New("pubkeys down")
	_, err = c.getSpentOutputCatchupVaultKeys()
	require.ErrorContains(t, err, "fail to get vault pubkeys")
	c.catchupSpentOutputRefs()
	mu.Lock()
	require.Equal(t, 1, queried[vaultA.String()], "no further queries without vault keys")
	mu.Unlock()
}

func TestPostKeysignFailureClientCov(t *testing.T) {
	t.Parallel()

	vault, _ := testVaultPubKeyAndAddress(t)
	tx := stypes.TxOutItem{
		Chain: common.XMRChain, VaultPubKeyFrost: vault, Memo: "OUT:ABC",
		Coins: common.Coins{common.NewCoin(common.XMRAsset, cosmos.NewUint(3))},
	}
	plain := errors.New("plain failure")

	require.NoError(t, (&Client{}).postKeysignFailure(tx, 1, nil))
	require.ErrorIs(t, (&Client{}).postKeysignFailure(tx, 1, plain), plain)

	bridge := &keysignFailureBridge{}
	c := &Client{chain: common.XMRChain, logger: log.Logger, thorchainBridge: bridge}
	require.ErrorIs(t, c.postKeysignFailure(tx, 1, plain), plain)
	require.Zero(t, bridge.calls)

	noNodes := bifrosttss.NewKeysignError(ttypes.Blame{FailReason: "x"})
	require.Error(t, c.postKeysignFailure(tx, 1, noNodes))
	require.Zero(t, bridge.calls)

	blamed := bifrosttss.NewKeysignError(ttypes.Blame{FailReason: "x", BlameNodes: []ttypes.Node{{Pubkey: "member-a"}}})
	err := c.postKeysignFailure(tx, 9, blamed)
	require.Error(t, err)
	require.Equal(t, 1, bridge.calls)
	require.Equal(t, int64(9), bridge.lastHeight)
	require.True(t, bridge.lastPubKey.Equals(vault))
	require.Equal(t, "OUT:ABC", bridge.lastMemo)

	bridge.retErr = errors.New("post down")
	err = c.postKeysignFailure(tx, 9, blamed)
	require.ErrorContains(t, err, "fail to post keysign failure to THORChain")
	require.Equal(t, 2, bridge.calls)
}

func TestVaultAddressMappingClientCov(t *testing.T) {
	t.Parallel()

	vault, addr := testVaultPubKeyAndAddress(t)
	_, otherAddr := testVaultPubKeyAndAddress(t)
	store := newTestVaultAddressStore(t)
	c := &Client{chain: common.XMRChain, logger: log.Logger, vaultAddressStore: store}

	c.setVaultAddressMapping(common.EmptyPubKey, addr)
	c.setVaultAddressMapping(vault, common.NoAddress)
	require.Empty(t, c.vaultAddressMap)

	c.setVaultAddressMapping(vault, addr)
	persisted, err := store.Get(vault.String())
	require.NoError(t, err)
	require.Equal(t, addr.String(), persisted)

	// Remapping drops the stale reverse entry.
	c.setVaultAddressMapping(vault, otherAddr)
	_, found := c.findVaultPubKeyByAddress(addr.String())
	require.False(t, found)
	pk, found := c.findVaultPubKeyByAddress(otherAddr.String())
	require.True(t, found)
	require.True(t, pk.Equals(vault))

	// Forward-map fallback when the reverse index is missing.
	delete(c.vaultAddrToKey, otherAddr.String())
	pk, found = c.findVaultPubKeyByAddress(" " + otherAddr.String() + " ")
	require.True(t, found)
	require.True(t, pk.Equals(vault))
	_, found = c.findVaultPubKeyByAddress(" ")
	require.False(t, found)

	// Persistence failure is logged, not fatal.
	require.NoError(t, store.db.Close())
	c.setVaultAddressMapping(vault, addr)
	require.Equal(t, addr, c.vaultAddressMap[vault.String()])
}
