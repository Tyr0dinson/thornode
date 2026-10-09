//go:build !regtest
// +build !regtest

package app

import (
	"os"
	"os/exec"
	"testing"

	"cosmossdk.io/log"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"

	"gitlab.com/thorchain/thornode/v3/config"
)

// haltHeightCovEnv makes the test binary run the halt height scenario instead of
// the tests (see TestMain), since BeginBlocker exits the process at the halt height.
const haltHeightCovEnv = "THORNODE_APP_HALT_HEIGHT_COV"

// exit code used when BeginBlocker returns instead of exiting
const haltHeightCovNoExit = 3

func TestMain(m *testing.M) {
	if os.Getenv(haltHeightCovEnv) == "1" {
		config.Init()
		ctx := sdk.Context{}.WithBlockHeight(config.GetThornode().Cosmos.HaltHeight).WithLogger(log.NewNopLogger())
		_, _ = (&THORChainApp{}).BeginBlocker(ctx)
		os.Exit(haltHeightCovNoExit)
	}
	os.Exit(m.Run())
}

func TestBeginBlockerHaltHeightCov(t *testing.T) {
	cmd := exec.Command(os.Args[0]) // #nosec G204 -- re-executes this test binary
	cmd.Env = append(os.Environ(), haltHeightCovEnv+"=1", "HARDFORK_BLOCK_HEIGHT=7")
	out, err := cmd.CombinedOutput()
	// the process exits cleanly once the halt height is reached
	require.NoError(t, err, string(out))
	require.Equal(t, 0, cmd.ProcessState.ExitCode())
}
