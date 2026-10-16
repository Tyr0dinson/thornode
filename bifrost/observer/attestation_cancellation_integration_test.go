package observer

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"

	"gitlab.com/thorchain/thornode/v3/bifrost/p2p"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/ebifrost"
	thortypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// The XMR delivery deadline starts before the handler takes s.mu, and the
// attestation is dropped if that deadline has already passed once the lock is
// held. A 50ms budget was shorter than scheduler/GC stalls on a loaded CI
// runner, so the import was sometimes never attempted. Keep the budget well
// above such stalls, and make the slow authorization outlast it so the
// deadline has really expired by the time authorization returns.
const (
	slowAuthDeliveryTimeout = 500 * time.Millisecond
	slowAuthDelay           = slowAuthDeliveryTimeout + 250*time.Millisecond
	slowAuthImportWait      = 5 * time.Second
)

func TestStateCancellationPreservesOrdinaryXMRSlowAuthorization(t *testing.T) {
	for _, delay := range []time.Duration{0, slowAuthDelay} {
		t.Run(delay.String(), func(t *testing.T) {
			s, _, _, _, legacy, _ := setupTestGossip(t)
			s.config.PeerTimeout = slowAuthDeliveryTimeout
			tx, plan := observerTestXMRPlan(t)
			key := secp256k1.GenPrivKey()
			vault := observerTestVaultFunc(tx.ObservedPubKey, observerTestPubKey(t, key))
			legacy.getVaultFunc = func(pk string) (thortypes.Vault, error) {
				// Authorization runs after the delivery critical section. The
				// delivery deadline must not discard a later successful import.
				time.Sleep(delay)
				return vault(pk)
			}
			legacy.getKeysignFunc = observerTestKeysignFunc(plan)
			imported := make(chan struct{}, 1)
			s.SetObserverApplySpentOutputRefs(func(_ spentOutputRefsImport, outcome func(bool)) {
				outcome(true)
				imported <- struct{}{}
			})
			batch := signedCancellationBatch(t, tx, key)
			s.handleObservedTxAttestation(context.Background(), common.AttestTx{ObsTx: tx, Attestation: batch.QuoTxs[0].Attestations[0]})
			select {
			case <-imported:
			case <-time.After(slowAuthImportWait):
				t.Fatal("ordinary delivery discarded a successful authorization after its delivery deadline")
			}
			state, exists := observerTestConflictState(s, tx)
			require.True(t, exists)
			require.Equal(t, xmrSpentRefsImportConfirmed, state)
		})
	}
}

func TestStateCancellationXMRSlowAuthorizationUsesTransferDeadline(t *testing.T) {
	for _, delay := range []time.Duration{0, slowAuthDelay} {
		t.Run(delay.String(), func(t *testing.T) {
			s, _, _, _, legacy, _ := setupTestGossip(t)
			s.config.PeerTimeout = slowAuthDeliveryTimeout
			tx, plan := observerTestXMRPlan(t)
			key := secp256k1.GenPrivKey()
			vault := observerTestVaultFunc(tx.ObservedPubKey, observerTestPubKey(t, key))
			legacy.getKeysignFunc = observerTestKeysignFunc(plan)
			var queryErr error
			s.bridge = &cancellationQueryBridge{MockThorchainBridge: legacy, vault: func(ctx context.Context, pk string) (thortypes.Vault, error) {
				timer := time.NewTimer(delay)
				defer timer.Stop()
				select {
				case <-timer.C:
					return vault(pk)
				case <-ctx.Done():
					queryErr = ctx.Err()
					return thortypes.Vault{}, queryErr
				}
			}}
			imported := make(chan struct{}, 1)
			s.SetObserverApplySpentOutputRefs(func(_ spentOutputRefsImport, outcome func(bool)) {
				outcome(true)
				imported <- struct{}{}
			})
			transfer, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			require.NoError(t, s.processAttestationStateBatchWithContext(transfer, s.logger, signedCancellationBatch(t, tx, key)))
			require.NoError(t, transfer.Err())
			require.NoError(t, queryErr, "authorization must use the live transfer's budget")
			select {
			case <-imported:
			case <-time.After(slowAuthImportWait):
				t.Fatal("live state transfer discarded a successful authorization after the delivery deadline")
			}
			state, exists := observerTestConflictState(s, tx)
			require.True(t, exists)
			require.Equal(t, xmrSpentRefsImportConfirmed, state)
		})
	}
}

