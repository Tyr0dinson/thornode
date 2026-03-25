package types

import (
	"strings"
	"time"

	"gitlab.com/thorchain/thornode/v3/common"
)

var tronSimTxMaxBroadcastAttempts = 4
var tronSimTxBroadcastRetryDelay = 2 * time.Second

// SignAndBroadcastTx signs a fresh transaction payload for each attempt so TRON
// retries can recover from txs that never make it on-chain before expiring.
func SignAndBroadcastTx(client LiteChainClient, tx SimTx) (string, error) {
	attempts := 1
	if tx.Chain.Equals(common.TRONChain) {
		attempts = tronSimTxMaxBroadcastAttempts
	}

	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		signed, err := client.SignTx(tx)
		if err != nil {
			return "", err
		}

		txid, err := client.BroadcastTx(signed)
		if err == nil {
			return txid, nil
		}

		lastErr = err
		if tx.Chain.Equals(common.TRONChain) &&
			attempt < attempts &&
			shouldRetryTronSimTxBroadcast(err) {
			time.Sleep(tronSimTxBroadcastRetryDelay)
			continue
		}

		return "", err
	}

	return "", lastErr
}

func shouldRetryTronSimTxBroadcast(err error) bool {
	if err == nil {
		return false
	}

	message := err.Error()
	return strings.Contains(message, "not confirmed on tron node") ||
		strings.Contains(message, "not observed on tron node")
}
