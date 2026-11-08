package tss

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/libp2p/go-libp2p-core/peer"

	"gitlab.com/thorchain/thornode/v3/bifrost/p2p"
	"gitlab.com/thorchain/thornode/v3/bifrost/p2p/conversion"
	"gitlab.com/thorchain/thornode/v3/bifrost/p2p/messages"
	"gitlab.com/thorchain/thornode/v3/bifrost/tss/go-tss/blame"
	tcommon "gitlab.com/thorchain/thornode/v3/bifrost/tss/go-tss/common"
	thorchaintypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

func enqueueFrostPresence(t *testing.T, coord *FrostKeygenCoordinator, sender, session, payload string) {
	t.Helper()
	id, err := conversion.GetPeerIDFromPubKey(sender)
	if err != nil {
		t.Fatal(err)
	}
	wire, err := json.Marshal(messages.WrappedMessage{
		MessageType: messages.TSSFrostDKGPresenceMsg, MsgID: session, Payload: []byte(payload),
	})
	if err != nil {
		t.Fatal(err)
	}
	coord.inbox.presence <- &p2p.Message{PeerID: id, Payload: wire}
}

func TestFrostKeygenPresenceWholeAbsenceAndSelectiveWithholding(t *testing.T) {
	ensureBech32Prefix()
	members := append([]string(nil), testPubKeys[:4]...)
	for _, selective := range []bool{false, true} {
		synctest.Test(t, func(t *testing.T) {
			reports := make(map[string]int)
			for victim := 0; victim < 3; victim++ {
				coord := newTestFrostKeygenCoordinator(members[victim], "presence", newFrostKeygenInbox(), time.Second)
				for sender := 0; sender < 4; sender++ {
					if sender == victim || (sender == 3 && (!selective || victim == 0)) {
						continue
					}
					enqueueFrostPresence(t, coord, strings.ToUpper(members[sender]), coord.msgID, frostKeygenPresence)
				}
				response := coord.exchangePresence(members, nil)
				// Even a victim that times out first announced itself unconditionally.
				outbound := <-coord.p2pCommunication.BroadcastMsgChan
				if outbound.WrappedMessage.MessageType != messages.TSSFrostDKGPresenceMsg {
					t.Fatal("presence was not the first unconditional message")
				}
				if response == nil {
					continue
				}
				if len(response.AbsentMembers) != 1 || response.AbsentMembers[0] != members[3] {
					t.Fatalf("only absent attacker may be nominated: %+v", response)
				}
				if len(response.Blame.BlameNodes) != 0 {
					t.Fatal("presence leaked into diagnostic blame")
				}
				id := frostKeygenBlameReportID(t, members, buildBlame("absence", "frost_dkg_presence", response.AbsentMembers))
				reports[id]++
			}
			want := 3
			if selective {
				want = 1
			}
			if len(reports) != 1 {
				t.Fatalf("canonical singleton reports split: %v", reports)
			}
			for _, count := range reports {
				if count != want {
					t.Fatalf("reports = %d, want %d", count, want)
				}
			}
		})
	}
}

// Enumerate the one Byzantine member (<1/3 of four) and every subset of
// recipients it withholds from. Honest presence is independent of all attacks.
func TestFrostKeygenPresenceCoalitionCannotFrameHonestMember(t *testing.T) {
	ensureBech32Prefix()
	members := testPubKeys[:4]
	for attacker := range members {
		for withheld := 0; withheld < 16; withheld++ {
			synctest.Test(t, func(t *testing.T) {
				for local := range members {
					if local == attacker {
						continue
					}
					coord := newTestFrostKeygenCoordinator(members[local], "coalition", newFrostKeygenInbox(), time.Second)
					// An authenticated attacker's malformed/false accusation cannot abort
					// admission or nominate the claimed honest identity.
					enqueueFrostPresence(t, coord, members[attacker], coord.msgID, members[local])
					for sender := range members {
						if sender == local || (sender == attacker && withheld&(1<<local) != 0) {
							continue
						}
						enqueueFrostPresence(t, coord, members[sender], coord.msgID, frostKeygenPresence)
					}
					if response := coord.exchangePresence(members, nil); response != nil {
						for _, absent := range response.AbsentMembers {
							if absent != members[attacker] {
								t.Fatalf("honest member framed: %+v", response)
							}
						}
					}
				}
			})
		}
	}
}

func TestFrostKeygenPresenceMinorityAndWrongSessionAreDiagnostic(t *testing.T) {
	ensureBech32Prefix()
	synctest.Test(t, func(t *testing.T) {
		coord := newTestFrostKeygenCoordinator(testPubKeys[0], "current", newFrostKeygenInbox(), time.Second)
		enqueueFrostPresence(t, coord, testPubKeys[1], "old", frostKeygenPresence)
		enqueueFrostPresence(t, coord, testPubKeys[2], "old", frostKeygenPresence)
		response := coord.exchangePresence(testPubKeys[:4], nil)
		if response == nil || len(response.AbsentMembers) != 0 {
			t.Fatalf("minority blamed peers: %+v", response)
		}
	})
}

