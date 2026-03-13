package reporter

import (
	"context"
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

// SolvencyAttester submits solvency reports through the observer attestation path.
// The unified reporter uses it to preserve the previous supermajority-gated
// observer behavior before a MsgSolvency reaches THORChain.
type SolvencyAttester interface {
	AttestSolvency(ctx context.Context, solvency common.Solvency) error
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
	Collect(from triggeredFrom, height int64) (collectedMsgs ReportBatch, err error)
	ShouldReport(from triggeredFrom, height int64) bool
}

type Reporters []Reporter

type baseReporter struct {
	logger          zerolog.Logger
	client          ChainClient
	thorchainBridge ThorchainBridge
}
