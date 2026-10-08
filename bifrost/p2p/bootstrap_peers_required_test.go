package p2p

import (
	"github.com/libp2p/go-libp2p-core/peer"
	"github.com/libp2p/go-libp2p-peerstore/addr"
	maddr "github.com/multiformats/go-multiaddr"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/bifrost/p2p/storage"
)

type BootstrapPeersRequiredSuite struct{}

var _ = Suite(&BootstrapPeersRequiredSuite{})

// On mainnet a node with no configured peers (seed endpoint unavailable) and no
// saved peers must fail to start rather than run isolated.
func (BootstrapPeersRequiredSuite) TestCheckBootstrapPeersAvailable(c *C) {
	defer func(prev bool) { requireBootstrapPeers = prev }(requireBootstrapPeers)
	requireBootstrapPeers = true

	fsm, err := storage.NewFileStateMgr(c.MkDir())
	c.Assert(err, IsNil)

	// no configured, no saved peers
	comm, err := NewCommunication(&Config{Port: 0, RendezvousString: "test"}, fsm, nil)
	c.Assert(err, IsNil)
	c.Assert(comm.checkBootstrapPeersAvailable(), NotNil)
	c.Assert(comm.startChannel(nil), ErrorMatches, "no TSS bootstrap peers.*")

	// config error
	comm, err = NewCommunication(&errConfig{}, fsm, nil)
	c.Assert(err, IsNil)
	c.Assert(comm.checkBootstrapPeersAvailable(), NotNil)

	// configured peers
	cfg := &Config{Port: 0, RendezvousString: "test"}
	c.Assert(cfg.BootstrapPeers.Set("/ip4/127.0.0.1/tcp/5040/p2p/16Uiu2HAm4TmEzUqy3q3Dv7HvdoSboHk5sFj2FH3npiN5vDbJC6gh"), IsNil)
	comm, err = NewCommunication(cfg, fsm, nil)
	c.Assert(err, IsNil)
	c.Assert(comm.checkBootstrapPeersAvailable(), IsNil)

	// saved peers only
	id, err := peer.Decode("16Uiu2HAm4TmEzUqy3q3Dv7HvdoSboHk5sFj2FH3npiN5vDbJC6gh")
	c.Assert(err, IsNil)
	a, err := maddr.NewMultiaddr("/ip4/1.2.3.4/tcp/5040") // loopback is not saved
	c.Assert(err, IsNil)
	c.Assert(fsm.SaveAddressBook(map[peer.ID]addr.AddrList{id: {a}}), IsNil)
	comm, err = NewCommunication(&Config{Port: 0, RendezvousString: "test"}, fsm, nil)
	c.Assert(err, IsNil)
	c.Assert(comm.checkBootstrapPeersAvailable(), IsNil)

	// not required off mainnet (e.g. a genesis node)
	requireBootstrapPeers = false
	comm, err = NewCommunication(&Config{Port: 0, RendezvousString: "test"}, nil, nil)
	c.Assert(err, IsNil)
	c.Assert(comm.checkBootstrapPeersAvailable(), IsNil)
}
