package utxo

import (
	"errors"

	"gitlab.com/thorchain/thornode/v3/bifrost/blockscanner"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	. "gopkg.in/check.v1"
)

func (s *DashSuite) TestDashInstantSendPauseBoundaries(c *C) {
	client := newDashClientForUnitTest()
	client.cfg.BlockScanner.ChainID = client.cfg.ChainID
	for _, key := range []string{"HaltDASHChain", "SolvencyHaltDASHChain", "HaltChainGlobal", "NodePauseChainGlobal"} {
		for _, value := range []int64{-1, 0, 99, 100, 101} {
			bridge := &dashMempoolPolicyBridge{height: 100, mimirs: map[string]int64{key: value}}
			client.bridge = bridge
			want := value > 0 && value <= bridge.height
			if key == "NodePauseChainGlobal" {
				want = value > 0 && value >= bridge.height
			}
			paused := client.isInstantSendPaused()
			c.Assert(paused, Equals, want, Commentf("%s=%d", key, value))
			// Successful reads must agree with the unchanged central scanner.
			c.Assert(paused, Equals, blockscanner.IsChainPaused(client.cfg.BlockScanner, client.log, bridge))
		}
	}
}

func (s *DashSuite) TestDashInstantSendPauseReturnsOnFirstError(c *C) {
	keys := []string{"HaltDASHChain", "SolvencyHaltDASHChain", "HaltChainGlobal", "NodePauseChainGlobal"}
	for i := -1; i < len(keys); i++ {
		client := newDashClientForUnitTest()
		bridge := &dashMempoolPolicyBridge{height: 100}
		client.bridge = bridge
		failed := errors.New("pause lookup failed")
		if i < 0 {
			bridge.heightErr = failed
		} else {
			bridge.mimirErrs = map[string]error{keys[i]: failed}
		}
		c.Assert(client.isInstantSendPaused(), Equals, true)
		c.Assert(bridge.heightReads, Equals, 1)
		c.Assert(bridge.mimirReads, HasLen, i+1)
		for j, key := range bridge.mimirReads {
			c.Assert(key, Equals, keys[j])
		}
	}
	client := newDashClientForUnitTest()
	c.Assert(client.isInstantSendPaused(), Equals, true) // missing bridge
}

// assertDashInboundsUnclaimed checks that a blocked delivery remains available
// to a later InstantSend scan or the ChainLocked block scanner.
func assertDashInboundsUnclaimed(c *C, client *Client, f *dashMempoolFixture) {
	for _, id := range []string{f.inbound, f.lowFeeInbound} {
		observed, err := client.temporalStorage.HasObservedTx(id)
		c.Assert(err, IsNil)
		c.Assert(observed, Equals, false, Commentf("txid: %s", id))
	}
	c.Assert(client.instantSendQueuedBatches, HasLen, 0)
	c.Assert(client.instantSendScanNext, Equals, false)
}

func (s *DashSuite) TestDashInstantSendHaltAndRecovery(c *C) {
	for _, key := range []string{"HaltDASHChain", "SolvencyHaltDASHChain", "HaltChainGlobal", "NodePauseChainGlobal"} {
		func() {
			c.Logf("halt control: %s", key)
			client, f, cleanup := newDashMempoolFixture(c)
			defer cleanup()
			bridge := client.bridge.(*dashMempoolPolicyBridge)
			bridge.mimirs = map[string]int64{}

			batch, err := client.FetchMemPool(123)
			c.Assert(err, IsNil)
			c.Assert(batch.MemPool, Equals, true)
			c.Assert(batch.TxArray, HasLen, 2)
			c.Assert(client.instantSendScanNext, Equals, true)

			// A halt must discard candidates even when they were queued while
			// the chain was running, before claiming their persistent markers.
			client.instantSendQueuedBatches = []types.TxIn{{TxArray: []*types.TxInItem{{Tx: f.inbound}}}}
			bridge.mimirs[key] = bridge.height
			batch, err = client.FetchMemPool(123)
			c.Assert(err, IsNil)
			c.Assert(batch.MemPool, Equals, true)
			c.Assert(f.ordinaryCalls, Equals, 2)
			c.Assert(f.instantSendCalls, Equals, 0)
			assertDashInboundsUnclaimed(c, client, f)

			// Removing a halt must not permit admission while its refresh is
			// failing, even though an earlier check succeeded before the halt.
			bridge.mimirs[key] = 0
			bridge.mimirErrs = map[string]error{key: errors.New("halt refresh failed")}
			batch, err = client.FetchMemPool(123)
			c.Assert(err, IsNil)
			c.Assert(batch.MemPool, Equals, true)
			c.Assert(f.instantSendCalls, Equals, 0)
			assertDashInboundsUnclaimed(c, client, f)

			bridge.mimirErrs = nil
			batch, err = client.FetchMemPool(123)
			c.Assert(err, IsNil)
			c.Assert(batch.MemPool, Equals, true) // resume with ordinary outbounds
			batch, err = client.FetchMemPool(123)
			c.Assert(err, IsNil)
			c.Assert(batch.MemPool, Equals, false)
			c.Assert(batch.TxArray, HasLen, 2)
			c.Assert(batch.TxArray[0].Tx, Equals, f.inbound)
			c.Assert(batch.TxArray[1].Tx, Equals, f.lowFeeInbound)
		}()
	}
}

