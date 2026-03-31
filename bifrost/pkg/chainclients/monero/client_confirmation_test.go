package monero

import (
	"testing"

	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	stypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/config"
	"gitlab.com/thorchain/thornode/v3/constants"
	ttypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

type confirmationBridge struct {
	thorclient.ThorchainBridge
	asgards          ttypes.Vaults
	confMultiplier   int64
	maxConfirmations int64
}

func (b *confirmationBridge) GetAsgards() (ttypes.Vaults, error) {
	return b.asgards, nil
}

func (b *confirmationBridge) GetMimirWithRef(template, _ string) (int64, error) {
	switch template {
	case constants.MimirTemplateConfMultiplierBasisPoints:
		return b.confMultiplier, nil
	case constants.MimirTemplateMaxConfirmations:
		return b.maxConfirmations, nil
	default:
		return -1, nil
	}
}

func TestGetConfirmationCountAppliesMaxConfirmationCap(t *testing.T) {
	t.Parallel()

	vaultPubKey, _ := testVaultPubKeyAndAddress(t)
	bridge := &confirmationBridge{
		asgards:          ttypes.Vaults{{PubKey: vaultPubKey, PubKeyEddsa: vaultPubKey}},
		confMultiplier:   int64(constants.MaxBasisPts),
		maxConfirmations: 3,
	}
	c := &Client{
		chain:           common.XMRChain,
		cfg:             config.BifrostChainConfiguration{MinConfirmations: 1},
		thorchainBridge: bridge,
		vaultAddressMap: make(map[string]common.Address),
	}

	txIn := stypes.TxIn{
		TxArray: []*stypes.TxInItem{{
			Sender: testOutboundXMRDestination().String(),
			Coins:  common.NewCoins(common.NewCoin(common.XMRAsset, cosmos.NewUint(xmrCoinbaseThorchain()*10))),
		}},
	}

	if got := c.GetConfirmationCount(txIn); got != 3 {
		t.Fatalf("expected max confirmation cap 3, got %d", got)
	}
}

func TestGetConfirmationCountHonorsMinimumFloor(t *testing.T) {
	t.Parallel()

	vaultPubKey, _ := testVaultPubKeyAndAddress(t)
	bridge := &confirmationBridge{
		asgards:          ttypes.Vaults{{PubKey: vaultPubKey, PubKeyEddsa: vaultPubKey}},
		confMultiplier:   int64(constants.MaxBasisPts),
		maxConfirmations: 0,
	}
	c := &Client{
		chain:           common.XMRChain,
		cfg:             config.BifrostChainConfiguration{MinConfirmations: 7},
		thorchainBridge: bridge,
		vaultAddressMap: make(map[string]common.Address),
	}

	txIn := stypes.TxIn{
		TxArray: []*stypes.TxInItem{{
			Sender: testOutboundXMRDestination().String(),
			Coins:  common.NewCoins(common.NewCoin(common.XMRAsset, cosmos.NewUint(xmrCoinbaseThorchain()-1))),
		}},
	}

	if got := c.GetConfirmationCount(txIn); got != 7 {
		t.Fatalf("expected min confirmation floor 7, got %d", got)
	}
}
