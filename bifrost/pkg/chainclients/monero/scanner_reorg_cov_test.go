package monero

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/monero/sidecar"
)

func newReorgJournalScannerCov(t *testing.T, next uint64, maxReorg int64) (*Scanner, *catchupSidecarCov, *ReorgStore) {
	t.Helper()
	scanner, fake, store := newCatchupScannerCov(t, next, maxReorg)
	require.NoError(t, store.Set(&reorgBlockMeta{Height: 8, BlockHash: testBlockHash(8), PreviousHash: testBlockHash(7)}))
	require.NoError(t, store.Set(&reorgBlockMeta{Height: 9, BlockHash: testStaleBlockHash(9), PreviousHash: testBlockHash(8)}))
	require.NoError(t, store.Set(&reorgBlockMeta{Height: 10, BlockHash: testStaleBlockHash(10), PreviousHash: testStaleBlockHash(9)}))
	return scanner, fake, store
}

func tipBlockCov(height int64) sidecar.ChainBlock {
	return sidecar.ChainBlock{Height: height, Hash: testBlockHash(height), PreviousHash: testBlockHash(height - 1)}
}

func TestProcessReorgGuardsCov(t *testing.T) {
	t.Parallel()

	t.Run("no-journal-or-store", func(t *testing.T) {
		scanner, _, _ := newCatchupScannerCov(t, 11, 8)
		metas, err := scanner.processReorg(tipBlockCov(11), 20)
		require.NoError(t, err)
		require.Nil(t, metas)
		scanner.reorgStore = nil
		metas, err = scanner.processReorg(tipBlockCov(11), 20)
		require.NoError(t, err)
		require.Nil(t, metas)
	})
	t.Run("corrupt-journal-key", func(t *testing.T) {
		scanner, _, store := newCatchupScannerCov(t, 11, 8)
		require.NoError(t, store.db.Put([]byte(reorgBlockMetaPrefix+"x"), []byte("{}"), nil))
		_, err := scanner.processReorg(tipBlockCov(11), 20)
		require.ErrorContains(t, err, "invalid xmr reorg journal key")
	})
	t.Run("corrupt-journal-entry", func(t *testing.T) {
		scanner, _, store := newCatchupScannerCov(t, 11, 8)
		require.NoError(t, store.db.Put([]byte(reorgBlockMetaKey(10)), []byte("{"), nil))
		_, err := scanner.processReorg(tipBlockCov(11), 20)
		require.ErrorContains(t, err, "fail to decode xmr reorg block meta")
	})
	t.Run("same-height-matching", func(t *testing.T) {
		scanner, _, _ := newReorgJournalScannerCov(t, 11, 8)
		metas, err := scanner.processReorg(sidecar.ChainBlock{Height: 10, Hash: testStaleBlockHash(10), PreviousHash: testStaleBlockHash(9)}, 20)
		require.NoError(t, err)
		require.Nil(t, metas)
	})
	t.Run("distant-tip-lookup-failure", func(t *testing.T) {
		scanner, fake, _ := newReorgJournalScannerCov(t, 11, 8)
		fake.set(func(c *catchupSidecarCov) { c.blockFail[10] = http.StatusInternalServerError })
		_, err := scanner.processReorg(tipBlockCov(20), 20)
		require.ErrorContains(t, err, "fetch latest journal block 10")
	})
	t.Run("discover-lookup-failure", func(t *testing.T) {
		scanner, fake, _ := newReorgJournalScannerCov(t, 11, 8)
		fake.set(func(c *catchupSidecarCov) { c.blockFail[10] = http.StatusInternalServerError })
		_, err := scanner.processReorg(tipBlockCov(11), 20)
		require.ErrorContains(t, err, "during reorg check")
	})
	t.Run("replay-disabled", func(t *testing.T) {
		scanner, _, _ := newReorgJournalScannerCov(t, 11, 0)
		_, err := scanner.processReorg(tipBlockCov(11), 20)
		require.ErrorContains(t, err, "replay is disabled")
	})
	t.Run("no-common-ancestor", func(t *testing.T) {
		scanner, _, _ := newReorgJournalScannerCov(t, 11, 1)
		_, err := scanner.processReorg(tipBlockCov(11), 20)
		require.ErrorContains(t, err, "no common ancestor within 1 blocks")
	})
	t.Run("state-failure", func(t *testing.T) {
		scanner, fake, _ := newReorgJournalScannerCov(t, 11, 8)
		fake.set(func(c *catchupSidecarCov) { c.stateFail = true })
		_, err := scanner.processReorg(tipBlockCov(11), 20)
		require.Error(t, err)
	})
	t.Run("rewind-beyond-limit", func(t *testing.T) {
		scanner, _, _ := newReorgJournalScannerCov(t, 12, 2)
		_, err := scanner.processReorg(tipBlockCov(11), 20)
		require.ErrorContains(t, err, "exceeds bounded replay limit 2")
	})
	t.Run("rewind-failure", func(t *testing.T) {
		scanner, fake, _ := newReorgJournalScannerCov(t, 11, 8)
		fake.set(func(c *catchupSidecarCov) { c.rewindFail = true })
		_, err := scanner.processReorg(tipBlockCov(11), 20)
		require.ErrorContains(t, err, "rewind")
	})
	t.Run("replay-fetch-failure-keeps-old-journal", func(t *testing.T) {
		scanner, fake, store := newReorgJournalScannerCov(t, 11, 8)
		_, err := scanner.processReorg(tipBlockCov(11), 20)
		// the fake has no scanner sync endpoint, so the replacement range fetch fails
		require.ErrorContains(t, err, "fail to get monero transfers at 9")
		require.ErrorContains(t, err, "/v1/monero/scanner/sync returned status 404")
		fake.mu.Lock()
		require.Len(t, fake.rewindCalls, 1)
		require.Equal(t, uint64(9), fake.rewindCalls[0].TargetNextHeight)
		require.Equal(t, testBlockHash(8), fake.rewindCalls[0].AnchorHash)
		fake.mu.Unlock()
		meta, err := store.Get(10)
		require.NoError(t, err)
		require.Equal(t, testStaleBlockHash(10), meta.BlockHash, "old journal survives a failed replay")
	})
	t.Run("pre-anchor-catchup", func(t *testing.T) {
		scanner, _, store := newReorgJournalScannerCov(t, 5, 8)
		require.NoError(t, store.db.Put([]byte(scannerCatchupMarkerDBKey), []byte("{"), nil))
		_, err := scanner.processReorg(tipBlockCov(11), 20)
		require.ErrorContains(t, err, "decode xmr scanner catch-up marker")
		require.NoError(t, store.db.Delete([]byte(scannerCatchupMarkerDBKey), nil))
		_, err = scanner.processReorg(tipBlockCov(11), 20)
		require.Error(t, err)
		marker, err := store.GetCatchupMarker()
		require.NoError(t, err)
		require.NotNil(t, marker)
		require.Equal(t, int64(5), marker.StartHeight)
		require.Equal(t, int64(8), marker.TargetHeight, "durable catch-up targets the reorg anchor")
	})
}
