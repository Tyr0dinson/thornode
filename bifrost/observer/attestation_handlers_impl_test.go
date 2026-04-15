package observer

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/ebifrost"
)

// Test the AttestMessage interface implementations
func TestAttestMessageInterface(t *testing.T) {
	// Create sample data for each message type
	tx := common.Tx{
		ID:    "tx1",
		Chain: common.BTCChain,
	}
	obsTx := common.ObservedTx{
		Tx: tx,
	}
	networkFee := common.NetworkFee{
		Chain:  common.BTCChain,
		Height: 100,
	}
	solvency := common.Solvency{
		Chain:  common.BTCChain,
		Height: 200,
	}
	errataTx := common.ErrataTx{
		Chain: common.BTCChain,
		Id:    "tx1",
	}

	// Create attestation messages
	attestTx := common.AttestTx{
		ObsTx:       obsTx,
		Attestation: &common.Attestation{},
	}
	attestNetworkFee := common.AttestNetworkFee{
		NetworkFee:  &networkFee,
		Attestation: &common.Attestation{},
	}
	attestSolvency := common.AttestSolvency{
		Solvency:    &solvency,
		Attestation: &common.Attestation{},
	}
	attestErrataTx := common.AttestErrataTx{
		ErrataTx:    &errataTx,
		Attestation: &common.Attestation{},
	}

	// Test GetAttestation for each type
	t.Run("GetAttestation returns correct attestation", func(t *testing.T) {
		assert.Equal(t, attestTx.Attestation, attestTx.GetAttestation())
		assert.Equal(t, attestNetworkFee.Attestation, attestNetworkFee.GetAttestation())
		assert.Equal(t, attestSolvency.Attestation, attestSolvency.GetAttestation())
		assert.Equal(t, attestErrataTx.Attestation, attestErrataTx.GetAttestation())
	})

	// Test GetSignablePayload for each type
	t.Run("GetSignablePayload returns marshaled data", func(t *testing.T) {
		// ObservedTx
		txPayload, err := attestTx.GetSignablePayload()
		require.NoError(t, err)
		expectedTxPayload, err := tx.Marshal()
		require.NoError(t, err)
		assert.Equal(t, expectedTxPayload, txPayload)

		// NetworkFee
		feePayload, err := attestNetworkFee.GetSignablePayload()
		require.NoError(t, err)
		expectedFeePayload, err := networkFee.Marshal()
		require.NoError(t, err)
		assert.Equal(t, expectedFeePayload, feePayload)

		// Solvency
		solvencyPayload, err := attestSolvency.GetSignablePayload()
		require.NoError(t, err)
		expectedSolvencyPayload, err := solvency.Marshal()
		require.NoError(t, err)
		assert.Equal(t, expectedSolvencyPayload, solvencyPayload)

		// ErrataTx
		errataPayload, err := attestErrataTx.GetSignablePayload()
		require.NoError(t, err)
		expectedErrataPayload, err := errataTx.Marshal()
		require.NoError(t, err)
		assert.Equal(t, expectedErrataPayload, errataPayload)
	})
}

// TestProcessAttestation tests the generic attestation processing function
func TestProcessAttestation(t *testing.T) {
	// Override verifySignature for testing
	origVerifySignature := verifySignature
	defer func() { verifySignature = origVerifySignature }()
	verifySignature = func(signBz, signature, attester []byte) error {
		return nil // Always succeed in tests
	}

	t.Run("adds valid attestation to empty list", func(t *testing.T) {
		attestations := []attestationSentState{}

		// Create a simple attestation message
		tx := common.Tx{
			ID:    "tx1",
			Chain: common.BTCChain,
		}
		obsTx := common.ObservedTx{
			Tx: tx,
		}
		attestation := &common.Attestation{
			PubKey:    []byte("pubkey1"),
			Signature: []byte("sig1"),
		}
		attestTx := common.AttestTx{
			ObsTx:       obsTx,
			Attestation: attestation,
		}

		// Process the attestation
		err := ProcessAttestation(&attestations, &attestTx)
		require.NoError(t, err)

		// Verify the attestation was added
		assert.Len(t, attestations, 1)
		assert.Equal(t, attestation, attestations[0].attestation)
		assert.False(t, attestations[0].sent)
	})

	t.Run("ignores duplicate attestations (same signature)", func(t *testing.T) {
		attestations := []attestationSentState{
			{
				attestation: &common.Attestation{
					PubKey:    []byte("pubkey1"),
					Signature: []byte("sig1"),
				},
				sent: true,
			},
		}

		// Create an attestation with the same signature
		tx := common.Tx{
			ID:    "tx1",
			Chain: common.BTCChain,
		}
		obsTx := common.ObservedTx{
			Tx: tx,
		}
		attestation := &common.Attestation{
			PubKey:    []byte("pubkey1"),
			Signature: []byte("sig1"),
		}
		attestTx := common.AttestTx{
			ObsTx:       obsTx,
			Attestation: attestation,
		}

		// Process the attestation
		err := ProcessAttestation(&attestations, &attestTx)
		require.NoError(t, err)

		// Verify no new attestation was added
		assert.Len(t, attestations, 1)
	})

	t.Run("rejects attestation with same pubkey but different signature", func(t *testing.T) {
		attestations := []attestationSentState{
			{
				attestation: &common.Attestation{
					PubKey:    []byte("pubkey1"),
					Signature: []byte("sig1"),
				},
				sent: true,
			},
		}

		// Create an attestation with the same pubkey but different signature
		tx := common.Tx{
			ID:    "tx1",
			Chain: common.BTCChain,
		}
		obsTx := common.ObservedTx{
			Tx: tx,
		}
		attestation := &common.Attestation{
			PubKey:    []byte("pubkey1"),
			Signature: []byte("sig2"), // Different signature
		}
		attestTx := common.AttestTx{
			ObsTx:       obsTx,
			Attestation: attestation,
		}

		// Process the attestation
		err := ProcessAttestation(&attestations, &attestTx)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "signature already present for")

		// Verify no new attestation was added
		assert.Len(t, attestations, 1)
	})
}

