package monero

import (
	"strings"
	"testing"

	"go.uber.org/atomic"

	stypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

func TestBuildOutboundObservationUsesOriginalMemo(t *testing.T) {
	t.Parallel()

	vaultPubKey, vaultAddr := testVaultPubKeyAndAddress(t)
	txHash := strings.Repeat("a", 64)
	outboundMemo := "OUT:" + strings.Repeat("B", 64)

	client := &Client{
		chain: common.XMRChain,
		scanner: &Scanner{
			currentBlockHeight: atomic.NewInt64(123),
			lastFee:            atomic.NewUint64(77),
		},
		vaultAddressMap: map[string]common.Address{
			vaultPubKey.String(): vaultAddr,
		},
	}

	observation := client.buildOutboundObservation(
		stypes.TxOutItem{
			Chain:            common.XMRChain,
			ToAddress:        common.Address(common.XMRChecksummedDummyAddress('8')),
			Coins:            common.NewCoins(common.NewCoin(common.XMRAsset, cosmos.NewUint(123))),
			Memo:             "",
			OriginalMemo:     outboundMemo,
			VaultPubKey:      vaultPubKey,
			VaultPubKeyEddsa: vaultPubKey,
		},
		txHash,
		0,
		0,
		nil,
	)
	if observation == nil {
		t.Fatal("expected outbound observation")
	}
	if observation.Memo != outboundMemo {
		t.Fatalf("expected observation memo %q, got %q", outboundMemo, observation.Memo)
	}
	if observation.Sender != vaultAddr.String() {
		t.Fatalf("expected sender %s, got %s", vaultAddr, observation.Sender)
	}
}

func TestBuildOutboundObservationUsesActualSignedAmount(t *testing.T) {
	t.Parallel()

	vaultPubKey, vaultAddr := testVaultPubKeyAndAddress(t)
	txHash := strings.Repeat("b", 64)

	client := &Client{
		chain: common.XMRChain,
		scanner: &Scanner{
			currentBlockHeight: atomic.NewInt64(123),
			lastFee:            atomic.NewUint64(77),
		},
		vaultAddressMap: map[string]common.Address{
			vaultPubKey.String(): vaultAddr,
		},
	}

	observation := client.buildOutboundObservation(
		stypes.TxOutItem{
			Chain:            common.XMRChain,
			ToAddress:        common.Address(common.XMRChecksummedDummyAddress('8')),
			Coins:            common.NewCoins(common.NewCoin(common.XMRAsset, cosmos.NewUint(90))),
			Memo:             "MIGRATE:123",
			VaultPubKey:      vaultPubKey,
			VaultPubKeyEddsa: vaultPubKey,
		},
		txHash,
		99*moneroAtomicToThorchain,
		0,
		nil,
	)
	if observation == nil {
		t.Fatal("expected outbound observation")
	}
	coin := observation.Coins.GetCoin(common.XMRAsset)
	if coin.IsEmpty() {
		t.Fatal("expected xmr coin in observation")
	}
	if !coin.Amount.Equal(cosmos.NewUint(99)) {
		t.Fatalf("expected observation amount 99, got %s", coin.Amount)
	}
}
