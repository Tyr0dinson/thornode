package thorclient

import (
	"encoding/json"
	"fmt"

	"gitlab.com/thorchain/thornode/v3/common"
	openapi "gitlab.com/thorchain/thornode/v3/openapi/gen"
)

// GetBlockHeight returns the current height for thorchain blocks
func (b *thorchainBridge) GetBlockHeight() (int64, error) {
	latestBlocks, err := b.getLastBlock(common.EmptyChain)
	if err != nil {
		return 0, fmt.Errorf("failed to GetThorchainHeight: %w", err)
	}
	for _, item := range latestBlocks {
		return item.Thorchain, nil
	}
	return 0, fmt.Errorf("failed to GetThorchainHeight")
}

// getLastBlock calls the /lastblock/{chain} endpoint and Unmarshal's into the QueryResLastBlockHeights type
func (b *thorchainBridge) getLastBlock(chain common.Chain) ([]openapi.LastBlock, error) {
	path := LastBlockEndpoint
	if !chain.IsEmpty() {
		path = fmt.Sprintf("%s/%s", path, chain.String())
	}
	buf, _, err := b.getWithPath(path)
	if err != nil {
		return nil, fmt.Errorf("failed to get lastblock: %w", err)
	}
	var lastBlock []openapi.LastBlock
	if err = json.Unmarshal(buf, &lastBlock); err != nil {
		return nil, fmt.Errorf("failed to unmarshal last block: %w", err)
	}
	return lastBlock, nil
}
