// Package blocktime holds THORChain's block time as Bifrost last read it
// (block_time_ms on /thorchain/constants), for the polls, tickers and caches
// that run about once per THORChain block. It changes with the BLOCKTIMEMS
// mimir and is the network's default ThorchainBlockTime while that is unset.
package blocktime

import (
	"sync"
	"sync/atomic"
	"time"

	"gitlab.com/thorchain/thornode/v3/constants"
)

var blockMs atomic.Int64

// Get returns the THORChain block time Bifrost last read, or the network's
// default ThorchainBlockTime before the first read.
func Get() time.Duration {
	if ms := blockMs.Load(); ms > 0 {
		return time.Duration(ms) * time.Millisecond
	}
	return constants.ThorchainBlockTime
}

// Set records a block time read from THORChain, in milliseconds. A value of
// zero or less is ignored.
func Set(ms int64) {
	if ms > 0 {
		blockMs.Store(ms)
	}
}

// Ticker delivers ticks about once per THORChain block, re-reading the block
// time before each one, so it follows a block time change. Like time.Ticker
// it drops ticks for a slow receiver.
type Ticker struct {
	C    <-chan time.Time
	stop chan struct{}
	once sync.Once
}

// NewTicker returns a Ticker whose first tick comes one block time from now.
func NewTicker() *Ticker {
	c := make(chan time.Time, 1)
	t := &Ticker{C: c, stop: make(chan struct{})}
	go func() {
		for {
			timer := time.NewTimer(Get())
			select {
			case now := <-timer.C:
				select {
				case c <- now:
				default:
				}
			case <-t.stop:
				timer.Stop()
				return
			}
		}
	}()
	return t
}

// Stop turns off the ticker. No more ticks are sent after it returns.
func (t *Ticker) Stop() {
	t.once.Do(func() { close(t.stop) })
}
