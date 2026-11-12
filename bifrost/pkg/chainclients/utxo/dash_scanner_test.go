package utxo

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/btcsuite/btcd/btcjson"
	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	cryptocodec "github.com/cosmos/cosmos-sdk/crypto/codec"
	"github.com/cosmos/cosmos-sdk/crypto/hd"
	"github.com/cosmos/cosmos-sdk/crypto/keyring"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/bifrost/blockscanner"
	btypes "gitlab.com/thorchain/thornode/v3/bifrost/blockscanner/types"
	sharedutxo "gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/utxo"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/cmd"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/config"
	ttypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

type dashCatchUpState struct {
	local, locked        int64
	unknown, wrongHash   bool
	failMethod, badBlock string
}

type dashCatchUpBridge struct {
	dashHealthBridge
	balanceReads atomic.Int64
}

func (*dashCatchUpBridge) WaitToCatchUp() error { return nil }

func (*dashCatchUpBridge) GetLastObservedInHeight(common.Chain) (int64, error) { return 0, nil }

func (*dashCatchUpBridge) FetchNodeStatus() (ttypes.NodeStatus, error) {
	return ttypes.NodeStatus_Standby, nil
}

// GetInstantSend keeps a fixed accounting snapshot available during catch-up.
func (b *dashCatchUpBridge) GetInstantSend(chain common.Chain) (*ttypes.QueryInstantSendResponse, error) {
	key, err := dashTestVaultPubKey()
	if err != nil {
		return nil, err
	}
	return &ttypes.QueryInstantSendResponse{Vaults: []ttypes.QueryInstantSendVault{{State: common.InstantSendState{Chain: common.DASHChain, PubKey: key}}}}, nil
}

type dashCatchUpFixture struct {
	state        atomic.Pointer[dashCatchUpState]
	missing981   atomic.Int64
	maxRequested atomic.Int64
	maxFetched   atomic.Int64
	heightReads  atomic.Int64
	bridge       dashCatchUpBridge
}

func dashCatchUpHash(height int64) string { return fmt.Sprintf("%064x", height) }

