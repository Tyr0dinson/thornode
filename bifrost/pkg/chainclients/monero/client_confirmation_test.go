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

func TestGetConfirmationCountUsesConfFormulaForInboundXMRAtOneConfirmation(t *testing.T) {
	t.Parallel()

	vaultPubKey, _ := testVaultPubKeyAndAddress(t)
	bridge := &confirmationBridge{
		asgards:          ttypes.Vaults{{PubKey: vaultPubKey, PubKeyEddsa: vaultPubKey}},
		confMultiplier:   int64(constants.MaxBasisPts),
		maxConfirmations: 45,
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
			Coins:  common.NewCoins(common.NewCoin(common.XMRAsset, cosmos.NewUint(xmrCoinbaseThorchain()))),
		}},
	}

	if got := c.GetConfirmationCount(txIn); got != 1 {
		t.Fatalf("expected inbound XMR finalization to follow formula at 1 confirmation, got %d", got)
	}
}

func TestGetConfirmationCountRoundsSmallInboundXMRUpToOneConfirmation(t *testing.T) {
	t.Parallel()

	vaultPubKey, _ := testVaultPubKeyAndAddress(t)
	bridge := &confirmationBridge{
		asgards:          ttypes.Vaults{{PubKey: vaultPubKey, PubKeyEddsa: vaultPubKey}},
		confMultiplier:   int64(constants.MaxBasisPts),
		maxConfirmations: 45,
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

	if got := c.GetConfirmationCount(txIn); got != 1 {
		t.Fatalf("expected small inbound XMR to round up to one confirmation, got %d", got)
	}
}

func TestGetConfirmationCountAppliesDefaultXMRHourCapToInbound(t *testing.T) {
	t.Parallel()

	vaultPubKey, _ := testVaultPubKeyAndAddress(t)
	bridge := &confirmationBridge{
		asgards:          ttypes.Vaults{{PubKey: vaultPubKey, PubKeyEddsa: vaultPubKey}},
		confMultiplier:   int64(constants.MaxBasisPts),
		maxConfirmations: 0,
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
			Coins:  common.NewCoins(common.NewCoin(common.XMRAsset, cosmos.NewUint(xmrCoinbaseThorchain()*100))),
		}},
	}

	if got := c.GetConfirmationCount(txIn); got != int64(xmrDefaultMaxConfirmationCap) {
		t.Fatalf("expected default XMR confirmation cap %d, got %d", xmrDefaultMaxConfirmationCap, got)
	}
}

func TestGetConfirmationCountAllowsMimirToRaiseDefaultCapForInbound(t *testing.T) {
	t.Parallel()

	vaultPubKey, _ := testVaultPubKeyAndAddress(t)
	bridge := &confirmationBridge{
		asgards:          ttypes.Vaults{{PubKey: vaultPubKey, PubKeyEddsa: vaultPubKey}},
		confMultiplier:   int64(constants.MaxBasisPts),
		maxConfirmations: 45,
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
			Coins:  common.NewCoins(common.NewCoin(common.XMRAsset, cosmos.NewUint(xmrCoinbaseThorchain()*100))),
		}},
	}

	if got := c.GetConfirmationCount(txIn); got != 45 {
		t.Fatalf("expected XMR confirmation cap 45 from mimir override, got %d", got)
	}
}

func TestGetConfirmationCountKeepsConfiguredMinimumForZeroValueXMRObservations(t *testing.T) {
	t.Parallel()

	vaultPubKey, vaultAddr := testVaultPubKeyAndAddress(t)
	bridge := &confirmationBridge{
		asgards:          ttypes.Vaults{{PubKey: vaultPubKey, PubKeyEddsa: vaultPubKey}},
		confMultiplier:   int64(constants.MaxBasisPts),
		maxConfirmations: 45,
	}
	c := &Client{
		chain:           common.XMRChain,
		cfg:             config.BifrostChainConfiguration{MinConfirmations: 7},
		thorchainBridge: bridge,
		vaultAddressMap: make(map[string]common.Address),
	}

	txIn := stypes.TxIn{
		TxArray: []*stypes.TxInItem{{
			Sender: vaultAddr.String(),
			Coins:  common.NewCoins(common.NewCoin(common.XMRAsset, cosmos.NewUint(xmrCoinbaseThorchain()*100))),
		}},
	}

	if got := c.GetConfirmationCount(txIn); got != 7 {
		t.Fatalf("expected zero-value XMR observation to keep configured minimum 7, got %d", got)
	}
}

