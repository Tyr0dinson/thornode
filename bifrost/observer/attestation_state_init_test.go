package observer

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	"github.com/libp2p/go-libp2p-core/network"
	"github.com/libp2p/go-libp2p-core/peer"
	"github.com/libp2p/go-libp2p-core/protocol"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gitlab.com/thorchain/thornode/v3/bifrost/metrics"
	"gitlab.com/thorchain/thornode/v3/bifrost/p2p"
	"gitlab.com/thorchain/thornode/v3/bifrost/p2p/conversion"
	"gitlab.com/thorchain/thornode/v3/bifrost/pubkeymanager"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/config"
	thortypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// TestSendAttestationStateEmpty tests sending an empty attestation state
func TestSendAttestationStateEmpty(t *testing.T) {
	// Disable deadline application during tests
	originalApplyDeadline := p2p.ApplyDeadline.Load()
	p2p.ApplyDeadline.Store(false)
	defer func() { p2p.ApplyDeadline.Store(originalApplyDeadline) }()

	// Create a test instance with empty state
	ag, _, _, _, _, _ := setupTestGossip(t)

	// Empty transfers finish with a done response, not ack_begin.
	stream := newDoneResponderStream("test-peer")

	// Call the method with a timeout
	done := make(chan error, 1)
	go func() {
		done <- ag.sendAttestationState(stream, time.Now().Add(defaultStateSyncTimeout))
	}()

	// Wait for completion or timeout
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Method timed out")
	}

	// Get the data written to the stream
	data := stream.GetWrittenData()
	require.NotEmpty(t, data, "Should have written data to the stream")

	// Extract all individual messages from the data
	messages := ParseMessages(data)
	require.NotEmpty(t, messages, "Should have extracted messages")

	// Count message types
	var beginCount, headerCount, dataCount, endCount int
	for _, msg := range messages {
		if len(msg) == 0 {
			continue
		}

		switch {
		case bytes.HasPrefix(msg, prefixBatchBegin):
			beginCount++
		case bytes.HasPrefix(msg, prefixBatchHeader):
			headerCount++
		case bytes.HasPrefix(msg, prefixBatchData):
			dataCount++
		case bytes.Equal(msg, prefixBatchEnd):
			endCount++
		}
	}

	// Verify the basic protocol flow
	assert.Equal(t, 1, beginCount, "Should have one batch begin message")
	assert.Equal(t, 0, headerCount, "Should have one batch header message")
	assert.Equal(t, 0, dataCount, "Should have one batch data message")
	assert.Equal(t, 0, endCount, "Should have one batch end message")

	// For empty state, check the batch header
	for _, msg := range messages {
		if bytes.HasPrefix(msg, prefixBatchBegin) {
			totalBatches := int(binary.LittleEndian.Uint32(msg[1:5]))
			assert.Equal(t, 0, totalBatches, "Empty state should have 1 batch")
		}
	}
}

// TestSendAttestationStateSingleBatch tests sending a small attestation state in a single batch
func TestSendAttestationStateSingleBatch(t *testing.T) {
	// Disable deadline application during tests
	originalApplyDeadline := p2p.ApplyDeadline.Load()
	p2p.ApplyDeadline.Store(false)
	defer func() { p2p.ApplyDeadline.Store(originalApplyDeadline) }()

	// Create a test instance
	ag, _, _, _, _, _ := setupTestGossip(t)

	numVals := 4
	valPrivs := make([]*secp256k1.PrivKey, numVals)
	valPubs := make([]common.PubKey, numVals)
	for i := 0; i < numVals; i++ {
		valPrivs[i] = secp256k1.GenPrivKey()
		bech32Pub, err := cosmos.Bech32ifyPubKey(cosmos.Bech32PubKeyTypeAccPub, valPrivs[i].PubKey())
		require.NoError(t, err, "Should be able to convert pubkey to bech32")
		valPubs[i] = common.PubKey(bech32Pub)
	}

	// Set active validators
	ag.setActiveValidators(valPubs)

	// Create a small set of observed transactions
	numTxs := 3
	for i := 0; i < numTxs; i++ {
		privKey := secp256k1.GenPrivKey()
		tx := &common.Tx{
			ID:    common.TxID(string(rune('A' + i))),
			Chain: common.BSCChain,
		}

		obsTx := &common.ObservedTx{
			Tx: *tx,
		}

		signBz, err := obsTx.GetSignablePayloadWithInbound(false)
		require.NoError(t, err, "Should be able to get signable payload")

		// Create attestation state
		state := ag.observedTxsPool.NewAttestationState(&attestableObservedTx{ObservedTx: obsTx})

		for j := 0; j < numVals; j++ {
			sig, err := valPrivs[j].Sign(signBz)
			require.NoError(t, err, "Should be able to sign payload")

			// Add attestation
			state.attestations = append(state.attestations, attestationSentState{
				attestation: &common.Attestation{
					PubKey:    privKey.PubKey().Bytes(),
					Signature: sig,
				},
				sent: false,
			})
		}

		// Add to attestation gossip
		key := txKey{
			Chain:                  tx.Chain,
			ID:                     tx.ID,
			AllowFutureObservation: false,
			Finalized:              false,
		}

		ag.mu.Lock()
		ag.observedTxs[key] = state
		ag.mu.Unlock()
	}

	// Create the auto-responder stream
	stream := NewAutoResponderStream("test-peer")

	// Call the method with a timeout
	done := make(chan error, 1)
	go func() {
		done <- ag.sendAttestationState(stream, time.Now().Add(defaultStateSyncTimeout))
	}()

	// Wait for completion or timeout
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Method timed out")
	}

	// Get the data written to the stream
	data := stream.GetWrittenData()
	require.NotEmpty(t, data, "Should have written data to the stream")

	// Extract all individual messages from the data
	messages := ParseMessages(data)
	require.NotEmpty(t, messages, "Should have extracted messages")

	// Count message types
	var beginCount, headerCount, dataCount, endCount int
	for _, msg := range messages {
		if len(msg) == 0 {
			continue
		}

		switch {
		case bytes.HasPrefix(msg, prefixBatchBegin):
			beginCount++
		case bytes.HasPrefix(msg, prefixBatchHeader):
			headerCount++
		case bytes.HasPrefix(msg, prefixBatchData):
			dataCount++
		case bytes.Equal(msg, prefixBatchEnd):
			endCount++
		}
	}

	// Verify the basic protocol flow
	assert.Equal(t, 1, beginCount, "Should have one batch begin message")
	assert.Equal(t, 1, headerCount, "Should have one batch header message")
	assert.Equal(t, 1, dataCount, "Should have one batch data message")
	assert.Equal(t, 1, endCount, "Should have one batch end message")

	// Check the batch begin message
	for _, msg := range messages {
		if bytes.HasPrefix(msg, prefixBatchBegin) {
			// Extract total batches
			totalBatches := binary.LittleEndian.Uint32(msg[1:5])
			assert.Equal(t, uint32(1), totalBatches, "Should have 1 batch")
		}

		// For data messages, check the batch contents
		if bytes.HasPrefix(msg, prefixBatchData) {
			// Unmarshal the batch
			var batch common.QuorumState
			err := batch.Unmarshal(msg[1:])
			require.NoError(t, err, "Should be able to unmarshal batch data")

			// Verify the batch has the expected number of transactions
			assert.Equal(t, numTxs, len(batch.QuoTxs), "Batch should have all transactions")

			// Verify transaction IDs
			txIDs := make(map[string]bool)
			for _, tx := range batch.QuoTxs {
				txIDs[string(tx.ObsTx.Tx.ID)] = true
			}

			for j := 0; j < numTxs; j++ {
				assert.True(t, txIDs[string(rune('A'+j))], "Should contain transaction ID %c", 'A'+j)
			}
		}
	}
}

// TestSendAttestationStateMultipleBatches tests sending a large attestation state in multiple batches
func TestSendAttestationStateMultipleBatches(t *testing.T) {
	// Disable deadline application during tests
	originalApplyDeadline := p2p.ApplyDeadline.Load()
	p2p.ApplyDeadline.Store(false)
	defer func() { p2p.ApplyDeadline.Store(originalApplyDeadline) }()

	// Create a test instance
	ag, _, _, _, _, _ := setupTestGossip(t)

	// Set a small byte budget so the state splits into multiple batches
	origMaxAttestationBatchBytes := maxAttestationBatchBytes
	maxAttestationBatchBytes = 1000                                            // Small value for testing
	defer func() { maxAttestationBatchBytes = origMaxAttestationBatchBytes }() // Restore original value

	// Create a set of observed transactions large enough to span several batches
	numTxs := 5

	numVals := 4
	valPrivs := make([]*secp256k1.PrivKey, numVals)
	valPubs := make([]common.PubKey, numVals)
	for i := 0; i < numVals; i++ {
		valPrivs[i] = secp256k1.GenPrivKey()
		bech32Pub, err := cosmos.Bech32ifyPubKey(cosmos.Bech32PubKeyTypeAccPub, valPrivs[i].PubKey())
		require.NoError(t, err, "Should be able to convert pubkey to bech32")
		valPubs[i] = common.PubKey(bech32Pub)
	}

	// Set active validators
	ag.setActiveValidators(valPubs)

	for i := 0; i < numTxs; i++ {
		tx := &common.Tx{
			ID:    common.TxID(string(rune('A' + i))),
			Chain: common.BSCChain,
		}

		obsTx := &common.ObservedTx{
			Tx: *tx,
		}

		// Create attestation state
		state := ag.observedTxsPool.NewAttestationState(&attestableObservedTx{ObservedTx: obsTx})

		signBz, err := obsTx.GetSignablePayloadWithInbound(false)
		require.NoError(t, err, "Should be able to get signable payload")

		// Create attestations
		for j := 0; j < numVals; j++ {
			sig, err := valPrivs[j].Sign(signBz)
			require.NoError(t, err, "Should be able to sign payload")

			// Add attestation
			state.attestations = append(state.attestations, attestationSentState{
				attestation: &common.Attestation{
					PubKey:    valPrivs[j].PubKey().Bytes(),
					Signature: sig,
				},
				sent: false,
			})
		}

		// Add to attestation gossip
		key := txKey{
			Chain:                  tx.Chain,
			ID:                     tx.ID,
			AllowFutureObservation: false,
			Finalized:              false,
		}

		ag.mu.Lock()
		ag.observedTxs[key] = state
		ag.mu.Unlock()
	}

	// Create the auto-responder stream
	stream := NewAutoResponderStream("test-peer")

	// Call the method with a timeout
	done := make(chan error, 1)
	go func() {
		done <- ag.sendAttestationState(stream, time.Now().Add(defaultStateSyncTimeout))
	}()

	// Wait for completion or timeout
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(1 * time.Second):
		t.Fatal("Method timed out")
	}

	// Get the data written to the stream
	data := stream.GetWrittenData()
	require.NotEmpty(t, data, "Should have written data to the stream")

	// Extract all individual messages from the data
	messages := ParseMessages(data)
	require.NotEmpty(t, messages, "Should have extracted messages")

	// Count message types
	var beginCount, headerCount, dataCount, endCount int
	for _, msg := range messages {
		if len(msg) == 0 {
			continue
		}

		switch {
		case bytes.HasPrefix(msg, prefixBatchBegin):
			beginCount++
		case bytes.HasPrefix(msg, prefixBatchHeader):
			headerCount++
		case bytes.HasPrefix(msg, prefixBatchData):
			dataCount++
		case bytes.Equal(msg, prefixBatchEnd):
			endCount++
		}
	}

	// Verify the basic protocol flow
	assert.Equal(t, 1, beginCount, "Should have one batch begin message")
	assert.GreaterOrEqual(t, headerCount, 2, "Byte budget should force multiple batches")
	assert.Equal(t, headerCount, dataCount, "Every batch header should have a data message")
	assert.Equal(t, 1, endCount, "Should have one batch end message")

	// Check the batch begin message
	for i, msg := range messages {
		if bytes.HasPrefix(msg, prefixBatchBegin) {
			// Extract total batches
			totalBatches := binary.LittleEndian.Uint32(msg[1:5])
			assert.Equal(t, uint32(headerCount), totalBatches, "Announced batch count should match batches sent")
		}

		// For data messages, check the batch number and contents
		if bytes.HasPrefix(msg, prefixBatchHeader) && len(msg) >= 9 {
			// Extract batch number
			batchNum := binary.LittleEndian.Uint32(msg[1:5])
			assert.Less(t, batchNum, uint32(headerCount), "Batch number should be valid")

			// Get the batch data (should be the next message)
			if len(messages) > i+1 {
				batchData := messages[i+1]

				assert.True(t, bytes.HasPrefix(batchData, prefixBatchData))

				// Every frame must respect the byte budget (minus the one-byte prefix)
				assert.LessOrEqual(t, len(batchData)-1, maxAttestationBatchBytes,
					"Batch %d should fit the byte budget", batchNum)

				// Unmarshal the batch
				var batch common.QuorumState
				err := batch.Unmarshal(batchData[1:])
				if err != nil {
					t.Logf("Error unmarshaling batch %d (likely not a batch): %v", batchNum, err)
					continue
				}

				assert.NotEmpty(t, batch.QuoTxs, "Batch %d should carry at least one transaction", batchNum)
			}
		}
	}

	// Collect all transactions from all batches to verify we have them all
	allTxs := make(map[string]bool)
	for _, msg := range messages {
		if bytes.HasPrefix(msg, prefixBatchData) {
			// Try to unmarshal the next message as batch data
			var batch common.QuorumState
			if err := batch.Unmarshal(msg[1:]); err == nil {
				// Record all transaction IDs
				for _, tx := range batch.QuoTxs {
					allTxs[string(tx.ObsTx.Tx.ID)] = true
				}
			}
		}
	}

	// Verify we have all expected transactions
	assert.Equal(t, numTxs, len(allTxs), "Should have all transactions across all batches")
	for i := 0; i < numTxs; i++ {
		assert.True(t, allTxs[string(rune('A'+i))], "Should contain transaction ID %c", 'A'+i)
	}
}