func (s *DashSuite) TestDashInstantSendPauseLookupFailure(c *C) {
	for _, lookup := range []string{"height", "HaltDASHChain", "SolvencyHaltDASHChain", "HaltChainGlobal", "NodePauseChainGlobal", "missing bridge"} {
		func() {
			c.Logf("failed lookup: %s", lookup)
			client, f, cleanup := newDashMempoolFixture(c)
			defer cleanup()
			bridge := client.bridge.(*dashMempoolPolicyBridge)
			failed := errors.New("pause lookup failed")
			switch lookup {
			case "height":
				bridge.heightErr = failed
			case "missing bridge":
				client.bridge = nil
			default:
				bridge.mimirErrs = map[string]error{lookup: failed}
			}
			client.instantSendScanNext = true
			client.instantSendQueuedBatches = []types.TxIn{{TxArray: []*types.TxInItem{{Tx: f.inbound}}}}

			// No previous successful halt check is required to monitor outbounds.
			batch, err := client.FetchMemPool(123)
			c.Assert(err, IsNil)
			c.Assert(batch.MemPool, Equals, true)
			c.Assert(batch.TxArray, HasLen, 2)
			for _, item := range batch.TxArray {
				c.Assert(item.Sender, Equals, f.asgard)
				c.Assert(item.To, Equals, f.user)
			}
			c.Assert(f.instantSendCalls, Equals, 0)
			assertDashInboundsUnclaimed(c, client, f)

			bridge.heightErr = nil
			bridge.mimirErrs = nil
			client.bridge = bridge
			_, err = client.FetchMemPool(123)
			c.Assert(err, IsNil)
			batch, err = client.FetchMemPool(123)
			c.Assert(err, IsNil)
			c.Assert(batch.MemPool, Equals, false)
			c.Assert(batch.TxArray, HasLen, 2)
		}()
	}
}

func (s *DashSuite) TestDashInstantSendPauseDuringScan(c *C) {
	for _, lookupFails := range []bool{false, true} {
		func() {
			c.Logf("lookup fails after scanning: %v", lookupFails)
			client, f, cleanup := newDashMempoolFixture(c)
			defer cleanup()
			bridge := client.bridge.(*dashMempoolPolicyBridge)
			// Model a scan that outlasts the bridge cache: the admission check
			// sees a different result than the check at the start of the scan.
			bridge.onHeightRead = func(reads int) {
				if reads != 2 {
					return
				}
				if lookupFails {
					bridge.heightErr = errors.New("height refresh failed")
					return
				}
				bridge.mimirs = map[string]int64{"HaltDASHChain": bridge.height}
			}
			client.instantSendScanNext = true
			batch, err := client.FetchMemPool(123)
			c.Assert(err, IsNil)
			c.Assert(f.instantSendCalls, Equals, 1)
			c.Assert(bridge.heightReads, Equals, 2)
			c.Assert(batch.MemPool, Equals, true)
			c.Assert(batch.TxArray, HasLen, 2)
			for _, item := range batch.TxArray {
				c.Assert(item.Sender, Equals, f.asgard)
			}
			assertDashInboundsUnclaimed(c, client, f)

			// The block scanner can still claim a deferred inbound.
			accepted, err := client.trackObservedTxs([]*types.TxInItem{{Tx: f.inbound}})
			c.Assert(err, IsNil)
			c.Assert(accepted, HasLen, 1)
		}()
	}
}
