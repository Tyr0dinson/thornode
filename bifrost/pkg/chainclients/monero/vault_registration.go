package monero

import (
	"encoding/hex"
	"fmt"
	"strings"

	cometed25519 "github.com/cometbft/cometbft/crypto/ed25519"
	"github.com/rs/zerolog"

	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	ttypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
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

func vaultPubKeyFromGroupKeyHex(groupKeyHex string) (common.PubKey, error) {
	groupKeyHex = strings.TrimSpace(groupKeyHex)
	if groupKeyHex == "" {
		return common.EmptyPubKey, fmt.Errorf("group key is empty")
	}

	raw, err := hex.DecodeString(groupKeyHex)
	if err != nil {
		return common.EmptyPubKey, fmt.Errorf("fail to decode group key %q: %w", groupKeyHex, err)
	}
	if len(raw) != 32 {
		return common.EmptyPubKey, fmt.Errorf("unexpected group key length %d", len(raw))
	}

	return common.NewPubKeyFromCrypto(cometed25519.PubKey(raw))
}

type frostVaultRegistrationResult struct {
	Address    common.Address
	Registered bool
}

func keyImageRefsFromVault(vault ttypes.Vault) []xmrFrostApplyKeyImageRef {
	if len(vault.KeyImagesXmr) == 0 {
		return nil
	}

	refs := make([]xmrFrostApplyKeyImageRef, 0, len(vault.KeyImagesXmr))
	for _, ref := range vault.KeyImagesXmr {
		if ref == nil {
			continue
		}
		txHash := strings.TrimSpace(ref.TxHash)
		keyImage := strings.TrimSpace(ref.KeyImage)
		if txHash == "" || keyImage == "" {
			continue
		}
		refs = append(refs, xmrFrostApplyKeyImageRef{
			TxHash:      txHash,
			OutputIndex: ref.OutputIndex,
			KeyImage:    keyImage,
		})
	}

	return refs
}

func txKeyEntriesFromVault(
	bridge thorclient.ThorchainBridge,
	chain common.Chain,
	vault ttypes.Vault,
) ([]xmrFrostRegisterTxKeyEntry, error) {
	if bridge == nil || len(vault.TxKeysXmr) == 0 {
		return nil, nil
	}

	vaultPubKey := keyImageCatchupVaultPubKey(vault)
	if vaultPubKey.IsEmpty() {
		return nil, nil
	}

	helper := &Client{
		chain:           chain,
		thorchainBridge: bridge,
	}
	entries := make([]xmrFrostRegisterTxKeyEntry, 0, len(vault.TxKeysXmr))
	for _, ref := range makeTxKeyCatchupRefs(vault.TxKeysXmr) {
		meta, err := helper.getTxKeyCatchupOutboundMeta(vaultPubKey, ref.txHash)
		if err != nil {
			return nil, fmt.Errorf("fail to build tx-key registration metadata for %s: %w", ref.txHash, err)
		}
		entries = append(entries, xmrFrostRegisterTxKeyEntry{
			TxHash:      ref.txHash,
			TxKeys:      append([]string(nil), ref.txKeys...),
			FromAddress: meta.FromAddress,
			ToAddress:   meta.ToAddress,
			Amount:      meta.Amount,
			Fee:         meta.Fee,
			Memo:        meta.Memo,
		})
	}

	return entries, nil
}

func ensureFrostVaultRegistered(
	logger zerolog.Logger,
	signer *FrostSigner,
	chain common.Chain,
	vaultPubKey common.PubKey,
	bootstrap *frostVaultBootstrap,
	source string,
	birthday int64,
) (frostVaultRegistrationResult, error) {
	return ensureFrostVaultRegisteredWithKeyImages(
		logger,
		signer,
		chain,
		vaultPubKey,
		bootstrap,
		source,
		birthday,
		nil,
	)
}

func ensureFrostVaultRegisteredWithKeyImages(
	logger zerolog.Logger,
	signer *FrostSigner,
	chain common.Chain,
	vaultPubKey common.PubKey,
	bootstrap *frostVaultBootstrap,
	source string,
	birthday int64,
	keyImages []xmrFrostApplyKeyImageRef,
) (frostVaultRegistrationResult, error) {
	return ensureFrostVaultRegisteredWithInventory(
		logger,
		signer,
		chain,
		vaultPubKey,
		bootstrap,
		source,
		birthday,
		keyImages,
		nil,
	)
}

func ensureFrostVaultRegisteredWithInventory(
	logger zerolog.Logger,
	signer *FrostSigner,
	chain common.Chain,
	vaultPubKey common.PubKey,
	bootstrap *frostVaultBootstrap,
	source string,
	birthday int64,
	keyImages []xmrFrostApplyKeyImageRef,
	txKeyEntries []xmrFrostRegisterTxKeyEntry,
) (frostVaultRegistrationResult, error) {
	if signer == nil {
		return frostVaultRegistrationResult{}, fmt.Errorf("frost signer is not initialized")
	}
	if vaultPubKey.IsEmpty() {
		return frostVaultRegistrationResult{}, fmt.Errorf("vault pubkey is empty")
	}

	address, err := vaultPubKey.GetAddress(chain)
	if err != nil {
		return frostVaultRegistrationResult{}, fmt.Errorf("fail to derive %s vault address for %s: %w", chain, vaultPubKey.String(), err)
	}
	if address.IsEmpty() {
		return frostVaultRegistrationResult{}, fmt.Errorf("derived %s vault address is empty for %s", chain, vaultPubKey.String())
	}

	groupKeyHex, err := groupKeyHexFromVaultPubKey(vaultPubKey)
	if err != nil {
		return frostVaultRegistrationResult{}, err
	}

	req := xmrFrostRegisterVaultRequest{
		VaultPubKey:         vaultPubKey.String(),
		GroupKey:            groupKeyHex,
		Address:             address.String(),
		Source:              source,
		VaultCreationHeight: birthday,
		KeyImages:           append([]xmrFrostApplyKeyImageRef(nil), keyImages...),
		TxKeyEntries:        append([]xmrFrostRegisterTxKeyEntry(nil), txKeyEntries...),
	}
	if spendKeyHex, ok := bootstrap.spendKeyHexFor(vaultPubKey); ok {
		req.SpendKeyHex = strings.TrimSpace(spendKeyHex)
	}

	resp, err := signer.RegisterVault(req)
	if err != nil {
		logger.Debug().
			Err(err).
			Str("vault_pubkey", vaultPubKey.String()).
			Str("address", address.String()).
			Msg("failed to register xmr vault with frost signer")
		return frostVaultRegistrationResult{}, err
	}

	return frostVaultRegistrationResult{
		Address:    address,
		Registered: resp.Registered,
	}, nil
}
