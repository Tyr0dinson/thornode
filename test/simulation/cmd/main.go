package main

import (
	crypto_rand "crypto/rand"
	"encoding/hex"
	"io"
	"math/big"
	"math/rand/v2"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/rivo/tview"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"golang.org/x/term"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/test/simulation/actors/core"
	"gitlab.com/thorchain/thornode/v3/test/simulation/actors/features"
	"gitlab.com/thorchain/thornode/v3/test/simulation/actors/suites"
	"gitlab.com/thorchain/thornode/v3/test/simulation/pkg/cli"
	pkgcosmos "gitlab.com/thorchain/thornode/v3/test/simulation/pkg/cosmos"
	"gitlab.com/thorchain/thornode/v3/test/simulation/pkg/dag"
	"gitlab.com/thorchain/thornode/v3/test/simulation/pkg/evm"
	"gitlab.com/thorchain/thornode/v3/test/simulation/pkg/monero"
	"gitlab.com/thorchain/thornode/v3/test/simulation/pkg/solana"
	"gitlab.com/thorchain/thornode/v3/test/simulation/pkg/tron"
	. "gitlab.com/thorchain/thornode/v3/test/simulation/pkg/types"
	"gitlab.com/thorchain/thornode/v3/test/simulation/pkg/utxo"
	"gitlab.com/thorchain/thornode/v3/test/simulation/pkg/xrp"
	"gitlab.com/thorchain/thornode/v3/test/simulation/watchers"
)

////////////////////////////////////////////////////////////////////////////////////////
// Config
////////////////////////////////////////////////////////////////////////////////////////

const (
	DefaultParallelism = "8"
)

var liteClientConstructors = map[common.Chain]LiteChainClientConstructor{
	common.BTCChain:   utxo.NewConstructor(chainRPCs[common.BTCChain]),
	common.LTCChain:   utxo.NewConstructor(chainRPCs[common.LTCChain]),
	common.BCHChain:   utxo.NewConstructor(chainRPCs[common.BCHChain]),
	common.DOGEChain:  utxo.NewConstructor(chainRPCs[common.DOGEChain]),
	common.ETHChain:   evm.NewConstructor(chainRPCs[common.ETHChain]),
	common.AVAXChain:  evm.NewConstructor(chainRPCs[common.AVAXChain]),
	common.GAIAChain:  pkgcosmos.NewConstructor(chainRPCs[common.GAIAChain]),
	common.BASEChain:  evm.NewConstructor(chainRPCs[common.BASEChain]),
	common.TRONChain:  tron.NewConstructor(chainRPCs[common.TRONChain]),
	common.XRPChain:   xrp.NewConstructor(chainRPCs[common.XRPChain]),
	common.NOBLEChain: pkgcosmos.NewConstructor(chainRPCs[common.NOBLEChain]),
	common.SOLChain:   solana.NewConstructor(chainRPCs[common.SOLChain]),
	common.XMRChain:   monero.NewConstructor(chainRPCs[common.XMRChain]),
	common.POLChain:   evm.NewConstructor(chainRPCs[common.POLChain]),
	common.ZECChain:   utxo.NewConstructor(chainRPCs[common.ZECChain]),
}

type simulationStage struct {
	name        string
	defaultOn   bool
	constructor func(rng *rand.Rand) *Actor
}

func simulationStages() []simulationStage {
	return []simulationStage{
		{name: "seed", defaultOn: true},
		{name: "bootstrap", defaultOn: true, constructor: suites.Bootstrap},
		{name: "xmr-coverage", defaultOn: true, constructor: suites.XMRCoverage},
		{name: "arb", defaultOn: true, constructor: core.NewArbActor},
		{name: "swaps", defaultOn: true, constructor: suites.Swaps},
		{name: "memoless-swaps", defaultOn: true, constructor: suites.MemolessSwaps},
		{name: "consolidate", defaultOn: true, constructor: features.Consolidate},
		{name: "churn", defaultOn: false, constructor: core.NewChurnActor},
		{name: "inactive-vault-refunds", defaultOn: false, constructor: features.InactiveVaultRefunds},
		{name: "solvency", defaultOn: true, constructor: core.NewSolvencyCheckActor},
		{name: "xmr-post-churn", defaultOn: false, constructor: suites.XMRPostChurn},
		{name: "ragnarok", defaultOn: true, constructor: suites.Ragnarok},
	}
}

func simulationStageOptions() []cli.Option {
	stages := simulationStages()
	options := make([]cli.Option, 0, len(stages))
	for _, stage := range stages {
		options = append(options, cli.Option{Name: stage.name, Default: stage.defaultOn})
	}
	return options
}

func appendSimulationStages(root *Actor, enabledStages map[string]bool, rng *rand.Rand) {
	appendIfEnabled := func(key string, constructor func(rng *rand.Rand) *Actor) {
		if constructor == nil {
			return
		}
		if enabledStages[key] || enabledStages["all"] {
			root.Append(constructor(rng))
		}
	}

	for _, stage := range simulationStages() {
		appendIfEnabled(stage.name, stage.constructor)
	}
}

