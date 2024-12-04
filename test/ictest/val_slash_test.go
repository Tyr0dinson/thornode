package ictest

import (
	"context"
	"fmt"
	"testing"

	"github.com/strangelove-ventures/interchaintest/v8"
	tc "github.com/strangelove-ventures/interchaintest/v8/chain/thorchain"
	"github.com/strangelove-ventures/interchaintest/v8/ibc"
	"github.com/strangelove-ventures/interchaintest/v8/testutil"
	"github.com/stretchr/testify/require"
)

// TestValSlash tests that slashing occur when they should
func TestValSlash(t *testing.T) {
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
		mainnet     = false

		initialRepo    = "thorchain"
		initialVersion = "local"
		imageUidGid    = "1025:1025"
	)
	
	// Start non-thorchain chains
	exoChains := StartExoChains(t, ctx, client, network, []*interchaintest.ChainSpec{
		BtcChainSpec(),
	})
 
	// Start thorchain
	chainImage := ibc.NewDockerImage(initialRepo, initialVersion, imageUidGid)
	thorchain := StartThorchain(t, ctx, client, network, numVals, numFns, mainnet, chainImage, exoChains, "", "")

	// Add validator for first churn
	addValidators(t, ctx, thorchain, 3)

	// Test missed blocks slashing by stopping a validator
	disabledVal := missedBlocksSlashTest(t, ctx, thorchain)

	// Test double sign slashing by duplicating a validator node
	doubleSignSlashTest(t, ctx, thorchain, disabledVal)

	// Verify no more slashings occur
	verifySlashPointsUnchanged(t, ctx, thorchain)

	// TODO: fail keygen slash
	// TODO: fail keysign slash
	// TODO: observe slash
	// TODO: bad val slash
	// TODO: banned node slash
		
	// Wait for churn
	err := testutil.WaitForBlocks(ctx, 1, thorchain)
	require.NoError(t, err, "thorchain failed to make blocks")

}

func addValidators(t *testing.T, ctx context.Context, thorchain *tc.Thorchain, numValsToAdd int) {
	// Add new validators
	err := thorchain.AddValidators(ctx, nil, numValsToAdd)
	require.NoError(t, err)

	// Wait 20 blocks to get close to the first churn
	err = testutil.WaitForBlocks(ctx, 20, thorchain)
	require.NoError(t, err, "thorchain failed to make blocks")

	// Wait for all vals to be active, then disable 1 val
	for i := 0; i < 20; i++ {
		numActiveVals := getNumActiveVals(t, ctx, thorchain)	
		err = testutil.WaitForBlocks(ctx, 5, thorchain)
		require.NoError(t, err, "thorchain failed to make blocks")

		if numActiveVals == len(thorchain.Validators) {
			break
		}
	}
}

func missedBlocksSlashTest(t *testing.T, ctx context.Context, thorchain *tc.Thorchain) *tc.ChainNode {
	valIndexToDisable := 3
	disabledVal := thorchain.Validators[valIndexToDisable]
	
	// Stop a validator to accumulate no votes
	err := disabledVal.StopContainer(ctx)
	require.NoError(t, err)
	disabledValHeight, err := thorchain.Height(ctx)
	require.NoError(t, err)

	err = testutil.WaitForBlocks(ctx, 10, thorchain)
	require.NoError(t, err, "thorchain failed to make blocks")
	
	// Verify val's slash pts are as expected
	height, err := thorchain.Height(ctx)
	require.NoError(t, err)
	disabledValNodeInfo, err := thorchain.APIGetNode(ctx, disabledVal.NodeAccount.NodeAddress)
	require.NoError(t, err)
	heightSinceDisabled := height-disabledValHeight
	fmt.Printf("\n\nHeight diff: %d, slash pts: %d\n\n", heightSinceDisabled, disabledValNodeInfo.SlashPoints)
	if heightSinceDisabled != disabledValNodeInfo.SlashPoints && heightSinceDisabled-1 != disabledValNodeInfo.SlashPoints {
		require.Fail(t, "unexpected slash points on disabled validator")
	}

	return disabledVal
}

func doubleSignSlashTest(t *testing.T, ctx context.Context, thorchain *tc.Thorchain, disabledVal *tc.ChainNode) {
	valIndexToDuplicate := 2

	// spin up a duplicate validator (time should be different so we double sign)
	valToDuplicate := thorchain.Validators[valIndexToDuplicate]
	duplicateVal, err := thorchain.AddDuplicateValidator(ctx, nil, valToDuplicate)
	require.NoError(t, err)

	err = testutil.WaitForBlocks(ctx, 20, thorchain)
	require.NoError(t, err, "thorchain failed to make blocks")

	duplicateValNodeInfo, err := thorchain.APIGetNode(ctx, duplicateVal.NodeAccount.NodeAddress)
	require.NoError(t, err)
	fmt.Println("Duplicate Val slash points:", duplicateValNodeInfo.SlashPoints)
	require.GreaterOrEqual(t, duplicateValNodeInfo.SlashPoints, int64(1000), "duplicate validator does not have double sign slash points")

	// Start disabled validator back up
	// We do this here to improve the test reliability of catching double signing
	err = disabledVal.StartContainer(ctx)
	require.NoError(t, err)

	// Duplicate vals don't start sidecars, so remove them for when we stop
	duplicateVal.Sidecars = nil

	// Remove duplicate validator
	err = duplicateVal.StopContainer(ctx)
	require.NoError(t, err)
}

func verifySlashPointsUnchanged(t *testing.T, ctx context.Context, thorchain *tc.Thorchain) {
	// Wait 2 blocks to remove any pending val slashing from pipeline
	err := testutil.WaitForBlocks(ctx, 2, thorchain)
	require.NoError(t, err, "thorchain failed to make blocks")

	// Verify slashings have ended
	na, err := thorchain.APIGetNodes(ctx)
	require.NoError(t, err)
	err = testutil.WaitForBlocks(ctx, 10, thorchain)
	require.NoError(t, err, "thorchain failed to make blocks")
	na2, err := thorchain.APIGetNodes(ctx)
	require.NoError(t, err)
	for i := range na {
		require.Equal(t, na[i].SlashPoints, na2[i].SlashPoints, fmt.Sprintf("Node %d, %s, slash points changed", i, na[i].NodeAddress))
	}
}

func getNumActiveVals(t *testing.T, ctx context.Context, thorchain *tc.Thorchain) (numActiveVals int) {
	na, err := thorchain.APIGetNodes(ctx)
	require.NoError(t, err)
		
	for _, node := range na {
		if node.Status == "Active" {
			numActiveVals++
		}
	}

	return numActiveVals
}