// Construct the production client to exercise scanner wiring, including
// NewBlockScanner's last-resort startup height lookup.
func newDashCatchUpClient(c *C, start, prefetch int64) (*Client, *dashCatchUpFixture, func()) {
	f := &dashCatchUpFixture{}
	f.state.Store(&dashCatchUpState{local: 980, locked: 1000})
	server := newRPCTestServer(c, func(method string, params []json.RawMessage) any {
		state := f.state.Load()
		if method == state.failMethod {
			return &btcjson.RPCError{Code: btcjson.ErrRPCInternal.Code, Message: "test RPC failure"}
		}
		switch method {
		case "listunspent":
			f.bridge.balanceReads.Add(1)
			return []btcjson.ListUnspentResult{}
		case "createwallet":
			return map[string]string{"name": ""}
		case "importaddress":
			return nil
		case "getnetworkinfo":
			return map[string]any{"relayfee": 0.00001}
		case "getbestchainlock":
			f.heightReads.Add(1)
			return map[string]any{"height": state.locked, "blockhash": dashCatchUpHash(state.locked), "known_block": !state.unknown}
		case "getblockcount":
			return state.local
		case "getblockhash":
			var height int64
			c.Assert(json.Unmarshal(params[0], &height), IsNil)
			for previous := f.maxRequested.Load(); height > previous; previous = f.maxRequested.Load() {
				if f.maxRequested.CompareAndSwap(previous, height) {
					break
				}
			}
			if height > state.local {
				if height == 981 {
					f.missing981.Add(1)
				}
				return &btcjson.RPCError{Code: btcjson.ErrRPCInvalidParameter, Message: "Block height out of range"}
			}
			if state.wrongHash {
				return dashCatchUpHash(height + 1)
			}
			return dashCatchUpHash(height)
		case "getblock":
			var hash string
			c.Assert(json.Unmarshal(params[0], &hash), IsNil)
			height, err := strconv.ParseInt(hash, 16, 64)
			c.Assert(err, IsNil)
			for previous := f.maxFetched.Load(); height > previous; previous = f.maxFetched.Load() {
				if f.maxFetched.CompareAndSwap(previous, height) {
					break
				}
			}
			block := map[string]any{"hash": hash, "height": height, "previousblockhash": dashCatchUpHash(height - 1), "chainlock": height <= state.locked, "tx": []any{}}
			switch state.badBlock {
			case "unlocked":
				block["chainlock"] = false
			case "height":
				block["height"] = height + 1
			case "hash":
				block["hash"] = dashCatchUpHash(height + 1)
			}
			return block
		case "getblockstats":
			return map[string]any{"avgfeerate": 0}
		default:
			c.Errorf("unexpected RPC %s", method)
			return nil
		}
	})
	ttypes.SetupConfigForTest()
	registry := codectypes.NewInterfaceRegistry()
	cryptocodec.RegisterInterfaces(registry)
	kb := keyring.NewInMemory(codec.NewProtoCodec(registry))
	_, _, err := kb.NewMnemonic(bob, keyring.English, cmd.THORChainHDPath, password, hd.Secp256k1)
	c.Assert(err, IsNil)
	cfg := config.BifrostChainConfiguration{
		ChainID: common.DASHChain, RPCHost: server.URL,
		BlockScanner: config.BifrostBlockScannerConfiguration{
			ChainID: common.DASHChain, StartBlockHeight: start, PrefetchBlocks: prefetch,
			BlockHeightDiscoverBackoff: time.Millisecond, HTTPRequestTimeout: time.Second,
			MaxHealthyLag: 30 * time.Minute,
			// Make every test block eligible for the solvency step; health must
			// still suppress it while the daemon is behind its ChainLock.
			ObservationFlexibilityBlocks: 1000,
		},
	}
	cfg.UTXO.BlockCacheCount = 2000
	cfg.UTXO.EstimatedAverageTxSize = 2000
	client, err := NewClient(thorclient.NewKeysWithKeybase(kb, bob, password), cfg, nil, &f.bridge, GetMetricForTest(c, common.DASHChain))
	c.Assert(err, IsNil)
	client.globalNetworkFeeQueue = make(chan common.NetworkFee, 100)
	client.globalSolvencyQueue = make(chan types.Solvency, 100)
	return client, f, func() { client.Stop(); server.Close() }
}

func waitForDashCatchUp(c *C, condition func() bool) {
	deadline := time.Now().Add(5 * time.Second)
	for !condition() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	c.Assert(condition(), Equals, true)
}

func (s *DashSuite) TestDashScannerCatchesUpThroughAvailableBlocks(c *C) {
	for _, prefetch := range []int64{0, 3} {
		func() {
			client, f, cleanup := newDashCatchUpClient(c, 970, prefetch)
			defer cleanup()
			client.blockScanner.Start(make(chan types.TxIn, 100), make(chan common.NetworkFee, 100))
			waitForDashCatchUp(c, func() bool { return f.missing981.Load() >= 3 })
			c.Assert(client.blockScanner.PreviousHeight(), Equals, int64(980))
			c.Assert(client.IsBlockScannerHealthy(), Equals, false)
			c.Assert(f.bridge.balanceReads.Load(), Equals, int64(0))
			target, err := client.GetHeight()
			c.Assert(err, IsNil)
			c.Assert(target, Equals, int64(1000))
			c.Assert(client.ReportSolvency(target), NotNil)

			f.state.Store(&dashCatchUpState{local: 999, locked: 1000})
			waitForDashCatchUp(c, func() bool { return client.blockScanner.PreviousHeight() == 999 })
			c.Assert(client.IsBlockScannerHealthy(), Equals, false)
			c.Assert(f.bridge.balanceReads.Load(), Equals, int64(0))
			for height := int64(971); height <= 999; height++ {
				var meta *sharedutxo.BlockMeta
				meta, err = client.temporalStorage.GetBlockMeta(height)
				c.Assert(err, IsNil)
				c.Assert(meta, NotNil) // Every intervening block was fetched; none skipped.
				c.Assert(meta.BlockHash, Equals, dashCatchUpHash(height))
			}

			// One ordinary block above the ChainLock must retain the existing
			// one-block signing tolerance and must never be fetched by the scanner.
			f.state.Store(&dashCatchUpState{local: 1001, locked: 1000})
			waitForDashCatchUp(c, func() bool { return client.blockScanner.PreviousHeight() == 1000 && client.IsBlockScannerHealthy() })
			c.Assert(f.maxFetched.Load(), Equals, int64(1000)) // Solvency may read the tip hash, but scanning cannot fetch its block.
			height, err := client.GetHeight()
			c.Assert(err, IsNil)
			c.Assert(height, Equals, int64(1000))
		}()
	}
}

