package p2p

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"net"
	"os"
	"path/filepath"

	"github.com/libp2p/go-libp2p-core/peer"
	maddr "github.com/multiformats/go-multiaddr"

	"github.com/libp2p/go-libp2p-core/crypto"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/bifrost/p2p/messages"
	"gitlab.com/thorchain/thornode/v3/bifrost/p2p/storage"
)

type CommunicationTestSuite struct{}

var _ = Suite(&CommunicationTestSuite{})

func (CommunicationTestSuite) TestBasicCommunication(c *C) {
	comm, err := NewCommunication(&Config{Port: 6688, RendezvousString: "rendezvous"}, nil, nil)
	c.Assert(err, IsNil)
	c.Assert(comm, NotNil)
	comm.SetSubscribe(messages.TSSKeyGenMsg, "hello", make(chan *Message))
	c.Assert(comm.getSubscriber(messages.TSSKeySignMsg, "hello"), IsNil)
	c.Assert(comm.getSubscriber(messages.TSSKeyGenMsg, "hello"), NotNil)
	comm.CancelSubscribe(messages.TSSKeyGenMsg, "hello")
	comm.CancelSubscribe(messages.TSSKeyGenMsg, "whatever")
	comm.CancelSubscribe(messages.TSSKeySignMsg, "asdsdf")
}

func checkExist(a []maddr.Multiaddr, b string) bool {
	for _, el := range a {
		if el.String() == b {
			return true
		}
	}
	return false
}

func testSecp256k1RawKey(c *C) []byte {
	sk, _, err := crypto.GenerateSecp256k1Key(rand.Reader)
	c.Assert(err, IsNil)
	raw, err := sk.Raw()
	c.Assert(err, IsNil)
	return raw
}

func testFreeTCPPort(c *C) int {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	c.Assert(err, IsNil)
	defer func() {
		c.Assert(listener.Close(), IsNil)
	}()
	tcpAddr, ok := listener.Addr().(*net.TCPAddr)
	c.Assert(ok, Equals, true)
	return tcpAddr.Port
}

func (CommunicationTestSuite) TestEstablishP2pCommunication(c *C) {
	bootstrapPort := testFreeTCPPort(c)
	comm2Port := testFreeTCPPort(c)
	comm3Port := testFreeTCPPort(c)
	comm4Port := testFreeTCPPort(c)
	invalidPort := testFreeTCPPort(c)
	bootstrapPeer := fmt.Sprintf("/ip4/127.0.0.1/tcp/%d/p2p/16Uiu2HAm4TmEzUqy3q3Dv7HvdoSboHk5sFj2FH3npiN5vDbJC6gh", bootstrapPort)
	bootstrapPrivKey := "6LABmWB4iXqkqOJ9H0YFEA2CSSx6bA7XAKGyI/TDtas="
	fakeExternalIP := "11.22.33.44"
	fakeExternalMultiAddr := fmt.Sprintf("/ip4/11.22.33.44/tcp/%d", bootstrapPort)
	validMultiAddr, err := maddr.NewMultiaddr(bootstrapPeer)
	c.Assert(err, IsNil)
	privKey, err := base64.StdEncoding.DecodeString(bootstrapPrivKey)
	c.Assert(err, IsNil)
	comm, err := NewCommunication(&Config{Port: bootstrapPort, RendezvousString: "commTest", ExternalIP: fakeExternalIP}, nil, nil)
	c.Assert(err, IsNil)
	c.Assert(comm.Start(privKey), IsNil)

	defer func() {
		stopErr := comm.Stop()
		c.Assert(stopErr, IsNil)
	}()
	comm2, err := NewCommunication(&Config{Port: comm2Port, RendezvousString: "commTest", BootstrapPeers: []maddr.Multiaddr{validMultiAddr}}, nil, nil)
	c.Assert(err, IsNil)
	err = comm2.Start(testSecp256k1RawKey(c))
	c.Assert(err, IsNil)
	defer func() {
		stopErr := comm2.Stop()
		c.Assert(stopErr, IsNil)
	}()

	// we connect to an invalid peer and see
	sk2, _, err := crypto.GenerateSecp256k1Key(rand.Reader)
	c.Assert(err, IsNil)
	id, err := peer.IDFromPrivateKey(sk2)
	c.Assert(err, IsNil)
	invalidAddr := fmt.Sprintf("/ip4/127.0.0.1/tcp/%d/p2p/%s", invalidPort, id.String())
	invalidMultiAddr, err := maddr.NewMultiaddr(invalidAddr)
	c.Assert(err, IsNil)
	comm3, err := NewCommunication(&Config{Port: comm3Port, RendezvousString: "commTest", BootstrapPeers: []maddr.Multiaddr{invalidMultiAddr}}, nil, nil)
	c.Assert(err, IsNil)
	err = comm3.Start(testSecp256k1RawKey(c))
	c.Assert(err, ErrorMatches, "fail to connect to bootstrap peer: fail to connect to any peer")

	// we connect to one invalid and one valid address
	comm4, err := NewCommunication(&Config{Port: comm4Port, RendezvousString: "commTest", BootstrapPeers: []maddr.Multiaddr{invalidMultiAddr, validMultiAddr}}, nil, nil)
	c.Assert(err, IsNil)
	err = comm4.Start(testSecp256k1RawKey(c))
	c.Assert(err, IsNil)
	defer func() {
		err := comm4.Stop()
		c.Assert(err, IsNil)
	}()

	// we add test for external ip advertising
	c.Assert(checkExist(comm.host.Addrs(), fakeExternalMultiAddr), Equals, true)
	ps := comm2.host.Peerstore()
	c.Assert(checkExist(ps.Addrs(comm.host.ID()), fakeExternalMultiAddr), Equals, true)
	ps = comm4.host.Peerstore()
	c.Assert(checkExist(ps.Addrs(comm.host.ID()), fakeExternalMultiAddr), Equals, true)
}

func (CommunicationTestSuite) TestBootstrapConnectivityRequiredOnlyForConfiguredPeers(c *C) {
	invalidPeer := "/ip4/127.0.0.1/tcp/1/p2p/16Uiu2HAm4TmEzUqy3q3Dv7HvdoSboHk5sFj2FH3npiN5vDbJC6gh"
	invalidMultiAddr, err := maddr.NewMultiaddr(invalidPeer)
	c.Assert(err, IsNil)

	commWithConfig, err := NewCommunication(&Config{Port: 3330, RendezvousString: "commConfig", BootstrapPeers: []maddr.Multiaddr{invalidMultiAddr}}, nil, nil)
	c.Assert(err, IsNil)
	c.Assert(commWithConfig.bootstrapConnectivityRequired(), Equals, true)

	dir := c.MkDir()
	err = os.WriteFile(filepath.Join(dir, "address_book.seed"), []byte(invalidPeer+"\n"), 0o600)
	c.Assert(err, IsNil)
	stateMgr, err := storage.NewFileStateMgr(dir)
	c.Assert(err, IsNil)
	commSavedOnly, err := NewCommunication(&Config{Port: 3331, RendezvousString: "commSavedOnly"}, stateMgr, nil)
	c.Assert(err, IsNil)
	c.Assert(commSavedOnly.bootstrapConnectivityRequired(), Equals, false)
}
