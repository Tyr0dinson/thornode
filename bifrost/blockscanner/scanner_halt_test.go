package blockscanner

import (
	"errors"
	"sync/atomic"
	"testing"

	"github.com/rs/zerolog"

	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/config"
)

type scannerHaltTestBridge struct {
	thorclient.ThorchainBridge
	getMimir         func(string) (int64, error)
	blockHeightCalls atomic.Int32
}

func (b *scannerHaltTestBridge) GetMimir(key string) (int64, error) {
	return b.getMimir(key)
}

func (b *scannerHaltTestBridge) GetBlockHeight() (int64, error) {
	b.blockHeightCalls.Add(1)
	return 100, nil
}

func TestXMRScannerHaltFailsClosed(t *testing.T) {
	tests := []struct {
		name           string
		scannerValue   int64
		scannerErr     error
		wantPaused     bool
		wantHeightCall int32
	}{
		{name: "enabled", scannerValue: 1, wantPaused: true},
		{name: "missing", scannerValue: -1, wantPaused: true},
		{name: "lookup error", scannerErr: errors.New("mimir unavailable"), wantPaused: true},
		{name: "clear", scannerValue: 0, wantPaused: false, wantHeightCall: 1},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			bridge := &scannerHaltTestBridge{
				getMimir: func(key string) (int64, error) {
					if key == "HaltScannerXMR" {
						return test.scannerValue, test.scannerErr
					}
					return 0, nil
				},
			}
			cfg := config.BifrostBlockScannerConfiguration{ChainID: common.XMRChain}
			if got := IsChainPaused(cfg, zerolog.Nop(), bridge); got != test.wantPaused {
				t.Fatalf("IsChainPaused() = %t, want %t", got, test.wantPaused)
			}
			if got := bridge.blockHeightCalls.Load(); got != test.wantHeightCall {
				t.Fatalf("GetBlockHeight calls = %d, want %d", got, test.wantHeightCall)
			}
		})
	}
}

func TestScannerHaltDoesNotFailClosedOtherChains(t *testing.T) {
	tests := []struct {
		name  string
		value int64
		err   error
	}{
		{name: "missing", value: -1},
		{name: "lookup error", err: errors.New("mimir unavailable")},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			bridge := &scannerHaltTestBridge{
				getMimir: func(key string) (int64, error) {
					switch key {
					case "HaltScannerXMR":
						return 1, nil
					case "HaltScannerETH":
						return test.value, test.err
					default:
						return 0, nil
					}
				},
			}
			cfg := config.BifrostBlockScannerConfiguration{ChainID: common.ETHChain}
			if IsChainPaused(cfg, zerolog.Nop(), bridge) {
				t.Fatal("an unavailable HaltScannerETH paused the ETH scanner")
			}
			if got := bridge.blockHeightCalls.Load(); got != 1 {
				t.Fatalf("GetBlockHeight calls = %d, want 1", got)
			}
		})
	}
}

func TestScannerHaltCanPauseAnotherChainExplicitly(t *testing.T) {
	bridge := &scannerHaltTestBridge{
		getMimir: func(key string) (int64, error) {
			if key == "HaltScannerETH" {
				return 1, nil
			}
			return 0, nil
		},
	}
	cfg := config.BifrostBlockScannerConfiguration{ChainID: common.ETHChain}
	if !IsChainPaused(cfg, zerolog.Nop(), bridge) {
		t.Fatal("positive HaltScannerETH did not pause the ETH scanner")
	}
	if got := bridge.blockHeightCalls.Load(); got != 0 {
		t.Fatalf("GetBlockHeight calls = %d, want 0", got)
	}
}
