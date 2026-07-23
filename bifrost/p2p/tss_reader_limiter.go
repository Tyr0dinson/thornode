package p2p

import (
	"sync"

	"github.com/libp2p/go-libp2p-core/peer"
)

// tssReaderLimiter bounds work before a frame's type and ceremony can be
// authenticated. The per-peer ceiling prevents one globally authorized
// validator from occupying every reader with slow or truncated headers.
type tssReaderLimiter struct {
	lock       sync.Mutex
	maxTotal   uint32
	maxPerPeer uint32
	total      uint32
	perPeer    map[peer.ID]uint32
}

func newTSSReaderLimiter(maxTotal, maxPerPeer uint32) *tssReaderLimiter {
	return &tssReaderLimiter{
		maxTotal:   maxTotal,
		maxPerPeer: maxPerPeer,
		perPeer:    make(map[peer.ID]uint32),
	}
}

func (l *tssReaderLimiter) acquire(id peer.ID) bool {
	if l == nil || id == "" {
		return false
	}
	l.lock.Lock()
	defer l.lock.Unlock()
	if (l.maxTotal > 0 && l.total >= l.maxTotal) ||
		(l.maxPerPeer > 0 && l.perPeer[id] >= l.maxPerPeer) {
		return false
	}
	l.total++
	l.perPeer[id]++
	return true
}

func (l *tssReaderLimiter) release(id peer.ID) {
	if l == nil || id == "" {
		return
	}
	l.lock.Lock()
	defer l.lock.Unlock()
	if l.perPeer[id] == 0 {
		return
	}
	l.perPeer[id]--
	l.total = saturatingSubtract32(l.total, 1)
	if l.perPeer[id] == 0 {
		delete(l.perPeer, id)
	}
}

func (l *tssReaderLimiter) snapshot() (uint32, map[peer.ID]uint32) {
	if l == nil {
		return 0, nil
	}
	l.lock.Lock()
	defer l.lock.Unlock()
	perPeer := make(map[peer.ID]uint32, len(l.perPeer))
	for id, count := range l.perPeer {
		perPeer[id] = count
	}
	return l.total, perPeer
}
