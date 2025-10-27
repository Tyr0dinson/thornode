package common

import (
	"os"
	"strings"

	"github.com/rs/zerolog/log"
	"gitlab.com/thorchain/thornode/v3/common"
)

////////////////////////////////////////////////////////////////////////////////////////
// Config
////////////////////////////////////////////////////////////////////////////////////////

func SimChains() []common.Chain {
	var chains []common.Chain

	for _, name := range strings.Split(os.Getenv("CHAINS"), ",") {
		if name == "" {
			break
		}

		chain, err := common.NewChain(name)
		if err != nil {
			log.Fatal().Err(err).Msg("failed to create chain")
		}
		chains = append(chains, chain)
	}

	if len(chains) == 0 {
		chains = []common.Chain{
			common.BTCChain,
			common.LTCChain,
			common.BCHChain,
			common.DOGEChain,
			common.ETHChain,
			common.AVAXChain,
			common.GAIAChain,
			common.BASEChain,
			common.TRONChain,
			common.XRPChain,
			common.NOBLEChain,
			common.SOLChain,
			common.TONChain,
		}
	}

	return chains
}
