package thorchain

import (
	"errors"

	cometed25519 "github.com/cometbft/cometbft/crypto/ed25519"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"
)

type XMRMonovaultSuite struct{}

var _ = Suite(&XMRMonovaultSuite{})

type failingXMRHaltKeeper struct {
	keeper.Keeper
}

func (k failingXMRHaltKeeper) GetMimir(ctx cosmos.Context, key string) (int64, error) {
	if key == constants.MimirKeyHaltMonovaultKeygen {
		return 0, errors.New("mimir read failed")
	}
	return k.Keeper.GetMimir(ctx, key)
}

func getRandomXMRFrostPubKey(c *C) common.PubKey {
	pubKey, err := common.NewPubKeyFromCrypto(cometed25519.GenPrivKey().PubKey())
	c.Assert(err, IsNil)
	_, err = pubKey.GetAddress(common.XMRChain)
	c.Assert(err, IsNil)
	return pubKey
}

func (s *XMRMonovaultSuite) TestXMRMonovaultKeygenHaltActivation(c *C) {
	ctx, k := setupKeeperForTest(c)
	ctx = ctx.WithBlockHeight(100)
	tests := []struct {
		value  int64
		halted bool
	}{
		{value: -1, halted: false},
		{value: 0, halted: false},
		{value: 99, halted: true},
		{value: 100, halted: true},
		{value: 101, halted: false},
	}
	for _, test := range tests {
		k.SetMimir(ctx, constants.MimirKeyHaltMonovaultKeygen, test.value)
		halted, err := isXMRMonovaultKeygenHalted(ctx, k)
		c.Check(err, IsNil)
		c.Check(halted, Equals, test.halted, Commentf("Mimir value %d", test.value))
	}
}

func (s *XMRMonovaultSuite) TestXMRMonovaultKeygenHaltFailsClosed(c *C) {
	ctx, k := setupKeeperForTest(c)
	failing := failingXMRHaltKeeper{Keeper: k}
	halted, err := isXMRMonovaultKeygenHalted(ctx, failing)
	c.Assert(err, NotNil)
	c.Assert(halted, Equals, true)

	node := GetRandomValidatorNode(NodeActive)
	c.Assert(k.SetNodeAccount(ctx, node), IsNil)
	networkMgr := &NetworkMgr{k: failing}
	keygenHeight := ctx.BlockHeight() + 1
	c.Assert(networkMgr.scheduleKeygen(ctx, NodeAccounts{node}, XMRMonovaultKeygen, keygenHeight), IsNil)
	block, err := k.GetKeygenBlock(ctx, keygenHeight)
	c.Assert(err, IsNil)
	c.Assert(block.Keygens, HasLen, 0)
}

func (s *XMRMonovaultSuite) TestShouldMaintainXMRMonovaultSingleValidator(c *C) {
	c.Assert(shouldMaintainXMRMonovault(nil, 1), Equals, true)
}

func (s *XMRMonovaultSuite) TestSelectActiveVaultForChainPrefersRegularTHORVaults(c *C) {
	ctx, k := setupKeeperForTest(c)
	regular := NewVault(1, ActiveVault, AsgardVault, GetRandomPubKey(), common.Chains{common.THORChain}.Strings(), nil)
	xmr := NewVaultV2(1, ActiveVault, AsgardVault, GetRandomPubKey(), xmrMonovaultChains().Strings(), nil, GetRandomPubKey())
	xmr.PubKeyFrost = getRandomXMRFrostPubKey(c)

	selected, err := selectActiveVaultForChain(ctx, k, Vaults{xmr, regular}, common.THORChain, 0)
	c.Assert(err, IsNil)
	c.Assert(selected.PubKey.Equals(regular.PubKey), Equals, true)
}

func (s *XMRMonovaultSuite) TestPreferredActiveVaultsForChainAcceptsXMRCapableVault(c *C) {
	hybrid := NewVault(1, ActiveVault, AsgardVault, GetRandomPubKey(), common.Chains{common.THORChain, common.XMRChain, common.BTCChain}.Strings(), nil)
	hybrid.PubKeyFrost = getRandomXMRFrostPubKey(c)

	filtered := preferredActiveVaultsForChain(Vaults{hybrid}, common.XMRChain)
	c.Assert(filtered, HasLen, 1)
	c.Assert(filtered[0].PubKey.Equals(hybrid.PubKey), Equals, true)
}

func (s *XMRMonovaultSuite) TestPreferredActiveVaultsForChainRejectsXMRVaultWithoutFrost(c *C) {
	hybrid := NewVault(1, ActiveVault, AsgardVault, GetRandomPubKey(), common.Chains{common.THORChain, common.XMRChain, common.BTCChain}.Strings(), nil)
	regular := NewVault(1, ActiveVault, AsgardVault, GetRandomPubKey(), common.Chains{common.THORChain, common.BTCChain}.Strings(), nil)

	filtered := preferredActiveVaultsForChain(Vaults{regular, hybrid}, common.XMRChain)
	c.Assert(filtered, HasLen, 0)
}

func (s *XMRMonovaultSuite) TestFilterActiveVaultsForOutboundChainRejectsXMRVaultWithoutFrost(c *C) {
	hybrid := NewVault(1, ActiveVault, AsgardVault, GetRandomPubKey(), common.Chains{common.THORChain, common.XMRChain, common.BTCChain}.Strings(), nil)
	regular := NewVault(1, ActiveVault, AsgardVault, GetRandomPubKey(), common.Chains{common.THORChain, common.BTCChain}.Strings(), nil)

	filtered := filterActiveVaultsForOutboundChain(Vaults{regular, hybrid}, common.XMRChain)
	c.Assert(filtered, HasLen, 0)
}

func (s *XMRMonovaultSuite) TestFilterActiveVaultsForOutboundChainKeepsXMRCapableVaultForOtherChains(c *C) {
	hybrid := NewVault(1, ActiveVault, AsgardVault, GetRandomPubKey(), common.Chains{common.THORChain, common.XMRChain, common.BTCChain}.Strings(), nil)
	hybrid.PubKeyFrost = getRandomXMRFrostPubKey(c)

	filtered := filterActiveVaultsForOutboundChain(Vaults{hybrid}, common.BTCChain)
	c.Assert(filtered, HasLen, 1)
	c.Assert(filtered[0].PubKey.Equals(hybrid.PubKey), Equals, true)
}
