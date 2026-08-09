package monero

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/monero/sidecar"

	"github.com/rs/zerolog/log"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/config"
	ttypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// TestRefreshAsgardRegistersFrostKeyAtNextScanHeight validates that
// refreshAsgardLookup registers the frost key at Bifrost's next scan height.
//
// The bug: if the eddsa key is registered, the sidecar returns 500 (conflict
// with the existing frost key registration). refreshAsgardLookup returns an
// error. fetchTransfersByHeight previously returned (nil, nil) on this error,
// causing the bifrost to silently advance past the block without scanning it.
// Outputs in that block are lost forever.
//
// This test verifies both registration inputs received by the sidecar.
func TestRefreshAsgardRegistersFrostKeyAtNextScanHeight(t *testing.T) {
	t.Parallel()

	const (
		vaultCreationHeight = int64(568)
		nextScanHeight      = int64(542)
	)
	frostKey := testVaultPubKey(t, "frost-scanner-reg")
	eddsaKey := testVaultPubKey(t, "eddsa-scanner-reg")

	var registered []sidecar.RegisterVaultReq

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/monero/vaults/register" {
			var req sidecar.RegisterVaultReq
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			registered = append(registered, req)

			if req.VaultPubKey == eddsaKey.String() {
				t.Errorf("BUG: eddsa key %s was sent to sidecar register-vault — "+
					"this will cause a 500 conflict and block the scanner", eddsaKey.String())
				http.Error(w, `{"error":"already registered with different mapping"}`, http.StatusInternalServerError)
				return
			}

			_ = json.NewEncoder(w).Encode(testRegisterVaultResponse(uint64(req.VaultCreationHeight)))
			return
		}
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer server.Close()

	signer, err := sidecar.NewClientWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
	require.NoError(t, err)

	scanner := NewScanner(
		log.Logger,
		signer,
		&scannerMemoBridge{
			asgards: ttypes.Vaults{{
				PubKey:                 testVaultPubKey(t, "ecdsa-scanner-reg"),
				PubKeyEddsa:            eddsaKey,
				PubKeyFrost:            frostKey,
				VaultCreationHeightXmr: vaultCreationHeight,
			}},
		},
		common.XMRChain,
		common.EmptyPubKey,
		72,
	)
	require.NoError(t, scanner.refreshAsgardLookup(nextScanHeight), "frost key registration should succeed")
	require.Len(t, registered, 1)

	assert.Equal(t, frostKey.String(), registered[0].VaultPubKey,
		"only the frost key should be registered on the sidecar")
	assert.Equal(t, nextScanHeight, registered[0].VaultCreationHeight,
		"joining scanner must not start at vault height 568 while Bifrost still requests 542")
}