// TestSendAttestationsToThornode tests sending attestations to Thornode
func TestSendAttestationsToThornode(t *testing.T) {
	// Create test instances
	ag, _, _, grpcClient, _, _ := setupTestGossip(t)

	// Create a test tx
	tx := &common.Tx{
		ID:    "send-tx",
		Chain: common.BTCChain,
	}
	obsTx := &common.ObservedTx{
		Tx: *tx,
	}

	// Create a new attestation state
	state := ag.observedTxsPool.NewAttestationState(obsTx)

	// Add two attestations
	state.attestations = []attestationSentState{
		{
			attestation: &common.Attestation{
				PubKey:    []byte("pubkey1"),
				Signature: []byte("sig1"),
			},
			sent: false,
		},
		{
			attestation: &common.Attestation{
				PubKey:    []byte("pubkey2"),
				Signature: []byte("sig2"),
			},
			sent: false,
		},
	}

	// Set up GRPC client to capture sent attestations
	var sentTx *common.QuorumTx
	grpcClient.sendQuorumTxFunc = func(ctx context.Context, quorumTx *common.QuorumTx, opts ...grpc.CallOption) (*ebifrost.SendQuorumTxResult, error) {
		sentTx = quorumTx
		return &ebifrost.SendQuorumTxResult{}, nil
	}

	// Send attestations to Thornode
	ag.sendObservedTxAttestationsToThornode(context.Background(), *obsTx, state, true, false, true)

	// Verify the tx was sent with the correct data
	require.NotNil(t, sentTx)
	assert.Equal(t, obsTx.Tx.ID, sentTx.ObsTx.Tx.ID)
	assert.Equal(t, obsTx.Tx.Chain, sentTx.ObsTx.Tx.Chain)
	assert.True(t, sentTx.Inbound)
	assert.Len(t, sentTx.Attestations, 2)

	// Verify all attestations are marked as sent
	for _, att := range state.attestations {
		assert.True(t, att.sent)
	}

	// Verify timestamps are updated
	assert.False(t, state.initialAttestationsSent.IsZero())
	assert.False(t, state.quorumAttestationsSent.IsZero())
	assert.False(t, state.lastAttestationsSent.IsZero())
}

// TestAttestableItemImplementation tests the AttestableItem interface implementation for all types
func TestAttestableItemImplementation(t *testing.T) {
	t.Run("common.Tx implements AttestableItem", func(t *testing.T) {
		tx := common.Tx{
			ID:    "tx1",
			Chain: common.BTCChain,
		}

		// Marshal the tx
		data, err := tx.Marshal()
		require.NoError(t, err)
		assert.NotEmpty(t, data)
	})

	t.Run("common.NetworkFee implements AttestableItem", func(t *testing.T) {
		networkFee := common.NetworkFee{
			Chain:  common.BTCChain,
			Height: 100,
		}

		// Marshal the network fee
		data, err := networkFee.Marshal()
		require.NoError(t, err)
		assert.NotEmpty(t, data)
	})

	t.Run("common.Solvency implements AttestableItem", func(t *testing.T) {
		solvency := common.Solvency{
			Chain:  common.BTCChain,
			Height: 200,
		}

		// Marshal the solvency
		data, err := solvency.Marshal()
		require.NoError(t, err)
		assert.NotEmpty(t, data)
	})

	t.Run("common.ErrataTx implements AttestableItem", func(t *testing.T) {
		errataTx := common.ErrataTx{
			Chain: common.BTCChain,
			Id:    "tx1",
		}

		// Marshal the errata tx
		data, err := errataTx.Marshal()
		require.NoError(t, err)
		assert.NotEmpty(t, data)
	})
}

