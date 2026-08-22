package reporter

import (
	"sync"
	"time"

	"github.com/rs/zerolog/log"
	"gitlab.com/thorchain/thornode/v3/common"
	. "gopkg.in/check.v1"
)

func (s *SolvencySuite) TestRunnerTickQueuesEveryConfiguredChain(c *C) {
	chains := map[common.Chain]ChainClient{
		common.BTCChain: &mockChainClient{chain: common.BTCChain},
		common.ETHChain: &mockChainClient{chain: common.ETHChain},
	}
	d := &dispatcher{
		logger:  log.Logger,
		stopper: make(chan struct{}),
		chains:  chains,
		chainTaskQueues: map[common.Chain]chan reportTask{
			common.BTCChain: make(chan reportTask, 1),
			common.ETHChain: make(chan reportTask, 1),
		},
	}

	d.processRunnerTick()

	for chain, queue := range d.chainTaskQueues {
		c.Assert(queue, HasLen, 1, Commentf("chain=%s", chain))
		task := <-queue
		c.Assert(task.from, Equals, fromRunner)
		c.Assert(task.height, Equals, int64(0))
	}
}

func (s *SolvencySuite) TestRunnerTickSkipsInactiveChainClients(c *C) {
	chains := map[common.Chain]ChainClient{
		common.BTCChain: &mockChainClient{chain: common.BTCChain},
		common.XMRChain: &mockChainClient{chain: common.XMRChain},
	}
	d := &dispatcher{
		logger:  log.Logger,
		stopper: make(chan struct{}),
		chains:  chains,
		chainTaskQueues: map[common.Chain]chan reportTask{
			common.BTCChain: make(chan reportTask, 1),
			common.XMRChain: make(chan reportTask, 1),
		},
		chainActiveCheck: func(chain common.Chain) bool {
			return chain != common.XMRChain
		},
	}

	d.processRunnerTick()

	c.Assert(d.chainTaskQueues[common.BTCChain], HasLen, 1)
	c.Assert(d.chainTaskQueues[common.XMRChain], HasLen, 0)
}

func (s *SolvencySuite) TestSharedRunnerStopsCleanly(c *C) {
	stopper := make(chan struct{})
	wg := &sync.WaitGroup{}
	d := &dispatcher{
		logger:  log.Logger,
		stopper: stopper,
		wg:      wg,
	}

	wg.Go(d.runner)
	close(stopper)
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		c.Fatal("shared reporter runner did not stop")
	}
}
