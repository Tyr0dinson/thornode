package observer

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p-core/peer"
	"github.com/stretchr/testify/require"

	"gitlab.com/thorchain/thornode/v3/common"
)

func newXMRBarrierTestBatcher(
	t *testing.T,
	peerTimeout time.Duration,
	remotes ...peer.ID,
) *AttestationBatcher {
	t.Helper()
	peers := append([]peer.ID{peer.ID("self")}, remotes...)
	batcher := NewAttestationBatcher(
		NewMockHost(peers),
		getTestLogger(t),
		nil,
		time.Hour,
		100,
		peerTimeout,
		len(remotes),
	)
	batcher.xmrErrataBarrier = &sync.RWMutex{}
	batcher.setActiveValGetter(func() map[peer.ID]bool {
		active := make(map[peer.ID]bool, len(remotes))
		for _, remote := range remotes {
			active[remote] = true
		}
		return active
	})
	return batcher
}

func xmrBarrierTestAttestation(chain common.Chain, txID common.TxID, minedHeight int64) common.AttestTx {
	return common.AttestTx{
		ObsTx: common.ObservedTx{
			Tx: common.Tx{
				ID:    txID,
				Chain: chain,
			},
			BlockHeight:    minedHeight,
			FinaliseHeight: minedHeight,
			XmrMinedHeight: minedHeight,
		},
		Attestation: &common.Attestation{
			PubKey:    []byte("pubkey"),
			Signature: []byte("signature"),
		},
	}
}

func TestXMRBatchFanoutReleasesErrataBarrierAtSharedDeadline(t *testing.T) {
	remotes := []peer.ID{"peer-1", "peer-2", "peer-3"}
	batcher := newXMRBarrierTestBatcher(t, 50*time.Millisecond, remotes...)

	started := make(chan peer.ID, len(remotes))
	deadlines := make(chan time.Time, len(remotes))
	var exited atomic.Int32
	batcher.broadcastToPeerFn = func(ctx context.Context, remote peer.ID, _ []byte) {
		deadline, _ := ctx.Deadline()
		deadlines <- deadline
		started <- remote
		<-ctx.Done()
		exited.Add(1)
	}
	batcher.AddObservedTx(xmrBarrierTestAttestation(common.XMRChain, "shared-fanout-deadline", 100))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sendDone := make(chan struct{})
	go func() {
		batcher.sendBatches(ctx, true)
		close(sendDone)
	}()

	for range remotes {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for every XMR fan-out attempt")
		}
	}

	writerAcquired := make(chan struct{})
	writerDone := make(chan struct{})
	startedWaiting := time.Now()
	go func() {
		batcher.xmrErrataBarrier.Lock()
		close(writerAcquired)
		batcher.xmrErrataBarrier.Unlock()
		close(writerDone)
	}()
	select {
	case <-writerAcquired:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("XMR batch fan-out kept the errata writer past its shared deadline")
	}
	require.Less(t, time.Since(startedWaiting), 500*time.Millisecond)
	require.Equal(t, int32(len(remotes)), exited.Load(), "barrier released before every fan-out goroutine exited")

	var sharedDeadline time.Time
	for i := range remotes {
		deadline := <-deadlines
		require.False(t, deadline.IsZero(), "XMR fan-out attempt did not receive an absolute deadline")
		if i == 0 {
			sharedDeadline = deadline
			continue
		}
		require.Equal(t, sharedDeadline, deadline)
	}
	select {
	case <-sendDone:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for bounded XMR batch send")
	}
	select {
	case <-writerDone:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for errata writer exit")
	}
}

