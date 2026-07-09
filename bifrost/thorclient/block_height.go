package thorclient

import (
	"encoding/json"
	"fmt"
	"strconv"

	"gitlab.com/thorchain/thornode/v3/common"
	openapi "gitlab.com/thorchain/thornode/v3/openapi/gen"
)

// GetLastObservedInHeight returns the lastobservedin value for the chain past in
func (b *thorchainBridge) GetLastObservedInHeight(chain common.Chain) (int64, error) {
	lastblock, err := b.getLastBlock(chain)
	if err != nil {
		return 0, fmt.Errorf("failed to GetLastObservedInHeight: %w", err)
	}
	for _, item := range lastblock {
		if item.Chain == chain.String() {
			return item.LastObservedIn, nil
		}
	}
	return 0, fmt.Errorf("fail to GetLastObservedInHeight,chain(%s)", chain)
}

// GetLastObservedInHeightAt returns the last_observed_in value for chain as of a
// specific THORChain height. Pinning to a fixed height makes the value identical
// on every node (historical consensus state) instead of each node's live,
// per-URL-cached tip — used for deterministic TRON ref-block selection at keysign.
func (b *thorchainBridge) GetLastObservedInHeightAt(chain common.Chain, thorHeight int64) (int64, error) {
	lastblock, err := b.getLastBlockAt(chain, thorHeight)
	if err != nil {
		return 0, fmt.Errorf("failed to GetLastObservedInHeightAt: %w", err)
	}
	for _, item := range lastblock {
		if item.Chain == chain.String() {
			return item.LastObservedIn, nil
		}
	}
	return 0, fmt.Errorf("fail to GetLastObservedInHeightAt,chain(%s)", chain)
}

// GetLastSignedOutHeight returns the lastsignedout value for thorchain
func (b *thorchainBridge) GetLastSignedOutHeight(chain common.Chain) (int64, error) {
	lastblock, err := b.getLastBlock(chain)
	if err != nil {
		return 0, fmt.Errorf("failed to GetLastSignedOutHeight: %w", err)
	}
	for _, item := range lastblock {
		if item.Chain == chain.String() {
			return item.LastSignedOut, nil
		}
	}
	return 0, fmt.Errorf("fail to GetLastSignedOutHeight,chain(%s)", chain)
}

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
	return b.getLastBlockAt(chain, 0)
}

// getLastBlockAt is getLastBlock optionally pinned to a THORChain height via
// ?height= (thorHeight <= 0 queries the latest, matching the un-pinned behaviour).
func (b *thorchainBridge) getLastBlockAt(chain common.Chain, thorHeight int64) ([]openapi.LastBlock, error) {
	path := LastBlockEndpoint
	if !chain.IsEmpty() {
		path = fmt.Sprintf("%s/%s", path, chain.String())
	}
	uri := b.getThorChainURL(path)
	if thorHeight > 0 {
		uri += "?height=" + strconv.FormatInt(thorHeight, 10)
	}
	buf, _, err := b.get(uri)
	if err != nil {
		return nil, fmt.Errorf("failed to get lastblock: %w", err)
	}
	var lastBlock []openapi.LastBlock
	if err = json.Unmarshal(buf, &lastBlock); err != nil {
		return nil, fmt.Errorf("failed to unmarshal last block: %w", err)
	}
	return lastBlock, nil
}
