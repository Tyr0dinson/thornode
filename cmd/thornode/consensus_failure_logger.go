package main

import (
	"context"
	"os"

	"cosmossdk.io/log/v2"

	"gitlab.com/thorchain/thornode/v3/app/divergence"
)

const (
	// consensusFailureMsg is the exact log message emitted by CometBFT's
	// receiveRoutine when it recovers from a consensus panic.
	consensusFailureMsg = "CONSENSUS FAILURE!!!"

	// blocksyncValidationMsg is the exact log message CometBFT's blocksync
	// reactor (poolRoutine) emits, with keyval "err", when a fetched block fails
	// validation; it then drops the peers and retries.
	blocksyncValidationMsg = "Error in validation"
)

// exitProcess is os.Exit, replaceable in tests.
var exitProcess = os.Exit

// ConsensusFailureLogger wraps a cosmossdk.io/log/v2.Logger and monitors for
// CometBFT consensus failure log messages. When CometBFT's consensus
// receiveRoutine catches a panic, it logs "CONSENSUS FAILURE!!!" and silently
// returns — leaving the process alive but unable to participate in consensus.
// This wrapper detects that message and terminates the process so that
// container orchestration can restart the node.
type ConsensusFailureLogger struct {
	log.Logger
}

// NewConsensusFailureLogger returns a logger that exits the process when a
// CometBFT consensus failure is detected.
func NewConsensusFailureLogger(inner log.Logger) ConsensusFailureLogger {
	return ConsensusFailureLogger{Logger: inner}
}

func (l ConsensusFailureLogger) Error(msg string, keyvals ...interface{}) {
	// Always log the message first so operators see the full error.
	l.Logger.Error(msg, keyvals...)
	l.handleError(msg, keyvals)
}

func (l ConsensusFailureLogger) ErrorContext(ctx context.Context, msg string, keyvals ...any) {
	// Always log the message first so operators see the full error.
	l.Logger.ErrorContext(ctx, msg, keyvals...)
	l.handleError(msg, keyvals)
}

// handleError reacts to the CometBFT failure messages after they are logged.
func (l ConsensusFailureLogger) handleError(msg string, keyvals []interface{}) {
	switch msg {
	case consensusFailureMsg:
		// In-memory state is lost at exit, so write the divergence bundle
		// first (bounded; a no-op when diagnostics are disabled).
		divergence.HandleConsensusFailure(divergence.ErrFromKeyvals(keyvals), divergence.DefaultDumpTimeout)

		// The consensus goroutine is dead. Other goroutines (gRPC, API, P2P)
		// keep the process alive but the node can no longer produce or validate
		// blocks. Exit so the container restarts.
		exitProcess(1)
	case blocksyncValidationMsg:
		// Bundle once per height in the background when the error is a
		// header hash mismatch; blocksync keeps retrying as before.
		divergence.HandleBlocksyncError(divergence.ErrFromKeyvals(keyvals))
	}
}

func (l ConsensusFailureLogger) With(keyvals ...interface{}) log.Logger {
	return NewConsensusFailureLogger(l.Logger.With(keyvals...))
}

func (l ConsensusFailureLogger) Impl() interface{} {
	return l.Logger.Impl()
}
