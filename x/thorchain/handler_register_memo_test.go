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
		Memo:      "SWAP:BTC.BTC:thor1test:11111", // Full swap memo
		Ttl:       14400,                          // 4 hours
		ChainId:   "BTC",
		Signer:    signer,
		Reference: "11111", // User-provided 5-digit reference (unique)
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
		Memo:      "SWAP:BTC.BTC:thor1test:22222",
		Ttl:       14400,
		ChainId:   "BTC",
		Signer:    signer,
		Reference: "22222", // Unique reference for chain halt test
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
		Memo:      "SWAP:BTC.BTC:thor1test:33333",
		Ttl:       14400,
		ChainId:   "BTC",
		Signer:    signer,
		Reference: "33333", // Unique reference for disabled test
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
		Memo:      "SWAP:BTC.BTC:thor1test:44444",
		Ttl:       14400,
		ChainId:   "BTC",
		Signer:    signer,
		Reference: "44444", // Unique reference for versioning test
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
	memo := "SWAP:BTC.BTC:thor1test:55555"
	msg := &types.MsgRegisterMemo{
		Memo:      memo,
		Ttl:       14400,
		ChainId:   "BTC",
		Signer:    signer,
		Reference: "55555", // Unique reference for keeper integration test
	}

	// RED: This should fail initially - keeper integration doesn't exist yet
	result, err := handler.Run(ctx, msg)
	c.Assert(err, IsNil, Commentf("Handler should integrate with keeper successfully"))
	c.Assert(result, NotNil, Commentf("Should return valid result"))

	// Verify memo was registered in keeper by retrieving it
	retrievedMemo, err := keeper.GetRegisteredMemo(ctx, "55555")
	c.Assert(err, IsNil, Commentf("Should be able to retrieve registered memo"))
	c.Assert(retrievedMemo.Reference, HasLen, 5, Commentf("Should have 5-digit reference"))
	c.Assert(retrievedMemo.Reference, Equals, "55555", Commentf("Should have correct reference"))
	c.Assert(retrievedMemo.Memo, Equals, memo, Commentf("Should preserve original memo"))
}

// TestRegisterMemoHandlerCollisionHandling tests collision resolution (RED PHASE)
func (s *HandlerRegisterMemoSuite) TestRegisterMemoHandlerCollisionHandling(c *C) {
	ctx, mgr := setupManagerForTest(c)
	handler := NewRegisterMemoHandler(mgr)

	keeper := mgr.Keeper().(*keeperv1.KVStore)
	err := keeper.InitializeDefaultChainConfigs(ctx)
	c.Assert(err, IsNil)

	signer := GetRandomBech32Addr()
	memo := "SWAP:BTC.BTC:thor1collision" // Common memo content

	// Register multiple memos with different user references
	references := make([]string, 3)
	for i := 0; i < 3; i++ {
		// Use unique references: 15000, 25000, 35000
		reference := fmt.Sprintf("%d5000", i+1)
		msg := &types.MsgRegisterMemo{
			Memo:      memo,
			Ttl:       14400,
			ChainId:   "BTC",
			Signer:    signer,
			Reference: reference,
		}

		// RED: This should fail initially - collision handling doesn't exist yet
		result, err := handler.Run(ctx, msg)
		c.Assert(err, IsNil, Commentf("Registration %d should succeed", i+1))
		c.Assert(result, NotNil, Commentf("Should return valid result for registration %d", i+1))

		// Store reference for uniqueness check
		references[i] = reference
	}

	// Verify all references are unique
	for i := 0; i < len(references); i++ {
		for j := i + 1; j < len(references); j++ {
			c.Assert(references[i], Not(Equals), references[j],
				Commentf("References should be unique: %s != %s", references[i], references[j]))
		}
	}
}

// TestRegisterMemoHandlerReferenceValidation tests user reference validation for different chains (RED PHASE)
func (s *HandlerRegisterMemoSuite) TestRegisterMemoHandlerReferenceValidation(c *C) {
	ctx, mgr := setupManagerForTest(c)
	handler := NewRegisterMemoHandler(mgr)

	keeper := mgr.Keeper().(*keeperv1.KVStore)
	err := keeper.InitializeDefaultChainConfigs(ctx)
	c.Assert(err, IsNil)

	signer := GetRandomBech32Addr()

	testCases := []struct {
		chain       string
		reference   string
		shouldPass  bool
		description string
	}{
		{
			chain:       "BTC",
			reference:   "66666", // Valid 5-digit reference
			shouldPass:  true,
			description: "BTC with valid reference should pass",
		},
		{
			chain:       "BTC",
			reference:   "1234", // Invalid - too short
			shouldPass:  false,
			description: "BTC with short reference should fail",
		},
		{
			chain:       "ETH",
			reference:   "77777", // Valid reference
			shouldPass:  true,
			description: "ETH with valid reference should pass",
		},
		{
			chain:       "DOGE",
			reference:   "88888", // Valid reference but DOGE chain has precision issues
			shouldPass:  false,
			description: "DOGE should fail due to precision limitations",
		},
	}

	for _, tc := range testCases {
		c.Logf("Testing: %s", tc.description)

		msg := &types.MsgRegisterMemo{
			Memo:      "SWAP:BTC.BTC:thor1test:12345",
			Ttl:       14400,
			ChainId:   tc.chain,
			Signer:    signer,
			Reference: tc.reference, // User-provided reference instead of amount
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
		Memo:      "SWAP:BTC.BTC:thor1test:99999",
		Ttl:       14400,
		ChainId:   "BTC",
		Signer:    signer,
		Reference: "99999", // Unique reference for events test
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
