package utxo

import (
	"encoding/json"
	"errors"
	"sync/atomic"

	"github.com/btcsuite/btcd/btcjson"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
)

// A failed accounting query retains queued observations for a later retry.
func (s *DashSuite) TestInstantSendQueuedAccountingRetry(c *C) {
	client, f, cleanup := newDashMempoolFixture(c)
	defer cleanup()
	item := instantSendTestItem(f, f.inbound)
	client.instantSend.txQueue = [][]*types.TxInItem{{item}}
	client.bridge = &dashMempoolPolicyBridge{accountingErr: errors.New("query unavailable")}
	_, err := client.instantSend.fetchBatch()
	c.Assert(err, NotNil)
	c.Assert(client.instantSend.txQueue[0][0], Equals, item)
	assertInstantSendScanClaims(c, client, []string{f.inbound}, nil)

	client.bridge = &dashMempoolPolicyBridge{enabled: 1}
	batch, err := client.instantSend.fetchBatch()
	c.Assert(err, IsNil)
	c.Assert(batch.TxArray, HasLen, 1)
	c.Assert(batch.TxArray[0], Equals, item)
	c.Assert(client.instantSend.txQueue, HasLen, 0)
	assertInstantSendScanClaims(c, client, []string{f.inbound}, []string{f.inbound})
}

// A failed lookup or a missing lock defers only that candidate, without local claims.
func (s *DashSuite) TestInstantSendRetriesRawTransactionFailures(c *C) {
	client, f, ids, cleanup := newInstantSendScanFixture(c, 3)
	defer cleanup()
	var recovered atomic.Bool
	f.overrideRPC = func(method string, params []json.RawMessage) (any, bool) {
		if method != "getrawtransaction" || recovered.Load() {
			return nil, false
		}
		var id string
		c.Assert(json.Unmarshal(params[0], &id), IsNil)
		switch id {
		case ids[0]:
			return &btcjson.RPCError{Code: btcjson.ErrRPCInternal.Code, Message: "unavailable"}, true
		case ids[1]:
			return dashUnlockedRawTx(id), true
		}
		return nil, false
	}
	batch, err := client.instantSend.fetchBatch()
	c.Assert(err, IsNil)
	c.Assert(batch.TxArray, HasLen, 1)
	c.Assert(batch.TxArray[0].Tx, Equals, ids[2])
	assertInstantSendScanClaims(c, client, ids, ids[2:])
	recovered.Store(true)
	batch, err = client.instantSend.fetchBatch()
	c.Assert(err, IsNil)
	c.Assert(batch.TxArray, HasLen, 2)
	assertInstantSendScanClaims(c, client, ids, ids)
}

func (s *DashSuite) TestDashNetworkFeeAdvancesAnchorOnEmptyBlocks(c *C) {
	client, f, cleanup := newDashMempoolFixture(c)
	defer cleanup()
	client.cfg.UTXO.EstimatedAverageTxSize = 2000
	client.cfg.UTXO.MinSatsPerVByte = 2
	client.m = GetMetricForTest(c, common.DASHChain)
	queue := make(chan common.NetworkFee, 2)
	client.globalNetworkFeeQueue = queue
	f.overrideRPC = func(method string, _ []json.RawMessage) (any, bool) {
		if method == "getblockstats" {
			return map[string]any{"avgfeerate": 0}, true
		}
		return nil, false
	}
	c.Assert(client.sendNetworkFee(100), IsNil)
	c.Assert(client.sendNetworkFee(101), IsNil)
	c.Assert(queue, HasLen, 2)
	for _, height := range []int64{100, 101} {
		fee := <-queue
		c.Assert(fee.Height, Equals, height)
		c.Assert(fee.TransactionRate, Equals, uint64(2))
	}
}