// TestRefactoredHandlersIntegration tests the refactored handlers integration
func TestRefactoredHandlersIntegration(t *testing.T) {
	ag, _, _, grpcClient, bridge, _ := setupTestGossip(t)

	// Override verifySignature for testing
	origVerifySignature := verifySignature
	defer func() { verifySignature = origVerifySignature }()
	verifySignature = func(signBz, signature, attester []byte) error {
		return nil // Always succeed in tests
	}

	t.Run("handles all attestation types through the same flow", func(t *testing.T) {
		// Create private keys for testing
		privKey1 := secp256k1.GenPrivKey()
		privKey2 := secp256k1.GenPrivKey()
		privKey3 := secp256k1.GenPrivKey()

		// Get bech32 public keys
		pub1, err := cosmos.Bech32ifyPubKey(cosmos.Bech32PubKeyTypeAccPub, privKey1.PubKey())
		require.NoError(t, err)
		pub2, err := cosmos.Bech32ifyPubKey(cosmos.Bech32PubKeyTypeAccPub, privKey2.PubKey())
		require.NoError(t, err)
		pub3, err := cosmos.Bech32ifyPubKey(cosmos.Bech32PubKeyTypeAccPub, privKey3.PubKey())
		require.NoError(t, err)

		// Set active validators
		ag.setActiveValidators([]common.PubKey{common.PubKey(pub1), common.PubKey(pub2), common.PubKey(pub3)})

		// Setup bridge mock
		bridge.getKeysignPartyFunc = func(pubKey common.PubKey) (common.PubKeys, error) {
			return common.PubKeys{"pubkey1", "pubkey2", "pubkey3", "pubkey4"}, nil
		}

		// Track sent messages for each type
		var sentTxs []*common.QuorumTx
		var sentFees []*common.QuorumNetworkFee
		var sentSolvencies []*common.QuorumSolvency
		var sentErratas []*common.QuorumErrataTx

		// Setup GRPC client mocks
		grpcClient.sendQuorumTxFunc = func(ctx context.Context, quorumTx *common.QuorumTx, opts ...grpc.CallOption) (*ebifrost.SendQuorumTxResult, error) {
			sentTxs = append(sentTxs, quorumTx)
			return &ebifrost.SendQuorumTxResult{}, nil
		}

		grpcClient.sendQuorumNetworkFeeFunc = func(ctx context.Context, quorumNetworkFee *common.QuorumNetworkFee, opts ...grpc.CallOption) (*ebifrost.SendQuorumNetworkFeeResult, error) {
			sentFees = append(sentFees, quorumNetworkFee)
			return &ebifrost.SendQuorumNetworkFeeResult{}, nil
		}

		grpcClient.sendQuorumSolvencyFunc = func(ctx context.Context, quorumSolvency *common.QuorumSolvency, opts ...grpc.CallOption) (*ebifrost.SendQuorumSolvencyResult, error) {
			sentSolvencies = append(sentSolvencies, quorumSolvency)
			return &ebifrost.SendQuorumSolvencyResult{}, nil
		}

		grpcClient.sendQuorumErrataFunc = func(ctx context.Context, quorumErrataTx *common.QuorumErrataTx, opts ...grpc.CallOption) (*ebifrost.SendQuorumErrataTxResult, error) {
			sentErratas = append(sentErratas, quorumErrataTx)
			return &ebifrost.SendQuorumErrataTxResult{}, nil
		}

		// Create test data for each type
		tx := common.ObservedTx{
			Tx: common.Tx{
				ID:    "tx-quorum",
				Chain: common.BTCChain,
			},
		}

		networkFee := common.NetworkFee{
			Chain:  common.BTCChain,
			Height: 100,
		}

		solvency := common.Solvency{
			Chain:  common.BTCChain,
			Height: 200,
		}

		errataTx := common.ErrataTx{
			Chain: common.BTCChain,
			Id:    "tx-errata",
		}

		// Process each type with multiple attestations to reach quorum
		for i, privKey := range []*secp256k1.PrivKey{privKey1, privKey2, privKey3} {
			// ObservedTx attestation
			ag.handleObservedTxAttestation(context.Background(), common.AttestTx{
				ObsTx:   tx,
				Inbound: true,
				Attestation: &common.Attestation{
					PubKey:    privKey.PubKey().Bytes(),
					Signature: []byte(fmt.Sprintf("sig-tx-%d", i)),
				},
			})

			// NetworkFee attestation
			ag.handleNetworkFeeAttestation(context.Background(), common.AttestNetworkFee{
				NetworkFee: &networkFee,
				Attestation: &common.Attestation{
					PubKey:    privKey.PubKey().Bytes(),
					Signature: []byte(fmt.Sprintf("sig-fee-%d", i)),
				},
			})

			// Solvency attestation
			ag.handleSolvencyAttestation(context.Background(), common.AttestSolvency{
				Solvency: &solvency,
				Attestation: &common.Attestation{
					PubKey:    privKey.PubKey().Bytes(),
					Signature: []byte(fmt.Sprintf("sig-solvency-%d", i)),
				},
			})

			// ErrataTx attestation
			ag.handleErrataAttestation(context.Background(), common.AttestErrataTx{
				ErrataTx: &errataTx,
				Attestation: &common.Attestation{
					PubKey:    privKey.PubKey().Bytes(),
					Signature: []byte(fmt.Sprintf("sig-errata-%d", i)),
				},
			})
		}

		// Wait for processing to complete
		time.Sleep(200 * time.Millisecond)

		// Verify all types were sent to thornode
		assert.Len(t, sentTxs, 2, "Should have sent txs to thornode")
		assert.Len(t, sentFees, 2, "Should have sent network fee to thornode")
		assert.Len(t, sentSolvencies, 2, "Should have sent solvency to thornode")
		assert.Len(t, sentErratas, 2, "Should have sent errata tx to thornode")

		// Verify correct data was sent for each type
		sentAttestations := 0
		for _, sentTx := range sentTxs {
			assert.Equal(t, tx.Tx.ID, sentTx.ObsTx.Tx.ID)
			assert.Equal(t, tx.Tx.Chain, sentTx.ObsTx.Tx.Chain)
			sentAttestations += len(sentTx.Attestations)
		}
		assert.Equal(t, sentAttestations, 3, "Should have sent 3 attestations in total")

		sentAttestations = 0
		for _, sentFee := range sentFees {
			assert.Equal(t, networkFee.Chain, sentFee.NetworkFee.Chain)
			assert.Equal(t, networkFee.Height, sentFee.NetworkFee.Height)
			sentAttestations += len(sentFee.Attestations)
		}
		assert.Equal(t, sentAttestations, 3, "Should have sent 3 attestations in total")

		sentAttestations = 0
		for _, sentSolvency := range sentSolvencies {
			assert.Equal(t, solvency.Chain, sentSolvency.Solvency.Chain)
			assert.Equal(t, solvency.Height, sentSolvency.Solvency.Height)
			sentAttestations += len(sentSolvency.Attestations)
		}
		assert.Equal(t, sentAttestations, 3, "Should have sent 3 attestations in total")

		sentAttestations = 0
		for _, sentErrata := range sentErratas {
			assert.Equal(t, errataTx.Chain, sentErrata.ErrataTx.Chain)
			assert.Equal(t, errataTx.Id, sentErrata.ErrataTx.Id)
			sentAttestations += len(sentErrata.Attestations)
		}

		// Verify all state data is stored correctly
		ag.mu.Lock()
		defer ag.mu.Unlock()

		// Check observed tx state
		k, err := observedTxKey(tx, false, true)
		require.NoError(t, err)
		txState, exists := ag.observedTxs[k]
		assert.True(t, exists, "TX should exist in observed txs map")
		if exists {
			assert.Equal(t, 3, txState.AttestationCount(), "Should have 3 tx attestations")
		}

		// Check network fee state
		feeState, exists := ag.networkFees[networkFee]
		assert.True(t, exists, "Network fee should exist in networkFees map")
		if exists {
			assert.Equal(t, 3, feeState.AttestationCount(), "Should have 3 fee attestations")
		}

		// Check solvency state - this uses the hash as a key
		solvencyHash, err := solvency.Hash()
		require.NoError(t, err)
		solvencyState, exists := ag.solvencies[solvencyHash]
		assert.True(t, exists, "Solvency should exist in solvencies map")
		if exists {
			assert.Equal(t, 3, solvencyState.AttestationCount(), "Should have 3 solvency attestations")
		}

		// Check errata tx state
		errataState, exists := ag.errataTxs[errataTx]
		assert.True(t, exists, "Errata tx should exist in errataTxs map")
		if exists {
			assert.Equal(t, 3, errataState.AttestationCount(), "Should have 3 errata attestations")
		}
	})
}

