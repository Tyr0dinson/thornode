package ictest

import (
	"context"
	"fmt"
	"testing"

	"github.com/strangelove-ventures/interchaintest/v8"
	"github.com/strangelove-ventures/interchaintest/v8/chain/xrp"
	"github.com/strangelove-ventures/interchaintest/v8/ibc"
	"github.com/stretchr/testify/require"
)

// go test -timeout 20m -v -run TestXrp examples/thorchain/*.go -count 1
// go test -timeout 20m -v -run TestXrp . -count 1
func TestXrp(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping in short mode")
	}
	t.Parallel()

	ctx := context.Background()
	client, network := interchaintest.DockerSetup(t)

	const (
		numVals     = 1 // 2
		numFns      = 0 // 1
		mainnet     = false
	)

	// Start non-thorchain chains
	exoChains := StartExoChains(t, ctx, client, network, []*interchaintest.ChainSpec{
		//EthChainSpec("geth"), // only use this chain spec for eth or the one below
		// EthChainSpec("anvil"), // Anvil supported contracts are not support in this repo
		//GaiaChainSpec(),
		BtcChainSpec(),
		//BchChainSpec(),
		//LtcChainSpec(),
		//DogeChainSpec(),
		//BscChainSpec(),
		XrpChainSpec(),
	})
	//gaiaEg := SetupGaia(t, ctx, exoChains["GAIA"])
	//ethRouterContractAddress, bscRouterContractAddress, err := SetupContracts(ctx, exoChains["ETH"], exoChains["BSC"])
	ethRouterContractAddress, bscRouterContractAddress := "", ""
	//require.NoError(t, err, "error setting up contracts for eth and bsc")
 
	// Start thorchain
	chainImage := ibc.NewDockerImage("thorchain", "local", "1025:1025")
	thorchain := StartThorchain(t, ctx, client, network, numVals, numFns, mainnet, chainImage, exoChains, ethRouterContractAddress, bscRouterContractAddress)
	//require.NoError(t, gaiaEg.Wait()) // Wait for 100 transactions before starting tests

	BootstrapPools(t, ctx, thorchain, exoChains)
	CreateSavers(t, ctx, thorchain, exoChains)
	_ = CreateArbers(t, ctx, thorchain, exoChains)
	PerformSwaps(t, ctx, thorchain, exoChains)
	PerformSaverEjects(t, ctx, thorchain, exoChains)
	Ragnarok(t, ctx, thorchain, exoChains)

	xrpChain, ok := exoChains["XRP"].chain.(*xrp.XrpChain)
	require.True(t, ok)
	
	xrpVaultAddr, _, err := thorchain.APIGetInboundAddress(ctx, "XRP")
	require.NoError(t, err)
	xrpVaultBalance, err := xrpChain.GetBalance(ctx, xrpVaultAddr, "")
	require.NoError(t, err)

	fmt.Println("Xrp vault balance:", xrpVaultBalance)

	//err = testutil.WaitForBlocks(ctx, 300, thorchain)
	//require.NoError(t, err, "thorchain failed to make blocks")
}
