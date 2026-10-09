package monero

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/monero/sidecar"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/config"
)

// errataTxModeCov describes how the fake sidecar reports one transaction.
type errataTxModeCov struct {
	locationStatus int // 0 => located
	location       sidecar.CanonicalTxLocationResp
	existsStatus   int // 0 => ok
	exists         bool
}

func newErrataScannerCov(t *testing.T, modes map[string]errataTxModeCov) (*Scanner, *ReorgStore) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		rest := strings.TrimPrefix(r.URL.Path, "/v1/monero/chain/tx/")
		parts := strings.Split(rest, "/")
		if len(parts) != 2 {
			http.NotFound(w, r)
			return
		}
		mode, ok := modes[parts[0]]
		if !ok {
			http.NotFound(w, r)
			return
		}
		switch parts[1] {
		case "location":
			if mode.locationStatus != 0 {
				http.Error(w, "location", mode.locationStatus)
				return
			}
			_ = json.NewEncoder(w).Encode(mode.location)
		case "exists":
			if mode.existsStatus != 0 {
				http.Error(w, "exists", mode.existsStatus)
				return
			}
			_ = json.NewEncoder(w).Encode(sidecar.TxExistsResp{Exists: mode.exists})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	client, err := sidecar.NewClientWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
	require.NoError(t, err)
	store := newScannerReceiptStore(t)
	scanner := NewScanner(testLogger(), client, &scannerMemoBridge{}, common.XMRChain, common.EmptyPubKey, 8)
	scanner.SetReorgStore(store)
	return scanner, store
}

func watchForCov(t *testing.T, store *ReorgStore, txID string) scannerRelocationWatch {
	t.Helper()
	watches, err := store.ListRelocationWatches(0)
	require.NoError(t, err)
	for _, w := range watches {
		if w.TxID == normalizeTxID(txID) {
			return w
		}
	}
	t.Fatalf("no relocation watch for %s", txID)
	return scannerRelocationWatch{}
}

func TestEmitReorgErrataProvenanceHoldsCov(t *testing.T) {
	t.Parallel()

	absent := errataTxModeCov{locationStatus: http.StatusNotFound}
	legacy := testXMRHash("errata-legacy")
	noHandler := testXMRHash("errata-no-handler")
	scanner, store := newErrataScannerCov(t, map[string]errataTxModeCov{legacy: absent, noHandler: absent})

	// Legacy journals without provenance hold every orphan.
	require.NoError(t, scanner.emitReorgErrata(&reorgBlockMeta{Height: 50, BlockHash: "b", CustomerTransactions: []string{legacy}}, nil))
	w := watchForCov(t, store, legacy)
	require.True(t, w.HoldAbsent)
	require.True(t, w.VaultOrigin)

	// Without a provenance checker the orphan is held too.
	require.NoError(t, scanner.emitReorgErrata(&reorgBlockMeta{
		Height: 51, BlockHash: "b", CustomerTransactions: []string{noHandler}, ProvenanceComplete: true,
	}, nil))
	require.True(t, watchForCov(t, store, noHandler).HoldAbsent)
}

func TestEmitReorgErrataLocalSignedHandlerCov(t *testing.T) {
	t.Parallel()

	absent := errataTxModeCov{locationStatus: http.StatusNotFound}
	failing := testXMRHash("errata-handler-error")
	customer := testXMRHash("errata-customer")
	scanner, store := newErrataScannerCov(t, map[string]errataTxModeCov{failing: absent, customer: absent})
	scanner.SetLocalSignedTxHandler(func(txID string) (bool, error) {
		if txID == failing {
			return false, errors.New("provenance store unavailable")
		}
		return false, nil
	})

	require.NoError(t, scanner.emitReorgErrata(&reorgBlockMeta{
		Height: 60, BlockHash: "b", CustomerTransactions: []string{failing}, ProvenanceComplete: true,
	}, nil))
	require.True(t, watchForCov(t, store, failing).HoldAbsent, "a provenance failure holds the tx")

	// An absent customer deposit produces errata, which needs the observer queue.
	err := scanner.emitReorgErrata(&reorgBlockMeta{
		Height: 61, BlockHash: "b", CustomerTransactions: []string{customer}, ProvenanceComplete: true,
	}, nil)
	require.ErrorContains(t, err, "errata queue is unavailable")
}

func TestEmitReorgErrataLocationsCov(t *testing.T) {
	t.Parallel()

	relocated := testXMRHash("errata-relocated")
	replayed := testXMRHash("errata-replayed")
	inPool := testXMRHash("errata-in-pool")
	locErr := testXMRHash("errata-location-error")
	existsErr := testXMRHash("errata-exists-error")
	scanner, store := newErrataScannerCov(t, map[string]errataTxModeCov{
		relocated: {location: sidecar.CanonicalTxLocationResp{TxHash: relocated, Height: 75, BlockHash: testBlockHash(75), PreviousHash: testBlockHash(74)}},
		replayed:  {location: sidecar.CanonicalTxLocationResp{TxHash: replayed, Height: 71, BlockHash: testBlockHash(71), PreviousHash: testBlockHash(70)}},
		inPool:    {locationStatus: http.StatusNotFound, exists: true},
		locErr:    {locationStatus: http.StatusInternalServerError},
		existsErr: {locationStatus: http.StatusNotFound, existsStatus: http.StatusInternalServerError},
	})
	scanner.SetLocalSignedTxHandler(func(string) (bool, error) { return true, nil })

	replacement := &reorgBlockMeta{Height: 71, BlockHash: testBlockHash(71), PreviousHash: testBlockHash(70)}
	require.NoError(t, scanner.emitReorgErrata(&reorgBlockMeta{
		Height: 70, BlockHash: "b", ProvenanceComplete: true,
		CustomerTransactions: []string{relocated, replayed, inPool},
	}, []*reorgBlockMeta{nil, {Height: 71, BlockHash: "other"}, replacement}))

	w := watchForCov(t, store, relocated)
	require.Equal(t, int64(75), w.LocationHeight)
	require.Equal(t, int64(70), w.OriginalHeight)
	require.True(t, w.VaultOrigin)

	require.Equal(t, []string{replayed}, replacement.CustomerTransactions)
	require.Equal(t, []string{replayed}, replacement.VaultOriginTransactions)

	require.True(t, watchForCov(t, store, inPool).InPool)

	err := scanner.emitReorgErrata(&reorgBlockMeta{Height: 70, BlockHash: "b", ProvenanceComplete: true, CustomerTransactions: []string{locErr}}, nil)
	require.ErrorContains(t, err, "unable to locate canonical monero tx")
	err = scanner.emitReorgErrata(&reorgBlockMeta{Height: 70, BlockHash: "b", ProvenanceComplete: true, CustomerTransactions: []string{existsErr}}, nil)
	require.ErrorContains(t, err, "pool presence")
}
