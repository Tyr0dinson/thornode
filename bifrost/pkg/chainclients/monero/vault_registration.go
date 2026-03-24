package monero

import (
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/rs/zerolog"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

func groupKeyHexFromVaultPubKey(vaultPubKey common.PubKey) (string, error) {
	if vaultPubKey.IsEmpty() {
		return "", fmt.Errorf("vault pubkey is empty")
	}

	pk, err := cosmos.GetPubKeyFromBech32(cosmos.Bech32PubKeyTypeAccPub, vaultPubKey.String())
	if err != nil {
		return "", fmt.Errorf("fail to decode vault pubkey %s: %w", vaultPubKey.String(), err)
	}
	raw := pk.Bytes()
	if len(raw) != 32 {
		return "", fmt.Errorf("unexpected Ed25519 vault pubkey length %d", len(raw))
	}

	return hex.EncodeToString(raw), nil
}

func ensureFrostVaultRegistered(
	logger zerolog.Logger,
	signer *FrostSigner,
	chain common.Chain,
	vaultPubKey common.PubKey,
	bootstrap *frostVaultBootstrap,
	source string,
) (common.Address, error) {
	if signer == nil {
		return common.NoAddress, fmt.Errorf("frost signer is not initialized")
	}
	if vaultPubKey.IsEmpty() {
		return common.NoAddress, fmt.Errorf("vault pubkey is empty")
	}

	address, err := vaultPubKey.GetAddress(chain)
	if err != nil {
		return common.NoAddress, fmt.Errorf("fail to derive %s vault address for %s: %w", chain, vaultPubKey.String(), err)
	}
	if address.IsEmpty() {
		return common.NoAddress, fmt.Errorf("derived %s vault address is empty for %s", chain, vaultPubKey.String())
	}

	groupKeyHex, err := groupKeyHexFromVaultPubKey(vaultPubKey)
	if err != nil {
		return common.NoAddress, err
	}

	req := xmrFrostRegisterVaultRequest{
		VaultPubKey: vaultPubKey.String(),
		GroupKey:    groupKeyHex,
		Address:     address.String(),
		Source:      source,
	}
	if spendKeyHex, ok := bootstrap.spendKeyHexFor(vaultPubKey); ok {
		req.SpendKeyHex = strings.TrimSpace(spendKeyHex)
	}

	if _, err = signer.RegisterVault(req); err != nil {
		logger.Debug().
			Err(err).
			Str("vault_pubkey", vaultPubKey.String()).
			Str("address", address.String()).
			Msg("failed to register xmr vault with frost signer")
		return common.NoAddress, err
	}

	return address, nil
}
