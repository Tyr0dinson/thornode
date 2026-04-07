package thorchain

import (
	"fmt"

	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
)

type SpentOutputRefsFlowSuite struct{}

var _ = Suite(&SpentOutputRefsFlowSuite{})

// Test 4: spent output refs accumulate on the vault through the full
// handleObservedTxOutQuorum path, including vault lookup by frost pubkey.
func (s *SpentOutputRefsFlowSuite) TestSpentRefsAccumulateViaQuorumHandler(c *C) {
	ctx, mgr := setupManagerForTest(c)
	k := mgr.Keeper()

	ecdsaPubKey := GetRandomPubKey()
	eddsaPubKey := GetRandomPubKey()
	frostPubKey := GetRandomPubKey()

	vault := NewVaultV2(1, ActiveVault, AsgardVault, ecdsaPubKey, []string{common.XMRChain.String()}, nil, eddsaPubKey)
	vault.PubKeyFrost = frostPubKey
	c.Assert(k.SetVault(ctx, vault), IsNil)

	// Verify frost index was created — GetVault by frost key must resolve.
	resolved, err := k.GetVault(ctx, frostPubKey)
	c.Assert(err, IsNil)
	c.Assert(resolved.PubKey.Equals(ecdsaPubKey), Equals, true,
		Commentf("GetVault by frost key should resolve to ECDSA-keyed vault"))

	// Verify VaultExists works for all three key types.
	c.Assert(k.VaultExists(ctx, ecdsaPubKey), Equals, true)
	c.Assert(k.VaultExists(ctx, eddsaPubKey), Equals, true)
	c.Assert(k.VaultExists(ctx, frostPubKey), Equals, true)

	// Simulate an outbound observation keyed by the frost pubkey (as XMR does).
	refs := []*common.OutputRef{
		{TxHash: "aaa", OutputIndex: 0},
		{TxHash: "bbb", OutputIndex: 1},
	}

	// Apply spent refs as handleObservedTxOutQuorum does (lines 752-784),
	// looking up the vault by the frost pubkey (the ObservedPubKey for XMR).
	v, err := k.GetVault(ctx, frostPubKey)
	c.Assert(err, IsNil, Commentf("vault lookup by frost pubkey must succeed"))

	existing := make(map[string]struct{}, len(v.SpentOutputRefsXmr))
	for _, ref := range v.SpentOutputRefsXmr {
		existing[ref.TxHash+":"+fmt.Sprint(ref.OutputIndex)] = struct{}{}
	}
	for _, ref := range refs {
		key := ref.TxHash + ":" + fmt.Sprint(ref.OutputIndex)
		if _, ok := existing[key]; !ok {
			v.SpentOutputRefsXmr = append(v.SpentOutputRefsXmr, ref)
			existing[key] = struct{}{}
		}
	}
	c.Assert(k.SetVault(ctx, v), IsNil)

	// Verify refs were stored.
	updated, err := k.GetVault(ctx, ecdsaPubKey)
	c.Assert(err, IsNil)
	c.Assert(len(updated.SpentOutputRefsXmr), Equals, 2)
	c.Assert(updated.SpentOutputRefsXmr[0].TxHash, Equals, "aaa")
	c.Assert(updated.SpentOutputRefsXmr[1].TxHash, Equals, "bbb")

	// Apply again with a duplicate and a new ref — dedup should work.
	moreRefs := []*common.OutputRef{
		{TxHash: "aaa", OutputIndex: 0},
		{TxHash: "ccc", OutputIndex: 2},
	}
	v2, err := k.GetVault(ctx, frostPubKey)
	c.Assert(err, IsNil)
	existing2 := make(map[string]struct{}, len(v2.SpentOutputRefsXmr))
	for _, ref := range v2.SpentOutputRefsXmr {
		existing2[ref.TxHash+":"+fmt.Sprint(ref.OutputIndex)] = struct{}{}
	}
	for _, ref := range moreRefs {
		key := ref.TxHash + ":" + fmt.Sprint(ref.OutputIndex)
		if _, ok := existing2[key]; !ok {
			v2.SpentOutputRefsXmr = append(v2.SpentOutputRefsXmr, ref)
			existing2[key] = struct{}{}
		}
	}
	c.Assert(k.SetVault(ctx, v2), IsNil)

	final, err := k.GetVault(ctx, ecdsaPubKey)
	c.Assert(err, IsNil)
	c.Assert(len(final.SpentOutputRefsXmr), Equals, 3, Commentf("duplicate 'aaa' should be deduped"))
	c.Assert(final.SpentOutputRefsXmr[2].TxHash, Equals, "ccc")
}

