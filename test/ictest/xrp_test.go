package ictest

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/strangelove-ventures/interchaintest/v8"
	"github.com/strangelove-ventures/interchaintest/v8/chain/cosmos"
	tc "github.com/strangelove-ventures/interchaintest/v8/chain/thorchain"
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
		numVals = 2 // 2
		numFns  = 0 // 1
		mainnet = false
	)

	// Start non-thorchain chains
	exoChains := StartExoChains(t, ctx, client, network, []*interchaintest.ChainSpec{
		//EthChainSpec("geth"), // only use this chain spec for eth or the one below
		// EthChainSpec("anvil"), // Anvil supported contracts are not support in this repo
		GaiaChainSpec(),
		//BtcChainSpec(),
		//BchChainSpec(),
		//LtcChainSpec(),
		//DogeChainSpec(),
		//BscChainSpec(),
		XrpChainSpec(),
	})
	//ethRouterContractAddress, bscRouterContractAddress, err := SetupContracts(ctx, exoChains["ETH"], exoChains["BSC"])
	ethRouterContractAddress, bscRouterContractAddress := "", ""
	//require.NoError(t, err, "error setting up contracts for eth and bsc")

	// Start thorchain
	chainImage := ibc.NewDockerImage("thorchain", "local", "1025:1025")
	thorchain := StartThorchain(t, ctx, client, network, 1, numFns, mainnet, chainImage, exoChains, ethRouterContractAddress, bscRouterContractAddress)
	xrpChain := exoChains["XRP"].chain.(*xrp.XrpChain)
	gaiaChain := exoChains["GAIA"].chain.(*cosmos.CosmosChain)
	err := setNetworkFee(thorchain.Validators, exoChains)
	require.NoError(t, err)

	printVaultAddressesAndBalance(t, ctx, thorchain, exoChains)
	if numVals > 1 {
		err := addValidators(t, ctx, thorchain, numVals-1)
		require.NoError(t, err)
	}
	printVaultAddressesAndBalance(t, ctx, thorchain, exoChains)

	BootstrapPools(t, ctx, thorchain, exoChains)
	printVaultAddressesAndBalance(t, ctx, thorchain, exoChains)
	CreateSavers(t, ctx, thorchain, exoChains)
	printVaultAddressesAndBalance(t, ctx, thorchain, exoChains)
	_ = CreateArbers(t, ctx, thorchain, exoChains)
	printVaultAddressesAndBalance(t, ctx, thorchain, exoChains)
	PerformSwaps(t, ctx, thorchain, exoChains)
	printVaultAddressesAndBalance(t, ctx, thorchain, exoChains)
	PerformSaverEjects(t, ctx, thorchain, exoChains)
	printVaultAddressesAndBalance(t, ctx, thorchain, exoChains)

	oldXrpVaultAddr, _, err := thorchain.APIGetInboundAddress(ctx, "XRP")
	require.NoError(t, err)
	oldGaiaVaultAddr, _, err := thorchain.APIGetInboundAddress(ctx, "GAIA")
	require.NoError(t, err)

	// Add 1 more validator so we churn addresses again
	err = addValidators(t, ctx, thorchain, 1)
	require.NoError(t, err)
	oldXrpVaultBalance1, err1 := xrpChain.GetBalance(ctx, oldXrpVaultAddr, "")
	oldGaiaVaultBalance1, err11 := gaiaChain.GetBalance(ctx, oldGaiaVaultAddr, gaiaChain.Config().Denom)
	time.Sleep(time.Minute)
	oldXrpVaultBalance2, err2 := xrpChain.GetBalance(ctx, oldXrpVaultAddr, "")
	oldGaiaVaultBalance2, err21 := gaiaChain.GetBalance(ctx, oldGaiaVaultAddr, gaiaChain.Config().Denom)
	time.Sleep(time.Minute)
	oldXrpVaultBalance3, err3 := xrpChain.GetBalance(ctx, oldXrpVaultAddr, "")
	oldGaiaVaultBalance3, err31 := gaiaChain.GetBalance(ctx, oldGaiaVaultAddr, gaiaChain.Config().Denom)
	if err1 != nil {
		fmt.Println("Chain:", "XRP", ", old vault addr:", oldXrpVaultAddr, ", old vault balance err:", err1)
	} else {
		fmt.Println("Chain:", "XRP", ", old vault addr:", oldXrpVaultAddr, ", old vault balance:", oldXrpVaultBalance1)
	}	
	if err2 != nil {
		fmt.Println("Chain:", "XRP", ", old vault addr:", oldXrpVaultAddr, ", old vault balance err:", err2)
	} else {
		fmt.Println("Chain:", "XRP", ", old vault addr:", oldXrpVaultAddr, ", old vault balance:", oldXrpVaultBalance2)
	}	
	if err3 != nil {
		fmt.Println("Chain:", "XRP", ", old vault addr:", oldXrpVaultAddr, ", old vault balance err:", err3)
	} else {
		fmt.Println("Chain:", "XRP", ", old vault addr:", oldXrpVaultAddr, ", old vault balance:", oldXrpVaultBalance3)
	}	
	if err11 != nil {
		fmt.Println("Chain:", "GAIA", ", old vault addr:", oldGaiaVaultAddr, ", old vault balance err:", err11)
	} else {
		fmt.Println("Chain:", "GAIA", ", old vault addr:", oldGaiaVaultAddr, ", old vault balance:", oldGaiaVaultBalance1)
	}	
	if err21 != nil {
		fmt.Println("Chain:", "GAIA", ", old vault addr:", oldGaiaVaultAddr, ", old vault balance err:", err21)
	} else {
		fmt.Println("Chain:", "GAIA", ", old vault addr:", oldGaiaVaultAddr, ", old vault balance:", oldGaiaVaultBalance2)
	}	
	if err31 != nil {
		fmt.Println("Chain:", "GAIA", ", old vault addr:", oldGaiaVaultAddr, ", old vault balance err:", err31)
	} else {
		fmt.Println("Chain:", "GAIA", ", old vault addr:", oldGaiaVaultAddr, ", old vault balance:", oldGaiaVaultBalance3)
	}	
	printVaultAddressesAndBalance(t, ctx, thorchain, exoChains)
	PerformSwaps(t, ctx, thorchain, exoChains)
	oldXrpVaultBalance, err := xrpChain.GetBalance(ctx, oldXrpVaultAddr, "")
	if err != nil {
		fmt.Println("Chain:", "XRP", ", old vault addr:", oldXrpVaultAddr, ", old vault balance err:", err)
	} else {
		fmt.Println("Chain:", "XRP", ", old vault addr:", oldXrpVaultAddr, ", old vault balance:", oldXrpVaultBalance)
	}	
	oldGaiaVaultBalance, err := gaiaChain.GetBalance(ctx, oldGaiaVaultAddr, gaiaChain.Config().Denom)
	if err != nil {
		fmt.Println("Chain:", "GAIA", ", old vault addr:", oldGaiaVaultAddr, ", old vault balance err:", err)
	} else {
		fmt.Println("Chain:", "GAIA", ", old vault addr:", oldGaiaVaultAddr, ", old vault balance:", oldGaiaVaultBalance)
	}	
	printVaultAddressesAndBalance(t, ctx, thorchain, exoChains)
	Ragnarok(t, ctx, thorchain, exoChains)
	printVaultAddressesAndBalance(t, ctx, thorchain, exoChains)
}

func printVaultAddressesAndBalance(t *testing.T, ctx context.Context, thorchain *tc.Thorchain, exoChains ExoChains) {
	fmt.Println("**************************************************************")
	fmt.Println("**************************************************************")
	for _, exoChain := range exoChains {
		tCtx, _ := context.WithTimeout(ctx, time.Minute)
		cfg := exoChain.chain.Config()
		fmt.Println("Getting inbound addr:", cfg.Name)
		vaultAddr, _, err := thorchain.APIGetInboundAddress(tCtx, cfg.Name)
		require.NoError(t, err)
		fmt.Println("Getting balance of:", vaultAddr)
		vaultBalance, err := exoChain.chain.GetBalance(tCtx, vaultAddr, cfg.Denom)
		if err != nil {
			fmt.Println("Chain:", cfg.Name, ", vault addr:", vaultAddr, ", vault balance err:", err)
		} else {
			fmt.Println("Chain:", cfg.Name, ", vault addr:", vaultAddr, ", vault balance:", vaultBalance)
		}
	}
	fmt.Println("**************************************************************")
	fmt.Println("**************************************************************")
}
