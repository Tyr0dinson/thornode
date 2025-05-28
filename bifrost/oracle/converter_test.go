package oracle

import (
	"github.com/stretchr/testify/assert"
	"math/big"
	"testing"
)

func TestDeviation(t *testing.T) {
	values := []*big.Float{
		big.NewFloat(32),
		big.NewFloat(37),
		big.NewFloat(28),
		big.NewFloat(19),
		big.NewFloat(35),
	}

	deviation, mean, err := ComputeStandardDeviation(values)
	assert.Nil(t, err)
	assert.Equal(t, "6.36867333", deviation.Text('f', 8))
	assert.Equal(t, "30.2", mean.Text('f', -1))
}
