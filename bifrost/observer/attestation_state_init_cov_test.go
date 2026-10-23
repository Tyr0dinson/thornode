package observer

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p-core/network"
	"github.com/libp2p/go-libp2p-core/peer"
	"github.com/libp2p/go-libp2p-core/protocol"
	"github.com/stretchr/testify/require"

	"gitlab.com/thorchain/thornode/v3/bifrost/p2p"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

// scriptStreamCov replays pre-framed responses, never crossing a frame
// boundary within one Read (ReadStreamWithBuffer uses a fresh bufio.Reader per
// message), and records every frame written to it.
type scriptStreamCov struct {
	network.Stream

	mu          sync.Mutex
	frames      [][]byte
	cur         []byte
	written     bytes.Buffer
	writes      int
	failWriteAt int // 1-based write index that fails; 0 = never
	peer        peer.ID
}

func frameCov(msg []byte) []byte {
	out := make([]byte, 4, 4+len(msg))
	binary.LittleEndian.PutUint32(out, uint32(len(msg)))
	return append(out, msg...)
}

func newScriptStreamCov(responses ...[]byte) *scriptStreamCov {
	s := &scriptStreamCov{peer: "script-peer"}
	for _, r := range responses {
		s.frames = append(s.frames, frameCov(r))
	}
	return s
}

func (s *scriptStreamCov) Read(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.cur) == 0 {
		if len(s.frames) == 0 {
			return 0, io.EOF
		}
		s.cur, s.frames = s.frames[0], s.frames[1:]
	}
	n := copy(p, s.cur)
	s.cur = s.cur[n:]
	return n, nil
}

func (s *scriptStreamCov) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.writes++
	if s.failWriteAt > 0 && s.writes >= s.failWriteAt {
		return 0, errors.New("write failed")
	}
	return s.written.Write(p)
}

func (s *scriptStreamCov) Close() error                     { return nil }
func (s *scriptStreamCov) Reset() error                     { return nil }
func (s *scriptStreamCov) SetReadDeadline(time.Time) error  { return nil }
func (s *scriptStreamCov) SetWriteDeadline(time.Time) error { return nil }
func (s *scriptStreamCov) Conn() network.Conn               { return &MockConn{peer: s.peer} }

// writtenFrames decodes every length-prefixed frame written to the stream.
func (s *scriptStreamCov) writtenFrames(t *testing.T) [][]byte {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	data := s.written.Bytes()
	var out [][]byte
	for len(data) > 0 {
		require.GreaterOrEqual(t, len(data), 4)
		n := int(binary.LittleEndian.Uint32(data[:4]))
		require.GreaterOrEqual(t, len(data), 4+n)
		out = append(out, data[4:4+n])
		data = data[4+n:]
	}
	return out
}

func disableDeadlinesCov(t *testing.T) {
	orig := p2p.ApplyDeadline.Load()
	p2p.ApplyDeadline.Store(false)
	t.Cleanup(func() { p2p.ApplyDeadline.Store(orig) })
}

// withPriceFeedsCov initialises the price feed state the shared test gossip
// fixture leaves unset.
func withPriceFeedsCov(ag *AttestationGossip) {
	ag.mu.Lock()
	defer ag.mu.Unlock()
	ag.priceFeeds = make(map[string]*AttestationState[*common.PriceFeed])
	ag.priceFeedsPool = NewAttestationStatePool[*common.PriceFeed]()
}

