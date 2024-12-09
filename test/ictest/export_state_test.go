package ictest

import (
	"context"
	"fmt"
	"testing"

	"github.com/strangelove-ventures/interchaintest/v8"
	"github.com/strangelove-ventures/interchaintest/v8/ibc"

	"github.com/stretchr/testify/require"
)

func TestExportState(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping in short mode")
	}
	t.Parallel()

	client, network := interchaintest.DockerSetup(t)
	ctx := context.Background()

	// ----------------------------
	// Set up thorchain and others
	// ----------------------------
	const (
		numVals     = 4
		numFns      = 0
		mainnet     = false

		initialRepo    = "ghcr.io/strangelove-ventures/heighliner/thorchain"
		initialVersion = "v2.137.3-mocknet"
		imageUidGid    = "1025:1025"
	)

	// Start chain with 1 validator, then validators may be churned in (current bifrost requirement)
	initialValSetSize := 1
	// Start non-thorchain chains
	exoChains := StartExoChains(t, ctx, client, network, []*interchaintest.ChainSpec{
		BtcChainSpec(),
	}) 
	// Start thorchain
	chainImage := ibc.NewDockerImage(initialRepo, initialVersion, imageUidGid)
	thorchain := StartThorchain(t, ctx, client, network, initialValSetSize, numFns, mainnet, chainImage, exoChains, "", "")

	// Add validators
	if numVals > 1 {
		addValidators(t, ctx, thorchain, numVals-1)
	}
	
	err := thorchain.StopAllSidecars(ctx)
	require.NoError(t, err, "error stopping sidecar(s)")

	// bring down nodes to prepare for upgrade
	err = thorchain.StopAllNodes(ctx)
	require.NoError(t, err, "error stopping node(s)")

	state, err := thorchain.ExportState(ctx, -1)
	require.NoError(t, err)

	fmt.Println("State:", state)
}