// TestReceiveBatchedAttestationState tests receiving batched attestation state
func TestReceiveBatchedAttestationState(t *testing.T) {
	// Disable deadline application during tests
	originalApplyDeadline := p2p.ApplyDeadline.Load()
	p2p.ApplyDeadline.Store(false)
	defer func() { p2p.ApplyDeadline.Store(originalApplyDeadline) }()

	// Create a test instance
	ag, _, _, _, _, _ := setupTestGossip(t)

	// The receiver accepts whatever batch boundaries the sender chose; this
	// test simulates a sender that packed two txs per batch.
	batchSize := 2

	numVals := 4

	// Create test data: 5 transactions split into 3 batches
	numTxs := 5
	expectedBatches := (numTxs + batchSize - 1) / batchSize

	numSolvencies := 3

	valPrivs := make([]*secp256k1.PrivKey, numVals)
	valPubs := make([]common.PubKey, numVals)
	for i := 0; i < numVals; i++ {
		valPrivs[i] = secp256k1.GenPrivKey()
		bech32Pub, err := cosmos.Bech32ifyPubKey(cosmos.Bech32PubKeyTypeAccPub, valPrivs[i].PubKey())
		require.NoError(t, err, "Should be able to convert pubkey to bech32")
		valPubs[i] = common.PubKey(bech32Pub)
	}

	ag.setActiveValidators(valPubs)

	// Create the test transactions
	txs := make([]*common.QuorumTx, numTxs)
	for i := range numTxs {
		// Create observed tx
		obsTx := common.ObservedTx{
			Tx: common.Tx{
				ID:    common.TxID(string(rune('A' + i))),
				Chain: common.BSCChain,
			},
		}

		inbound := i%2 == 0 // Alternate inbound/outbound
		signBz, err := obsTx.GetSignablePayloadWithInbound(inbound)
		require.NoError(t, err, "Should be able to get signable payload")

		atts := make([]*common.Attestation, numVals)

		for j := range numVals {
			sig, err := valPrivs[j].Sign(signBz)
			require.NoError(t, err, "Should be able to sign payload")
			atts[j] = &common.Attestation{
				PubKey:    valPrivs[j].PubKey().Bytes(),
				Signature: sig,
			}
		}

		// Create quorum tx
		txs[i] = &common.QuorumTx{
			ObsTx:        obsTx,
			Inbound:      inbound,
			Attestations: atts,
		}
	}

	solvencies := make([]*common.QuorumSolvency, numSolvencies)
	for i := range numSolvencies {
		s := &common.Solvency{
			Chain:  common.BSCChain,
			Height: int64(i),
			Coins: []common.Coin{
				{
					Asset:  common.BNBBEP20Asset,
					Amount: cosmos.NewUint(100 + uint64(i)),
				},
			},
		}

		signBz, err := s.GetSignablePayload()
		require.NoError(t, err, "Should be able to get signable payload")

		atts := make([]*common.Attestation, numVals)
		for j := range numVals {
			sig, err := valPrivs[j].Sign(signBz)
			require.NoError(t, err, "Should be able to sign payload")
			atts[j] = &common.Attestation{
				PubKey:    valPrivs[j].PubKey().Bytes(),
				Signature: sig,
			}
		}

		solvencies[i] = &common.QuorumSolvency{
			Solvency:     s,
			Attestations: atts,
		}
	}

	// Create a simulated peer to send these transactions
	// Create stream pair for testing
	// Real blocking reads preserve backpressure without the old mock's
	// half-second polling-to-EOF race against synchronous batch processing.
	peerStream, ourStream := stateBoundsPipe(t, "test-peer", ag.host.ID())

	var wg sync.WaitGroup
	wg.Add(1)

	// Start a goroutine to simulate sending batched state from the peer
	go func() {
		defer wg.Done()
		// Wait for acknowledgment
		ack, err := p2p.ReadStreamWithBuffer(ourStream)
		require.NoError(t, err, "Should read begin acknowledgment")
		require.NotEmpty(t, ack, "Should receive acknowledgment")
		require.Equal(t, "ack_begin", string(ack), "Should receive 'ack_begin' after batch begin")

		t.Log("Received batch begin acknowledgment")

		// Send each batch
		for batchIdx := 0; batchIdx < expectedBatches; batchIdx++ {
			startIdx := batchIdx * batchSize
			endIdx := startIdx + batchSize

			if endIdx > numTxs {
				endIdx = numTxs
			}

			batchTxs := txs[startIdx:endIdx]

			var batchSolvencies []*common.QuorumSolvency
			if startIdx < numSolvencies {
				endIdxSolvency := startIdx + batchSize
				if endIdxSolvency > numSolvencies {
					endIdxSolvency = numSolvencies
				}

				batchSolvencies = solvencies[startIdx:endIdxSolvency]
			}

			// Create batch
			batch := common.QuorumState{
				QuoTxs:        batchTxs,
				QuoSolvencies: batchSolvencies,
			}

			// Marshal batch
			batchData, _ := batch.Marshal()

			// Create batch header
			batchHeader := make([]byte, 9)
			copy(batchHeader, prefixBatchHeader)
			binary.LittleEndian.PutUint32(batchHeader[1:5], uint32(batchIdx))
			binary.LittleEndian.PutUint32(batchHeader[5:9], uint32(len(batchData)))

			err = p2p.WriteStreamWithBuffer(batchHeader, ourStream)
			require.NoError(t, err, "Should write batch header")

			t.Log("Sent batch header")

			ack, err = p2p.ReadStreamWithBuffer(ourStream)
			require.NoError(t, err, "Should read header acknowledgment")
			require.Equal(t, "ack_header", string(ack), "Should receive 'ack_header' after batch header")

			msgData := make([]byte, len(batchData)+1)
			copy(msgData, prefixBatchData)
			copy(msgData[1:], batchData)

			err = p2p.WriteStreamWithBuffer(msgData, ourStream)
			require.NoError(t, err, "Should write batch data")

			t.Log("Sent batch data")

			// Wait for acknowledgment
			ack, err = p2p.ReadStreamWithBuffer(ourStream)
			require.NoError(t, err, "Should read data acknowledgment")
			require.Equal(t, "ack_data", string(ack), "Should receive 'ack_data' after batch data")
		}

		// Send batch end
		err = p2p.WriteStreamWithBuffer(prefixBatchEnd, ourStream)
		require.NoError(t, err, "Should write batch end")

		t.Log("Sent batch end")

		// Wait for acknowledgment
		ack, err = p2p.ReadStreamWithBuffer(ourStream)
		require.NoError(t, err, "Should read done acknowledgment")
		require.Equal(t, "done", string(ack), "Should receive 'done' after batch end")
	}()

	// Call receiveBatchedAttestationState with the initial batch begin message
	beginHeader := make([]byte, 4)
	binary.LittleEndian.PutUint32(beginHeader, uint32(expectedBatches))

	err := ag.receiveBatchedAttestationState(peerStream, beginHeader)
	require.NoError(t, err)

	// Wait for the sending goroutine to finish
	wg.Wait()

	// Verify that all transactions were added to the state
	ag.mu.Lock()
	defer ag.mu.Unlock()

	// Check for all test transactions by ID
	txCount := 0
	attCount := 0
	for k, ots := range ag.observedTxs {
		if k.Chain == common.BSCChain && len(k.ID) == 1 && k.ID[0] >= 'A' && k.ID[0] < 'A'+byte(numTxs) {
			txCount++
			ots.mu.Lock()
			for _, att := range ots.attestations {
				if att.attestation != nil && att.attestation.PubKey != nil {
					attCount++
				}
			}
			ots.mu.Unlock()
		}
	}

	assert.Equal(t, numTxs, txCount, "All transactions should be added to the state")
	assert.Equal(t, numTxs*numVals, attCount, "All attestations should be added to the state")

	solvencyCount := 0
	solvencyAttCount := 0
	for _, s := range ag.solvencies {
		if s.Item.Chain == common.BSCChain && s.Item.Height+100 == int64(s.Item.Coins[0].Amount.Uint64()) {
			solvencyCount++
			s.mu.Lock()
			for _, att := range s.attestations {
				if att.attestation != nil && att.attestation.PubKey != nil {
					solvencyAttCount++
				}
			}
			s.mu.Unlock()
		}
	}

	assert.Equal(t, numSolvencies, solvencyCount, "All solvencies should be added to the state")
	assert.Equal(t, numSolvencies*numVals, solvencyAttCount, "All solvency attestations should be added to the state")
}

func TestSendReceiveBatchedAttestationState(t *testing.T) {
	// Create a test instance
	agSender, _, _, _, _, _ := setupTestGossip(t)
	agReceiver, _, _, _, _, _ := setupTestGossip(t)

	// Set a small byte budget so the state splits into multiple batches
	origMaxAttestationBatchBytes := maxAttestationBatchBytes
	maxAttestationBatchBytes = 1000                                            // Small value for testing
	defer func() { maxAttestationBatchBytes = origMaxAttestationBatchBytes }() // Restore original value

	// Create enough items that the byte budget forces several batches
	numTxs := 5
	numSolvencies := 3
	numVals := 4
	valPrivs := make([]*secp256k1.PrivKey, numVals)
	valPubs := make([]common.PubKey, numVals)
	for i := 0; i < numVals; i++ {
		valPrivs[i] = secp256k1.GenPrivKey()
		bech32Pub, err := cosmos.Bech32ifyPubKey(cosmos.Bech32PubKeyTypeAccPub, valPrivs[i].PubKey())
		require.NoError(t, err, "Should be able to convert pubkey to bech32")
		valPubs[i] = common.PubKey(bech32Pub)
	}

	// Set active validators
	agSender.setActiveValidators(valPubs)
	agReceiver.setActiveValidators(valPubs)
	// The requesting transport identity is also an active validator.
	agSender.avMu.Lock()
	agSender.activeVals[agReceiver.host.ID()] = true
	agSender.avMu.Unlock()

	// Set attestation state on sender
	for i := 0; i < numTxs; i++ {
		tx := &common.Tx{
			ID:    common.TxID(string(rune('A' + i))),
			Chain: common.BSCChain,
		}

		obsTx := &common.ObservedTx{
			Tx: *tx,
		}

		// Create attestation state
		state := agSender.observedTxsPool.NewAttestationState(&attestableObservedTx{ObservedTx: obsTx})

		signBz, err := obsTx.GetSignablePayloadWithInbound(false)
		require.NoError(t, err, "Should be able to get signable payload")

		// Create attestations
		for j := 0; j < numVals; j++ {
			sig, err := valPrivs[j].Sign(signBz)
			require.NoError(t, err, "Should be able to sign payload")

			// Add attestation
			state.attestations = append(state.attestations, attestationSentState{
				attestation: &common.Attestation{
					PubKey:    valPrivs[j].PubKey().Bytes(),
					Signature: sig,
				},
				sent: false,
			})
		}

		// Add to attestation gossip
		key := txKey{
			Chain:                  tx.Chain,
			ID:                     tx.ID,
			AllowFutureObservation: false,
			Finalized:              false,
		}

		agSender.mu.Lock()
		agSender.observedTxs[key] = state
		agSender.mu.Unlock()
	}

	for i := range numSolvencies {
		s := &common.Solvency{
			Chain:  common.BSCChain,
			Height: int64(i),
			Coins: []common.Coin{
				{
					Asset:  common.BNBBEP20Asset,
					Amount: cosmos.NewUint(100 + uint64(i)),
				},
			},
		}

		signBz, err := s.GetSignablePayload()
		require.NoError(t, err, "Should be able to get signable payload")

		state := agSender.solvenciesPool.NewAttestationState(s)

		for j := range numVals {
			var sig []byte
			sig, err = valPrivs[j].Sign(signBz)
			require.NoError(t, err, "Should be able to sign payload")

			state.attestations = append(state.attestations, attestationSentState{
				attestation: &common.Attestation{
					PubKey:    valPrivs[j].PubKey().Bytes(),
					Signature: sig,
				},
			})
		}

		k, err := s.Hash()
		require.NoError(t, err, "Should be able to hash solvency")

		agSender.mu.Lock()
		agSender.solvencies[k] = state
		agSender.mu.Unlock()
	}

	// Create a simulated peer to send these transactions
	// Create stream pair for testing
	senderReader := bytes.NewBuffer(nil)
	senderWriter := &bytes.Buffer{}

	receiverReader := senderWriter
	receiverWriter := senderReader

	var sharedMu sync.Mutex

	receiverStream := &MockStream{
		reader: receiverReader,
		writer: receiverWriter,
		peer:   agSender.host.ID(),
		mu:     &sharedMu,
	}

	senderStream := &MockStream{
		reader: senderReader,
		writer: senderWriter,
		peer:   agReceiver.host.ID(),
		mu:     &sharedMu,
	}

	// Call the method with a timeout
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		agSender.handleStreamAttestationState(senderStream)
	}()

	go func() {
		defer wg.Done()
		agReceiver.askStreamForState(receiverStream)
	}()

	// Wait for both goroutines to finish
	wg.Wait()

	// agReceiver should have received all transactions
	agReceiver.mu.Lock()
	defer agReceiver.mu.Unlock()
	// Check for all test transactions by ID
	txCount := 0
	attCount := 0
	for k, ots := range agReceiver.observedTxs {
		if k.Chain == common.BSCChain && len(k.ID) == 1 && k.ID[0] >= 'A' && k.ID[0] < 'A'+byte(numTxs) {
			txCount++
			ots.mu.Lock()
			for _, att := range ots.attestations {
				if att.attestation != nil && att.attestation.PubKey != nil {
					attCount++
				}
			}
			ots.mu.Unlock()
		}
	}
	assert.Equal(t, numTxs, txCount, "All transactions should be added to the state")
	assert.Equal(t, numTxs*numVals, attCount, "All attestations should be added to the state")

	solvencyCount := 0
	solvencyAttCount := 0
	for _, s := range agReceiver.solvencies {
		if s.Item.Chain == common.BSCChain && s.Item.Height+100 == int64(s.Item.Coins[0].Amount.Uint64()) {
			solvencyCount++
			s.mu.Lock()
			for _, att := range s.attestations {
				if att.attestation != nil && att.attestation.PubKey != nil {
					solvencyAttCount++
				}
			}
			s.mu.Unlock()
		}
	}

	assert.Equal(t, numSolvencies, solvencyCount, "All solvencies should be added to the state")
	assert.Equal(t, numSolvencies*numVals, solvencyAttCount, "All solvency attestations should be added to the state")
}

// AutoResponderStream is a stream that automatically responds to protocol messages
type AutoResponderStream struct {
	pendingRead []byte
	MockStream
	writtenData *bytes.Buffer
	readData    chan []byte
	callCount   int
}

// NewAutoResponderStream creates a MockStream that automatically responds to protocol messages
func NewAutoResponderStream(peer peer.ID) *AutoResponderStream {
	readChan := make(chan []byte, 10)
	writtenData := &bytes.Buffer{}

	return &AutoResponderStream{
		MockStream: MockStream{
			peer: peer,
			mu:   new(sync.Mutex),
		},
		writtenData: writtenData,
		readData:    readChan,
		callCount:   0,
	}
}

// Read implementation that returns the next item from the readData channel
func (s *AutoResponderStream) Read(p []byte) (n int, err error) {
	if len(s.pendingRead) == 0 {
		select {
		case s.pendingRead = <-s.readData:
		case <-time.After(100 * time.Millisecond):
			return 0, io.EOF
		}
	}
	n = copy(p, s.pendingRead)
	s.pendingRead = s.pendingRead[n:]
	return n, nil
}

// Write implementation that captures the data and automatically responds
func (s *AutoResponderStream) Write(p []byte) (n int, err error) {
	// Count the call
	s.callCount++

	// Capture the data
	n, err = s.writtenData.Write(p)
	if err != nil {
		return n, err
	}

	// Check if this is a complete message
	if len(p) < p2p.LengthHeader {
		return n, nil
	}

	// Extract the length
	length := binary.LittleEndian.Uint32(p[:p2p.LengthHeader])
	if len(p) < p2p.LengthHeader+int(length) {
		return n, nil
	}

	// Extract the message
	message := p[p2p.LengthHeader : p2p.LengthHeader+int(length)]

	respHeader := make([]byte, p2p.LengthHeader)
	// If we got a batch end message, respond with "done"

	var respData []byte
	switch {
	case bytes.HasPrefix(message, prefixBatchBegin):
		respData = []byte("ack_begin")
	case bytes.HasPrefix(message, prefixBatchHeader):
		respData = []byte("ack_header")
	case bytes.HasPrefix(message, prefixBatchData):
		respData = []byte("ack_data")
	case bytes.HasPrefix(message, prefixBatchEnd):
		respData = []byte("done")
	default:
		panic(fmt.Errorf("unexpected message: %s", hex.EncodeToString(message)))
	}

	binary.LittleEndian.PutUint32(respHeader, uint32(len(respData)))

	// Queue the response
	s.readData <- append(respHeader, respData...)

	return n, nil
}

// GetWrittenData returns all data written to the stream
func (s *AutoResponderStream) GetWrittenData() []byte {
	return s.writtenData.Bytes()
}

// ParseMessages extracts all length-prefixed messages from raw data
func ParseMessages(data []byte) [][]byte {
	var messages [][]byte
	buf := bytes.NewBuffer(data)

	for buf.Len() >= p2p.LengthHeader {
		// Read length header
		lengthBytes := make([]byte, p2p.LengthHeader)
		n, err := buf.Read(lengthBytes)
		if err != nil || n != p2p.LengthHeader {
			break
		}

		length := binary.LittleEndian.Uint32(lengthBytes)
		if buf.Len() < int(length) {
			break
		}

		message := make([]byte, length)
		n, err = buf.Read(message)
		if err != nil || n != int(length) {
			break
		}

		messages = append(messages, message)
	}

	return messages
}

