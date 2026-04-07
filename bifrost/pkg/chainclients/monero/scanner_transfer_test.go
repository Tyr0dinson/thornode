package monero

import (
	"testing"

	"github.com/rs/zerolog/log"
	"go.uber.org/atomic"

	stypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

func TestUnknownSenderAddress(t *testing.T) {
	addr := unknownSenderAddress("tx-hash-a", "8abcd")
	parsed, err := common.NewAddress(addr)
	if err != nil {
		t.Fatalf("expected parseable unknown sender marker address: %v", err)
	}
	if parsed.IsChain(common.XMRChain) {
		t.Fatalf("expected unknown sender marker to not be chain-valid XMR")
	}
	if !common.IsXMRUnknownSenderAddress(parsed) {
		t.Fatalf("expected unknown sender marker to be detectable")
	}

	mockAddr := unknownSenderAddress("tx-hash-b", "4abcd")
	parsedMock, err := common.NewAddress(mockAddr)
	if err != nil {
		t.Fatalf("expected parseable unknown sender marker for alternate tx hash: %v", err)
	}
	if !common.IsXMRUnknownSenderAddress(parsedMock) {
		t.Fatalf("expected alternate unknown sender marker to be detectable")
	}
	if addr == mockAddr {
		t.Fatalf("expected unique marker addresses per tx hash")
	}
}

func TestTransferToTxInItemUsesFeeFallback(t *testing.T) {
	pk := common.PubKey("thorpub1testvault")

	s := &Scanner{
		logger:             log.Logger,
		currentBlockHeight: atomic.NewInt64(0),
		lastFee:            atomic.NewUint64(123),
	}
	item, ok := s.transferToTxInItem(xmrFrostTransferEntry{
		TxID:   "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
		Amount: 10000,
		Fee:    0,
	}, unknownSenderAddress("sender", "8abc"), unknownSenderAddress("receiver", "8def"), pk, "", 100)
	if !ok {
		t.Fatal("expected transfer to convert to tx in item")
	}
	if item.Gas[0].Amount.Uint64() != 123 {
		t.Fatalf("expected fallback fee 123, got %d", item.Gas[0].Amount.Uint64())
	}
}

func TestResolveOutgoingSenderPrefersExplicitMetadata(t *testing.T) {
	vaultPubKey, vaultAddr := testVaultPubKeyAndAddress(t)
	s := &Scanner{
		logger:             log.Logger,
		currentBlockHeight: atomic.NewInt64(0),
		lastFee:            atomic.NewUint64(0),
	}

	sender, pk := s.resolveOutgoingSender(xmrFrostTransferEntry{
		TxID:        "tx-1",
		FromAddress: vaultAddr.String(),
		VaultPubKey: vaultPubKey.String(),
	})
	if sender != vaultAddr.String() {
		t.Fatalf("expected explicit sender %s, got %s", vaultAddr, sender)
	}
	if !pk.Equals(vaultPubKey) {
		t.Fatalf("expected explicit vault pubkey %s, got %s", vaultPubKey, pk)
	}
}

func TestResolveOutgoingSenderRejectsMissingExplicitMetadata(t *testing.T) {
	vaultPubKey, vaultAddr := testVaultPubKeyAndAddress(t)
	s := &Scanner{
		logger:             log.Logger,
		currentBlockHeight: atomic.NewInt64(0),
		lastFee:            atomic.NewUint64(0),
	}

	sender, pk := s.resolveOutgoingSender(xmrFrostTransferEntry{
		TxID:        "tx-missing-sender",
		FromAddress: "",
		VaultPubKey: vaultPubKey.String(),
	})
	if sender != "" || !pk.IsEmpty() {
		t.Fatalf("expected missing sender to fail closed, got sender=%q pk=%s", sender, pk.String())
	}

	sender, pk = s.resolveOutgoingSender(xmrFrostTransferEntry{
		TxID:        "tx-missing-vault",
		FromAddress: vaultAddr.String(),
		VaultPubKey: "",
	})
	if sender != "" || !pk.IsEmpty() {
		t.Fatalf("expected missing vault key to fail closed, got sender=%q pk=%s", sender, pk.String())
	}
}

func TestResolveOutgoingSenderRejectsSenderVaultMismatch(t *testing.T) {
	vaultPubKey, _ := testVaultPubKeyAndAddress(t)
	_, otherAddr := testVaultPubKeyAndAddress(t)
	s := &Scanner{
		logger:             log.Logger,
		currentBlockHeight: atomic.NewInt64(0),
		lastFee:            atomic.NewUint64(0),
	}

	sender, pk := s.resolveOutgoingSender(xmrFrostTransferEntry{
		TxID:        "tx-mismatch",
		FromAddress: otherAddr.String(),
		VaultPubKey: vaultPubKey.String(),
	})
	if sender != "" || !pk.IsEmpty() {
		t.Fatalf("expected sender/vault mismatch to fail closed, got sender=%q pk=%s", sender, pk.String())
	}
}

func TestDedupeTxInItemsByTxIDKeepsDistinctRecipientsByAmountOrMemo(t *testing.T) {
	pk := common.PubKey("thorpub1testvault")
	newItem := func(amount uint64, memo string) *stypes.TxInItem {
		return &stypes.TxInItem{
			Tx:                  "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
			Memo:                memo,
			Sender:              "48from",
			To:                  "48to",
			Coins:               common.NewCoins(common.NewCoin(common.XMRAsset, cosmos.NewUint(amount))),
			Gas:                 common.Gas{common.NewCoin(common.XMRAsset, cosmos.NewUint(1))},
			ObservedVaultPubKey: pk,
		}
	}

	s := &Scanner{}
	items := []*stypes.TxInItem{
		newItem(10, ""),
		newItem(20, ""),
		newItem(20, "OUT:distinct"),
	}

	deduped := s.dedupeTxInItemsByTxID(items)
	if len(deduped) != 3 {
		t.Fatalf("expected 3 distinct observations to survive dedupe, got %d", len(deduped))
	}
}
