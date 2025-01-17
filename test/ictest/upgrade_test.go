package ictest

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"testing"
	"time"

	"github.com/strangelove-ventures/interchaintest/v8"
	tc "github.com/strangelove-ventures/interchaintest/v8/chain/thorchain"
	"github.com/strangelove-ventures/interchaintest/v8/ibc"
	"github.com/strangelove-ventures/interchaintest/v8/testutil"

	"github.com/stretchr/testify/require"
	"github.com/docker/docker/client"
	"golang.org/x/sync/errgroup"
)

type UpgradeInfo struct {
	name    string
	repo    string
	version string
	uidgid  string
}

func TestUpgrade_3_1_to_3_2(t *testing.T) {
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
		numVals     = 3
		numFns      = 0
		mainnet     = false

		initialRepo    = "thorchain"
		initialVersion = "v3.1.1-mocknet"
		imageUidGid    = "1025:1025"
	)

	// Start non-thorchain chains
	exoChains := StartExoChains(t, ctx, client, network, []*interchaintest.ChainSpec{
		//EthChainSpec("geth"), // only use this chain spec for eth or the one below
		// EthChainSpec("anvil"), // Anvil supported contracts are not support in this repo
		//GaiaChainSpec(),
		BtcChainSpec(),
		BchChainSpec(),
		//LtcChainSpec(),
		//DogeChainSpec(),
		//BscChainSpec(),
	})
	//gaiaEg := SetupGaia(t, ctx, exoChains["GAIA"])
	//ethRouterContractAddress, bscRouterContractAddress, err := SetupContracts(ctx, exoChains["ETH"], exoChains["BSC"])
	//require.NoError(t, err, "error setting up contracts for eth and bsc")
	ethRouterContractAddress, bscRouterContractAddress := "", ""
	// Start thorchain
	chainImage := ibc.NewDockerImage(initialRepo, initialVersion, imageUidGid)
	// Start chain with 1 validator, then validators may be churned in (current bifrost requirement)
	thorchain := StartThorchain(t, ctx, client, network, 1, numFns, mainnet, chainImage, exoChains, ethRouterContractAddress, bscRouterContractAddress)
	err := setNetworkFee(thorchain.Validators, exoChains)
	require.NoError(t, err)
	//require.NoError(t, gaiaEg.Wait()) // Wait for 100 transactions before starting tests

	// Add validators
	if numVals > 1 {
		err := addValidators(t, ctx, thorchain, numVals-1)
		require.NoError(t, err)
	}
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
		name: "3.2.0",
		repo: "thorchain",
		version: "v3.2.0-mocknet",
		uidgid: imageUidGid,
	})

	// --------------------------------------------------------
	// Post-upgrade tests
	// --------------------------------------------------------
	PerformSwaps(t, ctx, thorchain, exoChains)
	PerformSaverEjects(t, ctx, thorchain, exoChains)
	Ragnarok(t, ctx, thorchain, exoChains)
}