// TestSendAttestationStateEmptyProtocolCorrect is a regression test for the
// totalBatches==0 fallthrough bug in sendAttestationState.
//
// AutoResponderStream always responds "ack_begin" to prefixBatchBegin,
// regardless of totalBatches. That can hide the bug because the buggy sender
// falls through and eventually fails on a different check. Empty-state tests
// therefore use the protocol-correct doneResponderStream instead.
//
// In the real wire protocol, when the sender writes prefixBatchBegin with
// totalBatches=0, the receiver replies with StreamMsgDone (see
// receiveBatchedAttestationState). The pre-fix sender validated the "done"
// ack but then fell through to the streamAckBegin check and logged a false
// "unexpected response to batch begin" error. This test uses a stream that
// responds protocol-correctly with "done" and verifies the sender exits
// cleanly without sending additional batch messages and without the false
// error.
func TestSendAttestationStateEmptyProtocolCorrect(t *testing.T) {
	originalApplyDeadline := p2p.ApplyDeadline.Load()
	p2p.ApplyDeadline.Store(false)
	defer func() { p2p.ApplyDeadline.Store(originalApplyDeadline) }()

	ag, _, _, _, _, _ := setupTestGossip(t)

	stream := newDoneResponderStream("test-peer")

	done := make(chan error, 1)
	go func() {
		done <- ag.sendAttestationState(stream, time.Now().Add(defaultStateSyncTimeout))
	}()

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(500 * time.Millisecond):
		t.Fatal("sendAttestationState did not return after StreamMsgDone ack")
	}

	// Sender must have written exactly one prefixBatchBegin and nothing else
	// (no header, no data, no end) - the empty-state path completes after the
	// done ack.
	messages := ParseMessages(stream.GetWrittenData())
	require.NotEmpty(t, messages, "Should have written at least one message")

	var beginCount, headerCount, dataCount, endCount int
	for _, msg := range messages {
		if len(msg) == 0 {
			continue
		}
		switch {
		case bytes.HasPrefix(msg, prefixBatchBegin):
			beginCount++
			totalBatches := int(binary.LittleEndian.Uint32(msg[1:5]))
			assert.Equal(t, 0, totalBatches, "Empty state should encode totalBatches=0 on the wire")
		case bytes.HasPrefix(msg, prefixBatchHeader):
			headerCount++
		case bytes.HasPrefix(msg, prefixBatchData):
			dataCount++
		case bytes.Equal(msg, prefixBatchEnd):
			endCount++
		}
	}
	assert.Equal(t, 1, beginCount, "Should write exactly one batch begin")
	assert.Equal(t, 0, headerCount, "Should not write any batch header for empty state")
	assert.Equal(t, 0, dataCount, "Should not write any batch data for empty state")
	assert.Equal(t, 0, endCount, "Should not write batch end after a done ack")
}

// doneResponderStream is a MockStream that responds protocol-correctly to a
// prefixBatchBegin with totalBatches=0 by replying with StreamMsgDone, instead
// of always replying ack_begin like AutoResponderStream. Used to verify the
// sender does not fall through to the ack_begin check after a valid done ack.
type doneResponderStream struct {
	pendingRead []byte
	MockStream
	writtenData *bytes.Buffer
	readData    chan []byte
}

func newDoneResponderStream(peer peer.ID) *doneResponderStream {
	return &doneResponderStream{
		MockStream: MockStream{
			peer: peer,
			mu:   new(sync.Mutex),
		},
		writtenData: &bytes.Buffer{},
		readData:    make(chan []byte, 4),
	}
}

func (s *doneResponderStream) Read(p []byte) (n int, err error) {
	if len(s.pendingRead) == 0 {
		select {
		case s.pendingRead = <-s.readData:
		case <-time.After(100 * time.Millisecond):
			return 0, io.EOF
		}
	}
	n = copy(p, s.pendingRead)
	s.pendingRead = s.pendingRead[n:]
	return n, nil
}

func (s *doneResponderStream) Write(p []byte) (n int, err error) {
	n, err = s.writtenData.Write(p)
	if err != nil {
		return n, err
	}
	if len(p) < p2p.LengthHeader {
		return n, nil
	}
	length := binary.LittleEndian.Uint32(p[:p2p.LengthHeader])
	if len(p) < p2p.LengthHeader+int(length) {
		return n, nil
	}
	message := p[p2p.LengthHeader : p2p.LengthHeader+int(length)]

	// Only respond to prefixBatchBegin: reply with the protocol-correct
	// StreamMsgDone if totalBatches==0, ack_begin otherwise.
	if !bytes.HasPrefix(message, prefixBatchBegin) {
		return n, nil
	}
	var resp []byte
	totalBatches := uint32(0)
	if len(message) >= 5 {
		totalBatches = binary.LittleEndian.Uint32(message[1:5])
	}
	if totalBatches == 0 {
		resp = []byte(p2p.StreamMsgDone)
	} else {
		resp = []byte("ack_begin")
	}
	respHeader := make([]byte, p2p.LengthHeader)
	binary.LittleEndian.PutUint32(respHeader, uint32(len(resp)))
	s.readData <- append(respHeader, resp...)
	return n, nil
}

func (s *doneResponderStream) GetWrittenData() []byte {
	return s.writtenData.Bytes()
}

// TestAskStreamForStateEmptyState is a regression test for the prefix-strip
// bug in askStreamForState.
//
// Before the fix, askStreamForState passed the full initialData (including
// the prefixBatchBegin byte) to receiveBatchedAttestationState, which then
// read initialData[:4] as totalBatches and got garbage [0x02, b0, b1, b2]
// instead of the real [b0, b1, b2, b3]. The bug was invisible whenever the
// real totalBatches was non-zero (the receiver loop terminates on
// prefixBatchEnd, not on a counter), so existing send/receive tests with
// non-empty state didn't catch it. It only manifested in the empty-state
// case: real totalBatches=0 was misread as 2, the receiver sent ack_begin
// instead of done, and the sender errored.
//
// This test exercises the full ask/handle wire flow with an EMPTY sender
// state and verifies both sides complete cleanly.
func TestAskStreamForStateEmptyState(t *testing.T) {
	originalApplyDeadline := p2p.ApplyDeadline.Load()
	p2p.ApplyDeadline.Store(false)
	defer func() { p2p.ApplyDeadline.Store(originalApplyDeadline) }()

	agSender, _, _, _, _, _ := setupTestGossip(t)
	agReceiver, _, _, _, _, _ := setupTestGossip(t)

	// Authorize the receiver to ask agSender for state.
	agSender.avMu.Lock()
	agSender.activeVals = map[peer.ID]bool{agReceiver.host.ID(): true}
	agSender.avMu.Unlock()

	// Buffered stream pair: what one writes the other reads.
	senderReader := bytes.NewBuffer(nil)
	senderWriter := &bytes.Buffer{}
	receiverReader := senderWriter
	receiverWriter := senderReader

	var sharedMu sync.Mutex
	receiverStream := &MockStream{
		reader: receiverReader,
		writer: receiverWriter,
		peer:   agSender.host.ID(),
		mu:     &sharedMu,
	}
	senderStream := &MockStream{
		reader: senderReader,
		writer: senderWriter,
		peer:   agReceiver.host.ID(),
		mu:     &sharedMu,
	}

	done := make(chan struct{})
	go func() {
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			agSender.handleStreamAttestationState(senderStream)
		}()
		go func() {
			defer wg.Done()
			agReceiver.askStreamForState(receiverStream)
		}()
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("ask/handle did not complete on empty state - totalBatches=0 wire path is wedged")
	}

	// Receiver should not have populated any observed txs (sender was empty).
	agReceiver.mu.Lock()
	defer agReceiver.mu.Unlock()
	assert.Equal(t, 0, len(agReceiver.observedTxs), "receiver should have no txs after empty-state init")
	assert.Equal(t, 0, len(agReceiver.networkFees), "receiver should have no network fees after empty-state init")
	assert.Equal(t, 0, len(agReceiver.solvencies), "receiver should have no solvencies after empty-state init")
}

// stateSyncTestFrameTimeout replaces the ordinary per-frame read deadline in
// the deadline tests; their stalls, slow batches and budgets are multiples of
// it. It also bounds how long the sender waits for the receiver to process
// each batch, which includes leveldb writes. 250ms was too short on a loaded
// CI runner: TestStateSyncExportReleaseOnEveryExit failed its retry step in
// 1 of 70 to 1 of 280 subtests with 8 copies in parallel; 1s gave 0 of 1,120.
const stateSyncTestFrameTimeout = time.Second

// Successful stream cleanup may log after the handler and its test return.
type stateSyncLogBuffer struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

func (b *stateSyncLogBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.Write(p)
}

func (b *stateSyncLogBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.String()
}

type stateSyncReadDeadline struct {
	at       time.Time
	deadline time.Time
	ordinary bool
}

// TCP buffers the next small header while the receiver processes a batch.
// Unlike the older pipe fixture, these streams enforce deadlines. Scale only
// the sender's ordinary 40-second reads to keep regressions fast; record the
// requested deadline and pass absolute exchange deadlines through unchanged.
// Receiver reads and all writes retain their production deadlines.
type stateSyncDeadlineStream struct {
	MockStream
	transport  net.Conn
	shortReads bool
	reads      chan stateSyncReadDeadline
	resets     atomic.Int32
	closes     atomic.Int32
}

func (s *stateSyncDeadlineStream) Read(p []byte) (int, error) {
	return s.transport.Read(p)
}

func (s *stateSyncDeadlineStream) Write(p []byte) (int, error) {
	return s.transport.Write(p)
}

func (s *stateSyncDeadlineStream) SetReadDeadline(deadline time.Time) error {
	now := time.Now()
	remaining := deadline.Sub(now)
	ordinary := remaining > p2p.TimeoutReadPayload-time.Second && remaining <= p2p.TimeoutReadPayload
	s.reads <- stateSyncReadDeadline{at: now, deadline: deadline, ordinary: ordinary}
	if ordinary && s.shortReads {
		deadline = now.Add(stateSyncTestFrameTimeout)
	}
	return s.transport.SetReadDeadline(deadline)
}

func (s *stateSyncDeadlineStream) SetWriteDeadline(deadline time.Time) error {
	return s.transport.SetWriteDeadline(deadline)
}

func (s *stateSyncDeadlineStream) Reset() error {
	s.resets.Add(1)
	return s.transport.Close()
}

func (s *stateSyncDeadlineStream) Close() error {
	s.closes.Add(1)
	return s.transport.Close()
}

func stateSyncDeadlinePair(t *testing.T) (*stateSyncDeadlineStream, *stateSyncDeadlineStream) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })
	client, err := net.DialTimeout("tcp", listener.Addr().String(), time.Second)
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })
	server, err := listener.Accept()
	require.NoError(t, err)
	t.Cleanup(func() { _ = server.Close() })
	require.NoError(t, listener.Close())
	wrap := func(conn net.Conn, remote peer.ID, shortReads bool) *stateSyncDeadlineStream {
		return &stateSyncDeadlineStream{
			MockStream: MockStream{peer: remote},
			transport:  conn,
			shortReads: shortReads,
			reads:      make(chan stateSyncReadDeadline, 32),
		}
	}
	return wrap(server, "receiver", true), wrap(client, "sender", false)
}

func stateSyncDeadlineFixture(t *testing.T, count int) *AttestationGossip {
	t.Helper()
	sender, _, _, _, _, _ := setupTestGossip(t)
	sender.logger = zerolog.Nop()
	sender.activeVals["receiver"] = true
	sender.config.StateSyncTimeout = 15 * time.Second
	for i := range count {
		fee := &common.NetworkFee{Chain: common.BTCChain, Height: int64(123 + i), TransactionSize: 100, TransactionRate: 42}
		payload, err := fee.GetSignablePayload()
		require.NoError(t, err)
		signature, err := sender.privKey.Sign(payload)
		require.NoError(t, err)
		att := &common.Attestation{PubKey: sender.pubKey, Signature: signature}
		sender.networkFees[*fee] = &AttestationState[*common.NetworkFee]{
			Item: fee, attestations: []attestationSentState{{attestation: att}},
		}
	}
	return sender
}

func stateSyncDeadlineTestSettings(t *testing.T) {
	t.Helper()
	previousLimit, previousDeadline := maxAttestationBatchBytes, p2p.ApplyDeadline.Load()
	maxAttestationBatchBytes = 1 // One small, valid item per batch.
	p2p.ApplyDeadline.Store(true)
	t.Cleanup(func() {
		maxAttestationBatchBytes = previousLimit
		p2p.ApplyDeadline.Store(previousDeadline)
	})
}

func waitStateSyncDeadlineHandler(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("state handler did not finish")
	}
}

func stateSyncNextReadDeadline(t *testing.T, stream *stateSyncDeadlineStream) stateSyncReadDeadline {
	t.Helper()
	select {
	case deadline := <-stream.reads:
		return deadline
	case <-time.After(5 * time.Second):
		t.Fatal("sender did not start the expected read")
		return stateSyncReadDeadline{}
	}
}

// This receiver keeps the existing wire messages but processes each batch
// inline, matching the bounded receiver's ACK order. Processing uses the real
// import handler; only the injected processing delay is synthetic.
func stateSyncReceiveInline(t *testing.T, receiver *AttestationGossip, client *stateSyncDeadlineStream, slowBatch int) {
	t.Helper()
	write := func(payload []byte) { require.NoError(t, p2p.WriteStreamWithBuffer(payload, client)) }
	read := func(prefix []byte) []byte {
		message, err := p2p.ReadStreamWithBuffer(client)
		require.NoError(t, err)
		require.True(t, bytes.HasPrefix(message, prefix))
		return message
	}
	write(prefixSendState)
	begin := read(prefixBatchBegin)
	require.Len(t, begin, 5)
	count := binary.LittleEndian.Uint32(begin[1:])
	require.EqualValues(t, 3, count)
	write([]byte(streamAckBegin))
	for i := range count {
		header := read(prefixBatchHeader)
		require.Len(t, header, 9)
		require.Equal(t, i, binary.LittleEndian.Uint32(header[1:5]))
		write([]byte(streamAckHeader))
		data := read(prefixBatchData)
		require.EqualValues(t, len(data)-1, binary.LittleEndian.Uint32(header[5:9]))
		var batch common.QuorumState
		require.NoError(t, batch.Unmarshal(data[1:]))
		write([]byte(streamAckData))
		if int(i)+1 == slowBatch {
			time.Sleep(2 * stateSyncTestFrameTimeout)
		}
		require.NoError(t, receiver.processAttestationStateBatch(zerolog.Nop(), batch))
	}
	require.Equal(t, prefixBatchEnd, read(prefixBatchEnd))
	write([]byte(p2p.StreamMsgDone))
}

func TestStateSyncSenderProcessingACKDeadlines(t *testing.T) {
	stateSyncDeadlineTestSettings(t)
	for _, tc := range []struct {
		name string
		slow int
	}{
		{"inline middle batch", 2},
		{"inline final batch", 3},
		{"current receiver", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sender := stateSyncDeadlineFixture(t, 3)
			receiver, _, _, _, _, _ := setupTestGossip(t)
			receiver.logger = zerolog.Nop()
			var logs stateSyncLogBuffer
			sender.logger = zerolog.New(&logs)
			server, client := stateSyncDeadlinePair(t)
			done := make(chan struct{})
			go func() { sender.handleStreamAttestationState(server); close(done) }()
			t.Cleanup(func() { _ = server.transport.Close(); waitStateSyncDeadlineHandler(t, done) })
			if tc.slow == 0 {
				receiver.askStreamForState(client)
			} else {
				stateSyncReceiveInline(t, receiver, client, tc.slow)
			}
			waitStateSyncDeadlineHandler(t, done)
			require.Contains(t, logs.String(), "successfully sent attestation state in batches")
			require.NotContains(t, logs.String(), "i/o timeout")
			require.Len(t, receiver.networkFees, 3)
			for fee := range sender.networkFees {
				require.NotNil(t, receiver.networkFees[fee])
				require.Equal(t, 1, receiver.networkFees[fee].AttestationCount())
			}
			var exchangeDeadline time.Time
			for _, ordinary := range []bool{true, true, true, true, false, true, false, true, false} {
				got := stateSyncNextReadDeadline(t, server)
				require.Equal(t, ordinary, got.ordinary)
				if !ordinary {
					if exchangeDeadline.IsZero() {
						exchangeDeadline = got.deadline
					}
					require.Equal(t, exchangeDeadline, got.deadline, "processing must not restart the budget")
				}
			}
		})
	}
}

