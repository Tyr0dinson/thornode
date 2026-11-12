package utxo

import (
	"errors"
	"sync"

	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/bifrost/blockscanner"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
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
	keys := []string{"HaltDASHChain", "SolvencyHaltDASHChain", "HaltChainGlobal", "HaltDASHTrading", "NodePauseChainGlobal"}
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
				c.Assert(bridge.reads, HasLen, len(keys)+1)
			}
			// A trading halt stops early credit but leaves ChainLock scanning active.
			c.Assert(blockscanner.IsChainPaused(client.cfg.BlockScanner, client.log, bridge), Equals, want && key != "HaltDASHTrading")
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

func (s *DashSuite) TestInstantSendEnabledMimirDefaultsAndFailures(c *C) {
	for _, previous := range []bool{false, true} {
		client, cleanup := newDashClientForRPCServer(c, "http://localhost:1")
		defer cleanup()
		client.instantSend.enabled = previous
		bridge := &instantSendPolicyBridge{values: make(map[string]int64)}
		client.bridge = bridge
		for _, tc := range []struct {
			value int64
			fail  bool
			want  bool
		}{
			{-1, true, false},
			{-1, false, false},
			{1, false, true},
			{-1, false, false},
			{0, false, false},
			{-1, false, false},
			{1, false, true},
			{1, true, false},
			{-1, false, false},
			{1, false, true},
			{-2, false, false},
		} {
			bridge.values["DASHInstantSendEnabled"] = tc.value
			if tc.value == -1 {
				delete(bridge.values, "DASHInstantSendEnabled")
			}
			bridge.fail = ""
			if tc.fail {
				bridge.fail = "DASHInstantSendEnabled"
			}
			bridge.reads = nil
			err := client.instantSend.refreshSettings()
			c.Check(err != nil, Equals, tc.fail)
			c.Check(client.instantSend.enabled, Equals, tc.want,
				Commentf("previous: %t, value: %d, failed read: %t", previous, tc.value, tc.fail))
			if tc.fail {
				c.Check(bridge.reads, DeepEquals, []string{"DASHInstantSendEnabled"})
			}
		}
		// Losing the bridge must also disable a previously enabled switch.
		bridge.values["DASHInstantSendEnabled"] = 1
		c.Assert(client.instantSend.refreshSettings(), IsNil)
		client.bridge = nil
		c.Check(client.instantSend.refreshSettings(), NotNil)
		c.Check(client.instantSend.enabled, Equals, false)
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
		bridge.values["DASHInstantSendEnabled"] = value
		bridge.fail = ""
		if value == 2 {
			bridge.fail = "DASHInstantSendEnabled"
		}
		err := client.instantSend.refreshSettings()
		c.Assert(err != nil, Equals, value == 2)
		cfg := client.GetConfig()
		c.Assert(cfg, DeepEquals, initialConfig)
		c.Assert(client.instantSend.enabled, Equals, value == 1)
	}
}

func (s *DashSuite) TestInstantSendFeeRateOverride(c *C) {
	client, cleanup := newDashClientForRPCServer(c, "http://localhost:1")
	defer cleanup()
	bridge := &instantSendPolicyBridge{values: map[string]int64{"DASHInstantSendEnabled": 1}}
	client.bridge = bridge
	for _, tc := range []struct {
		configured uint64
		override   int64
		want       uint64
	}{
		{0, -1, 8}, {0, 0, 8}, {2, -1, 2}, {2, 0, 2}, {2, 8, 8}, {2, 1, 1},
	} {
		client.cfg.InstantSend.MinFeeRate = tc.configured
		bridge.values["DASHInstantSendMinFeeRate"] = tc.override
		err := client.instantSend.refreshSettings()
		c.Assert(err, IsNil)
		c.Assert(client.instantSend.minFeeRate, Equals, tc.want)
	}
	bridge.fail = "DASHInstantSendMinFeeRate"
	err := client.instantSend.refreshSettings()
	c.Assert(err, NotNil)
	c.Assert(client.instantSend.enabled, Equals, false)
}
