package utxo

import (
	"gitlab.com/thorchain/thornode/v3/bifrost/metrics"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/utxo/rpc"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
)

type instantSendLockStatus uint8

const (
	instantSendUnknown instantSendLockStatus = iota
	instantSendUnlocked
	instantSendLocked
)

type instantSendMempoolEntry struct {
	time int64
	lock instantSendLockStatus
}

// instantSendBackend contains native chain reads and proofs. It never owns the
// allowance journal or writes observation markers; those belong to the lifecycle.
// Transaction results preserve input order and per-ID errors, including not-found.
type instantSendBackend interface {
	mempool() (map[string]instantSendMempoolEntry, error)
	transaction(string) (*rpc.TxResult, error)
	transactions([]string) ([]*rpc.TxResult, []error, error)
	scan() ([]types.TxIn, error)
	outbounds(int64) (types.TxIn, error)
	snapshotAmount(*rpc.TxResult) (uint64, error)
	// getChainLockedBlockTxIDs verifies inclusion and returns all transaction IDs
	// from the active ChainLocked block. Reservation updates belong to the caller.
	getChainLockedBlockTxIDs(string, *rpc.TxResult) ([]string, error)
	// checkClaimFinality rechecks finality and identifies vault senders. The
	// handler applies halt, amount and reservation rules separately.
	checkClaimFinality(*types.TxInItem, *rpc.TxResult) (valid, fromVault bool, err error)
}

type instantSendMetrics struct {
	observed, deferred, reserved, available, pending, warming metrics.MetricName
}

type instantSendPolicy struct {
	enabled, minimum, maximum string
	metrics                   instantSendMetrics
}

// initInstantSend is the only chain selection point for the shared lifecycle.
// Call before starting workers; the handler must not move after its backend binds.
func (c *Client) initInstantSend() {
	h := &c.instantSend
	*h = instantSendHandler{client: c, enabled: c.cfg.InstantSend.Enabled}
	switch c.cfg.ChainID {
	case common.DASHChain:
		h.backend = dashInstantSendBackend{h}
		h.policy = instantSendPolicy{
			enabled: DashInstantSendEnabledMimirKey,
			minimum: DashInstantSendMinAmountMimirKey,
			maximum: DashInstantSendMaxAmountMimirKey,
		}
	case common.FIROChain:
		h.backend = firoInstantSendBackend{h}
		h.policy = instantSendPolicy{
			enabled: FiroInstantSendEnabledMimirKey,
			minimum: FiroInstantSendMinAmountMimirKey,
			maximum: FiroInstantSendMaxAmountMimirKey,
		}
	default:
		return
	}
	h.policy.metrics = instantSendMetrics{
		observed:  metrics.InstantSendObserved(c.cfg.ChainID),
		deferred:  metrics.InstantSendDeferred(c.cfg.ChainID),
		reserved:  metrics.InstantSendReserved(c.cfg.ChainID),
		available: metrics.InstantSendAvailable(c.cfg.ChainID),
		pending:   metrics.InstantSendPending(c.cfg.ChainID),
		warming:   metrics.InstantSendWarmingUp(c.cfg.ChainID),
	}
}