// Stop at a selected sender ACK read, without sending that ACK.
func stateSyncStallAtACK(t *testing.T, client *stateSyncDeadlineStream, phase string) {
	t.Helper()
	write := func(data []byte) {
		require.NoError(t, p2p.WriteStreamWithBuffer(data, client))
	}
	read := func(prefix []byte) {
		message, err := p2p.ReadStreamWithBuffer(client)
		require.NoError(t, err)
		require.True(t, bytes.HasPrefix(message, prefix))
	}
	write(prefixSendState)
	read(prefixBatchBegin)
	if phase == "begin" {
		return
	}
	write([]byte(streamAckBegin))
	read(prefixBatchHeader)
	if phase == "first header" {
		return
	}
	write([]byte(streamAckHeader))
	read(prefixBatchData)
	if phase == "data" {
		return
	}
	write([]byte(streamAckData))
	if phase == "next header" {
		read(prefixBatchHeader)
	} else {
		require.Equal(t, "final", phase)
		read(prefixBatchEnd)
	}
}

func TestStateSyncSenderOrdinaryACKDeadlines(t *testing.T) {
	stateSyncDeadlineTestSettings(t)
	for _, tc := range []struct {
		phase string
		reads int
		log   string
	}{
		{"begin", 2, "failed to read ack after batch begin"},
		{"first header", 3, "read state batch header ACK"},
		{"data", 4, "read state batch data ACK"},
	} {
		t.Run(tc.phase, func(t *testing.T) {
			sender := stateSyncDeadlineFixture(t, 1)
			var logs stateSyncLogBuffer
			sender.logger = zerolog.New(&logs)
			server, client := stateSyncDeadlinePair(t)
			done := make(chan struct{})
			go func() { sender.handleStreamAttestationState(server); close(done) }()
			t.Cleanup(func() { _ = server.transport.Close(); waitStateSyncDeadlineHandler(t, done) })
			stateSyncStallAtACK(t, client, tc.phase)
			var got stateSyncReadDeadline
			for range tc.reads {
				got = stateSyncNextReadDeadline(t, server)
				require.True(t, got.ordinary)
				require.InDelta(t, float64(p2p.TimeoutReadPayload), float64(got.deadline.Sub(got.at)), float64(time.Second))
			}
			waitStateSyncDeadlineHandler(t, done)
			require.GreaterOrEqual(t, time.Since(got.at), stateSyncTestFrameTimeout)
			require.Contains(t, logs.String(), tc.log)
			require.Contains(t, logs.String(), "i/o timeout")
			require.Zero(t, stateSyncExportCount(sender))
		})
	}
}

func TestStateSyncSenderProcessingACKExpiry(t *testing.T) {
	stateSyncDeadlineTestSettings(t)
	for _, phase := range []string{"next header", "final"} {
		t.Run(phase, func(t *testing.T) {
			count := 1
			if phase == "next header" {
				count = 2
			}
			sender := stateSyncDeadlineFixture(t, count)
			sender.config.StateSyncTimeout = 3 * stateSyncTestFrameTimeout
			var logs stateSyncLogBuffer
			sender.logger = zerolog.New(&logs)
			server, client := stateSyncDeadlinePair(t)
			done := make(chan struct{})
			go func() { sender.handleStreamAttestationState(server); close(done) }()
			t.Cleanup(func() { _ = server.transport.Close(); waitStateSyncDeadlineHandler(t, done) })
			stateSyncStallAtACK(t, client, phase)
			for range 4 {
				require.True(t, stateSyncNextReadDeadline(t, server).ordinary)
			}
			got := stateSyncNextReadDeadline(t, server)
			require.False(t, got.ordinary)
			waitStateSyncDeadlineHandler(t, done)
			require.False(t, time.Now().Before(got.deadline))
			require.NotContains(t, logs.String(), "successfully sent attestation state")
		})
	}
}

func TestStateSyncSenderBudgetIncludesSnapshotConstruction(t *testing.T) {
	stateSyncDeadlineTestSettings(t)
	sender := stateSyncDeadlineFixture(t, 1)
	sender.config.StateSyncTimeout = time.Second
	server, client := stateSyncDeadlinePair(t)
	sender.mu.Lock()
	locked := true
	done := make(chan struct{})
	go func() { sender.handleStreamAttestationState(server); close(done) }()
	t.Cleanup(func() {
		if locked {
			sender.mu.Unlock()
		}
		_ = server.transport.Close()
		waitStateSyncDeadlineHandler(t, done)
	})
	require.NoError(t, p2p.WriteStreamWithBuffer(prefixSendState, client))
	require.Eventually(t, func() bool { return server.resets.Load()+server.closes.Load() > 0 }, 3*time.Second, time.Millisecond,
		"budget must expire even while snapshot construction waits on its lock")
	// S15 makes the snapshot lock wait cancellable. The owner must exit and
	// release its slot while the mutex is still held by this test.
	waitStateSyncDeadlineHandler(t, done)
	require.Zero(t, stateSyncExportCount(sender))
	sender.mu.Unlock()
	locked = false
}

