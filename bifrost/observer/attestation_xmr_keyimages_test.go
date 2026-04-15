package observer

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"gitlab.com/thorchain/thornode/v3/common"
)

func TestHandleObservedTxAttestationCoAttestsFinalXMRInboundAfterValidation(t *testing.T) {
	ag, _, _, _, _, _ := setupTestGossip(t)

	origVerifySignature := verifySignature
	defer func() { verifySignature = origVerifySignature }()
	verifySignature = func(signBz []byte, signature []byte, attester []byte) error {
		return nil
	}

	validated := make(chan common.TxID, 1)
	ag.observerValidateInboundXMRKeyImages = func(tx common.ObservedTx) error {
		validated <- tx.Tx.ID
		return nil
	}

	tx := common.ObservedTx{
		Tx: common.Tx{
			ID:          common.TxID(stringsRepeat("e", 64)),
			Chain:       common.XMRChain,
			FromAddress: common.Address(common.XMRSeededDummyAddress('8', []byte("sender"))),
			ToAddress:   common.Address(common.XMRSeededDummyAddress('4', []byte("vault"))),
		},
		BlockHeight:    120,
		FinaliseHeight: 120,
		ObservedPubKey: common.PubKey("thorpub1vault"),
		XmrKeyImages: []*common.XmrKeyImageProof{
			{TxHash: stringsRepeat("e", 64), OutputIndex: 0, KeyImage: "ki0", ProofC: "c0", ProofS: "s0"},
		},
	}

	privKey := secp256k1.GenPrivKey()
	att := common.AttestTx{
		ObsTx:       tx,
		Inbound:     true,
		Attestation: &common.Attestation{PubKey: privKey.PubKey().Bytes(), Signature: []byte("sig")},
	}
	ag.handleObservedTxAttestation(context.Background(), att)

	select {
	case got := <-validated:
		assert.Equal(t, tx.Tx.ID, got)
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for XMR key-image validation")
	}

	key, err := observedTxKey(tx, false, true)
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		ag.mu.Lock()
		defer ag.mu.Unlock()
		state := ag.observedTxs[key]
		return state != nil && state.AttestationCount() == 2
	}, time.Second, 10*time.Millisecond)
}

func TestHandleObservedTxAttestationDoesNotCoAttestInvalidFinalXMRInbound(t *testing.T) {
	ag, _, _, _, _, _ := setupTestGossip(t)

	origVerifySignature := verifySignature
	defer func() { verifySignature = origVerifySignature }()
	verifySignature = func(signBz []byte, signature []byte, attester []byte) error {
		return nil
	}

	ag.observerValidateInboundXMRKeyImages = func(tx common.ObservedTx) error {
		return fmt.Errorf("invalid proof set")
	}

	tx := common.ObservedTx{
		Tx: common.Tx{
			ID:          common.TxID(stringsRepeat("f", 64)),
			Chain:       common.XMRChain,
			FromAddress: common.Address(common.XMRSeededDummyAddress('8', []byte("sender"))),
			ToAddress:   common.Address(common.XMRSeededDummyAddress('4', []byte("vault"))),
		},
		BlockHeight:    130,
		FinaliseHeight: 130,
		ObservedPubKey: common.PubKey("thorpub1vault"),
		XmrKeyImages: []*common.XmrKeyImageProof{
			{TxHash: stringsRepeat("f", 64), OutputIndex: 0, KeyImage: "ki0", ProofC: "c0", ProofS: "s0"},
		},
	}

	privKey := secp256k1.GenPrivKey()
	att := common.AttestTx{
		ObsTx:       tx,
		Inbound:     true,
		Attestation: &common.Attestation{PubKey: privKey.PubKey().Bytes(), Signature: []byte("sig")},
	}
	ag.handleObservedTxAttestation(context.Background(), att)

	key, err := observedTxKey(tx, false, true)
	require.NoError(t, err)
	time.Sleep(100 * time.Millisecond)

	ag.mu.Lock()
	defer ag.mu.Unlock()
	state := ag.observedTxs[key]
	require.NotNil(t, state)
	assert.Equal(t, 1, state.AttestationCount())
}