func TestUpgrade_3_0_to_3_1(t *testing.T) {
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
		numVals     = 3
		numFns      = 0
		mainnet     = false

		initialRepo    = "thorchain"
		initialVersion = "v3.0.1-mocknet"
		imageUidGid    = "1025:1025"
	)

	// Start chain with 1 validator, then validators may be churned in (current bifrost requirement)
	initialValSetSize := 1

	// Start non-thorchain chains
	exoChains := StartExoChains(t, ctx, client, network, []*interchaintest.ChainSpec{
		//EthChainSpec("geth"), // only use this chain spec for eth or the one below
		// EthChainSpec("anvil"), // Anvil supported contracts are not support in this repo
		//GaiaChainSpec(),
		BtcChainSpec(),
		// BchChainSpec(),
		// LtcChainSpec(),
		// DogeChainSpec(),
		//BscChainSpec(),
	})
	//gaiaEg := SetupGaia(t, ctx, exoChains["GAIA"])
	//ethRouterContractAddress, bscRouterContractAddress, err := SetupContracts(ctx, exoChains["ETH"], exoChains["BSC"])
	//require.NoError(t, err, "error setting up contracts for eth and bsc")
	ethRouterContractAddress, bscRouterContractAddress := "", ""
	// Start thorchain
	chainImage := ibc.NewDockerImage(initialRepo, initialVersion, imageUidGid)
	thorchain := StartThorchain(t, ctx, client, network, initialValSetSize, numFns, mainnet, chainImage, exoChains, ethRouterContractAddress, bscRouterContractAddress)
	err := setNetworkFee(thorchain.Validators, exoChains)
	require.NoError(t, err)
	//require.NoError(t, gaiaEg.Wait()) // Wait for 100 transactions before starting tests

	// Add validators
	if numVals > 1 {
		err := addValidators(t, ctx, thorchain, numVals-1)
		require.NoError(t, err)
	}
	// --------------------------------------------------------
	// Pre-upgrade setup and tests
	// --------------------------------------------------------
	BootstrapPools(t, ctx, thorchain, exoChains)
	//CreateSavers(t, ctx, thorchain, exoChains)
	arbTimer := CreateArbers(t, ctx, thorchain, exoChains)
	//PerformSwaps(t, ctx, thorchain, exoChains)
	//PerformSaverEjects(t, ctx, thorchain, exoChains)

	// --------------------------------------------------------
	// Upgrade
	// --------------------------------------------------------
	PerformUpgrade(t, ctx, client, thorchain, arbTimer, UpgradeInfo{
		name: "3.1.0",
		repo: "thorchain",
		version: "v3.1.0-mocknet",
		uidgid: imageUidGid,
	})

	// --------------------------------------------------------
	// Post-upgrade tests
	// --------------------------------------------------------
	//PerformSwaps(t, ctx, thorchain, exoChains)
	//PerformSaverEjects(t, ctx, thorchain, exoChains)
	Ragnarok(t, ctx, thorchain, exoChains)
}
// TODO:
// restart node after upgrade
// test mainnet
func TestUpgrade_2_137_to_3_0(t *testing.T) {
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
		numVals     = 2
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
		EthChainSpec("geth"), // only use this chain spec for eth or the one below
		// EthChainSpec("anvil"), // Anvil supported contracts are not support in this repo
		GaiaChainSpec(),
		BtcChainSpec(),
		BchChainSpec(),
		LtcChainSpec(),
		DogeChainSpec(),
		BscChainSpec(),
	})
	//gaiaEg := SetupGaia(t, ctx, exoChains["GAIA"])
	ethRouterContractAddress, bscRouterContractAddress, err := SetupContracts(ctx, exoChains["ETH"], exoChains["BSC"])
	require.NoError(t, err, "error setting up contracts for eth and bsc")
 
	// Start thorchain
	chainImage := ibc.NewDockerImage(initialRepo, initialVersion, imageUidGid)
	thorchain := StartThorchain(t, ctx, client, network, initialValSetSize, numFns, mainnet, chainImage, exoChains, ethRouterContractAddress, bscRouterContractAddress)
	err = setNetworkFee(thorchain.Validators, exoChains)
	require.NoError(t, err)
	//require.NoError(t, gaiaEg.Wait()) // Wait for 100 transactions before starting tests

	// Add validators
	if numVals > 1 {
		err := addValidators(t, ctx, thorchain, numVals-1)
		require.NoError(t, err)
	}
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
		name: "3.0.0",
		repo: "thorchain",
		version: "v3.0.0-mocknet",
		uidgid: imageUidGid,
	})

	// --------------------------------------------------------
	// Post-upgrade tests
	// --------------------------------------------------------
	PerformSwaps(t, ctx, thorchain, exoChains)
	PerformSaverEjects(t, ctx, thorchain, exoChains)
	Ragnarok(t, ctx, thorchain, exoChains)

	storeVersion := getStoreVersion(t, ctx, thorchain, true)
	require.Equal(t, int64(-1), storeVersion)
}

