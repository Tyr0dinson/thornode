package observer

import (
    "errors"
    "testing"

    "github.com/rs/zerolog"
    "github.com/stretchr/testify/require"

    btypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
    "gitlab.com/thorchain/thornode/v3/common"
    "gitlab.com/thorchain/thornode/v3/common/cosmos"
    stypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

func TestLoserSignal_SchedulesRefund(t *testing.T) {
    logger := zerolog.New(zerolog.NewTestWriter(t))
    mockBridge := &MockThorchainBridgeForMemoless{}
    registeredMemo := &stypes.RegisteredMemo{
        Reference:    "12345",
        Memo:         "SWAP:BTC.BTC",
        ChainId:      "BTC",
        RegisteredAt: 100,
        ExpiresAt:    144100,
        Status:       "active",
    }
    mockBridge.getRegisteredMemoFunc = func(reference string) (*stypes.RegisteredMemo, error) {
        if reference == "12345" { return registeredMemo, nil }
        return nil, errors.New("not found")
    }

    handler := NewMemolessHandler(mockBridge, logger)
    handler.collisionWiringEnabled = true
    // Provide two candidates: winner has (height 100, txid "a"), current is loser (height 100, txid "b")
    handler.candidateProvider = func(txIn *btypes.TxInItem, reference, chainID string) []CollisionCandidate {
        return []CollisionCandidate{
            {ChainID: chainID, VaultPubKey: txIn.ObservedVaultPubKey.String(), Reference: reference, InboundHeight: 100, TxID: "a"},
            {ChainID: chainID, VaultPubKey: txIn.ObservedVaultPubKey.String(), Reference: reference, InboundHeight: 100, TxID: txIn.Tx},
        }
    }

    txIn := &btypes.TxInItem{ Memo: "", Coins: common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(100012345))}, Tx: "b" }
    _, err := handler.ProcessMemolessTransaction(txIn)
    require.NoError(t, err)
    // Refund record should be present for loser txid "b"
    require.True(t, handler.rr.HasRefundRecord("b"))
}

func TestWinner_NoRefund(t *testing.T) {
    logger := zerolog.New(zerolog.NewTestWriter(t))
    mockBridge := &MockThorchainBridgeForMemoless{}
    registeredMemo := &stypes.RegisteredMemo{ Reference: "12345", Memo: "SWAP:BTC.BTC", ChainId: "BTC", RegisteredAt: 100, ExpiresAt: 144100, Status: "active" }
    mockBridge.getRegisteredMemoFunc = func(reference string) (*stypes.RegisteredMemo, error) { return registeredMemo, nil }

    handler := NewMemolessHandler(mockBridge, logger)
    handler.collisionWiringEnabled = true
    // Provide two candidates: current is winner (height 100, txid "a")
    handler.candidateProvider = func(txIn *btypes.TxInItem, reference, chainID string) []CollisionCandidate {
        return []CollisionCandidate{
            {ChainID: chainID, VaultPubKey: txIn.ObservedVaultPubKey.String(), Reference: reference, InboundHeight: 100, TxID: txIn.Tx},
            {ChainID: chainID, VaultPubKey: txIn.ObservedVaultPubKey.String(), Reference: reference, InboundHeight: 100, TxID: "z"},
        }
    }

    txIn := &btypes.TxInItem{ Memo: "", Coins: common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(100012345))}, Tx: "a" }
    _, err := handler.ProcessMemolessTransaction(txIn)
    require.NoError(t, err)
    // No refund record for winner txid
    require.False(t, handler.rr.HasRefundRecord("a"))
}
