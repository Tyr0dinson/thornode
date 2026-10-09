//go:build mocknet
// +build mocknet

package thorchain

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestXMRMonovaultGenesisDelayFromEnvCovC(t *testing.T) {
	for _, tc := range []struct {
		env  string
		want int64
	}{
		{"", 128},
		{"7", 7},
		{"0", 128},
		{"-5", 128},
		{"abc", 128},
	} {
		t.Setenv("XMR_MONOVAULT_GENESIS_DELAY_BLOCKS", tc.env)
		require.Equal(t, tc.want, xmrMonovaultGenesisDelayFromEnv(), "env=%q", tc.env)
	}
}