func TestRetryPendingInboundXMRKeyImageAttestationsCoAttestsAfterValidationSucceeds(t *testing.T) {
	ag, _, _, _, _, _ := setupTestGossip(t)

	origVerifySignature := verifySignature
	defer func() { verifySignature = origVerifySignature }()
	verifySignature = func(signBz []byte, signature []byte, attester []byte) error {
		return nil
	}

	var validateCalls atomic.Int32
	ag.observerValidateInboundXMRKeyImages = func(tx common.ObservedTx) error {
		if validateCalls.Add(1) == 1 {
			return fmt.Errorf("outputs not ready")
		}
		return nil
	}

	tx := common.ObservedTx{
		Tx: common.Tx{
			ID:          common.TxID(stringsRepeat("a", 64)),
			Chain:       common.XMRChain,
			FromAddress: common.Address(common.XMRSeededDummyAddress('8', []byte("sender"))),
			ToAddress:   common.Address(common.XMRSeededDummyAddress('4', []byte("vault"))),
		},
		BlockHeight:    140,
		FinaliseHeight: 140,
		ObservedPubKey: common.PubKey("thorpub1vault"),
		XmrKeyImages: []*common.XmrKeyImageProof{
			{TxHash: stringsRepeat("a", 64), OutputIndex: 0, KeyImage: "ki0", ProofC: "c0", ProofS: "s0"},
		},
	}

	privKey := secp256k1.GenPrivKey()
	att := common.AttestTx{
		ObsTx:       tx,
		Inbound:     true,
		Attestation: &common.Attestation{PubKey: privKey.PubKey().Bytes(), Signature: []byte("sig")},
	}
	ag.handleObservedTxAttestation(context.Background(), att)

	key, err := observedTxKey(tx, false, true)
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		return validateCalls.Load() >= 1
	}, time.Second, 10*time.Millisecond)

	ag.mu.Lock()
	state := ag.observedTxs[key]
	ag.mu.Unlock()
	require.NotNil(t, state)
	state.mu.Lock()
	assert.Equal(t, 1, state.AttestationCount())
	state.mu.Unlock()

	require.Eventually(t, func() bool {
		ag.retryPendingInboundXMRKeyImageAttestations(context.Background())
		ag.mu.Lock()
		defer ag.mu.Unlock()
		state := ag.observedTxs[key]
		return validateCalls.Load() >= 2 && state != nil && state.AttestationCount() == 2
	}, time.Second, 10*time.Millisecond)
}

func TestHandleObservedTxAttestationSeparatesFinalXMRPayloadBuckets(t *testing.T) {
	ag, _, _, _, _, _ := setupTestGossip(t)

	origVerifySignature := verifySignature
	defer func() { verifySignature = origVerifySignature }()
	verifySignature = func(signBz []byte, signature []byte, attester []byte) error {
		return nil
	}

	baseTx := common.ObservedTx{
		Tx: common.Tx{
			ID:          common.TxID(stringsRepeat("b", 64)),
			Chain:       common.XMRChain,
			FromAddress: common.Address(common.XMRSeededDummyAddress('8', []byte("sender"))),
			ToAddress:   common.Address(common.XMRSeededDummyAddress('4', []byte("vault"))),
		},
		BlockHeight:    150,
		FinaliseHeight: 150,
		ObservedPubKey: common.PubKey("thorpub1vault"),
	}

	wrongTx := baseTx
	wrongTx.XmrKeyImages = []*common.XmrKeyImageProof{
		{TxHash: stringsRepeat("b", 64), OutputIndex: 0, KeyImage: "wrong", ProofC: "c0", ProofS: "s0"},
	}
	correctTx := baseTx
	correctTx.XmrKeyImages = []*common.XmrKeyImageProof{
		{TxHash: stringsRepeat("b", 64), OutputIndex: 0, KeyImage: "correct", ProofC: "c1", ProofS: "s1"},
	}

	firstKey := secp256k1.GenPrivKey()
	secondKey := secp256k1.GenPrivKey()
	ag.handleObservedTxAttestation(context.Background(), common.AttestTx{
		ObsTx:       wrongTx,
		Inbound:     true,
		Attestation: &common.Attestation{PubKey: firstKey.PubKey().Bytes(), Signature: []byte("sig-a")},
	})
	ag.handleObservedTxAttestation(context.Background(), common.AttestTx{
		ObsTx:       correctTx,
		Inbound:     true,
		Attestation: &common.Attestation{PubKey: secondKey.PubKey().Bytes(), Signature: []byte("sig-b")},
	})

	wrongKey, err := observedTxKey(wrongTx, false, true)
	require.NoError(t, err)
	correctKey, err := observedTxKey(correctTx, false, true)
	require.NoError(t, err)
	require.NotEqual(t, wrongKey.PayloadHash, correctKey.PayloadHash)

	ag.mu.Lock()
	defer ag.mu.Unlock()
	require.NotNil(t, ag.observedTxs[wrongKey])
	require.NotNil(t, ag.observedTxs[correctKey])
	assert.Equal(t, 1, ag.observedTxs[wrongKey].AttestationCount())
	assert.Equal(t, 1, ag.observedTxs[correctKey].AttestationCount())
}

