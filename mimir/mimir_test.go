package mimir_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"gitlab.com/thorchain/thornode/constants"
	"gitlab.com/thorchain/thornode/mimir"
)

func TestNewAffiliateFeeBasisPointsMax(t *testing.T) {
	expectedDefaultValue := constants.NewConstantValue().GetInt64Value(constants.MaxAffiliateFeeBasisPoints)
	// Call the function without any reference strings
	m := mimir.NewAffiliateFeeBasisPointsMax()
	// Assert that the defaultValue is as expected
	if m.DefaultValue() != expectedDefaultValue {
		t.Errorf("Expected default value %d, got %d", expectedDefaultValue, m.DefaultValue())
	}
}

func TestNewPauseBond(t *testing.T) {
	// Test case for "bond" reference
	t.Run("bond reference", func(t *testing.T) {
		mimir := mimir.NewBondPause("bond")
		assert.NotNil(t, mimir, "Mimir should not be nil")
		assert.Equal(t, "PAUSEBONDINGBOND", mimir.Name(), "Incorrect name for bond reference")
		assert.Equal(t, "Pauses bonding", mimir.Description(), "Incorrect description for bond reference")
	})
	// Test case for "unbond" reference
	t.Run("unbond reference", func(t *testing.T) {
		mimir := mimir.NewBondPause("unbond")
		assert.NotNil(t, mimir, "Mimir should not be nil")
		assert.Equal(t, "PAUSEBONDINGUNBOND", mimir.Name(), "Incorrect name for unbond reference")
		assert.Equal(t, "Pauses unbonding", mimir.Description(), "Incorrect description for unbond reference")
	})
}

func TestNewPauseChain(t *testing.T) {
	// Test for a supported chain
	t.Run("supported chain", func(t *testing.T) {
		mimir := mimir.NewPauseChain("avax")
		assert.NotNil(t, mimir, "Expected non-nil Mimir for supported chain")
		assert.Equal(t, "PAUSECHAINAVAX", mimir.Name(), "Incorrect name for supported chain")
		assert.Equal(t, "AVAX", mimir.Reference(), "Incorrect reference for supported chain")
		assert.Equal(t, "Halts interaction with the avax chain", mimir.Description(), "Incorrect description for supported chain")
		assert.Equal(t, "HALTAVAXCHAIN", strings.ToUpper(mimir.LegacyKey("avax")), "Incorrect legacy key for supported chain")
	})
	t.Run("thor chain", func(t *testing.T) {
		mimir := mimir.NewPauseChain("thor")
		assert.NotNil(t, mimir, "Expected non-nil Mimir for supported chain")
		assert.Equal(t, "PAUSECHAINTHOR", mimir.Name(), "Incorrect name for supported chain")
		assert.Equal(t, "THOR", mimir.Reference(), "Incorrect reference for supported chain")
		assert.Equal(t, "Halts interaction with the thor chain", mimir.Description(), "Incorrect description for supported chain")
		assert.Equal(t, "HALTTHORCHAIN", strings.ToUpper(mimir.LegacyKey("thor")), "Incorrect legacy key for supported chain")
	})
}

func TestNewPauseSynths(t *testing.T) {
	// Test for "burn" reference
	t.Run("burn reference", func(t *testing.T) {
		mimir := mimir.NewPauseSynths("burn")
		assert.NotNil(t, mimir, "Expected non-nil Mimir for 'burn' reference")
		assert.Equal(t, "PAUSESYNTHSBURN", mimir.Name(), "Incorrect name for 'burn' reference")
		assert.Equal(t, "Pauses the burning of synths", mimir.Description(), "Incorrect description for 'burn' reference")
		assert.Equal(t, "BURNSYNTHS", mimir.LegacyKey("burn"), "Incorrect legacy key for 'burn' reference")
	})
	// Test for "mint" reference
	t.Run("mint reference", func(t *testing.T) {
		mimir := mimir.NewPauseSynths("mint")
		assert.NotNil(t, mimir, "Expected non-nil Mimir for 'mint' reference")
		assert.Equal(t, "PAUSESYNTHSMINT", mimir.Name(), "Incorrect name for 'mint' reference")
		assert.Equal(t, "Pauses the minting of synths", mimir.Description(), "Incorrect description for 'mint' reference")
		assert.Equal(t, "MINTSYNTHS", mimir.LegacyKey("mint"), "Incorrect legacy key for 'mint' reference")
	})
}