// TestRefreshAsgardAcceptsVaultBirthdayRewind pins the churn sequence where a
// vault is first enrolled near its DKG height, then Bifrost rolls its scanner
// back before activating the new node set. The sidecar must be able to publish
// the earlier durable birthday under a newer reset epoch, and a later retry of
// the on-chain birthday must remain idempotent against that earlier value.
func TestRefreshAsgardAcceptsVaultBirthdayRewind(t *testing.T) {
	t.Parallel()

	const (
		vaultCreationHeight = int64(772)
		churnRollbackHeight = int64(718)
	)
	frostKey, frostAddress := testVaultPubKeyAndAddress(t)
	registerRequests := make(chan sidecar.RegisterVaultReq, 4)
	rewound := false

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/monero/vaults/register" {
			http.NotFound(w, r)
			return
		}

		var req sidecar.RegisterVaultReq
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		registerRequests <- req

		switch req.VaultCreationHeight {
		case vaultCreationHeight:
			if !rewound {
				next := uint64(786)
				_ = json.NewEncoder(w).Encode(sidecar.RegisterVaultResp{
					Registered:         true,
					EffectiveBirthday:  uint64(vaultCreationHeight),
					ResetEpoch:         0,
					ExpectedNextHeight: &next,
				})
				return
			}

			// A retry based on the unchanged on-chain vault height must
			// retain the earlier durable enrollment rather than move forward.
			next := uint64(churnRollbackHeight)
			_ = json.NewEncoder(w).Encode(sidecar.RegisterVaultResp{
				Registered:         true,
				EffectiveBirthday:  uint64(churnRollbackHeight),
				ResetEpoch:         1,
				ExpectedNextHeight: &next,
			})
		case churnRollbackHeight:
			rewound = true
			next := uint64(churnRollbackHeight)
			_ = json.NewEncoder(w).Encode(sidecar.RegisterVaultResp{
				Registered:         true,
				EffectiveBirthday:  uint64(churnRollbackHeight),
				ResetEpoch:         1,
				ExpectedNextHeight: &next,
			})
		default:
			http.Error(w, `{"error":"vault birthday rewind rejected"}`, http.StatusConflict)
		}
	}))
	defer server.Close()

	signer, err := sidecar.NewClientWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
	require.NoError(t, err)

	scanner := NewScanner(
		log.Logger,
		signer,
		&scannerMemoBridge{asgards: ttypes.Vaults{{
			PubKey:                 testVaultPubKey(t, "secp-birthday-rewind"),
			PubKeyFrost:            frostKey,
			VaultCreationHeightXmr: vaultCreationHeight,
		}}},
		common.XMRChain,
		common.EmptyPubKey,
		72,
	)

	forceRefresh := func(nextScanHeight int64) error {
		scanner.asgardMu.Lock()
		scanner.asgardUpdated = time.Time{}
		scanner.asgardMu.Unlock()
		return scanner.refreshAsgardLookup(nextScanHeight)
	}
	assertRequestBirthday := func(want int64) {
		t.Helper()
		select {
		case req := <-registerRequests:
			assert.Equal(t, frostKey.String(), req.VaultPubKey)
			assert.Equal(t, want, req.VaultCreationHeight)
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for vault registration at birthday %d", want)
		}
	}

	// Initial enrollment can be ahead of the height Bifrost will request after
	// the generic churn-in rollback.
	require.NoError(t, forceRefresh(vaultCreationHeight))
	assertRequestBirthday(vaultCreationHeight)
	state, known := signer.CachedScannerState()
	require.True(t, known)
	require.NotNil(t, state.NextHeight)
	assert.Equal(t, uint64(786), *state.NextHeight)
	assert.Equal(t, uint64(0), state.ResetEpoch)

	// Registration at the rollback height publishes the rewound cursor even
	// though its height is lower, because the reset epoch advanced.
	require.NoError(t, forceRefresh(churnRollbackHeight))
	assertRequestBirthday(churnRollbackHeight)
	state, known = signer.CachedScannerState()
	require.True(t, known)
	require.NotNil(t, state.NextHeight)
	assert.Equal(t, uint64(churnRollbackHeight), *state.NextHeight)
	assert.Equal(t, uint64(1), state.ResetEpoch)
	assert.True(t, state.StrictAfterReset)

	// THORChain continues to advertise 772. The sidecar is allowed to return
	// its earlier effective birthday, and the client must keep that continuity.
	require.NoError(t, forceRefresh(vaultCreationHeight))
	assertRequestBirthday(vaultCreationHeight)
	state, known = signer.CachedScannerState()
	require.True(t, known)
	require.NotNil(t, state.NextHeight)
	assert.Equal(t, uint64(churnRollbackHeight), *state.NextHeight)
	assert.Equal(t, uint64(1), state.ResetEpoch)

	// Any future refusal still fails the refresh closed: do not publish the
	// partial replacement lookup or discard the authenticated rewind cursor.
	require.Error(t, forceRefresh(churnRollbackHeight-1))
	assertRequestBirthday(churnRollbackHeight - 1)
	state, known = signer.CachedScannerState()
	require.True(t, known)
	require.NotNil(t, state.NextHeight)
	assert.Equal(t, uint64(churnRollbackHeight), *state.NextHeight)
	assert.Equal(t, uint64(1), state.ResetEpoch)
	scanner.asgardMu.RLock()
	assert.Equal(t, frostKey, scanner.asgardLookup[frostAddress.String()])
	scanner.asgardMu.RUnlock()
}