func TestRetryPendingInboundXMRKeyImageAttestationsDedupesInFlightValidation(t *testing.T) {
	ag, _, _, _, _, _ := setupTestGossip(t)

	origVerifySignature := verifySignature
	defer func() { verifySignature = origVerifySignature }()
	verifySignature = func(signBz []byte, signature []byte, attester []byte) error {
		return nil
	}

	var validateCalls atomic.Int32
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	ag.observerValidateInboundXMRKeyImages = func(tx common.ObservedTx) error {
		if validateCalls.Add(1) == 1 {
			started <- struct{}{}
		}
		<-release
		return nil
	}

	tx := common.ObservedTx{
		Tx: common.Tx{
			ID:          common.TxID(stringsRepeat("c", 64)),
			Chain:       common.XMRChain,
			FromAddress: common.Address(common.XMRSeededDummyAddress('8', []byte("sender"))),
			ToAddress:   common.Address(common.XMRSeededDummyAddress('4', []byte("vault"))),
		},
		BlockHeight:    160,
		FinaliseHeight: 160,
		ObservedPubKey: common.PubKey("thorpub1vault"),
		XmrKeyImages: []*common.XmrKeyImageProof{
			{TxHash: stringsRepeat("c", 64), OutputIndex: 0, KeyImage: "ki0", ProofC: "c0", ProofS: "s0"},
		},
	}

	privKey := secp256k1.GenPrivKey()
	att := common.AttestTx{
		ObsTx:       tx,
		Inbound:     true,
		Attestation: &common.Attestation{PubKey: privKey.PubKey().Bytes(), Signature: []byte("sig")},
	}
	ag.handleObservedTxAttestation(context.Background(), att)

	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for first validation to start")
	}

	ag.retryPendingInboundXMRKeyImageAttestations(context.Background())
	ag.retryPendingInboundXMRKeyImageAttestations(context.Background())
	time.Sleep(100 * time.Millisecond)
	assert.Equal(t, int32(1), validateCalls.Load())

	close(release)

	key, err := observedTxKey(tx, false, true)
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		ag.mu.Lock()
		defer ag.mu.Unlock()
		state := ag.observedTxs[key]
		return state != nil && state.AttestationCount() == 2
	}, time.Second, 10*time.Millisecond)
}

