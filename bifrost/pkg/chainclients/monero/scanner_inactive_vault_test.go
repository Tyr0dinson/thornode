package monero

import (
	"fmt"
	"testing"

	"github.com/rs/zerolog/log"

	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/config"
	ttypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

type scannerTrackedVaultBridge struct {
	thorclient.ThorchainBridge
	asgards    ttypes.Vaults
	pubKeys    []thorclient.PubKeyContractAddressPair
	vaultByKey map[string]ttypes.Vault
}

func (b *scannerTrackedVaultBridge) GetAsgards() (ttypes.Vaults, error) {
	return b.asgards, nil
}

func (b *scannerTrackedVaultBridge) GetPubKeys() ([]thorclient.PubKeyContractAddressPair, error) {
	return b.pubKeys, nil
}

func (b *scannerTrackedVaultBridge) GetVault(pubkey string) (ttypes.Vault, error) {
	vault, ok := b.vaultByKey[pubkey]
	if !ok {
		return ttypes.Vault{}, fmt.Errorf("vault not found for %s", pubkey)
	}
	return vault, nil
}

func TestFetchTxsAcceptsInactiveVaultInboundWhenFrostKeyIsTracked(t *testing.T) {
	t.Parallel()

	currentVaultPubKey, currentVaultAddr := testVaultPubKeyAndAddress(t)
	inactiveVaultPubKey, inactiveVaultAddr := testVaultPubKeyAndAddress(t)
	txID := "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"

	frostServer := newFrostTransfersServer(
		t,
		[]xmrFrostTransferEntry{
			{
				TxID:        txID,
				Address:     inactiveVaultAddr.String(),
				VaultPubKey: inactiveVaultPubKey.String(),
				Amount:      uint64(5 * moneroAtomicToThorchain),
				Height:      100,
				Note:        "ADD:XMR.XMR:thor1qqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqq0l5m4g",
			},
		},
		nil,
		[]string{currentVaultAddr.String(), inactiveVaultAddr.String()},
	)
	defer frostServer.Close()

	frostSigner, err := NewFrostSignerWithEndpoint(config.BifrostChainConfiguration{}, frostServer.URL)
	if err != nil {
		t.Fatalf("new frost signer: %v", err)
	}

	currentVault := ttypes.Vault{
		PubKey:                 currentVaultPubKey,
		PubKeyEddsa:            currentVaultPubKey,
		PubKeyFrost:            currentVaultPubKey,
		Status:                 ttypes.VaultStatus_ActiveVault,
		VaultCreationHeightXmr: 50,
	}
	inactiveVault := ttypes.Vault{
		PubKey:                 inactiveVaultPubKey,
		PubKeyEddsa:            inactiveVaultPubKey,
		PubKeyFrost:            inactiveVaultPubKey,
		Status:                 ttypes.VaultStatus_InactiveVault,
		InboundTxCount:         1,
		VaultCreationHeightXmr: 25,
	}

	bridge := &scannerTrackedVaultBridge{
		asgards: ttypes.Vaults{currentVault},
		pubKeys: []thorclient.PubKeyContractAddressPair{
			{PubKey: currentVaultPubKey, Algo: common.SigningAlgoEd25519},
			{PubKey: inactiveVaultPubKey, Algo: common.SigningAlgoEd25519, Inactive: true},
		},
		vaultByKey: map[string]ttypes.Vault{
			currentVaultPubKey.String():  currentVault,
			inactiveVaultPubKey.String(): inactiveVault,
		},
	}

	scanner := NewScanner(log.Logger, frostSigner, bridge, common.XMRChain, common.EmptyPubKey, 72)

	txIn, err := scanner.FetchTxs(100, 100)
	if err != nil {
		t.Fatalf("fetch txs: %v", err)
	}
	if len(txIn.TxArray) != 1 {
		t.Fatalf("expected 1 inactive-vault observation, got %d", len(txIn.TxArray))
	}
	if !txIn.TxArray[0].ObservedVaultPubKey.Equals(inactiveVaultPubKey) {
		t.Fatalf("expected inactive vault pubkey %s, got %s", inactiveVaultPubKey, txIn.TxArray[0].ObservedVaultPubKey)
	}
}
