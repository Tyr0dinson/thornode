package monero

import (
	"errors"
	"strings"
	"testing"

	stypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
)

func TestAverageNetworkFeeFromBlock(t *testing.T) {
	// defaultCoinbaseValue must be in piconero (Monero atomic units, 1e12 per XMR).
	// The Monero daemon RPC get_block returns reward in piconero, and this constant
	// is subtracted directly from that value. On mainnet the tail emission is ~0.6 XMR
	// = 600,000,000,000 piconero.
	if defaultCoinbaseValue < 100000000000 { // sanity: at least 0.1 XMR in piconero
		t.Fatalf("defaultCoinbaseValue %d appears too small; must be in piconero (1e12 per XMR)", defaultCoinbaseValue)
	}

	// 200,000 piconero total fees across 2 txs -> 100,000 piconero per tx.
	reward := defaultCoinbaseValue + uint64(200000)
	fee := averageNetworkFeeFromBlock(reward, 2)
	if fee != 100000 {
		t.Fatalf("expected average fee 100000, got %d", fee)
	}

	// Reward equal to coinbase → zero fees.
	fee = averageNetworkFeeFromBlock(defaultCoinbaseValue, 2)
	if fee != 0 {
		t.Fatalf("expected zero fee when reward equals coinbase, got %d", fee)
	}

	// Zero tx count → zero fees.
	fee = averageNetworkFeeFromBlock(defaultCoinbaseValue+200000, 0)
	if fee != 0 {
		t.Fatalf("expected zero fee when tx count is zero, got %d", fee)
	}

	// Reward below coinbase → zero fees (no underflow).
	fee = averageNetworkFeeFromBlock(defaultCoinbaseValue-1, 5)
	if fee != 0 {
		t.Fatalf("expected zero fee when reward is below coinbase, got %d", fee)
	}
}

func TestIsUnavailableBlockErr(t *testing.T) {
	if !IsUnavailableBlockErr(ErrFrostUnavailableBlock) {
		t.Fatal("expected unavailable block error to be detected")
	}
	if !IsUnavailableBlockErr(errors.Join(errString("wrapper"), ErrFrostUnavailableBlock)) {
		t.Fatal("expected wrapped unavailable block error to be detected")
	}
	if IsUnavailableBlockErr(errString("some other rpc error")) {
		t.Fatal("expected non-block-height errors to be ignored")
	}
}

func TestDedupeTxInItemsPreservesReplayMetadata(t *testing.T) {
	txHash := strings.Repeat("a", 64)
	vaultPubKey := common.PubKey("cthorpub1zcjduepqyf5a5mzu9v5njvd5s88ynqj3rnmjcejctd44848hp6wl8k3a394sv6uwps")
	base := &stypes.TxInItem{
		Tx:                  txHash,
		Sender:              "sender",
		To:                  "to",
		ObservedVaultPubKey: vaultPubKey,
	}
	replay := &stypes.TxInItem{
		Tx:                  txHash,
		Sender:              "sender",
		To:                  "to",
		ObservedVaultPubKey: vaultPubKey,
		TxKeys:              []string{"tx-key"},
		XmrKeyImages: []*common.XmrKeyImageProof{
			{TxHash: txHash, OutputIndex: 0, KeyImage: "key-image"},
		},
	}

	deduped := (&Scanner{}).dedupeTxInItemsByTxID([]*stypes.TxInItem{base, replay})
	if len(deduped) != 1 {
		t.Fatalf("expected one deduped item, got %d", len(deduped))
	}
	if len(deduped[0].TxKeys) != 1 || deduped[0].TxKeys[0] != "tx-key" {
		t.Fatalf("expected txkey replay metadata to be preserved, got %#v", deduped[0].TxKeys)
	}
	if len(deduped[0].XmrKeyImages) != 1 || deduped[0].XmrKeyImages[0].KeyImage != "key-image" {
		t.Fatalf("expected keyimage replay metadata to be preserved, got %#v", deduped[0].XmrKeyImages)
	}
}

type errString string

func (e errString) Error() string { return string(e) }