func TestGetConfirmationCountTreatsInternalVaultChangeAsOneConfirmation(t *testing.T) {
	t.Parallel()

	vaultPubKey, vaultAddr := testVaultPubKeyAndAddress(t)
	bridge := &confirmationBridge{
		asgards:          ttypes.Vaults{{PubKey: vaultPubKey, PubKeyEddsa: vaultPubKey}},
		confMultiplier:   int64(constants.MaxBasisPts),
		maxConfirmations: 45,
	}
	c := &Client{
		chain:           common.XMRChain,
		cfg:             config.BifrostChainConfiguration{MinConfirmations: 7},
		thorchainBridge: bridge,
		vaultAddressMap: map[string]common.Address{vaultPubKey.String(): vaultAddr},
	}

	txIn := stypes.TxIn{
		TxArray: []*stypes.TxInItem{{
			Sender:              vaultAddr.String(),
			To:                  vaultAddr.String(),
			ObservedVaultPubKey: vaultPubKey,
			Memo:                "OUT:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
			Coins:               common.NewCoins(common.NewCoin(common.XMRAsset, cosmos.NewUint(1))),
		}},
	}

	if got := c.GetConfirmationCount(txIn); got != 1 {
		t.Fatalf("expected internal vault change to finalize after 1 confirmation, got %d", got)
	}
}

func TestGetConfirmationCountTreatsExternalVaultOutboundAsOneConfirmation(t *testing.T) {
	t.Parallel()

	vaultPubKey, vaultAddr := testVaultPubKeyAndAddress(t)
	bridge := &confirmationBridge{
		asgards:          ttypes.Vaults{{PubKey: vaultPubKey, PubKeyEddsa: vaultPubKey}},
		confMultiplier:   int64(constants.MaxBasisPts),
		maxConfirmations: 45,
	}
	c := &Client{
		chain:           common.XMRChain,
		cfg:             config.BifrostChainConfiguration{MinConfirmations: 7},
		thorchainBridge: bridge,
		vaultAddressMap: map[string]common.Address{vaultPubKey.String(): vaultAddr},
	}

	txIn := stypes.TxIn{
		TxArray: []*stypes.TxInItem{{
			Sender:              vaultAddr.String(),
			To:                  testOutboundXMRDestination().String(),
			ObservedVaultPubKey: vaultPubKey,
			Memo:                "OUT:BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB",
			Coins:               common.NewCoins(common.NewCoin(common.XMRAsset, cosmos.NewUint(1))),
		}},
	}

	if got := c.GetConfirmationCount(txIn); got != 1 {
		t.Fatalf("expected external vault outbound to finalize after 1 confirmation, got %d", got)
	}
}

func TestGetConfirmationCountTreatsVaultToVaultTransferAsOneConfirmation(t *testing.T) {
	t.Parallel()

	sourceVaultPubKey, sourceVaultAddr := testVaultPubKeyAndAddress(t)
	destVaultPubKey, destVaultAddr := testVaultPubKeyAndAddress(t)
	bridge := &confirmationBridge{
		asgards: ttypes.Vaults{
			{PubKey: sourceVaultPubKey, PubKeyEddsa: sourceVaultPubKey},
			{PubKey: destVaultPubKey, PubKeyEddsa: destVaultPubKey},
		},
		confMultiplier:   int64(constants.MaxBasisPts),
		maxConfirmations: 45,
	}
	c := &Client{
		chain:           common.XMRChain,
		cfg:             config.BifrostChainConfiguration{MinConfirmations: 7},
		thorchainBridge: bridge,
		vaultAddressMap: map[string]common.Address{
			sourceVaultPubKey.String(): sourceVaultAddr,
			destVaultPubKey.String():   destVaultAddr,
		},
	}

	txIn := stypes.TxIn{
		TxArray: []*stypes.TxInItem{
			{
				Sender:              sourceVaultAddr.String(),
				To:                  destVaultAddr.String(),
				ObservedVaultPubKey: sourceVaultPubKey,
				Memo:                "MIGRATE:100",
				Coins:               common.NewCoins(common.NewCoin(common.XMRAsset, cosmos.NewUint(1))),
			},
			{
				Sender:              common.XMRSeededDummyAddress('8', []byte("unknown-sender")),
				To:                  destVaultAddr.String(),
				ObservedVaultPubKey: destVaultPubKey,
				Memo:                "MIGRATE:100",
				Coins:               common.NewCoins(common.NewCoin(common.XMRAsset, cosmos.NewUint(1))),
			},
		},
	}

	if got := c.GetConfirmationCount(txIn); got != 1 {
		t.Fatalf("expected vault-to-vault transfer to finalize after 1 confirmation, got %d", got)
	}
}