func (s *DashSuite) TestDashScannerHeightValidation(c *C) {
	client, f, cleanup := newDashCatchUpClient(c, 970, 0)
	defer cleanup()
	for _, unknown := range []bool{false, true} {
		f.state.Store(&dashCatchUpState{local: 980, locked: 1000, unknown: unknown})
		height, err := client.GetHeight()
		c.Assert(err, IsNil)
		c.Assert(height, Equals, int64(1000))
		c.Assert(client.chainLockHeightHealth.Load().isHealthy(common.DASHChain, time.Now(), 980), Equals, false)
		c.Assert(f.maxRequested.Load(), Equals, int64(0)) // No lookup of the unavailable target.
		c.Assert(client.ReportSolvency(height), NotNil)
		c.Assert(f.bridge.balanceReads.Load(), Equals, int64(0))
	}
	for _, state := range []dashCatchUpState{
		{local: 1000, locked: 1000, unknown: true},
		{local: 1000, locked: 1000, wrongHash: true},
		{local: -1, locked: 1000},
		{local: 1000, locked: -1},
		{local: 1000, locked: 1000, failMethod: "getbestchainlock"},
		{local: 1000, locked: 1000, failMethod: "getblockcount"},
		{local: 1000, locked: 1000, failMethod: "getblockhash"},
	} {
		f.state.Store(&state)
		stale := &chainLockHeightHealth{1000, 1000, time.Now().Add(-time.Hour)}
		client.chainLockHeightHealth.Store(stale)
		_, err := client.GetHeight()
		c.Assert(err, NotNil)
		c.Assert(client.chainLockHeightHealth.Load(), Equals, stale)
	}
	for _, invalid := range []string{"unlocked", "height", "hash"} {
		f.state.Store(&dashCatchUpState{local: 980, locked: 1000, badBlock: invalid})
		_, err := client.FetchTxs(980, 1000)
		c.Assert(errors.Is(err, btypes.ErrUnavailableBlock), Equals, true)
		meta, err := client.temporalStorage.GetBlockMeta(980)
		c.Assert(err, IsNil)
		c.Assert(meta, IsNil)
	}
}

func (s *DashSuite) TestDashScannerStartsWithoutSavedPositionDuringCatchUp(c *C) {
	client, _, cleanup := newDashCatchUpClient(c, 0, 0)
	defer cleanup()
	c.Assert(client.blockScanner.PreviousHeight(), Equals, int64(1000))
	c.Assert(client.IsBlockScannerHealthy(), Equals, false)
	height, err := client.GetHeight()
	c.Assert(err, IsNil)
	c.Assert(height, Equals, int64(1000))
	c.Assert(client.ReportSolvency(height), NotNil)
}

// Solvency continues independently of scanner health and InstantSend policy.
func (s *DashSuite) TestDashSolvencyAfterDaemonCatchUp(c *C) {
	client, f, cleanup := newDashCatchUpClient(c, 970, 0)
	defer cleanup()
	client.instantSend.enabled = false
	c.Assert(client.ReportSolvency(1000), NotNil)
	c.Assert(f.bridge.balanceReads.Load(), Equals, int64(0))
	f.state.Store(&dashCatchUpState{local: 1001, locked: 1000})
	c.Assert(client.ReportSolvency(1000), IsNil)
	c.Assert(f.bridge.balanceReads.Load(), Equals, int64(1))
	c.Assert(client.globalSolvencyQueue, HasLen, 0) // solvent and not halted
	c.Assert(client.IsBlockScannerHealthy(), Equals, false)
}