func TestStateSyncSenderDeadlineCancellationAndFallback(t *testing.T) {
	for _, timeout := range []time.Duration{0, -time.Second, 5 * time.Second} {
		t.Run(timeout.String(), func(t *testing.T) {
			sender := stateSyncDeadlineFixture(t, 0)
			sender.config.StateSyncTimeout = timeout
			server, _ := stateSyncDeadlinePair(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			before := time.Now()
			exchange, stop := sender.stateExchangeDeadline(ctx, server)
			deadline, _ := exchange.Deadline()
			defer stop()
			want := timeout
			if want <= 0 {
				want = 30 * time.Minute
			}
			require.False(t, deadline.Before(before.Add(want)))
			require.False(t, deadline.After(time.Now().Add(want)))
			cancel()
			require.Eventually(t, func() bool { return server.resets.Load() > 0 }, time.Second, time.Millisecond)
		})
	}
	sender := stateSyncDeadlineFixture(t, 0)
	server, _ := stateSyncDeadlinePair(t)
	ctx, cancel := context.WithCancel(context.Background())
	_, stop := sender.stateExchangeDeadline(ctx, server)
	stop()
	cancel()
	time.Sleep(20 * time.Millisecond)
	require.Zero(t, server.resets.Load(), "finished transfers must disarm their reset")
}

func stateSyncExportCount(s *AttestationGossip) int {
	return int(s.stateSnapshotsActive.Load())
}

func startStateSyncExport(t *testing.T, sender *AttestationGossip, remote peer.ID) (*stateSyncDeadlineStream, *stateSyncDeadlineStream, <-chan struct{}) {
	t.Helper()
	server, client := stateSyncDeadlinePair(t)
	server.peer = remote
	sender.avMu.Lock()
	sender.activeVals[remote] = true
	sender.avMu.Unlock()
	done := make(chan struct{})
	go func() { sender.handleStreamAttestationState(server); close(done) }()
	t.Cleanup(func() { _ = server.transport.Close(); waitStateSyncDeadlineHandler(t, done) })
	return server, client, done
}

func TestStateSyncExportAdmission(t *testing.T) {
	stateSyncDeadlineTestSettings(t)
	sender := stateSyncDeadlineFixture(t, 2)
	_, a, doneA := startStateSyncExport(t, sender, "a")
	stateSyncStallAtACK(t, a, "next header")
	require.Equal(t, 1, stateSyncExportCount(sender))

	// A duplicate must not consume the other peer's free slot.
	duplicate, duplicateClient, duplicateDone := startStateSyncExport(t, sender, "a")
	require.NoError(t, p2p.WriteStreamWithBuffer(prefixSendState, duplicateClient))
	waitStateSyncDeadlineHandler(t, duplicateDone)
	require.Positive(t, duplicate.resets.Load())
	require.Equal(t, 1, stateSyncExportCount(sender))
	receiver := stateSyncDeadlineFixture(t, 0)
	_, b, doneB := startStateSyncExport(t, sender, "b")
	require.True(t, receiver.askStreamForState(b).completed)
	waitStateSyncDeadlineHandler(t, doneB)
	require.Len(t, receiver.networkFees, 2)
	require.Equal(t, 1, stateSyncExportCount(sender))

	_, b, doneB = startStateSyncExport(t, sender, "b")
	stateSyncStallAtACK(t, b, "next header")
	require.Equal(t, 2, stateSyncExportCount(sender))

	// With both copies held, rejection must not wait for the snapshot lock.
	sender.mu.Lock()
	locked := true
	t.Cleanup(func() {
		if locked {
			sender.mu.Unlock()
		}
	})
	rejected, c, doneC := startStateSyncExport(t, sender, "c")
	require.NoError(t, p2p.WriteStreamWithBuffer(prefixSendState, c))
	select {
	case <-doneC:
	case <-time.After(time.Second):
		sender.mu.Unlock()
		locked = false
		t.Fatal("full capacity reached the snapshot lock")
	}
	require.Positive(t, rejected.resets.Load())
	require.Equal(t, 2, stateSyncExportCount(sender))
	sender.mu.Unlock()
	locked = false
	require.NoError(t, a.Reset())
	require.NoError(t, b.Reset())
	waitStateSyncDeadlineHandler(t, doneA)
	waitStateSyncDeadlineHandler(t, doneB)
	require.Zero(t, stateSyncExportCount(sender))
}

func TestStateSyncExportReleaseOnEveryExit(t *testing.T) {
	stateSyncDeadlineTestSettings(t)
	for _, phase := range []string{"begin", "next header", "final", "bad ack", "reset", "success", "empty"} {
		t.Run(phase, func(t *testing.T) {
			count := 1
			if phase == "next header" {
				count = 2
			}
			if phase == "empty" {
				count = 0
			}
			sender := stateSyncDeadlineFixture(t, count)
			sender.config.StateSyncTimeout = time.Second
			// Build receivers before the handler starts: the sender's first
			// read is scaled to one second, and fixture setup can exceed it.
			receiver := stateSyncDeadlineFixture(t, 0)
			_, client, done := startStateSyncExport(t, sender, "peer")
			if phase == "success" || phase == "empty" {
				require.True(t, receiver.askStreamForState(client).completed)
			} else {
				stall := phase
				if phase == "bad ack" || phase == "reset" {
					stall = "begin"
				}
				stateSyncStallAtACK(t, client, stall)
				require.Equal(t, 1, stateSyncExportCount(sender))
				switch phase {
				case "bad ack":
					require.NoError(t, p2p.WriteStreamWithBuffer([]byte("wrong"), client))
				case "reset":
					require.NoError(t, client.Reset())
				}
			}
			waitStateSyncDeadlineHandler(t, done)
			require.Zero(t, stateSyncExportCount(sender))
			// The same identity can obtain a new copy after any exit.
			sender.config.StateSyncTimeout = 15 * time.Second
			retryReceiver := stateSyncDeadlineFixture(t, 0)
			_, retry, retryDone := startStateSyncExport(t, sender, "peer")
			require.True(t, retryReceiver.askStreamForState(retry).completed)
			waitStateSyncDeadlineHandler(t, retryDone)
			require.Zero(t, stateSyncExportCount(sender))
		})
	}
}

// A small framed reply or a blocked read. Cancellation is exercised through
// Reset, rather than pretending deadline setters interrupt a bytes.Buffer.
type startupRetryStream struct {
	MockStream
	data                *bytes.Reader
	started, stopped    chan struct{}
	startOnce, stopOnce sync.Once
	gate, join          <-chan struct{}
	block               bool
	resets              atomic.Int32
}

func newStartupRetryStream(remote peer.ID, empty bool) *startupRetryStream {
	var frame []byte
	if empty {
		payload := append(append([]byte(nil), prefixBatchBegin...), 0, 0, 0, 0)
		frame = make([]byte, 4+len(payload))
		binary.LittleEndian.PutUint32(frame, uint32(len(payload)))
		copy(frame[4:], payload)
	}
	return &startupRetryStream{
		MockStream: MockStream{peer: remote}, data: bytes.NewReader(frame),
		started: make(chan struct{}), stopped: make(chan struct{}),
	}
}

func (s *startupRetryStream) Read(p []byte) (int, error) {
	s.startOnce.Do(func() { close(s.started) })
	if s.block {
		<-s.stopped
		if s.join != nil {
			<-s.join
		}
		return 0, io.ErrClosedPipe
	}
	if s.gate != nil {
		select {
		case <-s.gate:
		case <-s.stopped:
			return 0, io.ErrClosedPipe
		}
	}
	return s.data.Read(p)
}

func (s *startupRetryStream) Write(p []byte) (int, error) { return len(p), nil }

func (s *startupRetryStream) Reset() error {
	s.resets.Add(1)
	s.stopOnce.Do(func() { close(s.stopped) })
	return nil
}

func (s *startupRetryStream) Close() error {
	s.stopOnce.Do(func() { close(s.stopped) })
	return nil
}

func TestStartupStateSyncBusyThenRecovers(t *testing.T) {
	stateSyncDeadlineTestSettings(t)
	sender := stateSyncDeadlineFixture(t, 2)
	_, stalledA, doneA := startStateSyncExport(t, sender, "holder-a")
	_, stalledB, doneB := startStateSyncExport(t, sender, "holder-b")
	stateSyncStallAtACK(t, stalledA, "next header")
	stateSyncStallAtACK(t, stalledB, "next header")
	require.Equal(t, 2, stateSyncExportCount(sender))

	requesters := make([]*AttestationGossip, 3)
	exits := make(chan chan struct{}, 20)
	for i := range requesters {
		g, h := startupRetryGossip(t, "server")
		requesters[i] = g
		// Prepare sockets on the test goroutine; the production dial callback
		// only consumes prepared pairs and starts the real remote handler.
		pairs := make(chan [2]*stateSyncDeadlineStream, 5)
		for range 5 {
			server, client := stateSyncDeadlinePair(t)
			server.peer = peer.ID(fmt.Sprintf("requester-%d", i))
			sender.avMu.Lock()
			sender.activeVals[server.peer] = true
			sender.avMu.Unlock()
			pairs <- [2]*stateSyncDeadlineStream{server, client}
		}
		h.streamFunc = func(context.Context, peer.ID, ...protocol.ID) (network.Stream, error) {
			select {
			case pair := <-pairs:
				done := make(chan struct{})
				exits <- done
				go func() { sender.handleStreamAttestationState(pair[0]); close(done) }()
				return pair[1], nil
			default:
				return nil, fmt.Errorf("retry socket budget exhausted")
			}
		}
	}
	joinSenders := func() {
		for len(exits) > 0 {
			waitStateSyncDeadlineHandler(t, <-exits)
		}
	}
	now := time.Now()
	for _, g := range requesters {
		g.tickAttestationStateSync(context.Background(), true, true, now)
	}
	for _, g := range requesters {
		settleStartupRetry(t, g, now)
		require.Equal(t, 1, g.startupState.result.failed)
		require.False(t, g.startupState.finished)
	}
	joinSenders()
	require.NoError(t, stalledA.Reset())
	require.NoError(t, stalledB.Reset())
	waitStateSyncDeadlineHandler(t, doneA)
	waitStateSyncDeadlineHandler(t, doneB)
	for range 4 {
		for _, g := range requesters {
			if !g.startupState.finished {
				g.tickAttestationStateSync(context.Background(), true, false, g.startupState.next)
			}
		}
		for _, g := range requesters {
			if !g.startupState.finished {
				settleStartupRetry(t, g, g.startupState.next)
			}
		}
		joinSenders()
	}
	for _, g := range requesters {
		require.True(t, g.startupState.finished)
		require.Equal(t, 1, g.startupState.result.nonEmpty)
		require.LessOrEqual(t, g.startupState.requests, 5)
		require.Len(t, g.networkFees, 2)
		for _, state := range g.networkFees {
			require.Equal(t, 1, state.AttestationCount())
		}
	}
	require.Zero(t, stateSyncExportCount(sender))
}

func TestStartupStateSyncWholeGroupAndEmptyRetry(t *testing.T) {
	g, h := startupRetryGossip(t, "empty", "failed", "slow")
	gate := make(chan struct{})
	defer close(gate)
	slow := newStartupRetryStream("slow", true)
	slow.gate = gate
	empty := newStartupRetryStream("empty", true)
	h.streamFunc = func(_ context.Context, id peer.ID, _ ...protocol.ID) (network.Stream, error) {
		switch id {
		case "slow":
			return slow, nil
		case "empty":
			return empty, nil
		default:
			return nil, fmt.Errorf("busy")
		}
	}
	now := time.Now()
	g.tickAttestationStateSync(context.Background(), true, true, now)
	waitStateSyncDeadlineHandler(t, slow.started)
	waitStateSyncDeadlineHandler(t, empty.stopped)
	for range 3 {
		g.tickAttestationStateSync(context.Background(), true, false, now.Add(time.Minute))
	}
	require.Equal(t, 3, g.startupState.requests)
	require.False(t, g.startupState.finished)
	require.Zero(t, slow.resets.Load(), "a completed peer must not cancel another import")
	// Reset ends the still-running peer; an empty response does not stop collection.
	require.NoError(t, slow.Reset())
	settleStartupRetry(t, g, now.Add(time.Minute))
	require.False(t, g.startupState.finished)
	require.Equal(t, attestationStateGroupResult{empty: 1, failed: 2}, g.startupState.result)
}

func TestStartupStateSyncFreshPeersAndRequestBudget(t *testing.T) {
	g, h := startupRetryGossip(t, "a", "b", "c", "d")
	g.config.AskPeers = 2
	calls := make(chan peer.ID, 10)
	h.streamFunc = func(_ context.Context, id peer.ID, _ ...protocol.ID) (network.Stream, error) {
		calls <- id
		return nil, fmt.Errorf("busy")
	}
	now := time.Now()
	g.startupState = newAttestationStateCollection(context.Background(), now, time.Hour, 5)
	deadline := g.startupState.deadline
	for _, count := range []int{2, 4, 5} {
		dispatch := g.startupState.next
		g.tickAttestationStateSync(context.Background(), true, false, dispatch)
		require.Equal(t, count, g.startupState.requests)
		settleStartupRetry(t, g, dispatch)
		require.Equal(t, deadline, g.startupState.deadline, "retries must not renew the collection window")
		ctxDeadline, _ := g.startupState.ctx.Deadline()
		require.Equal(t, deadline, ctxDeadline)
		if count < 5 {
			g.tickAttestationStateSync(context.Background(), true, false, dispatch.Add(time.Second))
			require.Nil(t, g.startupState.group, "all retry groups require backoff")
		}
	}
	require.True(t, g.startupState.finished)
	require.Equal(t, attestationStateGroupResult{failed: 5}, g.startupState.result)
	seen := map[peer.ID]bool{}
	for range 4 {
		seen[<-calls] = true
	}
	require.Len(t, seen, 4)
	require.True(t, seen[<-calls])
	old := g.startupState
	g.tickAttestationStateSync(context.Background(), true, false, now.Add(2*time.Hour))
	require.Same(t, old, g.startupState)
	require.Equal(t, 5, g.startupState.requests)

	// A peer discovered after reuse still precedes any previously asked peer.
	c := newAttestationStateCollection(context.Background(), now, time.Hour, 50)
	defer c.cancel()
	c.selectPeers([]peer.ID{"a"}, 1)
	c.selectPeers([]peer.ID{"a"}, 1)
	require.Equal(t, []peer.ID{"new"}, c.selectPeers([]peer.ID{"a", "new"}, 2))
}

func TestStartupStateSyncCancellationAndWindow(t *testing.T) {
	for _, mode := range []string{"parent", "shutdown", "leave", "window", "attempt"} {
		t.Run(mode, func(t *testing.T) {
			g, h := startupRetryGossip(t, "peer")
			stream := newStartupRetryStream("peer", false)
			stream.block = true
			var dialDeadline time.Time
			h.streamFunc = func(ctx context.Context, _ peer.ID, _ ...protocol.ID) (network.Stream, error) {
				dialDeadline, _ = ctx.Deadline()
				return stream, nil
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			now := time.Now()
			// The test needs the dial to happen before the budget expires.
			// The window starts here, before the dial goroutines run, so a
			// 50ms window could expire on a loaded runner before NewStream,
			// and stream.started never closed. The attempt budget starts
			// inside the per-peer goroutine just before NewStream, so only a
			// preemption in that gap can end the attempt before the dial.
			window := time.Hour
			if mode == "window" {
				window = stateSyncTestFrameTimeout
			}
			if mode == "attempt" {
				g.config.StateSyncTimeout = stateSyncTestFrameTimeout
			}
			g.startupState = newAttestationStateCollection(ctx, now, window, 50)
			g.tickAttestationStateSync(ctx, true, false, now)
			group := g.startupState.group
			waitStateSyncDeadlineHandler(t, stream.started)
			switch mode {
			case "parent":
				cancel()
			case "shutdown":
				g.stopStartupStateSync()
			case "leave":
				g.tickAttestationStateSync(ctx, false, false, now)
			}
			waitStateSyncDeadlineHandler(t, group.done)
			g.tickAttestationStateSync(ctx, true, false, now)
			require.Positive(t, stream.resets.Load())
			require.Equal(t, 1, g.startupState.requests)
			require.Equal(t, mode != "attempt", g.startupState.finished)
			if mode == "window" {
				require.Equal(t, g.startupState.deadline, dialDeadline)
			}
			if mode == "attempt" {
				require.True(t, dialDeadline.Before(g.startupState.deadline))
			}
		})
	}
}

func TestStartupStateSyncReactivationJoinsPriorGroup(t *testing.T) {
	g, h := startupRetryGossip(t, "peer")
	join := make(chan struct{})
	var once sync.Once
	release := func() { once.Do(func() { close(join) }) }
	defer release()
	stream := newStartupRetryStream("peer", false)
	stream.block, stream.join = true, join
	var calls atomic.Int32
	h.streamFunc = func(_ context.Context, id peer.ID, _ ...protocol.ID) (network.Stream, error) {
		if calls.Add(1) == 1 {
			return stream, nil
		}
		return newStartupRetryStream(id, true), nil
	}
	now := time.Now()
	g.tickAttestationStateSync(context.Background(), true, true, now)
	old := g.startupState
	waitStateSyncDeadlineHandler(t, stream.started)
	g.tickAttestationStateSync(context.Background(), false, false, now)
	waitStateSyncDeadlineHandler(t, stream.stopped)
	activated := now.Add(time.Second)
	g.tickAttestationStateSync(context.Background(), true, true, activated)
	require.Same(t, old, g.startupState)
	require.EqualValues(t, 1, calls.Load())
	release()
	waitStateSyncDeadlineHandler(t, old.group.done)
	g.tickAttestationStateSync(context.Background(), true, false, activated.Add(time.Minute))
	require.NotSame(t, old, g.startupState)
	require.Equal(t, activated.Add(startupStateSyncWindow), g.startupState.deadline)
	settleStartupRetry(t, g, activated.Add(time.Minute))
	require.False(t, g.startupState.finished)
	require.Equal(t, 1, g.startupState.result.empty)
	require.EqualValues(t, 2, calls.Load())
}

func TestStartupStateSyncNoPeersAndJitter(t *testing.T) {
	g, _ := startupRetryGossip(t)
	now := time.Now()
	g.startupState = newAttestationStateCollection(context.Background(), now, 30*time.Millisecond, 50)
	g.tickAttestationStateSync(context.Background(), true, false, now)
	require.Nil(t, g.startupState.group)
	require.Zero(t, g.startupState.requests)
	waitStateSyncDeadlineHandler(t, g.startupState.ctx.Done())
	g.tickAttestationStateSync(context.Background(), true, false, now)
	require.True(t, g.startupState.finished)
	for _, round := range []int{1, 2, 10, 1000} {
		for _, random := range []float64{0, 0.5, 1} {
			delay := attestationStateRetryDelay(round, random)
			require.GreaterOrEqual(t, delay, 2500*time.Millisecond)
			require.LessOrEqual(t, delay, time.Minute)
		}
	}
}

// Exercise the actual startup group and Observer.Stop, including the production
// bridge's HTTP cancellation and its wait behind a legacy cached request.
func TestStartupStateSyncObserverStopCancelsVaultLookup(t *testing.T) {
	for _, phase := range []string{"request", "cache wait"} {
		t.Run(phase, func(t *testing.T) {
			g, h, _, _, legacy, _ := setupTestGossip(t)
			g.config.AskPeers = 1
			g.config.StateSyncTimeout = time.Hour // Only shutdown should end this import.
			g.activeVals = map[peer.ID]bool{h.peers[1]: true}
			tx, _ := observerTestXMRPlan(t)
			batch := signedCancellationBatch(t, tx, secp256k1.GenPrivKey())
			data, err := batch.Marshal()
			require.NoError(t, err)
			stream := newStateBoundsStream(h.peers[1], append([]byte{prefixBatchBegin[0]}, stateBoundsCount(1)...), stateBoundsHeader(0, uint32(len(data))), append([]byte{prefixBatchData[0]}, data...), prefixBatchEnd)
			h.streamFunc = func(ctx context.Context, _ peer.ID, _ ...protocol.ID) (network.Stream, error) {
				return stream, nil
			}

			requestStarted, requestExited := make(chan struct{}), make(chan struct{})
			release := make(chan struct{})
			var releaseOnce sync.Once
			unblock := func() { releaseOnce.Do(func() { close(release) }) }
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if requests.Add(1) == 1 {
					close(requestStarted)
					defer close(requestExited)
				}
				select {
				case <-r.Context().Done():
				case <-release:
					_, _ = w.Write([]byte("{}"))
				}
			}))
			t.Cleanup(server.Close)
			t.Cleanup(unblock)
			bridge, err := thorclient.NewThorchainBridge(config.BifrostClientConfiguration{ChainID: "thorchain", ChainHost: server.URL}, getSharedTestMetrics(), nil)
			require.NoError(t, err)
			queries, ok := bridge.(thorclient.ContextQueryBridge)
			require.True(t, ok)
			lookupStarted, lookupExited := make(chan struct{}), make(chan struct{})
			g.bridge = &cancellationQueryBridge{MockThorchainBridge: legacy, vault: func(ctx context.Context, key string) (thortypes.Vault, error) {
				close(lookupStarted)
				defer close(lookupExited)
				vault, queryErr := queries.GetVaultWithContext(ctx, key)
				return vault, queryErr
			}}
			// If state processing regresses to the legacy API, it must hit the same
			// stalled request rather than silently failing before the tested decision.
			legacy.getVaultFunc = func(key string) (thortypes.Vault, error) {
				close(lookupStarted)
				defer close(lookupExited)
				return bridge.GetVault(key)
			}
			if phase == "cache wait" {
				legacyDone := make(chan struct{})
				go func() { _, _ = bridge.GetVault(tx.ObservedPubKey.String()); close(legacyDone) }()
				t.Cleanup(func() { unblock(); waitStateBounds(t, legacyDone) })
				waitStateBounds(t, requestStarted)
			}
			pkm, err := pubkeymanager.NewPubKeyManager(nil, nil)
			require.NoError(t, err)
			o, err := NewObserver(pkm, nil, legacy, getSharedTestMetrics(), t.TempDir(), metrics.NewTssKeysignMetricMgr(), g, "")
			require.NoError(t, err)
			var imports atomic.Int32
			g.SetObserverApplySpentOutputRefs(func(spentOutputRefsImport, func(bool)) { imports.Add(1) })
			stopped := make(chan struct{})
			var stopOnce sync.Once
			var stopErr error
			stop := func() { stopOnce.Do(func() { go func() { stopErr = o.Stop(); close(stopped) }() }) }
			t.Cleanup(func() { unblock(); stop(); waitStateBounds(t, stopped) })
			g.tickAttestationStateSync(context.Background(), true, true, time.Now())
			group := g.startupState.group
			require.NotNil(t, group)
			waitStateBounds(t, lookupStarted)
			waitStateBounds(t, requestStarted)
			stop()
			select {
			case <-stopped:
				require.NoError(t, stopErr)
			case <-time.After(3 * time.Second):
				t.Fatal("Observer.Stop did not cancel and join the stalled vault lookup")
			}
			waitStateBounds(t, lookupExited)
			waitStateBounds(t, group.done)
			require.True(t, g.startupState.finished)
			require.True(t, g.startupStateStopped)
			require.Zero(t, group.result.nonEmpty)
			require.Positive(t, stream.resets.Load())
			require.Zero(t, imports.Load(), "cancelled authorization must not launch an import")
			require.Zero(t, cancellationTestReservationCount(g))
			_, err = o.storage.db.Has([]byte("closed-after-join"), nil)
			require.Error(t, err)
			if phase != "cache wait" {
				waitStateBounds(t, requestExited)
			} else {
				select {
				case <-requestExited:
					t.Fatal("cancelling the cache waiter interrupted the unrelated legacy request")
				default:
				}
			}
			require.EqualValues(t, 1, requests.Load(), "cache wait cancellation must not launch a second request")
		})
	}
}

func TestStateSyncSlowProcessingACKDeadlines(t *testing.T) {
	stateSyncDeadlineTestSettings(t)
	for _, tc := range []struct {
		name string
		slow int32
	}{
		{"middle batch", 2},
		{"final batch", 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sender := stateSyncDeadlineFixture(t, 3)
			receiver, _, _, _, _, _ := setupTestGossip(t)
			var senderLogs, receiverLogs stateSyncLogBuffer
			sender.logger = zerolog.New(&senderLogs)
			receiver.logger = zerolog.New(&receiverLogs)
			receiver.config.StateSyncTimeout = sender.config.StateSyncTimeout
			for _, state := range sender.networkFees {
				stateSyncTrustAttestors(t, receiver, []*common.Attestation{state.attestations[0].attestation})
			}
			originalVerify := verifySignature
			var verified atomic.Int32
			verifySignature = func(payload, signature, pubkey []byte) error {
				if verified.Add(1) == tc.slow {
					time.Sleep(2 * stateSyncTestFrameTimeout)
				}
				return originalVerify(payload, signature, pubkey)
			}
			t.Cleanup(func() { verifySignature = originalVerify })
			server, client := stateSyncDeadlinePair(t)
			serverDone, clientDone := make(chan struct{}), make(chan struct{})
			go func() { sender.handleStreamAttestationState(server); close(serverDone) }()
			go func() { receiver.askStreamForState(client); close(clientDone) }()
			t.Cleanup(func() {
				_ = server.transport.Close()
				_ = client.transport.Close()
				waitStateSyncDeadlineHandler(t, serverDone)
				waitStateSyncDeadlineHandler(t, clientDone)
			})
			waitStateSyncDeadlineHandler(t, serverDone)
			waitStateSyncDeadlineHandler(t, clientDone)
			require.Zero(t, server.resets.Load(), "sender: %s; receiver: %s", senderLogs.String(), receiverLogs.String())
			require.Zero(t, client.resets.Load())
			require.EqualValues(t, 1, server.closes.Load())
			require.EqualValues(t, 1, client.closes.Load())
			require.Len(t, receiver.networkFees, 3)
			for fee := range sender.networkFees {
				require.Equal(t, 1, receiver.networkFees[fee].AttestationCount())
			}
			require.Zero(t, sender.stateSnapshotsActive.Load())
			requireStateRequestCount(t, sender, 0)

			// Request, begin, first header and all data ACKs keep frame deadlines.
			// Later header ACKs and the final ACK share one original deadline.
			var exchangeDeadline time.Time
			for _, ordinary := range []bool{true, true, true, true, false, true, false, true, false} {
				got := stateSyncNextReadDeadline(t, server)
				require.Equal(t, ordinary, got.ordinary)
				if !ordinary {
					if exchangeDeadline.IsZero() {
						exchangeDeadline = got.deadline
					}
					require.Equal(t, exchangeDeadline, got.deadline, "processing must not restart the budget")
				}
			}
		})
	}
}