func TestHandleObservedTxAttestationBoundsDistinctInboundXMRValidations(t *testing.T) {
	ag, _, _, _, _, _ := setupTestGossip(t)

	origVerifySignature := verifySignature
	defer func() { verifySignature = origVerifySignature }()
	verifySignature = func(signBz []byte, signature []byte, attester []byte) error {
		return nil
	}

	var startedCount atomic.Int32
	var activeCount atomic.Int32
	var maxActive atomic.Int32
	firstRelease := make(chan struct{})
	secondStarted := make(chan common.TxID, 1)
	firstTxID := common.TxID(stringsRepeat("d", 64))
	secondTxID := common.TxID(stringsRepeat("e", 64))
	ag.observerValidateInboundXMRKeyImages = func(tx common.ObservedTx) error {
		currentActive := activeCount.Add(1)
		for {
			prev := maxActive.Load()
			if currentActive <= prev || maxActive.CompareAndSwap(prev, currentActive) {
				break
			}
		}
		callNum := startedCount.Add(1)
		defer activeCount.Add(-1)
		if tx.Tx.ID.Equals(firstTxID) {
			if callNum == 1 {
				<-firstRelease
			}
			return nil
		}
		secondStarted <- tx.Tx.ID
		return nil
	}

	makeTx := func(id common.TxID) common.ObservedTx {
		return common.ObservedTx{
			Tx: common.Tx{
				ID:          id,
				Chain:       common.XMRChain,
				FromAddress: common.Address(common.XMRSeededDummyAddress('8', []byte("sender"))),
				ToAddress:   common.Address(common.XMRSeededDummyAddress('4', []byte("vault"))),
			},
			BlockHeight:    170,
			FinaliseHeight: 170,
			ObservedPubKey: common.PubKey("thorpub1vault"),
			XmrKeyImages: []*common.XmrKeyImageProof{
				{TxHash: id.String(), OutputIndex: 0, KeyImage: "ki0", ProofC: "c0", ProofS: "s0"},
			},
		}
	}

	privKey1 := secp256k1.GenPrivKey()
	privKey2 := secp256k1.GenPrivKey()
	ag.handleObservedTxAttestation(context.Background(), common.AttestTx{
		ObsTx:       makeTx(firstTxID),
		Inbound:     true,
		Attestation: &common.Attestation{PubKey: privKey1.PubKey().Bytes(), Signature: []byte("sig-1")},
	})

	require.Eventually(t, func() bool {
		return startedCount.Load() == 1
	}, time.Second, 10*time.Millisecond)

	ag.handleObservedTxAttestation(context.Background(), common.AttestTx{
		ObsTx:       makeTx(secondTxID),
		Inbound:     true,
		Attestation: &common.Attestation{PubKey: privKey2.PubKey().Bytes(), Signature: []byte("sig-2")},
	})

	time.Sleep(100 * time.Millisecond)
	assert.Equal(t, int32(1), startedCount.Load())
	assert.Equal(t, int32(1), maxActive.Load())

	close(firstRelease)

	require.Eventually(t, func() bool {
		return startedCount.Load() >= 2
	}, time.Second, 10*time.Millisecond)

	got := <-secondStarted
	assert.Equal(t, secondTxID, got)
	assert.Equal(t, int32(1), maxActive.Load())
}

func TestHandleObservedTxAttestationCoAttestsFinalXMROutboundAfterKeyImageValidation(t *testing.T) {
	ag, _, _, _, _, _ := setupTestGossip(t)

	origVerifySignature := verifySignature
	defer func() { verifySignature = origVerifySignature }()
	verifySignature = func(signBz []byte, signature []byte, attester []byte) error {
		return nil
	}

	validated := make(chan common.TxID, 1)
	ag.SetObserverValidateXMRTx(func(tx common.ObservedTx) error {
		validated <- tx.Tx.ID
		return nil
	})

	tx := common.ObservedTx{
		Tx: common.Tx{
			ID:          common.TxID(stringsRepeat("1", 64)),
			Chain:       common.XMRChain,
			FromAddress: common.Address(common.XMRSeededDummyAddress('4', []byte("vault"))),
			ToAddress:   common.Address(common.XMRSeededDummyAddress('8', []byte("external"))),
		},
		BlockHeight:    180,
		FinaliseHeight: 180,
		ObservedPubKey: common.PubKey("thorpub1vault"),
		TxKeys:         []string{stringsRepeat("a", 64)},
		XmrKeyImages: []*common.XmrKeyImageProof{
			{TxHash: stringsRepeat("1", 64), OutputIndex: 0, KeyImage: "ki0", ProofC: "c0", ProofS: "s0"},
		},
	}

	privKey := secp256k1.GenPrivKey()
	att := common.AttestTx{
		ObsTx:       tx,
		Inbound:     false,
		Attestation: &common.Attestation{PubKey: privKey.PubKey().Bytes(), Signature: []byte("sig")},
	}
	ag.handleObservedTxAttestation(context.Background(), att)

	select {
	case got := <-validated:
		assert.Equal(t, tx.Tx.ID, got)
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for outbound key-image validation")
	}

	key, err := observedTxKey(tx, false, false)
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		ag.mu.Lock()
		defer ag.mu.Unlock()
		state := ag.observedTxs[key]
		return state != nil && state.AttestationCount() == 2
	}, time.Second, 10*time.Millisecond)
}

