package ictest

import (
	"context"
	_ "embed"
	"fmt"
	"strings"
	"testing"
	"time"

	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkmath "cosmossdk.io/math"

	"github.com/docker/docker/client"
	ethcommon "github.com/ethereum/go-ethereum/common"

	"github.com/strangelove-ventures/interchaintest/v8"
	"github.com/strangelove-ventures/interchaintest/v8/chain/ethereum"
	"github.com/strangelove-ventures/interchaintest/v8/chain/ethereum/foundry"
	"github.com/strangelove-ventures/interchaintest/v8/chain/ethereum/geth"
	tc "github.com/strangelove-ventures/interchaintest/v8/chain/thorchain"
	"github.com/strangelove-ventures/interchaintest/v8/chain/utxo"
	"github.com/strangelove-ventures/interchaintest/v8/chain/xrp"
	"github.com/strangelove-ventures/interchaintest/v8/ibc"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zaptest"
	"golang.org/x/sync/errgroup"

	"gitlab.com/thorchain/thornode/v3/build/scripts/evm"

)

func StartExoChains(t *testing.T, ctx context.Context, client *client.Client, network string, chainSpecs []*interchaintest.ChainSpec) ExoChains {
	cf0 := interchaintest.NewBuiltinChainFactory(zaptest.NewLogger(t), chainSpecs)

	chains, err := cf0.Chains(t.Name())
	require.NoError(t, err)

	exoChains := make(map[string]*ExoChain, len(chains))
	for _, chain := range chains {
		name := chain.Config().Name
		exoChains[name] = &ExoChain{
			chain: chain,
		}

		if name == "BTC" || name == "BCH" || name == "LTC" {
			utxoChain := chain.(*utxo.UtxoChain)
			utxoChain.UnloadWalletAfterUse(true)
		}
	}

	ic := interchaintest.NewInterchain()
	for _, chain := range chains {
		name := chain.Config().Name
		var additionalGenesisWallets []ibc.WalletAmount
		for _, wallet := range exoChains[name].genWallets {
			additionalGenesisWallets = append(additionalGenesisWallets, ibc.WalletAmount{
				Address: wallet.FormattedAddress(),
				Amount:  sdkmath.NewInt(100_000_000),
				Denom:   chain.Config().Denom,
			})
		}
		if name == "GAIA" {
			// this wallet just stops bifrost complaining about it not existing
			additionalGenesisWallets = append(additionalGenesisWallets, ibc.WalletAmount{
				Address: "cosmos1zf3gsk7edzwl9syyefvfhle37cjtql35427vcp",
				Amount:  sdkmath.NewInt(1_000_000),
				Denom:   chain.Config().Denom,
			})
		}
		ic.AddChain(chain, additionalGenesisWallets...)
	}

	require.NoError(t, ic.Build(ctx, nil, interchaintest.InterchainBuildOptions{
		TestName:         t.Name(),
		Client:           client,
		NetworkID:        network,
		SkipPathCreation: true,
	}))
	t.Cleanup(func() {
		_ = ic.Close()
		for _, chain := range chains {
			utxoChain, ok := chain.(*utxo.UtxoChain)
			if ok {
				utxoChain.Stop()
			}
			xrpChain, ok := chain.(*xrp.XrpChain)
			if ok {
				xrpChain.Stop()
			}
		}
	})

	return exoChains
}

