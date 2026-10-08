//go:build mocknet
// +build mocknet

package config

import (
	. "gopkg.in/check.v1"
)

// -------------------------------------------------------------------------------------
// getSeedAddrs (mocknet)
// -------------------------------------------------------------------------------------

func (Test) TestGetSeedAddrsMocknet(c *C) {
	result, err := getSeedAddrs()
	c.Assert(err, IsNil)
	c.Assert(result, HasLen, 0)
}

// -------------------------------------------------------------------------------------
// assertBifrostHasSeeds (mocknet)
// -------------------------------------------------------------------------------------

func (Test) TestAssertBifrostHasSeedsMocknet(c *C) {
	// In mocknet, assertBifrostHasSeeds checks for a seed file.
	// It should not panic regardless of whether the file exists.
	assertBifrostHasSeeds()
}
