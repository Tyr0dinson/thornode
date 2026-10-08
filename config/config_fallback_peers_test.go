package config

import (
	"os"
	"path/filepath"

	. "gopkg.in/check.v1"
)

// Without seeds a node needs persistent peers or a non-empty address book;
// otherwise it would start with nothing to dial.
func (Test) TestRequireFallbackPeers(c *C) {
	defer func(prev string) { config.Thornode.Tendermint.P2P.PersistentPeers = prev }(config.Thornode.Tendermint.P2P.PersistentPeers)
	config.Thornode.Tendermint.P2P.PersistentPeers = ""

	home := c.MkDir()
	c.Assert(os.MkdirAll(filepath.Join(home, "config"), 0o755), IsNil)
	book := filepath.Join(home, "config", "addrbook.json")

	// existing node state, no address book
	c.Check(requireFallbackPeers(home), NotNil)

	// empty address book
	c.Assert(os.WriteFile(book, []byte(`{"key":"abc","addrs":[]}`), 0o600), IsNil)
	c.Check(requireFallbackPeers(home), NotNil)

	// unreadable address book
	c.Assert(os.WriteFile(book, []byte(`{`), 0o600), IsNil)
	c.Check(requireFallbackPeers(home), NotNil)

	// address book with peers
	c.Assert(os.WriteFile(book, []byte(`{"key":"abc","addrs":[{"addr":{"id":"x","ip":"1.2.3.4","port":27146}}]}`), 0o600), IsNil)
	c.Check(requireFallbackPeers(home), IsNil)

	// persistent peers, no address book
	c.Assert(os.Remove(book), IsNil)
	config.Thornode.Tendermint.P2P.PersistentPeers = "id@1.2.3.4:27146"
	c.Check(requireFallbackPeers(home), IsNil)
}
