package utxo

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/syndtr/goleveldb/leveldb"
)

func TestInstantSendDeliveryPhasesSurviveRestart(t *testing.T) {
	path := t.TempDir()
	db, err := leveldb.OpenFile(path, nil)
	require.NoError(t, err)
	store, err := NewTemporalStorage(db, 0)
	require.NoError(t, err)
	ids, err := store.ClaimInstantSendTxs([]string{"ABC", "abc"})
	require.NoError(t, err)
	require.Equal(t, []string{"abc"}, ids)
	require.NoError(t, store.CompleteInstantSendObservations(ids))
	require.NoError(t, db.Close())
	db, err = leveldb.OpenFile(path, nil)
	require.NoError(t, err)
	defer db.Close()
	store, err = NewTemporalStorage(db, 0)
	require.NoError(t, err)
	ids, err = store.ClaimInstantSendTxs([]string{"abc"})
	require.NoError(t, err)
	require.Empty(t, ids)
	ids, err = store.TrackInstantSendBlockTxs([]string{"abc"})
	require.NoError(t, err)
	require.Equal(t, []string{"abc"}, ids)
	require.NoError(t, store.CompleteInstantSendObservations(ids)) // Late callback cannot restore the early marker.
	ids, err = store.TrackInstantSendBlockTxs([]string{"abc"})
	require.NoError(t, err)
	require.Empty(t, ids)
}

func TestInstantSendBlockClaimWinsConcurrentAdmission(t *testing.T) {
	db, err := leveldb.OpenFile(t.TempDir(), nil)
	require.NoError(t, err)
	defer db.Close()
	store, err := NewTemporalStorage(db, 0)
	require.NoError(t, err)
	var wg sync.WaitGroup
	failures := make(chan error, 20)
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, claimErr := store.ClaimInstantSendTxs([]string{"abc"})
			failures <- claimErr
		}()
	}
	ids, err := store.TrackInstantSendBlockTxs([]string{"abc"})
	require.NoError(t, err)
	require.Equal(t, []string{"abc"}, ids)
	wg.Wait()
	close(failures)
	for claimErr := range failures {
		require.NoError(t, claimErr)
	}
	ids, err = store.ClaimInstantSendTxs([]string{"abc"})
	require.NoError(t, err)
	require.Empty(t, ids)
}
