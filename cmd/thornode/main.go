package main

import (
	"os"

	"gitlab.com/thorchain/thornode/cmd/thornode/cmd"
)

func main() {
	rootCmd, _ := cmd.NewRootCmd()
	if err := cmd.Execute(rootCmd); err != nil {
		os.Exit(1)
	}
}