// ReportSolvency must validate fresh RPC data even if GetHeight recently succeeded.
func (s *DashSuite) TestDashReportSolvencyValidatesLocalChainLock(c *C) {
	client, f, cleanup := newDashCatchUpClient(c, 970, 0)
	defer cleanup()
	f.state.Store(&dashCatchUpState{local: 1001, locked: 1000})
	height, err := client.GetHeight()
	c.Assert(err, IsNil)
	c.Assert(height, Equals, int64(1000))

	for _, state := range []dashCatchUpState{
		{local: 980, locked: 1000},
		{local: 999, locked: 1000, unknown: true},
		{local: 1000, locked: 1000, unknown: true},
		{local: 1000, locked: 1000, wrongHash: true},
		{local: -1, locked: 1000},
		{local: 1000, locked: -1},
		{local: 1000, locked: 1000, failMethod: "getbestchainlock"},
		{local: 1000, locked: 1000, failMethod: "getblockcount"},
		{local: 1000, locked: 1000, failMethod: "getblockhash"},
	} {
		f.state.Store(&state)
		c.Assert(client.ReportSolvency(height), NotNil, Commentf("state: %+v", state))
		c.Assert(f.bridge.balanceReads.Load(), Equals, int64(0))
		c.Assert(client.lastSolvencyCheckHeight.Load(), Equals, int64(0))
		c.Assert(client.globalSolvencyQueue, HasLen, 0)
	}

	// A failed attempt must not suppress a retry at the same height.
	f.state.Store(&dashCatchUpState{local: 1000, locked: 1000})
	c.Assert(client.ReportSolvency(height), IsNil)
	c.Assert(f.bridge.balanceReads.Load(), Equals, int64(1))
	c.Assert(client.lastSolvencyCheckHeight.Load(), Equals, height)
}

func (s *DashSuite) TestDashGetHeightUsesBestChainLock(c *C) {
	hash := "0000000000000000000000000000000000000000000000000000000000000123"
	known := true
	activeHash := hash
	server := newRPCTestServer(c, func(method string, _ []json.RawMessage) any {
		switch method {
		case "getbestchainlock":
			return map[string]any{"blockhash": hash, "height": 321, "known_block": known}
		case "getblockcount":
			return 322
		case "getblockhash":
			return activeHash
		}
		c.Fatalf("unexpected method %s", method)
		return nil
	})
	defer server.Close()
	client, cleanup := newDashClientForRPCServer(c, server.URL)
	defer cleanup()
	height, err := client.GetHeight()
	c.Assert(err, IsNil)
	c.Assert(height, Equals, int64(321))
	known = false
	_, err = client.GetHeight()
	c.Assert(err, NotNil)
	known = true
	activeHash = "different"
	_, err = client.GetHeight()
	c.Assert(err, NotNil)
}

