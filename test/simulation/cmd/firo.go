package main

import (
	"fmt"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/utxo/rpc"
	"gitlab.com/thorchain/thornode/v3/common"
	. "gitlab.com/thorchain/thornode/v3/test/simulation/pkg/types"
)

func waitForFiroRPCReadiness(endpoint string) error {
	client, err := rpc.NewClient(endpoint, "thorchain", "password", 0, 5*time.Second, common.FIROChain, zerolog.Nop())
	if err != nil {
		return err
	}
	return waitForFiroReadiness(func() (int64, error) { return firoReadyHeight(client) }, time.Now, time.Sleep, 10*time.Minute)
}

// Wait for exchange activation and advancing ChainLocks before importing wallets
// and funding users. A reachable RPC alone is not a ready FIRO quorum.
func waitForFiroReadiness(getHeight func() (int64, error), now func() time.Time, sleep func(time.Duration), timeout time.Duration) error {
	deadline := now().Add(timeout)
	lastHeight := int64(-1)
	var lastErr error
	for now().Before(deadline) {
		height, err := getHeight()
		lastErr = err
		if err == nil {
			if lastHeight >= 1000 && height > lastHeight {
				return nil
			}
			lastHeight = height
		}
		log.Info().Err(err).Int64("chainlock_height", lastHeight).Msg("waiting for FIRO quorum and ChainLocks")
		sleep(time.Second)
	}
	return fmt.Errorf("FIRO quorum readiness timed out after %s at height %d (last RPC error: %v)", timeout, lastHeight, lastErr)
}

func firoReadyHeight(client *rpc.Client) (int64, error) {
	info, err := client.GetBlockchainInfoFIRO()
	if err != nil {
		return 0, err
	}
	if info.Chain != "regtest" || info.Pruned || info.Blocks < 1000 || info.Headers > info.Blocks+1 {
		return 0, fmt.Errorf("FIRO requires synced, unpruned regtest past exchange activation (chain=%s blocks=%d headers=%d pruned=%t)", info.Chain, info.Blocks, info.Headers, info.Pruned)
	}
	for height := info.Blocks; height >= info.Blocks-1 && height >= 1000; height-- {
		hash, err := client.GetBlockHash(height)
		if err != nil {
			return 0, err
		}
		header, err := client.GetBlockHeaderFIRO(hash)
		if err != nil {
			return 0, err
		}
		if hash != "" && header.Hash == hash && header.Height == height && header.Confirmations > 0 && header.ChainLock {
			return height, nil
		}
	}
	return 0, fmt.Errorf("FIRO has no active ChainLock at tip height %d or its parent; check firo1 bootstrap logs and masternode status", info.Blocks)
}

type firoFundingReader interface {
	GetRawTransactionVerbose(string) (*rpc.TxResult, error)
}

// Funding spends change serially. Never sign another transfer after acceptance
// until this one is confirmed and ChainLocked; a timeout must retain its txid.
func waitForFiroFunding(client LiteChainClient, txID string, sleep func(time.Duration)) error {
	reader, ok := client.(firoFundingReader)
	if !ok {
		return fmt.Errorf("%w: FIRO client cannot verify funding %s", ErrSimTxUncertain, txID)
	}
	var lastErr error
	for attempt := 0; attempt < 60; attempt++ {
		tx, err := reader.GetRawTransactionVerbose(txID)
		lastErr = err
		if err == nil && tx != nil && tx.Txid == txID && tx.BlockHash != "" && tx.Confirmations > 0 && tx.ChainLock {
			return nil
		}
		if attempt < 59 {
			sleep(2 * time.Second)
		}
	}
	return fmt.Errorf("%w: FIRO funding %s has not reached ChainLock finality (last RPC error: %v)", ErrSimTxUncertain, txID, lastErr)
}