func TestFrostKeygenEquivocationCannotBecomeAbsence(t *testing.T) {
	ensureBech32Prefix()
	active, indexes, _ := testFrostKeygenParticipants(t, testPubKeys[:4])
	coord := newTestFrostKeygenCoordinator(testPubKeys[0], "equivocation", newFrostKeygenInbox(), time.Second)
	initTestFrostKeygenRoundState(t, coord, active, indexes)
	verifier, err := coord.roundState.verifier(frostDKGRound1)
	if err != nil {
		t.Fatal(err)
	}
	for _, hash := range []string{strings.Repeat("a", 64), strings.Repeat("b", 64)} {
		_, failure := verifier.recordTranscriptVote(testPubKeys[3], hash)
		if failure != nil {
			t.Fatal(failure)
		}
	}
	failure := coord.failForPubKeys(active, "transport equivocation", frostDKGRound1, []string{testPubKeys[3]})
	if len(failure.AbsentMembers) != 0 {
		t.Fatal("equivocation became admission evidence")
	}
	// Even a quorum peer failure cannot populate local evidence.
	inbox := newFrostKeygenInbox()
	for i := 0; i < 3; i++ {
		enqueueRoundMessage(t, inbox, messages.TSSFrostDKGResultMsg, "peer-failure", testPubKeys[i],
			FrostDKGResultPayload{SenderPubKey: testPubKeys[i], Response: FrostDKGResult{
				Status: uint8(tcommon.Fail), Blame: blame.NewBlame("peer allegation", []blame.Node{{Pubkey: testPubKeys[3]}}),
			}})
	}
	coord.inbox = inbox
	coord.msgID = "peer-failure"
	adopted, err := coord.WaitForResult(testPubKeys[:4], 3, time.Second)
	if err != nil || len(adopted.AbsentMembers) != 0 {
		t.Fatalf("peer result became local evidence: err=%v response=%+v", err, adopted)
	}
}

// Each peer has a real libp2p host; subscriptions are installed by callers at
// the actual admission start, so frames sent too early really are rejected.
func newFrostPresenceTestNetwork(t *testing.T) ([]*p2p.Communication, []peer.ID) {
	t.Helper()
	ensureBech32Prefix()
	comms := make([]*p2p.Communication, 4)
	peers := make([]peer.ID, 4)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for i := range comms {
		encoded, err := base64.StdEncoding.DecodeString(testPriKeyArr[i])
		if err != nil {
			t.Fatal(err)
		}
		key, err := hex.DecodeString(string(encoded))
		if err != nil {
			t.Fatal(err)
		}
		comm, err := p2p.NewCommunication(&p2p.Config{Port: 0, RendezvousString: "presence-accountability"}, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err = comm.Start(key); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = comm.Stop() })
		comms[i], peers[i] = comm, comm.GetHost().ID()
		for j := 0; j < i; j++ {
			if err = comm.GetHost().Connect(ctx, peer.AddrInfo{ID: peers[j], Addrs: comms[j].GetHost().Addrs()}); err != nil {
				t.Fatal(err)
			}
		}
	}
	return comms, peers
}