func TestDashHeightHealthBoundaries(t *testing.T) {
	now := time.Now()
	for _, tc := range []struct {
		name    string
		health  *chainLockHeightHealth
		scanned int64
		want    bool
	}{
		{"no successful read", nil, 100, false},
		{"missing timestamp", &chainLockHeightHealth{tipHeight: 100, chainLockHeight: 100}, 100, false},
		{"caught up", &chainLockHeightHealth{100, 100, now}, 100, true},
		{"one block finality gap", &chainLockHeightHealth{101, 100, now}, 100, true},
		{"two block finality gap", &chainLockHeightHealth{102, 100, now}, 100, false},
		{"just before expiry", &chainLockHeightHealth{100, 100, now.Add(-150*time.Second + time.Nanosecond)}, 100, true},
		{"at expiry", &chainLockHeightHealth{100, 100, now.Add(-150 * time.Second)}, 100, false},
		{"after expiry", &chainLockHeightHealth{100, 100, now.Add(-time.Hour)}, 100, false},
		{"locks recovered but scanner behind", &chainLockHeightHealth{110, 110, now}, 100, false},
		{"one block scanner gap", &chainLockHeightHealth{110, 110, now}, 109, true},
		{"two block scanner gap", &chainLockHeightHealth{110, 110, now}, 108, false},
		{"combined gap exceeds limit", &chainLockHeightHealth{110, 109, now}, 108, false},
		{"daemon behind ChainLock", &chainLockHeightHealth{980, 1000, now}, 980, false},
		{"daemon one block behind ChainLock", &chainLockHeightHealth{999, 1000, now}, 999, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.health.isHealthy(common.DASHChain, now, tc.scanned); got != tc.want {
				t.Fatalf("health = %v, want %v", got, tc.want)
			}
		})
	}
}

type dashHealthBridge struct{ thorclient.ThorchainBridge }

func (dashHealthBridge) GetBlockHeight() (int64, error) { return 1, nil }

func (dashHealthBridge) GetMimir(string) (int64, error) { return 0, nil }

func (dashHealthBridge) GetNetworkFee(common.Chain) (uint64, uint64, error) {
	return 1, 1, nil
}

// Drive the real scanner to a controlled height, without unrelated transaction,
// fee or solvency RPCs. Its healthy flag stays true while this height is frozen.
type dashHealthFetcher struct{ height atomic.Int64 }

func (f *dashHealthFetcher) GetHeight() (int64, error) { return f.height.Load(), nil }

func (*dashHealthFetcher) GetNetworkFee() (uint64, uint64) { return 1, 1 }

func (*dashHealthFetcher) FetchTxs(int64, int64) (types.TxIn, error) {
	return types.TxIn{}, nil
}

func (*dashHealthFetcher) FetchMemPool(int64) (types.TxIn, error) {
	return types.TxIn{}, nil
}

