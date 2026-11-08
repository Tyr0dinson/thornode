package tss

import (
	"bytes"
	"time"

	"github.com/libp2p/go-libp2p-core/peer"

	"gitlab.com/thorchain/thornode/v3/bifrost/p2p/conversion"
	"gitlab.com/thorchain/thornode/v3/bifrost/p2p/messages"
	"gitlab.com/thorchain/thornode/v3/bifrost/tss/go-tss/blame"
	tcommon "gitlab.com/thorchain/thornode/v3/bifrost/tss/go-tss/common"
)

const frostKeygenPresence = "frost-dkg-presence-v1"

// exchangePresence is a failure detector, not a vote on peer accusations.
// Every honest member broadcasts before reading anything another member can
// control. Selective DKG withholding therefore cannot make its victim silent.
// Transport authenticates the sender; the wire payload cannot nominate a key.
func (c *FrostKeygenCoordinator) exchangePresence(members []string, peers []peer.ID) *FrostKeygenResponse {
	_, _, canonical, err := buildParticipantIndex(members)
	if err != nil {
		return &FrostKeygenResponse{Status: tcommon.Fail, Blame: blame.NewBlame(err.Error(), nil)}
	}
	local := normalizePubKey(c.localPubKey)
	if _, member := canonical[local]; !member {
		return &FrostKeygenResponse{Status: tcommon.Fail, Blame: blame.NewBlame("local node is not a keygen member", nil)}
	}
	// broadcast enqueues on Communication; successful writes do not imply that
	// the receiver had subscribed yet. Its first announcement triggers a reply.
	_ = c.broadcast(messages.TSSFrostDKGPresenceMsg, []byte(frostKeygenPresence), peers)
	present := map[string]bool{local: true}
	timer := time.NewTimer(c.timeout)
	defer timer.Stop()
	for len(present) < len(canonical) {
		select {
		case msg := <-c.inbox.presence:
			if msg == nil {
				continue
			}
			wrapped, ok := parseWrappedMessage(msg.Payload)
			if !ok || wrapped.MessageType != messages.TSSFrostDKGPresenceMsg || wrapped.MsgID != c.msgID ||
				!bytes.Equal(wrapped.Payload, []byte(frostKeygenPresence)) {
				continue
			}
			pk, resolveErr := conversion.GetPubKeyFromPeerID(msg.PeerID.String())
			if resolveErr != nil {
				continue
			}
			normalized := normalizePubKey(pk)
			if _, member := canonical[normalized]; member && !present[normalized] {
				present[normalized] = true
				// Reply once, including to the final member before returning. A
				// late subscriber may have missed our initial announcement.
				// Full-close outbound streams belong to Communication and survive
				// this coordinator's inbound subscription cleanup.
				_ = c.broadcast(messages.TSSFrostDKGPresenceMsg, []byte(frostKeygenPresence), []peer.ID{msg.PeerID})
			}
		case <-timer.C:
			response := &FrostKeygenResponse{Status: tcommon.Fail, Blame: blame.NewBlame("incomplete frost keygen presence", nil)}
			// A minority partition must not even nominate a member. THORNode still
			// independently requires a quorum of identical singleton reports.
			if len(present) >= frostKeygenAdoptionQuorum(len(canonical)) {
				for _, member := range members {
					if !present[normalizePubKey(member)] {
						response.AbsentMembers = append(response.AbsentMembers, member)
					}
				}
			}
			return response
		}
	}
	return nil
}
