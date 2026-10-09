package p2p

import (
	"encoding/binary"
	"testing"
	"time"

	"github.com/golang/protobuf/proto" // nolint: staticcheck
	"github.com/libp2p/go-libp2p-core/network"
	"github.com/libp2p/go-libp2p-core/peer"
	"github.com/stretchr/testify/require"

	"gitlab.com/thorchain/thornode/v3/bifrost/p2p/messages"
)

func disableDeadlinesPCCov(t *testing.T) {
	orig := ApplyDeadline.Load()
	ApplyDeadline.Store(false)
	t.Cleanup(func() { ApplyDeadline.Store(orig) })
}

func streamCountCov(pc *PartyCoordinator, msgID string) int {
	pc.streamMgr.streamLocker.RLock()
	defer pc.streamMgr.streamLocker.RUnlock()
	return len(pc.streamMgr.unusedStreams[msgID])
}

func emptyHandlerStreamCov(remote peer.ID) *handlerStream {
	return &handlerStream{MockNetworkStream: NewMockNetworkStream(), conn: fakeTSSConn{remote: remote}}
}

func joinLeaderFrameCov(t *testing.T, msg *messages.JoinPartyLeaderComm) []byte {
	t.Helper()
	bz, err := proto.Marshal(msg)
	require.NoError(t, err)
	return bz
}

func TestNewPartyCoordinatorDefaultTimeoutCov(t *testing.T) {
	hosts := setupHostsLocally(t, 1)
	pc := NewPartyCoordinator(hosts[0], 0)
	defer pc.Stop()
	require.Equal(t, 10*time.Second, pc.timeout)
}

func TestPartyCoordinatorHandleStreamErrorsCov(t *testing.T) {
	disableDeadlinesPCCov(t)
	hosts := setupHostsLocally(t, 2)
	pc := NewPartyCoordinator(hosts[0], time.Second)
	defer pc.Stop()
	remote := hosts[1].ID()

	// unreadable stream is parked as unknown
	pc.HandleStream(emptyHandlerStreamCov(remote))
	require.Equal(t, 1, streamCountCov(pc, StreamUnknown))

	// garbage payload is parked as unknown
	pc.HandleStream(newHandlerStream(remote, []byte{0xff, 0xff, 0xff}))
	require.Equal(t, 2, streamCountCov(pc, StreamUnknown))

	// request for a party this node has not created yet is parked by msgID
	req, err := proto.Marshal(&messages.JoinPartyRequest{ID: "not-ready"})
	require.NoError(t, err)
	pc.HandleStream(newHandlerStream(remote, req))
	require.Equal(t, 1, streamCountCov(pc, "not-ready"))

	// request from a peer outside the party is rejected without joining it
	group, err := pc.createJoinPartyGroups("party", "", []peer.ID{hosts[0].ID()}, 1)
	require.NoError(t, err)
	req, err = proto.Marshal(&messages.JoinPartyRequest{ID: "party"})
	require.NoError(t, err)
	pc.HandleStream(newHandlerStream(remote, req))
	require.Equal(t, 1, streamCountCov(pc, "party"))
	online, _ := group.getPeersStatus()
	require.NotContains(t, online, remote)
}

