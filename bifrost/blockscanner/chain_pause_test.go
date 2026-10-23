package blockscanner

import (
	"errors"
	"testing"

	"github.com/rs/zerolog"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/config"
)

type pauseCheckBridge struct {
	thorclient.ThorchainBridge
	height    int64
	heightErr error
	mimirs    map[string]int64
	mimirErrs map[string]error
}

func (b *pauseCheckBridge) GetBlockHeight() (int64, error) { return b.height, b.heightErr }

func (b *pauseCheckBridge) GetMimir(key string) (int64, error) {
	return b.mimirs[key], b.mimirErrs[key]
}

func TestCheckChainPausedBoundaries(t *testing.T) {
	for _, chain := range []common.Chain{common.DASHChain, common.ETHChain} {
		cfg := config.BifrostBlockScannerConfiguration{ChainID: chain}
		for _, key := range []string{"Halt" + chain.String() + "Chain", "SolvencyHalt" + chain.String() + "Chain", "HaltChainGlobal", "NodePauseChainGlobal"} {
			for _, value := range []int64{-1, 0, 99, 100, 101} {
				b := &pauseCheckBridge{height: 100, mimirs: map[string]int64{key: value}}
				want := value > 0 && value <= b.height
				if key == "NodePauseChainGlobal" {
					want = value > 0 && value >= b.height
				}
				paused, err := CheckChainPaused(cfg, zerolog.Nop(), b)
				if err != nil || paused != want {
					t.Fatalf("%s=%d: paused=%v, err=%v; want paused=%v", key, value, paused, err, want)
				}
				if got := IsChainPaused(cfg, zerolog.Nop(), b); got != want {
					t.Fatalf("legacy %s=%d: paused=%v; want %v", key, value, got, want)
				}
			}
		}
	}
}

func TestCheckChainPausedLookupErrors(t *testing.T) {
	cfg := config.BifrostBlockScannerConfiguration{ChainID: common.DASHChain}
	failed := errors.New("pause lookup failed")
	for _, lookup := range []string{"height", "HaltDASHChain", "SolvencyHaltDASHChain", "HaltChainGlobal", "NodePauseChainGlobal"} {
		t.Run(lookup, func(t *testing.T) {
			b := &pauseCheckBridge{height: 100, mimirs: map[string]int64{}, mimirErrs: map[string]error{}}
			if lookup == "height" {
				b.height = 0
				b.heightErr = failed
			} else {
				b.mimirErrs[lookup] = failed
			}
			paused, err := CheckChainPaused(cfg, zerolog.Nop(), b)
			if paused || !errors.Is(err, failed) {
				t.Fatalf("paused=%v, err=%v; want false and lookup error", paused, err)
			}
			if IsChainPaused(cfg, zerolog.Nop(), b) {
				t.Fatal("legacy caller must retain its best-effort unpaused result")
			}
			// Earlier failures must not hide an active pause found later. Keep
			// the returned value even when the last lookup itself reports an error.
			b.mimirs["NodePauseChainGlobal"] = 100
			paused, err = CheckChainPaused(cfg, zerolog.Nop(), b)
			if !paused || !errors.Is(err, failed) || !IsChainPaused(cfg, zerolog.Nop(), b) {
				t.Fatalf("active pause after lookup error: paused=%v, err=%v", paused, err)
			}
		})
	}
}

func TestCheckChainPausedKeepsFirstError(t *testing.T) {
	first := errors.New("height failed")
	b := &pauseCheckBridge{
		heightErr: first,
		mimirErrs: map[string]error{"HaltDASHChain": errors.New("mimir failed")},
	}
	_, err := CheckChainPaused(config.BifrostBlockScannerConfiguration{ChainID: common.DASHChain}, zerolog.Nop(), b)
	if !errors.Is(err, first) {
		t.Fatalf("got %v; want first lookup error", err)
	}
}

func TestCheckChainPausedMissingBridge(t *testing.T) {
	paused, err := CheckChainPaused(config.BifrostBlockScannerConfiguration{ChainID: common.DASHChain}, zerolog.Nop(), nil)
	if paused || err == nil {
		t.Fatalf("paused=%v, err=%v; want unknown pause state", paused, err)
	}
}