func TestStateSyncOrdinaryACKDeadlines(t *testing.T) {
	stateSyncDeadlineTestSettings(t)
	for _, tc := range []struct {
		phase string
		reads int
		log   string
	}{
		{"begin", 2, "failed to read ack after batch begin"},
		{"first header", 3, "read state batch header ACK"},
		{"data", 4, "read state batch data ACK"},
	} {
		t.Run(tc.phase, func(t *testing.T) {
			sender := stateSyncDeadlineFixture(t, 1)
			var logs stateSyncLogBuffer
			sender.logger = zerolog.New(&logs)
			server, client := stateSyncDeadlinePair(t)
			done := make(chan struct{})
			go func() { sender.handleStreamAttestationState(server); close(done) }()
			t.Cleanup(func() { _ = server.transport.Close(); waitStateSyncDeadlineHandler(t, done) })
			stateSyncStallAtACK(t, client, tc.phase)
			var got stateSyncReadDeadline
			for range tc.reads {
				got = stateSyncNextReadDeadline(t, server)
				require.True(t, got.ordinary)
				require.InDelta(t, float64(p2p.TimeoutReadPayload), float64(got.deadline.Sub(got.at)), float64(time.Second))
			}
			waitStateSyncDeadlineHandler(t, done)
			require.GreaterOrEqual(t, time.Since(got.at), stateSyncTestFrameTimeout)
			require.Contains(t, logs.String(), tc.log)
			require.Contains(t, logs.String(), "i/o timeout")
			require.Positive(t, server.resets.Load())
			require.Zero(t, server.closes.Load())
			require.Zero(t, sender.stateSnapshotsActive.Load())
			requireStateRequestCount(t, sender, 0)
		})
	}
}

func TestStateSyncProcessingACKExpiryAndShutdown(t *testing.T) {
	stateSyncDeadlineTestSettings(t)
	for _, phase := range []string{"next header", "final"} {
		for _, shutdown := range []bool{false, true} {
			name := phase + "/expiry"
			if shutdown {
				name = phase + "/shutdown"
			}
			t.Run(name, func(t *testing.T) {
				count := 1
				if phase == "next header" {
					count = 2
				}
				sender := stateSyncDeadlineFixture(t, count)
				if !shutdown {
					sender.config.StateSyncTimeout = 3 * stateSyncTestFrameTimeout
				}
				server, client := stateSyncDeadlinePair(t)
				done := make(chan struct{})
				started := time.Now()
				go func() { sender.handleStreamAttestationState(server); close(done) }()
				t.Cleanup(func() { _ = server.transport.Close(); waitStateSyncDeadlineHandler(t, done) })
				stateSyncStallAtACK(t, client, phase)
				requestRead := stateSyncNextReadDeadline(t, server)
				for range 3 {
					require.True(t, stateSyncNextReadDeadline(t, server).ordinary)
				}
				processingRead := stateSyncNextReadDeadline(t, server)
				require.False(t, processingRead.ordinary)
				require.False(t, processingRead.deadline.Before(started.Add(sender.config.StateSyncTimeout)))
				require.False(t, processingRead.deadline.After(requestRead.at.Add(sender.config.StateSyncTimeout)),
					"deadline must start before the request and snapshot construction")
				require.EqualValues(t, 1, sender.stateSnapshotsActive.Load())
				if shutdown {
					sender.stopAttestationStateRequests()
				}
				waitStateSyncDeadlineHandler(t, done)
				if !shutdown {
					require.False(t, time.Now().Before(processingRead.deadline))
				}
				require.Positive(t, server.resets.Load())
				require.Zero(t, server.closes.Load())
				require.Zero(t, sender.stateSnapshotsActive.Load())
				requireStateRequestCount(t, sender, 0)
				if !shutdown {
					// Expiry releases both admission and snapshot ownership.
					frames := [][]byte{prefixSendState, []byte(streamAckBegin)}
					for range count {
						frames = append(frames, []byte(streamAckHeader), []byte(streamAckData))
					}
					frames = append(frames, []byte(p2p.StreamMsgDone))
					next := newStateBoundsStream("receiver", frames...)
					sender.handleStreamAttestationState(next)
					require.EqualValues(t, 1, next.closes.Load())
					requireStateRequestCount(t, sender, 0)
				}
			})
		}
	}
}

func TestStartupStateSyncBoundedImportRejectionThenRecovers(t *testing.T) {
	stateSyncDeadlineTestSettings(t)
	requester, host := startupRetryGossip(t, "sender")
	sender := stateSyncDeadlineFixture(t, 1)
	server, client := stateSyncDeadlinePair(t)
	done := make(chan struct{})
	// A complete header with the wrong batch number must fail collection,
	// even though the peer supplied a valid begin frame.
	bad := newStateBoundsStream("sender",
		append(append([]byte(nil), prefixBatchBegin...), stateBoundsCount(1)...),
		stateBoundsHeader(1, 1))
	var attempts atomic.Int32
	host.streamFunc = func(context.Context, peer.ID, ...protocol.ID) (network.Stream, error) {
		if attempts.Add(1) == 1 {
			return bad, nil
		}
		go func() { sender.handleStreamAttestationState(server); close(done) }()
		return client, nil
	}
	t.Cleanup(func() {
		_ = server.transport.Close()
		if attempts.Load() > 1 {
			waitStateSyncDeadlineHandler(t, done)
		}
	})
	now := time.Now()
	requester.tickAttestationStateSync(context.Background(), true, true, now)
	settleStartupRetry(t, requester, now)
	require.Equal(t, attestationStateGroupResult{failed: 1}, requester.startupState.result)
	require.False(t, requester.startupState.finished)
	require.Positive(t, bad.resets.Load())
	require.Empty(t, requester.networkFees)

	dispatch := requester.startupState.next
	requester.tickAttestationStateSync(context.Background(), true, false, dispatch)
	settleStartupRetry(t, requester, dispatch)
	waitStateSyncDeadlineHandler(t, done)
	require.True(t, requester.startupState.finished)
	require.Equal(t, attestationStateGroupResult{failed: 1, nonEmpty: 1}, requester.startupState.result)
	require.Equal(t, 2, requester.startupState.requests)
	require.Len(t, requester.networkFees, 1)
	for _, state := range requester.networkFees {
		require.Equal(t, 1, state.AttestationCount())
	}
	require.Zero(t, sender.stateSnapshotsActive.Load())
	requireStateRequestCount(t, sender, 0)
}

func stateSyncExportForTest(t *testing.T, s *AttestationGossip, wantItems int) common.QuorumState {
	t.Helper()
	stream := newStateBoundsStream("receiver", []byte(streamAckBegin), []byte(streamAckHeader), []byte(streamAckData), []byte(p2p.StreamMsgDone))
	if wantItems == 0 {
		stream = newStateBoundsStream("receiver", []byte(p2p.StreamMsgDone))
	}
	require.NoError(t, s.sendAttestationState(stream, time.Now().Add(defaultStateSyncTimeout)))
	frames := ParseMessages(stream.output.Bytes())
	require.NotEmpty(t, frames)
	require.Len(t, frames[0], 5)
	require.Equal(t, prefixBatchBegin[0], frames[0][0])
	count := int(binary.LittleEndian.Uint32(frames[0][1:]))
	var all common.QuorumState
	if wantItems == 0 {
		require.Zero(t, count)
		require.Len(t, frames, 1)
		return all
	}
	require.Equal(t, 1, count, "the test fixture fits in one batch")
	require.Len(t, frames, 2*count+2)
	require.Equal(t, prefixBatchEnd, frames[len(frames)-1])
	for i := range count {
		data := frames[2+2*i]
		require.Equal(t, prefixBatchData[0], data[0])
		require.LessOrEqual(t, len(data), p2p.MaxPayload)
		var batch common.QuorumState
		require.NoError(t, batch.Unmarshal(data[1:]))
		require.NoError(t, validateAttestationStateBatch(batch))
		all.QuoTxs = append(all.QuoTxs, batch.QuoTxs...)
		all.QuoNetworkFees = append(all.QuoNetworkFees, batch.QuoNetworkFees...)
		all.QuoSolvencies = append(all.QuoSolvencies, batch.QuoSolvencies...)
		all.QuoErrataTxs = append(all.QuoErrataTxs, batch.QuoErrataTxs...)
		all.QuoPriceFeeds = append(all.QuoPriceFeeds, batch.QuoPriceFeeds...)
	}
	require.Equal(t, wantItems, attestationStateBatchItemCount(all))
	return all
}

func TestStateSyncRejectedAttestationsDoNotBlockExport(t *testing.T) {
	s, _, _, _, _, _ := setupTestGossip(t)
	s.logger = zerolog.Nop()
	fee, atts := stateSyncSignedFee(t, 123456, 1)
	invalid := &common.Attestation{PubKey: atts[0].PubKey, Signature: make([]byte, 64)}
	for i := range maxAttestationStateItemsPerTransfer + 1 {
		item := &common.NetworkFee{Chain: common.BTCChain, Height: int64(i + 1), TransactionSize: 100, TransactionRate: 2}
		s.handleNetworkFeeAttestation(context.Background(), common.AttestNetworkFee{
			NetworkFee: item, Attestation: invalid,
		})
	}
	// Retaining rejected buckets is a separate ingress/cleanup issue. They
	// must not consume the export budget or prevent unrelated valid evidence.
	require.Len(t, s.networkFees, maxAttestationStateItemsPerTransfer+1)
	for _, state := range s.networkFees {
		require.Zero(t, state.AttestationCount())
	}
	s.handleNetworkFeeAttestation(context.Background(), common.AttestNetworkFee{
		NetworkFee: fee, Attestation: atts[0],
	})
	exported := stateSyncExportForTest(t, s, 1)
	require.Equal(t, 1, attestationStateBatchItemCount(exported))
	require.Equal(t, []*common.QuorumNetworkFee{{NetworkFee: fee, Attestations: atts}}, exported.QuoNetworkFees)
}

func TestStateSyncEmptyBucketsAfterFullExport(t *testing.T) {
	for _, committed := range []bool{false, true} {
		name := "empty"
		if committed {
			name = "fully committed"
		}
		t.Run(name, func(t *testing.T) {
			s, _, _, _, _, _ := setupTestGossip(t)
			s.logger = zerolog.Nop()
			s.priceFeeds = make(map[string]*AttestationState[*common.PriceFeed])
			// These bytes exercise export accounting only, not signature checks.
			att := &common.Attestation{PubKey: []byte{1}, Signature: []byte{2}}
			for i := range maxAttestationStateItemsPerTransfer {
				fee := &common.NetworkFee{Chain: common.BTCChain, Height: int64(i)}
				s.networkFees[*fee] = &AttestationState[*common.NetworkFee]{
					Item: fee, attestations: []attestationSentState{{attestation: att}},
				}
			}
			var inactive []attestationSentState
			if committed {
				inactive = []attestationSentState{{attestation: att, committed: true}}
			}
			tx := &common.ObservedTx{Tx: common.Tx{Chain: common.BTCChain, ID: "inactive-tx"}}
			s.observedTxs[txKey{Chain: common.BTCChain, ID: tx.Tx.ID}] = &AttestationState[*attestableObservedTx]{
				Item: &attestableObservedTx{ObservedTx: tx}, attestations: inactive,
			}
			// Fees precede these categories, so the full budget is always
			// spent before visiting these buckets, regardless of map order.
			solvency := &common.Solvency{Chain: common.BTCChain, Id: "inactive-solvency"}
			s.solvencies[solvency.Id] = &AttestationState[*common.Solvency]{Item: solvency, attestations: inactive}
			errata := &common.ErrataTx{Chain: common.BTCChain, Id: "inactive-errata"}
			s.errataTxs[*errata] = &AttestationState[*common.ErrataTx]{Item: errata, attestations: inactive}
			s.priceFeeds["inactive-price"] = &AttestationState[*common.PriceFeed]{Item: &common.PriceFeed{Time: 1}, attestations: inactive}

			exported := stateSyncExportForTest(t, s, maxAttestationStateItemsPerTransfer)
			require.Equal(t, maxAttestationStateItemsPerTransfer, attestationStateBatchItemCount(exported))
			require.Len(t, exported.QuoNetworkFees, maxAttestationStateItemsPerTransfer)
			seen := make(map[int64]bool)
			for _, item := range exported.QuoNetworkFees {
				require.False(t, seen[item.NetworkFee.Height], "duplicate exported fee")
				seen[item.NetworkFee.Height] = true
				require.Equal(t, []*common.Attestation{att}, item.Attestations)
			}
			// The same non-exportable categories must also produce a valid
			// empty transfer once the only exportable category is removed.
			clear(s.networkFees)
			require.Zero(t, attestationStateBatchItemCount(stateSyncExportForTest(t, s, 0)))
		})
	}
}

func TestStateSyncSnapshotSlots(t *testing.T) {
	stateSyncDeadlineTestSettings(t)
	for _, exit := range []string{"reset", "expiry", "shutdown"} {
		t.Run(exit, func(t *testing.T) {
			sender := stateSyncDeadlineFixture(t, 1)
			sender.activeVals["second"] = true
			sender.activeVals["third"] = true
			if exit == "expiry" {
				sender.config.StateSyncTimeout = 3 * stateSyncTestFrameTimeout
			}
			first, firstClient := stateSyncDeadlinePair(t)
			firstDone := make(chan struct{})
			go func() { sender.handleStreamAttestationState(first); close(firstDone) }()
			t.Cleanup(func() { _ = first.Reset(); waitStateSyncDeadlineHandler(t, firstDone) })
			stateSyncStallAtACK(t, firstClient, "final")
			require.EqualValues(t, 1, sender.stateSnapshotsActive.Load())

			duplicate := newStateBoundsStream("receiver", prefixSendState)
			sender.handleStreamAttestationState(duplicate)
			require.Zero(t, duplicate.readBytes.Load(), "duplicate peer must be rejected before reading")
			require.EqualValues(t, 1, duplicate.resets.Load())
			require.EqualValues(t, 1, sender.stateSnapshotsActive.Load())

			// Another peer can finish while the first peer withholds its ACK.
			successful := newStateBoundsStream("second", prefixSendState, []byte(streamAckBegin), []byte(streamAckHeader), []byte(streamAckData), []byte(p2p.StreamMsgDone))
			sender.handleStreamAttestationState(successful)
			frames := ParseMessages(successful.output.Bytes())
			require.Len(t, frames, 4)
			require.Equal(t, prefixBatchEnd, frames[3])
			var batch common.QuorumState
			require.NoError(t, batch.Unmarshal(frames[2][1:]))
			require.Len(t, batch.QuoNetworkFees, 1)
			require.EqualValues(t, 1, sender.stateSnapshotsActive.Load())
			requireStateRequestCount(t, sender, 1)

			second, secondClient := stateSyncDeadlinePair(t)
			second.peer = "second"
			secondDone := make(chan struct{})
			go func() { sender.handleStreamAttestationState(second); close(secondDone) }()
			t.Cleanup(func() { _ = second.Reset(); waitStateSyncDeadlineHandler(t, secondDone) })
			stateSyncStallAtACK(t, secondClient, "final")
			require.EqualValues(t, 2, sender.stateSnapshotsActive.Load())

			third := newStateBoundsStream("third", prefixSendState)
			sender.handleStreamAttestationState(third)
			require.EqualValues(t, 5, third.readBytes.Load())
			require.Zero(t, third.output.Len(), "a third snapshot must not be built")
			require.EqualValues(t, 1, third.resets.Load())
			require.EqualValues(t, 2, sender.stateSnapshotsActive.Load())
			requireStateRequestCount(t, sender, 2)

			switch exit {
			case "reset":
				require.NoError(t, first.Reset())
				waitStateSyncDeadlineHandler(t, firstDone)
				require.EqualValues(t, 1, sender.stateSnapshotsActive.Load())
				require.NoError(t, second.Reset())
			case "shutdown":
				sender.stopAttestationStateRequests()
			}
			waitStateSyncDeadlineHandler(t, firstDone)
			waitStateSyncDeadlineHandler(t, secondDone)
			require.Zero(t, sender.stateSnapshotsActive.Load())
			requireStateRequestCount(t, sender, 0)
			if exit != "shutdown" {
				recovered := newStateBoundsStream("third", prefixSendState, []byte(streamAckBegin), []byte(streamAckHeader), []byte(streamAckData), []byte(p2p.StreamMsgDone))
				sender.handleStreamAttestationState(recovered)
				require.Len(t, ParseMessages(recovered.output.Bytes()), 4)
				require.Zero(t, sender.stateSnapshotsActive.Load())
				requireStateRequestCount(t, sender, 0)
			}
		})
	}
}

