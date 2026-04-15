package core

import (
	"testing"

	"gitlab.com/thorchain/thornode/v3/common"
	openapi "gitlab.com/thorchain/thornode/v3/openapi/gen"
)

func TestXMRActiveVaultReady(t *testing.T) {
	inboundBefore := common.Address("43SYFcEWxKVM7Er883dPBG3XrkJe4a3Ht6wjo83QcXEG3MkrzaVf9wHJZCnLTnFVzS9hQNjVXdJi3hUxMwmPrPDkRWkL6V0")
	inboundAfter := common.Address("43SYFcEWxKVM7Er883dPBG3XrkJe4a3Ht6wjo83QcXEG3MkrzaVf9wHJZCnLTnFVzS9hQNjVXdJi3hUxMwmPrPDkRWkL6V1")

	vaults := []openapi.Vault{{
		Status: "ActiveVault",
		Coins: []openapi.Coin{{
			Asset:  common.XMRAsset.String(),
			Amount: "1",
		}},
		Addresses: []openapi.VaultAddress{{
			Chain:   common.XMRChain.String(),
			Address: inboundAfter.String(),
		}},
	}}

	if !xmrVaultRotationReady(vaults, inboundBefore, inboundAfter, 2) {
		t.Fatal("expected XMR active vault to be ready")
	}
}

func TestXMRActiveVaultReadyRequiresFundedInboundVault(t *testing.T) {
	inboundBefore := common.Address("43SYFcEWxKVM7Er883dPBG3XrkJe4a3Ht6wjo83QcXEG3MkrzaVf9wHJZCnLTnFVzS9hQNjVXdJi3hUxMwmPrPDkRWkL6V0")
	inboundAfter := common.Address("43SYFcEWxKVM7Er883dPBG3XrkJe4a3Ht6wjo83QcXEG3MkrzaVf9wHJZCnLTnFVzS9hQNjVXdJi3hUxMwmPrPDkRWkL6V1")

	vaults := []openapi.Vault{{
		Status: "ActiveVault",
		Coins: []openapi.Coin{{
			Asset:  common.XMRAsset.String(),
			Amount: "0",
		}},
		Addresses: []openapi.VaultAddress{{
			Chain:   common.XMRChain.String(),
			Address: inboundAfter.String(),
		}},
	}}

	if xmrVaultRotationReady(vaults, inboundBefore, inboundAfter, 2) {
		t.Fatal("expected zero-balance XMR active vault to be unready")
	}
}
