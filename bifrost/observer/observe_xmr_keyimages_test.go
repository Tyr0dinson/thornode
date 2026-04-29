package observer

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients"
	chainclienttypes "gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/types"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
)

type mockObservedTxInValidator struct {
	mockChainClient
	validate func(common.ObservedTx) error
}

func (m *mockObservedTxInValidator) GetChain() common.Chain {
	return common.XMRChain
}

func (m *mockObservedTxInValidator) ValidateObservedTxIn(tx common.ObservedTx) error {
	if m.validate != nil {
		return m.validate(tx)
	}
	return nil
}

var _ chainclients.ChainClient = (*mockObservedTxInValidator)(nil)
var _ chainclienttypes.ObservedTxInValidator = (*mockObservedTxInValidator)(nil)

func TestValidateInboundXMRKeyImagesDefersDeckPersistenceWhileLockHeld(t *testing.T) {
	storage, tempDir := setupTestDB(t)
	defer cleanupTestDB(t, storage, tempDir)

	tx := common.ObservedTx{
		Tx: common.Tx{
			ID:          common.TxID(stringsRepeat("d", 64)),
			Chain:       common.XMRChain,
			FromAddress: common.Address(common.XMRSeededDummyAddress('8', []byte("sender"))),
			ToAddress:   common.Address(common.XMRSeededDummyAddress('4', []byte("vault"))),
			Memo:        "ADD:XMR.XMR:cthor1deadbeef",
		},
		BlockHeight:    100,
		FinaliseHeight: 100,
		ObservedPubKey: common.PubKey("thorpub1vault"),
		XmrKeyImages: []*common.XmrKeyImageProof{
			{TxHash: stringsRepeat("d", 64), OutputIndex: 0, KeyImage: "ki0", ProofC: "c0", ProofS: "s0"},
		},
	}

	validated := make(chan common.TxID, 1)
	validator := &mockObservedTxInValidator{
		validate: func(got common.ObservedTx) error {
			validated <- got.Tx.ID
			return nil
		},
	}

	deckKey := txInKey{chain: common.XMRChain, height: tx.FinaliseHeight}
	deck := &types.TxIn{
		Chain: common.XMRChain,
		TxArray: []*types.TxInItem{
			{
				BlockHeight:         tx.BlockHeight,
				Tx:                  tx.Tx.ID.String(),
				Memo:                tx.Tx.Memo,
				Sender:              tx.Tx.FromAddress.String(),
				To:                  tx.Tx.ToAddress.String(),
				Coins:               tx.Tx.Coins,
				Gas:                 tx.Tx.Gas,
				ObservedVaultPubKey: tx.ObservedPubKey,
			},
		},
	}

	observer := &Observer{
		logger:  zerolog.Nop(),
		chains:  map[common.Chain]chainclients.ChainClient{common.XMRChain: validator},
		onDeck:  map[txInKey]*types.TxIn{deckKey: deck},
		lock:    &sync.Mutex{},
		storage: storage,
	}

	observer.lock.Lock()
	done := make(chan error, 1)
	go func() {
		done <- observer.ValidateInboundXMRKeyImages(tx)
	}()

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(250 * time.Millisecond):
		t.Fatal("ValidateInboundXMRKeyImages blocked while observer deck lock was held")
	}

	select {
	case got := <-validated:
		require.Equal(t, tx.Tx.ID, got)
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for XMR inbound validation")
	}

	require.Nil(t, deck.TxArray[0].XmrKeyImages, "deck should not be updated until the lock is released")
	observer.lock.Unlock()

	require.Eventually(t, func() bool {
		observer.lock.Lock()
		defer observer.lock.Unlock()
		proofs := observer.onDeck[deckKey].TxArray[0].XmrKeyImages
		return len(proofs) == 1 && proofs[0].KeyImage == "ki0"
	}, time.Second, 10*time.Millisecond)

}