func TestRefreshAsgardSkipsVaultWithoutPubKeyFrost(t *testing.T) {
	t.Parallel()

	eddsaKey := testVaultPubKey(t, "eddsa-scanner-fallback")

	var registerCalls int

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/monero/vaults/register" {
			registerCalls++
			_ = json.NewEncoder(w).Encode(sidecar.RegisterVaultResp{
				Registered:         true,
				ExpectedNextHeight: uint64Ptr(0),
			})
			return
		}
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer server.Close()

	signer, err := sidecar.NewClientWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
	require.NoError(t, err)

	scanner := NewScanner(
		log.Logger,
		signer,
		&scannerMemoBridge{
			asgards: ttypes.Vaults{{
				PubKey:      testVaultPubKey(t, "ecdsa-scanner-fallback"),
				PubKeyEddsa: eddsaKey,
			}},
		},
		common.XMRChain,
		common.EmptyPubKey,
		72,
	)
	require.NoError(t, scanner.refreshAsgardLookup(0), "vaults without frost keys should be ignored, not registered")

	assert.Equal(t, 0, registerCalls, "eddsa-only xmr vaults should not hit sidecar registration")
}

func TestFetchTxsObservesInactiveFrostVaultFromPubKeys(t *testing.T) {
	t.Parallel()

	secpKey := testVaultPubKey(t, "inactive-xmr-secp")
	frostKey, frostAddr := testVaultPubKeyAndAddress(t)
	txID := strings.Repeat("9", 64)

	frostServer := newFrostTransfersServer(
		t,
		[]sidecar.Transfer{{
			TxID:        txID,
			Address:     frostAddr.String(),
			VaultPubKey: frostKey.String(),
			Amount:      uint64(2 * moneroAtomicToThorchain),
			Height:      100,
			Note:        "REFUND:" + strings.Repeat("a", 64),
		}},
		nil,
		[]string{frostAddr.String()},
	)
	defer frostServer.Close()

	signer, err := sidecar.NewClientWithEndpoint(config.BifrostChainConfiguration{}, frostServer.URL)
	require.NoError(t, err)

	scanner := NewScanner(
		log.Logger,
		signer,
		&scannerMemoBridge{
			asgards: ttypes.Vaults{},
			pubkeyVaults: ttypes.Vaults{{
				PubKey:      secpKey,
				PubKeyFrost: frostKey,
				Status:      ttypes.VaultStatus_InactiveVault,
			}},
		},
		common.XMRChain,
		common.EmptyPubKey,
		72,
	)

	txIn, err := scanner.FetchTxs(100, 150)
	require.NoError(t, err)
	require.Len(t, txIn.TxArray, 1)
	assert.Equal(t, frostKey, txIn.TxArray[0].ObservedVaultPubKey)
	assert.Equal(t, frostAddr.String(), txIn.TxArray[0].To)
}

func TestFetchTxsObservesInactiveFrostVaultByAddressLookup(t *testing.T) {
	t.Parallel()

	secpKey := testVaultPubKey(t, "inactive-xmr-secp-by-address")
	frostKey, frostAddr := testVaultPubKeyAndAddress(t)
	txID := strings.Repeat("8", 64)

	frostServer := newFrostTransfersServer(
		t,
		[]sidecar.Transfer{{
			TxID:    txID,
			Address: frostAddr.String(),
			Amount:  uint64(2 * moneroAtomicToThorchain),
			Height:  100,
			Note:    "REFUND:" + strings.Repeat("b", 64),
		}},
		nil,
		[]string{frostAddr.String()},
	)
	defer frostServer.Close()

	signer, err := sidecar.NewClientWithEndpoint(config.BifrostChainConfiguration{}, frostServer.URL)
	require.NoError(t, err)

	scanner := NewScanner(
		log.Logger,
		signer,
		&scannerMemoBridge{
			asgards: ttypes.Vaults{},
			pubkeyVaults: ttypes.Vaults{{
				PubKey:      secpKey,
				PubKeyFrost: frostKey,
				Status:      ttypes.VaultStatus_InactiveVault,
			}},
		},
		common.XMRChain,
		common.EmptyPubKey,
		72,
	)

	txIn, err := scanner.FetchTxs(100, 150)
	require.NoError(t, err)
	require.Len(t, txIn.TxArray, 1)
	assert.Equal(t, frostKey, txIn.TxArray[0].ObservedVaultPubKey)
	assert.Equal(t, frostAddr.String(), txIn.TxArray[0].To)
}
