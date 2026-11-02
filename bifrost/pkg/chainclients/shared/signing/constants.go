package signing

import (
	"fmt"

	"gitlab.com/thorchain/thornode/v3/constants"
)

// ConstantsReader is the slice of thorclient.ThorchainBridge that
// ReadConstants needs.
type ConstantsReader interface {
	GetConstants() (map[string]int64, error)
	GetBlockTime() (int64, constants.BlockTimeHistory, error)
}

// ReadConstants returns THORChain's constants together with the block time
// they are scaled to and its history. The bridge reads them in separate
// requests and its response cache can refresh in between, so it reads the
// block time on both sides of the constants and retries if it moved: constants
// scaled to one block time must not be paired with another.
func ReadConstants(bridge ConstantsReader) (values map[string]int64, blockTimeMs int64, history constants.BlockTimeHistory, err error) {
	for range 3 {
		before, beforeHistory, err := bridge.GetBlockTime()
		if err != nil {
			return nil, 0, nil, fmt.Errorf("fail to get block time: %w", err)
		}
		values, err = bridge.GetConstants()
		if err != nil {
			return nil, 0, nil, fmt.Errorf("fail to get constants: %w", err)
		}
		blockTimeMs, history, err = bridge.GetBlockTime()
		if err != nil {
			return nil, 0, nil, fmt.Errorf("fail to get block time: %w", err)
		}
		if blockTimeMs == before && SameRevision(history, beforeHistory) {
			return values, blockTimeMs, history, nil
		}
	}
	return nil, 0, nil, fmt.Errorf("block time kept changing while reading constants")
}

// SameRevision reports whether two reads of the block time history are the
// same revision. THORChain only ever appends to it, so the length and the last
// segment identify one.
func SameRevision(a, b constants.BlockTimeHistory) bool {
	return len(a) == len(b) && (len(a) == 0 || a[len(a)-1] == b[len(b)-1])
}