func populateAllStateKindsCov(t *testing.T, ag *AttestationGossip) {
	t.Helper()
	withPriceFeedsCov(ag)
	att := &common.Attestation{PubKey: []byte("pk"), Signature: []byte("sig")}

	obs := &common.ObservedTx{Tx: common.Tx{ID: "TXA", Chain: common.BTCChain}}
	txState := ag.observedTxsPool.NewAttestationState(&attestableObservedTx{ObservedTx: obs})
	txState.attestations = []attestationSentState{{attestation: att}}

	nf := &common.NetworkFee{Chain: common.BTCChain, Height: 1, TransactionSize: 2, TransactionRate: 3}
	nfState := ag.networkFeesPool.NewAttestationState(nf)
	nfState.attestations = []attestationSentState{{attestation: att}}

	sol := &common.Solvency{Id: "SOL", Chain: common.BTCChain, Height: 5, Coins: common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(7))}}
	solState := ag.solvenciesPool.NewAttestationState(sol)
	solState.attestations = []attestationSentState{{attestation: att}}

	errata := &common.ErrataTx{Id: "ERR", Chain: common.ETHChain}
	errataState := ag.errataTxsPool.NewAttestationState(errata)
	errataState.attestations = []attestationSentState{{attestation: att}}

	pf := &common.PriceFeed{Time: 42, Rates: []*common.OraclePrice{{Amount: 1}}}
	pfState := ag.priceFeedsPool.NewAttestationState(pf)
	pfState.attestations = []attestationSentState{{attestation: att}}

	ag.mu.Lock()
	ag.observedTxs[txKey{Chain: common.BTCChain, ID: "TXA"}] = txState
	ag.networkFees[*nf] = nfState
	ag.solvencies["SOL"] = solState
	ag.errataTxs[*errata] = errataState
	ag.priceFeeds["node"] = pfState
	ag.mu.Unlock()
}

func TestSendAttestationStateAllKindsCov(t *testing.T) {
	disableDeadlinesCov(t)
	ag, _, _, _, _, _ := setupTestGossip(t)
	populateAllStateKindsCov(t, ag)

	stream := newScriptStreamCov(
		[]byte(streamAckBegin), []byte(streamAckHeader), []byte(streamAckData), []byte(p2p.StreamMsgDone),
	)
	ag.sendAttestationState(stream)

	frames := stream.writtenFrames(t)
	require.Len(t, frames, 4)
	require.True(t, bytes.HasPrefix(frames[0], prefixBatchBegin))
	require.Equal(t, uint32(1), binary.LittleEndian.Uint32(frames[0][1:5]))
	require.True(t, bytes.HasPrefix(frames[1], prefixBatchHeader))
	require.True(t, bytes.HasPrefix(frames[2], prefixBatchData))
	require.Equal(t, prefixBatchEnd, frames[3])

	var batch common.QuorumState
	require.NoError(t, batch.Unmarshal(frames[2][1:]))
	require.Len(t, batch.QuoTxs, 1)
	require.Len(t, batch.QuoNetworkFees, 1)
	require.Len(t, batch.QuoSolvencies, 1)
	require.Len(t, batch.QuoErrataTxs, 1)
	require.Equal(t, common.TxID("ERR"), batch.QuoErrataTxs[0].ErrataTx.Id)
	require.Len(t, batch.QuoPriceFeeds, 1)
	require.Equal(t, int64(42), batch.QuoPriceFeeds[0].PriceFeed.Time)
	require.Equal(t, uint32(len(frames[2])-1), binary.LittleEndian.Uint32(frames[1][5:9]))
}

