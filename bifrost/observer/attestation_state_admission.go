package observer

import (
	"context"
	"time"

	"github.com/libp2p/go-libp2p-core/network"
	"github.com/libp2p/go-libp2p-core/peer"
)

const (
	// At ~200us/signature, ten transfers of 10,000 * 128 attestations need
	// ~22 minutes of signature CPU on two effective cores. Allow 30 minutes
	// for an exchange, independently of the single-message timeout. This is
	// operational headroom, not a bound on accumulated-state scans or RPCs.
	// Ordinary frame deadlines still interrupt stalled peers; ACKs that wait
	// for processing can retain the snapshot slot until the exchange expires.
	defaultStateSyncTimeout = 30 * time.Minute

	maxConcurrentAttestationStateRequests  = 10
	maxConcurrentAttestationStateSnapshots = 2
	maxAttestationStateBatches             = 1000
	maxAttestationStateItemsPerTransfer    = 10000
	maxAttestationStateAttestationsPerItem = 128
)

// admitAttestationState takes no waiting slot: the authenticated peer must be
// active now, and its one slot counts against the fixed global limit. Hold
// membership and admission locks together so churn cannot race admission.
func (s *AttestationGossip) admitAttestationState(stream network.Stream) bool {
	remote := stream.Conn().RemotePeer()
	s.avMu.Lock()
	defer s.avMu.Unlock()
	s.stateRequestMu.Lock()
	defer s.stateRequestMu.Unlock()
	if !s.activeVals[remote] || s.stateRequestsStopped ||
		len(s.stateRequests) >= maxConcurrentAttestationStateRequests || s.stateRequests[remote] != nil {
		return false
	}
	if s.stateRequests == nil {
		s.stateRequests = make(map[peer.ID]network.Stream)
	}
	s.stateRequests[remote] = stream
	return true
}

// Per-peer admission precedes this limit, so one stalled peer cannot own both
// snapshot slots. Only the handler that acquired a slot releases it.
func (s *AttestationGossip) acquireAttestationStateSnapshot() bool {
	for active := s.stateSnapshotsActive.Load(); active < maxConcurrentAttestationStateSnapshots; active = s.stateSnapshotsActive.Load() {
		if s.stateSnapshotsActive.CompareAndSwap(active, active+1) {
			return true
		}
	}
	return false
}

// Only the admitted handler's exit removes its slot; timeout and shutdown
// reset I/O but never release capacity while that handler still owns work.
func (s *AttestationGossip) releaseAttestationState(remote peer.ID) {
	s.stateRequestMu.Lock()
	defer s.stateRequestMu.Unlock()
	delete(s.stateRequests, remote)
}

func (s *AttestationGossip) stopAttestationStateRequests() {
	s.stateRequestMu.Lock()
	s.stateRequestsStopped = true
	streams := make([]network.Stream, 0, len(s.stateRequests))
	for _, stream := range s.stateRequests {
		streams = append(streams, stream)
	}
	s.stateRequestMu.Unlock()
	for _, stream := range streams {
		_ = stream.Reset()
	}
}

func (s *AttestationGossip) stateSyncTimeout() time.Duration {
	if s.config.StateSyncTimeout > 0 {
		return s.config.StateSyncTimeout
	}
	return defaultStateSyncTimeout
}

// One absolute budget covers transport, processing and queries. Reset interrupts
// caller-owned stream I/O; processing and lock waits use the same context.
func (s *AttestationGossip) stateExchangeContext(parent context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(parent, s.stateSyncTimeout())
}

func (s *AttestationGossip) stateExchangeDeadline(parent context.Context, stream network.Stream) (context.Context, context.CancelFunc) {
	ctx, cancel := s.stateExchangeContext(parent)
	stop := context.AfterFunc(ctx, func() { _ = stream.Reset() })
	return ctx, func() { stop(); cancel() }
}
