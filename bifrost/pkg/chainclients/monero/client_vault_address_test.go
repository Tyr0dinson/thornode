package monero

import (
	"errors"
	"testing"

	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	"gitlab.com/thorchain/thornode/v3/common"
	ttypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

type mockMoneroBridge struct {
	thorclient.ThorchainBridge
	vaultByPubKey map[string]ttypes.Vault
}

func (m *mockMoneroBridge) GetVault(pubkey string) (ttypes.Vault, error) {
	if v, ok := m.vaultByPubKey[pubkey]; ok {
		return v, nil
	}
	return ttypes.Vault{}, errors.New("vault not found")
}

func (m *mockMoneroBridge) GetAsgards() (ttypes.Vaults, error) {
	vaults := make(ttypes.Vaults, 0, len(m.vaultByPubKey))
	for _, v := range m.vaultByPubKey {
		vaults = append(vaults, v)
	}
	return vaults, nil
}

func TestGetVaultAddressReturnsEmptyForUnknownVault(t *testing.T) {
	t.Parallel()

	c := &Client{
		chain:           common.XMRChain,
		thorchainBridge: &mockMoneroBridge{vaultByPubKey: map[string]ttypes.Vault{}},
		vaultAddressMap: make(map[string]common.Address),
	}

	got := c.getVaultAddress(common.PubKey("thorpub1unknownvault"))
	if got != "" {
		t.Fatalf("expected empty vault address when mapping is unknown, got %s", got)
	}
}
