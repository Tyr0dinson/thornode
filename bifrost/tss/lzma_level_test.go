package tss

import (
	"github.com/itchio/lzma"
	. "gopkg.in/check.v1"
)

// productionLzmaLevel is lzmaLevel before init lowers it.
var productionLzmaLevel = lzmaLevel

// lzma.BestCompression gives every keyshare encryption a 2^27-byte
// dictionary: about 1 GiB of match-finder tables to allocate and clear, and
// a 128 MiB window for its round-trip decrypt. The test keyshares fit whole in
// the BestSpeed dictionary (64 KiB), and the decoder reads the dictionary size
// from the stream header, so the tests run the same code with BestSpeed.
func init() {
	lzmaLevel = lzma.BestSpeed
}

type LzmaLevelSuite struct{}

var _ = Suite(&LzmaLevelSuite{})

func (LzmaLevelSuite) TestProductionLzmaLevel(c *C) {
	c.Assert(LzmaLevel, Equals, lzma.BestCompression)
	c.Assert(productionLzmaLevel, Equals, LzmaLevel)
}