func TestFrostKeygenPresenceRealTransport(t *testing.T) {
	comms, peers := newFrostPresenceTestNetwork(t)
	ms := time.Millisecond
	for _, scenario := range []struct {
		name         string
		start        [4]time.Duration
		presubscribe bool
		offline      bool
		selective    bool
	}{
		{name: "all present"},
		{name: "pre-subscribed control", start: [4]time.Duration{0, 400 * ms, 800 * ms, 1200 * ms}, presubscribe: true},
		{name: "one early member", start: [4]time.Duration{0, 500 * ms, 500 * ms, 500 * ms}},
		{name: "staggered subscriptions", start: [4]time.Duration{0, 400 * ms, 800 * ms, 1200 * ms}},
		{name: "offline with staggered subscriptions", start: [4]time.Duration{0, 400 * ms, 800 * ms}, offline: true},
		{name: "selective with staggered subscriptions", start: [4]time.Duration{0, 400 * ms, 800 * ms}, selective: true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			sessionID, hashErr := tcommon.MsgToHashString([]byte(scenario.name))
			if hashErr != nil {
				t.Fatal(hashErr)
			}
			// Cover the complete 2s/6s write retry schedule. A successful flush
			// before subscription must be repaired at the admission layer.
			const admission = 9 * time.Second
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			coords := make([]*FrostKeygenCoordinator, 4)
			subscribe := func(i int) {
				comms[i].SetSubscribeWithPeers(messages.TSSFrostDKGPresenceMsg, sessionID,
					coords[i].inbox.presence, peers, 512, p2p.FrostKeygenPresenceMessagesPerPeer)
			}
			count := 4
			if scenario.offline || scenario.selective {
				count = 3
			}
			for i, comm := range comms {
				coord := newTestFrostKeygenCoordinator(testPubKeys[i], sessionID, newFrostKeygenInbox(), admission)
				coord.p2pCommunication = comm
				coords[i] = coord
				if scenario.presubscribe {
					subscribe(i)
				}
			}
			type result struct {
				member  int
				failure *FrostKeygenResponse
			}
			done := make(chan result, count)
			begin := time.Now()
			for i := 0; i < count; i++ {
				go func() {
					time.Sleep(time.Until(begin.Add(scenario.start[i])))
					if !scenario.presubscribe {
						subscribe(i)
					}
					failure := coords[i].exchangePresence(testPubKeys[:4], peers)
					// Exercise immediate cleanup, including the earliest finisher.
					comms[i].CancelSubscribe(messages.TSSFrostDKGPresenceMsg, sessionID)
					comms[i].ReleaseStream(sessionID)
					done <- result{i, failure}
				}()
			}
			if scenario.selective {
				time.Sleep(time.Until(begin.Add(1200 * ms)))
				_ = coords[3].broadcast(messages.TSSFrostDKGPresenceMsg, []byte(frostKeygenPresence), peers[1:3])
			}
			reports := make(map[string]int)
			for i := 0; i < count; i++ {
				select {
				case completed := <-done:
					shouldFail := scenario.offline || (scenario.selective && completed.member == 0)
					if !shouldFail && completed.failure != nil {
						t.Errorf("honest member %d failed admission: %+v", completed.member, completed.failure)
					}
					if shouldFail {
						if completed.failure == nil || len(completed.failure.AbsentMembers) != 1 || completed.failure.AbsentMembers[0] != testPubKeys[3] {
							t.Errorf("member %d wrong actual transport attribution: %+v", completed.member, completed.failure)
							continue
						}
						id := frostKeygenBlameReportID(t, testPubKeys[:4],
							buildBlame("absence", "frost_dkg_presence", completed.failure.AbsentMembers))
						reports[id]++
					}
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
			}
			if scenario.offline || scenario.selective {
				want := 3
				if scenario.selective {
					want = 1
				}
				if len(reports) != 1 {
					t.Fatalf("reports did not converge: %v", reports)
				}
				for _, votes := range reports {
					if votes != want {
						t.Errorf("identical reports = %d, want %d", votes, want)
					}
				}
			}
		})
	}
}

// A reply is a direct announcement, never an echo of a peer's payload. Invalid
// messages and duplicates must not amplify traffic or nominate another member.
func TestFrostKeygenPresenceRepliesOncePerMember(t *testing.T) {
	ensureBech32Prefix()
	synctest.Test(t, func(t *testing.T) {
		coord := newTestFrostKeygenCoordinator(testPubKeys[0], "bounded-replies", newFrostKeygenInbox(), time.Second)
		for i := 0; i < 10; i++ {
			enqueueFrostPresence(t, coord, testPubKeys[1], coord.msgID, frostKeygenPresence)
		}
		enqueueFrostPresence(t, coord, testPubKeys[2], "wrong-session", frostKeygenPresence)
		enqueueFrostPresence(t, coord, testPubKeys[2], coord.msgID, "false accusation")
		enqueueFrostPresence(t, coord, thorchaintypes.GetRandomPubKey().String(), coord.msgID, frostKeygenPresence)
		response := coord.exchangePresence(testPubKeys[:4], nil)
		if response == nil || len(response.AbsentMembers) != 0 {
			t.Fatalf("minority or invalid traffic became evidence: %+v", response)
		}
		initial := <-coord.p2pCommunication.BroadcastMsgChan
		if initial.WrappedMessage.MessageType != messages.TSSFrostDKGPresenceMsg {
			t.Fatal("first message was not unconditional presence")
		}
		if len(coord.p2pCommunication.BroadcastMsgChan) != 1 {
			t.Fatal("expected exactly one direct reply")
		}
		reply := <-coord.p2pCommunication.BroadcastMsgChan
		sender, err := conversion.GetPeerIDFromPubKey(testPubKeys[1])
		if err != nil {
			t.Fatal(err)
		}
		if len(reply.PeersID) != 1 || reply.PeersID[0] != sender ||
			reply.WrappedMessage.MsgID != coord.msgID ||
			reply.WrappedMessage.MessageType != messages.TSSFrostDKGPresenceMsg ||
			string(reply.WrappedMessage.Payload) != frostKeygenPresence {
			t.Fatalf("unexpected direct presence reply: %+v", reply)
		}
	})
}
