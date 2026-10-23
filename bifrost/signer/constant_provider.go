package signer

import (
	"fmt"
	"sync"

	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	"gitlab.com/thorchain/thornode/v3/constants"
)

// ConstantsProvider which will query thorchain to get the constants value per request
// it will also cache the constant values internally
type ConstantsProvider struct {
	requestHeight int64 // the block height last request to thorchain to retrieve constant values
	bridge        thorclient.ThorchainBridge
	constantsLock *sync.Mutex
	constants     map[string]int64 // the constant values get from thorchain and cached in memory
	// the block time the cached constants are scaled to, and its history,
	// fetched together with them
	blockTimeMs int64
	history     constants.BlockTimeHistory
}

// NewConstantsProvider create a new instance of ConstantsProvider
func NewConstantsProvider(bridge thorclient.ThorchainBridge) *ConstantsProvider {
	return &ConstantsProvider{
		constants:     make(map[string]int64),
		requestHeight: 0,
		bridge:        bridge,
		constantsLock: &sync.Mutex{},
	}
}

// GetInt64Value get the constant value that match the given key
func (cp *ConstantsProvider) GetInt64Value(thorchainBlockHeight int64, key constants.ConstantName) (int64, error) {
	if err := cp.EnsureConstants(thorchainBlockHeight); err != nil {
		return 0, fmt.Errorf("fail to get constants from thorchain: %w", err)
	}
	cp.constantsLock.Lock()
	defer cp.constantsLock.Unlock()
	return cp.constants[key.String()], nil
}

// GetInt64ValueWithBlockTime returns the constant value that matches key
// together with the block time it is scaled to and the block time history,
// all from the same refresh, so a duration read from it converts to wall-clock
// time consistently.
func (cp *ConstantsProvider) GetInt64ValueWithBlockTime(thorchainBlockHeight int64, key constants.ConstantName) (value, blockTimeMs int64, history constants.BlockTimeHistory, err error) {
	if err = cp.EnsureConstants(thorchainBlockHeight); err != nil {
		return 0, 0, nil, fmt.Errorf("fail to get constants from thorchain: %w", err)
	}
	cp.constantsLock.Lock()
	defer cp.constantsLock.Unlock()
	return cp.constants[key.String()], cp.blockTimeMs, cp.history, nil
}

func (cp *ConstantsProvider) EnsureConstants(thorchainBlockHeight int64) error {
	// requestHeight and churnInterval are read together under the lock: they are
	// written together by getConstantsFromThorchain, and reading either outside
	// the lock races with a concurrent refresh.
	cp.constantsLock.Lock()
	requestHeight := cp.requestHeight
	churnInterval := cp.constants[constants.ChurnInterval.String()]
	blockTimeMs := cp.blockTimeMs
	history := cp.history
	cp.constantsLock.Unlock()
	if requestHeight == 0 {
		return cp.getConstantsFromThorchain(thorchainBlockHeight)
	}
	// THORChain scales block-denominated constants to its block time, so
	// refresh as soon as the block time or its history changes: a change and
	// its revert between two lookups leave ms as cached but add two segments,
	// and the signer measures outbound age over the history. Fail closed when
	// the block time can't be read, for the same reason.
	ms, latest, err := cp.bridge.GetBlockTime()
	if err != nil {
		return fmt.Errorf("fail to get block time: %w", err)
	}
	if ms != blockTimeMs || !sameRevision(latest, history) {
		return cp.getConstantsFromThorchain(thorchainBlockHeight)
	}
	// Thorchain will have new version and constants only when new node get rotated in , and the new version get consensus
	if thorchainBlockHeight-requestHeight < churnInterval {
		return nil
	}
	return cp.getConstantsFromThorchain(thorchainBlockHeight)
}

func (cp *ConstantsProvider) getConstantsFromThorchain(height int64) error {
	// Read the block time on both sides of the constants and retry if it moved
	// in between: constants scaled to one block time must not be paired with
	// another.
	for range 3 {
		before, beforeHistory, err := cp.bridge.GetBlockTime()
		if err != nil {
			return fmt.Errorf("fail to get block time: %w", err)
		}
		values, err := cp.bridge.GetConstants()
		if err != nil {
			return fmt.Errorf("fail to get constants: %w", err)
		}
		blockTimeMs, history, err := cp.bridge.GetBlockTime()
		if err != nil {
			return fmt.Errorf("fail to get block time: %w", err)
		}
		if blockTimeMs != before || !sameRevision(history, beforeHistory) {
			continue
		}
		cp.constantsLock.Lock()
		cp.constants = values
		cp.blockTimeMs = blockTimeMs
		cp.history = history
		cp.requestHeight = height
		cp.constantsLock.Unlock()
		return nil
	}
	return fmt.Errorf("block time kept changing while reading constants")
}

// sameRevision reports whether two reads of the block time history are the
// same revision. THORChain only ever appends to it, so the length and the last
// segment identify one.
func sameRevision(a, b constants.BlockTimeHistory) bool {
	return len(a) == len(b) && (len(a) == 0 || a[len(a)-1] == b[len(b)-1])
}
