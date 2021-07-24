package observer

import (
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"gitlab.com/thorchain/thornode/bifrost/pkg/chainclients"
	"gitlab.com/thorchain/thornode/bifrost/pubkeymanager"
	"gitlab.com/thorchain/thornode/common"
)

// ClientSolvencyChecker is to make sure that asgard is solvent
type ClientSolvencyChecker struct {
	logger    zerolog.Logger
	pubkeyMgr *pubkeymanager.PubKeyManager
	chains    map[common.Chain]chainclients.ChainClient
	storage   *ObserverStorage
}

// NewClientSolvencyChecker create a new instance of client solvency checker
func NewClientSolvencyChecker(pubkeyMgr *pubkeymanager.PubKeyManager,
	chains map[common.Chain]chainclients.ChainClient,
	storage *ObserverStorage) *ClientSolvencyChecker {
	return &ClientSolvencyChecker{
		logger:    log.Logger.With().Str("module", "solvency_checker").Logger(),
		pubkeyMgr: pubkeyMgr,
		chains:    chains,
		storage:   storage,
	}
	pubkeyMgr.RegisterCallback()
}

func()