func StartThorchain(
	t *testing.T,
	ctx context.Context,
	client *client.Client,
	network string,
	numValidators int,
	numFullNodes int,
	mainnet bool,
	chainImage ibc.DockerImage,
	exoChains ExoChains,
	ethRouterContractAddress string,
	bscRouterContractAddress string,
) *tc.Thorchain {
	Bech32PrefixAccAddr  := "tthor"
	Bech32PrefixAccPub   := "tthorpub"
	Bech32PrefixValAddr  := "tthorv"
	Bech32PrefixValPub   := "tthorvpub"
	Bech32PrefixConsAddr := "tthorc"
	Bech32PrefixConsPub  := "tthorcpub"
	if mainnet {
		Bech32PrefixAccAddr  = "thor"
		Bech32PrefixAccPub   = "thorpub"
		Bech32PrefixValAddr  = "thorv"
		Bech32PrefixValPub   = "thorvpub"
		Bech32PrefixConsAddr = "thorc"
		Bech32PrefixConsPub  = "thorcpub"
	}
	cfg := sdk.GetConfig()
	cfg.SetBech32PrefixForAccount(Bech32PrefixAccAddr, Bech32PrefixAccPub)
	cfg.SetBech32PrefixForValidator(Bech32PrefixValAddr, Bech32PrefixValPub)
	cfg.SetBech32PrefixForConsensusNode(Bech32PrefixConsAddr, Bech32PrefixConsPub)
	cfg.Seal()

	bifrostEnvOverrides := map[string]string{
		"BIFROST_CHAINS_GAIA_BLOCK_SCANNER_START_BLOCK_HEIGHT": "2",
	}
	for _, exoChain := range exoChains {
		rpcAddress := ""
		if exoChain.chain.Config().Images == nil {
			rpcAddress = exoChain.remoteRpc
		} else {
			rpcAddress = exoChain.chain.GetRPCAddress()
		}
		name := exoChain.chain.Config().Name
		hostKey := fmt.Sprintf("%s_HOST", name)
		bifrostEnvOverrides[hostKey] = rpcAddress
		if name == "GAIA" {
			hostGRPCKey := fmt.Sprintf("%s_GRPC_HOST", name)
			bifrostEnvOverrides[hostGRPCKey] = exoChain.chain.GetGRPCAddress()
		}
		disableChainKey := fmt.Sprintf("BIFROST_CHAINS_%s_DISABLED", name)
		bifrostEnvOverrides[disableChainKey] = "false"
		if name == "BSC" || name == "XRP" {
			hostKey = fmt.Sprintf("BIFROST_CHAINS_%s_RPC_HOST", name)
			bifrostEnvOverrides[hostKey] = rpcAddress
			bsRpcHost := fmt.Sprintf("BIFROST_CHAINS_%s_BLOCK_SCANNER_RPC_HOST", name)
			bifrostEnvOverrides[bsRpcHost] = rpcAddress
		}
		if name == "XRP" {
			hostKey = fmt.Sprintf("BIFROST_CHAINS_%s_CHAIN_NETWORK", name)
			bifrostEnvOverrides[hostKey] = exoChain.chain.Config().ChainID
		}
	}
	thorchainChainSpec := ThorchainDefaultChainSpec(t.Name(), chainImage, mainnet, numValidators,
		numFullNodes, ethRouterContractAddress, bscRouterContractAddress, nil, bifrostEnvOverrides)

	if exoChains == nil {
		thorchainChainSpec.SidecarConfigs = nil
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

	if exoChains != nil {
		err = thorchain.StartAllValSidecars(ctx)
		require.NoError(t, err, "failed starting validator sidecars")

		// Give some time for bifrost to initialize before any tests start
		time.Sleep(time.Second * 15)
		require.NoError(t, err)
	}

	return thorchain
}

func SetupContracts(ctx context.Context, ethExoChain *ExoChain, bscExoChain *ExoChain) (ethContractAddr, bscContractAddr string, err error) {
	eg, egCtx := errgroup.WithContext(ctx)
	eg.Go(func() error {
		var err error
		if ethExoChain.chain.Config().Bin == "geth" {
			ethContractAddr, err = SetupGethContracts(egCtx, ethExoChain)
		} else {
			ethContractAddr, err = SetupAnvilContracts(egCtx, ethExoChain) 
		}
		return err
	})
	eg.Go(func() error {
		var err error
		bscContractAddr, err = SetupGethContracts(egCtx, bscExoChain)
		return err
	})
		
	return ethContractAddr, bscContractAddr, eg.Wait()
}

func SetupGethContracts(ctx context.Context, exoChain *ExoChain) (string, error) {
	abi := evm.RouterAbi
	byteCode := evm.RouterByteCode
	if exoChain.chain.Config().Name == "ETH" {
		abi = evm.EthRouterAbi
		byteCode = append(evm.EthRouterByteCode, []byte("000000000000000000000000de06987c28d839daaefb6c85816a2cc55277654c")...) // RUNE token (doesn't matter)
	}

	ethChain := exoChain.chain.(*geth.GethChain)

	ethUserInitialAmount := ethereum.ETHER.MulRaw(100)

	ethUser, err := interchaintest.GetAndFundTestUserWithMnemonic(ctx, "user", strings.Repeat("dog ", 23)+"fossil", ethUserInitialAmount, ethChain)
	if err != nil {
		return "", err
	}

	ethRouterContractAddress, err := ethChain.DeployContract(ctx, ethUser.KeyName(), abi, byteCode)
	if err != nil {
		return "", err
	}
	if ethRouterContractAddress == "" {
		return "", fmt.Errorf("router contract address for (%s) chain is empty", ethChain.Config().Name)
	}
	if !ethcommon.IsHexAddress(ethRouterContractAddress) {
		return "", fmt.Errorf("router contract address for (%s) chain is not a hex address", ethChain.Config().Name)
	}

	return ethRouterContractAddress, nil
}

func SetupAnvilContracts(ctx context.Context, exoChain *ExoChain) (string, error) {
	ethChain := exoChain.chain.(*foundry.AnvilChain)

	ethUserInitialAmount := ethereum.ETHER.MulRaw(2)

	ethUser, err := interchaintest.GetAndFundTestUserWithMnemonic(ctx, "user", strings.Repeat("dog ", 23)+"fossil", ethUserInitialAmount, ethChain)
	if err != nil {
		return "", err
	}

	stdout, _, err := ethChain.ForgeScript(ctx, ethUser.KeyName(), foundry.ForgeScriptOpts{
		ContractRootDir:  "contracts",
		SolidityContract: "script/Token.s.sol",
		RawOptions:       []string{"--sender", ethUser.FormattedAddress(), "--json"},
	})
	if err != nil {
		return "", err
	}

	tokenContractAddress, err := GetEthAddressFromStdout(string(stdout))
	if err != nil {
		return "", err
	}
	if tokenContractAddress == "" {
		return "", fmt.Errorf("token contract address for (%s) chain is empty", ethChain.Config().Name)
	}
	if !ethcommon.IsHexAddress(tokenContractAddress) {
		return "", fmt.Errorf("token contract address for (%s) chain is not a hex address", ethChain.Config().Name)
	}

	stdout, _, err = ethChain.ForgeScript(ctx, ethUser.KeyName(), foundry.ForgeScriptOpts{
		ContractRootDir:  "contracts",
		SolidityContract: "script/Router.s.sol",
		RawOptions:       []string{"--sender", ethUser.FormattedAddress(), "--json"},
	})
	if err != nil {
		return "", err
	}

	ethRouterContractAddress, err := GetEthAddressFromStdout(string(stdout))
	if err != nil {
		return "", err
	}
	if ethRouterContractAddress == "" {
		return "", fmt.Errorf("router contract address for (%s) chain is empty", ethChain.Config().Name)
	}
	if !ethcommon.IsHexAddress(ethRouterContractAddress) {
		return "", fmt.Errorf("router contract address for (%s) chain is not a hex address", ethChain.Config().Name)
	}

	return ethRouterContractAddress, nil
}
