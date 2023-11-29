package mimir_test

import (
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
		mimir := mimir.NewPauseBond("bond")
		assert.NotNil(t, mimir, "Mimir should not be nil")
		assert.Equal(t, "PAUSEBONDING.BOND", mimir.Name(), "Incorrect name for bond reference")
		assert.Equal(t, "Pauses bonding", mimir.Description(), "Incorrect description for bond reference")
	})

	// Test case for "unbond" reference
	t.Run("unbond reference", func(t *testing.T) {
		mimir := mimir.NewPauseBond("unbond")
		assert.NotNil(t, mimir, "Mimir should not be nil")
		assert.Equal(t, "PAUSEBONDING.UNBOND", mimir.Name(), "Incorrect name for unbond reference")
		assert.Equal(t, "Pauses unbonding", mimir.Description(), "Incorrect description for unbond reference")
	})

	// Test case for invalid reference
	t.Run("invalid reference", func(t *testing.T) {
		mimir := mimir.NewPauseBond("invalid_ref")
		assert.Nil(t, mimir, "Mimir should be nil for invalid reference")
	})
}

func TestNewPauseChain(t *testing.T) {
	// Test for a supported chain
	t.Run("supported chain", func(t *testing.T) {
		mimir := mimir.NewPauseChain("avax")
		assert.NotNil(t, mimir, "Expected non-nil Mimir for supported chain")
		assert.Equal(t, "PAUSECHAIN.AVAX", mimir.Name(), "Incorrect name for supported chain")
		assert.Equal(t, "AVAX", mimir.Reference(), "Incorrect reference for supported chain")
		assert.Equal(t, "Halts interaction with the avax chain", mimir.Description(), "Incorrect description for supported chain")
		assert.Equal(t, "HALTAVAXCHAIN", strings.ToUpper(mimir.LegacyKey("avax")), "Incorrect legacy key for supported chain")
	})
	// Test for an unsupported chain
	t.Run("unsupported chain", func(t *testing.T) {
		mimir := mimir.NewPauseChain("unsupported")
		assert.Nil(t, mimir, "Expected nil Mimir for unsupported chain")
	})
}
