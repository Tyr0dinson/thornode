//go:build !mocknet
// +build !mocknet

package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/http"
	"os"

	"github.com/rs/zerolog/log"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

const (
	rpcPort      = 27147
	p2pPort      = 27146
	ebifrostPort = 50051
)

// getSeedAddrs returns the IPs of active nodes from the seed nodes endpoint. A
// failure is returned rather than fatal: a node with an address book or configured
// peers can still start (an upgrading network's own API may be down while it waits
// for validators), so the caller decides how to proceed.
func getSeedAddrs() ([]string, error) {
	if config.Thornode.SeedNodesEndpoint == "" {
		return nil, errors.New("no seed nodes endpoint provided")
	}

	// get nodes
	res, err := http.Get(config.Thornode.SeedNodesEndpoint)
	if err != nil {
		return nil, fmt.Errorf("failed to get thorchain nodes: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("seed nodes endpoint returned status %d", res.StatusCode)
	}

	// parse nodes
	var nodes []types.NodeAccount
	if err = json.NewDecoder(res.Body).Decode(&nodes); err != nil {
		return nil, fmt.Errorf("failed to decode thorchain nodes: %w", err)
	}

	// include active nodes with an ip address
	var seeds []string
	for _, node := range nodes {
		if node.Status != types.NodeStatus_Active {
			continue
		}
		if node.IPAddress == "" || node.IPAddress == "0.0.0.0" {
			continue
		}
		seeds = append(seeds, node.IPAddress)
	}

	// randomly shuffle seeds
	rand.Shuffle(len(seeds), func(i, j int) {
		seeds[i], seeds[j] = seeds[j], seeds[i]
	})

	log.Info().Msgf("found %d thorchain seeds", len(seeds))

	return seeds, nil
}

func assertBifrostHasSeeds() {
	if config.Thornode.SeedNodesEndpoint == "" {
		log.Warn().Msg("no seed nodes endpoint provided, skipping seed file check")
		return
	}

	// fail if seed file is missing or empty since bifrost will hang
	seedPath := os.ExpandEnv("$HOME/.thornode/address_book.seed")
	fi, err := os.Stat(seedPath)
	if os.IsNotExist(err) {
		log.Fatal().Msg("no seed file found")
	}
	if err != nil {
		log.Fatal().Err(err).Msg("failed to stat seed file")
	}
	if fi.Size() == 0 {
		log.Fatal().Msg("seed file is empty")
	}
}
