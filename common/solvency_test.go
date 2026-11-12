package common

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSolvencyRoundTripAndSignedBalance(t *testing.T) {
	original := Solvency{Chain: DASHChain, PubKey: PubKey("vault"), Height: 123}
	id, err := original.Hash()
	require.NoError(t, err)
	data, err := original.Marshal()
	require.NoError(t, err)
	var decoded Solvency
	require.NoError(t, decoded.Unmarshal(data))
	same, err := decoded.Hash()
	require.NoError(t, err)
	require.Equal(t, id, same)
	decoded.Height++
	changed, err := decoded.Hash()
	require.NoError(t, err)
	require.NotEqual(t, id, changed)
	var missing *Solvency
	require.Error(t, missing.Validate())
}
