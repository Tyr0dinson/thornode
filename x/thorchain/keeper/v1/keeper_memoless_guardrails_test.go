package keeperv1

import (
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	thorchaintypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
	. "gopkg.in/check.v1"
)

type MemolessGuardrailsSuite struct{}

var _ = Suite(&MemolessGuardrailsSuite{})

// TestRegistrationCapEnforced sets a low MemolessMaxActiveRefs and verifies
// that new registrations are rejected once the cap is reached.
func (s *MemolessGuardrailsSuite) TestRegistrationCapEnforced(c *C) {
	ctx, k := setupKeeperForTest(c)

	// Set cap to 2 via Mimir
	k.SetMimir(ctx, constants.MemolessMaxActiveRefs.String(), 2)
	cp := constants.GetConstantValues(GetCurrentVersion())

	// Register two memos
	for i := 0; i < 2; i++ {
		ref := "3" + string(rune('0'+i)) + "123" // 30123, 31123
        msg := thorchaintypes.MsgRegisterMemo{
            Memo:      "SWAP:BTC.BTC:thor1xyz:14400",
            ChainId:   "BTC",
            Signer:    GetRandomBech32Addr(),
            Reference: ref,
        }
		err := k.ValidateMemoRegistrationComplete(ctx, msg, cp)
		c.Assert(err, IsNil)
		_, err = k.ProcessMemoRegistration(ctx, msg, cp)
		c.Assert(err, IsNil)
	}

	// Third registration should fail due to cap
    msg := thorchaintypes.MsgRegisterMemo{
        Memo:      "SWAP:BTC.BTC:thor1xyz:14400",
        ChainId:   "BTC",
        Signer:    GetRandomBech32Addr(),
        Reference: "33123",
    }
	err := k.ValidateMemoRegistrationComplete(ctx, msg, cp)
	c.Assert(err, NotNil, Commentf("expected cap enforcement error"))
}

// TestCleanupIntervalMimirOverride verifies ShouldRunCleanup respects a Mimir
// override for MemolessCleanupInterval.
func (s *MemolessGuardrailsSuite) TestCleanupIntervalMimirOverride(c *C) {
	ctx, k := setupKeeperForTest(c)

	// Initially, no last cleanup → should run
	should, err := k.ShouldRunCleanup(ctx)
	c.Assert(err, IsNil)
	c.Assert(should, Equals, true)

	// Set last cleanup height to current
	k.setLastCleanupHeight(ctx, ctx.BlockHeight())

	// Set cleanup interval to 7 via Mimir
	k.SetMimir(ctx, constants.MemolessCleanupInterval.String(), 7)

	// Advance block height by 6 → should not run
	ctx = ctx.WithBlockHeight(ctx.BlockHeight() + 6)
	should, err = k.ShouldRunCleanup(ctx)
	c.Assert(err, IsNil)
	c.Assert(should, Equals, false)

	// Advance to +7 → should run
	ctx = ctx.WithBlockHeight(ctx.BlockHeight() + 1)
	should, err = k.ShouldRunCleanup(ctx)
	c.Assert(err, IsNil)
	c.Assert(should, Equals, true)
}

// TestPerSignerActiveLimit enforces a maximum number of active registrations per signer.
func (s *MemolessGuardrailsSuite) TestPerSignerActiveLimit(c *C) {
	ctx, k := setupKeeperForTest(c)

	// Set per-signer limit to 2 via Mimir (string key; no ConstantName required)
	k.SetMimir(ctx, "MemolessActivePerSender", 2)
	cp := constants.GetConstantValues(GetCurrentVersion())

	signer := GetRandomBech32Addr()

	// Helper to build a message with given reference
    mk := func(ref string) thorchaintypes.MsgRegisterMemo {
        return thorchaintypes.MsgRegisterMemo{
            Memo:      "SWAP:BTC.BTC:thor1xyz:14400",
            ChainId:   "BTC",
            Signer:    signer,
            Reference: ref,
        }
    }

	// Two registrations OK
	for i := 0; i < 2; i++ {
		ref := "4" + string(rune('0'+i)) + "567" // 40567, 41567
		msg := mk(ref)
		c.Assert(k.ValidateMemoRegistrationComplete(ctx, msg, cp), IsNil)
		_, err := k.ProcessMemoRegistration(ctx, msg, cp)
		c.Assert(err, IsNil)
	}

	// Third registration for same signer should fail
	msg := mk("42567")
	err := k.ValidateMemoRegistrationComplete(ctx, msg, cp)
	c.Assert(err, NotNil, Commentf("expected per-signer cap enforcement error"))

	// Different signer should still be able to register
    msgOther := thorchaintypes.MsgRegisterMemo{
        Memo:      "SWAP:BTC.BTC:thor1xyz:14400",
        ChainId:   "BTC",
        Signer:    GetRandomBech32Addr(),
        Reference: "43567",
    }
	c.Assert(k.ValidateMemoRegistrationComplete(ctx, msgOther, cp), IsNil)
}

// TestNegativeLookupCacheTTL ensures negative cache entries respect TTL and expire.
func (s *MemolessGuardrailsSuite) TestNegativeLookupCacheTTL(c *C) {
	ctx, k := setupKeeperForTest(c)

	// Set negative cache TTL to 5 blocks via Mimir key string
	k.SetMimir(ctx, "MemolessNegativeCacheTTL", 5)

	// Access new methods via type assertion
	type negCache interface {
		SetNegativeRegistrationLookup(ctx cosmos.Context, reference string, ttlBlocks int64) error
		HasNegativeRegistrationLookup(ctx cosmos.Context, reference string) bool
	}
	nc, ok := any(k).(negCache)
	c.Assert(ok, Equals, true)

	ref := "55555"

	// Compute TTL from Mimir (or use 5 directly)
	ttl := int64(5)
	c.Assert(nc.SetNegativeRegistrationLookup(ctx, ref, ttl), IsNil)
	c.Assert(nc.HasNegativeRegistrationLookup(ctx, ref), Equals, true)

	// Advance 4 blocks ⇒ still cached
	ctx = ctx.WithBlockHeight(ctx.BlockHeight() + 4)
	c.Assert(nc.HasNegativeRegistrationLookup(ctx, ref), Equals, true)

	// Advance to expiry ⇒ expired and evicted
	ctx = ctx.WithBlockHeight(ctx.BlockHeight() + 1)
	c.Assert(nc.HasNegativeRegistrationLookup(ctx, ref), Equals, false)
}

// TestKillSwitch disables memoless via Mimir key and expects validation to fail.
func (s *MemolessGuardrailsSuite) TestKillSwitch(c *C) {
	ctx, k := setupKeeperForTest(c)
	cp := constants.GetConstantValues(GetCurrentVersion())

	// Enable kill-switch
	k.SetMimir(ctx, "MemolessKillSwitch", 1)

    msg := thorchaintypes.MsgRegisterMemo{
        Memo:      "SWAP:BTC.BTC:thor1xyz:14400",
        ChainId:   "BTC",
        Signer:    GetRandomBech32Addr(),
        Reference: "44567",
    }

	err := k.ValidateMemoRegistrationComplete(ctx, msg, cp)
	c.Assert(err, NotNil, Commentf("expected validation to fail when kill-switch is on"))

	// Disable kill-switch
	k.SetMimir(ctx, "MemolessKillSwitch", 0)
	c.Assert(k.ValidateMemoRegistrationComplete(ctx, msg, cp), IsNil)
}
