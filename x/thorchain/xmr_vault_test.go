package thorchain

import (
	cometed25519 "github.com/cometbft/cometbft/crypto/ed25519"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
)

type XMRVaultSuite struct{}

var _ = Suite(&XMRVaultSuite{})

func getRandomXMRFrostPubKey(c *C) common.PubKey {
	pubKey, err := common.NewPubKeyFromCrypto(cometed25519.GenPrivKey().PubKey())
	c.Assert(err, IsNil)
	_, err = pubKey.GetAddress(common.XMRChain)
	c.Assert(err, IsNil)
	return pubKey
}

func (s *XMRVaultSuite) TestSelectActiveVaultForChainAcceptsXMRCapableAsgard(c *C) {
	ctx, k := setupKeeperForTest(c)
	xmr := NewVaultV2(1, ActiveVault, AsgardVault, GetRandomPubKey(), common.Chains{common.THORChain, common.XMRChain, common.BTCChain}.Strings(), nil, GetRandomPubKey())
	xmr.PubKeyFrost = getRandomXMRFrostPubKey(c)

	selected, err := selectActiveVaultForChain(ctx, k, Vaults{xmr}, common.XMRChain, 0)
	c.Assert(err, IsNil)
	c.Assert(selected.PubKey.Equals(xmr.PubKey), Equals, true)
}

func (s *XMRVaultSuite) TestPreferredActiveVaultsForChainAcceptsXMRCapableVault(c *C) {
	hybrid := NewVault(1, ActiveVault, AsgardVault, GetRandomPubKey(), common.Chains{common.THORChain, common.XMRChain, common.BTCChain}.Strings(), nil)
	hybrid.PubKeyFrost = getRandomXMRFrostPubKey(c)

	filtered := preferredActiveVaultsForChain(Vaults{hybrid}, common.XMRChain)
	c.Assert(filtered, HasLen, 1)
	c.Assert(filtered[0].PubKey.Equals(hybrid.PubKey), Equals, true)
}

func (s *XMRVaultSuite) TestPreferredActiveVaultsForChainRejectsXMRVaultWithoutFrost(c *C) {
	hybrid := NewVault(1, ActiveVault, AsgardVault, GetRandomPubKey(), common.Chains{common.THORChain, common.XMRChain, common.BTCChain}.Strings(), nil)
	regular := NewVault(1, ActiveVault, AsgardVault, GetRandomPubKey(), common.Chains{common.THORChain, common.BTCChain}.Strings(), nil)

	filtered := preferredActiveVaultsForChain(Vaults{regular, hybrid}, common.XMRChain)
	c.Assert(filtered, HasLen, 0)
}

func (s *XMRVaultSuite) TestFilterActiveVaultsForOutboundChainRejectsXMRVaultWithoutFrost(c *C) {
	hybrid := NewVault(1, ActiveVault, AsgardVault, GetRandomPubKey(), common.Chains{common.THORChain, common.XMRChain, common.BTCChain}.Strings(), nil)
	regular := NewVault(1, ActiveVault, AsgardVault, GetRandomPubKey(), common.Chains{common.THORChain, common.BTCChain}.Strings(), nil)

	filtered := filterActiveVaultsForOutboundChain(Vaults{regular, hybrid}, common.XMRChain)
	c.Assert(filtered, HasLen, 0)
}

func (s *XMRVaultSuite) TestFilterActiveVaultsForOutboundChainKeepsXMRCapableVaultForOtherChains(c *C) {
	hybrid := NewVault(1, ActiveVault, AsgardVault, GetRandomPubKey(), common.Chains{common.THORChain, common.XMRChain, common.BTCChain}.Strings(), nil)
	hybrid.PubKeyFrost = getRandomXMRFrostPubKey(c)

	filtered := filterActiveVaultsForOutboundChain(Vaults{hybrid}, common.BTCChain)
	c.Assert(filtered, HasLen, 1)
	c.Assert(filtered[0].PubKey.Equals(hybrid.PubKey), Equals, true)
}