func TestStateCancellationWhileErrataBarrierLocked(t *testing.T) {
	s, _, _, _, _, _ := setupTestGossip(t)
	tx, _ := observerTestXMRPlan(t)
	batch := signedCancellationBatch(t, tx, secp256k1.GenPrivKey())
	msg := common.AttestTx{ObsTx: tx, Attestation: batch.QuoTxs[0].Attestations[0]}
	s.xmrErrataBarrier.Lock()
	var unlockOnce sync.Once
	unlock := func() { unlockOnce.Do(s.xmrErrataBarrier.Unlock) }
	defer unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	done := make(chan struct{})
	go func() {
		s.handleObservedTxAttestationFromStateSync(ctx, msg)
		close(done)
	}()
	select {
	case <-done:
		require.ErrorIs(t, ctx.Err(), context.DeadlineExceeded)
	case <-time.After(time.Second):
		unlock()
		waitStateBounds(t, done)
		t.Fatal("cancelled state import waited for errata cleanup")
	}
	require.Empty(t, s.observedTxs)
	unlock()
	// The cancelled reader must neither retain the barrier nor poison a retry.
	require.NoError(t, s.processAttestationStateBatchWithContext(context.Background(), s.logger, batch))
	require.Equal(t, 1, s.observedTxs[observedTxKey(tx, false, false)].AttestationCount())
}

func TestStateCancellationWhileFrameRevalidationLocked(t *testing.T) {
	s, _, _, _, _, _ := setupTestGossip(t)
	tx, _ := observerTestXMRPlan(t)
	batch := signedCancellationBatch(t, tx, secp256k1.GenPrivKey())
	stream := NewAutoResponderStream("state-peer")
	s.mu.Lock()
	var unlockOnce sync.Once
	unlock := func() { unlockOnce.Do(s.mu.Unlock) }
	defer unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	deadline, ok := ctx.Deadline()
	require.True(t, ok)
	done := make(chan error, 1)
	go func() { done <- s.sendAttestationStateBatchWithContext(ctx, stream, 0, batch, deadline) }()
	select {
	case err := <-done:
		require.ErrorIs(t, err, context.DeadlineExceeded)
	case <-time.After(time.Second):
		unlock()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("frame export did not exit after unlocking")
		}
		t.Fatal("cancelled frame export waited for the global mutex")
	}
	unlock()
	require.NoError(t, s.sendAttestationStateBatch(NewAutoResponderStream("retry-peer"), 0, batch, time.Now().Add(time.Second)))
}

func TestStateCancellationKeepsOrdinaryLocksBlocking(t *testing.T) {
	for _, kind := range []string{"network fee", "XMR observation"} {
		t.Run(kind, func(t *testing.T) {
			s, _, _, _, _, _ := setupTestGossip(t)
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			fee, atts := stateSyncSignedFee(t, 123, 1)
			tx, _ := observerTestXMRPlan(t)
			batch := signedCancellationBatch(t, tx, secp256k1.GenPrivKey())
			s.mu.Lock()
			var unlockOnce sync.Once
			unlock := func() { unlockOnce.Do(s.mu.Unlock) }
			defer unlock()
			started, done := make(chan struct{}), make(chan struct{})
			go func() {
				close(started)
				if kind == "network fee" {
					s.handleNetworkFeeAttestation(ctx, common.AttestNetworkFee{NetworkFee: fee, Attestation: atts[0]})
				} else {
					// The XMR handler adds a timeout, which must not select the polling path.
					s.handleObservedTxAttestation(ctx, common.AttestTx{ObsTx: tx, Attestation: batch.QuoTxs[0].Attestations[0]})
				}
				close(done)
			}()
			waitStateBounds(t, started)
			select {
			case <-done:
				t.Fatal("ordinary processing abandoned its blocking lock wait")
			case <-time.After(50 * time.Millisecond):
			}
			unlock()
			waitStateBounds(t, done)
			if kind == "network fee" {
				require.NotNil(t, s.networkFees[*fee])
				require.Equal(t, 1, s.networkFees[*fee].AttestationCount())
			} else {
				require.Empty(t, s.observedTxs, "cancelled XMR delivery must not add observations after acquiring the lock")
			}
		})
	}
}