func TestRetryPendingInboundXMRKeyImageAttestationsRetriesFinalXMROutboundWithKeyImages(t *testing.T) {
	ag, _, _, _, _, _ := setupTestGossip(t)

	origVerifySignature := verifySignature
	defer func() { verifySignature = origVerifySignature }()
	verifySignature = func(signBz []byte, signature []byte, attester []byte) error {
		return nil
	}

	var validateCalls atomic.Int32
	ag.SetObserverValidateXMRTx(func(tx common.ObservedTx) error {
		if validateCalls.Add(1) == 1 {
			return fmt.Errorf("outputs not ready")
		}
		return nil
	})

	tx := common.ObservedTx{
		Tx: common.Tx{
			ID:          common.TxID(stringsRepeat("2", 64)),
			Chain:       common.XMRChain,
			FromAddress: common.Address(common.XMRSeededDummyAddress('4', []byte("vault"))),
			ToAddress:   common.Address(common.XMRSeededDummyAddress('8', []byte("external"))),
		},
		BlockHeight:    190,
		FinaliseHeight: 190,
		ObservedPubKey: common.PubKey("thorpub1vault"),
		TxKeys:         []string{stringsRepeat("b", 64)},
		XmrKeyImages: []*common.XmrKeyImageProof{
			{TxHash: stringsRepeat("2", 64), OutputIndex: 0, KeyImage: "ki0", ProofC: "c0", ProofS: "s0"},
		},
	}

	privKey := secp256k1.GenPrivKey()
	att := common.AttestTx{
		ObsTx:       tx,
		Inbound:     false,
		Attestation: &common.Attestation{PubKey: privKey.PubKey().Bytes(), Signature: []byte("sig")},
	}
	ag.handleObservedTxAttestation(context.Background(), att)

	key, err := observedTxKey(tx, false, false)
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		return validateCalls.Load() >= 1
	}, time.Second, 10*time.Millisecond)

	ag.mu.Lock()
	state := ag.observedTxs[key]
	ag.mu.Unlock()
	require.NotNil(t, state)
	state.mu.Lock()
	assert.Equal(t, 1, state.AttestationCount())
	state.mu.Unlock()

	require.Eventually(t, func() bool {
		ag.retryPendingInboundXMRKeyImageAttestations(context.Background())
		ag.mu.Lock()
		defer ag.mu.Unlock()
		state := ag.observedTxs[key]
		return validateCalls.Load() >= 2 && state != nil && state.AttestationCount() == 2
	}, time.Second, 10*time.Millisecond)
}

