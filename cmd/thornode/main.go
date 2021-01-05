package main

import (
	"os"

	prefix "gitlab.com/thorchain/thornode/cmd"
	"gitlab.com/thorchain/thornode/cmd/thornode/cmd"
	"gitlab.com/thorchain/thornode/common/cosmos"
)

func main() {
	cosmosSDKConfg := cosmos.GetConfig()
	cosmosSDKConfg.SetBech32PrefixForAccount(prefix.Bech32PrefixAccAddr, prefix.Bech32PrefixAccPub)
	cosmosSDKConfg.Seal()

	rootCmd, _ := cmd.NewRootCmd()
	if err := cmd.Execute(rootCmd); err != nil {
		os.Exit(1)
	}
}
