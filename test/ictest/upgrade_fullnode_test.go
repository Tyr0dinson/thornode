package ictest

import (
	"context"
	_ "embed"
	"fmt"
	"testing"
	"time"

	"github.com/strangelove-ventures/interchaintest/v8"
	tc "github.com/strangelove-ventures/interchaintest/v8/chain/thorchain"
	"github.com/strangelove-ventures/interchaintest/v8/ibc"

	"github.com/docker/docker/client"
	"github.com/stretchr/testify/require"
)


func TestFullnodeUpgrade(t *testing.T) {
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
		numVals     = 1
		numFns      = 0
		mainnet     = true

		initialRepo    = "thorchain"
		initialVersion = "v2.137.3-mainnet"
		imageUidGid    = "1025:1025"
	)

	// Start thorchain
	chainImage := ibc.NewDockerImage(initialRepo, initialVersion, imageUidGid)
	thorchain := StartMainnetThorchain(t, ctx, client, network, numVals, numFns, mainnet, chainImage)

	// --------------------------------------------------------
	// Upgrade
	// --------------------------------------------------------
	FullnodeUpgrade(t, ctx, client, thorchain)
}

func FullnodeUpgrade(t *testing.T, ctx context.Context, client *client.Client, thorchain *tc.Thorchain) {
	// Start and sync a fullnode
	fmt.Printf("\n  ----------------\n  Starting fullnode\n  ----------------\n")
	err := thorchain.AddFullNodes(ctx, nil, 1)
	require.NoError(t, err)

	fmt.Println("Fullnode started, check it")
	time.Sleep(time.Second * 5)
	
	// Stop fullnode
	fmt.Printf("\n  ----------------\n  Stopping fullnode\n  ----------------\n")
	err = thorchain.FullNodes[0].StopContainer(ctx)
	require.NoError(t, err)
	err = thorchain.FullNodes[0].RemoveContainer(ctx)
	require.NoError(t, err)
	
	// Switch out image
	newImage := ibc.NewDockerImage("thorchain", "v3.0.1-mainnet", "1025:1025")
	thorchain.FullNodes[0].Image = newImage

	
	// Start fullnode
	fmt.Printf("\n  ----------------\n  Starting v3.0.1 fullnode\n  ----------------\n")
	err = thorchain.FullNodes[0].CreateNodeContainer(ctx)
	require.NoError(t, err)
	err = thorchain.FullNodes[0].StartContainer(ctx)
	require.NoError(t, err)
	fmt.Println("Fullnode v3.0.1 started, check it")
	
	// Check fullnode output
	time.Sleep(time.Second * 100)

}