// TestXMRNonSignerCoAttestation verifies that a non-signing node automatically
// co-attests to XMR outbound observations it receives from signing peers.
// In a 2-of-3 threshold setup the two signers create attestations during
// signing; the non-signer learns the observation via gossip and must add its
// own vote so quorum can be reached.
func TestXMRNonSignerCoAttestation(t *testing.T) {
	ag, _, _, _, bridge, _ := setupTestGossip(t)
	ag.SetObserverValidateXMRTx(func(tx common.ObservedTx) error { return nil })

	origVerifySignature := verifySignature
	defer func() { verifySignature = origVerifySignature }()
	verifySignature = func(signBz, signature, attester []byte) error {
		return nil
	}

	bridge.getKeysignPartyFunc = func(pubKey common.PubKey) (common.PubKeys, error) {
		return common.PubKeys{"pubkey1", "pubkey2", "pubkey3"}, nil
	}

	obsTx := common.ObservedTx{
		Tx: common.Tx{
			ID:    "xmr-outbound-tx-1",
			Chain: common.XMRChain,
			Coins: common.NewCoins(common.NewCoin(common.XMRAsset, cosmos.NewUint(500000000))),
		},
		ObservedPubKey: "thorpub1addwnpepqtest",
		TxKeys:         []string{strings.Repeat("1", 64)},
	}

	// Simulate a signing peer sending its attestation to the non-signer.
	signerKey := secp256k1.GenPrivKey()
	signBz, err := obsTx.GetSignablePayload()
	require.NoError(t, err)
	signerSig, err := signerKey.Sign(signBz)
	require.NoError(t, err)

	peerAttestation := common.AttestTx{
		ObsTx:   obsTx,
		Inbound: false,
		Attestation: &common.Attestation{
			PubKey:    signerKey.PubKey().Bytes(),
			Signature: signerSig,
		},
	}

	ag.handleObservedTxAttestation(context.Background(), peerAttestation)

	// Give the async co-attestation goroutine time to run.
	time.Sleep(200 * time.Millisecond)

	k, err := observedTxKey(obsTx, false, false)
	require.NoError(t, err)

	ag.mu.Lock()
	state, exists := ag.observedTxs[k]
	ag.mu.Unlock()

	require.True(t, exists, "attestation state should exist")

	state.mu.Lock()
	count := state.AttestationCount()
	state.mu.Unlock()

	assert.Equal(t, 2, count, "should have 2 attestations: signer + non-signer co-attestation")
}

