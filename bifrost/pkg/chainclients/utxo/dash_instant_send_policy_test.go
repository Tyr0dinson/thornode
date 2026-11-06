package utxo

import (
	"encoding/json"
	"errors"
	"math"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/btcsuite/btcd/btcjson"
	"github.com/prometheus/client_golang/prometheus/testutil"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/bifrost/blockscanner"
	"gitlab.com/thorchain/thornode/v3/bifrost/metrics"
	sharedutxo "gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/utxo"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/config"
	ttypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

type dashHaltBridge struct {
	thorclient.ThorchainBridge
	getHeight func() (int64, error)
	getMimir  func(string) (int64, error)
	reads     []string
}

func (b *dashHaltBridge) GetBlockHeight() (int64, error) {
	b.reads = append(b.reads, "height")
	if b.getHeight != nil {
		return b.getHeight()
	}
	return b.ThorchainBridge.GetBlockHeight()
}

func (b *dashHaltBridge) GetMimir(key string) (int64, error) {
	b.reads = append(b.reads, key)
	if b.getMimir != nil {
		return b.getMimir(key)
	}
	return b.ThorchainBridge.GetMimir(key)
}

func (s *DashSuite) TestInstantSendHaltBoundariesAndReadErrors(c *C) {
	client, cleanup := newDashClientForRPCServer(c, "http://localhost:1")
	defer cleanup()
	base := client.bridge
	keys := []string{"HaltDASHChain", "SolvencyHaltDASHChain", "HaltChainGlobal", "NodePauseChainGlobal"}
	for i, key := range keys {
		for _, value := range []int64{-1, 0, 99, 100, 101} {
			bridge := &dashHaltBridge{ThorchainBridge: base, getMimir: func(k string) (int64, error) {
				if k == key {
					return value, nil
				}
				return -1, nil
			}}
			client.bridge = bridge
			want := value > 0 && value <= 100
			if key == "NodePauseChainGlobal" {
				want = value > 0 && value >= 100
			}
			c.Assert(client.instantSend.isPaused(), Equals, want, Commentf("%s=%d", key, value))
			if want {
				c.Assert(bridge.reads, HasLen, i+2) // height and keys through the active halt
			} else {
				c.Assert(bridge.reads, HasLen, 5)
			}
			c.Assert(blockscanner.IsChainPaused(client.cfg.BlockScanner, client.log, bridge), Equals, want)
		}
	}
	allReads := append([]string{"height"}, keys...)
	for i, failed := range allReads {
		bridge := &dashHaltBridge{
			ThorchainBridge: base,
			getHeight: func() (int64, error) {
				if failed == "height" {
					return 0, errors.New("height unavailable")
				}
				return 100, nil
			},
			getMimir: func(key string) (int64, error) {
				if key == failed {
					return -1, errors.New("Mimir unavailable")
				}
				return -1, nil
			},
		}
		client.bridge = bridge
		c.Assert(client.instantSend.isPaused(), Equals, true)
		c.Assert(bridge.reads, DeepEquals, allReads[:i+1]) // no calls after the failure
	}
	client.bridge = nil
	c.Assert(client.instantSend.isPaused(), Equals, true)
}

func (s *DashSuite) TestInstantSendEarlyHaltRetainsReservationsAndResumes(c *C) {
	for _, key := range []string{"HaltDASHChain", "SolvencyHaltDASHChain", "HaltChainGlobal", "NodePauseChainGlobal"} {
		c.Logf("halt control: %s", key)
		client, f, cleanup := newDashMempoolFixture(c)
		defer cleanup()
		client.m = GetMetricForTest(c, common.DASHChain)
		counter := client.m.GetCounter(metrics.InstantSendObserved(common.DASHChain))
		before := testutil.ToFloat64(counter)
		batch, err := client.FetchMemPool(123)
		c.Assert(err, IsNil)
		c.Assert(batch.MemPool, Equals, true)
		c.Assert(batch.TxArray, HasLen, 2)
		// Already delivered deposits stay charged while new queued candidates wait.
		_, err = client.instantSend.claimBatch([]*types.TxInItem{instantSendTestItem(f, f.inbound)}, instantSendAdmission{allowed: true})
		c.Assert(err, IsNil)
		c.Assert(client.temporalStorage.CompleteInstantSendObservations([]string{f.inbound}), IsNil)
		client.instantSend.txQueue = instantSendObservationGroups(common.DASHChain,
			map[int64][]*types.TxInItem{100: {instantSendTestItem(f, f.lowFeeInbound)}})
		base := client.bridge
		phase := "halted"
		client.bridge = &dashHaltBridge{ThorchainBridge: base, getMimir: func(k string) (int64, error) {
			if k == key {
				switch phase {
				case "halted":
					return 100, nil
				case "failed refresh":
					return -1, errors.New("halt refresh failed")
				}
			}
			return base.GetMimir(k)
		}}
		var state sharedutxo.InstantSendState
		var marked bool
		for _, nextPhase := range []string{"halted", "failed refresh"} {
			phase = nextPhase
			batch, err = client.FetchMemPool(123)
			c.Assert(err, IsNil)
			c.Assert(batch.MemPool, Equals, true)
			c.Assert(batch.TxArray, HasLen, 0) // ordinary outbounds were already delivered
			c.Assert(f.instantSendCalls, Equals, 0)
			c.Assert(client.instantSend.txQueue, HasLen, 0)
			c.Assert(client.nextMempoolScan, Equals, scanOutbounds)
			state, err = client.temporalStorage.GetInstantSendState()
			c.Assert(err, IsNil)
			c.Assert(state.Reserved, Equals, uint64(common.One/10))
			marked, err = client.temporalStorage.HasObservedTx(f.lowFeeInbound)
			c.Assert(err, IsNil)
			c.Assert(marked, Equals, false)
			c.Assert(testutil.ToFloat64(client.m.GetGauge(metrics.InstantSendAvailable(common.DASHChain))), Equals, float64(0))
			c.Assert(testutil.ToFloat64(counter), Equals, before)
		}
		c.Assert(f.ordinaryCalls, Equals, 3)

		phase = "recovered"
		batch, err = client.FetchMemPool(123)
		c.Assert(err, IsNil)
		c.Assert(batch.MemPool, Equals, true)
		batch, err = client.FetchMemPool(123)
		c.Assert(err, IsNil)
		c.Assert(batch.MemPool, Equals, false)
		c.Assert(batch.TxArray, HasLen, 1)
		c.Assert(batch.TxArray[0].Tx, Equals, f.lowFeeInbound)
		// Neither the earlier reservation nor the resumed observation is emitted twice.
		_, err = client.FetchMemPool(123)
		c.Assert(err, IsNil)
		batch, err = client.FetchMemPool(123)
		c.Assert(err, IsNil)
		c.Assert(batch.TxArray, HasLen, 0)
	}
}

func (s *DashSuite) TestInstantSendEarlyLookupFailureAndRecovery(c *C) {
	for _, lookup := range []string{"height", "HaltDASHChain", "SolvencyHaltDASHChain", "HaltChainGlobal", "NodePauseChainGlobal", "missing bridge"} {
		c.Logf("failed lookup: %s", lookup)
		client, f, cleanup := newDashMempoolFixture(c)
		defer cleanup()
		base := client.bridge
		client.bridge = &dashHaltBridge{
			ThorchainBridge: base,
			getHeight: func() (int64, error) {
				if lookup == "height" {
					return 0, errors.New("height unavailable")
				}
				return base.GetBlockHeight()
			},
			getMimir: func(key string) (int64, error) {
				if lookup == key {
					return -1, errors.New("Mimir unavailable")
				}
				return base.GetMimir(key)
			},
		}
		entries := f.entries
		if lookup == "missing bridge" {
			client.bridge = nil
			// No vault lookup is needed, even if the seeded address cache expires.
			f.entries = nil
		}
		client.nextMempoolScan = scanInstantSend
		client.instantSend.txQueue = instantSendObservationGroups(common.DASHChain,
			map[int64][]*types.TxInItem{100: {instantSendTestItem(f, f.inbound)}})
		batch, err := client.FetchMemPool(123)
		c.Assert(err, IsNil)
		c.Assert(batch.MemPool, Equals, true)
		if lookup == "missing bridge" {
			c.Assert(batch.TxArray, HasLen, 0)
		} else {
			c.Assert(batch.TxArray, HasLen, 2)
			for _, item := range batch.TxArray {
				c.Assert(item.Sender, Equals, f.asgard)
			}
		}
		c.Assert(f.ordinaryCalls, Equals, 1)
		c.Assert(f.instantSendCalls, Equals, 0)
		c.Assert(client.instantSend.txQueue, HasLen, 0)
		c.Assert(client.nextMempoolScan, Equals, scanOutbounds)
		var marked bool
		for _, id := range []string{f.inbound, f.lowFeeInbound} {
			marked, err = client.temporalStorage.HasObservedTx(id)
			c.Assert(err, IsNil)
			c.Assert(marked, Equals, false)
		}
		state, err := client.temporalStorage.GetInstantSendState()
		c.Assert(err, IsNil)
		c.Assert(state.Reserved, Equals, uint64(0))

		client.bridge = base
		f.entries = entries
		_, err = client.FetchMemPool(123)
		c.Assert(err, IsNil)
		batch, err = client.FetchMemPool(123)
		c.Assert(err, IsNil)
		c.Assert(batch.MemPool, Equals, false)
		c.Assert(batch.TxArray, HasLen, 2)
	}
}

func (s *DashSuite) TestInstantSendLateHaltFallsBackWithoutClaims(c *C) {
	for _, queued := range []bool{false, true} {
		for _, failure := range []string{"halt", "height", "HaltDASHChain", "SolvencyHaltDASHChain", "HaltChainGlobal", "NodePauseChainGlobal"} {
			c.Logf("queued: %v, late failure: %s", queued, failure)
			client, f, cleanup := newDashMempoolFixture(c)
			defer cleanup()
			client.m = GetMetricForTest(c, common.DASHChain)
			counter := client.m.GetCounter(metrics.InstantSendObserved(common.DASHChain))
			before := testutil.ToFloat64(counter)
			client.nextMempoolScan = scanInstantSend
			if queued {
				client.instantSend.txQueue = instantSendObservationGroups(common.DASHChain,
					map[int64][]*types.TxInItem{100: {instantSendTestItem(f, f.inbound)}})
			}
			var paused atomic.Bool
			f.beforeRawTransaction = func(id string) {
				if id == f.inbound {
					paused.Store(true) // during discovery or the queued batch's finality RPC
				}
			}
			base := client.bridge
			client.bridge = &dashHaltBridge{
				ThorchainBridge: base,
				getHeight: func() (int64, error) {
					if paused.Load() && failure == "height" {
						return 0, errors.New("height unavailable")
					}
					return base.GetBlockHeight()
				},
				getMimir: func(key string) (int64, error) {
					if paused.Load() {
						if key == failure {
							return -1, errors.New("Mimir unavailable")
						}
						if key == "HaltDASHChain" && failure == "halt" {
							return 100, nil
						}
					}
					return base.GetMimir(key)
				},
			}
			batch, err := client.FetchMemPool(123)
			c.Assert(err, IsNil)
			c.Assert(batch.MemPool, Equals, true)
			c.Assert(batch.TxArray, HasLen, 2)
			c.Assert(client.instantSend.txQueue, HasLen, 0)
			c.Assert(client.nextMempoolScan, Equals, scanOutbounds)
			state, err := client.temporalStorage.GetInstantSendState()
			c.Assert(err, IsNil)
			c.Assert(state.Reserved, Equals, uint64(0))
			for _, id := range []string{f.inbound, f.lowFeeInbound, f.lockedOutbound} {
				var marked bool
				marked, err = client.temporalStorage.HasObservedTx(id)
				c.Assert(err, IsNil)
				c.Assert(marked, Equals, false)
			}
			c.Assert(testutil.ToFloat64(client.m.GetGauge(metrics.InstantSendAvailable(common.DASHChain))), Equals, float64(0))
			c.Assert(testutil.ToFloat64(counter), Equals, before)
			// A deferred deposit remains eligible for the normal block observation.
			c.Assert(client.instantSend.reconcileBlock(&btcjson.GetBlockVerboseTxResult{Tx: []btcjson.TxRawResult{{Txid: f.inbound}}}), IsNil)
			accepted, err := client.trackObservedTxs([]*types.TxInItem{instantSendTestItem(f, f.inbound)})
			c.Assert(err, IsNil)
			c.Assert(accepted, HasLen, 1)
		}
	}
}

func (s *DashSuite) TestInstantSendVaultHaltIncludesStorageFallback(c *C) {
	for _, corrupt := range []bool{false, true} {
		client, f, cleanup := newDashMempoolFixture(c)
		defer cleanup()
		if corrupt {
			storage, err := blockscanner.NewBlockScannerStorage(c.MkDir(), config.LevelDBOptions{})
			c.Assert(err, IsNil)
			defer storage.Close()
			client.temporalStorage, err = sharedutxo.NewTemporalStorage(storage.GetInternalDb(), 0)
			c.Assert(err, IsNil)
			c.Assert(storage.GetInternalDb().Put([]byte("instant-send-state"), []byte("invalid journal"), nil), IsNil)
		}
		checks := 0
		base := client.bridge
		client.bridge = &dashHaltBridge{ThorchainBridge: base, getHeight: func() (int64, error) {
			// Both the initial guard and the storage-fallback guard must let
			// block settlement acquire the allowance lock while the bridge waits.
			c.Assert(client.instantSend.stateMu.TryLock(), Equals, true)
			client.instantSend.stateMu.Unlock()
			checks++
			if !corrupt || checks > 1 {
				return 0, errors.New("height unavailable")
			}
			return 100, nil
		}}
		item := instantSendTestItem(f, f.lockedOutbound)
		item.Sender = f.asgard
		item.To = f.asgard // vault-to-vault still obeys emergency halts
		accepted, err := client.instantSend.claimBatch([]*types.TxInItem{item}, instantSendAdmission{})
		c.Assert(errors.Is(err, errInstantSendPaused), Equals, true)
		c.Assert(accepted, HasLen, 0)
		marked, err := client.temporalStorage.HasObservedTx(item.Tx)
		c.Assert(err, IsNil)
		c.Assert(marked, Equals, false)
		if corrupt {
			c.Assert(checks, Equals, 2) // second guard protects the vault delivery fallback
		}
		client.bridge = base
		accepted, err = client.instantSend.claimBatch([]*types.TxInItem{item}, instantSendAdmission{})
		c.Assert(err, IsNil)
		c.Assert(accepted, HasLen, 1)
		fallback, err := client.trackObservedTxs(accepted)
		c.Assert(err, IsNil)
		c.Assert(fallback, HasLen, 1, Commentf("unacknowledged vault transfers keep their block fallback"))
	}
}

// TestInstantSendHaltDoesNotStartWarmup exercises the InstantSend turn with an
// otherwise healthy scanner, then proves removing the halt starts the snapshot.
func (s *DashSuite) TestInstantSendHaltDoesNotStartWarmup(c *C) {
	for _, scenario := range []string{"halt", "height unavailable"} {
		func() {
			c.Log(scenario)
			var snapshots atomic.Int64
			client, _, cleanup := newInstantSendWarmupClient(c, func(method string, params []json.RawMessage) any {
				c.Assert(method, Equals, "getrawmempool")
				if len(params) > 0 {
					snapshots.Add(1)
					return map[string]any{}
				}
				return []string{}
			})
			defer cleanup()
			base := client.bridge
			bridge := &dashHaltBridge{ThorchainBridge: base}
			if scenario == "halt" {
				bridge.getMimir = func(key string) (int64, error) {
					if key == "HaltDASHChain" {
						return 1, nil
					}
					return base.GetMimir(key)
				}
			} else {
				bridge.getHeight = func() (int64, error) {
					return 0, errors.New("height unavailable")
				}
			}
			client.bridge = bridge
			client.nextMempoolScan = scanInstantSend
			batch, err := client.FetchMemPool(123)
			c.Assert(err, IsNil)
			c.Assert(batch.MemPool, Equals, true)
			c.Assert(snapshots.Load(), Equals, int64(0))
			state, err := client.temporalStorage.GetInstantSendState()
			c.Assert(err, IsNil)
			c.Assert(state.Started, Equals, false)

			client.bridge = base
			client.nextMempoolScan = scanInstantSend
			_, err = client.FetchMemPool(123)
			c.Assert(err, IsNil)
			state, err = client.temporalStorage.GetInstantSendState()
			c.Assert(err, IsNil)
			c.Assert(state.Started, Equals, true)
			c.Assert(snapshots.Load() > 0, Equals, true)
		}()
	}
}

func (s *DashSuite) TestInstantSendHaltStillSettlesVerifiedReservations(c *C) {
	client, f, cleanup := newDashMempoolFixture(c)
	defer cleanup()
	_, err := client.temporalStorage.AdmitInstantSendTxs([]sharedutxo.InstantSendClaim{
		{TxID: f.lockedOutbound, Amount: 60}, {TxID: f.inbound, Amount: 40},
	}, 0, 100, true, 1)
	c.Assert(err, IsNil)
	client.instantSend.recovery.Reset()
	f.chainLocked = true
	f.txs[f.lockedOutbound]["chainlock"] = true
	f.txs[f.lockedOutbound]["blockhash"] = strings.Repeat("2", 64)
	base := client.bridge
	client.bridge = &dashHaltBridge{ThorchainBridge: base, getMimir: func(key string) (int64, error) {
		if key == "HaltDASHChain" {
			return 100, nil
		}
		return base.GetMimir(key)
	}}
	batch, err := client.FetchMemPool(123)
	c.Assert(err, IsNil)
	c.Assert(batch.MemPool, Equals, true)
	c.Assert(f.instantSendCalls, Equals, 0)
	state, err := client.temporalStorage.GetInstantSendState()
	c.Assert(err, IsNil)
	c.Assert(state.Reserved, Equals, uint64(40))
	c.Assert(state.Reservations, HasLen, 1)
	c.Assert(state.Reservations[f.inbound].Amount, Equals, uint64(40))
	c.Assert(client.instantSend.recovery.Complete(), Equals, true)
}

func (s *DashSuite) TestInstantSendMimirDefaultsChangesAndFailures(c *C) {
	client, f, cleanup := newDashMempoolFixture(c)
	defer cleanup()
	// Use 1.5 DASH deposits to exercise lowering the limit below usage.
	for id, amount := range map[string]float64{
		f.inbound:               1.5,
		f.lowFeeInbound:         1.5,
		strings.Repeat("f", 64): 1.50002,
	} {
		outputs, ok := f.txs[id]["vout"].([]any)
		c.Assert(ok, Equals, true)
		output, ok := outputs[0].(map[string]any)
		c.Assert(ok, Equals, true)
		output["value"] = amount
	}
	bridge := &instantSendPolicyBridge{values: make(map[string]int64), status: ttypes.NodeStatus_Active}
	client.bridge = bridge
	c.Assert(client.instantSend.refreshSettings(), IsNil)
	c.Assert(client.instantSend.minAmount, Equals, uint64(common.One/100))
	c.Assert(client.instantSend.maxAmount, Equals, uint64(0))
	admission, err := client.instantSend.prepareAllowance(nil)
	c.Assert(err, IsNil)
	c.Assert(admission.allowed, Equals, false)
	bridge.values[DashInstantSendMinAmountMimirKey] = 0
	bridge.values[DashInstantSendMaxAmountMimirKey] = 2 * common.One
	c.Assert(client.instantSend.refreshSettings(), IsNil)
	c.Assert(client.instantSend.maxAmount, Equals, uint64(2*common.One))
	admission, err = client.instantSend.prepareAllowance(nil)
	c.Assert(err, IsNil)
	c.Assert(admission.allowed, Equals, true)
	batch, err := client.instantSend.fetchBatch(admission)
	c.Assert(err, IsNil)
	c.Assert(batch.TxArray, HasLen, 1)
	batch.TxArray[0].OnObservedTxStored()
	// A reduced limit retains the first transaction's full reservation.
	bridge.values[DashInstantSendMaxAmountMimirKey] = common.One
	c.Assert(client.instantSend.refreshSettings(), IsNil)
	state, err := client.temporalStorage.GetInstantSendState()
	c.Assert(err, IsNil)
	c.Assert(state.Reserved, Equals, uint64(3*common.One/2))
	c.Assert(state.Available(client.instantSend.maxAmount), Equals, uint64(0))
	bridge.values[DashInstantSendMaxAmountMimirKey] = 3 * common.One
	bridge.fail = DashInstantSendMinAmountMimirKey
	err = client.instantSend.refreshSettings()
	c.Assert(err, NotNil)
	admission, err = client.instantSend.prepareAllowance(err)
	c.Assert(err, IsNil)
	c.Assert(admission.allowed, Equals, false)
	c.Assert(admission.reason, Equals, "settings")
	batch, err = client.instantSend.fetchBatch(admission)
	c.Assert(err, IsNil)
	c.Assert(batch.TxArray, HasLen, 0)
	bridge.fail = ""
	bridge.values[DashInstantSendMinAmountMimirKey] = 2 * common.One
	c.Assert(client.instantSend.refreshSettings(), IsNil)
	admission, err = client.instantSend.prepareAllowance(nil)
	c.Assert(err, IsNil)
	batch, err = client.instantSend.fetchBatch(admission)
	c.Assert(err, IsNil)
	c.Assert(batch.TxArray, HasLen, 0) // minimum now exceeds the deposit
	delete(bridge.values, DashInstantSendMinAmountMimirKey)
	c.Assert(client.instantSend.refreshSettings(), IsNil)
	c.Assert(client.instantSend.minAmount, Equals, defaultInstantSendMinAmount)
	admission, err = client.instantSend.prepareAllowance(nil)
	c.Assert(err, IsNil)
	batch, err = client.instantSend.fetchBatch(admission)
	c.Assert(err, IsNil)
	c.Assert(batch.TxArray, HasLen, 1)
	c.Assert(batch.TxArray[0].Tx, Equals, f.lowFeeInbound)
	delete(bridge.values, DashInstantSendMaxAmountMimirKey)
	c.Assert(client.instantSend.refreshSettings(), IsNil)
	c.Assert(client.instantSend.maxAmount, Equals, uint64(0))
}

func (s *DashSuite) TestInstantSendEnabledMimirDefaultsAndFailures(c *C) {
	for _, defaultEnabled := range []bool{false, true} {
		client, cleanup := newDashClientForRPCServer(c, "http://localhost:1")
		defer cleanup()
		client.instantSend.enabled = defaultEnabled
		client.cfg.InstantSend.Enabled = defaultEnabled
		bridge := &instantSendPolicyBridge{values: make(map[string]int64)}
		client.bridge = bridge
		for _, tc := range []struct {
			value int64
			fail  bool
			want  bool
		}{
			{-1, true, false},
			{-1, false, defaultEnabled},
			{1, false, true},
			{-1, false, defaultEnabled},
			{0, false, false},
			{-1, false, defaultEnabled},
			{1, false, true},
			{1, true, false},
			{-1, false, defaultEnabled},
			{1, false, true},
			{-2, false, defaultEnabled},
		} {
			bridge.values[DashInstantSendEnabledMimirKey] = tc.value
			if tc.value == -1 {
				delete(bridge.values, DashInstantSendEnabledMimirKey)
			}
			bridge.fail = ""
			if tc.fail {
				bridge.fail = DashInstantSendEnabledMimirKey
			}
			bridge.reads = nil
			err := client.instantSend.refreshSettings()
			c.Check(err != nil, Equals, tc.fail)
			c.Check(client.instantSend.enabled, Equals, tc.want,
				Commentf("default: %t, value: %d, failed read: %t", defaultEnabled, tc.value, tc.fail))
			c.Check(client.GetConfig().InstantSend.Enabled, Equals, defaultEnabled)
			if tc.fail {
				c.Check(bridge.reads, DeepEquals, []string{DashInstantSendEnabledMimirKey})
			}
		}
		// Losing the bridge must also disable a previously enabled switch.
		bridge.values[DashInstantSendEnabledMimirKey] = 1
		c.Assert(client.instantSend.refreshSettings(), IsNil)
		client.bridge = nil
		c.Check(client.instantSend.refreshSettings(), NotNil)
		c.Check(client.instantSend.enabled, Equals, false)
		c.Check(client.GetConfig().InstantSend.Enabled, Equals, defaultEnabled)
	}
}

func (s *DashSuite) TestInstantSendConfigConcurrentReads(c *C) {
	client, cleanup := newDashClientForRPCServer(c, "http://localhost:1")
	defer cleanup()
	bridge := &instantSendPolicyBridge{values: make(map[string]int64)}
	client.bridge = bridge
	initialConfig := client.GetConfig()
	started, stop := make(chan struct{}), make(chan struct{})
	var readers sync.WaitGroup
	readers.Add(1)
	go func() {
		defer readers.Done()
		close(started)
		for {
			select {
			case <-stop:
				return
			default:
				_ = client.GetConfig()
			}
		}
	}()
	<-started
	defer func() { close(stop); readers.Wait() }()
	for i := range 100 {
		value := int64(i%4) - 1
		bridge.values[DashInstantSendEnabledMimirKey] = value
		bridge.fail = ""
		if value == 2 {
			bridge.fail = DashInstantSendEnabledMimirKey
		}
		err := client.instantSend.refreshSettings()
		c.Assert(err != nil, Equals, value == 2)
		cfg := client.GetConfig()
		c.Assert(cfg, DeepEquals, initialConfig)
		c.Assert(client.instantSend.enabled, Equals, value == -1 || value == 1)
	}
}

func (s *DashSuite) TestInstantSendEnabledReadFailureKeepsOutboundsAndSettlement(c *C) {
	client, f, cleanup := newDashMempoolFixture(c)
	defer cleanup()
	client.instantSend.maxAmount = common.One / 10
	batch, err := client.instantSend.fetchBatch(instantSendAdmission{allowed: true})
	c.Assert(err, IsNil)
	c.Assert(batch.TxArray, HasLen, 1)
	c.Assert(batch.TxArray[0].Tx, Equals, f.inbound)
	batch.TxArray[0].OnObservedTxStored()
	bridge := &instantSendPolicyBridge{
		values: map[string]int64{DashInstantSendEnabledMimirKey: 1, DashInstantSendMaxAmountMimirKey: common.One},
		status: ttypes.NodeStatus_Active, fail: DashInstantSendEnabledMimirKey,
	}
	client.bridge = bridge
	client.nextMempoolScan = scanInstantSend
	vaultItem := instantSendTestItem(f, f.lockedOutbound)
	vaultItem.Sender = f.asgard
	client.instantSend.txQueue = instantSendObservationGroups(common.DASHChain,
		map[int64][]*types.TxInItem{100: {instantSendTestItem(f, f.lowFeeInbound), vaultItem}})
	instantSendCalls := f.instantSendCalls

	batch, err = client.FetchMemPool(123)
	c.Assert(err, IsNil)
	c.Check(batch.MemPool, Equals, true)
	c.Check(batch.TxArray, HasLen, 2)
	c.Check(client.instantSend.txQueue, HasLen, 0)
	c.Check(client.nextMempoolScan, Equals, scanOutbounds)
	c.Check(f.instantSendCalls, Equals, instantSendCalls)
	c.Check(bridge.reads, DeepEquals, []string{DashInstantSendEnabledMimirKey})
	state, err := client.temporalStorage.GetInstantSendState()
	c.Assert(err, IsNil)
	c.Check(state.Reserved, Equals, uint64(common.One/10))
	for _, id := range []string{f.lowFeeInbound, f.lockedOutbound} {
		var observed bool
		observed, err = client.temporalStorage.HasObservedTx(id)
		c.Assert(err, IsNil)
		c.Check(observed, Equals, false)
	}

	// Settlement releases the existing reservation while the switch is disabled.
	c.Assert(client.instantSend.reconcileBlock(&btcjson.GetBlockVerboseTxResult{
		Height: 200, Tx: []btcjson.TxRawResult{{Txid: f.inbound}},
	}), IsNil)
	state, err = client.temporalStorage.GetInstantSendState()
	c.Assert(err, IsNil)
	c.Check(state.Reserved, Equals, uint64(0))
	bridge.fail = ""
	_, err = client.FetchMemPool(123)
	c.Assert(err, IsNil)
	batch, err = client.FetchMemPool(123)
	c.Assert(err, IsNil)
	c.Check(batch.MemPool, Equals, false)
	c.Assert(batch.TxArray, HasLen, 1)
	c.Check(batch.TxArray[0].Tx, Equals, f.lowFeeInbound)
}

func (s *DashSuite) TestInstantSendAmountMimirsUseDuffs(c *C) {
	client, cleanup := newDashClientForRPCServer(c, "http://localhost:1")
	defer cleanup()
	bridge := &instantSendPolicyBridge{values: make(map[string]int64)}
	client.bridge = bridge
	for _, tc := range []struct {
		value   int64
		minimum uint64
		maximum uint64
	}{
		{-1, common.One / 100, 0},
		{0, 0, 0},
		{1, 1, 1},
		{common.One / 100, common.One / 100, common.One / 100},
		{common.One, common.One, common.One},
		{1000 * common.One, 1000 * common.One, 1000 * common.One},
		{math.MaxInt64, math.MaxInt64, math.MaxInt64},
	} {
		bridge.values[DashInstantSendMinAmountMimirKey] = tc.value
		bridge.values[DashInstantSendMaxAmountMimirKey] = tc.value
		c.Assert(client.instantSend.refreshSettings(), IsNil)
		c.Assert(client.instantSend.minAmount, Equals, tc.minimum, Commentf("Mimir value %d", tc.value))
		c.Assert(client.instantSend.maxAmount, Equals, tc.maximum, Commentf("Mimir value %d", tc.value))
	}
	bridge.values[DashInstantSendMaxAmountMimirKey] = 1000 * common.One
	c.Assert(client.instantSend.refreshSettings(), IsNil)
	bridge.fail = DashInstantSendMaxAmountMimirKey
	c.Assert(client.instantSend.refreshSettings(), NotNil)
	bridge.fail = ""
	delete(bridge.values, DashInstantSendMaxAmountMimirKey)
	c.Assert(client.instantSend.refreshSettings(), IsNil)
	c.Assert(client.instantSend.maxAmount, Equals, uint64(0))
}

func (s *DashSuite) TestInstantSendFetchMemPoolPausesOnAmountReadFailure(c *C) {
	for _, key := range []string{DashInstantSendMinAmountMimirKey, DashInstantSendMaxAmountMimirKey} {
		client, f, cleanup := newDashMempoolFixture(c)
		defer cleanup()
		client.cfg.UTXO.TransactionBatchSize = 1
		client.cfg.UTXO.MaxMempoolBatches = 10
		bridge := &instantSendPolicyBridge{
			values: map[string]int64{DashInstantSendEnabledMimirKey: 1, DashInstantSendMaxAmountMimirKey: common.One},
			status: ttypes.NodeStatus_Active,
		}
		client.bridge = bridge
		client.nextMempoolScan = scanInstantSend
		batch, err := client.FetchMemPool(123)
		c.Assert(err, IsNil)
		c.Assert(batch.TxArray, HasLen, 1)
		c.Assert(batch.TxArray[0].Tx, Equals, f.inbound)
		batch.TxArray[0].OnObservedTxStored()

		// The next scan must not reuse the previous scan's successful admission.
		bridge.fail = key
		client.nextMempoolScan = scanInstantSend
		batch, err = client.FetchMemPool(123)
		c.Assert(err, IsNil)
		c.Assert(batch.TxArray, HasLen, 0)
		state, err := client.temporalStorage.GetInstantSendState()
		c.Assert(err, IsNil)
		c.Assert(state.Reserved, Equals, uint64(common.One/10))
		observed, err := client.temporalStorage.HasObservedTx(f.lowFeeInbound)
		c.Assert(err, IsNil)
		c.Assert(observed, Equals, false)

		bridge.fail = ""
		client.nextMempoolScan = scanInstantSend
		batch, err = client.FetchMemPool(123)
		c.Assert(err, IsNil)
		c.Assert(batch.TxArray, HasLen, 1)
		c.Assert(batch.TxArray[0].Tx, Equals, f.lowFeeInbound)
	}
}