func TestSendAttestationStateProtocolErrorsCov(t *testing.T) {
	disableDeadlinesCov(t)
	ag, _, _, _, _, _ := setupTestGossip(t)
	populateAllStateKindsCov(t, ag)

	ackBegin, ackHeader, ackData, done := []byte(streamAckBegin), []byte(streamAckHeader), []byte(streamAckData), []byte(p2p.StreamMsgDone)
	bogus := []byte("bogus")
	cases := []struct {
		name        string
		responses   [][]byte
		failWriteAt int
		wantFrames  int
	}{
		{"begin write fails", nil, 1, 0},
		{"begin ack missing", nil, 0, 1},
		{"begin ack wrong", [][]byte{bogus}, 0, 1},
		{"header write fails", [][]byte{ackBegin}, 2, 1},
		{"header ack missing", [][]byte{ackBegin}, 0, 2},
		{"header ack wrong", [][]byte{ackBegin, bogus}, 0, 2},
		{"data write fails", [][]byte{ackBegin, ackHeader}, 3, 2},
		{"data ack missing", [][]byte{ackBegin, ackHeader}, 0, 3},
		{"data ack wrong", [][]byte{ackBegin, ackHeader, bogus}, 0, 3},
		{"end write fails", [][]byte{ackBegin, ackHeader, ackData}, 4, 3},
		{"end ack missing", [][]byte{ackBegin, ackHeader, ackData}, 0, 4},
		{"end ack wrong", [][]byte{ackBegin, ackHeader, ackData, bogus}, 0, 4},
		{"success", [][]byte{ackBegin, ackHeader, ackData, done}, 0, 4},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stream := newScriptStreamCov(tc.responses...)
			stream.failWriteAt = tc.failWriteAt
			ag.sendAttestationState(stream)
			require.Len(t, stream.writtenFrames(t), tc.wantFrames)
			// every scripted response was consumed: the sender stopped exactly
			// where the protocol broke
			stream.mu.Lock()
			defer stream.mu.Unlock()
			require.Empty(t, stream.frames)
		})
	}

	// empty state with a non-"done" reply still terminates after the begin frame
	empty, _, _, _, _, _ := setupTestGossip(t)
	stream := newScriptStreamCov(bogus)
	empty.sendAttestationState(stream)
	frames := stream.writtenFrames(t)
	require.Len(t, frames, 1)
	require.Equal(t, uint32(0), binary.LittleEndian.Uint32(frames[0][1:5]))
}

func batchBeginCov(n uint32) []byte {
	b := make([]byte, 4)
	binary.LittleEndian.PutUint32(b, n)
	return b
}

func batchHeaderCov(num, size uint32) []byte {
	b := make([]byte, 8)
	binary.LittleEndian.PutUint32(b[0:4], num)
	binary.LittleEndian.PutUint32(b[4:8], size)
	return append(append([]byte{}, prefixBatchHeader...), b...)
}

func TestReceiveBatchedAttestationStateErrorsCov(t *testing.T) {
	disableDeadlinesCov(t)
	ag, _, _, _, _, _ := setupTestGossip(t)

	garbage := []byte{0xff, 0xff, 0xff}
	cases := []struct {
		name        string
		initial     []byte
		responses   [][]byte
		failWriteAt int
		wantErr     string
	}{
		{"short begin", []byte{1, 2}, nil, 0, "invalid batch begin format"},
		{"empty state ack fails", batchBeginCov(0), nil, 1, "failed to send final ack"},
		{"begin ack fails", batchBeginCov(1), nil, 1, "failed to acknowledge batch begin"},
		{"no batch message", batchBeginCov(1), nil, 0, "failed to read batch message"},
		{"bad header", batchBeginCov(1), [][]byte{[]byte("x")}, 0, "invalid batch header format"},
		{"header ack fails", batchBeginCov(1), [][]byte{batchHeaderCov(0, 3)}, 2, "failed to acknowledge batch header"},
		{"no batch data", batchBeginCov(1), [][]byte{batchHeaderCov(0, 3)}, 0, "failed to read batch data"},
		{"bad data prefix", batchBeginCov(1), [][]byte{batchHeaderCov(0, 3), []byte("zzzz")}, 0, "invalid batch data format"},
		{"size mismatch", batchBeginCov(1), [][]byte{batchHeaderCov(0, 9), append(append([]byte{}, prefixBatchData...), garbage...)}, 0, "batch data size mismatch"},
		{"data ack fails", batchBeginCov(1), [][]byte{batchHeaderCov(0, 3), append(append([]byte{}, prefixBatchData...), garbage...)}, 3, "failed to acknowledge batch data"},
		{"bad batch proto", batchBeginCov(1), [][]byte{batchHeaderCov(0, 3), append(append([]byte{}, prefixBatchData...), garbage...)}, 0, "failed to unmarshal batch"},
		{"end ack fails", batchBeginCov(1), [][]byte{batchHeaderCov(0, 0), prefixBatchData, prefixBatchEnd}, 4, "failed to send final ack"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stream := newScriptStreamCov(tc.responses...)
			stream.failWriteAt = tc.failWriteAt
			err := ag.receiveBatchedAttestationState(stream, tc.initial)
			require.Error(t, err)
			require.Contains(t, err.Error(), tc.wantErr)
		})
	}

	// an empty batch is acknowledged and skipped
	stream := newScriptStreamCov(batchHeaderCov(0, 0), prefixBatchData, prefixBatchEnd)
	require.NoError(t, ag.receiveBatchedAttestationState(stream, batchBeginCov(1)))
	frames := stream.writtenFrames(t)
	require.Equal(t, [][]byte{[]byte(streamAckBegin), []byte(streamAckHeader), []byte(streamAckData), []byte(p2p.StreamMsgDone)}, frames)
}

