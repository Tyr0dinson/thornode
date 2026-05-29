package p2p

import (
	"context"
	"strings"
	"testing"

	tptu "github.com/libp2p/go-libp2p-transport-upgrader"
	maddr "github.com/multiformats/go-multiaddr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewOnionTransport(t *testing.T) {
	transport, err := newOnionTransport("127.0.0.1:9050", &tptu.Upgrader{})
	require.NoError(t, err)
	assert.NotNil(t, transport)

	_, err = newOnionTransport("not-an-addr", &tptu.Upgrader{})
	assert.Error(t, err)

	_, err = newOnionTransport("127.0.0.1:9050", nil)
	assert.Error(t, err)
}

func TestOnionTransportCanDial(t *testing.T) {
	label := strings.Repeat("a", 56)
	validAddr, err := maddr.NewMultiaddr("/onion3/" + label + ":5040/p2p/16Uiu2HAm4TmEzUqy3q3Dv7HvdoSboHk5sFj2FH3npiN5vDbJC6gh")
	require.NoError(t, err)
	ipAddr, err := maddr.NewMultiaddr("/ip4/127.0.0.1/tcp/5040")
	require.NoError(t, err)

	transport := &onionTransport{}
	assert.True(t, transport.CanDial(validAddr))
	assert.False(t, transport.CanDial(ipAddr))

	target, err := onionDialTarget(validAddr)
	require.NoError(t, err)
	assert.Equal(t, label+".onion:5040", target)
}

func TestOnionTransportRejectsInvalidAddresses(t *testing.T) {
	_, err := maddr.NewMultiaddr("/onion3/" + strings.Repeat("a", 55) + ":5040")
	assert.Error(t, err)
	_, err = maddr.NewMultiaddr("/onion3/" + strings.Repeat("a", 56) + ":0")
	assert.Error(t, err)
	wrongSyntaxAddr := "/onion3/" + strings.Repeat("a", 56) + "/tcp/5040/p2p/16Uiu2HAm4TmEzUqy3q3Dv7HvdoSboHk5sFj2FH3npiN5vDbJC6gh"

	_, err = maddr.NewMultiaddr(wrongSyntaxAddr)
	assert.Error(t, err)

	ipAddr, err := maddr.NewMultiaddr("/ip4/127.0.0.1/tcp/5040")
	require.NoError(t, err)
	transport := &onionTransport{}
	_, err = transport.Dial(context.Background(), ipAddr, "")
	assert.Error(t, err)
}

func TestOnionTransportListen(t *testing.T) {
	transport := &onionTransport{}
	listener, err := transport.Listen(nil)
	assert.Nil(t, listener)
	assert.Error(t, err)
	assert.Equal(t, []int{maddr.P_ONION3}, transport.Protocols())
	assert.True(t, transport.Proxy())
}
