package utxo

import (
	"bytes"
	"strings"

	"github.com/btcsuite/btcd/btcjson"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/rs/zerolog"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/bifrost/metrics"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
)

func (s *DashSuite) TestInstantSendClaimsOnlySwapMemosAndEligibleVaults(c *C) {
	for _, mode := range []string{"swap", "alias", "name", "noop", "empty", "retiring", "changed"} {
		client, f, cleanup := newDashMempoolFixture(c)
		bridge := &dashMempoolPolicyBridge{enabled: 1}
		state, err := bridge.GetInstantSend(common.DASHChain)
		c.Assert(err, IsNil)
		bridge.accounting = state
		client.bridge = bridge
		item := instantSendTestItem(f, f.inbound)
		switch mode {
		case "alias":
			item.Memo = "s:THOR.RUNE"
		case "name":
			item.Memo = "=:ETH.ETH:alice"
		case "noop":
			item.Memo = "NOOP"
		case "empty":
			item.Memo = ""
		case "retiring", "changed":
			state.Vaults[0].Eligible = false
		}
		items, err := client.instantSend.claimBatch([]*types.TxInItem{item})
		c.Assert(err, IsNil)
		want := mode == "swap" || mode == "alias" || mode == "name"
		c.Assert(len(items) > 0, Equals, want, Commentf(mode))
		marked, err := client.temporalStorage.HasObservedTx(item.Tx)
		c.Assert(err, IsNil)
		c.Assert(marked, Equals, want)
		cleanup()
	}
}

func (s *DashSuite) TestInstantSendExtractionUsesSharedAnchor(c *C) {
	client, f, cleanup := newDashMempoolFixture(c)
	defer cleanup()
	tx, err := client.rpc.GetRawTransactionVerbose(f.inbound)
	c.Assert(err, IsNil)
	state, err := client.getInstantSendAccounting()
	c.Assert(err, IsNil)
	for _, height := range []int64{96, 100, 107} {
		state.ObservationHeight = height
		item := client.instantSend.buildTxInItem(tx, state)
		c.Assert(item, NotNil)
		c.Assert(item.BlockHeight, Equals, int64(96))
	}
	state.ObservationHeight = 108
	item := client.instantSend.buildTxInItem(tx, state)
	c.Assert(item.BlockHeight, Equals, int64(108))
	state.Vaults[0].Eligible = false
	c.Assert(client.instantSend.buildTxInItem(tx, state), IsNil)
	state.Vaults[0].Eligible = true
	state.ObservationHeight = 0
	c.Assert(client.instantSend.buildTxInItem(tx, state), IsNil)
}

func (s *DashSuite) TestEarlyObservationDoesNotWriteAnchorBlockMeta(c *C) {
	client, f, cleanup := newDashMempoolFixture(c)
	defer cleanup()
	item := instantSendTestItem(f, f.inbound)
	client.OnObservedTxIn(*item, item.BlockHeight)
	meta, err := client.temporalStorage.GetBlockMeta(item.BlockHeight)
	c.Assert(err, IsNil)
	c.Assert(meta, IsNil)
	item.InstantSend = nil
	item.BlockHeight = 101
	client.OnObservedTxIn(*item, item.BlockHeight)
	meta, err = client.temporalStorage.GetBlockMeta(101)
	c.Assert(err, IsNil)
	c.Assert(meta, NotNil)
	c.Assert(meta.CustomerTransactions, DeepEquals, []string{item.Tx})
}

func (s *DashSuite) TestDashInputLimitWarnsWithoutStoppingBlock(c *C) {
	client, f, cleanup := newDashMempoolFixture(c)
	defer cleanup()
	client.m = GetMetricForTest(c, common.DASHChain)
	var output bytes.Buffer
	client.log = zerolog.New(&output)
	tx, err := client.rpc.GetRawTransactionVerbose(f.inbound)
	c.Assert(err, IsNil)
	large := tx.TxRawResult
	large.Txid = strings.Repeat("A", 64)
	large.Vin = make([]btcjson.Vin, common.InstantSendMaxInputs+1)
	for i := range large.Vin {
		large.Vin[i] = tx.Vin[0]
	}
	block := &btcjson.GetBlockVerboseTxResult{Height: 101, Tx: []btcjson.TxRawResult{large, tx.TxRawResult}}
	items, err := client.extractTxs(block)
	c.Assert(err, IsNil)
	c.Assert(items.TxArray, HasLen, 1)
	c.Assert(items.TxArray[0].Tx, Equals, f.inbound)
	c.Assert(strings.Contains(output.String(), "input limit exceeded"), Equals, true)
	c.Assert(strings.Contains(output.String(), large.Txid), Equals, true)
	counter := client.m.GetCounterVec(metrics.ObservationSkipped(common.DASHChain)).WithLabelValues("input_limit")
	c.Assert(testutil.ToFloat64(counter), Equals, float64(1))
}
