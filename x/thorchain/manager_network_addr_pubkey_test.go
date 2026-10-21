package thorchain

import (
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
)

type NetworkMgrAddrPubKeySuite struct{}

var _ = Suite(&NetworkMgrAddrPubKeySuite{})

func (s *NetworkMgrAddrPubKeySuite) TestRotateVaultCreatesAddressMappings(c *C) {
	ctx, mgr := setupManagerForTest(c)
	vm := newNetworkMgr(mgr.Keeper(), NewTxStoreDummy(), mgr.EventMgr())

	// Create a vault with multiple chains
	pubKey := GetRandomPubKey()
	pubKeyEddsa := GetRandomPubKey() // Using secp256k1 pubkey for simplicity in testing
	chains := []string{common.BTCChain.String(), common.ETHChain.String(), common.THORChain.String()}

	vault := NewVaultV2(ctx.BlockHeight(), InitVault, AsgardVault, pubKey, chains, nil, pubKeyEddsa)
	vault.Membership = []string{pubKey.String()}

	// Verify no address mappings exist before rotation
	for _, chainStr := range chains {
		chain, err := common.NewChain(chainStr)
		c.Assert(err, IsNil)

		// Check ECDSA pubkey
		addr, err := pubKey.GetAddress(chain)
		c.Assert(err, IsNil)
		result, err := mgr.Keeper().GetAddrPubKey(ctx, addr)
		c.Assert(err, IsNil)
		c.Assert(result, Equals, common.EmptyPubKey)

		// Check EdDSA pubkey for chains that support it
		if chain.GetSigningAlgo() == common.SigningAlgoEd25519 {
			addrEddsa, err := pubKeyEddsa.GetAddress(chain)
			c.Assert(err, IsNil)
			result, err = mgr.Keeper().GetAddrPubKey(ctx, addrEddsa)
			c.Assert(err, IsNil)
			c.Assert(result, Equals, common.EmptyPubKey)
		}
	}

	// Rotate the vault (this should create address mappings)
	err := vm.RotateVault(ctx, vault)
	c.Assert(err, IsNil)

	// Verify address mappings were created after rotation
	for _, chainStr := range chains {
		chain, err := common.NewChain(chainStr)
		c.Assert(err, IsNil)

		// Check ECDSA pubkey mapping
		addr, err := pubKey.GetAddress(chain)
		c.Assert(err, IsNil)
		result, err := mgr.Keeper().GetAddrPubKey(ctx, addr)
		c.Assert(err, IsNil)
		c.Assert(result, Equals, pubKey)

		// Check EdDSA pubkey mapping for chains that support it
		if chain.GetSigningAlgo() == common.SigningAlgoEd25519 {
			addrEddsa, err := pubKeyEddsa.GetAddress(chain)
			c.Assert(err, IsNil)
			result, err = mgr.Keeper().GetAddrPubKey(ctx, addrEddsa)
			c.Assert(err, IsNil)
			c.Assert(result, Equals, pubKeyEddsa)
		}
	}

	// Verify the vault status was updated to Active
	savedVault, err := mgr.Keeper().GetVault(ctx, pubKey)
	c.Assert(err, IsNil)
	c.Assert(savedVault.Status, Equals, ActiveVault)
}

func (s *NetworkMgrAddrPubKeySuite) TestRotateVaultHandlesInvalidChains(c *C) {
	ctx, mgr := setupManagerForTest(c)
	vm := newNetworkMgr(mgr.Keeper(), NewTxStoreDummy(), mgr.EventMgr())

	// Create a vault with an invalid chain
	pubKey := GetRandomPubKey()
	chains := []string{"INVALID_CHAIN", common.BTCChain.String()}

	vault := NewVaultV2(ctx.BlockHeight(), InitVault, AsgardVault, pubKey, chains, nil, common.EmptyPubKey)
	vault.Membership = []string{pubKey.String()}

	// Rotate the vault (should handle invalid chain gracefully)
	err := vm.RotateVault(ctx, vault)
	c.Assert(err, IsNil)

	// Verify valid chain mapping was still created
	chain, err := common.NewChain(common.BTCChain.String())
	c.Assert(err, IsNil)
	addr, err := pubKey.GetAddress(chain)
	c.Assert(err, IsNil)
	result, err := mgr.Keeper().GetAddrPubKey(ctx, addr)
	c.Assert(err, IsNil)
	c.Assert(result, Equals, pubKey)

	// Verify the vault status was updated to Active
	savedVault, err := mgr.Keeper().GetVault(ctx, pubKey)
	c.Assert(err, IsNil)
	c.Assert(savedVault.Status, Equals, ActiveVault)
}
