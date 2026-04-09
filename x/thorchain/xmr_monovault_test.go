package thorchain

import (
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
)

type XMRMonovaultSuite struct{}

var _ = Suite(&XMRMonovaultSuite{})

func (s *XMRMonovaultSuite) TestShouldMaintainXMRMonovaultSingleValidator(c *C) {
	c.Assert(shouldMaintainXMRMonovault(nil, 1), Equals, true)
}

func (s *XMRMonovaultSuite) TestSelectActiveVaultForChainPrefersRegularTHORVaults(c *C) {
	ctx, k := setupKeeperForTest(c)
	regular := NewVault(1, ActiveVault, AsgardVault, GetRandomPubKey(), common.Chains{common.THORChain}.Strings(), nil)
	xmr := NewVaultV2(1, ActiveVault, AsgardVault, GetRandomPubKey(), xmrMonovaultChains().Strings(), nil, GetRandomPubKey())
	xmr.PubKeyFrost = GetRandomPubKey()

	selected, err := selectActiveVaultForChain(ctx, k, Vaults{xmr, regular}, common.THORChain, 0)
	c.Assert(err, IsNil)
	c.Assert(selected.PubKey.Equals(regular.PubKey), Equals, true)
}