func TestReceiveBatchedAttestationStateErrataAndPriceFeedCov(t *testing.T) {
	disableDeadlinesCov(t)
	ag, _, _, _, _, _ := setupTestGossip(t)
	withPriceFeedsCov(ag)

	att := &common.Attestation{PubKey: []byte("not-a-key"), Signature: []byte("sig")}
	batch := common.QuorumState{
		QuoErrataTxs: []*common.QuorumErrataTx{{
			ErrataTx:     &common.ErrataTx{Id: "ERR", Chain: common.ETHChain},
			Attestations: []*common.Attestation{att},
		}},
		QuoPriceFeeds: []*common.QuorumPriceFeed{{
			PriceFeed:    &common.PriceFeed{Time: 1},
			Attestations: []*common.Attestation{att},
		}},
	}
	bz, err := batch.Marshal()
	require.NoError(t, err)

	stream := newScriptStreamCov(
		batchHeaderCov(0, uint32(len(bz))),
		append(append([]byte{}, prefixBatchData...), bz...),
		prefixBatchEnd,
	)
	require.NoError(t, ag.receiveBatchedAttestationState(stream, batchBeginCov(1)))

	ag.mu.Lock()
	defer ag.mu.Unlock()
	// the errata is tracked (its invalid attestation is rejected), while an
	// unverifiable price feed is dropped entirely
	_, ok := ag.errataTxs[common.ErrataTx{Id: "ERR", Chain: common.ETHChain}]
	require.True(t, ok)
	require.Empty(t, ag.priceFeeds)
}

func TestAskStreamForStateErrorsCov(t *testing.T) {
	disableDeadlinesCov(t)
	ag, _, _, _, _, _ := setupTestGossip(t)

	// write failure: nothing is read
	stream := newScriptStreamCov([]byte("unread"))
	stream.failWriteAt = 1
	ag.askStreamForState(stream)
	stream.mu.Lock()
	require.Len(t, stream.frames, 1)
	stream.mu.Unlock()

	// no reply
	stream = newScriptStreamCov()
	ag.askStreamForState(stream)
	require.Equal(t, [][]byte{prefixSendState}, stream.writtenFrames(t))

	// reply that is not a batch begin
	stream = newScriptStreamCov([]byte("nope"))
	ag.askStreamForState(stream)
	require.Equal(t, [][]byte{prefixSendState}, stream.writtenFrames(t))

	// batch begin with a malformed receive is logged and returns
	stream = newScriptStreamCov(append(append([]byte{}, prefixBatchBegin...), 1, 0, 0, 0))
	ag.askStreamForState(stream)
	require.Equal(t, [][]byte{prefixSendState, []byte(streamAckBegin)}, stream.writtenFrames(t))
}

func TestAskPeerForStateStreamErrorCov(t *testing.T) {
	disableDeadlinesCov(t)
	ag, host, _, _, _, _ := setupTestGossip(t)
	var called []peer.ID
	var mu sync.Mutex
	host.streamFunc = func(_ context.Context, p peer.ID, _ ...protocol.ID) (network.Stream, error) {
		mu.Lock()
		defer mu.Unlock()
		called = append(called, p)
		return nil, errors.New("dial failed")
	}
	result := ag.askPeerForState(context.Background(), "remote")
	require.False(t, result.completed)
	require.EqualError(t, result.err, "dial failed")
	require.Equal(t, []peer.ID{"remote"}, called)
}
