package types

import (
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	cosmos "gitlab.com/thorchain/thornode/v3/common/cosmos"
)

type NetworkSuite struct{}

var _ = Suite(&NetworkSuite{})

func (s *NetworkSuite) TestCalcNodeRewards(c *C) {
	network1 := NewNetwork()
	c.Check(network1.BondRewardRune.Uint64(), Equals, cosmos.ZeroUint().Uint64())

	vault := Network{
		TotalBondUnits: cosmos.NewUint(100),
		BondRewardRune: cosmos.NewUint(3000),
	}
	reward := vault.CalcNodeRewards(cosmos.NewUint(5))
	c.Check(reward.Uint64(), Equals, uint64(150))

	vault = Network{
		TotalBondUnits: cosmos.NewUint(7357),
		BondRewardRune: cosmos.NewUint(275.357 * common.One),
	}
	reward = vault.CalcNodeRewards(cosmos.NewUint(78))
	c.Check(reward.Uint64(), Equals, uint64(291937556))

	vault = Network{
		TotalBondUnits: cosmos.NewUint(7357),
		BondRewardRune: cosmos.ZeroUint(),
	}
	reward = vault.CalcNodeRewards(cosmos.NewUint(78))
	c.Check(reward.Uint64(), Equals, uint64(0))
}

func (s *NetworkSuite) TestGetSetChainRewardAmount(c *C) {
	network := NewNetwork()

	// Test getting reward for non-existent chain returns zero
	reward := network.GetChainRewardAmount(common.ETHChain.String())
	c.Check(reward.IsZero(), Equals, true)

	// Test setting reward for new chain
	network.SetChainRewardAmount(common.ETHChain.String(), cosmos.NewUint(1000))
	reward = network.GetChainRewardAmount(common.ETHChain.String())
	c.Check(reward.Uint64(), Equals, uint64(1000))

	// Test updating existing chain reward
	network.SetChainRewardAmount(common.ETHChain.String(), cosmos.NewUint(2000))
	reward = network.GetChainRewardAmount(common.ETHChain.String())
	c.Check(reward.Uint64(), Equals, uint64(2000))

	// Test multiple chains
	network.SetChainRewardAmount(common.BTCChain.String(), cosmos.NewUint(3000))
	network.SetChainRewardAmount(common.AVAXChain.String(), cosmos.NewUint(4000))

	c.Check(network.GetChainRewardAmount(common.ETHChain.String()).Uint64(), Equals, uint64(2000))
	c.Check(network.GetChainRewardAmount(common.BTCChain.String()).Uint64(), Equals, uint64(3000))
	c.Check(network.GetChainRewardAmount(common.AVAXChain.String()).Uint64(), Equals, uint64(4000))
	c.Check(len(network.ChainRewards), Equals, 3)
}

func (s *NetworkSuite) TestGetSetChainBondUnitsAmount(c *C) {
	network := NewNetwork()

	// Test getting bond units for non-existent chain returns zero
	units := network.GetChainBondUnitsAmount(common.BTCChain.String())
	c.Check(units.IsZero(), Equals, true)

	// Test setting bond units for new chain
	network.SetChainBondUnitsAmount(common.BTCChain.String(), cosmos.NewUint(500))
	units = network.GetChainBondUnitsAmount(common.BTCChain.String())
	c.Check(units.Uint64(), Equals, uint64(500))

	// Test updating existing chain bond units
	network.SetChainBondUnitsAmount(common.BTCChain.String(), cosmos.NewUint(750))
	units = network.GetChainBondUnitsAmount(common.BTCChain.String())
	c.Check(units.Uint64(), Equals, uint64(750))

	// Test multiple chains
	network.SetChainBondUnitsAmount(common.ETHChain.String(), cosmos.NewUint(1000))
	network.SetChainBondUnitsAmount(common.GAIAChain.String(), cosmos.NewUint(250))

	c.Check(network.GetChainBondUnitsAmount(common.BTCChain.String()).Uint64(), Equals, uint64(750))
	c.Check(network.GetChainBondUnitsAmount(common.ETHChain.String()).Uint64(), Equals, uint64(1000))
	c.Check(network.GetChainBondUnitsAmount(common.GAIAChain.String()).Uint64(), Equals, uint64(250))
	c.Check(len(network.ChainBondUnits), Equals, 3)
}

func (s *NetworkSuite) TestChainRewardAndBondUnitsIntegration(c *C) {
	network := NewNetwork()

	// Set up rewards and bond units for multiple chains
	chains := []string{common.ETHChain.String(), common.BTCChain.String(), common.AVAXChain.String()}
	for i, chain := range chains {
		rewardAmount := cosmos.NewUint(uint64((i + 1) * 1000))
		bondUnits := cosmos.NewUint(uint64((i + 1) * 100))
		network.SetChainRewardAmount(chain, rewardAmount)
		network.SetChainBondUnitsAmount(chain, bondUnits)
	}

	// Verify all chains have correct values
	c.Check(network.GetChainRewardAmount(common.ETHChain.String()).Uint64(), Equals, uint64(1000))
	c.Check(network.GetChainBondUnitsAmount(common.ETHChain.String()).Uint64(), Equals, uint64(100))

	c.Check(network.GetChainRewardAmount(common.BTCChain.String()).Uint64(), Equals, uint64(2000))
	c.Check(network.GetChainBondUnitsAmount(common.BTCChain.String()).Uint64(), Equals, uint64(200))

	c.Check(network.GetChainRewardAmount(common.AVAXChain.String()).Uint64(), Equals, uint64(3000))
	c.Check(network.GetChainBondUnitsAmount(common.AVAXChain.String()).Uint64(), Equals, uint64(300))

	// Test that non-existent chain still returns zero
	c.Check(network.GetChainRewardAmount(common.DOGEChain.String()).IsZero(), Equals, true)
	c.Check(network.GetChainBondUnitsAmount(common.DOGEChain.String()).IsZero(), Equals, true)
}