// TestXMRCoAttestationSkipsNonXMR verifies that co-attestation only triggers
// for XMR outbound transactions with tx keys.
func TestXMRCoAttestationSkipsNonXMR(t *testing.T) {
	ag, _, _, _, bridge, _ := setupTestGossip(t)
	ag.SetObserverValidateXMRTx(func(tx common.ObservedTx) error { return nil })

	origVerifySignature := verifySignature
	defer func() { verifySignature = origVerifySignature }()
	verifySignature = func(signBz, signature, attester []byte) error {
		return nil
	}

	bridge.getKeysignPartyFunc = func(pubKey common.PubKey) (common.PubKeys, error) {
		return common.PubKeys{"pubkey1", "pubkey2", "pubkey3"}, nil
	}

	// BTC outbound — should NOT trigger co-attestation.
	obsTx := common.ObservedTx{
		Tx: common.Tx{
			ID:    "btc-outbound-tx-1",
			Chain: common.BTCChain,
			Coins: common.NewCoins(common.NewCoin(common.BTCAsset, cosmos.NewUint(100000))),
		},
		ObservedPubKey: "thorpub1addwnpepqtest",
	}

	signerKey := secp256k1.GenPrivKey()
	signBz, err := obsTx.GetSignablePayload()
	require.NoError(t, err)
	signerSig, err := signerKey.Sign(signBz)
	require.NoError(t, err)

	peerAttestation := common.AttestTx{
		ObsTx:   obsTx,
		Inbound: false,
		Attestation: &common.Attestation{
			PubKey:    signerKey.PubKey().Bytes(),
			Signature: signerSig,
		},
	}

	ag.handleObservedTxAttestation(context.Background(), peerAttestation)
	time.Sleep(200 * time.Millisecond)

	k, err := observedTxKey(obsTx, false, false)
	require.NoError(t, err)

	ag.mu.Lock()
	state, exists := ag.observedTxs[k]
	ag.mu.Unlock()

	require.True(t, exists)

	state.mu.Lock()
	count := state.AttestationCount()
	state.mu.Unlock()

	assert.Equal(t, 1, count, "only the peer attestation — no co-attestation for non-XMR chains")
}

// TestXMRCoAttestationIdempotent verifies that receiving the same XMR outbound
// attestation twice does not produce duplicate co-attestations.
func TestXMRCoAttestationIdempotent(t *testing.T) {
	ag, _, _, _, bridge, _ := setupTestGossip(t)
	ag.SetObserverValidateXMRTx(func(tx common.ObservedTx) error { return nil })

	origVerifySignature := verifySignature
	defer func() { verifySignature = origVerifySignature }()
	verifySignature = func(signBz, signature, attester []byte) error {
		return nil
	}

	bridge.getKeysignPartyFunc = func(pubKey common.PubKey) (common.PubKeys, error) {
		return common.PubKeys{"pubkey1", "pubkey2", "pubkey3"}, nil
	}

	obsTx := common.ObservedTx{
		Tx: common.Tx{
			ID:    "xmr-outbound-dedup",
			Chain: common.XMRChain,
			Coins: common.NewCoins(common.NewCoin(common.XMRAsset, cosmos.NewUint(500000000))),
		},
		ObservedPubKey: "thorpub1addwnpepqtest",
		TxKeys:         []string{strings.Repeat("2", 64)},
	}

	signerKey := secp256k1.GenPrivKey()
	signBz, err := obsTx.GetSignablePayload()
	require.NoError(t, err)
	signerSig, err := signerKey.Sign(signBz)
	require.NoError(t, err)

	peerAttestation := common.AttestTx{
		ObsTx:   obsTx,
		Inbound: false,
		Attestation: &common.Attestation{
			PubKey:    signerKey.PubKey().Bytes(),
			Signature: signerSig,
		},
	}

	// Send the same attestation twice.
	ag.handleObservedTxAttestation(context.Background(), peerAttestation)
	time.Sleep(200 * time.Millisecond)
	ag.handleObservedTxAttestation(context.Background(), peerAttestation)
	time.Sleep(200 * time.Millisecond)

	k, err := observedTxKey(obsTx, false, false)
	require.NoError(t, err)

	ag.mu.Lock()
	state, exists := ag.observedTxs[k]
	ag.mu.Unlock()

	require.True(t, exists)

	state.mu.Lock()
	count := state.AttestationCount()
	state.mu.Unlock()

	assert.Equal(t, 2, count, "should still be 2: signer + one co-attestation, no duplicates")
}

