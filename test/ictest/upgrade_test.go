package ictest

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/strangelove-ventures/interchaintest/v8"
	tc "github.com/strangelove-ventures/interchaintest/v8/chain/thorchain"
	"github.com/strangelove-ventures/interchaintest/v8/ibc"

	"github.com/strangelove-ventures/interchaintest/v8/testutil"
	"github.com/stretchr/testify/require"
	"github.com/docker/docker/client"
)

const (
	haltHeightDelta    = 10 // will propose upgrade this many blocks in the future
	blocksAfterUpgrade = 10
	imageUidGid        = "1025:1025"
)

type UpgradeInfo struct {
	name    string
	repo    string
	version string
	uidgid  string
}

// TODO:
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
		mainnet     = false

		initialRepo    = "ghcr.io/strangelove-ventures/heighliner/thorchain"
		initialVersion = "mocknet-v2.136.0"
	)

	// Start non-thorchain chains
	exoChains := StartExoChains(t, ctx, client, network, []*interchaintest.ChainSpec{
		EthChainSpec("geth"), // only use this chain spec for eth or the one below
		// EthChainSpec("anvil"), // Anvil supported contracts are not support in this repo
		GaiaChainSpec(),
		BtcChainSpec(),
		BchChainSpec(),
		LtcChainSpec(),
		DogeChainSpec(),
		BscChainSpec(),
	})
	gaiaEg := SetupGaia(t, ctx, exoChains["GAIA"])
	ethRouterContractAddress, bscRouterContractAddress, err := SetupContracts(ctx, exoChains["ETH"], exoChains["BSC"])
	require.NoError(t, err, "error setting up contracts for eth and bsc")
 
	// Start thorchain
	chainImage := ibc.NewDockerImage(initialRepo, initialVersion, imageUidGid)
	thorchain := StartThorchain(t, ctx, client, network, numVals, numFns, mainnet, chainImage, exoChains, ethRouterContractAddress, bscRouterContractAddress)
	require.NoError(t, gaiaEg.Wait()) // Wait for 100 transactions before starting tests

	// --------------------------------------------------------
	// Pre-upgrade setup and tests
	// --------------------------------------------------------
	BootstrapPools(t, ctx, thorchain, exoChains)
	CreateSavers(t, ctx, thorchain, exoChains)
	arbTimer := CreateArbers(t, ctx, thorchain, exoChains)
	PerformSwaps(t, ctx, thorchain, exoChains)
	PerformSaverEjects(t, ctx, thorchain, exoChains)

	// --------------------------------------------------------
	// Upgrade
	// --------------------------------------------------------
	PerformUpgrade(t, ctx, client, thorchain, arbTimer, UpgradeInfo{
		name: "2.137.0",
		repo: "thorchain",
		version: "local",
		uidgid: imageUidGid,
	})

	// --------------------------------------------------------
	// Post-upgrade tests
	// --------------------------------------------------------
	PerformSwaps(t, ctx, thorchain, exoChains)
	PerformSaverEjects(t, ctx, thorchain, exoChains)
	Ragnarok(t, ctx, thorchain, exoChains)

	//err = testutil.WaitForBlocks(ctx, 300, thorchain)
	//require.NoError(t, err, "thorchain failed to make blocks")
	//nodes, err := tc.ApiGetNodes()
	//require.NoError(t, err, "error fetching nodes after upgrade")

	// require.Len(t, nodes, numVals, "expected number of nodes")
	// for _, n := range nodes {
	// 	require.Equal(t, upgradeName, n.Version, "expected version")
	// }
}

// If there are no arbers and thus no arbTimer, pass in nil
func PerformUpgrade(t *testing.T, ctx context.Context, client *client.Client, thorchain *tc.Thorchain, arbTimer *time.Timer, upgradeInfo UpgradeInfo) {
	fmt.Printf("\n  ----------------\n  Starting upgrade\n  ----------------\n")
	if arbTimer != nil {
		// Stop the arbs for the upgrade, make sure we actually stop the timer or it can get reset
		for arbsStopped := arbTimer.Stop(); !arbsStopped; arbsStopped = arbTimer.Stop() {
			time.Sleep(time.Millisecond * 200)
		}
	}

	height, err := thorchain.Height(ctx)
	require.NoError(t, err, "error fetching height before submit upgrade proposal")

	haltHeight := height + haltHeightDelta

	// submit software upgrade proposal from first validator
	err = ProposeUpgrade(ctx, thorchain.Validators[0], upgradeInfo.name, haltHeight)
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
	require.Equal(t, upgradeInfo.name, plan["name"], "upgrade name mismatch")
	require.Equal(t, fmt.Sprint(haltHeight), plan["height"], "upgrade height mismatch")

	// now wait for chain to halt at upgrade height

	height, err = thorchain.Height(ctx)
	require.NoError(t, err, "error fetching height before upgrade")

	timeoutCtx, timeoutCtxCancel := context.WithTimeout(ctx, time.Second*30)
	defer timeoutCtxCancel()

	// this should timeout due to chain halt at upgrade height.
	_ = testutil.WaitForBlocks(timeoutCtx, int(haltHeight-height)+1, thorchain)

	height, err = thorchain.Height(ctx)
	require.NoError(t, err, "error fetching height after chain should have halted")

	// make sure that chain is halted
	require.Equal(t, haltHeight, height, "height is not equal to halt height")

	err = thorchain.StopAllSidecars(ctx)
	require.NoError(t, err, "error stopping sidecar(s)")

	// bring down nodes to prepare for upgrade
	err = thorchain.StopAllNodes(ctx)
	require.NoError(t, err, "error stopping node(s)")

	// upgrade version on all nodes
	newImage := ibc.NewDockerImage(upgradeInfo.repo, upgradeInfo.version, upgradeInfo.uidgid)

	// not necessary for local images and will error. needed for remote images, so leaving like this to support both.
	_ = newImage.PullImage(ctx, client)

	for _, n := range thorchain.Nodes() {
		n.Image = newImage
		// TODO: add deprecated auth endpoint and don't change bifrost
		for _, s := range n.Sidecars {
			s.Image = newImage
		}
	}

	// start all nodes back up.
	// validators reach consensus on first block after upgrade height
	// and chain block production resumes.
	err = thorchain.StartAllNodes(ctx)
	require.NoError(t, err, "error starting upgraded node(s)")

	err = thorchain.StartAllValSidecars(ctx)
	require.NoError(t, err, "error starting upgraded sidecar(s)")

	timeoutCtx, timeoutCtxCancel = context.WithTimeout(ctx, time.Second*45)
	defer timeoutCtxCancel()

	fmt.Println("Waiting for chain to produce blocks")
	err = testutil.WaitForBlocks(timeoutCtx, int(blocksAfterUpgrade), thorchain)
	require.NoError(t, err, "chain did not produce blocks after upgrade")

	fmt.Println("Chain is producing blocks")
	// We are now producing blocks
	
	// Start the arbers back up
	if arbTimer != nil {
		arbTimer.Reset(time.Second) // Start the arbs back up
	}

}