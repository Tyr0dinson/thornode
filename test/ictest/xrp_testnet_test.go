package ictest

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Peersyst/xrpl-go/pkg/crypto"
	"github.com/Peersyst/xrpl-go/xrpl/faucet"
	"github.com/Peersyst/xrpl-go/xrpl/queries/server"
	"github.com/Peersyst/xrpl-go/xrpl/rpc"
	"github.com/Peersyst/xrpl-go/xrpl/wallet"
	"github.com/strangelove-ventures/interchaintest/v8/chain/thorchain/common"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest"

	"github.com/strangelove-ventures/interchaintest/v8"
	"github.com/strangelove-ventures/interchaintest/v8/chain/cosmos"
	"github.com/strangelove-ventures/interchaintest/v8/chain/xrp"
	"github.com/strangelove-ventures/interchaintest/v8/ibc"
	"github.com/stretchr/testify/require"
)

func AddXrpTestnetChain(t *testing.T, log *zap.Logger, exoChains ExoChains) ExoChains {
	remoteRpc := "https://s.altnet.rippletest.net:51234/"
	name := common.XRPChain.String()
	coinDecimals := int64(6)
	chainConfig := ibc.ChainConfig{
		Type:         "xrp",
		Name:         name,
		ChainID:      "2",
		CoinType:     "144",
		CoinDecimals: &coinDecimals,
		Denom:        "drop",
		GasPrices:    "0.00001", // flat fee
		Bin:          "rippled,/opt/ripple/bin/validator-keys",
	}
	chain := xrp.NewXrpChain(t.Name(), chainConfig, log)
	rpcConfig, err := rpc.NewClientConfig(remoteRpc, rpc.WithFaucetProvider(faucet.NewTestnetFaucetProvider()))
	require.NoError(t, err)
	chain.RPCClient = rpc.NewClient(rpcConfig)

	// Pass in rpc address
	// Pass in a number of wallets?
	exoChains[name] = &ExoChain{
		chain:     chain,
		remoteRpc: remoteRpc,
	}
	return exoChains
}

func TestFaucet(t *testing.T) {
	rpcConfig, err := rpc.NewClientConfig("https://s.altnet.rippletest.net:51234/", rpc.WithFaucetProvider(faucet.NewTestnetFaucetProvider()))
	require.NoError(t, err)
	r := rpc.NewClient(rpcConfig)

	w, err := wallet.New(crypto.SECP256K1())
	require.NoError(t, err)

	err = r.FundWallet(&w)
	require.NoError(t, err)

	balance, err := r.GetXrpBalance(w.GetAddress())
	require.NoError(t, err)

	fmt.Println("Address:", w.GetAddress(), "Balance:", balance, "Seed:", w.Seed)
	require.Equal(t, "100", balance)

	// Ensure testnet has the account reserve we expect
	resp, err := r.GetServerState(&server.StateRequest{})
	require.NoError(t, err)

	require.Equal(t, uint(1000000), resp.State.ValidatedLedger.ReserveBase)
	require.Equal(t, uint(200000), resp.State.ValidatedLedger.ReserveInc)
}

// go test -timeout 20m -v -run TestXrpTestnet examples/thorchain/*.go -count 1
// go test -timeout 20m -v -run TestXrpTestnet . -count 1
func TestXrpTestnet(t *testing.T) {
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
		GaiaChainSpec(),
	})
	exoChains = AddXrpTestnetChain(t, zaptest.NewLogger(t), exoChains)

	ethRouterContractAddress, bscRouterContractAddress := "", ""

	// Start thorchain
	chainImage := ibc.NewDockerImage("thorchain", "local", "1025:1025")
	thorchain := StartThorchain(t, ctx, client, network, 1, numFns, mainnet, chainImage, exoChains, ethRouterContractAddress, bscRouterContractAddress)
	gaiaChain := exoChains["GAIA"].chain.(*cosmos.CosmosChain)
	xrpChain := exoChains["XRP"].chain.(*xrp.XrpChain)
	err := setNetworkFee(thorchain.Validators, exoChains)
	require.NoError(t, err)

	printVaultAddressesAndBalance(t, ctx, thorchain, exoChains)
	if numVals > 1 {
		err := addValidators(t, ctx, thorchain, numVals-1)
		require.NoError(t, err)
	}
	printVaultAddressesAndBalance(t, ctx, thorchain, exoChains)

	// XRP testnet faucet funds accounts with 100 XRP, run bootstrap pools 3x to get 270 XRP (90% of 100 XRP x 3) into pool for greater pool depth
	BootstrapPools(t, ctx, thorchain, exoChains)
	BootstrapPools(t, ctx, thorchain, exoChains)
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