func TestXMRNonSignerCoAttestationSkipsValidationFailure(t *testing.T) {
	ag, _, _, _, bridge, _ := setupTestGossip(t)
	ag.SetObserverValidateXMRTx(func(tx common.ObservedTx) error { return fmt.Errorf("invalid outbound") })

	origVerifySignature := verifySignature
	defer func() { verifySignature = origVerifySignature }()
	verifySignature = func(signBz, signature, attester []byte) error {
		return nil
	}

	bridge.getKeysignPartyFunc = func(pubKey common.PubKey) (common.PubKeys, error) {
		return common.PubKeys{"pubkey1", "pubkey2", "pubkey3"}, nil
	}

	obsTx := common.ObservedTx{
		Tx: common.Tx{
			ID:    "xmr-outbound-invalid",
			Chain: common.XMRChain,
			Coins: common.NewCoins(common.NewCoin(common.XMRAsset, cosmos.NewUint(500000000))),
		},
		ObservedPubKey: "thorpub1addwnpepqtest",
		TxKeys:         []string{strings.Repeat("3", 64)},
	}

	signerKey := secp256k1.GenPrivKey()
	signBz, err := obsTx.GetSignablePayload()
	require.NoError(t, err)
	signerSig, err := signerKey.Sign(signBz)
	require.NoError(t, err)

	peerAttestation := common.AttestTx{
		ObsTx:   obsTx,
		Inbound: false,
		Attestation: &common.Attestation{
			PubKey:    signerKey.PubKey().Bytes(),
			Signature: signerSig,
		},
	}

	ag.handleObservedTxAttestation(context.Background(), peerAttestation)
	time.Sleep(200 * time.Millisecond)

	k, err := observedTxKey(obsTx, false, false)
	require.NoError(t, err)

	ag.mu.Lock()
	state, exists := ag.observedTxs[k]
	ag.mu.Unlock()

	require.True(t, exists)

	state.mu.Lock()
	count := state.AttestationCount()
	state.mu.Unlock()

	assert.Equal(t, 1, count, "validation failure should prevent non-signer co-attestation")
}

func TestXMROutboundAttestationRequiresTxKeys(t *testing.T) {
	origVerifySignature := verifySignature
	defer func() { verifySignature = origVerifySignature }()
	verifySignature = func(signBz, signature, attester []byte) error {
		return nil
	}

	t.Run("local attestation rejects outbound without tx keys", func(t *testing.T) {
		ag, _, _, _, _, _ := setupTestGossip(t)
		obsTx := &common.ObservedTx{
			Tx:             common.Tx{ID: "xmr-outbound-local-no-keys", Chain: common.XMRChain},
			ObservedPubKey: "thorpub1addwnpepqtest",
		}

		err := ag.AttestObservedTx(context.Background(), obsTx, false)
		require.Error(t, err)
		require.Contains(t, err.Error(), "requires tx keys")
	})

	t.Run("peer attestation is dropped when outbound tx keys are missing", func(t *testing.T) {
		ag, _, _, _, _, _ := setupTestGossip(t)

		obsTx := common.ObservedTx{
			Tx:             common.Tx{ID: "xmr-outbound-peer-no-keys", Chain: common.XMRChain},
			ObservedPubKey: "thorpub1addwnpepqtest",
		}
		signerKey := secp256k1.GenPrivKey()
		signBz, err := obsTx.GetSignablePayload()
		require.NoError(t, err)
		signerSig, err := signerKey.Sign(signBz)
		require.NoError(t, err)

		ag.handleObservedTxAttestation(context.Background(), common.AttestTx{
			ObsTx:   obsTx,
			Inbound: false,
			Attestation: &common.Attestation{
				PubKey:    signerKey.PubKey().Bytes(),
				Signature: signerSig,
			},
		})

		k, err := observedTxKey(obsTx, false, false)
		require.NoError(t, err)

		ag.mu.Lock()
		_, exists := ag.observedTxs[k]
		ag.mu.Unlock()
		assert.False(t, exists, "xmr outbound without tx keys should not enter attestation state")
	})
}

