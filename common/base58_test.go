package common

import (
	. "gopkg.in/check.v1"
)

type Base58Suite struct{}

var _ = Suite(&Base58Suite{})

func (s Base58Suite) TestBase58(c *C) {
	// Test encode
	data := []byte{0x01, 0x02, 0x03, 0x04, 0x05}
	encoded := Base58Encode(data)
	c.Check(encoded, Equals, "7bWpTW")

	// Test decode
	decoded, err := Base58Decode("7bWpTW")
	c.Assert(err, IsNil)
	c.Check(decoded, DeepEquals, data)

	// Test invalid base58 string
	decoded, err = Base58Decode("Ldpz!")
	c.Check(err, NotNil)
	c.Check(decoded, IsNil)

	// SOL Addr
	decoded, err = Base58Decode("FXazoWBBd7GXm6xYhLSxE3GtYPpZs6PCM7t6KTqr1QY")
	c.Assert(err, IsNil)
	c.Check(len(decoded), Equals, 32)
}
