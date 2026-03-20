package monero

import (
	"fmt"
	"strings"

	"gitlab.com/thorchain/thornode/v3/common"
)

func expectedXMRVaultAddress(_ common.PubKey, poolPubKeyEddsa common.PubKey) (common.Address, error) {
	vaultPubKey := poolPubKeyEddsa
	if vaultPubKey.IsEmpty() {
		return common.NoAddress, fmt.Errorf("xmr keygen requires a non-empty eddsa vault pubkey")
	}

	expected, err := vaultPubKey.GetAddress(common.XMRChain)
	if err != nil {
		return common.NoAddress, fmt.Errorf("fail to derive monero address from vault pubkey %s: %w", vaultPubKey.String(), err)
	}
	if expected.IsEmpty() {
		return common.NoAddress, fmt.Errorf("monero vault pubkey %s derived an empty address", vaultPubKey.String())
	}
	return expected, nil
}

func expectedXMRVaultAddressFromKeygenRequest(vaultPubKey, vaultPubKeyEddsa string) (common.Address, error) {
	parseVaultPubKey := func(value string) (common.PubKey, error) {
		value = strings.TrimSpace(value)
		if value == "" {
			return common.EmptyPubKey, nil
		}
		pubKey, err := common.NewPubKey(value)
		if err != nil {
			return common.EmptyPubKey, fmt.Errorf("invalid vault pubkey %q: %w", value, err)
		}
		return pubKey, nil
	}

	poolPubKey, err := parseVaultPubKey(vaultPubKey)
	if err != nil {
		return common.NoAddress, err
	}
	poolPubKeyEddsa, err := parseVaultPubKey(vaultPubKeyEddsa)
	if err != nil {
		return common.NoAddress, err
	}
	return expectedXMRVaultAddress(poolPubKey, poolPubKeyEddsa)
}
