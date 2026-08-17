package core

import (
	"testing"

	"gitlab.com/thorchain/thornode/v3/common"
	openapi "gitlab.com/thorchain/thornode/v3/openapi/gen"
	ttypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

func TestFindActiveVaultForChainUsesInboundSignerNotVaultOrder(t *testing.T) {
	retiringCanonical := ttypes.GetRandomPubKey()
	activeCanonical := ttypes.GetRandomPubKey()
	inboundSigner := activeCanonical

	retiring := testOpenAPIVault(retiringCanonical, common.EmptyPubKey, common.EmptyPubKey, ttypes.VaultStatus_RetiringVault.String(), common.BTCChain)
	active := testOpenAPIVault(activeCanonical, common.EmptyPubKey, common.EmptyPubKey, ttypes.VaultStatus_ActiveVault.String(), common.BTCChain)

	vault, canonical, err := findActiveVaultForChain([]openapi.Vault{retiring, active}, common.BTCChain, inboundSigner)
	if err != nil {
		t.Fatalf("find active vault: %v", err)
	}
	if !canonical.Equals(activeCanonical) {
		t.Fatalf("expected active canonical pubkey %s, got %s", activeCanonical, canonical)
	}
	if vault.Status != ttypes.VaultStatus_ActiveVault.String() {
		t.Fatalf("expected active vault, got status %s", vault.Status)
	}
}

func TestFindActiveVaultForChainRequiresMatchingChain(t *testing.T) {
	canonical := ttypes.GetRandomPubKey()
	inboundSigner := canonical
	vault := testOpenAPIVault(canonical, common.EmptyPubKey, common.EmptyPubKey, ttypes.VaultStatus_ActiveVault.String(), common.ETHChain)

	if _, _, err := findActiveVaultForChain([]openapi.Vault{vault}, common.BTCChain, inboundSigner); err == nil {
		t.Fatal("expected an active vault without BTC support to be rejected")
	}
}

func TestVaultSigningPubKeyUsesXMRFrostKey(t *testing.T) {
	canonical := ttypes.GetRandomPubKey()
	eddsa := ttypes.GetRandomEd25519PubKey()
	frost := ttypes.GetRandomEd25519PubKey()
	vault := testOpenAPIVault(canonical, eddsa, frost, ttypes.VaultStatus_ActiveVault.String(), common.XMRChain)

	signer, err := vaultSigningPubKey(vault, common.XMRChain)
	if err != nil {
		t.Fatalf("get XMR signer: %v", err)
	}
	if !signer.Equals(frost) {
		t.Fatalf("expected XMR FROST signer %s, got %s", frost, signer)
	}
	if signer.Equals(eddsa) {
		t.Fatalf("XMR signer must not use regular EdDSA key %s", eddsa)
	}
}

func testOpenAPIVault(canonical, eddsaSigner, frostSigner common.PubKey, status string, chain common.Chain) openapi.Vault {
	canonicalString := canonical.String()
	eddsaString := eddsaSigner.String()
	frostString := frostSigner.String()
	return openapi.Vault{
		PubKey:      &canonicalString,
		PubKeyEddsa: &eddsaString,
		PubKeyFrost: &frostString,
		Status:      status,
		Chains:      []string{chain.String()},
	}
}
