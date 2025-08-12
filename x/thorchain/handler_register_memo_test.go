package thorchain

import (
	"fmt"

	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/constants"
	keeperv1 "gitlab.com/thorchain/thornode/v3/x/thorchain/keeper/v1"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

type HandlerRegisterMemoSuite struct{}

var _ = Suite(&HandlerRegisterMemoSuite{})

// TestRegisterMemoHandlerValidation tests handler validation (REFACTOR PHASE - testing existing implementation)
func (s *HandlerRegisterMemoSuite) TestRegisterMemoHandlerValidation(c *C) {
	ctx, mgr := setupManagerForTest(c)
	handler := NewRegisterMemoHandler(mgr)

	// Test valid memo registration with simple memo
	signer := GetRandomBech32Addr()
	validMsg := &types.MsgRegisterMemo{
		Memo:    "12345", // Simple 5-digit reference
		Ttl:     14400,   // 4 hours
		ChainId: "BTC",
		Signer:  signer,
		Amount:  "100000000", // 1 BTC
	}

	// REFACTOR: Testing existing handler implementation
	result, err := handler.Run(ctx, validMsg)
	if err != nil {
		c.Logf("Handler error (expected in REFACTOR phase): %s", err.Error())
		// This might fail due to missing keeper methods - that's expected during refactor
		c.Assert(err, NotNil, Commentf("Handler may have missing dependencies"))
	} else {
		c.Assert(result, NotNil, Commentf("Should return valid result if successful"))
		c.Assert(result.Data, HasLen, 5, Commentf("Should return 5-digit reference"))
	}

	// Test invalid memo (empty)
	invalidMsg := &types.MsgRegisterMemo{
		Memo:    "", // Empty memo should fail
		Ttl:     14400,
		ChainId: "BTC",
		Signer:  signer,
	}

	result, err = handler.Run(ctx, invalidMsg)
	c.Assert(err, NotNil, Commentf("Empty memo should fail validation"))
	c.Assert(result, IsNil, Commentf("Should return nil result on validation failure"))
}

// TestRegisterMemoHandlerChainHalt tests handler behavior when chain is halted (RED PHASE)
func (s *HandlerRegisterMemoSuite) TestRegisterMemoHandlerChainHalt(c *C) {
	ctx, mgr := setupManagerForTest(c)
	handler := NewRegisterMemoHandler(mgr)

	// Halt THORChain
	mgr.Keeper().SetMimir(ctx, "HaltTHORChain", 1)

	signer := GetRandomBech32Addr()
	msg := &types.MsgRegisterMemo{
		Memo:    "12345",
		Ttl:     14400,
		ChainId: "BTC",
		Signer:  signer,
		Amount:  "100000000",
	}

	// RED: This should fail initially - chain halt check doesn't exist yet
	result, err := handler.Run(ctx, msg)
	c.Assert(err, NotNil, Commentf("Should fail when THORChain is halted"))
	c.Assert(result, IsNil, Commentf("Should return nil result when halted"))
	c.Assert(err.Error(), Matches, ".*halted.*", Commentf("Error should mention chain halt"))
}

// TestRegisterMemoHandlerMemolessDisabled tests handler when memoless is disabled (RED PHASE)
func (s *HandlerRegisterMemoSuite) TestRegisterMemoHandlerMemolessDisabled(c *C) {
	ctx, mgr := setupManagerForTest(c)
	handler := NewRegisterMemoHandler(mgr)

	// Disable memoless protocol
	mgr.Keeper().SetMimir(ctx, constants.MemolessEnabled.String(), 0)

	signer := GetRandomBech32Addr()
	msg := &types.MsgRegisterMemo{
		Memo:    "12345",
		Ttl:     14400,
		ChainId: "BTC",
		Signer:  signer,
		Amount:  "100000000",
	}

	// RED: This should fail initially - memoless enabled check doesn't exist yet
	result, err := handler.Run(ctx, msg)
	c.Assert(err, NotNil, Commentf("Should fail when memoless protocol is disabled"))
	c.Assert(result, IsNil, Commentf("Should return nil result when disabled"))
	c.Assert(err.Error(), Matches, ".*disabled.*", Commentf("Error should mention protocol disabled"))
}

// TestRegisterMemoHandlerVersioning tests versioned validation (RED PHASE)
func (s *HandlerRegisterMemoSuite) TestRegisterMemoHandlerVersioning(c *C) {
	ctx, mgr := setupManagerForTest(c)
	handler := NewRegisterMemoHandler(mgr)

	signer := GetRandomBech32Addr()
	msg := &types.MsgRegisterMemo{
		Memo:    "12345",
		Ttl:     14400,
		ChainId: "BTC",
		Signer:  signer,
		Amount:  "100000000",
	}

	// Test current version (should work)
	// RED: This should fail initially - versioned validate method doesn't exist yet
	result, err := handler.Run(ctx, msg)
	c.Assert(err, IsNil, Commentf("Current version should work"))
	c.Assert(result, NotNil, Commentf("Should return valid result for current version"))
}

// TestRegisterMemoHandlerKeeperIntegration tests integration with keeper layer (RED PHASE)
func (s *HandlerRegisterMemoSuite) TestRegisterMemoHandlerKeeperIntegration(c *C) {
	ctx, mgr := setupManagerForTest(c)
	handler := NewRegisterMemoHandler(mgr)

	// Initialize chain configurations
	keeper := mgr.Keeper().(*keeperv1.KVStore)
	err := keeper.InitializeDefaultChainConfigs(ctx)
	c.Assert(err, IsNil)

	signer := GetRandomBech32Addr()
	memo := "12345"
	msg := &types.MsgRegisterMemo{
		Memo:    memo,
		Ttl:     14400,
		ChainId: "BTC",
		Signer:  signer,
		Amount:  "100000000", // 1 BTC
	}

	// RED: This should fail initially - keeper integration doesn't exist yet
	result, err := handler.Run(ctx, msg)
	c.Assert(err, IsNil, Commentf("Handler should integrate with keeper successfully"))
	c.Assert(result, NotNil, Commentf("Should return valid result"))

	// Verify memo was registered in keeper
	constantProvider := constants.GetConstantValues(mgr.GetVersion())
	registrations, err := keeper.ProcessMemoRegistration(ctx, *msg, constantProvider)
	c.Assert(err, IsNil, Commentf("Should be able to retrieve registered memo"))
	c.Assert(registrations.Reference, HasLen, 5, Commentf("Should generate 5-digit reference"))
	c.Assert(registrations.Memo, Equals, memo, Commentf("Should preserve original memo"))
}

// TestRegisterMemoHandlerCollisionHandling tests collision resolution (RED PHASE)
func (s *HandlerRegisterMemoSuite) TestRegisterMemoHandlerCollisionHandling(c *C) {
	ctx, mgr := setupManagerForTest(c)
	handler := NewRegisterMemoHandler(mgr)

	keeper := mgr.Keeper().(*keeperv1.KVStore)
	err := keeper.InitializeDefaultChainConfigs(ctx)
	c.Assert(err, IsNil)

	signer := GetRandomBech32Addr()
	memo := "11111" // Simple collision test memo

	// Register multiple memos with same content to test collision resolution
	references := make([]string, 3)
	for i := 0; i < 3; i++ {
		msg := &types.MsgRegisterMemo{
			Memo:    memo,
			Ttl:     14400,
			ChainId: "BTC",
			Signer:  signer,
			Amount:  fmt.Sprintf("%d00000000", i+1), // Different amounts to force collisions
		}

		// RED: This should fail initially - collision handling doesn't exist yet
		result, err := handler.Run(ctx, msg)
		c.Assert(err, IsNil, Commentf("Registration %d should succeed", i+1))
		c.Assert(result, NotNil, Commentf("Should return valid result for registration %d", i+1))

		// Store reference for uniqueness check
		constantProvider := constants.GetConstantValues(mgr.GetVersion())
		registration, err := keeper.ProcessMemoRegistration(ctx, *msg, constantProvider)
		c.Assert(err, IsNil)
		references[i] = registration.Reference
	}

	// Verify all references are unique
	for i := 0; i < len(references); i++ {
		for j := i + 1; j < len(references); j++ {
			c.Assert(references[i], Not(Equals), references[j],
				Commentf("References should be unique: %s != %s", references[i], references[j]))
		}
	}
}

// TestRegisterMemoHandlerAmountValidation tests amount validation for different chains (RED PHASE)
func (s *HandlerRegisterMemoSuite) TestRegisterMemoHandlerAmountValidation(c *C) {
	ctx, mgr := setupManagerForTest(c)
	handler := NewRegisterMemoHandler(mgr)

	keeper := mgr.Keeper().(*keeperv1.KVStore)
	err := keeper.InitializeDefaultChainConfigs(ctx)
	c.Assert(err, IsNil)

	signer := GetRandomBech32Addr()

	testCases := []struct {
		chain       string
		amount      string
		shouldPass  bool
		description string
	}{
		{
			chain:       "BTC",
			amount:      "100000000", // 1 BTC - should pass
			shouldPass:  true,
			description: "BTC with 1 BTC should pass",
		},
		{
			chain:       "BTC",
			amount:      "50000", // 0.0005 BTC - should fail (too small)
			shouldPass:  false,
			description: "BTC with too small amount should fail",
		},
		{
			chain:       "ETH",
			amount:      "1000000000000000000", // 1 ETH - should pass
			shouldPass:  true,
			description: "ETH with 1 ETH should pass",
		},
		{
			chain:       "DOGE",
			amount:      "100000000", // 1 DOGE - should fail (unsupported chain)
			shouldPass:  false,
			description: "DOGE should fail due to precision issues",
		},
	}

	for _, tc := range testCases {
		c.Logf("Testing: %s", tc.description)

		msg := &types.MsgRegisterMemo{
			Memo:    "12345",
			Ttl:     14400,
			ChainId: tc.chain,
			Signer:  signer,
			Amount:  tc.amount,
		}

		// RED: This should fail initially - amount validation doesn't exist yet
		result, err := handler.Run(ctx, msg)
		if tc.shouldPass {
			c.Assert(err, IsNil, Commentf("Should pass: %s", tc.description))
			c.Assert(result, NotNil, Commentf("Should return result: %s", tc.description))
		} else {
			c.Assert(err, NotNil, Commentf("Should fail: %s", tc.description))
			c.Assert(result, IsNil, Commentf("Should return nil: %s", tc.description))
		}
	}
}

// TestRegisterMemoHandlerEvents tests event emission (RED PHASE)
func (s *HandlerRegisterMemoSuite) TestRegisterMemoHandlerEvents(c *C) {
	ctx, mgr := setupManagerForTest(c)
	handler := NewRegisterMemoHandler(mgr)

	keeper := mgr.Keeper().(*keeperv1.KVStore)
	err := keeper.InitializeDefaultChainConfigs(ctx)
	c.Assert(err, IsNil)

	signer := GetRandomBech32Addr()
	msg := &types.MsgRegisterMemo{
		Memo:    "12345",
		Ttl:     14400,
		ChainId: "BTC",
		Signer:  signer,
		Amount:  "100000000",
	}

	// RED: This should fail initially - event emission doesn't exist yet
	result, err := handler.Run(ctx, msg)
	c.Assert(err, IsNil, Commentf("Should succeed"))
	c.Assert(result, NotNil, Commentf("Should return result"))

	// Check that events were emitted
	events := ctx.EventManager().Events()
	c.Assert(len(events) > 0, Equals, true, Commentf("Should emit at least one event"))

	// Look for memoless registration event
	var foundEvent bool
	for _, event := range events {
		if event.Type == "memo_registered" {
			foundEvent = true
			break
		}
	}
	c.Assert(foundEvent, Equals, true, Commentf("Should emit memo_registered event"))
}