func (s *DashSuite) TestDashScannerHeightHealth(c *C) {
	const hash = "0000000000000000000000000000000000000000000000000000000000000123"
	type rpcState struct {
		block, locked int64
		failMethod    string
		unknownBlock  bool
		wrongHash     bool
	}
	var state atomic.Pointer[rpcState]
	state.Store(&rpcState{block: 100, locked: 100})
	server := newRPCTestServer(c, func(method string, _ []json.RawMessage) any {
		current := state.Load()
		if method == current.failMethod {
			return []string{"invalid RPC result"}
		}
		switch method {
		case "getbestchainlock":
			return map[string]any{"blockhash": hash, "height": current.locked, "known_block": !current.unknownBlock}
		case "getblockhash":
			if current.wrongHash {
				return strings.Repeat("b", 64)
			}
			return hash
		case "getblockcount":
			return current.block
		default:
			c.Errorf("unexpected RPC %s", method)
			return nil
		}
	})
	defer server.Close()
	client, cleanup := newDashClientForRPCServer(c, server.URL)
	defer cleanup()
	storage, err := blockscanner.NewBlockScannerStorage("", config.LevelDBOptions{})
	c.Assert(err, IsNil)
	defer storage.Close()
	fetcher := &dashHealthFetcher{}
	fetcher.height.Store(100)
	scanner, err := blockscanner.NewBlockScanner(config.BifrostBlockScannerConfiguration{
		ChainID:                    common.DASHChain,
		StartBlockHeight:           99,
		BlockHeightDiscoverBackoff: time.Millisecond,
		MaxHealthyLag:              30 * time.Minute,
	}, storage, GetMetricForTest(c, common.DASHChain), dashHealthBridge{}, fetcher)
	c.Assert(err, IsNil)
	client.blockScanner = scanner

	// Even a fresh finality snapshot must not override an unhealthy scanner.
	_, err = client.GetHeight()
	c.Assert(err, IsNil)
	c.Assert(client.IsBlockScannerHealthy(), Equals, false)
	client.chainLockHeightHealth.Store(nil)
	scanner.Start(make(chan types.TxIn), make(chan common.NetworkFee))
	defer scanner.Stop()
	waitForScan := func(height int64) {
		deadline := time.Now().Add(5 * time.Second)
		for scanner.PreviousHeight() < height && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
		}
		c.Assert(scanner.PreviousHeight(), Equals, height)
		c.Assert(scanner.IsHealthy(), Equals, true)
	}
	waitForScan(100)
	c.Assert(client.IsBlockScannerHealthy(), Equals, false) // no snapshot yet
	height, err := client.GetHeight()
	c.Assert(err, IsNil)
	c.Assert(height, Equals, int64(100))
	c.Assert(client.IsBlockScannerHealthy(), Equals, true)

	// Successful polling of a frozen ChainLock must not hide a growing gap.
	for _, block := range []int64{101, 102, 120} {
		state.Store(&rpcState{block: block, locked: 100})
		height, err = client.GetHeight()
		c.Assert(err, IsNil)
		c.Assert(height, Equals, int64(100)) // scan limit remains the ChainLock
		c.Assert(scanner.IsHealthy(), Equals, true)
		c.Assert(client.IsBlockScannerHealthy(), Equals, block <= 101)
	}

	// A recovering ChainLock alone must not re-enable signing before scanning.
	state.Store(&rpcState{block: 120, locked: 120})
	_, err = client.GetHeight()
	c.Assert(err, IsNil)
	c.Assert(client.IsBlockScannerHealthy(), Equals, false)
	fetcher.height.Store(120)
	waitForScan(120)
	c.Assert(client.IsBlockScannerHealthy(), Equals, true)

	// Every read/validation failure must preserve the old timestamp, allowing
	// health to expire even when the shared scanner's healthy flag stays true.
	for _, failure := range []rpcState{
		{block: 120, locked: 120, failMethod: "getbestchainlock"},
		{block: 120, locked: 120, failMethod: "getblockhash"},
		{block: 120, locked: 120, failMethod: "getblockcount"},
		{block: 120, locked: 120, unknownBlock: true},
		{block: 120, locked: 120, wrongHash: true},
		{block: 120, locked: -1},
	} {
		stale := &chainLockHeightHealth{120, 120, time.Now().Add(-time.Hour)}
		client.chainLockHeightHealth.Store(stale)
		state.Store(&failure)
		_, err = client.GetHeight()
		c.Assert(err, NotNil)
		c.Assert(client.chainLockHeightHealth.Load(), Equals, stale)
		c.Assert(scanner.IsHealthy(), Equals, true)
		c.Assert(client.IsBlockScannerHealthy(), Equals, false)
	}
	state.Store(&rpcState{block: 120, locked: 120})
	_, err = client.GetHeight()
	c.Assert(err, IsNil)
	c.Assert(client.IsBlockScannerHealthy(), Equals, true)

	// A daemon behind its ChainLock still supplies the scanner target, but
	// publishing that catch-up state must disable signing immediately.
	state.Store(&rpcState{block: 119, locked: 120})
	height, err = client.GetHeight()
	c.Assert(err, IsNil)
	c.Assert(height, Equals, int64(120))
	c.Assert(client.IsBlockScannerHealthy(), Equals, false)

	// With no completed reads (including a hung RPC), the snapshot expires.
	client.chainLockHeightHealth.Store(&chainLockHeightHealth{120, 120, time.Now().Add(-time.Hour)})
	c.Assert(client.IsBlockScannerHealthy(), Equals, false)

	// Other UTXO chains still use the shared scanner flag and ordinary height.
	client.chainLockHeightHealth.Store(nil)
	for _, chain := range []common.Chain{common.BTCChain, common.BCHChain, common.LTCChain, common.DOGEChain, common.ZECChain} {
		client.cfg.ChainID = chain
		state.Store(&rpcState{block: 125, locked: 120, failMethod: "getbestchainlock"})
		height, err = client.GetHeight()
		c.Assert(err, IsNil)
		c.Assert(height, Equals, int64(125))
		c.Assert(client.chainLockHeightHealth.Load(), IsNil)
		c.Assert(client.IsBlockScannerHealthy(), Equals, true)
	}
}

