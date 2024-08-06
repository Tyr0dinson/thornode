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
	c.Check(encoded, Equals, "Ldpz")

	// Test decode
	decoded, err := Base58Decode("2idJ2vUnD2uxedhPCoqHLKbBWZQdkQgDfkxzrYY9YeYPVyXLWAttBNJvcbBGGu9cDXMkkkzbVdkEsTDYJg9X1RRH")
	c.Assert(err, IsNil)
	c.Check(decoded, DeepEquals, data)

	// Test invalid base58 string
	decoded, err = Base58Decode("Ldpz!")
	c.Check(err, NotNil)
	c.Check(decoded, IsNil)
}
