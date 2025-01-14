package ictest

import (
	"github.com/strangelove-ventures/interchaintest/v8"
	"github.com/strangelove-ventures/interchaintest/v8/chain/cosmos"
//	"github.com/strangelove-ventures/interchaintest/v8/chain/ethereum/foundry"
	"github.com/strangelove-ventures/interchaintest/v8/chain/ethereum/geth"
	"github.com/strangelove-ventures/interchaintest/v8/chain/thorchain/common"
	"github.com/strangelove-ventures/interchaintest/v8/chain/utxo"
	"github.com/strangelove-ventures/interchaintest/v8/ibc"
)

type ExoChains map[string]*ExoChain

type ExoChain struct {
	chain      ibc.Chain
	lpers      []ibc.Wallet
	savers     []ibc.Wallet
	genWallets []ibc.Wallet
}

func (e ExoChains) GetChains() []ibc.Chain {
	var chains []ibc.Chain
	for _, exoChain := range e {
		chains = append(chains, exoChain.chain)
	}

	return chains
}

func XrpChainSpec() *interchaintest.ChainSpec {
	name := common.XRPChain.String()
	version := "2.3.0"
	numVals := 1
	numFn := 0
	chainConfig := ibc.ChainConfig{
		Type:           "xrp",
		Name:           name,
		ChainID:        "1234",
		Bech32Prefix:   "n/a",
		CoinType:       "144",
		Denom:          "drop",
		GasPrices:      "0.00001", // flat fee
		GasAdjustment:  0,         // N/A
		TrustingPeriod: "0",
		NoHostMount:    false,
		Images: []ibc.DockerImage{
			{
				Repository: "xrpllabsofficial/xrpld",
				Version:    version,
				UIDGID:     "1025:1025",
			},
		},
		Bin: "rippled,/opt/ripple/bin/validator-keys",
		HostPortOverride: map[int]int{
			80:    8001,
			5005:  5005,
			6006:  6006,
			51235: 51235,
		},
	}
	return &interchaintest.ChainSpec{
		Name: name,
		ChainName: name,
		Version:   version,
		ChainConfig: chainConfig,
		NumValidators: &numVals,
		NumFullNodes: &numFn,
	}
}

func GaiaChainSpec() *interchaintest.ChainSpec {
	name := common.GAIAChain.String() // Must use this name for tests
	version := "v18.1.0"
	numVals := 1
	numFn := 0
	denom := "uatom"
	gasPrices := "0.01uatom"
	genesisKVMods := []cosmos.GenesisKV{
		cosmos.NewGenesisKV("app_state.feemarket.params.enabled", false),
		cosmos.NewGenesisKV("app_state.feemarket.params.min_base_gas_price", "0.001000000000000000"),
		cosmos.NewGenesisKV("app_state.feemarket.state.base_gas_price", "0.001000000000000000"),
	}

	defaultChainConfig := ibc.ChainConfig{
		Denom:         denom,
		GasPrices:     gasPrices,
		ChainID:       "localgaia",
		ModifyGenesis: cosmos.ModifyGenesis(genesisKVMods),
	}

	return &interchaintest.ChainSpec{
		Name:          "gaia",
		ChainName:     name,
		Version:       version,
		ChainConfig:   defaultChainConfig,
		NumValidators: &numVals,
		NumFullNodes:  &numFn,
	}
}

func EthChainSpec(chainType string) *interchaintest.ChainSpec {
	ethChainName := common.ETHChain.String() // must use this name for test

	chainConfig := geth.DefaultEthereumGethChainConfig(ethChainName)
	// Anvil contracts are not currently imported into this repo
	//if chainType == "anvil" {
	//	chainConfig = foundry.DefaultEthereumAnvilChainConfig(ethChainName)
	//}

	return &interchaintest.ChainSpec{
		ChainName:   ethChainName,
		Name:        ethChainName,
		ChainConfig: chainConfig,
	}
}

func BscChainSpec() *interchaintest.ChainSpec {
	bscChainName := common.BSCChain.String() // must use this name for test

	return &interchaintest.ChainSpec{
		ChainName:   bscChainName,
		Name:        bscChainName,
		ChainConfig: geth.DefaultBscChainConfig(bscChainName),
	}
}

func BtcChainSpec() *interchaintest.ChainSpec {
	btcChainName := common.BTCChain.String() // must use this name for test

	return &interchaintest.ChainSpec{
		ChainName:   btcChainName,
		Name:        btcChainName,
		ChainConfig: utxo.DefaultBitcoinChainConfig(btcChainName, "thorchain", "password"),
	}
}

func BchChainSpec() *interchaintest.ChainSpec {
	bchChainName := common.BCHChain.String() // must use this name for test

	return &interchaintest.ChainSpec{
		ChainName:   bchChainName,
		Name:        bchChainName,
		ChainConfig: utxo.DefaultBitcoinCashChainConfig(bchChainName, "thorchain", "password"),
	}
}

func LtcChainSpec() *interchaintest.ChainSpec {
	liteChainName := common.LTCChain.String() // must use this name for test

	return &interchaintest.ChainSpec{
		ChainName:   liteChainName,
		Name:        liteChainName,
		ChainConfig: utxo.DefaultLitecoinChainConfig(liteChainName, "thorchain", "password"),
	}
}

func DogeChainSpec() *interchaintest.ChainSpec {
	dogeChainName := common.DOGEChain.String() // must use this name for test

	return &interchaintest.ChainSpec{
		ChainName:   dogeChainName,
		Name:        dogeChainName,
		ChainConfig: utxo.DefaultDogecoinChainConfig(dogeChainName, "thorchain", "password"),
	}
}
