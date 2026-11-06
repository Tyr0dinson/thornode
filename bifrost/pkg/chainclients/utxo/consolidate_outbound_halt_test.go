package utxo

import (
	"strings"

	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/constants"
	types2 "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// outboundHaltBridge serves the HaltOutbound mimirs and the Asgard set over the
// test's real bridge.
type outboundHaltBridge struct {
	thorclient.ThorchainBridge
	haltOutbound int64
	asgards      types2.Vaults
	asgardCalls  int
}

func (b *outboundHaltBridge) GetMimir(key string) (int64, error) {
	if strings.HasPrefix(strings.ToUpper(key), constants.MimirKeyHaltOutbound) {
		return b.haltOutbound, nil
	}
	return b.ThorchainBridge.GetMimir(key)
}

func (b *outboundHaltBridge) GetAsgards() (types2.Vaults, error) {
	b.asgardCalls++
	return b.asgards, nil
}

// A consolidation is a CONSOLIDATE self-send to the vault's own address, so it
// still signs under HaltOutbound. The sign error proves it reached TSS.
func (s *BitcoinSignerSuite) TestConsolidateVaultUTXOsSignsUnderHaltOutbound(c *C) {
	s.client.cfg.UTXO.MaxUTXOsToSpend = 10
	s.client.tssKeySigner = &errorThorchainKeyManager{}
	vault := types2.NewVault(1, types2.VaultStatus_ActiveVault, types2.VaultType_AsgardVault,
		types2.GetRandomPubKey(), common.Chains{common.BTCChain}.Strings(), nil)
	bridge := &outboundHaltBridge{ThorchainBridge: s.client.bridge, haltOutbound: 1, asgards: types2.Vaults{vault}}
	s.client.bridge = bridge

	err := s.client.consolidateVaultUTXOs(vault, 2, 0)
	c.Assert(err, ErrorMatches, "(?s)sign consolidate txout item: .*mock remote sign failure.*")
	c.Check(bridge.asgardCalls > 0, Equals, true)
}

// Fail closed: if the destination is not a known Active/Retiring Asgard vault
// address the consolidation is treated as external and is not signed.
func (s *BitcoinSignerSuite) TestConsolidateVaultUTXOsBlockedWhenNotAsgard(c *C) {
	s.client.cfg.UTXO.MaxUTXOsToSpend = 10
	s.client.tssKeySigner = &errorThorchainKeyManager{}
	vault := types2.NewVault(1, types2.VaultStatus_ActiveVault, types2.VaultType_AsgardVault,
		types2.GetRandomPubKey(), common.Chains{common.BTCChain}.Strings(), nil)
	inactive := vault
	inactive.Status = types2.VaultStatus_InactiveVault
	for _, asgards := range []types2.Vaults{nil, {inactive}} {
		s.client.bridge = &outboundHaltBridge{ThorchainBridge: s.client.bridge, haltOutbound: 1, asgards: asgards}
		c.Check(s.client.consolidateVaultUTXOs(vault, 2, 0), IsNil, Commentf("asgards %v", asgards))
	}
}

// Without an outbound halt the vault set is not read.
func (s *BitcoinSignerSuite) TestConsolidateVaultUTXOsIgnoresUnsetHaltOutbound(c *C) {
	s.client.cfg.UTXO.MaxUTXOsToSpend = 10
	s.client.tssKeySigner = &errorThorchainKeyManager{}
	vault := types2.Vault{PubKey: types2.GetRandomPubKey()}
	bridge := &outboundHaltBridge{ThorchainBridge: s.client.bridge, haltOutbound: -1}
	s.client.bridge = bridge

	err := s.client.consolidateVaultUTXOs(vault, 2, 0)
	c.Assert(err, ErrorMatches, "(?s)sign consolidate txout item: .*mock remote sign failure.*")
	c.Check(bridge.asgardCalls, Equals, 0)
}
