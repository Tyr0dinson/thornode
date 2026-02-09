package reporter

import (
	"fmt"
	"math/big"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/rs/zerolog"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/config"
)

// alias
type (
	ThorchainBridge = thorclient.ThorchainBridge
)

// ChainClient is the minimal client interface needed by the reporter package.
type ChainClient interface {
	GetHeight() (int64, error)
	GetBlockScannerHeight() (int64, error)
	GetGasEstimateForSolvency() cosmos.Uint
	GetAccount(poolPubKey common.PubKey, height *big.Int) (common.Account, error)
	GetChain() common.Chain
	GetConfig() config.BifrostChainConfiguration
	IsBlockScannerHealthy() bool
}

type Dispatcher interface {
	Process(chain common.Chain, height int64)
}

type ReportBatch []sdk.Msg

type triggeredFrom uint8

const (
	_ triggeredFrom = iota
	fromBlockScanner
	fromRunner
)

func (from triggeredFrom) String() string {
	switch from {
	case fromBlockScanner:
		return "block_scanner"
	case fromRunner:
		return "runner"
	default:
		return "unknown"
	}
}

type Reporter interface {
	String() string
	Collect(from triggeredFrom) (collectedMsgs ReportBatch, err error)
	ShouldReport(from triggeredFrom, height int64) bool
}

type Reporters []Reporter

type baseReporter struct {
	logger          zerolog.Logger
	client          ChainClient
	thorchainBridge ThorchainBridge
}

func (r baseReporter) getRelevantHeight(from triggeredFrom) (int64, error) {
	if from == fromRunner {
		chainHeight, err := r.client.GetHeight()
		if err != nil {
			return -1, fmt.Errorf("fail to get chain height: %w", err)
		}
		return chainHeight, nil
	} else {
		fetchHeight, err := r.client.GetBlockScannerHeight()
		if err != nil {
			return -1, fmt.Errorf("fail to get fetch (blockscanner) height: %w", err)
		}
		return fetchHeight, nil
	}
}
