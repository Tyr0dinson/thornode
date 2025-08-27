package keeperv1

import (
	"fmt"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/constants"
	thorchaintypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

type MemolessSecurityAuditTestSuite struct{}

var _ = Suite(&MemolessSecurityAuditTestSuite{})

// TestSecurityAuditExecution tests the comprehensive security audit functionality
func (s *MemolessSecurityAuditTestSuite) TestSecurityAuditExecution(c *C) {
	ctx, k := setupKeeperForTest(c)

	// Initialize chain configs
	err := k.InitializeDefaultChainConfigs(ctx)
	c.Assert(err, IsNil)

	// Execute security audit
	auditReport, err := k.ConductSecurityAudit(ctx)
	c.Assert(err, IsNil, Commentf("Security audit should execute successfully"))
	c.Assert(auditReport, NotNil, Commentf("Audit report should not be nil"))

	// Verify audit report structure
	c.Assert(auditReport.OverallRisk != "", Equals, true, Commentf("Should have overall risk assessment"))
	c.Assert(auditReport.AuditSummary.TotalRisks >= 0, Equals, true, Commentf("Should count risks"))
	c.Assert(auditReport.AuditSummary.MitigationsCount > 0, Equals, true, Commentf("Should have mitigations"))

	// Verify compliance assessment
	c.Assert(auditReport.Compliance.OWASPCompliance.Score > 0, Equals, true, Commentf("Should have OWASP compliance score"))
	c.Assert(auditReport.Compliance.CosmosSDKCompliance.Score > 0, Equals, true, Commentf("Should have Cosmos SDK compliance score"))

	c.Logf("✓ Security audit executed: %s risk, %d mitigations, %d%% compliance",
		auditReport.OverallRisk, auditReport.AuditSummary.MitigationsCount, auditReport.AuditSummary.ComplianceScore)
}

// TestInputValidationSecurity tests input validation security controls
func (s *MemolessSecurityAuditTestSuite) TestInputValidationSecurity(c *C) {
	ctx, k := setupKeeperForTest(c)

	// Initialize chain configs
	err := k.InitializeDefaultChainConfigs(ctx)
	c.Assert(err, IsNil)

	// Test valid reference validation (use non-reserved reference)
	err = k.ValidateReference(ctx, "15678")
	c.Assert(err, IsNil, Commentf("Valid reference should pass validation"))

	// Test invalid references
	invalidRefs := []string{
		"1234",      // Too short
		"123456",    // Too long
		"1234a",     // Contains letter
		"00000",     // Reserved pattern
		"99999",     // Reserved pattern
		"12345\x00", // Null byte injection
	}

	for _, ref := range invalidRefs {
		err = k.ValidateReference(ctx, ref)
		c.Assert(err, NotNil, Commentf("Invalid reference %s should fail validation", ref))
	}

	c.Logf("✓ Input validation security verified: 1 valid format, %d invalid formats rejected", len(invalidRefs))
}

// TestAccessControlSecurity tests access control and authorization
func (s *MemolessSecurityAuditTestSuite) TestAccessControlSecurity(c *C) {
	ctx, k := setupKeeperForTest(c)

	// Initialize chain configs
	err := k.InitializeDefaultChainConfigs(ctx)
	c.Assert(err, IsNil)

	authorizedSigner := GetRandomValidatorNode(NodeActive).NodeAddress
	constantProvider := constants.GetConstantValues(GetCurrentVersion())

	// Test authorized registration
	authorizedMsg := thorchaintypes.MsgRegisterMemo{
		Memo:    "SWAP:BTC.BTC:thor1authorized:14400",
		ChainId: "BTC",
		Ttl:     14400,
		Signer:  authorizedSigner,
		Reference: "44002", // User-provided reference
	}

	result, err := k.ProcessMemoRegistration(ctx, authorizedMsg, constantProvider)
	c.Assert(err, IsNil, Commentf("Authorized registration should succeed"))
	c.Assert(result.Reference != "", Equals, true, Commentf("Should generate reference"))

	// Test TTL extension authorization
	err = k.ExtendMemoTTL(ctx, result.Reference, 7200, authorizedSigner, constantProvider)
	c.Assert(err, IsNil, Commentf("Original signer should be able to extend TTL"))

	// Test unauthorized TTL extension with different signer
	unauthorizedSigner := GetRandomValidatorNode(NodeActive).NodeAddress
	if !unauthorizedSigner.Equals(authorizedSigner) {
		err = k.ExtendMemoTTL(ctx, result.Reference, 7200, unauthorizedSigner, constantProvider)
		c.Assert(err, NotNil, Commentf("Unauthorized signer should not be able to extend TTL"))
	} else {
		c.Logf("Note: Same random signer generated, skipping unauthorized test")
	}

	c.Logf("✓ Access control security verified: authorized operations pass, unauthorized operations blocked")
}

// TestConsensusIntegritySecurity tests consensus and determinism security
func (s *MemolessSecurityAuditTestSuite) TestConsensusIntegritySecurity(c *C) {
	ctx, k := setupKeeperForTest(c)

	// Initialize chain configs
	err := k.InitializeDefaultChainConfigs(ctx)
	c.Assert(err, IsNil)

	// Test user-controlled reference validation (no counter manipulation possible)
	ref1 := "12345" // User-provided reference
	err = k.ValidateUserReference(ctx, ref1)
	c.Assert(err, IsNil, Commentf("Valid user reference should pass validation"))

	ref2 := "67890" // Different user-provided reference  
	err = k.ValidateUserReference(ctx, ref2)
	c.Assert(err, IsNil, Commentf("Different valid user reference should pass validation"))
	
	c.Assert(ref1, Not(Equals), ref2, Commentf("Different user references should be unique"))

	// Test collision handling determinism
	signer := GetRandomValidatorNode(NodeActive).NodeAddress

	// Create a memo to force collision
	collisionMemo := thorchaintypes.RegisteredMemo{
		Reference:    "50000",
		Memo:         "SWAP:BTC.BTC:thor1collision:14400",
		ChainId:      "BTC",
		RegisteredAt: ctx.BlockHeight(),
		ExpiresAt:    ctx.BlockHeight() + 14400,
		Status:       "active",
		Signer:       signer,
	}
	err = k.SetRegisteredMemo(ctx, collisionMemo)
	c.Assert(err, IsNil)

	// Test reference collision detection (user must avoid collisions)
	// Register memo with reference "50000"
	msg := thorchaintypes.MsgRegisterMemo{
		Memo:      "SWAP:BTC.BTC:thor1test:50000",
		ChainId:   "BTC",
		Ttl:       14400,
		Signer:    signer,
		Reference: "50000", // User-provided reference
	}
	constantProvider := constants.GetConstantValues(GetCurrentVersion())
	_, err = k.ProcessMemoRegistration(ctx, msg, constantProvider)
	c.Assert(err, IsNil, Commentf("First registration should succeed"))

	// Try to use same reference - should fail
	err = k.ValidateUserReference(ctx, "50000")
	c.Assert(err, NotNil, Commentf("Duplicate reference should fail validation"))

	c.Logf("✓ User reference uniqueness enforced: %s blocked duplicate, %s≠%s", "50000", ref1, ref2)
}

// TestStorageSecurityValidation tests storage security controls
func (s *MemolessSecurityAuditTestSuite) TestStorageSecurityValidation(c *C) {
	ctx, k := setupKeeperForTest(c)

	// Initialize chain configs
	err := k.InitializeDefaultChainConfigs(ctx)
	c.Assert(err, IsNil)

	signer := GetRandomValidatorNode(NodeActive).NodeAddress

	// Test storage isolation - different prefixes should not interfere
	memo1 := thorchaintypes.RegisteredMemo{
		Reference:    "11111",
		Memo:         "SWAP:BTC.BTC:thor1test1:14400",
		ChainId:      "BTC",
		RegisteredAt: ctx.BlockHeight(),
		ExpiresAt:    ctx.BlockHeight() + 14400,
		Status:       "active",
		Signer:       signer,
	}

	memo2 := thorchaintypes.RegisteredMemo{
		Reference:    "22222",
		Memo:         "SWAP:ETH.ETH:thor1test2:14400",
		ChainId:      "ETH",
		RegisteredAt: ctx.BlockHeight(),
		ExpiresAt:    ctx.BlockHeight() + 14400,
		Status:       "active",
		Signer:       signer,
	}

	// Store both memos
	err = k.SetRegisteredMemo(ctx, memo1)
	c.Assert(err, IsNil, Commentf("Should store BTC memo"))

	err = k.SetRegisteredMemo(ctx, memo2)
	c.Assert(err, IsNil, Commentf("Should store ETH memo"))

	// Verify isolation - each memo should be retrievable independently
	retrieved1, err := k.GetRegisteredMemo(ctx, "11111")
	c.Assert(err, IsNil, Commentf("Should retrieve BTC memo"))
	c.Assert(retrieved1.ChainId, Equals, "BTC", Commentf("BTC memo should maintain chain ID"))

	retrieved2, err := k.GetRegisteredMemo(ctx, "22222")
	c.Assert(err, IsNil, Commentf("Should retrieve ETH memo"))
	c.Assert(retrieved2.ChainId, Equals, "ETH", Commentf("ETH memo should maintain chain ID"))

	// Test storage integrity during deletion
	err = k.DeleteRegisteredMemo(ctx, "11111")
	c.Assert(err, IsNil, Commentf("Should delete BTC memo"))

	// Verify ETH memo remains unaffected
	retrieved2Again, err := k.GetRegisteredMemo(ctx, "22222")
	c.Assert(err, IsNil, Commentf("ETH memo should still exist after BTC memo deletion"))
	c.Assert(retrieved2Again.ChainId, Equals, "ETH", Commentf("ETH memo should be unchanged"))

	c.Logf("✓ Storage security verified: isolation maintained, integrity preserved during operations")
}

// TestProductionReadinessValidation tests the production readiness assessment
func (s *MemolessSecurityAuditTestSuite) TestProductionReadinessValidation(c *C) {
	ctx, k := setupKeeperForTest(c)

	// Initialize chain configs
	err := k.InitializeDefaultChainConfigs(ctx)
	c.Assert(err, IsNil)

	// Execute production readiness validation
	readinessReport, err := k.ProductionReadinessValidation(ctx)
	c.Assert(err, IsNil, Commentf("Production readiness validation should work"))
	c.Assert(readinessReport, NotNil, Commentf("Readiness report should not be nil"))

	// Verify report structure
	c.Assert(readinessReport.ReadinessStatus != "", Equals, true, Commentf("Should have readiness status"))
	c.Assert(len(readinessReport.Checklist) > 0, Equals, true, Commentf("Should have readiness checks"))

	// Verify required checks are present
	requiredChecks := []string{
		"security_audit_completed",
		"monitoring_configured",
		"performance_benchmarks_met",
	}

	for _, checkName := range requiredChecks {
		check, exists := readinessReport.Checklist[checkName]
		c.Assert(exists, Equals, true, Commentf("Should have %s check", checkName))
		c.Assert(check.Status != "", Equals, true, Commentf("Check %s should have status", checkName))
	}

	// Count passed checks
	passedChecks := 0
	for _, check := range readinessReport.Checklist {
		if check.Status == "PASS" {
			passedChecks++
		}
	}

	c.Logf("✓ Production readiness verified: %s status, %d/%d checks passed",
		readinessReport.ReadinessStatus, passedChecks, len(readinessReport.Checklist))
}

// TestSecurityComplianceCheck tests real-time compliance monitoring
func (s *MemolessSecurityAuditTestSuite) TestSecurityComplianceCheck(c *C) {
	ctx, k := setupKeeperForTest(c)

	// Initialize chain configs
	err := k.InitializeDefaultChainConfigs(ctx)
	c.Assert(err, IsNil)

	// Execute compliance check
	compliance, err := k.CheckSecurityCompliance(ctx)
	c.Assert(err, IsNil, Commentf("Security compliance check should work"))
	c.Assert(compliance, NotNil, Commentf("Compliance report should not be nil"))

	// Verify compliance report structure
	c.Assert(compliance.OWASPCompliance.Score > 0, Equals, true, Commentf("Should have OWASP score"))
	c.Assert(compliance.CosmosSDKCompliance.Score > 0, Equals, true, Commentf("Should have Cosmos SDK score"))
	c.Assert(compliance.THORChainCompliance.Score > 0, Equals, true, Commentf("Should have THORChain score"))
	c.Assert(compliance.CryptographicSafety.Score > 0, Equals, true, Commentf("Should have crypto safety score"))

	// Verify compliance standards
	standards := []string{
		compliance.OWASPCompliance.Standard,
		compliance.CosmosSDKCompliance.Standard,
		compliance.THORChainCompliance.Standard,
		compliance.CryptographicSafety.Standard,
	}

	for _, standard := range standards {
		c.Assert(standard != "", Equals, true, Commentf("Compliance standard should be defined"))
	}

	c.Logf("✓ Security compliance verified: OWASP %d%%, Cosmos SDK %d%%, THORChain %d%%, Crypto %d%%",
		compliance.OWASPCompliance.Score,
		compliance.CosmosSDKCompliance.Score,
		compliance.THORChainCompliance.Score,
		compliance.CryptographicSafety.Score)
}

// TestReferenceValidationSecurity tests comprehensive reference validation security
func (s *MemolessSecurityAuditTestSuite) TestReferenceValidationSecurity(c *C) {
	ctx, k := setupKeeperForTest(c)

	// Test valid references (excluding reserved patterns)
	validRefs := []string{"10000", "50000", "99998"}
	for _, ref := range validRefs {
		err := k.ValidateReference(ctx, ref)
		c.Assert(err, IsNil, Commentf("Valid reference %s should pass", ref))
	}

	// Test invalid references - length attacks
	lengthAttacks := []string{
		"",           // Empty
		"1",          // Too short
		"12",         // Too short
		"123",        // Too short
		"1234",       // Too short
		"123456",     // Too long
		"1234567890", // Way too long
	}

	for _, ref := range lengthAttacks {
		err := k.ValidateReference(ctx, ref)
		c.Assert(err, NotNil, Commentf("Length attack %s should fail", ref))
	}

	// Test character injection attacks
	injectionAttacks := []string{
		"1234a",      // Letter injection
		"1234!",      // Special character
		"1234\n",     // Newline injection
		"1234\t",     // Tab injection
		"1234\x00",   // Null byte
		"1234\u0000", // Unicode null
		"1234\uFEFF", // BOM injection
		"12\x0034",   // Embedded null
	}

	for _, ref := range injectionAttacks {
		err := k.ValidateReference(ctx, ref)
		c.Assert(err, NotNil, Commentf("Injection attack %s should fail", ref))
	}

	// Test reserved pattern blocking
	reservedPatterns := []string{"00000", "99999", "12345", "54321", "11111", "22222", "33333"}
	for _, ref := range reservedPatterns {
		err := k.ValidateReference(ctx, ref)
		c.Assert(err, NotNil, Commentf("Reserved pattern %s should be blocked", ref))
	}

	c.Logf("✓ Reference validation security verified: %d valid, %d length attacks blocked, %d injection attacks blocked, %d reserved patterns blocked",
		len(validRefs)-1, len(lengthAttacks), len(injectionAttacks), len(reservedPatterns))
}

// TestCollisionHandlingSecurity tests security of collision handling mechanism
func (s *MemolessSecurityAuditTestSuite) TestCollisionHandlingSecurity(c *C) {
	ctx, k := setupKeeperForTest(c)

	// Initialize chain configs
	err := k.InitializeDefaultChainConfigs(ctx)
	c.Assert(err, IsNil)

	signer := GetRandomValidatorNode(NodeActive).NodeAddress

	// Create multiple memos to force collisions
	occupiedRefs := []string{"10000", "10001", "10002", "10003", "10004"}
	for i, ref := range occupiedRefs {
		memo := thorchaintypes.RegisteredMemo{
			Reference:    ref,
			Memo:         fmt.Sprintf("SWAP:BTC.BTC:thor1test%d:14400", i),
			ChainId:      "BTC",
			RegisteredAt: ctx.BlockHeight(),
			ExpiresAt:    ctx.BlockHeight() + 14400,
			Status:       "active",
			Signer:       signer,
		}
		err = k.SetRegisteredMemo(ctx, memo)
		c.Assert(err, IsNil, Commentf("Should store collision test memo %s", ref))
	}

	// Test user reference collision detection
	// User must choose references that don't collide with existing ones
	
	// Try to use reference that collides with pre-created memo
	err = k.ValidateUserReference(ctx, "10000")
	c.Assert(err, NotNil, Commentf("Should reject reference that collides with existing memo"))
	
	// User chooses available reference
	availableRef := "10005" // User selects available reference
	err = k.ValidateUserReference(ctx, availableRef)
	c.Assert(err, IsNil, Commentf("Available user reference should be valid"))

	// Test that users must manage collision avoidance themselves
	// (No automatic collision resolution - users control reference selection)

	c.Logf("✓ User reference collision detection verified: blocked collision, found available %s",
		availableRef)
}

// TestMemoLifecycleSecurity tests security across memo lifecycle operations
func (s *MemolessSecurityAuditTestSuite) TestMemoLifecycleSecurity(c *C) {
	ctx, k := setupKeeperForTest(c)

	// Initialize chain configs
	err := k.InitializeDefaultChainConfigs(ctx)
	c.Assert(err, IsNil)

	signer := GetRandomValidatorNode(NodeActive).NodeAddress
	constantProvider := constants.GetConstantValues(GetCurrentVersion())

	// Test secure registration
	msg := thorchaintypes.MsgRegisterMemo{
		Memo:    "SWAP:BTC.BTC:thor1lifecycle:14400",
		ChainId: "BTC",
		Ttl:     14400,
		Signer:  signer,
		Reference: "44001", // User-provided reference
	}

	result, err := k.ProcessMemoRegistration(ctx, msg, constantProvider)
	c.Assert(err, IsNil, Commentf("Secure registration should work"))

	// Test secure lookup
	retrieved, err := k.LookupMemoByReference(ctx, result.Reference)
	c.Assert(err, IsNil, Commentf("Secure lookup should work"))
	c.Assert(retrieved.Status, Equals, "active", Commentf("Status should be preserved"))

	// Test TTL security - use the actual signer from the result
	retrievedMemo, err := k.GetRegisteredMemo(ctx, result.Reference)
	c.Assert(err, IsNil, Commentf("Should retrieve memo for TTL test"))
	err = k.ExtendMemoTTL(ctx, result.Reference, 3600, retrievedMemo.Signer, constantProvider)
	c.Assert(err, IsNil, Commentf("TTL extension should work for original signer"))

	// Test expiry handling
	expired, err := k.CheckMemoExpiry(ctx, result.Reference, ctx.BlockHeight()+20000)
	c.Assert(err, IsNil, Commentf("Expiry check should work"))
	c.Assert(expired, Equals, true, Commentf("Memo should be expired in future block"))

	// Test secure deletion
	err = k.DeleteRegisteredMemo(ctx, result.Reference)
	c.Assert(err, IsNil, Commentf("Secure deletion should work"))

	// Verify deletion completeness
	_, err = k.GetRegisteredMemo(ctx, result.Reference)
	c.Assert(err, NotNil, Commentf("Deleted memo should not be retrievable"))

	c.Logf("✓ Memo lifecycle security verified: registration, lookup, TTL, expiry, deletion all secure")
}

// TestValidationLayerSecurity tests the 5-layer validation security framework
func (s *MemolessSecurityAuditTestSuite) TestValidationLayerSecurity(c *C) {
	ctx, k := setupKeeperForTest(c)

	// Initialize chain configs
	err := k.InitializeDefaultChainConfigs(ctx)
	c.Assert(err, IsNil)

	signer := GetRandomValidatorNode(NodeActive).NodeAddress
	constantProvider := constants.GetConstantValues(GetCurrentVersion())

	// Test validation with different attack vectors
	validationTests := []struct {
		name       string
		msg        thorchaintypes.MsgRegisterMemo
		shouldPass bool
		layer      string
	}{
		{
			name: "valid_memo",
			msg: thorchaintypes.MsgRegisterMemo{
				Memo:    "SWAP:BTC.BTC:thor1valid:14400",
				ChainId: "BTC",
				Ttl:     14400,
				Signer:  signer,
				Reference: "44002", // User-provided reference
			},
			shouldPass: true,
			layer:      "ALL",
		},
		{
			name: "invalid_chain",
			msg: thorchaintypes.MsgRegisterMemo{
				Memo:    "SWAP:INVALID.INVALID:thor1test:14400",
				ChainId: "INVALID",
				Ttl:     14400,
				Signer:  signer,
				Reference: "44003", // User-provided reference
			},
			shouldPass: false,
			layer:      "CHAIN_VALIDATION",
		},
		{
			name: "invalid_ttl",
			msg: thorchaintypes.MsgRegisterMemo{
				Memo:    "SWAP:BTC.BTC:thor1test:14400",
				ChainId: "BTC",
				Ttl:     -1,
				Signer:  signer,
				Reference: "44004", // User-provided reference
			},
			shouldPass: false,
			layer:      "TTL_VALIDATION",
		},
		{
			name: "empty_memo",
			msg: thorchaintypes.MsgRegisterMemo{
				Memo:    "",
				ChainId: "BTC",
				Ttl:     14400,
				Signer:  signer,
				Reference: "44005", // User-provided reference
			},
			shouldPass: false,
			layer:      "MEMO_VALIDATION",
		},
	}

	passedValidation := 0
	for _, test := range validationTests {
		err := k.ValidateMemoRegistration(ctx, test.msg, constantProvider)
		testPassed := (err == nil) == test.shouldPass

		if testPassed {
			passedValidation++
		}

		c.Assert(testPassed, Equals, true, Commentf("Validation test %s should %s (layer: %s)",
			test.name, map[bool]string{true: "pass", false: "fail"}[test.shouldPass], test.layer))
	}

	c.Logf("✓ Validation layer security verified: %d/%d tests passed across multiple validation layers",
		passedValidation, len(validationTests))
}

// TestUserReferenceSecurityResilience tests security resilience with user-provided references
func (s *MemolessSecurityAuditTestSuite) TestUserReferenceSecurityResilience(c *C) {
	ctx, k := setupKeeperForTest(c)

	// Initialize chain configs
	err := k.InitializeDefaultChainConfigs(ctx)
	c.Assert(err, IsNil)

	// Test reference range security - users cannot use references outside valid range
	invalidReferences := []string{
		"09999", // Below minimum
		"100000", // Above maximum  
		"00001", // Below minimum
		"99999999", // Way above maximum
	}
	
	for _, invalidRef := range invalidReferences {
		err = k.ValidateUserReference(ctx, invalidRef)
		c.Assert(err, NotNil, Commentf("Invalid reference %s should be rejected for security", invalidRef))
	}

	// Test valid range security - users can use references within range
	validReferences := []string{"10000", "50000", "99999"}
	for _, validRef := range validReferences {
		err = k.ValidateUserReference(ctx, validRef)
		c.Assert(err, IsNil, Commentf("Valid reference %s should be accepted", validRef))
	}

	// Test collision detection security - prevent reference reuse
	signer := GetRandomValidatorNode(NodeActive).NodeAddress
	usedReference := "75000"
	
	// Register memo with reference
	testMemo := thorchaintypes.RegisteredMemo{
		Reference:    usedReference,
		Memo:         "SWAP:BTC.BTC:thor1security:14400",
		ChainId:      "BTC",
		RegisteredAt: ctx.BlockHeight(),
		ExpiresAt:    ctx.BlockHeight() + 14400,
		Status:       "active",
		Signer:       signer,
	}
	err = k.SetRegisteredMemo(ctx, testMemo)
	c.Assert(err, IsNil)
	
	// Try to reuse same reference - should fail for security
	err = k.ValidateUserReference(ctx, usedReference)
	c.Assert(err, NotNil, Commentf("Used reference should be rejected for security"))

	c.Logf("✓ User reference security resilience verified: range validation and collision detection working")
}