func (s *DashSuite) TestDashBlockRequiresMatchingChainLock(c *C) {
	hash := strings.Repeat("a", 64)
	locked := false
	returnedHash := hash
	returnedHeight := int64(123)
	includeLock := true
	server := newRPCTestServer(c, func(method string, _ []json.RawMessage) any {
		switch method {
		case "getblockhash":
			return hash
		case "getblock":
			block := map[string]any{"hash": returnedHash, "height": returnedHeight}
			if includeLock {
				block["chainlock"] = locked
			}
			return block
		}
		c.Fatalf("unexpected RPC %s", method)
		return nil
	})
	defer server.Close()
	client, cleanup := newDashClientForRPCServer(c, server.URL)
	defer cleanup()
	_, err := client.getBlock(123)
	c.Assert(err, NotNil)
	locked = true
	block, err := client.getBlock(123)
	c.Assert(err, IsNil)
	c.Assert(block.Hash, Equals, hash)
	returnedHash = strings.Repeat("b", 64)
	_, err = client.getBlock(123)
	c.Assert(err, NotNil)
	returnedHash = hash
	returnedHeight = 124
	_, err = client.getBlock(123)
	c.Assert(err, NotNil)
	returnedHeight = 123
	includeLock = false
	_, err = client.getBlock(123)
	c.Assert(err, NotNil)
}

func (s *DashSuite) TestDashObservationBatchPreservesOrderAndFirstItem(c *C) {
	client, cleanup := newDashClientForRPCServer(c, "http://localhost:1")
	defer cleanup()
	firstID, secondID := strings.Repeat("a", 64), strings.Repeat("b", 64)
	first := &types.TxInItem{Tx: strings.ToUpper(firstID), BlockHeight: 100}
	duplicate := &types.TxInItem{Tx: firstID, BlockHeight: 200}
	second := &types.TxInItem{Tx: secondID, BlockHeight: 100}
	accepted, err := client.trackObservedTxs([]*types.TxInItem{first, duplicate, second})
	c.Assert(err, IsNil)
	c.Assert(accepted, DeepEquals, []*types.TxInItem{first, second})
	accepted, err = client.trackObservedTxs([]*types.TxInItem{duplicate, second})
	c.Assert(err, IsNil)
	c.Assert(accepted, HasLen, 0)
}

// Start a real scanner so admission tests exercise the same health gate as production.
func startDashTestScanner(c *C, client *Client) func() {
	storage, err := blockscanner.NewBlockScannerStorage("", config.LevelDBOptions{})
	c.Assert(err, IsNil)
	fetcher := &dashHealthFetcher{}
	fetcher.height.Store(100)
	scanner, err := blockscanner.NewBlockScanner(config.BifrostBlockScannerConfiguration{
		ChainID: common.DASHChain, StartBlockHeight: 99,
		BlockHeightDiscoverBackoff: time.Millisecond, MaxHealthyLag: 30 * time.Minute,
	}, storage, GetMetricForTest(c, common.DASHChain), dashHealthBridge{}, fetcher)
	c.Assert(err, IsNil)
	client.blockScanner = scanner
	scanner.Start(make(chan types.TxIn, 1), make(chan common.NetworkFee, 1))
	deadline := time.Now().Add(5 * time.Second)
	for (scanner.PreviousHeight() < 100 || !scanner.IsHealthy()) && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	c.Assert(scanner.IsHealthy(), Equals, true)
	client.chainLockHeightHealth.Store(&chainLockHeightHealth{tipHeight: 100, chainLockHeight: 100, checkedAt: time.Now()})
	var once sync.Once
	return func() { once.Do(func() { scanner.Stop(); _ = storage.Close() }) }
}
