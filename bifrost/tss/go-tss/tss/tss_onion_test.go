package tss

import (
	"strings"
	"testing"

	maddr "github.com/multiformats/go-multiaddr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPeerAddressFromMultiaddr(t *testing.T) {
	ipAddr, err := maddr.NewMultiaddr("/ip4/1.2.3.4/tcp/5040")
	require.NoError(t, err)
	assert.Equal(t, "1.2.3.4", peerAddressFromMultiaddr(ipAddr))

	label := strings.Repeat("a", 56)
	onionAddr, err := maddr.NewMultiaddr("/onion3/" + label + ":5040/p2p/16Uiu2HAm4TmEzUqy3q3Dv7HvdoSboHk5sFj2FH3npiN5vDbJC6gh")
	require.NoError(t, err)
	assert.Equal(t, label+".onion", peerAddressFromMultiaddr(onionAddr))

	peerOnlyAddr, err := maddr.NewMultiaddr("/p2p/16Uiu2HAm4TmEzUqy3q3Dv7HvdoSboHk5sFj2FH3npiN5vDbJC6gh")
	require.NoError(t, err)
	assert.Equal(t, "", peerAddressFromMultiaddr(peerOnlyAddr))
}

func TestPeerAddressFromMultiaddrs(t *testing.T) {
	label := strings.Repeat("a", 56)
	ipAddr, err := maddr.NewMultiaddr("/ip4/1.2.3.4/tcp/5040")
	require.NoError(t, err)
	onionAddr, err := maddr.NewMultiaddr("/onion3/" + label + ":5040/p2p/16Uiu2HAm4TmEzUqy3q3Dv7HvdoSboHk5sFj2FH3npiN5vDbJC6gh")
	require.NoError(t, err)
	peerOnlyAddr, err := maddr.NewMultiaddr("/p2p/16Uiu2HAm4TmEzUqy3q3Dv7HvdoSboHk5sFj2FH3npiN5vDbJC6gh")
	require.NoError(t, err)

	assert.Equal(t, "1.2.3.4", peerAddressFromMultiaddrs([]maddr.Multiaddr{ipAddr}))
	assert.Equal(t, label+".onion", peerAddressFromMultiaddrs([]maddr.Multiaddr{onionAddr}))
	assert.Equal(t, "1.2.3.4", peerAddressFromMultiaddrs([]maddr.Multiaddr{onionAddr, ipAddr}))
	assert.Equal(t, label+".onion", peerAddressFromMultiaddrs([]maddr.Multiaddr{peerOnlyAddr, onionAddr}))
	assert.Equal(t, "", peerAddressFromMultiaddrs([]maddr.Multiaddr{peerOnlyAddr}))
}
