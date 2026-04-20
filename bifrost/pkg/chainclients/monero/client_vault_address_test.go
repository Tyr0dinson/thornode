package monero

import (
	"errors"
	"strings"
	"testing"

	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	"gitlab.com/thorchain/thornode/v3/common"
	ttypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

type mockMoneroBridge struct {
	thorclient.ThorchainBridge
	vaultByPubKey       map[string]ttypes.Vault
	asgards             ttypes.Vaults
	pubKeys             []thorclient.PubKeyContractAddressPair
	observedTxVoterByID map[string]*ttypes.QueryObservedTxVoter
	txStatusByID        map[string]*ttypes.QueryTxStatusResponse
	constants           map[string]int64
	nodeStatus          ttypes.NodeStatus
	blockHeight         int64
}

func (m *mockMoneroBridge) GetVault(pubkey string) (ttypes.Vault, error) {
	if v, ok := m.vaultByPubKey[pubkey]; ok {
		return v, nil
	}
	return ttypes.Vault{}, errors.New("vault not found")
}

func (m *mockMoneroBridge) GetAsgards() (ttypes.Vaults, error) {
	if m.asgards != nil {
		return append(ttypes.Vaults(nil), m.asgards...), nil
	}
	vaults := make(ttypes.Vaults, 0, len(m.vaultByPubKey))
	for _, v := range m.vaultByPubKey {
		vaults = append(vaults, v)
	}
	return vaults, nil
}

func (m *mockMoneroBridge) GetPubKeys() ([]thorclient.PubKeyContractAddressPair, error) {
	if m.pubKeys != nil {
		return append([]thorclient.PubKeyContractAddressPair(nil), m.pubKeys...), nil
	}

	pairs := make([]thorclient.PubKeyContractAddressPair, 0, len(m.vaultByPubKey))
	seen := make(map[string]struct{})
	for _, vault := range m.vaultByPubKey {
		for _, pubKey := range []common.PubKey{vault.PubKeyFrost, vault.PubKeyEddsa} {
			if pubKey.IsEmpty() {
				continue
			}
			key := pubKey.String()
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			pairs = append(pairs, thorclient.PubKeyContractAddressPair{
				PubKey:   pubKey,
				Algo:     common.SigningAlgoEd25519,
				Inactive: vault.Status == ttypes.VaultStatus_InactiveVault,
			})
		}
	}
	return pairs, nil
}

func (m *mockMoneroBridge) GetObservedTxVoter(txID string) (*ttypes.QueryObservedTxVoter, error) {
	if voter, ok := m.observedTxVoterByID[txID]; ok {
		return voter, nil
	}
	for key, voter := range m.observedTxVoterByID {
		if strings.EqualFold(key, txID) {
			return voter, nil
		}
	}
	return nil, errors.New("observed tx voter not found")
}

func (m *mockMoneroBridge) GetTxStatus(txID string) (*ttypes.QueryTxStatusResponse, error) {
	if status, ok := m.txStatusByID[txID]; ok {
		return status, nil
	}
	for key, status := range m.txStatusByID {
		if strings.EqualFold(key, txID) {
			return status, nil
		}
	}
	return nil, errors.New("tx status not found")
}

func (m *mockMoneroBridge) FetchNodeStatus() (ttypes.NodeStatus, error) {
	if m.nodeStatus == ttypes.NodeStatus_Unknown {
		return ttypes.NodeStatus_Active, nil
	}
	return m.nodeStatus, nil
}

func (m *mockMoneroBridge) GetBlockHeight() (int64, error) {
	return m.blockHeight, nil
}

func (m *mockMoneroBridge) GetConstants() (map[string]int64, error) {
	if m.constants == nil {
		return nil, nil
	}
	out := make(map[string]int64, len(m.constants))
	for key, value := range m.constants {
		out[key] = value
	}
	return out, nil
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