// This stream preserves unread bytes and exposes the precise preflight read
// boundary. A blocked read is released only by reset, like a stopped transport.
type stateBoundsStream struct {
	MockStream
	input     *bytes.Reader
	output    bytes.Buffer
	readBytes atomic.Int64
	resets    atomic.Int32
	closes    atomic.Int32
	block     bool
	blocked   chan struct{}
	stopped   chan struct{}
	blockOnce sync.Once
	stopOnce  sync.Once
}

func newStateBoundsStream(remote peer.ID, frames ...[]byte) *stateBoundsStream {
	var input []byte
	for _, frame := range frames {
		input = append(input, encodeStreamMsg(frame)...)
	}
	return &stateBoundsStream{
		MockStream: MockStream{peer: remote, mu: new(sync.Mutex)},
		input:      bytes.NewReader(input), blocked: make(chan struct{}), stopped: make(chan struct{}),
	}
}

func (s *stateBoundsStream) Read(p []byte) (int, error) {
	if s.block && s.input.Len() == 0 {
		s.blockOnce.Do(func() { close(s.blocked) })
		<-s.stopped
		return 0, io.ErrClosedPipe
	}
	n, err := s.input.Read(p)
	s.readBytes.Add(int64(n))
	return n, err
}

func (s *stateBoundsStream) Write(p []byte) (int, error) { return s.output.Write(p) }

func (s *stateBoundsStream) Reset() error {
	s.resets.Add(1)
	s.stopOnce.Do(func() { close(s.stopped) })
	return nil
}

func (s *stateBoundsStream) Close() error { s.closes.Add(1); return nil }

func (s *stateBoundsStream) SetReadDeadline(time.Time) error { return nil }

func (s *stateBoundsStream) SetWriteDeadline(time.Time) error { return nil }

func stateBoundsCount(n uint32) []byte {
	b := make([]byte, 4)
	binary.LittleEndian.PutUint32(b, n)
	return b
}

func stateBoundsHeader(n, size uint32) []byte {
	return append(append([]byte{prefixBatchHeader[0]}, stateBoundsCount(n)...), stateBoundsCount(size)...)
}

func newStateBoundsGossip(peers ...peer.ID) *AttestationGossip {
	s := &AttestationGossip{logger: zerolog.Nop(), activeVals: make(map[peer.ID]bool)}
	for _, p := range peers {
		s.activeVals[p] = true
	}
	return s
}

type stateBoundsPipeStream struct {
	MockStream
	pipe net.Conn
}

func (s *stateBoundsPipeStream) Read(p []byte) (int, error) { return s.pipe.Read(p) }

func (s *stateBoundsPipeStream) Write(p []byte) (int, error) { return s.pipe.Write(p) }

func (s *stateBoundsPipeStream) Reset() error { return s.pipe.Close() }

func (s *stateBoundsPipeStream) Close() error { return s.pipe.Close() }

func stateBoundsPipe(t *testing.T, first, second peer.ID) (*stateBoundsPipeStream, *stateBoundsPipeStream) {
	t.Helper()
	a, b := net.Pipe()
	require.NoError(t, a.SetDeadline(time.Now().Add(5*time.Second)))
	require.NoError(t, b.SetDeadline(time.Now().Add(5*time.Second)))
	t.Cleanup(func() { _ = a.Close(); _ = b.Close() })
	return &stateBoundsPipeStream{MockStream: MockStream{peer: first}, pipe: a}, &stateBoundsPipeStream{MockStream: MockStream{peer: second}, pipe: b}
}

func requireStateRequestCount(t *testing.T, s *AttestationGossip, count int) {
	t.Helper()
	s.stateRequestMu.Lock()
	defer s.stateRequestMu.Unlock()
	require.Len(t, s.stateRequests, count)
}

func waitStateBounds(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("state handler did not finish")
	}
}

func TestStateBoundsLocalExportCeilings(t *testing.T) {
	s := newStateBoundsGossip()
	s.networkFees = make(map[common.NetworkFee]*AttestationState[*common.NetworkFee])
	for i := range 10000 {
		fee := &common.NetworkFee{Chain: common.BTCChain, Height: int64(i)}
		s.networkFees[*fee] = &AttestationState[*common.NetworkFee]{Item: fee, attestations: []attestationSentState{{attestation: &common.Attestation{}}}}
	}
	stream := newStateBoundsStream("active", []byte(streamAckBegin), []byte(streamAckHeader), []byte(streamAckData), []byte(p2p.StreamMsgDone))
	require.NoError(t, s.sendAttestationState(stream, time.Now().Add(defaultStateSyncTimeout)))
	frames := ParseMessages(stream.output.Bytes())
	require.Len(t, frames, 4)
	var batch common.QuorumState
	require.NoError(t, batch.Unmarshal(frames[2][1:]))
	require.Len(t, batch.QuoNetworkFees, 10000, "the byte packer must not truncate to the old 100/category limit")
	fee := &common.NetworkFee{Height: 10001}
	s.networkFees[*fee] = &AttestationState[*common.NetworkFee]{Item: fee, attestations: []attestationSentState{{attestation: &common.Attestation{}}}}
	stream = newStateBoundsStream("active")
	require.ErrorContains(t, s.sendAttestationState(stream, time.Now().Add(defaultStateSyncTimeout)), "transfer maximum")
	require.Zero(t, stream.output.Len(), "oversized local export must fail before BatchBegin")
	delete(s.networkFees, *fee)
	oldBytes := maxAttestationBatchBytes
	maxAttestationBatchBytes = 1
	defer func() { maxAttestationBatchBytes = oldBytes }()
	stream = newStateBoundsStream("active")
	require.ErrorContains(t, s.sendAttestationState(stream, time.Now().Add(defaultStateSyncTimeout)), "batches")
	require.Zero(t, stream.output.Len())
	maxAttestationBatchBytes = oldBytes
	s.networkFees = map[common.NetworkFee]*AttestationState[*common.NetworkFee]{*fee: {Item: fee, attestations: make([]attestationSentState, 129)}}
	for i := range s.networkFees[*fee].attestations {
		s.networkFees[*fee].attestations[i].attestation = &common.Attestation{}
	}
	stream = newStateBoundsStream("active", []byte(streamAckBegin), []byte(streamAckHeader), []byte(streamAckData), []byte(p2p.StreamMsgDone))
	require.NoError(t, s.sendAttestationState(stream, time.Now().Add(defaultStateSyncTimeout)))
	frames = ParseMessages(stream.output.Bytes())
	batch = common.QuorumState{}
	require.NoError(t, batch.Unmarshal(frames[2][1:]))
	require.Len(t, batch.QuoNetworkFees, 2)
	require.Equal(t, 128, len(batch.QuoNetworkFees[0].Attestations))
	require.Equal(t, 1, len(batch.QuoNetworkFees[1].Attestations))
	// Committed entries were never exported and must not consume wire items.
	s.networkFees[*fee].attestations[0].committed = true
	chunks, err := copyStateSyncAttestations(s.networkFees[*fee], 1)
	require.NoError(t, err)
	require.Len(t, chunks, 1)
	require.Len(t, chunks[0], 128)
}

func stateSyncSignedFee(t *testing.T, height int64, count int) (*common.NetworkFee, []*common.Attestation) {
	t.Helper()
	fee := &common.NetworkFee{Chain: common.BTCChain, Height: height, TransactionSize: 100, TransactionRate: 2}
	payload, err := fee.GetSignablePayload()
	require.NoError(t, err)
	atts := make([]*common.Attestation, count)
	for i := range atts {
		key := secp256k1.GenPrivKey()
		sig, err := key.Sign(payload)
		require.NoError(t, err)
		atts[i] = &common.Attestation{PubKey: key.PubKey().Bytes(), Signature: sig}
	}
	return fee, atts
}

// Include BatchBegin so imports exercise the requester's reset/close decision.
func stateSyncImportStream(t *testing.T, remote peer.ID, payloads ...[]byte) *stateBoundsStream {
	t.Helper()
	frames := [][]byte{append([]byte{prefixBatchBegin[0]}, stateBoundsCount(uint32(len(payloads)))...)}
	for i, data := range payloads {
		frames = append(frames, stateBoundsHeader(uint32(i), uint32(len(data))), append([]byte{prefixBatchData[0]}, data...))
	}
	return newStateBoundsStream(remote, append(frames, prefixBatchEnd)...)
}

type stateSyncRecordingStream struct {
	*stateBoundsPipeStream
	resets atomic.Int32
	closes atomic.Int32
}

func (s *stateSyncRecordingStream) Reset() error {
	s.resets.Add(1)
	return s.stateBoundsPipeStream.Reset()
}

func (s *stateSyncRecordingStream) Close() error {
	s.closes.Add(1)
	return s.stateBoundsPipeStream.Close()
}

func TestStateSyncTransferOutlivesPeerTimeout(t *testing.T) {
	sender, _, _, _, _, _ := setupTestGossip(t)
	receiver, _, _, _, _, _ := setupTestGossip(t)
	sender.logger, receiver.logger = zerolog.Nop(), zerolog.Nop()
	sender.activeVals["receiver"] = true
	sender.config.PeerTimeout = 20 * time.Millisecond
	receiver.config.PeerTimeout = 20 * time.Millisecond
	fee, atts := stateSyncSignedFee(t, 123, 1)
	stateSyncTrustAttestors(t, receiver, atts)
	sender.networkFees[*fee] = &AttestationState[*common.NetworkFee]{
		Item: fee, attestations: []attestationSentState{{attestation: atts[0]}},
	}
	original := verifySignature
	verifySignature = func(payload, signature, pubkey []byte) error {
		time.Sleep(3 * sender.config.PeerTimeout)
		return original(payload, signature, pubkey)
	}
	defer func() { verifySignature = original }()
	a, b := stateBoundsPipe(t, "receiver", "sender")
	server := &stateSyncRecordingStream{stateBoundsPipeStream: a}
	client := &stateSyncRecordingStream{stateBoundsPipeStream: b}
	done := make(chan struct{})
	go func() { sender.handleStreamAttestationState(server); close(done) }()
	receiver.askStreamForState(client)
	waitStateBounds(t, done)
	require.Zero(t, server.resets.Load(), "completed snapshot must outlive the single-message budget")
	require.Zero(t, client.resets.Load(), "receiver must finish processing and the final handshake")
	require.EqualValues(t, 1, server.closes.Load())
	require.EqualValues(t, 1, client.closes.Load())
	require.Equal(t, 1, receiver.networkFees[*fee].AttestationCount())
}

func TestStateSyncAccumulatedBucketRoundTrip(t *testing.T) {
	relay, _, _, _, _, _ := setupTestGossip(t)
	relay.logger = zerolog.Nop()
	fee, atts := stateSyncSignedFee(t, 123, 129)
	unrelated, unrelatedAtts := stateSyncSignedFee(t, 124, 1)
	stateSyncTrustAttestors(t, relay, append(atts, unrelatedAtts...))
	input := common.QuorumState{QuoNetworkFees: []*common.QuorumNetworkFee{
		{NetworkFee: fee, Attestations: atts[:128]},
		{NetworkFee: fee, Attestations: atts[128:]},
		{NetworkFee: unrelated, Attestations: unrelatedAtts},
	}}
	data, err := input.Marshal()
	require.NoError(t, err)
	incoming := newStateBoundsStream("sender", stateBoundsHeader(0, uint32(len(data))), append([]byte{prefixBatchData[0]}, data...), prefixBatchEnd)
	require.NoError(t, relay.receiveBatchedAttestationState(incoming, stateBoundsCount(1)))
	require.Equal(t, 129, relay.networkFees[*fee].AttestationCount())

	outgoing := newStateBoundsStream("receiver", []byte(streamAckBegin), []byte(streamAckHeader), []byte(streamAckData), []byte(p2p.StreamMsgDone))
	require.NoError(t, relay.sendAttestationState(outgoing, time.Now().Add(defaultStateSyncTimeout)))
	frames := ParseMessages(outgoing.output.Bytes())
	require.Len(t, frames, 4)
	var exported common.QuorumState
	require.NoError(t, exported.Unmarshal(frames[2][1:]))
	require.Len(t, exported.QuoNetworkFees, 3)
	for _, item := range exported.QuoNetworkFees {
		require.LessOrEqual(t, len(item.Attestations), 128)
	}

	restored, _, _, _, _, _ := setupTestGossip(t)
	restored.logger = zerolog.Nop()
	stateSyncTrustAttestors(t, restored, append(atts, unrelatedAtts...))
	transfer := newStateBoundsStream("relay", frames[1:]...)
	require.NoError(t, restored.receiveBatchedAttestationState(transfer, frames[0][1:]))
	for _, want := range []*common.NetworkFee{fee, unrelated} {
		got := restored.networkFees[*want]
		require.NotNil(t, got)
		require.ElementsMatch(t, relay.networkFees[*want].AttestationsCopy(), got.AttestationsCopy())
	}
	require.True(t, bytes.Equal(frames[3], prefixBatchEnd))
}

func TestStateSyncChunkBoundaries(t *testing.T) {
	for _, count := range []int{0, 1, 128, 129, 256, 257} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			state := &AttestationState[*common.NetworkFee]{Item: &common.NetworkFee{}}
			for i := range count {
				state.attestations = append(state.attestations, attestationSentState{
					attestation: &common.Attestation{PubKey: []byte{byte(i)}, Signature: []byte{byte(i >> 8), byte(i)}},
				})
			}
			// Committed entries must not increase exported slice capacity.
			for range 1000 {
				state.attestations = append(state.attestations, attestationSentState{committed: true})
			}
			wantChunks := (count + 127) / 128
			chunks, err := copyStateSyncAttestations(state, wantChunks)
			require.NoError(t, err)
			require.Len(t, chunks, wantChunks)
			var copied []*common.Attestation
			for _, chunk := range chunks {
				require.LessOrEqual(t, len(chunk), 128)
				require.LessOrEqual(t, cap(chunk), 128)
				copied = append(copied, chunk...)
			}
			require.Len(t, copied, count)
			for i, att := range copied {
				require.Equal(t, state.attestations[i].attestation, att)
			}
			if count > 0 {
				copied[0].PubKey[0] = 255
				require.EqualValues(t, 0, state.attestations[0].attestation.PubKey[0])
			}
			if wantChunks > 1 {
				rejected, err := copyStateSyncAttestations(state, wantChunks-1)
				require.ErrorContains(t, err, "transfer maximum")
				require.Nil(t, rejected)
			}
		})
	}
}