func TestHandleObservedTxAttestationSerializesInboundAndOutboundXMRKeyImageValidations(t *testing.T) {
	ag, _, _, _, _, _ := setupTestGossip(t)

	origVerifySignature := verifySignature
	defer func() { verifySignature = origVerifySignature }()
	verifySignature = func(signBz []byte, signature []byte, attester []byte) error {
		return nil
	}

	var startedCount atomic.Int32
	var activeCount atomic.Int32
	var maxActive atomic.Int32
	firstRelease := make(chan struct{})
	secondStarted := make(chan common.TxID, 1)
	inboundTxID := common.TxID(stringsRepeat("3", 64))
	outboundTxID := common.TxID(stringsRepeat("4", 64))

	ag.observerValidateInboundXMRKeyImages = func(tx common.ObservedTx) error {
		currentActive := activeCount.Add(1)
		for {
			prev := maxActive.Load()
			if currentActive <= prev || maxActive.CompareAndSwap(prev, currentActive) {
				break
			}
		}
		callNum := startedCount.Add(1)
		defer activeCount.Add(-1)
		if tx.Tx.ID.Equals(inboundTxID) && callNum == 1 {
			<-firstRelease
			return nil
		}
		secondStarted <- tx.Tx.ID
		return nil
	}
	ag.SetObserverValidateXMRTx(func(tx common.ObservedTx) error {
		currentActive := activeCount.Add(1)
		for {
			prev := maxActive.Load()
			if currentActive <= prev || maxActive.CompareAndSwap(prev, currentActive) {
				break
			}
		}
		callNum := startedCount.Add(1)
		defer activeCount.Add(-1)
		if tx.Tx.ID.Equals(inboundTxID) && callNum == 1 {
			<-firstRelease
			return nil
		}
		secondStarted <- tx.Tx.ID
		return nil
	})

	inboundTx := common.ObservedTx{
		Tx: common.Tx{
			ID:          inboundTxID,
			Chain:       common.XMRChain,
			FromAddress: common.Address(common.XMRSeededDummyAddress('8', []byte("sender"))),
			ToAddress:   common.Address(common.XMRSeededDummyAddress('4', []byte("vault"))),
		},
		BlockHeight:    200,
		FinaliseHeight: 200,
		ObservedPubKey: common.PubKey("thorpub1vault"),
		XmrKeyImages: []*common.XmrKeyImageProof{
			{TxHash: inboundTxID.String(), OutputIndex: 0, KeyImage: "ki0", ProofC: "c0", ProofS: "s0"},
		},
	}
	outboundTx := common.ObservedTx{
		Tx: common.Tx{
			ID:          outboundTxID,
			Chain:       common.XMRChain,
			FromAddress: common.Address(common.XMRSeededDummyAddress('4', []byte("vault"))),
			ToAddress:   common.Address(common.XMRSeededDummyAddress('8', []byte("external"))),
		},
		BlockHeight:    200,
		FinaliseHeight: 200,
		ObservedPubKey: common.PubKey("thorpub1vault"),
		TxKeys:         []string{stringsRepeat("c", 64)},
		XmrKeyImages: []*common.XmrKeyImageProof{
			{TxHash: outboundTxID.String(), OutputIndex: 0, KeyImage: "ki1", ProofC: "c1", ProofS: "s1"},
		},
	}

	privKey1 := secp256k1.GenPrivKey()
	privKey2 := secp256k1.GenPrivKey()
	ag.handleObservedTxAttestation(context.Background(), common.AttestTx{
		ObsTx:       inboundTx,
		Inbound:     true,
		Attestation: &common.Attestation{PubKey: privKey1.PubKey().Bytes(), Signature: []byte("sig-1")},
	})

	require.Eventually(t, func() bool {
		return startedCount.Load() == 1
	}, time.Second, 10*time.Millisecond)

	ag.handleObservedTxAttestation(context.Background(), common.AttestTx{
		ObsTx:       outboundTx,
		Inbound:     false,
		Attestation: &common.Attestation{PubKey: privKey2.PubKey().Bytes(), Signature: []byte("sig-2")},
	})

	time.Sleep(100 * time.Millisecond)
	assert.Equal(t, int32(1), startedCount.Load())
	assert.Equal(t, int32(1), maxActive.Load())

	close(firstRelease)

	require.Eventually(t, func() bool {
		return startedCount.Load() >= 2
	}, time.Second, 10*time.Millisecond)

	got := <-secondStarted
	assert.Equal(t, outboundTxID, got)
	assert.Equal(t, int32(1), maxActive.Load())
}

func stringsRepeat(ch string, n int) string {
	out := ""
	for i := 0; i < n; i++ {
		out += ch
	}
	return out
}
