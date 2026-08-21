package observer

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	"github.com/libp2p/go-libp2p-core/network"
	"github.com/libp2p/go-libp2p-core/peer"
	"github.com/libp2p/go-libp2p-core/protocol"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"

	"gitlab.com/thorchain/thornode/v3/bifrost/p2p"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/ebifrost"
	stypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// TestSendAttestationStateEmpty tests sending an empty attestation state
func TestSendAttestationStateEmpty(t *testing.T) {
	// Disable deadline application during tests
	originalApplyDeadline := p2p.ApplyDeadline.Load()
	p2p.ApplyDeadline.Store(false)
	defer func() { p2p.ApplyDeadline.Store(originalApplyDeadline) }()

	// Create a test instance with empty state
	ag, _, _, _, _, _ := setupTestGossip(t)

	// Create the auto-responder stream
	stream := NewAutoResponderStream("test-peer")

	// Call the method with a timeout
	done := make(chan struct{})
	go func() {
		ag.sendAttestationState(context.Background(), stream)
		close(done)
	}()

	// Wait for completion or timeout
	select {
	case <-done:
		// Method completed
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
	done := make(chan struct{})
	go func() {
		ag.sendAttestationState(context.Background(), stream)
		close(done)
	}()

	// Wait for completion or timeout
	select {
	case <-done:
		// Method completed
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

	// Set a smaller batch size for testing
	origMaxQuorumTxsPerBatch := maxQuorumTxsPerBatch
	maxQuorumTxsPerBatch = 2                                           // Small value for testing
	defer func() { maxQuorumTxsPerBatch = origMaxQuorumTxsPerBatch }() // Restore original value

	// Create a large set of observed transactions (more than maxQuorumTxsPerBatch)
	numTxs := 5 // Will be split into 3 batches with maxQuorumTxsPerBatch=2
	expectedBatches := (numTxs + maxQuorumTxsPerBatch - 1) / maxQuorumTxsPerBatch

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
	done := make(chan struct{})
	go func() {
		ag.sendAttestationState(context.Background(), stream)
		close(done)
	}()

	// Wait for completion or timeout
	select {
	case <-done:
		// Method completed
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
	assert.Equal(t, expectedBatches, headerCount, "Should have expected number of batch header messages")
	assert.Equal(t, expectedBatches, dataCount, "Should have expected number of batch data messages")
	assert.Equal(t, 1, endCount, "Should have one batch end message")

	// Check the batch begin message
	for i, msg := range messages {
		if bytes.HasPrefix(msg, prefixBatchBegin) {
			// Extract total batches
			totalBatches := binary.LittleEndian.Uint32(msg[1:5])
			assert.Equal(t, uint32(expectedBatches), totalBatches, "Should have expected number of batches")
		}

		// For data messages, check the batch number and contents
		if bytes.HasPrefix(msg, prefixBatchHeader) && len(msg) >= 9 {
			// Extract batch number
			batchNum := binary.LittleEndian.Uint32(msg[1:5])
			assert.Less(t, batchNum, uint32(expectedBatches), "Batch number should be valid")

			// Get the batch data (should be the next message)
			if len(messages) > i+1 {
				batchData := messages[i+1]

				assert.True(t, bytes.HasPrefix(batchData, prefixBatchData))

				// Unmarshal the batch
				var batch common.QuorumState
				err := batch.Unmarshal(batchData[1:])
				if err != nil {
					t.Logf("Error unmarshaling batch %d (likely not a batch): %v", batchNum, err)
					continue
				}

				// Verify the batch size
				expectedSize := maxQuorumTxsPerBatch
				if batchNum == uint32(expectedBatches-1) && numTxs%maxQuorumTxsPerBatch != 0 {
					expectedSize = numTxs % maxQuorumTxsPerBatch
				}

				assert.Equal(t, expectedSize, len(batch.QuoTxs),
					"Batch %d should have %d transactions", batchNum, expectedSize)
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

	// Set a smaller batch size for testing
	origMaxQuorumTxsPerBatch := maxQuorumTxsPerBatch
	maxQuorumTxsPerBatch = 2                                           // Small value for testing
	defer func() { maxQuorumTxsPerBatch = origMaxQuorumTxsPerBatch }() // Restore original value

	numVals := 4

	// Create test data: 5 transactions split into 3 batches
	numTxs := 5
	expectedBatches := (numTxs + maxQuorumTxsPerBatch - 1) / maxQuorumTxsPerBatch

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
	clientReader := bytes.NewBuffer(nil)
	clientWriter := &bytes.Buffer{}

	serverReader := clientWriter
	serverWriter := clientReader

	var sharedMu sync.Mutex

	peerStream := &MockStream{
		reader: serverReader,
		writer: serverWriter,
		peer:   "test-peer",
		mu:     &sharedMu,
	}

	ourStream := &MockStream{
		reader: clientReader,
		writer: clientWriter,
		peer:   ag.host.ID(),
		mu:     &sharedMu,
	}

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
			startIdx := batchIdx * maxQuorumTxsPerBatch
			endIdx := startIdx + maxQuorumTxsPerBatch

			if endIdx > numTxs {
				endIdx = numTxs
			}

			batchTxs := txs[startIdx:endIdx]

			var batchSolvencies []*common.QuorumSolvency
			if startIdx < numSolvencies {
				endIdxSolvency := startIdx + maxQuorumTxsPerBatch
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

	err := ag.receiveBatchedAttestationState(context.Background(), peerStream, beginHeader)
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

	// Set a smaller batch size for testing
	origMaxQuorumTxsPerBatch := maxQuorumTxsPerBatch
	maxQuorumTxsPerBatch = 2                                           // Small value for testing
	defer func() { maxQuorumTxsPerBatch = origMaxQuorumTxsPerBatch }() // Restore original value

	// Create a large set of observed transactions (more than maxQuorumTxsPerBatch)
	numTxs := 5        // Will be split into 3 batches with maxQuorumTxsPerBatch=2
	numSolvencies := 3 // Will be split into 2 batches with maxQuorumTxsPerBatch=2
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
		agReceiver.askStreamForState(context.Background(), receiverStream)
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
	select {
	case data := <-s.readData:
		return copy(p, data), nil
	case <-time.After(100 * time.Millisecond):
		return 0, io.EOF
	}
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
// The existing TestSendAttestationStateEmpty uses AutoResponderStream which
// always responds "ack_begin" to prefixBatchBegin regardless of totalBatches.
// That hides the bug because the buggy code falls through and (eventually)
// fails on a different check.
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

	done := make(chan struct{})
	go func() {
		ag.sendAttestationState(context.Background(), stream)
		close(done)
	}()

	select {
	case <-done:
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
	select {
	case data := <-s.readData:
		return copy(p, data), nil
	case <-time.After(100 * time.Millisecond):
		return 0, io.EOF
	}
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

type slowAttestationStateStream struct {
	MockStream
	responseDelay time.Duration
	pendingWrites bytes.Buffer
	responses     chan []byte
	reset         chan struct{}
	resetOnce     sync.Once
	stateMu       sync.Mutex
	reads         int
}

func newSlowAttestationStateStream(responseDelay time.Duration) *slowAttestationStateStream {
	return &slowAttestationStateStream{
		MockStream: MockStream{
			peer: peer.ID("slow-state-peer"),
			mu:   new(sync.Mutex),
		},
		responseDelay: responseDelay,
		responses:     make(chan []byte, 8),
		reset:         make(chan struct{}),
	}
}

func (s *slowAttestationStateStream) Write(p []byte) (int, error) {
	_, _ = s.pendingWrites.Write(p)
	for s.pendingWrites.Len() >= p2p.LengthHeader {
		raw := s.pendingWrites.Bytes()
		length := int(binary.LittleEndian.Uint32(raw[:p2p.LengthHeader]))
		if len(raw) < p2p.LengthHeader+length {
			break
		}
		frame := append([]byte(nil), raw[p2p.LengthHeader:p2p.LengthHeader+length]...)
		s.pendingWrites.Next(p2p.LengthHeader + length)

		var response []byte
		switch {
		case bytes.HasPrefix(frame, prefixBatchBegin):
			response = []byte(streamAckBegin)
		case bytes.HasPrefix(frame, prefixBatchHeader):
			response = []byte(streamAckHeader)
		case bytes.HasPrefix(frame, prefixBatchData):
			response = []byte(streamAckData)
		case bytes.HasPrefix(frame, prefixBatchEnd):
			response = []byte(p2p.StreamMsgDone)
		default:
			return len(p), fmt.Errorf("unexpected state frame: %x", frame)
		}
		framed := make([]byte, p2p.LengthHeader+len(response))
		binary.LittleEndian.PutUint32(framed, uint32(len(response)))
		copy(framed[p2p.LengthHeader:], response)
		s.responses <- framed
	}
	return len(p), nil
}

func (s *slowAttestationStateStream) Read(p []byte) (int, error) {
	var response []byte
	select {
	case <-s.reset:
		return 0, io.ErrClosedPipe
	case response = <-s.responses:
	}
	timer := time.NewTimer(s.responseDelay)
	defer timer.Stop()
	select {
	case <-s.reset:
		return 0, io.ErrClosedPipe
	case <-timer.C:
	}
	s.stateMu.Lock()
	s.reads++
	s.stateMu.Unlock()
	return copy(p, response), nil
}

func (s *slowAttestationStateStream) Reset() error {
	s.resetOnce.Do(func() { close(s.reset) })
	return nil
}

func (s *slowAttestationStateStream) readCount() int {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	return s.reads
}

func TestSendAttestationStateUsesTotalTransferTimeout(t *testing.T) {
	ag, _, _, _, _, _ := setupTestGossip(t)
	ag.config.PeerTimeout = 35 * time.Millisecond
	observed := &common.ObservedTx{
		Tx: common.Tx{
			ID:    common.TxID("xmr-state-total-timeout"),
			Chain: common.XMRChain,
		},
		BlockHeight:    200,
		FinaliseHeight: 200,
	}
	state := ag.observedTxsPool.NewAttestationState(&attestableObservedTx{
		ObservedTx: observed,
		inbound:    true,
	})
	ag.observedTxs[observedTxKey(*observed, true, false)] = state

	// Each response is individually faster than the configured timeout. Four
	// rolling deadlines would therefore let this transfer run for roughly
	// 60ms; the total watchdog must reset it around 35ms instead.
	stream := newSlowAttestationStateStream(15 * time.Millisecond)
	stateCtx, cancel := ag.attestationStateContext(context.Background(), stream)
	defer cancel()
	done := make(chan struct{})
	started := time.Now()
	go func() {
		ag.sendAttestationState(stateCtx, stream)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("total attestation state timeout did not reset the stream")
	}
	require.Less(t, time.Since(started), 100*time.Millisecond)
	require.Less(t, stream.readCount(), 4, "all rolling acknowledgments unexpectedly completed")

	writerAcquired := make(chan struct{})
	go func() {
		ag.xmrErrataBarrier.Lock()
		close(writerAcquired)
		ag.xmrErrataBarrier.Unlock()
	}()
	select {
	case <-writerAcquired:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("XMR errata writer remained blocked after state transfer timeout")
	}
}

func TestSendAttestationStateReleasesBarrierWhileSnapshotIsBusy(t *testing.T) {
	for _, tc := range []struct {
		name string
		lock func(*AttestationGossip, *AttestationState[*attestableObservedTx]) func()
	}{
		{
			name: "global state lock",
			lock: func(ag *AttestationGossip, _ *AttestationState[*attestableObservedTx]) func() {
				ag.mu.Lock()
				return ag.mu.Unlock
			},
		},
		{
			name: "observation state lock",
			lock: func(_ *AttestationGossip, state *AttestationState[*attestableObservedTx]) func() {
				state.mu.Lock()
				return state.mu.Unlock
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ag, _, _, _, _, _ := setupTestGossip(t)
			ag.config.PeerTimeout = 35 * time.Millisecond
			observed := &common.ObservedTx{
				Tx: common.Tx{
					ID:    common.TxID("xmr-state-busy-snapshot"),
					Chain: common.XMRChain,
				},
				BlockHeight:    200,
				FinaliseHeight: 200,
			}
			state := ag.observedTxsPool.NewAttestationState(&attestableObservedTx{
				ObservedTx: observed,
				inbound:    true,
			})
			ag.observedTxs[observedTxKey(*observed, true, false)] = state

			unlockBusyState := tc.lock(ag, state)
			defer unlockBusyState()

			stream := newSlowAttestationStateStream(0)
			stateCtx, cancel := ag.attestationStateContext(context.Background(), stream)
			defer cancel()
			done := make(chan struct{})
			go func() {
				ag.sendAttestationState(stateCtx, stream)
				close(done)
			}()

			select {
			case <-stream.reset:
			case <-time.After(500 * time.Millisecond):
				t.Fatal("state snapshot timeout did not reset the stream")
			}

			writerAcquired := make(chan struct{})
			go func() {
				ag.xmrErrataBarrier.Lock()
				close(writerAcquired)
				ag.xmrErrataBarrier.Unlock()
			}()
			select {
			case <-writerAcquired:
			case <-time.After(200 * time.Millisecond):
				t.Fatal("busy state snapshot kept the XMR errata reader lock after timeout")
			}
			select {
			case <-done:
			case <-time.After(200 * time.Millisecond):
				t.Fatal("state snapshot did not stop after its total timeout")
			}
		})
	}
}

type scriptedAttestationStateStream struct {
	MockStream
	frames          chan []byte
	readMu          sync.Mutex
	current         []byte
	reset           chan struct{}
	resetOnce       sync.Once
	resetCalls      atomic.Int32
	secondReset     chan struct{}
	secondResetOnce sync.Once
	closed          chan struct{}
	closeOnce       sync.Once
}

func newScriptedAttestationStateStream(messages ...[]byte) *scriptedAttestationStateStream {
	stream := &scriptedAttestationStateStream{
		MockStream: MockStream{
			peer: peer.ID("scripted-state-peer"),
			mu:   new(sync.Mutex),
		},
		frames:      make(chan []byte, len(messages)),
		reset:       make(chan struct{}),
		secondReset: make(chan struct{}),
		closed:      make(chan struct{}),
	}
	for _, message := range messages {
		framed := make([]byte, p2p.LengthHeader+len(message))
		binary.LittleEndian.PutUint32(framed, uint32(len(message)))
		copy(framed[p2p.LengthHeader:], message)
		stream.frames <- framed
	}
	return stream
}

func (s *scriptedAttestationStateStream) Read(p []byte) (int, error) {
	s.readMu.Lock()
	defer s.readMu.Unlock()
	for len(s.current) == 0 {
		s.readMu.Unlock()
		select {
		case s.current = <-s.frames:
		case <-s.reset:
			s.readMu.Lock()
			return 0, io.ErrClosedPipe
		}
		s.readMu.Lock()
	}
	n := copy(p, s.current)
	s.current = s.current[n:]
	return n, nil
}

func (s *scriptedAttestationStateStream) Write(p []byte) (int, error) {
	return len(p), nil
}

func (s *scriptedAttestationStateStream) Reset() error {
	if s.resetCalls.Add(1) >= 2 {
		s.secondResetOnce.Do(func() { close(s.secondReset) })
	}
	s.resetOnce.Do(func() { close(s.reset) })
	return nil
}

func (s *scriptedAttestationStateStream) Close() error {
	s.closeOnce.Do(func() { close(s.closed) })
	return nil
}

func stateBatchHeader(batchNum, dataSize uint32) []byte {
	header := make([]byte, 9)
	copy(header, prefixBatchHeader)
	binary.LittleEndian.PutUint32(header[1:5], batchNum)
	binary.LittleEndian.PutUint32(header[5:9], dataSize)
	return header
}

func stateBatchCount(total uint32) []byte {
	begin := make([]byte, 4)
	binary.LittleEndian.PutUint32(begin, total)
	return begin
}

func TestAskStreamForStateTimesOutStalledTransfer(t *testing.T) {
	ag, _, _, _, _, _ := setupTestGossip(t)
	ag.config.PeerTimeout = 100 * time.Millisecond
	stream := newScriptedAttestationStateStream(
		append(append([]byte(nil), prefixBatchBegin...), stateBatchCount(1)...),
		stateBatchHeader(0, 0),
		append([]byte(nil), prefixBatchData...),
	)
	t.Cleanup(func() { _ = stream.Reset() })

	done := make(chan struct{})
	go func() {
		ag.askStreamForState(context.Background(), stream)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("stalled attestation state request exceeded its total timeout")
	}
	select {
	case <-stream.reset:
	default:
		t.Fatal("timed-out attestation state stream was not reset")
	}
}

func TestAskForAttestationStateStopsWaitingForBlockedProcessing(t *testing.T) {
	ag, host, _, _, bridge, _ := setupTestGossip(t)
	ag.config.AskPeers = 1
	ag.config.PeerTimeout = 100 * time.Millisecond

	remotePeer := host.peers[1]
	ag.avMu.Lock()
	ag.activeVals = map[peer.ID]bool{
		host.ID():  true,
		remotePeer: true,
	}
	ag.avMu.Unlock()
	ag.SetObserverApplySpentOutputRefs(func(common.ObservedTx, func(bool)) {})

	bridgeQueryStarted := make(chan struct{})
	releaseBridgeQuery := make(chan struct{})
	var releaseBridgeQueryOnce sync.Once
	releaseQuery := func() { releaseBridgeQueryOnce.Do(func() { close(releaseBridgeQuery) }) }
	t.Cleanup(releaseQuery)
	bridge.getVaultFunc = func(string) (stypes.Vault, error) {
		close(bridgeQueryStarted)
		<-releaseBridgeQuery
		return stypes.Vault{}, nil
	}

	attesterKey := secp256k1.GenPrivKey()
	vaultPubKeyString, err := cosmos.Bech32ifyPubKey(cosmos.Bech32PubKeyTypeAccPub, attesterKey.PubKey())
	require.NoError(t, err)
	vaultPubKey, err := common.NewPubKey(vaultPubKeyString)
	require.NoError(t, err)
	obsTx := common.ObservedTx{
		Tx: common.Tx{
			ID:    common.TxID("blocked-state-processing"),
			Chain: common.XMRChain,
		},
		ObservedPubKey: vaultPubKey,
		SpentOutputRefs: []common.OutputRef{
			{TxHash: "source", OutputIndex: 0, KeyImage: "key-image", SpendTxHash: "blocked-state-processing"},
		},
	}
	signPayload, err := obsTx.GetSignablePayloadWithInbound(false)
	require.NoError(t, err)
	signature, err := attesterKey.Sign(signPayload)
	require.NoError(t, err)
	attestation := &common.Attestation{
		PubKey:    attesterKey.PubKey().Bytes(),
		Signature: signature,
	}
	batchData, err := (&common.QuorumState{
		QuoTxs: []*common.QuorumTx{
			{
				ObsTx:        obsTx,
				Attestations: []*common.Attestation{attestation},
			},
		},
	}).Marshal()
	require.NoError(t, err)
	stream := newScriptedAttestationStateStream(
		append(append([]byte(nil), prefixBatchBegin...), stateBatchCount(1)...),
		stateBatchHeader(0, uint32(len(batchData))),
		append(append([]byte(nil), prefixBatchData...), batchData...),
		prefixBatchEnd,
	)
	stream.peer = remotePeer
	host.streamFunc = func(context.Context, peer.ID, ...protocol.ID) (network.Stream, error) {
		return stream, nil
	}
	t.Cleanup(func() { _ = stream.Reset() })

	done := make(chan struct{})
	go func() {
		ag.askForAttestationState(context.Background())
		close(done)
	}()

	select {
	case <-bridgeQueryStarted:
	case <-time.After(time.Second):
		t.Fatal("state-sync processing did not reach the blocked Thornode query")
	}
	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("churn-in remained blocked after the state-sync budget expired")
	}

	releaseQuery()
	select {
	case <-stream.secondReset:
	case <-time.After(time.Second):
		t.Fatal("timed-out state-sync worker did not finish after its blocked query was released")
	}
}

func TestReceiveBatchedAttestationStateRejectsInvalidBatchSequence(t *testing.T) {
	tests := []struct {
		name         string
		totalBatches uint32
		messages     [][]byte
		errContains  string
	}{
		{
			name:         "excessive advertised count",
			totalBatches: maxAttestationStateBatches + 1,
			messages:     [][]byte{prefixBatchEnd},
			errContains:  "exceeds maximum",
		},
		{
			name:         "non-sequential first batch",
			totalBatches: 1,
			messages: [][]byte{
				stateBatchHeader(1, 0),
				prefixBatchData,
				prefixBatchEnd,
			},
			errContains: "unexpected batch number",
		},
		{
			name:         "repeated batch number",
			totalBatches: 2,
			messages: [][]byte{
				stateBatchHeader(0, 0),
				prefixBatchData,
				stateBatchHeader(0, 0),
				prefixBatchData,
				prefixBatchEnd,
			},
			errContains: "unexpected batch number",
		},
		{
			name:         "more batches than declared",
			totalBatches: 1,
			messages: [][]byte{
				stateBatchHeader(0, 0),
				prefixBatchData,
				stateBatchHeader(1, 0),
				prefixBatchData,
				prefixBatchEnd,
			},
			errContains: "expected batch end",
		},
		{
			name:         "end before declared count",
			totalBatches: 2,
			messages: [][]byte{
				stateBatchHeader(0, 0),
				prefixBatchData,
				prefixBatchEnd,
			},
			errContains: "invalid batch header",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ag, _, _, _, _, _ := setupTestGossip(t)
			stream := newScriptedAttestationStateStream(tt.messages...)
			t.Cleanup(func() { _ = stream.Reset() })

			err := ag.receiveBatchedAttestationState(
				context.Background(),
				stream,
				stateBatchCount(tt.totalBatches),
			)
			require.ErrorContains(t, err, tt.errContains)
		})
	}
}

func TestValidateAttestationStateTransferCount(t *testing.T) {
	batch := common.QuorumState{
		QuoTxs:         make([]*common.QuorumTx, 100),
		QuoNetworkFees: make([]*common.QuorumNetworkFee, 100),
		QuoSolvencies:  make([]*common.QuorumSolvency, 100),
		QuoErrataTxs:   make([]*common.QuorumErrataTx, 100),
		QuoPriceFeeds:  make([]*common.QuorumPriceFeed, 100),
	}
	require.Equal(t, 500, attestationStateBatchItemCount(batch))

	total, err := validateAttestationStateTransferCount(
		maxAttestationStateItemsPerTransfer-500,
		batch,
	)
	require.NoError(t, err)
	require.Equal(t, maxAttestationStateItemsPerTransfer, total)

	total, err = validateAttestationStateTransferCount(
		maxAttestationStateItemsPerTransfer-499,
		batch,
	)
	require.ErrorContains(t, err, "exceeds transfer maximum")
	require.Equal(
		t,
		maxAttestationStateItemsPerTransfer-499,
		total,
		"a rejected batch must not advance the accepted item count",
	)
}

func TestReceiveBatchedAttestationStateRejectsMissingRequiredPayload(t *testing.T) {
	tests := []struct {
		name        string
		batch       common.QuorumState
		errContains string
		checkDecode func(*testing.T, common.QuorumState)
	}{
		{
			name: "network fee",
			batch: common.QuorumState{QuoNetworkFees: []*common.QuorumNetworkFee{{
				Attestations: []*common.Attestation{{}},
			}}},
			errContains: "networkFee is nil",
			checkDecode: func(t *testing.T, batch common.QuorumState) {
				require.NotNil(t, batch.QuoNetworkFees[0])
				require.NotNil(t, batch.QuoNetworkFees[0].Attestations[0])
				require.Nil(t, batch.QuoNetworkFees[0].NetworkFee)
			},
		},
		{
			name: "solvency",
			batch: common.QuorumState{QuoSolvencies: []*common.QuorumSolvency{{
				Attestations: []*common.Attestation{{}},
			}}},
			errContains: "solvency is nil",
			checkDecode: func(t *testing.T, batch common.QuorumState) {
				require.NotNil(t, batch.QuoSolvencies[0])
				require.NotNil(t, batch.QuoSolvencies[0].Attestations[0])
				require.Nil(t, batch.QuoSolvencies[0].Solvency)
			},
		},
		{
			name: "errata",
			batch: common.QuorumState{QuoErrataTxs: []*common.QuorumErrataTx{{
				Attestations: []*common.Attestation{{}},
			}}},
			errContains: "errataTx is nil",
			checkDecode: func(t *testing.T, batch common.QuorumState) {
				require.NotNil(t, batch.QuoErrataTxs[0])
				require.NotNil(t, batch.QuoErrataTxs[0].Attestations[0])
				require.Nil(t, batch.QuoErrataTxs[0].ErrataTx)
			},
		},
		{
			name: "price feed",
			batch: common.QuorumState{QuoPriceFeeds: []*common.QuorumPriceFeed{{
				Attestations: []*common.Attestation{{}},
			}}},
			errContains: "priceFeed is nil",
			checkDecode: func(t *testing.T, batch common.QuorumState) {
				require.NotNil(t, batch.QuoPriceFeeds[0])
				require.NotNil(t, batch.QuoPriceFeeds[0].Attestations[0])
				require.Nil(t, batch.QuoPriceFeeds[0].PriceFeed)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Exercise generated-protobuf behavior: omitted nested messages remain
			// nil while their containing record and attestations decode non-nil.
			batchData, err := tt.batch.Marshal()
			require.NoError(t, err)
			var decoded common.QuorumState
			require.NoError(t, decoded.Unmarshal(batchData))
			tt.checkDecode(t, decoded)

			ag, _, _, _, _, _ := setupTestGossip(t)
			stream := newScriptedAttestationStateStream(
				stateBatchHeader(0, uint32(len(batchData))),
				append(append([]byte(nil), prefixBatchData...), batchData...),
			)
			t.Cleanup(func() { _ = stream.Reset() })
			err = ag.receiveBatchedAttestationState(context.Background(), stream, stateBatchCount(1))
			require.ErrorContains(t, err, tt.errContains)
			require.Empty(t, ag.networkFees)
			require.Empty(t, ag.solvencies)
			require.Empty(t, ag.errataTxs)
			require.Empty(t, ag.priceFeeds)
		})
	}
}

func TestValidateAttestationStateBatchRejectsNilPointersAndOversize(t *testing.T) {
	tests := []struct {
		name        string
		batch       common.QuorumState
		errContains string
	}{
		{
			name:        "nil quorum tx",
			batch:       common.QuorumState{QuoTxs: []*common.QuorumTx{nil}},
			errContains: "quoTxs[0] is nil",
		},
		{
			name: "nil attestation",
			batch: common.QuorumState{QuoTxs: []*common.QuorumTx{{
				Attestations: []*common.Attestation{nil},
			}}},
			errContains: "attestations[0] is nil",
		},
		{
			name: "nil price rate",
			batch: common.QuorumState{QuoPriceFeeds: []*common.QuorumPriceFeed{{
				PriceFeed: &common.PriceFeed{Rates: []*common.OraclePrice{nil}},
			}}},
			errContains: "rates[0] is nil",
		},
		{
			name:        "too many transactions",
			batch:       common.QuorumState{QuoTxs: make([]*common.QuorumTx, maxAttestationStateItemsPerType+1)},
			errContains: "quoTxs count",
		},
		{
			name:        "too many network fees",
			batch:       common.QuorumState{QuoNetworkFees: make([]*common.QuorumNetworkFee, maxAttestationStateItemsPerType+1)},
			errContains: "quoNetworkFees count",
		},
		{
			name:        "too many solvencies",
			batch:       common.QuorumState{QuoSolvencies: make([]*common.QuorumSolvency, maxAttestationStateItemsPerType+1)},
			errContains: "quoSolvencies count",
		},
		{
			name:        "too many errata",
			batch:       common.QuorumState{QuoErrataTxs: make([]*common.QuorumErrataTx, maxAttestationStateItemsPerType+1)},
			errContains: "quoErrataTxs count",
		},
		{
			name:        "too many price feeds",
			batch:       common.QuorumState{QuoPriceFeeds: make([]*common.QuorumPriceFeed, maxAttestationStateItemsPerType+1)},
			errContains: "quoPriceFeeds count",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateAttestationStateBatch(context.Background(), tt.batch)
			require.ErrorContains(t, err, tt.errContains)
		})
	}
}

func TestValidateAttestationStateBatchBoundsNestedAttestations(t *testing.T) {
	tests := []struct {
		name  string
		batch func([]*common.Attestation) common.QuorumState
	}{
		{
			name: "transactions",
			batch: func(attestations []*common.Attestation) common.QuorumState {
				return common.QuorumState{QuoTxs: []*common.QuorumTx{{Attestations: attestations}}}
			},
		},
		{
			name: "network fees",
			batch: func(attestations []*common.Attestation) common.QuorumState {
				return common.QuorumState{QuoNetworkFees: []*common.QuorumNetworkFee{{
					NetworkFee:   &common.NetworkFee{},
					Attestations: attestations,
				}}}
			},
		},
		{
			name: "solvencies",
			batch: func(attestations []*common.Attestation) common.QuorumState {
				return common.QuorumState{QuoSolvencies: []*common.QuorumSolvency{{
					Solvency:     &common.Solvency{},
					Attestations: attestations,
				}}}
			},
		},
		{
			name: "errata",
			batch: func(attestations []*common.Attestation) common.QuorumState {
				return common.QuorumState{QuoErrataTxs: []*common.QuorumErrataTx{{
					ErrataTx:     &common.ErrataTx{},
					Attestations: attestations,
				}}}
			},
		},
		{
			name: "price feeds",
			batch: func(attestations []*common.Attestation) common.QuorumState {
				return common.QuorumState{QuoPriceFeeds: []*common.QuorumPriceFeed{{
					PriceFeed:    &common.PriceFeed{},
					Attestations: attestations,
				}}}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			atLimit := tt.batch(makeAttestationsForStateTest(maxAttestationStateAttestationsPerItem))
			require.NoError(t, validateAttestationStateBatch(context.Background(), atLimit))

			overLimit := tt.batch(makeAttestationsForStateTest(maxAttestationStateAttestationsPerItem + 1))
			err := validateAttestationStateBatch(context.Background(), overLimit)
			require.ErrorContains(t, err, fmt.Sprintf(
				"attestations count %d exceeds maximum %d",
				maxAttestationStateAttestationsPerItem+1,
				maxAttestationStateAttestationsPerItem,
			))
		})
	}
}

func signedNetworkFeeAttestations(
	t *testing.T,
	networkFee *common.NetworkFee,
	count int,
) []*common.Attestation {
	t.Helper()
	payload, err := networkFee.GetSignablePayload()
	require.NoError(t, err)
	attestations := make([]*common.Attestation, count)
	for i := range attestations {
		key := secp256k1.GenPrivKey()
		signature, err := key.Sign(payload)
		require.NoError(t, err)
		attestations[i] = &common.Attestation{
			PubKey:    key.PubKey().Bytes(),
			Signature: signature,
		}
	}
	return attestations
}

func TestValidateAttestationStateBatchRejectsTooManyValidDistinctSigners(t *testing.T) {
	networkFee := &common.NetworkFee{
		Chain:           common.BTCChain,
		Height:          1,
		TransactionSize: 1,
		TransactionRate: 1,
	}
	attestations := signedNetworkFeeAttestations(
		t,
		networkFee,
		maxAttestationStateAttestationsPerItem+1,
	)
	err := validateAttestationStateBatch(context.Background(), common.QuorumState{
		QuoNetworkFees: []*common.QuorumNetworkFee{{
			NetworkFee:   networkFee,
			Attestations: attestations,
		}},
	})
	require.ErrorContains(t, err, fmt.Sprintf(
		"attestations count %d exceeds maximum %d",
		maxAttestationStateAttestationsPerItem+1,
		maxAttestationStateAttestationsPerItem,
	))
}

func TestProcessAttestationStateBatchRejectsNestedOversizeBeforeDispatch(t *testing.T) {
	ag, _, _, grpcClient, _, _ := setupTestGossip(t)
	var thornodeRPCs atomic.Int32
	grpcClient.sendQuorumNetworkFeeFunc = func(
		context.Context,
		*common.QuorumNetworkFee,
		...grpc.CallOption,
	) (*ebifrost.SendQuorumNetworkFeeResult, error) {
		thornodeRPCs.Add(1)
		return &ebifrost.SendQuorumNetworkFeeResult{}, nil
	}

	networkFee := &common.NetworkFee{
		Chain:           common.BTCChain,
		Height:          1,
		TransactionSize: 1,
		TransactionRate: 1,
	}
	err := ag.processAttestationStateBatch(context.Background(), ag.logger, common.QuorumState{
		QuoNetworkFees: []*common.QuorumNetworkFee{{
			NetworkFee:   networkFee,
			Attestations: signedNetworkFeeAttestations(t, networkFee, 3),
		}},
		QuoSolvencies: []*common.QuorumSolvency{{
			Solvency:     &common.Solvency{},
			Attestations: makeAttestationsForStateTest(maxAttestationStateAttestationsPerItem + 1),
		}},
	})
	require.ErrorContains(t, err, fmt.Sprintf(
		"quoSolvencies[0].attestations count %d exceeds maximum %d",
		maxAttestationStateAttestationsPerItem+1,
		maxAttestationStateAttestationsPerItem,
	))
	require.Empty(t, ag.observedTxs)
	require.Empty(t, ag.networkFees)
	require.Empty(t, ag.solvencies)
	require.Empty(t, ag.errataTxs)
	require.Empty(t, ag.priceFeeds)
	require.Zero(t, thornodeRPCs.Load(), "oversized preflight must not call Thornode")
}

func makeAttestationsForStateTest(count int) []*common.Attestation {
	attestations := make([]*common.Attestation, count)
	for i := range attestations {
		attestations[i] = &common.Attestation{}
	}
	return attestations
}

func TestProcessAttestationStateBatchPreflightsBeforeDispatch(t *testing.T) {
	ag, _, _, _, _, _ := setupTestGossip(t)
	err := ag.processAttestationStateBatch(context.Background(), ag.logger, common.QuorumState{
		QuoNetworkFees: []*common.QuorumNetworkFee{
			{
				NetworkFee:   &common.NetworkFee{},
				Attestations: []*common.Attestation{{}},
			},
			{
				Attestations: []*common.Attestation{{}},
			},
		},
	})
	require.ErrorContains(t, err, "quoNetworkFees[1].networkFee is nil")
	require.Empty(t, ag.networkFees, "a valid earlier record was dispatched before batch validation completed")
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

	// Authorize the active receiver to ask agSender for state.
	agSender.avMu.Lock()
	agSender.activeVals[agReceiver.host.ID()] = true
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
			agReceiver.askStreamForState(context.Background(), receiverStream)
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