func TestNewPauseSwaps(t *testing.T) {
	// Test for a supported chain
	t.Run("supported chain", func(t *testing.T) {
		mimir := mimir.NewPauseSwaps("avax")
		assert.NotNil(t, mimir, "Expected non-nil Mimir for supported chain")
		assert.Equal(t, "PAUSESWAPSAVAX", mimir.Name(), "Incorrect name for supported chain")
		assert.Equal(t, "AVAX", mimir.Reference(), "Incorrect reference for supported chain")
		assert.Equal(t, "Pauses swaps on avax chain", mimir.Description(), "Incorrect description for supported chain")
		assert.Equal(t, "HALTAVAXTRADING", mimir.LegacyKey("avax"), "Incorrect legacy key for supported chain")
	})
}

func TestNewPauseLP(t *testing.T) {
	// Test for a supported chain
	t.Run("supported chain", func(t *testing.T) {
		mimir := mimir.NewPauseLP("bnb")
		assert.NotNil(t, mimir, "Expected non-nil Mimir for supported chain")
		assert.Equal(t, "PAUSELPBNB", mimir.Name(), "Incorrect name for supported chain")
		assert.Equal(t, "BNB", mimir.Reference(), "Incorrect reference for supported chain")
		assert.Equal(t, "Pauses the ability for LPs to add/remove liquidity", mimir.Description(), "Incorrect description for supported chain")
		assert.Equal(t, "PAUSELPBNB", mimir.LegacyKey("bnb"), "Incorrect legacy key for supported chain")
	})
}

func TestNewPauseSigning(t *testing.T) {
	// Test for a supported chain
	t.Run("supported chain", func(t *testing.T) {
		mimir := mimir.NewPauseSigning("doge")
		assert.NotNil(t, mimir, "Expected non-nil Mimir for supported chain")
		assert.Equal(t, "PAUSESIGNINGDOGE", mimir.Name(), "Incorrect name for supported chain")
		assert.Equal(t, "DOGE", mimir.Reference(), "Incorrect reference for supported chain")
		assert.Equal(t, "Halts signing", mimir.Description(), "Incorrect description for supported chain")
		assert.Equal(t, "HALTSIGNINGDOGE", mimir.LegacyKey("doge"), "Incorrect legacy key for supported chain")
	})
}

// TestNewAnchor tests the NewAnchor function for creating a Mimir object.
func TestNewAnchor(t *testing.T) {
	// Test with a specific input reference
	t.Run("specific reference", func(t *testing.T) {
		// Example input reference
		inputRef := "AnchorBNB-BUSD-BD1"

		// Calling the NewAnchor function
		mimir := mimir.NewAnchor(inputRef)

		// Assertions to validate the behavior of NewAnchor function
		assert.NotNil(t, mimir, "Expected non-nil Mimir for given reference")
		assert.Contains(t, mimir.Name(), "ANCHOR", "Name should contain 'Anchor'")
		assert.Equal(t, strings.ToUpper(inputRef), mimir.Reference(), "Incorrect reference for given input")
		assert.Equal(t, "Allow listed asset to be included in the TOR Price Anchor.", mimir.Description(), "Incorrect description")
		assert.Equal(t, fmt.Sprintf("TORANCHOR-%s", strings.ToUpper(inputRef)), mimir.LegacyKey(inputRef), "Incorrect legacy key for given input")
	})
}