////////////////////////////////////////////////////////////////////////////////////////
// Init
////////////////////////////////////////////////////////////////////////////////////////

func init() {
	log.Logger = log.Output(zerolog.ConsoleWriter{
		Out:        os.Stdout,
		TimeFormat: time.TimeOnly,
	}).With().Caller().Logger()
}

////////////////////////////////////////////////////////////////////////////////////////
// Main
////////////////////////////////////////////////////////////////////////////////////////

func main() {
	// prompt to filter run stages if connected to a terminal
	enabledStages := map[string]bool{}
	stages := simulationStageOptions()
	if os.Getenv("STAGES") != "" {
		for _, stage := range strings.Split(os.Getenv("STAGES"), ",") {
			enabledStages[stage] = true
		}
	} else if term.IsTerminal(int(os.Stdout.Fd())) {
		app := tview.NewApplication()
		opts := cli.NewOptions(app, stages)
		opts.SetBorder(true).SetTitle(" Select Stages ")
		app.SetRoot(opts, true)
		if err := app.Run(); err != nil {
			panic(err)
		}
		enabledStages = opts.Selected()
	} else {
		for _, stage := range stages {
			enabledStages[stage.Name] = stage.Default
		}
	}

	// wait until bifrost is ready
	for {
		res, err := http.Get("http://localhost:6040/p2pid")
		if err == nil && res.StatusCode == 200 {
			break
		}
		log.Info().Msg("waiting for bifrost to be ready")
		time.Sleep(time.Second)
	}

	// wait for tron chain to produce its first block
	for {
		res, err := http.Post(
			"http://localhost:8090/wallet/getblockbynum",
			"application/json",
			strings.NewReader(`{"num":1}`),
		)

		if err == nil && res.StatusCode == 200 {
			defer res.Body.Close()

			data, err := io.ReadAll(res.Body)
			if err == nil && len(data) > 3 {
				break
			}
		}

		log.Info().Msg("waiting for tron to be ready")
		time.Sleep(time.Second)
	}

	for _, chain := range []common.Chain{
		common.ETHChain,
		common.AVAXChain,
		common.BASEChain,
		common.POLChain,
	} {
		waitForEVMRPCReadiness(chain, chainRPCs[chain])
	}

	var randSeed [32]byte
	randSeedHex := os.Getenv("RANDOM_SEED_HEX")
	if randSeedHex == "" {
		_, err := io.ReadFull(crypto_rand.Reader, randSeed[:])
		if err != nil {
			log.Fatal().Err(err).Msg("failed to generate random seed")
		}
	} else {
		randSeedBytes, err := hex.DecodeString(randSeedHex)
		if err != nil || len(randSeedBytes) > 32 {
			log.Fatal().Err(err).Msg("failed to parse RANDOM_SEED_HEX")
		}
		for byte_idx, byte_value := range randSeedBytes {
			randSeed[32-len(randSeedBytes)+byte_idx] = byte_value
		}
	}
	log.Info().Msgf("Random seed set to `%s`", new(big.Int).SetBytes(randSeed[:]).Text(16))
	rng := rand.New(rand.NewChaCha8(randSeed))

	// combine all actor dags for the complete test run
	root := NewActor("Root", rng)
	appendSimulationStages(root, enabledStages, rng)

	// gather config from the environment
	parallelism := os.Getenv("PARALLELISM")
	if parallelism == "" {
		parallelism = DefaultParallelism
	}
	parallelismInt, err := strconv.Atoi(parallelism)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to parse PARALLELISM")
	}

	cfg := InitConfig(parallelismInt, enabledStages["seed"] || enabledStages["all"], rng)

	// start watchers
	enabledWatchers := []*Watcher{
		watchers.NewInvariants(),
		watchers.NewSolvencyHalt(),
		watchers.NewSecurityEvents(),
	}
	for _, w := range enabledWatchers {
		log.Info().Str("watcher", w.Name).Msg("starting watcher")
		go func(w *Watcher) {
			err = w.Execute(cfg, log.Output(os.Stderr))
			if err != nil {
				log.Fatal().Err(err).Msg("watcher failed")
			}
		}(w)
	}

	// run the simulation
	dag.Execute(cfg, root, parallelismInt, rng)
}

func waitForEVMRPCReadiness(chain common.Chain, endpoint string) {
	for {
		res, err := http.Post(
			endpoint,
			"application/json",
			strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"eth_chainId","params":[]}`),
		)
		if err == nil && res.StatusCode == http.StatusOK {
			body, readErr := io.ReadAll(res.Body)
			res.Body.Close()
			if readErr == nil && strings.Contains(string(body), `"result"`) {
				return
			}
		} else if res != nil {
			res.Body.Close()
		}

		log.Info().Stringer("chain", chain).Msg("waiting for evm chain to be ready")
		time.Sleep(time.Second)
	}
}
