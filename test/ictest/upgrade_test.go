package ictest

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/strangelove-ventures/interchaintest/v8"
	"github.com/strangelove-ventures/interchaintest/v8/ibc"
	"gitlab.com/thorchain/thornode/test/ictest/features"
	"golang.org/x/sync/errgroup"

	"github.com/strangelove-ventures/interchaintest/v8/testutil"
	"github.com/stretchr/testify/require"
)

const (
	haltHeightDelta    = 10 // will propose upgrade this many blocks in the future
	blocksAfterUpgrade = 10
)

func TestUpgrade(t *testing.T) {
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
		numVals     = 1 // 2
		numFns      = 0 // 1
		imageUidGid = "1025:1025"
		mainnet     = false

		initialRepo    = "ghcr.io/strangelove-ventures/heighliner/thorchain"
		initialVersion = "mocknet-v2.136.0"

		upgradeRepo    = "thorchain"
		upgradeVersion = "mocknet-local"

		upgradeName = "2.137.0"
	)

	// Start non-thorchain chains
	exoChains := StartExoChains(t, ctx, client, network, EthChainSpec("geth"))
	ethRouterContractAddress, err := SetupGethContracts(ctx, exoChains["ETH"])
	require.NoError(t, err, "error setting up contracts for eth and bsc")

	chainImage := ibc.NewDockerImage(initialRepo, initialVersion, imageUidGid)

	// Start thorchain
	tc := StartThorchain(t, ctx, client, network, numVals, numFns, mainnet, chainImage, exoChains, ethRouterContractAddress, "")

	// --------------------------------------------------------
	// Bootstrap pool
	// --------------------------------------------------------
	eg, egCtx := errgroup.WithContext(ctx)
	for _, exoChain := range exoChains {
		exoChain := exoChain
		eg.Go(func() error {
			_, lper, err := features.DualLp(t, egCtx, tc, exoChain.chain)
			if err != nil {
				return err
			}
			exoChain.lpers = append(exoChain.lpers, lper)
			return nil
		})
	}
	require.NoError(t, eg.Wait())

	height, err := tc.Height(ctx)
	require.NoError(t, err, "error fetching height before submit upgrade proposal")

	haltHeight := height + haltHeightDelta

	// submit software upgrade proposal from first validator
	_, err = tc.Validators[0].ExecTx(ctx, "thorchain", "thorchain", "propose-upgrade", upgradeName, fmt.Sprint(haltHeight))
	require.NoError(t, err, "error submitting software upgrade proposal tx")

	// we have two validators, so we need to approve the upgrade
	// from the second validator to reach quorum schedule the upgrade.

	// the upgrade plan should not be scheduled on-chain yet.
	// _, _, err = tc.GetNode().ExecQuery(ctx, "upgrade", "plan")
	// require.Error(t, err, "expected error from upgrade plan query")

	// // approve software upgrade from second validator
	// _, err = tc.Validators[1].ExecTx(ctx, "thorchain", "thorchain", "approve-upgrade", upgradeName)
	// require.NoError(t, err, "error submitting software upgrade approval tx")

	// ensure on-chain scheduled upgrade plan matches what was proposed and approved
	stdout, _, err := tc.GetNode().ExecQuery(ctx, "upgrade", "plan")
	require.NoError(t, err, "expected success from upgrade plan query")

	plan := make(map[string]any)
	require.NoError(t, json.Unmarshal(stdout, &plan))
	require.Equal(t, upgradeName, plan["name"], "upgrade name mismatch")
	require.Equal(t, fmt.Sprint(haltHeight), plan["height"], "upgrade height mismatch")

	// now wait for chain to halt at upgrade height

	height, err = tc.Height(ctx)
	require.NoError(t, err, "error fetching height before upgrade")

	timeoutCtx, timeoutCtxCancel := context.WithTimeout(ctx, time.Second*45)
	defer timeoutCtxCancel()

	// this should timeout due to chain halt at upgrade height.
	_ = testutil.WaitForBlocks(timeoutCtx, int(haltHeight-height)+1, tc)

	height, err = tc.Height(ctx)
	require.NoError(t, err, "error fetching height after chain should have halted")

	// make sure that chain is halted
	require.Equal(t, haltHeight, height, "height is not equal to halt height")

	// bring down nodes to prepare for upgrade
	err = tc.StopAllNodes(ctx)
	require.NoError(t, err, "error stopping node(s)")

	// upgrade version on all nodes
	newImage := ibc.NewDockerImage(upgradeRepo, upgradeVersion, imageUidGid)

	// not necessary for local images and will error. needed for remote images, so leaving like this to support both.
	_ = newImage.PullImage(ctx, client)

	for _, n := range tc.Nodes() {
		n.Image = newImage
	}

	// start all nodes back up.
	// validators reach consensus on first block after upgrade height
	// and chain block production resumes.
	err = tc.StartAllNodes(ctx)
	require.NoError(t, err, "error starting upgraded node(s)")

	timeoutCtx, timeoutCtxCancel = context.WithTimeout(ctx, time.Second*45)
	defer timeoutCtxCancel()

	err = testutil.WaitForBlocks(timeoutCtx, int(blocksAfterUpgrade), tc)
	require.NoError(t, err, "chain did not produce blocks after upgrade")

	nodes, err := tc.ApiGetNodes()
	require.NoError(t, err, "error fetching nodes after upgrade")

	require.Len(t, nodes, numVals, "expected number of nodes")
	for _, n := range nodes {
		require.Equal(t, upgradeName, n.Version, "expected version")
	}
}
