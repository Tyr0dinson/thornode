package ictest

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
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


// TODO:
// add swaps, savers eject, ragnarok after upgrade
// restart node after upgrade
// test multiple validators
// test mainnet
func TestUpgrade136to137(t *testing.T) {
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
		upgradeVersion = "local"

		upgradeName = "2.137.0"
	)

	// Start non-thorchain chains
	exoChains := StartExoChains(t, ctx, client, network)
	gaiaEg := SetupGaia(t, ctx, exoChains["GAIA"])
	ethRouterContractAddress, bscRouterContractAddress, err := SetupContracts(ctx, exoChains["ETH"], exoChains["BSC"])
	require.NoError(t, err, "error setting up contracts for eth and bsc")
 
	// Start thorchain
	chainImage := ibc.NewDockerImage(initialRepo, initialVersion, imageUidGid)
	thorchain := StartThorchain(t, ctx, client, network, numVals, numFns, mainnet, chainImage, exoChains, ethRouterContractAddress, bscRouterContractAddress)
	require.NoError(t, gaiaEg.Wait()) // Wait for 100 transactions before starting tests

	// --------------------------------------------------------
	// Bootstrap pool
	// --------------------------------------------------------
	eg, egCtx := errgroup.WithContext(ctx)
	for _, exoChain := range exoChains {
		exoChain := exoChain
		eg.Go(func() error {
			_, lper, err := features.DualLp(t, egCtx, thorchain, exoChain.chain)
			if err != nil {
				return err
			}
			exoChain.lpers = append(exoChain.lpers, lper)
			return nil
		})
	}
	require.NoError(t, eg.Wait())

	// --------------------------------------------------------
	// Savers
	// --------------------------------------------------------
	eg, egCtx = errgroup.WithContext(ctx)
	for _, exoChain := range exoChains {
		exoChain := exoChain
		eg.Go(func() error {
			saver, err := features.Saver(t, egCtx, thorchain, exoChain.chain)
			if err != nil {
				return err
			}
			exoChain.savers = append(exoChain.savers, saver)
			return nil
		})
	}
	require.NoError(t, eg.Wait())

	// --------------------------------------------------------
	// Arb
	// --------------------------------------------------------
	_, err = features.Arb(t, ctx, thorchain, exoChains.GetChains()...)
	require.NoError(t, err)

	// --------------------------------------------------------
	// Swap - only swaps non-rune assets for now
	// --------------------------------------------------------
	eg, egCtx = errgroup.WithContext(ctx)
	exoChainList := exoChains.GetChains()
	for i := range exoChainList {
		i := i
		fmt.Println("Chain:", i, "Name:", exoChainList[i].Config().Name)
		randomChain := rand.Intn(len(exoChainList))
		if i == randomChain && i == 0 {
			randomChain++
		} else if i == randomChain {
			randomChain--
		}
		eg.Go(func() error {
			return features.DualSwap(t, ctx, thorchain, exoChainList[i], exoChainList[randomChain])
		})
	}
	require.NoError(t, eg.Wait())

	// --------------------------------------------------------
	// Start upgrade
	// --------------------------------------------------------
	height, err := thorchain.Height(ctx)
	require.NoError(t, err, "error fetching height before submit upgrade proposal")

	haltHeight := height + haltHeightDelta

	// submit software upgrade proposal from first validator
	_, err = thorchain.Validators[0].ExecTx(ctx, "thorchain", "thorchain", "propose-upgrade", upgradeName, fmt.Sprint(haltHeight))
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
	stdout, _, err := thorchain.GetNode().ExecQuery(ctx, "upgrade", "plan")
	require.NoError(t, err, "expected success from upgrade plan query")

	plan := make(map[string]any)
	require.NoError(t, json.Unmarshal(stdout, &plan))
	require.Equal(t, upgradeName, plan["name"], "upgrade name mismatch")
	require.Equal(t, fmt.Sprint(haltHeight), plan["height"], "upgrade height mismatch")

	// now wait for chain to halt at upgrade height

	height, err = thorchain.Height(ctx)
	require.NoError(t, err, "error fetching height before upgrade")

	timeoutCtx, timeoutCtxCancel := context.WithTimeout(ctx, time.Second*45)
	defer timeoutCtxCancel()

	// this should timeout due to chain halt at upgrade height.
	_ = testutil.WaitForBlocks(timeoutCtx, int(haltHeight-height)+1, thorchain)

	height, err = thorchain.Height(ctx)
	require.NoError(t, err, "error fetching height after chain should have halted")

	// make sure that chain is halted
	require.Equal(t, haltHeight, height, "height is not equal to halt height")

	// bring down nodes to prepare for upgrade
	err = thorchain.StopAllNodes(ctx)
	require.NoError(t, err, "error stopping node(s)")

	// upgrade version on all nodes
	newImage := ibc.NewDockerImage(upgradeRepo, upgradeVersion, imageUidGid)

	// not necessary for local images and will error. needed for remote images, so leaving like this to support both.
	_ = newImage.PullImage(ctx, client)

	for _, n := range thorchain.Nodes() {
		n.Image = newImage
	}

	// start all nodes back up.
	// validators reach consensus on first block after upgrade height
	// and chain block production resumes.
	err = thorchain.StartAllNodes(ctx)
	require.NoError(t, err, "error starting upgraded node(s)")

	timeoutCtx, timeoutCtxCancel = context.WithTimeout(ctx, time.Second*45)
	defer timeoutCtxCancel()

	err = testutil.WaitForBlocks(timeoutCtx, int(blocksAfterUpgrade), thorchain)
	require.NoError(t, err, "chain did not produce blocks after upgrade")

	//err = testutil.WaitForBlocks(ctx, 300, thorchain)
	//require.NoError(t, err, "thorchain failed to make blocks")
	//nodes, err := tc.ApiGetNodes()
	//require.NoError(t, err, "error fetching nodes after upgrade")

	// require.Len(t, nodes, numVals, "expected number of nodes")
	// for _, n := range nodes {
	// 	require.Equal(t, upgradeName, n.Version, "expected version")
	// }
}