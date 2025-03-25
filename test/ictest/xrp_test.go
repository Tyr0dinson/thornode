package ictest

import (
	"context"
	"fmt"
	"testing"
	"time"

	sdkmath "cosmossdk.io/math"
	"gitlab.com/thorchain/thornode/v3/test/ictest/features"

	"github.com/strangelove-ventures/interchaintest/v8"
	"github.com/strangelove-ventures/interchaintest/v8/chain/cosmos"
	tc "github.com/strangelove-ventures/interchaintest/v8/chain/thorchain"
	"github.com/strangelove-ventures/interchaintest/v8/chain/xrp"
	"github.com/strangelove-ventures/interchaintest/v8/ibc"
	"github.com/stretchr/testify/require"

	xrpclient "github.com/strangelove-ventures/interchaintest/v8/chain/xrp/client"
)

// go test -timeout 20m -v -run TestXrp examples/thorchain/*.go -count 1
// go test -timeout 20m -v -run TestXrp . -count 1
func TestXrpMocknet(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping in short mode")
	}
	t.Parallel()

	ctx := context.Background()
	client, network := interchaintest.DockerSetup(t)

	const (
		numVals = 1 // 2
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
	_ = CreateArbers(t, ctx, thorchain, exoChains)
	printVaultAddressesAndBalance(t, ctx, thorchain, exoChains)
	PerformSwaps(t, ctx, thorchain, exoChains)
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

// go test -timeout 20m -v -run TestXrpLargeBlock examples/thorchain/*.go -count 1
// go test -timeout 20m -v -run TestXrpLargeBlock . -count 1
func TestXrpLargeBlock(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping in short mode")
	}
	t.Parallel()

	ctx := context.Background()
	client, network := interchaintest.DockerSetup(t)

	const (
		numVals = 1 // 2
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

	BootstrapPools(t, ctx, thorchain, exoChains)
	printVaultAddressesAndBalance(t, ctx, thorchain, exoChains)
	// Create 5 users for swaps
	user1, err := features.GetAndFundTestUsers(t, ctx, "largeBlockUser1", false, xrpChain, gaiaChain)
	require.NoError(t, err)
	xrpUser1, gaiaUser1 := user1[0], user1[1]
	user2, err := features.GetAndFundTestUsers(t, ctx, "largeBlockUser2", false, xrpChain, gaiaChain)
	require.NoError(t, err)
	xrpUser2, gaiaUser2 := user2[0], user2[1]
	user3, err := features.GetAndFundTestUsers(t, ctx, "largeBlockUser3", false, xrpChain, gaiaChain)
	require.NoError(t, err)
	xrpUser3, gaiaUser3 := user3[0], user3[1]
	user4, err := features.GetAndFundTestUsers(t, ctx, "largeBlockUser4", false, xrpChain, gaiaChain)
	require.NoError(t, err)
	xrpUser4, gaiaUser4 := user4[0], user4[1]
	user5, err := features.GetAndFundTestUsers(t, ctx, "largeBlockUser5", false, xrpChain, gaiaChain)
	require.NoError(t, err)
	xrpUser5, gaiaUser5 := user5[0], user5[1]

	// Stop XRP network
	xrpChain.Stop()
	xrpClient := xrpclient.NewXrpClient(xrpChain.GetHostRPCAddress())


	// Create 200 users, 5 per block
	numUsers := 200
	userBatches := 5
	xrpUsers := make([]ibc.Wallet, numUsers)
	for i := 0; i < numUsers/userBatches; i++ {
		for j := 0; j < userBatches; j++ {
			user, err := xrpChain.BuildWallet(ctx, fmt.Sprintf("user-%d", i*userBatches+j), "")
			require.NoError(t, err)
			_, err = xrpChain.SendFundsWithoutWait(
				ctx, "faucet", ibc.WalletAmount{
					Address: user.FormattedAddress(),
					Amount:  sdkmath.NewInt(10_000_000),
					Denom:   "",
				},
				"",
			)
			require.NoError(t, err)
			xrpUsers[i*userBatches+j] = user
		}
		err = xrpClient.ForceLedgerClose()
		require.NoError(t, err)
	}

	// Perform a user1 swap (both directions)
	swap1TxHash, minUser1BalancePostSwap, maxUser1BalancePostSwap, err := features.SingleSwapWithoutWait(ctx, thorchain, xrpChain, xrpUser1, gaiaChain, gaiaUser1)
	//err = features.SingleSwapWithoutWait(ctx, thorchain, gaiaChain, gaiaUser1, xrpChain, xrpUser1)
	// Send 199 txs
	for i := 0; i < numUsers-1; i++ {
		_, err = xrpChain.SendFundsWithoutWait(
			ctx, xrpUsers[i].KeyName(), ibc.WalletAmount{
				Address: xrpUsers[i+1].FormattedAddress(),
				Amount:  sdkmath.NewInt(1_000_000),
				Denom:   "",
			},
			"",
		)
		require.NoError(t, err)
	}
	// Perform a user2 swap (both directions)
	swap2TxHash, minUser2BalancePostSwap, maxUser2BalancePostSwap, err := features.SingleSwapWithoutWait(ctx, thorchain, xrpChain, xrpUser2, gaiaChain, gaiaUser2)
	//err = features.SingleSwapWithoutWait(ctx, thorchain, gaiaChain, gaiaUser2, xrpChain, xrpUser2)
	// Send 198 txs
	for i := 0; i < numUsers-2; i++ {
		_, err = xrpChain.SendFundsWithoutWait(
			ctx, xrpUsers[i].KeyName(), ibc.WalletAmount{
				Address: xrpUsers[i+2].FormattedAddress(),
				Amount:  sdkmath.NewInt(1_000_000),
				Denom:   "",
			},
			"",
		)
		require.NoError(t, err)
	}
	// Performa user3 swap (both directions)
	swap3TxHash, minUser3BalancePostSwap, maxUser3BalancePostSwap, err := features.SingleSwapWithoutWait(ctx, thorchain, xrpChain, xrpUser3, gaiaChain, gaiaUser3)
	//err = features.SingleSwapWithoutWait(ctx, thorchain, gaiaChain, gaiaUser3, xrpChain, xrpUser3)
	// Send 197 txs
	for i := 0; i < numUsers-3; i++ {
		_, err = xrpChain.SendFundsWithoutWait(
			ctx, xrpUsers[i].KeyName(), ibc.WalletAmount{
				Address: xrpUsers[i+3].FormattedAddress(),
				Amount:  sdkmath.NewInt(1_000_000),
				Denom:   "",
			},
			"",
		)
		require.NoError(t, err)
	}
	// Perform user4 swap (both directions)
	swap4TxHash, minUser4BalancePostSwap, maxUser4BalancePostSwap, err := features.SingleSwapWithoutWait(ctx, thorchain, xrpChain, xrpUser4, gaiaChain, gaiaUser4)
	// Send 196 txs
	for i := 0; i < numUsers-4; i++ {
		_, err = xrpChain.SendFundsWithoutWait(
			ctx, xrpUsers[i].KeyName(), ibc.WalletAmount{
				Address: xrpUsers[i+4].FormattedAddress(),
				Amount:  sdkmath.NewInt(1_000_000),
				Denom:   "",
			},
			"",
		)
		require.NoError(t, err)
	}
	// Perform user5 swap (both directions)
	swap5TxHash, minUser5BalancePostSwap, maxUser5BalancePostSwap, err := features.SingleSwapWithoutWait(ctx, thorchain, xrpChain, xrpUser5, gaiaChain, gaiaUser5)
	// Send 195 txs
	for i := 0; i < numUsers-5; i++ {
		_, err = xrpChain.SendFundsWithoutWait(
			ctx, xrpUsers[i].KeyName(), ibc.WalletAmount{
				Address: xrpUsers[i+5].FormattedAddress(),
				Amount:  sdkmath.NewInt(1_000_000),
				Denom:   "",
			},
			"",
		)
		require.NoError(t, err)
	}
	err = xrpClient.ForceLedgerClose()
	// Restart XRP 2-second block production
	time.Sleep(time.Second * 2)
	err = xrpClient.ForceLedgerClose()
	var cancelCtx context.CancelFunc
	go func() {
		goRoutineCtx := context.Background()
		goRoutineCtx, cancelCtx = context.WithCancel(goRoutineCtx)

		client := xrpClient
		xrpBlockTime := time.Second * 2
		timer := time.NewTimer(xrpBlockTime)
		defer timer.Stop()
		for {
			select {
			case <-goRoutineCtx.Done():
				return
			case <-timer.C:
				if err := client.ForceLedgerClose(); err != nil {
					fmt.Println("error force ledger close,", err) //nolint:forbidigo
				}
				timer.Reset(xrpBlockTime)
			}
		}
	}()

	// Wait and verify swaps succeeded
	err = features.SingleSwapVerify(ctx, thorchain, swap1TxHash, gaiaChain, gaiaUser1, minUser1BalancePostSwap, maxUser1BalancePostSwap)
	err = features.SingleSwapVerify(ctx, thorchain, swap2TxHash, gaiaChain, gaiaUser2, minUser2BalancePostSwap, maxUser2BalancePostSwap)
	err = features.SingleSwapVerify(ctx, thorchain, swap3TxHash, gaiaChain, gaiaUser3, minUser3BalancePostSwap, maxUser3BalancePostSwap)
	err = features.SingleSwapVerify(ctx, thorchain, swap4TxHash, gaiaChain, gaiaUser4, minUser4BalancePostSwap, maxUser4BalancePostSwap)
	err = features.SingleSwapVerify(ctx, thorchain, swap5TxHash, gaiaChain, gaiaUser5, minUser5BalancePostSwap, maxUser5BalancePostSwap)
	printVaultAddressesAndBalance(t, ctx, thorchain, exoChains)
	
	// Do another set of swaps for good measure
	PerformSwaps(t, ctx, thorchain, exoChains)
	printVaultAddressesAndBalance(t, ctx, thorchain, exoChains)
	cancelCtx()
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
