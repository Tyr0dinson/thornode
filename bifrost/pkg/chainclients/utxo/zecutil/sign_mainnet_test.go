//go:build !mocknet
// +build !mocknet

package zecutil

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSigHashKeyMainnetNU62(t *testing.T) {
	require.Equal(t,
		append([]byte(blake2BSigHash), 0xF0, 0x4D, 0xEC, 0x4D),
		sigHashKey(3364599),
	)
	require.Equal(t,
		append([]byte(blake2BSigHash), 0x30, 0xF3, 0x37, 0x54),
		sigHashKey(3364600),
	)
}
