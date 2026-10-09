//go:build !mocknet
// +build !mocknet

package utxo

import (
	"errors"
	"fmt"

	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	"gitlab.com/thorchain/thornode/v3/constants"
)

// mimirRefBridge answers GetMimirWithRef from a fixed key->value table.
type mimirRefBridge struct {
	thorclient.ThorchainBridge
	values map[string]int64
	err    error
	asked  []string
}

func (b *mimirRefBridge) GetMimirWithRef(template, ref string) (int64, error) {
	key := fmt.Sprintf(template, ref)
	b.asked = append(b.asked, key)
	if b.err != nil {
		return 0, b.err
	}
	if v, ok := b.values[key]; ok {
		return v, nil
	}
	return -1, nil
}

type ConfAdjustmentMainnetCovSuite struct{}

var _ = Suite(&ConfAdjustmentMainnetCovSuite{})

func (s *ConfAdjustmentMainnetCovSuite) TestGetConfMulBasisPoint(c *C) {
	// mimir set -> used verbatim
	bridge := &mimirRefBridge{values: map[string]int64{"ConfMultiplierBasisPoints-BTC": 5000}}
	v, err := GetConfMulBasisPoint("BTC", bridge)
	c.Assert(err, IsNil)
	c.Assert(v.Uint64(), Equals, uint64(5000))
	c.Assert(bridge.asked, DeepEquals, []string{"ConfMultiplierBasisPoints-BTC"})

	// unset (-1) or zero -> defaults to 100%
	for _, val := range []int64{-1, 0} {
		bridge = &mimirRefBridge{values: map[string]int64{"ConfMultiplierBasisPoints-LTC": val}}
		v, err = GetConfMulBasisPoint("LTC", bridge)
		c.Assert(err, IsNil)
		c.Assert(v.Uint64(), Equals, constants.MaxBasisPts)
	}

	// bridge error -> default value plus the error
	bridge = &mimirRefBridge{err: errors.New("boom")}
	v, err = GetConfMulBasisPoint("DOGE", bridge)
	c.Assert(err, ErrorMatches, "boom")
	c.Assert(v.Uint64(), Equals, constants.MaxBasisPts)
}

func (s *ConfAdjustmentMainnetCovSuite) TestMaxConfAdjustment(c *C) {
	bridge := &mimirRefBridge{values: map[string]int64{"MaxConfirmations-BTC": 6}}
	// capped
	v, err := MaxConfAdjustment(10, "BTC", bridge)
	c.Assert(err, IsNil)
	c.Assert(v, Equals, uint64(6))
	c.Assert(bridge.asked, DeepEquals, []string{"MaxConfirmations-BTC"})
	// below the cap -> unchanged
	v, err = MaxConfAdjustment(3, "BTC", bridge)
	c.Assert(err, IsNil)
	c.Assert(v, Equals, uint64(3))
	// equal to the cap -> unchanged
	v, err = MaxConfAdjustment(6, "BTC", bridge)
	c.Assert(err, IsNil)
	c.Assert(v, Equals, uint64(6))

	// unset / zero cap -> unchanged
	for _, val := range []int64{-1, 0} {
		bridge = &mimirRefBridge{values: map[string]int64{"MaxConfirmations-LTC": val}}
		v, err = MaxConfAdjustment(100, "LTC", bridge)
		c.Assert(err, IsNil)
		c.Assert(v, Equals, uint64(100))
	}

	// bridge error -> original confirmations plus the error
	bridge = &mimirRefBridge{err: errors.New("boom")}
	v, err = MaxConfAdjustment(9, "DOGE", bridge)
	c.Assert(err, ErrorMatches, "boom")
	c.Assert(v, Equals, uint64(9))
}
