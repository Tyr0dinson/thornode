package blockedaddrmanager

import (
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"

	"gitlab.com/thorchain/thornode/v3/bifrost/metrics"
	"gitlab.com/thorchain/thornode/v3/constants"
)

type BlockedAddrManager interface {
	IsBlocked(address string) bool
}

type BlockedAddrMgr struct {
	rwMutex    *sync.RWMutex
	logger     zerolog.Logger
	errCounter *prometheus.CounterVec
	m          *metrics.Metrics
	stopChan   chan struct{}
	blocked    map[string]bool
}

func NewBlockedAddrMgr(m *metrics.Metrics) *BlockedAddrMgr {
	return &BlockedAddrMgr{
		logger:     log.With().Str("module", "blocked_addr_mgr").Logger(),
		errCounter: m.GetCounterVec(metrics.BlockedAddrManagerError),
		m:          m,
		stopChan:   make(chan struct{}),
		rwMutex:    &sync.RWMutex{},
	}
}

func (bam *BlockedAddrMgr) Start() error {
	defer bam.logger.Info().Msg("blocked address manager started")
	go bam.update()
	return nil
}

func (bam *BlockedAddrMgr) Stop() error {
	defer bam.logger.Info().Msg("blocked address manager stopped")
	close(bam.stopChan)
	return nil
}

func (bam *BlockedAddrMgr) IsBlocked(addr string) bool {
	bam.rwMutex.RLock()
	defer bam.rwMutex.RUnlock()

	_, ok := bam.blocked[addr]
	return ok
}

func (bam *BlockedAddrMgr) update() {
	for {
		select {
		case <-bam.stopChan:
			return
		case <-time.After(constants.ThorchainBlockTime):
			bam.fetch()
		}
	}
}

func (bam *BlockedAddrMgr) fetch() {
}
