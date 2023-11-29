package mimir_test

import (
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