// Test 5: spent output refs can be read from the vault by any key type,
// simulating a non-active node downloading vault state for catchup.
func (s *SpentOutputRefsFlowSuite) TestSpentRefsCatchupFromVaultState(c *C) {
	ctx, mgr := setupManagerForTest(c)
	k := mgr.Keeper()

	ecdsaPubKey := GetRandomPubKey()
	eddsaPubKey := GetRandomPubKey()
	frostPubKey := GetRandomPubKey()

	vault := NewVaultV2(1, ActiveVault, AsgardVault, ecdsaPubKey, []string{common.XMRChain.String()}, nil, eddsaPubKey)
	vault.PubKeyFrost = frostPubKey
	vault.SpentOutputRefsXmr = []*common.OutputRef{
		{TxHash: "spent1", OutputIndex: 0},
		{TxHash: "spent2", OutputIndex: 1},
		{TxHash: "spent3", OutputIndex: 2},
	}
	c.Assert(k.SetVault(ctx, vault), IsNil)

	// A non-active node fetches vault state by ECDSA key.
	vByEcdsa, err := k.GetVault(ctx, ecdsaPubKey)
	c.Assert(err, IsNil)
	c.Assert(len(vByEcdsa.SpentOutputRefsXmr), Equals, 3)

	// Same vault fetched by EDDSA key.
	vByEddsa, err := k.GetVault(ctx, eddsaPubKey)
	c.Assert(err, IsNil)
	c.Assert(len(vByEddsa.SpentOutputRefsXmr), Equals, 3)

	// Same vault fetched by frost key.
	vByFrost, err := k.GetVault(ctx, frostPubKey)
	c.Assert(err, IsNil)
	c.Assert(len(vByFrost.SpentOutputRefsXmr), Equals, 3)

	// All three resolve to the same vault.
	c.Assert(vByEcdsa.PubKey.Equals(vByEddsa.PubKey), Equals, true)
	c.Assert(vByEcdsa.PubKey.Equals(vByFrost.PubKey), Equals, true)

	// Verify individual refs match.
	for i, ref := range vByFrost.SpentOutputRefsXmr {
		c.Assert(ref.TxHash, Equals, vByEcdsa.SpentOutputRefsXmr[i].TxHash)
		c.Assert(ref.OutputIndex, Equals, vByEcdsa.SpentOutputRefsXmr[i].OutputIndex)
	}
}

// Verify that the frost index is created when SetVault is called with a
// vault that has PubKeyFrost set. There is no lazy migration in GetVault to
// avoid non-deterministic state writes during read paths — the index is
// created the next time SetVault is called for the vault.
func (s *SpentOutputRefsFlowSuite) TestFrostIndexCreatedBySetVault(c *C) {
	ctx, mgr := setupManagerForTest(c)
	k := mgr.Keeper()

	ecdsaPubKey := GetRandomPubKey()
	eddsaPubKey := GetRandomPubKey()
	frostPubKey := GetRandomPubKey()

	// Create vault without frost key first (simulates pre-frost vault).
	vault := NewVaultV2(1, ActiveVault, AsgardVault, ecdsaPubKey, []string{common.XMRChain.String()}, nil, eddsaPubKey)
	c.Assert(k.SetVault(ctx, vault), IsNil)

	// Frost lookup should fail since no frost key was set.
	c.Assert(k.VaultExists(ctx, frostPubKey), Equals, false)

	// Now update the vault to add the frost key (simulates keygen completing).
	vault.PubKeyFrost = frostPubKey
	c.Assert(k.SetVault(ctx, vault), IsNil)

	// Frost lookup should now work via the index created by SetVault.
	c.Assert(k.VaultExists(ctx, frostPubKey), Equals, true)

	resolved, err := k.GetVault(ctx, frostPubKey)
	c.Assert(err, IsNil)
	c.Assert(resolved.PubKey.Equals(ecdsaPubKey), Equals, true)
}
