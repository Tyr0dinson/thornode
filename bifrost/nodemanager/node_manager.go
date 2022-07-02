package nodemanager

import (
	"sync"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"

	"gitlab.com/thorchain/thornode/bifrost/thorclient"
	"gitlab.com/thorchain/thornode/constants"
	"gitlab.com/thorchain/thornode/x/thorchain/types"
)

// NodeManager manager an always up to date active node accounts
type NodeManager struct {
	bridge   *thorclient.ThorchainBridge
	nodes    types.NodeAccounts
	rwMutex  *sync.RWMutex
	logger   zerolog.Logger
	stopChan chan struct{}
}

// NewNodeManager create a new instance of NodeManager
func NewNodeManager(bridge *thorclient.ThorchainBridge) (*NodeManager, error) {
	return &NodeManager{
		logger:   log.With().Str("module", "node_mgr").Logger(),
		bridge:   bridge,
		stopChan: make(chan struct{}),
		rwMutex:  &sync.RWMutex{},
	}, nil
}

// Start to poll node accounts from thorchain
func (nm *NodeManager) Start() error {
	nm.fetchNodeAccounts()
	go nm.updateNodeAccounts()
	return nil
}

// Stop node manager
func (nm *NodeManager) Stop() error {
	defer nm.logger.Info().Msg("node manager stopped")
	close(nm.stopChan)
	return nil
}

// GetNodeAccounts return all the node accounts managed by this NodeManager
func (nm *NodeManager) GetNodeAccounts() types.NodeAccounts {
	nm.rwMutex.RLock()
	defer nm.rwMutex.RUnlock()
	return nm.nodes
}

func (nm *NodeManager) updateNodeAccounts() {
	nm.logger.Info().Msg("start to update node accounts")
	defer nm.logger.Info().Msg("stop to update node accounts")
	for {
		select {
		case <-nm.stopChan:
			return
		case <-time.After(constants.ThorchainBlockTime):
			if err := nm.fetchNodeAccounts(); err != nil {
				nm.logger.Error().Err(err).Msg("fail to get node accounts from THORChain")
			}
		}
	}
}

// fetchNodeAccounts from THORChain
func (nm *NodeManager) fetchNodeAccounts() error {
	nodes, err := nm.bridge.GetNodeAccounts()
	if err != nil {
		return err
	}
	active := make(types.NodeAccounts, 0)
	for _, node := range *nodes {
		if node.Status == types.NodeStatus_Active {
			active = append(active, node)
		}
	}
	nm.nodes = active
	return nil
}
