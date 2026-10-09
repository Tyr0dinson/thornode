package p2p

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/cometbft/cometbft/crypto/ed25519"
	"github.com/cometbft/cometbft/crypto/secp256k1"
	"github.com/libp2p/go-libp2p-core/peer"
	"github.com/libp2p/go-libp2p-core/peerstore"
	"github.com/stretchr/testify/require"

	"gitlab.com/thorchain/thornode/v3/bifrost/p2p/messages"
)

func TestDecodeTSSFrameHeaderErrorsCov(t *testing.T) {
	cases := []struct {
		name    string
		prefix  string
		wantErr string
	}{
		{"empty", ``, "invalid TSS frame header"},
		{"not object", `[1]`, "TSS frame is not a JSON object"},
		{"truncated", `{"message_type":1`, "TSS frame header exceeds bounded prefix"},
		{"incomplete", `{"message_type":1}`, "TSS frame header is incomplete"},
		{"duplicate type", `{"message_type":1,"message_type":2`, "duplicate TSS message_type header"},
		{"bad type", `{"message_type":"x"}`, "invalid TSS message_type header"},
		{"duplicate id", `{"message_id":"a","message_id":"b"`, "duplicate TSS message_id header"},
		{"bad id type", `{"message_id":5}`, "invalid TSS message_id header"},
		{"empty id", `{"message_id":""}`, "invalid TSS message_id header"},
		{"payload first", `{"payload":"eA=="}`, "TSS payload appeared before complete session header"},
		{"unknown field", `{"other":1}`, "unexpected TSS frame header field"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := decodeTSSFrameHeader([]byte(tc.prefix))
			require.Error(t, err)
			require.Contains(t, err.Error(), tc.wantErr)
		})
	}

	header, err := decodeTSSFrameHeader([]byte(`{"message_id":"abc","message_type":7,"payload":`))
	require.NoError(t, err)
	require.Equal(t, "abc", header.MsgID)
	require.Equal(t, messages.THORChainTSSMessageType(7), header.MessageType)

	require.True(t, validTSSFrameMsgID(strings.Repeat("a", maxTSSFrameMsgIDBytes)))
	require.False(t, validTSSFrameMsgID(strings.Repeat("a", maxTSSFrameMsgIDBytes+1)))
	require.False(t, validTSSFrameMsgID("has space"))
}

func TestStartP2PCov(t *testing.T) {
	t.Setenv("SIGNER_SEED_PHRASE", "")
	t.Setenv("SIGNER_SEED_PHRASE_FILE", "")

	// wrong private key type is rejected before the host starts
	_, _, err := StartP2P(&Config{Port: 0, RendezvousString: "cov"}, ed25519.GenPrivKey(), t.TempDir())
	require.ErrorContains(t, err, "fail to get private key")

	// invalid external address fails while building the communication layer
	_, _, err = StartP2P(&Config{Port: 0, RendezvousString: "cov", ExternalIP: "not-an-ip"}, secp256k1.GenPrivKey(), t.TempDir())
	require.ErrorContains(t, err, "fail to create communication layer")

	// a seed phrase enables at-rest encryption for the state manager
	t.Setenv("SIGNER_SEED_PHRASE", "abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon about")
	comm, stateMgr, err := StartP2P(&Config{Port: 0, RendezvousString: "cov"}, secp256k1.GenPrivKey(), t.TempDir())
	require.NoError(t, err)
	require.NotNil(t, stateMgr)
	require.NotEmpty(t, comm.GetLocalPeerID())
	require.NotEmpty(t, comm.GetHost().Addrs())
	require.NoError(t, comm.Stop())
}

func TestCommunicationStartFailureCleansUpCov(t *testing.T) {
	bridge := NewMockThorchainBridge()
	comm, err := NewCommunication(&Config{Port: 0, RendezvousString: "cov"}, nil, bridge)
	require.NoError(t, err)
	require.NotNil(t, comm.nodeGater)
	require.Error(t, comm.Start([]byte("not a secp256k1 key")))
	require.Nil(t, comm.host)
	require.NoError(t, comm.Stop())
}

func TestProcessBroadcastDeliversToPeerCov(t *testing.T) {
	t.Setenv("SIGNER_SEED_PHRASE", "")
	t.Setenv("SIGNER_SEED_PHRASE_FILE", "")
	sender, _, err := StartP2P(&Config{Port: 0, RendezvousString: "cov"}, secp256k1.GenPrivKey(), t.TempDir())
	require.NoError(t, err)
	defer func() { require.NoError(t, sender.Stop()) }()
	receiver, _, err := StartP2P(&Config{Port: 0, RendezvousString: "cov"}, secp256k1.GenPrivKey(), t.TempDir())
	require.NoError(t, err)
	defer func() { require.NoError(t, receiver.Stop()) }()

	sender.GetHost().Peerstore().AddAddrs(receiver.GetHost().ID(), receiver.GetHost().Addrs(), peerstore.PermanentAddrTTL)

	ch := make(chan *Message, 1)
	receiver.SetSubscribe(messages.TSSKeyGenMsg, "cov-msg", ch)
	defer receiver.CancelSubscribe(messages.TSSKeyGenMsg, "cov-msg")

	wrapped := messages.WrappedMessage{
		MessageType: messages.TSSKeyGenMsg,
		MsgID:       "cov-msg",
		Payload:     []byte("hello"),
	}
	sender.BroadcastMsgChan <- &messages.BroadcastMsgChan{
		WrappedMessage: wrapped,
		PeersID:        []peer.ID{receiver.GetHost().ID()},
	}

	select {
	case msg := <-ch:
		require.Equal(t, sender.GetHost().ID(), msg.PeerID)
		var got messages.WrappedMessage
		require.NoError(t, json.Unmarshal(msg.Payload, &got))
		require.Equal(t, wrapped, got)
	case <-time.After(10 * time.Second):
		t.Fatal("broadcast message was not delivered")
	}
}
