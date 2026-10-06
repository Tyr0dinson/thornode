package keysign

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
	rcmgr "github.com/libp2p/go-libp2p/p2p/host/resource-manager"
	tsslibcommon "gitlab.com/thorchain/tss/tss-lib-private.git/v3/common"

	"gitlab.com/thorchain/thornode/v3/bifrost/p2p"
	"gitlab.com/thorchain/thornode/v3/bifrost/p2p/conversion"
	"gitlab.com/thorchain/thornode/v3/bifrost/tss/go-tss/common"
)

// notifierStreamLimit stands in for go-libp2p's per-peer, per-protocol
// inbound stream budget (64+ by default) so the test exhausts it quickly.
const notifierStreamLimit = 4

func newLimitedNotifierHost(t *testing.T) host.Host {
	t.Helper()
	limits := rcmgr.PartialLimitConfig{
		ProtocolPeer: map[protocol.ID]rcmgr.ResourceLimits{
			signatureNotifierProtocol: {StreamsInbound: notifierStreamLimit},
		},
	}
	rm, err := rcmgr.NewResourceManager(rcmgr.NewFixedLimiter(limits.Build(rcmgr.InfiniteLimits)))
	if err != nil {
		t.Fatalf("resource manager: %v", err)
	}
	h, err := libp2p.New(libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"), libp2p.ResourceManager(rm))
	if err != nil {
		t.Fatalf("create host: %v", err)
	}
	t.Cleanup(func() { _ = h.Close() })
	return h
}

func notifierStreamCount(h host.Host) int {
	count := 0
	for _, conn := range h.Network().Conns() {
		for _, stream := range conn.GetStreams() {
			if stream.Protocol() == signatureNotifierProtocol {
				count++
			}
		}
	}
	return count
}

// Failed-status notifications reach peers outside the keysign party, which
// never run that keysign and so never release anything keyed by its msgID.
// Each notification stream must therefore be released once its single
// message is acked; otherwise unmatched failures exhaust the per-peer stream
// budget and block a later success notification from the same peer.
func TestSignatureNotifierUnmatchedFailuresDoNotBlockLaterSuccess(t *testing.T) {
	conversion.SetupBech32Prefix()
	p2p.ApplyDeadline.Store(false)
	t.Cleanup(func() { p2p.ApplyDeadline.Store(true) })

	receiverHost := newLimitedNotifierHost(t)
	senderHost := newLimitedNotifierHost(t)
	connectCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := senderHost.Connect(connectCtx, peer.AddrInfo{ID: receiverHost.ID(), Addrs: receiverHost.Addrs()}); err != nil {
		t.Fatalf("connect hosts: %v", err)
	}
	receiver := NewSignatureNotifier(receiverHost)
	sender := NewSignatureNotifier(senderHost)
	t.Cleanup(receiver.Stop)
	t.Cleanup(sender.Stop)

	for i := 0; i < 3*notifierStreamLimit; i++ {
		if err := sender.sendOneMsgToPeer(&signatureItem{
			messageID: fmt.Sprintf("unmatched-failed-keysign-%d", i),
			peerID:    receiverHost.ID(),
		}); err != nil {
			t.Fatalf("failed notification %d was not delivered: %v", i, err)
		}
	}

	poolPubKey := `thorpub1addwnpepq0ul3xt882a6nm6m7uhxj4tk2n82zyu647dyevcs5yumuadn4uamqx7neak`
	msg, err := base64.StdEncoding.DecodeString("yhEwrxWuNBGnPT/L7PNnVWg7gFWNzCYTV+GuX3tKRH8=")
	if err != nil {
		t.Fatal(err)
	}
	messageID, err := common.MsgToHashString(msg)
	if err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile("../test_data/signature_notify/sig1.json")
	if err != nil {
		t.Fatal(err)
	}
	var signature tsslibcommon.SignatureData
	if err = json.Unmarshal(content, &signature); err != nil {
		t.Fatal(err)
	}

	if err = sender.sendOneMsgToPeer(&signatureItem{
		messageID:     messageID,
		peerID:        receiverHost.ID(),
		signatureData: []*tsslibcommon.SignatureData{&signature},
	}); err != nil {
		t.Fatalf("success notification after unmatched failures was not delivered: %v", err)
	}
	sigs, err := receiver.WaitForSignature(messageID, [][]byte{msg}, poolPubKey, 5*time.Second, make(chan string))
	if err != nil || len(sigs) != 1 {
		t.Fatalf("success notification not processed: sigs=%d err=%v", len(sigs), err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for notifierStreamCount(receiverHost) != 0 || notifierStreamCount(senderHost) != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("notification streams retained: receiver=%d sender=%d",
				notifierStreamCount(receiverHost), notifierStreamCount(senderHost))
		}
		time.Sleep(10 * time.Millisecond)
	}
}
