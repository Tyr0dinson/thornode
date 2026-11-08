package tss

import (
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"gitlab.com/thorchain/thornode/v3/bifrost/p2p"
	tcommon "gitlab.com/thorchain/thornode/v3/bifrost/tss/go-tss/common"
)

// Exercise production subscription, presence, leader join, all transcript
// rounds and cleanup together. Only the sidecar's cryptographic work is mocked.
func TestFrostKeygenStaggeredAdmission(t *testing.T) {
	for _, sidecarDown := range []bool{false, true} {
		name := "successful DKG"
		if sidecarDown {
			name = "sidecar failure remains diagnostic"
		}
		t.Run(name, func(t *testing.T) {
			comms, _ := newFrostPresenceTestNetwork(t)
			members := testPubKeys[:4]
			_, indexes, _, err := buildParticipantIndex(members)
			if err != nil {
				t.Fatal(err)
			}
			req := FrostKeygenRequest{
				VaultPubKey: "thorpub-staggered-admission", SignerPubKeys: members,
				BlockHeight: 100, ExpectedAddr: "48xmrstaggered",
			}
			type result struct {
				member   int
				response FrostKeygenResponse
				err      error
			}
			done := make(chan result, len(members))
			sidecars := make([]*mockFrostSidecarKeygen, len(members))
			begin := time.Now()
			for i, comm := range comms {
				party := p2p.NewPartyCoordinator(comm.GetHost(), 5*time.Second)
				t.Cleanup(party.Stop)
				server := &TssServer{
					p2pCommunication: comm, partyCoordinator: party, localNodePubKey: members[i],
					conf: tcommon.TssConfig{FrostKeygenTimeout: minFrostKeygenTimeout},
				}
				shares := make(map[string]string)
				for j, member := range members {
					if j != i {
						shares[strconv.FormatUint(uint64(indexes[normalizePubKey(member)]), 10)] = "encrypted-share"
					}
				}
				sidecar := &mockFrostSidecarKeygen{
					round1Resp:   FrostSidecarDKGRound1Resp{Commitments: "commitment"},
					round2Resp:   FrostSidecarDKGRound2Resp{EncryptedShares: shares},
					round3Resp:   FrostSidecarDKGRound3Resp{Address: req.ExpectedAddr, GroupKey: strings.Repeat("ab", 32)},
					finalizeResp: FrostSidecarDKGFinalizeResp{Success: true, KeyBytesHex: "abcd"},
					chainHeight:  123,
				}
				if sidecarDown && i == 3 {
					sidecar.round1Err = errors.New("sidecar unavailable")
				}
				sidecars[i] = sidecar
				go func() {
					time.Sleep(time.Until(begin.Add(time.Duration(i) * 400 * time.Millisecond)))
					response, keygenErr := server.FrostKeygen(req, sidecar)
					done <- result{i, response, keygenErr}
				}()
			}
			// The production constructor deliberately floors round timeouts at
			// 60s. A sidecar outage takes one round plus result-adoption wait.
			timer := time.NewTimer(2*minFrostKeygenTimeout + 15*time.Second)
			defer timer.Stop()
			for range members {
				select {
				case completed := <-done:
					resp := completed.response
					if completed.err != nil || len(resp.AbsentMembers) != 0 {
						t.Errorf("member %d admission became an accusation: err=%v resp=%+v", completed.member, completed.err, resp)
					}
					sidecar := sidecars[completed.member]
					if len(sidecar.round1Reqs) != 1 {
						t.Errorf("member %d never passed admission and leader join", completed.member)
					}
					if sidecarDown {
						if resp.Status != tcommon.Fail || len(resp.Blame.BlameNodes) != 0 {
							t.Errorf("sidecar failure must stay diagnostic: %+v", resp)
						}
					} else if resp.Status != tcommon.Success || resp.Address != req.ExpectedAddr ||
						resp.GroupKey != strings.Repeat("ab", 32) || len(sidecar.finalizeReqs) != 1 {
						t.Errorf("member %d failed DKG after staggered startup: %+v", completed.member, resp)
					}
				case <-timer.C:
					t.Fatal("staggered keygen did not finish")
				}
			}
		})
	}
}