func TestXMRFinalInboundAttestationRequiresKeyImages(t *testing.T) {
	origVerifySignature := verifySignature
	defer func() { verifySignature = origVerifySignature }()
	verifySignature = func(signBz, signature, attester []byte) error {
		return nil
	}

	t.Run("local attestation rejects finalized inbound without key images", func(t *testing.T) {
		ag, _, _, _, _, _ := setupTestGossip(t)
		obsTx := &common.ObservedTx{
			Tx: common.Tx{
				ID:          "xmr-inbound-local-no-key-images",
				Chain:       common.XMRChain,
				FromAddress: common.Address(common.XMRSeededDummyAddress('8', []byte("sender"))),
				ToAddress:   common.Address(common.XMRSeededDummyAddress('4', []byte("vault"))),
			},
			BlockHeight:    100,
			FinaliseHeight: 100,
			ObservedPubKey: "thorpub1addwnpepqtest",
		}

		err := ag.AttestObservedTx(context.Background(), obsTx, true)
		require.Error(t, err)
		require.Contains(t, err.Error(), "requires key images")
	})

	t.Run("peer attestation is dropped when finalized inbound key images are missing", func(t *testing.T) {
		ag, _, _, _, _, _ := setupTestGossip(t)

		obsTx := common.ObservedTx{
			Tx: common.Tx{
				ID:          "xmr-inbound-peer-no-key-images",
				Chain:       common.XMRChain,
				FromAddress: common.Address(common.XMRSeededDummyAddress('8', []byte("sender"))),
				ToAddress:   common.Address(common.XMRSeededDummyAddress('4', []byte("vault"))),
			},
			BlockHeight:    100,
			FinaliseHeight: 100,
			ObservedPubKey: "thorpub1addwnpepqtest",
		}
		signerKey := secp256k1.GenPrivKey()
		signBz, err := obsTx.GetSignablePayload()
		require.NoError(t, err)
		signerSig, err := signerKey.Sign(signBz)
		require.NoError(t, err)

		ag.handleObservedTxAttestation(context.Background(), common.AttestTx{
			ObsTx:   obsTx,
			Inbound: true,
			Attestation: &common.Attestation{
				PubKey:    signerKey.PubKey().Bytes(),
				Signature: signerSig,
			},
		})

		k, err := observedTxKey(obsTx, false, true)
		require.NoError(t, err)

		ag.mu.Lock()
		_, exists := ag.observedTxs[k]
		ag.mu.Unlock()
		assert.False(t, exists, "xmr finalized inbound without key images should not enter attestation state")
	})
}

func TestAttestObservedTxGatesFinalXMROutboundOnValidatedMetadata(t *testing.T) {
	ag, _, _, _, _, _ := setupTestGossip(t)
	ag.SetObserverValidateXMRTx(func(tx common.ObservedTx) error {
		return fmt.Errorf("missing key images")
	})

	obsTx := &common.ObservedTx{
		Tx: common.Tx{
			ID:          common.TxID(stringsRepeat("9", 64)),
			Chain:       common.XMRChain,
			FromAddress: common.Address(common.XMRSeededDummyAddress('4', []byte("vault"))),
			ToAddress:   common.Address(common.XMRSeededDummyAddress('8', []byte("external"))),
		},
		BlockHeight:    100,
		FinaliseHeight: 100,
		ObservedPubKey: "thorpub1addwnpepqtest",
		TxKeys:         []string{strings.Repeat("9", 64)},
	}

	err := ag.AttestObservedTx(context.Background(), obsTx, false)
	require.Error(t, err)
	require.Contains(t, err.Error(), "not ready")

	k, keyErr := observedTxKey(*obsTx, false, false)
	require.NoError(t, keyErr)

	ag.mu.Lock()
	_, exists := ag.observedTxs[k]
	ag.mu.Unlock()
	assert.False(t, exists, "finalized xmr outbound should not enter attestation state until metadata is ready")
}

func TestSendObservedTxAttestationsToThornodeGatesFinalXMROutboundOnValidatedMetadata(t *testing.T) {
	ag, _, _, grpcClient, _, _ := setupTestGossip(t)
	ag.SetObserverValidateXMRTx(func(tx common.ObservedTx) error {
		return fmt.Errorf("missing key images")
	})

	obsTx := &common.ObservedTx{
		Tx: common.Tx{
			ID:          common.TxID(stringsRepeat("a", 64)),
			Chain:       common.XMRChain,
			FromAddress: common.Address(common.XMRSeededDummyAddress('4', []byte("vault"))),
			ToAddress:   common.Address(common.XMRSeededDummyAddress('8', []byte("external"))),
		},
		BlockHeight:    100,
		FinaliseHeight: 100,
		ObservedPubKey: "thorpub1addwnpepqtest",
		TxKeys:         []string{strings.Repeat("a", 64)},
	}

	state := ag.observedTxsPool.NewAttestationState(obsTx)
	state.attestations = []attestationSentState{
		{
			attestation: &common.Attestation{
				PubKey:    []byte("pubkey1"),
				Signature: []byte("sig1"),
			},
		},
	}

	called := false
	grpcClient.sendQuorumTxFunc = func(ctx context.Context, quorumTx *common.QuorumTx, opts ...grpc.CallOption) (*ebifrost.SendQuorumTxResult, error) {
		called = true
		return &ebifrost.SendQuorumTxResult{}, nil
	}

	ag.sendObservedTxAttestationsToThornode(context.Background(), *obsTx, state, false, false, true)

	assert.False(t, called, "finalized xmr outbound should not be sent before validator accepts propagated metadata")
	for _, att := range state.attestations {
		assert.False(t, att.sent)
	}
}

