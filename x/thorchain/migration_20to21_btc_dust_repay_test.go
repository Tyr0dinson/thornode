//go:build mainnet

package thorchain

import (
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

type Migrate20to21BtcDustRepaySuite struct{}

var _ = Suite(&Migrate20to21BtcDustRepaySuite{})

// activeBtcVault builds an active Asgard holding the given BTC sats.
func activeBtcVault(c *C, ctx cosmos.Context, mgr *Mgrs, sats uint64) Vault {
	v := GetRandomVault()
	v.Status = ActiveVault
	v.Type = AsgardVault
	v.PubKeyFrost = GetRandomEd25519PubKey()
	v.Coins = common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(sats))}
	c.Assert(mgr.Keeper().SetVault(ctx, v), IsNil)
	return v
}

// The step pins the outbound to the configured vault even when another funded
// Asgard could serve it, pays the recipient the full sats owed, and tags the
// outbound with the original inbound hash.
func (s *Migrate20to21BtcDustRepaySuite) TestBtcDustRepay322PinsConfiguredVault(c *C) {
	ctx, mgr := setupManagerForTest(c)

	// Create the fallback-eligible vault FIRST so it sorts ahead in the Asgard
	// index; pinning must still pick the SECOND, which the plain fallback-first
	// loop would not — so this fails if the pin block is removed.
	_ = activeBtcVault(c, ctx, mgr, common.One)
	pinned := activeBtcVault(c, ctx, mgr, common.One)

	orig := btcDustRepay322Vault
	btcDustRepay322Vault = pinned.PubKey.String()
	defer func() { btcDustRepay322Vault = orig }()

	NewMigrator(mgr).runBtcDustRepay322(ctx)

	items, err := mgr.TxOutStore().GetOutboundItems(ctx)
	c.Assert(err, IsNil)
	c.Assert(items, HasLen, 1)
	c.Check(items[0].Chain.Equals(common.BTCChain), Equals, true)
	c.Check(items[0].VaultPubKey.Equals(pinned.PubKey), Equals, true)
	c.Check(items[0].ToAddress.String(), Equals, btcDustRepay322Dest)
	c.Check(items[0].Coin.Asset.Equals(common.BTCAsset), Equals, true)
	c.Check(items[0].Coin.Amount.Equal(cosmos.NewUint(btcDustRepay322Sats)), Equals, true)
	inTxID, err := common.NewTxID(btcDustRepay322InHash)
	c.Assert(err, IsNil)
	c.Check(items[0].InHash.Equals(inTxID), Equals, true)
}

// When the configured vault is no longer an active Asgard at the cut, the step
// falls back to any funded active Asgard rather than skipping the re-pay.
func (s *Migrate20to21BtcDustRepaySuite) TestBtcDustRepay322FallsBackWhenPinnedVaultAbsent(c *C) {
	ctx, mgr := setupManagerForTest(c)

	fallback := activeBtcVault(c, ctx, mgr, common.One)

	orig := btcDustRepay322Vault
	// A pubkey that is not among the active vaults.
	btcDustRepay322Vault = GetRandomPubKey().String()
	defer func() { btcDustRepay322Vault = orig }()

	NewMigrator(mgr).runBtcDustRepay322(ctx)

	items, err := mgr.TxOutStore().GetOutboundItems(ctx)
	c.Assert(err, IsNil)
	c.Assert(items, HasLen, 1)
	c.Check(items[0].VaultPubKey.Equals(fallback.PubKey), Equals, true)
	c.Check(items[0].Coin.Amount.Equal(cosmos.NewUint(btcDustRepay322Sats)), Equals, true)
}

// The toggle gates the whole step: disabled schedules nothing.
func (s *Migrate20to21BtcDustRepaySuite) TestBtcDustRepay322Disabled(c *C) {
	ctx, mgr := setupManagerForTest(c)
	_ = activeBtcVault(c, ctx, mgr, common.One)

	orig := btcDustRepay322Enabled
	btcDustRepay322Enabled = false
	defer func() { btcDustRepay322Enabled = orig }()

	NewMigrator(mgr).runBtcDustRepay322(ctx)

	items, err := mgr.TxOutStore().GetOutboundItems(ctx)
	c.Assert(err, IsNil)
	c.Assert(items, HasLen, 0)
}

// No active Asgard holds enough BTC: the step soft-skips (no outbound, no panic).
func (s *Migrate20to21BtcDustRepaySuite) TestBtcDustRepay322SoftSkipsWhenUnfunded(c *C) {
	ctx, mgr := setupManagerForTest(c)
	_ = activeBtcVault(c, ctx, mgr, btcDustRepay322Sats-1) // short

	NewMigrator(mgr).runBtcDustRepay322(ctx)

	items, err := mgr.TxOutStore().GetOutboundItems(ctx)
	c.Assert(err, IsNil)
	c.Assert(items, HasLen, 0)
}