// If there are no arbers and thus no arbTimer, pass in nil
func PerformUpgrade(t *testing.T, ctx context.Context, client *client.Client, thorchain *tc.Thorchain, arbTimer *time.Timer, upgradeInfo UpgradeInfo) {
	const (
		haltHeightDelta    = 20 // will propose upgrade this many blocks in the future
		blocksAfterUpgrade = 10
	)
	
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

	if len(thorchain.Validators) > 1 {
		// the upgrade plan should not be scheduled on-chain yet.
		stdout, _, err := thorchain.GetNode().ExecQuery(ctx, "upgrade", "plan")
		if thorchain.GetNode().IsAboveSDK47(ctx) {
			require.Equal(t, "{}\n", string(stdout))
		} else {
			require.Error(t, err, "expected error from upgrade plan query")
		}
	}

	// approve software upgrade from other validators, if they exist
	var eg errgroup.Group
	for i := 1; i < len(thorchain.Validators); i++ {
		i := i
		eg.Go(func() error {
			return ApproveUpgrade(ctx, thorchain.Validators[i], upgradeInfo.name)
		})
	}
	err = eg.Wait()
	require.NoError(t, err)

	// ensure on-chain scheduled upgrade plan matches what was proposed and approved
	stdout, _, err := thorchain.GetNode().ExecQuery(ctx, "upgrade", "plan")
	require.NoError(t, err, "expected success from upgrade plan query")

	if thorchain.GetNode().IsAboveSDK47(ctx) {
		plan := make(map[string]map[string]any)
		require.NoError(t, json.Unmarshal(stdout, &plan))
		require.Equal(t, upgradeInfo.name, plan["plan"]["name"], "upgrade name mismatch")
		require.Equal(t, fmt.Sprint(haltHeight), plan["plan"]["height"], "upgrade height mismatch")
	} else {
		plan := make(map[string]any)
		require.NoError(t, json.Unmarshal(stdout, &plan))
		require.Equal(t, upgradeInfo.name, plan["name"], "upgrade name mismatch")
		require.Equal(t, fmt.Sprint(haltHeight), plan["height"], "upgrade height mismatch")
	}

	// now wait for chain to halt at upgrade height

	height, err = thorchain.Height(ctx)
	require.NoError(t, err, "error fetching height before upgrade")

	timeoutCtx, timeoutCtxCancel := context.WithTimeout(ctx, time.Second*haltHeightDelta*2)
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

	// Store version prior to 3.0.0 is 137
	if upgradeInfo.name == "3.0.0" {
		storeVersion := getStoreVersion(t, ctx, thorchain, false)
		require.Equal(t, int64(137), storeVersion)
	}

	// upgrade version on all nodes
	newImage := ibc.NewDockerImage(upgradeInfo.repo, upgradeInfo.version, upgradeInfo.uidgid)

	// not necessary for local images and will error. needed for remote images, so leaving like this to support both.
	_ = newImage.PullImage(ctx, client)

	for _, n := range thorchain.Nodes() {
		n.Image = newImage
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
	
	// Start the arbers back up
	if arbTimer != nil {
		arbTimer.Reset(time.Second) // Start the arbs back up
	}

	verifyVersionAPI(t, ctx, thorchain, upgradeInfo.name)
}

// app_state, "thorchain", "store_version"
func getStoreVersion(t *testing.T, ctx context.Context, thorchain *tc.Thorchain, stopAllNodes bool) int64 {
	type ThorchainState struct {
		StoreVersion string `json:"store_version,omitempty"`
	}
	type AppState struct {
		Thorchain ThorchainState `json:"thorchain"`
	}
	type GenesisState struct {
		AppState AppState `json:"app_state"`
	}
	
	if stopAllNodes {
		err := thorchain.StopAllSidecars(ctx)
		require.NoError(t, err, "error stopping sidecar(s)")
	
		// bring down nodes to prepare for upgrade
		err = thorchain.StopAllNodes(ctx)
		require.NoError(t, err, "error stopping node(s)")
	}
	state, err := thorchain.ExportState(ctx, -1)
	require.NoError(t, err)

	var gs GenesisState
	err = json.Unmarshal([]byte(state), &gs)
	require.NoError(t, err)

	if gs.AppState.Thorchain.StoreVersion == "" {
		return -1
	}

	storeVersion, err := strconv.ParseInt(gs.AppState.Thorchain.StoreVersion, 10, 64)
	require.NoError(t, err)

	return storeVersion
}

func verifyVersionAPI(t *testing.T, ctx context.Context, thorchain *tc.Thorchain, expectedVersion string) {
	version, err := thorchain.APIQuery(ctx, "thorchain/version")
	require.NoError(t, err)

	versionMap := version.(map[string]any)
	currentVersion := versionMap["current"].(string)
	nextVersion := versionMap["next"].(string)
	querierVersion := versionMap["querier"].(string)
	require.Equal(t, currentVersion, expectedVersion)
	require.Equal(t, nextVersion, expectedVersion)
	require.Equal(t, querierVersion, expectedVersion)
}