func TestSendObservedTxAttestationsToThornodeAllowsFinalXMROutboundWhenValidatedMetadataReady(t *testing.T) {
	ag, _, _, grpcClient, _, _ := setupTestGossip(t)

	var validateCalls int
	ag.SetObserverValidateXMRTx(func(tx common.ObservedTx) error {
		validateCalls++
		return nil
	})

	obsTx := &common.ObservedTx{
		Tx: common.Tx{
			ID:          common.TxID(stringsRepeat("b", 64)),
			Chain:       common.XMRChain,
			FromAddress: common.Address(common.XMRSeededDummyAddress('4', []byte("vault"))),
			ToAddress:   common.Address(common.XMRSeededDummyAddress('8', []byte("external"))),
		},
		BlockHeight:    100,
		FinaliseHeight: 100,
		ObservedPubKey: "thorpub1addwnpepqtest",
		TxKeys:         []string{strings.Repeat("b", 64)},
	}

	state := ag.observedTxsPool.NewAttestationState(obsTx)
	state.attestations = []attestationSentState{
		{
			attestation: &common.Attestation{
				PubKey:    []byte("pubkey1"),
				Signature: []byte("sig1"),
			},
		},
	}

	called := false
	grpcClient.sendQuorumTxFunc = func(ctx context.Context, quorumTx *common.QuorumTx, opts ...grpc.CallOption) (*ebifrost.SendQuorumTxResult, error) {
		called = true
		return &ebifrost.SendQuorumTxResult{}, nil
	}

	ag.sendObservedTxAttestationsToThornode(context.Background(), *obsTx, state, false, false, true)

	assert.True(t, called)
	assert.Equal(t, 1, validateCalls)
	for _, att := range state.attestations {
		assert.True(t, att.sent)
	}
}

func TestXMROutboundTxKeysUseActiveValidatorQuorum(t *testing.T) {
	ag, _, _, grpcClient, bridge, _ := setupTestGossip(t)

	origVerifySignature := verifySignature
	defer func() { verifySignature = origVerifySignature }()
	verifySignature = func(signBz, signature, attester []byte) error {
		return nil
	}

	activePrivKeys := []*secp256k1.PrivKey{
		secp256k1.GenPrivKey(),
		secp256k1.GenPrivKey(),
		secp256k1.GenPrivKey(),
		secp256k1.GenPrivKey(),
		secp256k1.GenPrivKey(),
		secp256k1.GenPrivKey(),
	}
	activeVals := make(common.PubKeys, 0, len(activePrivKeys))
	for _, privKey := range activePrivKeys {
		bech32Pub, err := cosmos.Bech32ifyPubKey(cosmos.Bech32PubKeyTypeAccPub, privKey.PubKey())
		require.NoError(t, err)
		activeVals = append(activeVals, common.PubKey(bech32Pub))
	}
	ag.setActiveValidators(activeVals)

	bridge.getKeysignPartyFunc = func(pubKey common.PubKey) (common.PubKeys, error) {
		return common.PubKeys{"signer-a", "signer-b", "signer-c"}, nil
	}

	var sent []*common.QuorumTx
	grpcClient.sendQuorumTxFunc = func(ctx context.Context, quorumTx *common.QuorumTx, opts ...grpc.CallOption) (*ebifrost.SendQuorumTxResult, error) {
		sent = append(sent, quorumTx)
		return &ebifrost.SendQuorumTxResult{}, nil
	}

	obsTx := common.ObservedTx{
		Tx: common.Tx{
			ID:    "xmr-outbound-active-quorum",
			Chain: common.XMRChain,
		},
		ObservedPubKey: "thorpub1addwnpepqtest",
		TxKeys:         []string{strings.Repeat("4", 64)},
	}

	for i := 0; i < 3; i++ {
		signBz, err := obsTx.GetSignablePayload()
		require.NoError(t, err)
		sig, err := activePrivKeys[i].Sign(signBz)
		require.NoError(t, err)

		ag.handleObservedTxAttestation(context.Background(), common.AttestTx{
			ObsTx:                  obsTx,
			Inbound:                false,
			AllowFutureObservation: true,
			Attestation: &common.Attestation{
				PubKey:    activePrivKeys[i].PubKey().Bytes(),
				Signature: sig,
			},
		})
	}

	require.Empty(t, sent, "three vault signers alone must not satisfy the active-validator quorum")

	signBz, err := obsTx.GetSignablePayload()
	require.NoError(t, err)
	sig, err := activePrivKeys[3].Sign(signBz)
	require.NoError(t, err)

	ag.handleObservedTxAttestation(context.Background(), common.AttestTx{
		ObsTx:                  obsTx,
		Inbound:                false,
		AllowFutureObservation: true,
		Attestation: &common.Attestation{
			PubKey:    activePrivKeys[3].PubKey().Bytes(),
			Signature: sig,
		},
	})

	require.Len(t, sent, 1, "active-validator quorum should be reached only after a fourth attestation")
}