// A compatible sender waits on the exchange deadline for the next header ACK.
// S14 must not read that header or retain its batch while the first import runs.
func TestStateCancellationBoundedReceiverWaitsForProcessing(t *testing.T) {
	for _, finish := range []string{"success", "cancel"} {
		t.Run(finish, func(t *testing.T) {
			stateSyncDeadlineTestSettings(t)
			receiver, _, _, rpc, _, _ := setupTestGossip(t)
			receiver.logger = zerolog.Nop()
			receiver.config.StateSyncTimeout = 10 * time.Second
			key := secp256k1.GenPrivKey()
			receiver.setActiveValidators(common.PubKeys{observerTestPubKey(t, key)})
			started, exited, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
			second := make(chan struct{})
			var releaseOnce sync.Once
			unblock := func() { releaseOnce.Do(func() { close(release) }) }
			defer unblock()
			rpc.sendQuorumNetworkFeeFunc = func(ctx context.Context, fee *common.QuorumNetworkFee, _ ...grpc.CallOption) (*ebifrost.SendQuorumNetworkFeeResult, error) {
				if fee.NetworkFee.Height == 100 {
					close(started)
					defer close(exited)
					select {
					case <-release:
					case <-ctx.Done():
						return nil, ctx.Err()
					}
				} else {
					close(second)
				}
				return &ebifrost.SendQuorumNetworkFeeResult{}, nil
			}
			server, client := stateSyncDeadlinePair(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan attestationStateTransferResult, 1)
			returned := make(chan struct{})
			go func() { defer close(returned); done <- receiver.askStreamForStateWithContext(ctx, client) }()
			t.Cleanup(func() { cancel(); unblock(); _ = server.transport.Close(); waitStateBounds(t, returned) })
			read := func(expected []byte) {
				message, err := p2p.ReadStreamWithBuffer(server)
				require.NoError(t, err)
				require.Equal(t, expected, message)
			}
			write := func(data []byte) { require.NoError(t, p2p.WriteStreamWithBuffer(data, server)) }
			read(prefixSendState)
			write(append([]byte{prefixBatchBegin[0]}, stateBoundsCount(2)...))
			read([]byte(streamAckBegin))
			for i := range 2 {
				fee := &common.NetworkFee{Chain: common.BTCChain, Height: int64(100 + i), TransactionSize: 1, TransactionRate: 1}
				payload, err := fee.GetSignablePayload()
				require.NoError(t, err)
				signature, err := key.Sign(payload)
				require.NoError(t, err)
				batch := common.QuorumState{QuoNetworkFees: []*common.QuorumNetworkFee{{NetworkFee: fee, Attestations: []*common.Attestation{{PubKey: key.PubKey().Bytes(), Signature: signature}}}}}
				data, err := batch.Marshal()
				require.NoError(t, err)
				write(stateBoundsHeader(uint32(i), uint32(len(data))))
				if i == 0 {
					read([]byte(streamAckHeader))
				} else {
					ack := make(chan error, 1)
					go func() {
						message, readErr := p2p.ReadStreamWithBufferLimitAndDeadline(server, uint32(len(streamAckHeader)), time.Now().Add(8*time.Second))
						if readErr == nil && string(message) != streamAckHeader {
							readErr = fmt.Errorf("unexpected header acknowledgement: %q", message)
						}
						ack <- readErr
					}()
					select {
					case err := <-ack:
						t.Fatalf("next header advanced while the first batch still owned processing: %v", err)
					case <-time.After(2 * stateSyncTestFrameTimeout):
					}
					select {
					case <-second:
						t.Fatal("a second import started behind the blocked first batch")
					default:
					}
					if finish == "cancel" {
						cancel()
					} else {
						unblock()
					}
					select {
					case err := <-ack:
						if finish == "cancel" {
							require.Error(t, err)
						} else {
							require.NoError(t, err)
						}
					case <-time.After(2 * time.Second):
						t.Fatal("header wait did not settle after processing or cancellation")
					}
					if finish == "cancel" {
						break
					}
				}
				write(append([]byte{prefixBatchData[0]}, data...))
				read([]byte(streamAckData))
				if i == 0 {
					waitStateBounds(t, started)
				}
			}
			wantItems := 1
			if finish == "success" {
				wantItems = 2
				write(prefixBatchEnd)
				read([]byte(p2p.StreamMsgDone))
				waitStateBounds(t, second)
			}
			select {
			case result := <-done:
				require.Equal(t, finish == "success", result.completed)
				require.Equal(t, wantItems, result.items)
			case <-time.After(2 * time.Second):
				t.Fatal("bounded receiver did not finish its owned processing")
			}
			waitStateBounds(t, exited)
			require.Len(t, receiver.networkFees, wantItems)
		})
	}
}
