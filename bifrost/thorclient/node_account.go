package thorclient

import (
	"encoding/json"
	"fmt"

	"gitlab.com/thorchain/thornode/x/thorchain/types"
)

// GetNodeAccount retrieves node account for this address from thorchain
func (b *ThorchainBridge) GetNodeAccount(thorAddr string) (*types.NodeAccount, error) {
	path := fmt.Sprintf("%s/%s", NodeAccountEndpoint, thorAddr)
	body, _, err := b.getWithPath(path)
	if err != nil {
		return &types.NodeAccount{}, fmt.Errorf("failed to get node account: %w", err)
	}
	var na types.NodeAccount
	if err := json.Unmarshal(body, &na); err != nil {
		return &types.NodeAccount{}, fmt.Errorf("failed to unmarshal node account: %w", err)
	}
	return &na, nil
}

// GetNodeAccounts retrieves all node accounts
func (b *ThorchainBridge) GetNodeAccounts() (*types.NodeAccounts, error) {
	body, _, err := b.getWithPath(NodeAccountsEndpoint)
	if err != nil {
		return &types.NodeAccounts{}, fmt.Errorf("failed to get node accounts: %w", err)
	}
	var accts types.NodeAccounts
	if err := json.Unmarshal(body, &accts); err != nil {
		return &types.NodeAccounts{}, fmt.Errorf("failed to unmarshal node accounts: %w", err)
	}
	return &accts, nil
}
