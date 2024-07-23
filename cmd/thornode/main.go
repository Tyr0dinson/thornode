//go:build !regtest
// +build !regtest

package main

import (
	"os"

	svrcmd "github.com/cosmos/cosmos-sdk/server/cmd"
	"github.com/cosmos/cosmos-sdk/types"

	"gitlab.com/thorchain/thornode/app"
	prefix "gitlab.com/thorchain/thornode/cmd"
	"gitlab.com/thorchain/thornode/cmd/thornode/cmd"
	"gitlab.com/thorchain/thornode/common/cosmos"
)

func main() {
	config := cosmos.GetConfig()
	config.SetBech32PrefixForAccount(prefix.Bech32PrefixAccAddr, prefix.Bech32PrefixAccPub)
	config.SetBech32PrefixForValidator(prefix.Bech32PrefixValAddr, prefix.Bech32PrefixValPub)
	config.SetBech32PrefixForConsensusNode(prefix.Bech32PrefixConsAddr, prefix.Bech32PrefixConsPub)
	config.SetCoinType(prefix.THORChainCoinType)
	config.SetPurpose(prefix.THORChainCoinPurpose)
	config.Seal()
	types.SetCoinDenomRegex(func() string {
		return prefix.DenomRegex
	})

	rootCmd, _ := cmd.NewRootCmd()
	if err := svrcmd.Execute(rootCmd, "", app.DefaultNodeHome()); err != nil {
		os.Exit(1)
	}
}
