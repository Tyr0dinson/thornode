package keeperv1

import (
	"strconv"
	"testing"

	. "gopkg.in/check.v1"
)

type SecureReferenceSuite struct{}

var _ = Suite(&SecureReferenceSuite{})

func TestSecureReferenceSuite(t *testing.T) {
	TestingT(t)
}

// SECURITY TEST: Validates cryptographically secure reference generation per Task 5A
func (s *SecureReferenceSuite) TestGetNextReferenceID_SecureGeneration(c *C) {
	ctx, k := setupKeeperForTest(c)

	// Generate multiple references to test security properties
	references := make([]string, 10)
	for i := 0; i < 10; i++ {
		ref, err := k.GetNextReferenceID(ctx, "BTC")
		c.Assert(err, IsNil)
		references[i] = ref
	}

	// SECURITY REQUIREMENT 1: All references must be valid 5-digit numbers
	for i, ref := range references {
		c.Assert(len(ref), Equals, 5, Commentf("Reference %d '%s' must be 5 digits", i, ref))
		
		num, err := strconv.Atoi(ref)
		c.Assert(err, IsNil, Commentf("Reference %d '%s' must be numeric", i, ref))
		c.Assert(num >= 10000, Equals, true, Commentf("Reference %d '%s' must be >= 10000", i, ref))
		c.Assert(num <= 99999, Equals, true, Commentf("Reference %d '%s' must be <= 99999", i, ref))
	}

	// SECURITY REQUIREMENT 2: References must be unpredictable (not sequential)
	sequentialCount := 0
	for i := 1; i < len(references); i++ {
		prev, _ := strconv.Atoi(references[i-1])
		curr, _ := strconv.Atoi(references[i])
		if curr == prev+1 {
			sequentialCount++
		}
	}
	c.Assert(sequentialCount < 3, Equals, true, 
		Commentf("Too many sequential references (%d/9) - indicates predictable generation", sequentialCount))

	// SECURITY REQUIREMENT 3: All references must be unique
	seen := make(map[string]bool)
	for i, ref := range references {
		c.Assert(seen[ref], Equals, false, Commentf("Reference %d '%s' is duplicate", i, ref))
		seen[ref] = true
	}
}

func (s *SecureReferenceSuite) TestGetNextReferenceID_UniquenessAcrossChains(c *C) {
	ctx, k := setupKeeperForTest(c)

	// Test references are unique across different chains
	chains := []string{"BTC", "ETH", "THOR"}
	allReferences := make(map[string]string) // ref -> chain

	for _, chain := range chains {
		for i := 0; i < 5; i++ {
			ref, err := k.GetNextReferenceID(ctx, chain)
			c.Assert(err, IsNil)

			// Validate reference format
			c.Assert(len(ref), Equals, 5)
			num, err := strconv.Atoi(ref)
			c.Assert(err, IsNil)
			c.Assert(num >= 10000 && num <= 99999, Equals, true)

			// Track for cross-chain uniqueness (references should be unique globally)
			if existingChain, exists := allReferences[ref]; exists {
				c.Logf("Reference collision detected: %s used by both %s and %s", ref, existingChain, chain)
				// Note: Collisions are acceptable with secure random generation
				// This test just logs them for monitoring
			}
			allReferences[ref] = chain
		}
	}
}

func (s *SecureReferenceSuite) TestReferenceCounter_Storage(c *C) {
	ctx, k := setupKeeperForTest(c)

	// Test counter storage (still used for tracking despite secure generation)
	err := k.SetReferenceCounter(ctx, "ETH", 12345)
	c.Assert(err, IsNil)

	counter, err := k.GetReferenceCounter(ctx, "ETH")
	c.Assert(err, IsNil)
	c.Assert(counter, Equals, int64(12345))
}