func TestStateSyncChunkAllCategoriesAndBytePacking(t *testing.T) {
	for _, splitFrames := range []bool{false, true} {
		t.Run(fmt.Sprint(splitFrames), func(t *testing.T) {
			s, _, _, _, _, _ := setupTestGossip(t)
			s.logger = zerolog.Nop()
			entries := make([]attestationSentState, 129)
			for i := range entries {
				entries[i].attestation = &common.Attestation{PubKey: []byte{byte(i)}, Signature: []byte{byte(i)}}
			}
			tx := &common.ObservedTx{Tx: common.Tx{Chain: common.BTCChain, ID: "chunked-tx"}}
			key := txKey{Chain: common.BTCChain, ID: tx.Tx.ID, Inbound: true, AllowFutureObservation: true}
			s.observedTxs[key] = &AttestationState[*attestableObservedTx]{Item: &attestableObservedTx{ObservedTx: tx, inbound: true}, attestations: entries}
			fee := &common.NetworkFee{Chain: common.BTCChain, Height: 123}
			s.networkFees[*fee] = &AttestationState[*common.NetworkFee]{Item: fee, attestations: entries}
			solvency := &common.Solvency{Chain: common.BTCChain, Id: "chunked-solvency"}
			s.solvencies[solvency.Id] = &AttestationState[*common.Solvency]{Item: solvency, attestations: entries}
			errata := &common.ErrataTx{Chain: common.BTCChain, Id: "chunked-errata"}
			s.errataTxs[*errata] = &AttestationState[*common.ErrataTx]{Item: errata, attestations: entries}
			price := &common.PriceFeed{Time: 123}
			s.priceFeeds = make(map[string]*AttestationState[*common.PriceFeed])
			s.priceFeeds["price"] = &AttestationState[*common.PriceFeed]{Item: price, attestations: entries}

			batchCount := 1
			if splitFrames {
				old := maxAttestationBatchBytes
				maxAttestationBatchBytes = 1
				defer func() { maxAttestationBatchBytes = old }()
				batchCount = 10
			}
			acks := [][]byte{[]byte(streamAckBegin)}
			for range batchCount {
				acks = append(acks, []byte(streamAckHeader), []byte(streamAckData))
			}
			acks = append(acks, []byte(p2p.StreamMsgDone))
			stream := newStateBoundsStream("receiver", acks...)
			require.NoError(t, s.sendAttestationState(stream, time.Now().Add(defaultStateSyncTimeout)))
			frames := ParseMessages(stream.output.Bytes())
			require.Len(t, frames, 2+2*batchCount)
			var all common.QuorumState
			for i := 0; i < batchCount; i++ {
				var batch common.QuorumState
				require.NoError(t, batch.Unmarshal(frames[2+2*i][1:]))
				require.NoError(t, validateAttestationStateBatch(batch))
				all.QuoTxs = append(all.QuoTxs, batch.QuoTxs...)
				all.QuoNetworkFees = append(all.QuoNetworkFees, batch.QuoNetworkFees...)
				all.QuoSolvencies = append(all.QuoSolvencies, batch.QuoSolvencies...)
				all.QuoErrataTxs = append(all.QuoErrataTxs, batch.QuoErrataTxs...)
				all.QuoPriceFeeds = append(all.QuoPriceFeeds, batch.QuoPriceFeeds...)
			}
			require.Len(t, all.QuoTxs, 2)
			require.Len(t, all.QuoNetworkFees, 2)
			require.Len(t, all.QuoSolvencies, 2)
			require.Len(t, all.QuoErrataTxs, 2)
			require.Len(t, all.QuoPriceFeeds, 2)
			groups := [][]*common.Attestation{
				append(all.QuoTxs[0].Attestations, all.QuoTxs[1].Attestations...),
				append(all.QuoNetworkFees[0].Attestations, all.QuoNetworkFees[1].Attestations...),
				append(all.QuoSolvencies[0].Attestations, all.QuoSolvencies[1].Attestations...),
				append(all.QuoErrataTxs[0].Attestations, all.QuoErrataTxs[1].Attestations...),
				append(all.QuoPriceFeeds[0].Attestations, all.QuoPriceFeeds[1].Attestations...),
			}
			for _, group := range groups {
				require.Len(t, group, 129)
				for i := range group {
					require.Equal(t, entries[i].attestation, group[i])
				}
			}
			for i := range 2 {
				require.True(t, all.QuoTxs[i].Inbound)
				require.True(t, all.QuoTxs[i].AllowFutureObservation)
				require.Equal(t, tx.Tx.ID, all.QuoTxs[i].ObsTx.Tx.ID)
				require.Equal(t, fee, all.QuoNetworkFees[i].NetworkFee)
				require.Equal(t, solvency.Id, all.QuoSolvencies[i].Solvency.Id)
				require.Equal(t, errata.Id, all.QuoErrataTxs[i].ErrataTx.Id)
				require.Equal(t, price.Time, all.QuoPriceFeeds[i].PriceFeed.Time)
			}
		})
	}
}

func TestStateSyncSplitTransferCeilingAndWarnings(t *testing.T) {
	s, _, _, _, _, _ := setupTestGossip(t)
	var logs bytes.Buffer
	s.logger = zerolog.New(zerolog.SyncWriter(&logs))
	for i := range maxAttestationStateItemsPerTransfer {
		fee := &common.NetworkFee{Chain: common.BTCChain, Height: int64(i)}
		s.networkFees[*fee] = &AttestationState[*common.NetworkFee]{Item: fee, attestations: []attestationSentState{{attestation: &common.Attestation{}}}}
	}
	fee := common.NetworkFee{Chain: common.BTCChain, Height: 0}
	for range 128 {
		s.networkFees[fee].attestations = append(s.networkFees[fee].attestations, attestationSentState{attestation: &common.Attestation{}})
	}
	for range 2 {
		stream := newStateBoundsStream("receiver")
		require.ErrorContains(t, s.sendAttestationState(stream, time.Now().Add(defaultStateSyncTimeout)), "transfer maximum")
		require.Empty(t, stream.output.Bytes())
	}
	require.Equal(t, 1, bytes.Count(logs.Bytes(), []byte(`"level":"warn"`)))
	require.Contains(t, logs.String(), "network fee BTC/")
	s.stateRequestMu.Lock()
	s.stateExportWarnAt = time.Now().Add(-2 * time.Minute)
	s.stateRequestMu.Unlock()
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := s.stateExportError(fmt.Errorf("still too large")); err == nil {
				t.Error("expected export error")
			}
		}()
	}
	wg.Wait()
	require.Equal(t, 2, bytes.Count(logs.Bytes(), []byte(`"level":"warn"`)))
}

// Explicit opt-in measurement; not a timing threshold in the ordinary suite.
// Exercises real signatures, byte-packed frames, inline imports and final ACK.
func TestStateSyncBudgetWorkload(t *testing.T) {
	if os.Getenv("STATE_SYNC_BUDGET_PROBE") != "1" {
		t.Skip("set STATE_SYNC_BUDGET_PROBE=1 to measure the complete 4000-item transfer")
	}
	sender, _, _, _, _, _ := setupTestGossip(t)
	sender.logger = zerolog.Nop()
	sender.activeVals["receiver"] = true
	keys := make([]*secp256k1.PrivKey, 70)
	for i := range keys {
		keys[i] = secp256k1.GenPrivKey()
	}
	for i := range 4000 {
		fee := &common.NetworkFee{Chain: common.BTCChain, Height: int64(i), TransactionSize: 100, TransactionRate: 2}
		payload, err := fee.GetSignablePayload()
		require.NoError(t, err)
		state := &AttestationState[*common.NetworkFee]{Item: fee}
		for _, key := range keys {
			sig, err := key.Sign(payload)
			require.NoError(t, err)
			state.attestations = append(state.attestations, attestationSentState{attestation: &common.Attestation{PubKey: key.PubKey().Bytes(), Signature: sig}})
		}
		sender.networkFees[*fee] = state
	}
	for _, oldBudget := range []bool{true, false} {
		t.Run(fmt.Sprint(oldBudget), func(t *testing.T) {
			receiver, _, _, _, _, _ := setupTestGossip(t)
			receiver.logger = zerolog.Nop()
			// Keep the benchmark on signature/import work, with 70 eligible slots
			// and the same no-op quorum RPC mock as the normal fixtures.
			receiver.activeVals = make(map[peer.ID]bool)
			for _, key := range keys {
				id, err := conversion.GetPeerIDFromSecp256PubKey(key.PubKey().Bytes())
				require.NoError(t, err)
				receiver.activeVals[id] = true
			}
			sender.config.StateSyncTimeout = 0
			receiver.config.StateSyncTimeout = 0
			if oldBudget {
				sender.config.StateSyncTimeout = defaultPeerTimeout
				receiver.config.StateSyncTimeout = defaultPeerTimeout
			}
			a, b := stateBoundsPipe(t, "receiver", "sender")
			require.NoError(t, a.pipe.SetDeadline(time.Now().Add(3*time.Minute)))
			require.NoError(t, b.pipe.SetDeadline(time.Now().Add(3*time.Minute)))
			server := &stateSyncRecordingStream{stateBoundsPipeStream: a}
			client := &stateSyncRecordingStream{stateBoundsPipeStream: b}
			done := make(chan struct{})
			start := time.Now()
			go func() { sender.handleStreamAttestationState(server); close(done) }()
			receiver.askStreamForState(client)
			waitStateBounds(t, done)
			elapsed := time.Since(start)
			retained := 0
			for _, state := range receiver.networkFees {
				retained += state.AttestationCount()
			}
			t.Logf("old_20s_budget=%t items=%d/4000 signatures=%d/280000 elapsed=%s effective_time_per_signature=%s resets=%d/%d",
				oldBudget, len(receiver.networkFees), retained, elapsed, elapsed/time.Duration(max(1, retained)), server.resets.Load(), client.resets.Load())
			if !oldBudget {
				require.Zero(t, server.resets.Load())
				require.Zero(t, client.resets.Load())
				require.Equal(t, 4000, len(receiver.networkFees))
				require.Equal(t, 280000, retained)
			}
		})
	}
}

func stateSyncTrustAttestors(t *testing.T, s *AttestationGossip, atts []*common.Attestation) {
	t.Helper()
	for _, att := range atts {
		id, err := conversion.GetPeerIDFromSecp256PubKey(att.PubKey)
		require.NoError(t, err)
		s.activeVals[id] = true
	}
}

func TestSendAttestationStateUnevenCategoriesRoundTrip(t *testing.T) {
	for _, tc := range []struct {
		name   string
		counts [5]int // transactions, network fees, solvencies, errata, price feeds
		budget int
	}{
		{"no transactions, single batch", [5]int{0, 1, 1, 1, 1}, maxAttestationBatchBytes},
		{"no transactions, uneven batches", [5]int{0, 3, 4, 5, 6}, 256},
		{"all five categories, uneven batches", [5]int{3, 9, 5, 7, 4}, 256},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// These tests stay serial because the byte budget is package-wide.
			originalBudget := maxAttestationBatchBytes
			maxAttestationBatchBytes = tc.budget
			t.Cleanup(func() { maxAttestationBatchBytes = originalBudget })

			s := newStateBoundsGossip()
			s.observedTxs = make(map[txKey]*AttestationState[*attestableObservedTx])
			s.networkFees = make(map[common.NetworkFee]*AttestationState[*common.NetworkFee])
			s.solvencies = make(map[common.TxID]*AttestationState[*common.Solvency])
			s.errataTxs = make(map[common.ErrataTx]*AttestationState[*common.ErrataTx])
			s.priceFeeds = make(map[string]*AttestationState[*common.PriceFeed])

			// Representative attestation bytes exercise export/serialization only;
			// signature validation and receiver admission have separate tests.
			atts := []*common.Attestation{{PubKey: bytes.Repeat([]byte{1}, 33), Signature: bytes.Repeat([]byte{2}, 64)}}
			storedAtts := []attestationSentState{{attestation: atts[0]}}
			var expected common.QuorumState
			for i := range tc.counts[0] {
				tx := &common.ObservedTx{Tx: common.Tx{ID: common.TxID(fmt.Sprintf("tx-%d", i)), Chain: common.BTCChain}}
				s.observedTxs[txKey{Chain: tx.Tx.Chain, ID: tx.Tx.ID, Inbound: true}] = &AttestationState[*attestableObservedTx]{
					Item: &attestableObservedTx{ObservedTx: tx}, attestations: storedAtts,
				}
				expected.QuoTxs = append(expected.QuoTxs, &common.QuorumTx{ObsTx: *tx, Inbound: true, Attestations: atts})
			}
			for i := range tc.counts[1] {
				fee := &common.NetworkFee{Chain: common.BTCChain, Height: int64(i + 1), TransactionSize: 100, TransactionRate: 2}
				s.networkFees[*fee] = &AttestationState[*common.NetworkFee]{Item: fee, attestations: storedAtts}
				expected.QuoNetworkFees = append(expected.QuoNetworkFees, &common.QuorumNetworkFee{NetworkFee: fee, Attestations: atts})
			}
			for i := range tc.counts[2] {
				solvency := &common.Solvency{Id: common.TxID(fmt.Sprintf("solvency-%d", i)), Chain: common.BTCChain, Height: int64(i + 1)}
				s.solvencies[solvency.Id] = &AttestationState[*common.Solvency]{Item: solvency, attestations: storedAtts}
				expected.QuoSolvencies = append(expected.QuoSolvencies, &common.QuorumSolvency{Solvency: solvency, Attestations: atts})
			}
			for i := range tc.counts[3] {
				errata := &common.ErrataTx{Id: common.TxID(fmt.Sprintf("errata-%d", i)), Chain: common.BTCChain}
				s.errataTxs[*errata] = &AttestationState[*common.ErrataTx]{Item: errata, attestations: storedAtts}
				expected.QuoErrataTxs = append(expected.QuoErrataTxs, &common.QuorumErrataTx{ErrataTx: errata, Attestations: atts})
			}
			for i := range tc.counts[4] {
				priceFeed := &common.PriceFeed{Time: int64(i + 1)}
				s.priceFeeds[fmt.Sprintf("price-%d", i)] = &AttestationState[*common.PriceFeed]{Item: priceFeed, attestations: storedAtts}
				expected.QuoPriceFeeds = append(expected.QuoPriceFeeds, &common.QuorumPriceFeed{PriceFeed: priceFeed, Attestations: atts})
			}

			stream := NewAutoResponderStream("active")
			require.NoError(t, s.sendAttestationState(stream, time.Now().Add(defaultStateSyncTimeout)))
			frames := ParseMessages(stream.GetWrittenData())
			require.NotEmpty(t, frames)
			require.Len(t, frames[0], 5)
			require.Equal(t, prefixBatchBegin, frames[0][:1])
			batchCount := int(binary.LittleEndian.Uint32(frames[0][1:]))
			require.Positive(t, batchCount)
			require.Len(t, frames, 2*batchCount+2, "one header/data pair per announced batch, then end")
			require.Equal(t, prefixBatchEnd, frames[len(frames)-1])

			var reconstructed, last common.QuorumState
			var categoryBatches [5]int
			for i := range batchCount {
				header, data := frames[1+2*i], frames[2+2*i]
				require.Len(t, header, 9)
				require.Equal(t, prefixBatchHeader, header[:1])
				require.EqualValues(t, i, binary.LittleEndian.Uint32(header[1:5]))
				require.Greater(t, len(data), 1, "no empty data frame")
				require.Equal(t, prefixBatchData, data[:1])
				require.EqualValues(t, len(data)-1, binary.LittleEndian.Uint32(header[5:9]))
				require.LessOrEqual(t, len(data)-1, tc.budget)
				require.LessOrEqual(t, len(data), p2p.MaxPayload)

				var decoded common.QuorumState
				require.NoError(t, decoded.Unmarshal(data[1:]))
				reconstructed.QuoTxs = append(reconstructed.QuoTxs, decoded.QuoTxs...)
				reconstructed.QuoNetworkFees = append(reconstructed.QuoNetworkFees, decoded.QuoNetworkFees...)
				reconstructed.QuoSolvencies = append(reconstructed.QuoSolvencies, decoded.QuoSolvencies...)
				reconstructed.QuoErrataTxs = append(reconstructed.QuoErrataTxs, decoded.QuoErrataTxs...)
				reconstructed.QuoPriceFeeds = append(reconstructed.QuoPriceFeeds, decoded.QuoPriceFeeds...)
				for category, count := range [5]int{len(decoded.QuoTxs), len(decoded.QuoNetworkFees), len(decoded.QuoSolvencies), len(decoded.QuoErrataTxs), len(decoded.QuoPriceFeeds)} {
					if count > 0 {
						categoryBatches[category]++
					}
				}
				last = decoded
			}

			// ElementsMatch checks multiplicity as well as complete values, without
			// depending on map iteration order or hiding duplicates in a map.
			require.ElementsMatch(t, expected.QuoTxs, reconstructed.QuoTxs)
			require.ElementsMatch(t, expected.QuoNetworkFees, reconstructed.QuoNetworkFees)
			require.ElementsMatch(t, expected.QuoSolvencies, reconstructed.QuoSolvencies)
			require.ElementsMatch(t, expected.QuoErrataTxs, reconstructed.QuoErrataTxs)
			require.ElementsMatch(t, expected.QuoPriceFeeds, reconstructed.QuoPriceFeeds)
			if tc.budget == 256 {
				for category, count := range tc.counts {
					if count > 0 {
						require.Greater(t, categoryBatches[category], 1, "category %d must span multiple batches", category)
					}
				}
				require.Empty(t, last.QuoTxs)
				require.Empty(t, last.QuoNetworkFees)
				require.Empty(t, last.QuoSolvencies)
				require.Empty(t, last.QuoErrataTxs)
				require.NotEmpty(t, last.QuoPriceFeeds, "retain the final price-feed-only batch")
			} else {
				require.Equal(t, 1, batchCount)
			}
		})
	}
}