func TestProcessObservedTxMergesXMRMetadataIntoExistingDeckItem(t *testing.T) {
	storage, tempDir := setupTestDB(t)
	defer cleanupTestDB(t, storage, tempDir)

	txHash := stringsRepeat("e", 64)
	vaultPubKey := common.PubKey("thorpub1vault")
	base := &types.TxInItem{
		BlockHeight:         100,
		Tx:                  txHash,
		Memo:                "REFUND:" + stringsRepeat("a", 64),
		Sender:              common.XMRSeededDummyAddress('8', []byte("sender")),
		To:                  common.XMRSeededDummyAddress('4', []byte("vault")),
		ObservedVaultPubKey: vaultPubKey,
		XmrKeyImages: []*common.XmrKeyImageProof{
			{TxHash: txHash, OutputIndex: 1, KeyImage: "stale-cross-vault-ki", ProofC: "stale-c", ProofS: "stale-s"},
		},
	}
	deckKey := txInKey{chain: common.XMRChain, height: 100}
	deck := &types.TxIn{
		Chain:    common.XMRChain,
		TxArray:  []*types.TxInItem{base},
		Filtered: true,
	}

	observer := &Observer{
		logger:  zerolog.Nop(),
		onDeck:  map[txInKey]*types.TxIn{deckKey: deck},
		lock:    &sync.Mutex{},
		storage: storage,
	}

	replay := *base
	replay.XmrKeyImages = []*common.XmrKeyImageProof{
		{TxHash: txHash, OutputIndex: 0, KeyImage: "ki0", ProofC: "c0", ProofS: "s0"},
	}
	observer.processObservedTx(types.TxIn{
		Chain:    common.XMRChain,
		TxArray:  []*types.TxInItem{&replay},
		Filtered: true,
	})

	require.Len(t, deck.TxArray, 1)
	require.Len(t, deck.TxArray[0].XmrKeyImages, 1)
	require.Equal(t, "ki0", deck.TxArray[0].XmrKeyImages[0].KeyImage)
	require.Equal(t, uint32(0), deck.TxArray[0].XmrKeyImages[0].OutputIndex)
}

func TestProcessObservedTxMergesXMRMetadataIntoDuplicateDeckItems(t *testing.T) {
	storage, tempDir := setupTestDB(t)
	defer cleanupTestDB(t, storage, tempDir)

	txHash := stringsRepeat("f", 64)
	vaultPubKey := common.PubKey("thorpub1vault")
	base := &types.TxInItem{
		BlockHeight:         100,
		Tx:                  txHash,
		Memo:                "REFUND:" + stringsRepeat("b", 64),
		Sender:              common.XMRSeededDummyAddress('8', []byte("sender")),
		To:                  common.XMRSeededDummyAddress('4', []byte("vault")),
		ObservedVaultPubKey: vaultPubKey,
	}
	duplicate := base.Copy()
	duplicate.Tx = strings.ToUpper(txHash)

	deckKey := txInKey{chain: common.XMRChain, height: 100}
	deck := &types.TxIn{
		Chain:    common.XMRChain,
		TxArray:  []*types.TxInItem{base, duplicate},
		Filtered: true,
	}
	observer := &Observer{
		logger:  zerolog.Nop(),
		onDeck:  map[txInKey]*types.TxIn{deckKey: deck},
		lock:    &sync.Mutex{},
		storage: storage,
	}

	replay := base.Copy()
	replay.XmrKeyImages = []*common.XmrKeyImageProof{
		{TxHash: txHash, OutputIndex: 0, KeyImage: "ki0", ProofC: "c0", ProofS: "s0"},
	}
	observer.processObservedTx(types.TxIn{
		Chain:    common.XMRChain,
		TxArray:  []*types.TxInItem{replay},
		Filtered: true,
	})

	require.Len(t, deck.TxArray, 2)
	for _, item := range deck.TxArray {
		require.Len(t, item.XmrKeyImages, 1)
		require.Equal(t, "ki0", item.XmrKeyImages[0].KeyImage)
	}
}

func TestFilteredObservationScopesXMRMetadataToSourceVault(t *testing.T) {
	sourceVault := common.PubKey("thorpub1source")
	otherVault := common.PubKey("thorpub1other")
	item := &types.TxInItem{
		Tx:                  stringsRepeat("1", 64),
		ObservedVaultPubKey: sourceVault,
		TxKeys:              []string{"tx-key"},
		XmrKeyImages: []*common.XmrKeyImageProof{
			{TxHash: stringsRepeat("1", 64), OutputIndex: 0, KeyImage: "ki0", ProofC: "c0", ProofS: "s0"},
		},
	}

	sameVault := filteredObservationForVault(item, sourceVault)
	require.Equal(t, []string{"tx-key"}, sameVault.TxKeys)
	require.Len(t, sameVault.XmrKeyImages, 1)

	crossVault := filteredObservationForVault(item, otherVault)
	require.Empty(t, crossVault.TxKeys)
	require.Empty(t, crossVault.XmrKeyImages)
	require.Equal(t, otherVault, crossVault.ObservedVaultPubKey)
}
