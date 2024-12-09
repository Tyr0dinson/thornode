package ictest

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/strangelove-ventures/interchaintest/v8"
	tc "github.com/strangelove-ventures/interchaintest/v8/chain/thorchain"
	"github.com/strangelove-ventures/interchaintest/v8/ibc"
	"github.com/strangelove-ventures/interchaintest/v8/testutil"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	"github.com/docker/docker/client"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zaptest"
	"golang.org/x/sync/errgroup"
)

//go:embed genesis.1733524678.json
var genesisBz []byte

func TestProfileMainnetUpgrade(t *testing.T) {
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
	ProfileUpgrade(t, ctx, client, thorchain, UpgradeInfo{
		name: "3.0.0",
		repo: "thorchain",
		version: "v3.0.0-mainnet",
		uidgid: imageUidGid,
	})

	storeVersion := getStoreVersion(t, ctx, thorchain, true)
	require.Equal(t, int64(-1), storeVersion)
}

func StartMainnetThorchain(
	t *testing.T,
	ctx context.Context,
	client *client.Client,
	network string,
	numValidators int,
	numFullNodes int,
	mainnet bool,
	chainImage ibc.DockerImage,
) *tc.Thorchain {
	thorchainChainSpec := ThorchainDefaultChainSpec(t.Name(), chainImage, mainnet, numValidators,
		numFullNodes, "", "", nil, nil)

	// Remove bifrost sidecar so we don't need to start it up and shut it down
	thorchainChainSpec.SidecarConfigs = nil

	// Remove all node accounts except for one
	// Select the one node account
	nodeAccounts := gjson.GetBytes(genesisBz, "app_state.thorchain.node_accounts")
	selectedNodeAccount := gjson.Result{}
	for _, nodeAccount := range nodeAccounts.Array() {
		status := nodeAccount.Get("status")
		fmt.Println("Status string", status.String())
		if status.String() == "Active" {
			selectedNodeAccount = nodeAccount
			break
		}
	}

	// Set the one node account
	var nodeAccountsFinal []interface{}
	nodeAccountsFinal = append(nodeAccountsFinal, selectedNodeAccount.Value())
	genesisFile, err := sjson.Set(string(genesisBz), "app_state.thorchain.node_accounts", nodeAccountsFinal)
	require.NoError(t, err)


	// Update all vaults to only have the single node account's pub key in membership
	vaults := gjson.GetBytes(genesisBz, "app_state.thorchain.vaults")
	pubkey := selectedNodeAccount.Get("pub_key_set.secp256k1").String()
	for i := range vaults.Array() {
		genesisFile, err = sjson.Set(genesisFile, fmt.Sprintf("app_state.thorchain.vaults.%d.membership", i), []string{pubkey})
		require.NoError(t, err)
	}

	// Update auth accounts with the test key's pub key, so we can make transactions
	accounts := gjson.GetBytes(genesisBz, "app_state.auth.accounts")
	for i, account := range accounts.Array() {
		if account.Get("address").String() == selectedNodeAccount.Get("node_address").String() {
			genesisFile, err = sjson.Set(genesisFile, fmt.Sprintf("app_state.auth.accounts.%d.pub_key.key", i), "AmF4AUTWZEUSBtgqiR5n2Lgic/Yrr1mWupMo5TAubNRO")
			require.NoError(t, err)
		}
	}

	// Set the chain_id to the expected chain id
	genesisFile, err = sjson.Set(genesisFile, "chain_id", "thorchain")
	require.NoError(t, err)

	thorchainChainSpec.Genesis = &ibc.GenesisConfig{
		Contents: []byte(genesisFile),
		AllValidators: false,
		MaxVals: numValidators,
	}

	cf := interchaintest.NewBuiltinChainFactory(zaptest.NewLogger(t), []*interchaintest.ChainSpec{
		thorchainChainSpec,
	})

	chains, err := cf.Chains(t.Name())
	require.NoError(t, err)

	thorchain := chains[0].(*tc.Thorchain)

	ic := interchaintest.NewInterchain().
		AddChain(thorchain)

	require.NoError(t, ic.Build(ctx, nil, interchaintest.InterchainBuildOptions{
		TestName:         t.Name(),
		Client:           client,
		NetworkID:        network,
		SkipPathCreation: true,
	}))
	t.Cleanup(func() {
		_ = ic.Close()
	})

	return thorchain
}

// If there are no arbers and thus no arbTimer, pass in nil
func ProfileUpgrade(t *testing.T, ctx context.Context, client *client.Client, thorchain *tc.Thorchain, upgradeInfo UpgradeInfo) {
	const (
		haltHeightDelta    = 10 // will propose upgrade this many blocks in the future
		blocksAfterUpgrade = 2
	)
	
	fmt.Printf("\n  ----------------\n  Starting upgrade\n  ----------------\n")
	height, err := thorchain.Height(ctx)
	require.NoError(t, err, "error fetching height before submit upgrade proposal")

	haltHeight := height + haltHeightDelta

	// submit software upgrade proposal from first validator
	err = ProposeUpgrade(ctx, thorchain.Validators[0], upgradeInfo.name, haltHeight)
	require.NoError(t, err, "error submitting software upgrade proposal tx")

	if len(thorchain.Validators) > 1 {
		// the upgrade plan should not be scheduled on-chain yet.
		_, _, err = thorchain.GetNode().ExecQuery(ctx, "upgrade", "plan")
		require.Error(t, err, "expected error from upgrade plan query")
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

	plan := make(map[string]any)
	require.NoError(t, json.Unmarshal(stdout, &plan))
	require.Equal(t, upgradeInfo.name, plan["name"], "upgrade name mismatch")
	require.Equal(t, fmt.Sprint(haltHeight), plan["height"], "upgrade height mismatch")

	// now wait for chain to halt at upgrade height

	height, err = thorchain.Height(ctx)
	require.NoError(t, err, "error fetching height before upgrade")

	timeoutCtx, timeoutCtxCancel := context.WithTimeout(ctx, time.Second*haltHeightDelta*2+time.Second*5)
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

	storeVersion := getStoreVersion(t, ctx, thorchain, false)
	require.Equal(t, int64(137), storeVersion)

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

	upgradeStartTime := time.Now()
	// start all nodes back up.
	// validators reach consensus on first block after upgrade height
	// and chain block production resumes.
	err = thorchain.StartAllNodes(ctx)
	require.NoError(t, err, "error starting upgraded node(s)")

	// timeout after 15 minutes
	timeoutCtx, timeoutCtxCancel = context.WithTimeout(ctx, time.Second*900)
	defer timeoutCtxCancel()

	fmt.Println("Waiting for chain to produce blocks")
	err = testutil.WaitForBlocks(timeoutCtx, int(blocksAfterUpgrade), thorchain)
	require.NoError(t, err, "chain did not produce blocks after upgrade")

	upgradeFinishedTime := time.Now()
	fmt.Println("Chain is producing blocks")
	fmt.Println("It took (in seconds):", upgradeFinishedTime.Sub(upgradeStartTime).Seconds())
	
	verifyVersionAPI(t, ctx, thorchain, upgradeInfo.name)
}