func TestNonXMRBatchBroadcastsWhileErrataWriterWaits(t *testing.T) {
	remote := peer.ID("peer-1")
	batcher := newXMRBarrierTestBatcher(t, 2*time.Second, remote)

	xmrStarted := make(chan struct{})
	nonXMRBroadcast := make(chan struct{}, 1)
	decodeErr := make(chan error, 2)
	var xmrStartedOnce sync.Once
	batcher.broadcastToPeerFn = func(ctx context.Context, _ peer.ID, payload []byte) {
		var batch common.AttestationBatch
		if err := batch.Unmarshal(payload); err != nil {
			decodeErr <- err
			return
		}
		if containsXMRObservedTx(batch.AttestTxs) {
			xmrStartedOnce.Do(func() { close(xmrStarted) })
			<-ctx.Done()
			return
		}
		nonXMRBroadcast <- struct{}{}
	}
	batcher.AddObservedTx(xmrBarrierTestAttestation(common.XMRChain, "stalled-xmr", 100))

	releaseWriter := make(chan struct{})
	xmrCtx, cancelXMR := context.WithCancel(context.Background())
	defer cancelXMR()
	var releaseWriterOnce sync.Once
	releaseErrataWriter := func() {
		releaseWriterOnce.Do(func() { close(releaseWriter) })
	}
	t.Cleanup(releaseErrataWriter)
	xmrSendDone := make(chan struct{})
	go func() {
		batcher.sendBatches(xmrCtx, true)
		close(xmrSendDone)
	}()
	select {
	case <-xmrStarted:
	case err := <-decodeErr:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for stalled XMR broadcast")
	}

	writerAttempted := make(chan struct{})
	writerAcquired := make(chan struct{})
	writerDone := make(chan struct{})
	go func() {
		close(writerAttempted)
		batcher.xmrErrataBarrier.Lock()
		close(writerAcquired)
		<-releaseWriter
		batcher.xmrErrataBarrier.Unlock()
		close(writerDone)
	}()
	<-writerAttempted
	deadline := time.Now().Add(time.Second)
	for {
		if !batcher.xmrErrataBarrier.TryRLock() {
			break
		}
		batcher.xmrErrataBarrier.RUnlock()
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for errata writer to queue")
		}
		time.Sleep(time.Millisecond)
	}

	addDone := make(chan struct{})
	go func() {
		batcher.AddObservedTx(xmrBarrierTestAttestation(common.BTCChain, "independent-btc", 101))
		close(addDone)
	}()
	select {
	case <-addDone:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("XMR network I/O kept the batch mutex locked")
	}

	nonXMRSendDone := make(chan struct{})
	go func() {
		batcher.sendBatches(context.Background(), true)
		close(nonXMRSendDone)
	}()
	select {
	case <-nonXMRBroadcast:
	case err := <-decodeErr:
		require.NoError(t, err)
	case <-time.After(500 * time.Millisecond):
		t.Fatal("non-XMR batch waited behind the XMR errata barrier")
	}
	select {
	case <-nonXMRSendDone:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for independent non-XMR send")
	}

	cancelXMR()
	select {
	case <-xmrSendDone:
	case <-time.After(time.Second):
		t.Fatal("timed out stopping XMR send")
	}
	select {
	case <-writerAcquired:
	case <-time.After(time.Second):
		t.Fatal("errata writer did not acquire after XMR send stopped")
	}
	releaseErrataWriter()
	select {
	case <-writerDone:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for errata writer exit")
	}
}

func TestXMRBatchRevalidatesAfterErrataWriterWins(t *testing.T) {
	remote := peer.ID("peer-1")
	batcher := newXMRBarrierTestBatcher(t, time.Second, remote)
	xmrTxID := common.TxID("purged-before-dequeue")
	batcher.AddObservedTx(xmrBarrierTestAttestation(common.XMRChain, xmrTxID, 77))
	batcher.AddObservedTx(xmrBarrierTestAttestation(common.BTCChain, "surviving-btc", 78))

	inspectionReached := make(chan struct{})
	allowReader := make(chan struct{})
	var allowReaderOnce sync.Once
	allowBarrierReader := func() {
		allowReaderOnce.Do(func() { close(allowReader) })
	}
	t.Cleanup(allowBarrierReader)
	var inspectionOnce sync.Once
	batcher.beforeXMRBarrierRLock = func() {
		inspectionOnce.Do(func() { close(inspectionReached) })
		<-allowReader
	}
	payloads := make(chan []byte, 1)
	batcher.broadcastToPeerFn = func(_ context.Context, _ peer.ID, payload []byte) {
		payloads <- append([]byte(nil), payload...)
	}

	sendDone := make(chan struct{})
	go func() {
		batcher.sendBatches(context.Background(), true)
		close(sendDone)
	}()
	select {
	case <-inspectionReached:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for XMR batch inspection")
	}

	writerHeld := false
	t.Cleanup(func() {
		if writerHeld {
			batcher.xmrErrataBarrier.Unlock()
		}
	})
	batcher.xmrErrataBarrier.Lock()
	writerHeld = true
	batcher.removeObservedTxsForErrata(common.ErrataTx{
		Chain: common.XMRChain,
		Id:    xmrTxID,
	}, 77)
	allowBarrierReader()
	select {
	case <-payloads:
		t.Fatal("batch broadcast crossed cleanup while the errata writer held the barrier")
	case <-time.After(25 * time.Millisecond):
	}
	batcher.xmrErrataBarrier.Unlock()
	writerHeld = false

	var payload []byte
	select {
	case payload = <-payloads:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for revalidated batch")
	}
	var batch common.AttestationBatch
	require.NoError(t, batch.Unmarshal(payload))
	require.Len(t, batch.AttestTxs, 1)
	require.True(t, batch.AttestTxs[0].ObsTx.Tx.Chain.Equals(common.BTCChain))
	require.Equal(t, common.TxID("surviving-btc"), batch.AttestTxs[0].ObsTx.Tx.ID)
	select {
	case <-sendDone:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for revalidated batch send")
	}
}
