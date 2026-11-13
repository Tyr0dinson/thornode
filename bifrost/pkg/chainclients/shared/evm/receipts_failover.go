package evm

import (
	"fmt"
	"sync"

	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/endpoints"
)

// ReceiptsFailover moves an ordinary block scan off an active endpoint that serves
// blocks, headers and its health probes but not the receipts of the scanned block (a
// provider whose receipt reads go to a broken or lagging backend). Its answers are
// well-formed, so the router credits them and inline errors never evict it: without
// this every retry of the block would read the receipts from it again while another
// endpoint has them. A reorg rescan sidelines its witness the same way
// (SidelineWitness).
//
// One failure is not enough: a load balancer that answered from a node one block
// behind, or a reorg at the tip between the block read and the receipts read, fails
// once and is gone on the retry, which reads the block again. The second failure in a
// row of the same height from the same active endpoint sidelines it (Manager.Sideline:
// cooldown and handoff), so the next retry reads the block and its receipts from the
// next endpoint. With a single endpoint there is nowhere to move: nothing changes.
type ReceiptsFailover struct {
	mu     sync.Mutex
	height int64
	source string
}

// ReceiptsSource names the endpoint an unpinned read of a block's receipts goes to
// first when another endpoint could take over: "" with fewer than two endpoints, when
// the active endpoint is not eligible (the router then picks by priority), or when no
// other endpoint is eligible (sidelining would only pause the active one).
func ReceiptsSource(mgr *endpoints.Manager) string {
	if mgr == nil || mgr.Len() < 2 {
		return ""
	}
	active := mgr.Active()
	if !active.Eligible() {
		return ""
	}
	for _, e := range mgr.Entries() {
		if e != active && e.Eligible() {
			return active.Name
		}
	}
	return ""
}

// Failed records that the receipts of the block at height could not be read, with
// source (ReceiptsSource taken before the read) the endpoint they were asked from.
func (f *ReceiptsFailover) Failed(mgr *endpoints.Manager, source string, height int64) {
	if mgr == nil || source == "" {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if mgr.Active().Name != source {
		// the active role moved during the read (a rate limit): the failure may be
		// the new endpoint's first answer, so nothing is judged yet
		f.height, f.source = 0, ""
		return
	}
	if f.height == height && f.source == source {
		f.height, f.source = 0, ""
		mgr.Sideline(source, fmt.Sprintf("cannot serve the receipts of block %d", height))
		return
	}
	f.height, f.source = height, source
}

// Served clears the record once a block's receipts were read.
func (f *ReceiptsFailover) Served() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.height, f.source = 0, ""
}