func TestPartyCoordinatorHandleStreamWithLeaderErrorsCov(t *testing.T) {
	disableDeadlinesPCCov(t)
	hosts := setupHostsLocally(t, 3)
	pc := NewPartyCoordinator(hosts[0], time.Second)
	defer pc.Stop()
	leader, other := hosts[1].ID(), hosts[2].ID()

	pc.HandleStreamWithLeader(emptyHandlerStreamCov(leader))
	pc.HandleStreamWithLeader(newHandlerStream(leader, []byte{0xff, 0xff, 0xff}))
	pc.HandleStreamWithLeader(newHandlerStream(leader, joinLeaderFrameCov(t, &messages.JoinPartyLeaderComm{ID: "x", MsgType: "bogus"})))
	require.Equal(t, 3, streamCountCov(pc, StreamUnknown))

	// a request from a peer outside the party does not mark it online
	group, err := pc.createJoinPartyGroups("party", leader, []peer.ID{hosts[0].ID(), leader}, 1)
	require.NoError(t, err)
	pc.HandleStreamWithLeader(newHandlerStream(other, joinLeaderFrameCov(t, &messages.JoinPartyLeaderComm{ID: "party", MsgType: "request"})))
	online, _ := group.getPeersStatus()
	require.NotContains(t, online, other)

	// a response from a non-leader is ignored, but still acknowledged
	stream := newHandlerStream(other, joinLeaderFrameCov(t, &messages.JoinPartyLeaderComm{ID: "party", MsgType: "response"}))
	pc.HandleStreamWithLeader(stream)
	require.Nil(t, group.getLeaderResponse())
	ack, err := ReadStreamWithBuffer(stream)
	require.NoError(t, err)
	require.Equal(t, StreamMsgDone, string(ack))

	// a response from the leader is recorded and the waiter notified
	stream = newHandlerStream(leader, joinLeaderFrameCov(t, &messages.JoinPartyLeaderComm{ID: "party", MsgType: "response", PeerIDs: []string{leader.String()}}))
	pc.HandleStreamWithLeader(stream)
	select {
	case <-group.notify:
	case <-time.After(time.Second):
		t.Fatal("leader response did not notify")
	}
	resp := group.getLeaderResponse()
	require.NotNil(t, resp)
	require.Equal(t, []string{leader.String()}, resp.PeerIDs)
	// the leader's response is acknowledged twice: once by processRespMsg and
	// once by HandleStreamWithLeader
	frame := make([]byte, LengthHeader)
	binary.LittleEndian.PutUint32(frame, uint32(len(StreamMsgDone)))
	frame = append(frame, StreamMsgDone...)
	require.Equal(t, append(append([]byte{}, frame...), frame...), stream.Bytes())
}

func TestJoinPartyInputValidationCov(t *testing.T) {
	hosts := setupHostsLocally(t, 2)
	pc := NewPartyCoordinator(hosts[0], time.Second)
	defer pc.Stop()
	self, other := hosts[0].ID().String(), hosts[1].ID().String()

	_, _, err := pc.JoinPartyWithLeader("msg", 0, []string{self}, 0, nil)
	require.ErrorContains(t, err, "invalid input for finding the leader")

	_, _, err = pc.JoinPartyWithSelectedLeader("", self, []string{self}, 0, nil)
	require.ErrorContains(t, err, "invalid input for joining party with selected leader")
	_, _, err = pc.JoinPartyWithSelectedLeader("msg", "not-a-peer-id", []string{self}, 0, nil)
	require.Error(t, err)
	_, _, err = pc.JoinPartyWithSelectedLeader("msg", self, []string{"not-a-peer-id"}, 0, nil)
	require.ErrorContains(t, err, "fail to decode peer id")
	_, _, err = pc.JoinPartyWithSelectedLeader("msg", other, []string{self}, 0, nil)
	require.ErrorContains(t, err, "selected leader is not a party member")

	// a single-member party led by ourselves forms immediately
	onlines, leader, err := pc.JoinPartyWithSelectedLeader("msg", self, []string{self}, 0, nil)
	require.NoError(t, err)
	require.Equal(t, self, leader)
	require.Equal(t, []peer.ID{hosts[0].ID()}, onlines)

	_, err = pc.JoinPartyWithRetry("msg", []string{"not-a-peer-id"})
	require.ErrorContains(t, err, "fail to decode peer id")
}

func TestSendRequestToLeaderUnreachableCov(t *testing.T) {
	hosts := setupHostsLocally(t, 1)
	pc := NewPartyCoordinator(hosts[0], time.Second)
	defer pc.Stop()

	unknown := setupHostsLocally(t, 1)[0].ID() // a different mocknet: unreachable
	err := pc.sendRequestToLeader(&messages.JoinPartyLeaderComm{ID: "m"}, unknown)
	require.EqualError(t, err, "fail to send request to leader")
}

// sanity check of the frame helper used above
func TestHandlerStreamFrameCov(t *testing.T) {
	s := newHandlerStream("p", []byte("abc"))
	var hdr [LengthHeader]byte
	_, err := s.Read(hdr[:])
	require.NoError(t, err)
	require.Equal(t, uint32(3), binary.LittleEndian.Uint32(hdr[:]))
	var _ network.Stream = s
}
