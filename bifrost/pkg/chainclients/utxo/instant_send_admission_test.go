package utxo

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	btcjson "github.com/btcsuite/btcd/btcjson"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	ttypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

type instantSendPolicyBridge struct {
	dashMempoolPolicyBridge
	values map[string]int64
	fail   string
	status ttypes.NodeStatus
	reads  []string
}

func (b *instantSendPolicyBridge) GetMimir(key string) (int64, error) {
	b.reads = append(b.reads, key)
	if key == b.fail {
		return -1, errors.New("Mimir unavailable")
	}
	if value, ok := b.values[key]; ok {
		return value, nil
	}
	return -1, nil
}

func (b *instantSendPolicyBridge) FetchNodeStatus() (ttypes.NodeStatus, error) { return b.status, nil }

func instantSendTestItem(f *dashMempoolFixture, id string) *types.TxInItem {
	return &types.TxInItem{
		SpentOutputRefs: []*common.OutputRef{{TxHash: strings.Repeat("F", 64)}},
		Tx:              id, Sender: f.user, To: f.asgard, BlockHeight: 96,
		InstantSend: &common.InstantSend{}, Memo: "=:THOR.RUNE",
		Coins: common.NewCoins(common.NewCoin(common.DASHAsset, cosmos.NewUint(common.One/10))),
	}
}

func (s *DashSuite) TestInstantSendAdmissionUsesNetworkExposure(c *C) {
	client, f, cleanup := newDashMempoolFixture(c)
	defer cleanup()
	bridge := &dashMempoolPolicyBridge{enabled: 1}
	state, err := bridge.GetInstantSend(common.DASHChain)
	c.Assert(err, IsNil)
	state.MaxAmount = common.One / 10
	bridge.accounting = state
	client.bridge = bridge
	// Both locally valid candidates cannot fit the network budget together.
	items, err := client.instantSend.claimBatch([]*types.TxInItem{instantSendTestItem(f, f.inbound), instantSendTestItem(f, f.lowFeeInbound)})
	c.Assert(err, IsNil)
	c.Assert(items, HasLen, 1)
	state.Vaults[0].State.Pending = []common.PendingInbound{{Id: common.TxID(strings.ToUpper(f.inbound)), Amount: common.One / 10, Height: 1}}
	items, err = client.instantSend.claimBatch([]*types.TxInItem{instantSendTestItem(f, f.lowFeeInbound)})
	c.Assert(err, IsNil)
	c.Assert(items, HasLen, 0)
	marked, err := client.temporalStorage.HasTxMarker(f.lowFeeInbound)
	c.Assert(err, IsNil)
	c.Assert(marked, Equals, false)
}

func (s *DashSuite) TestInstantSendFixedCapacityDefersWithoutMarker(c *C) {
	client, f, cleanup := newDashMempoolFixture(c)
	defer cleanup()
	bridge := &dashMempoolPolicyBridge{enabled: 1}
	state, err := bridge.GetInstantSend(common.DASHChain)
	c.Assert(err, IsNil)
	for i := 1; i <= common.InstantSendMaxPending; i++ {
		id := fmt.Sprintf("%064X", i)
		state.Vaults[0].State.Pending = append(state.Vaults[0].State.Pending, common.PendingInbound{Id: common.TxID(id), Amount: common.One / 10, Height: 1})
		lower := strings.ToLower(id)
		f.txs[lower] = map[string]any{"txid": lower, "instantlock": true}
	}
	bridge.accounting = state
	client.bridge = bridge
	item := instantSendTestItem(f, f.inbound)
	items, err := client.instantSend.claimBatch([]*types.TxInItem{item})
	c.Assert(err, IsNil)
	c.Assert(items, HasLen, 0)
	marked, err := client.temporalStorage.HasTxMarker(item.Tx)
	c.Assert(err, IsNil)
	c.Assert(marked, Equals, false)
	// A fresh core snapshot reflects settlement freeing one slot.
	state.Vaults[0].State.Pending = state.Vaults[0].State.Pending[:common.InstantSendMaxPending-1]
	items, err = client.instantSend.claimBatch([]*types.TxInItem{item})
	c.Assert(err, IsNil)
	c.Assert(items, HasLen, 1)
	marked, err = client.temporalStorage.HasTxMarker(item.Tx)
	c.Assert(err, IsNil)
	c.Assert(marked, Equals, true)
}

func (s *DashSuite) TestInstantSendAdmissionAnchorWindow(c *C) {
	for _, tc := range []struct {
		height, floor int64
		accepted      int
	}{
		{95, 0, 0},
		{96, 0, 1},
		{96, 97, 0}, // Persisted floor remains binding if the queried height falls.
		{125, 0, 0},
	} {
		client, f, cleanup := newDashMempoolFixture(c)
		bridge := &dashMempoolPolicyBridge{enabled: 1}
		state, err := bridge.GetInstantSend(common.DASHChain)
		c.Assert(err, IsNil)
		state.ObservationHeight = 120
		state.Status.MinHeight = tc.floor
		bridge.accounting = state
		client.bridge = bridge
		item := instantSendTestItem(f, f.inbound)
		item.BlockHeight = tc.height
		items, err := client.instantSend.claimBatch([]*types.TxInItem{item})
		c.Assert(err, IsNil)
		c.Assert(items, HasLen, tc.accepted)
		cleanup()
	}
}

func (s *DashSuite) TestInstantSendLostLockRecheckedAfterRestart(c *C) {
	client, f, cleanup := newDashMempoolFixture(c)
	defer cleanup()
	bridge := &dashMempoolPolicyBridge{enabled: 1}
	state, err := bridge.GetInstantSend(common.DASHChain)
	c.Assert(err, IsNil)
	state.Vaults[0].State.Pending = []common.PendingInbound{{Id: common.TxID(strings.ToUpper(f.inbound)), Amount: common.One / 10, Height: 1}}
	bridge.accounting = state
	client.bridge = bridge
	f.txs[f.inbound]["instantlock"] = false
	_, err = client.instantSend.claimBatch([]*types.TxInItem{instantSendTestItem(f, f.lowFeeInbound)})
	c.Assert(err, Equals, errInstantSendPaused)
	client.instantSend = instantSendHandler{client: client, enabled: true}
	c.Assert(client.instantSend.verifyPending(state), Equals, errInstantSendPaused)
	f.txs[f.inbound]["instantlock"] = true
	c.Assert(client.instantSend.verifyPending(state), IsNil)
	c.Assert(state.Vaults[0].State.Pending, HasLen, 1) // Daemon recovery never releases exposure.
	state.Vaults[0].State.Pending = nil                // Core settled or wrote off the deposit.
	f.txs[f.inbound]["instantlock"] = false
	c.Assert(client.instantSend.verifyPending(state), IsNil)
}

func (s *DashSuite) TestInstantSendTransientFailureAndUnhealthyScanner(c *C) {
	client, f, cleanup := newDashMempoolFixture(c)
	defer cleanup()
	bridge := &dashMempoolPolicyBridge{enabled: 1}
	state, err := bridge.GetInstantSend(common.DASHChain)
	c.Assert(err, IsNil)
	state.Vaults[0].State.Pending = []common.PendingInbound{{Id: common.TxID(strings.ToUpper(f.inbound)), Amount: common.One / 10, Height: 1}}
	f.overrideRPC = func(method string, _ []json.RawMessage) (any, bool) {
		if method == "getrawtransaction" {
			return &btcjson.RPCError{Code: btcjson.ErrRPCInternal.Code, Message: "temporarily unavailable"}, true
		}
		return nil, false
	}
	c.Assert(client.instantSend.verifyPending(state), NotNil)
	f.overrideRPC = nil
	client.chainLockHealth.Store(nil)
	_, err = client.instantSend.claimBatch([]*types.TxInItem{instantSendTestItem(f, f.lowFeeInbound)})
	c.Assert(err, Equals, errInstantSendPaused)
	marked, err := client.temporalStorage.HasTxMarker(f.lowFeeInbound)
	c.Assert(err, IsNil)
	c.Assert(marked, Equals, false)
}

func (s *DashSuite) TestMissingPendingDepositKeepsOrdinaryMempoolScan(c *C) {
	client, f, cleanup := newDashMempoolFixture(c)
	defer cleanup()
	bridge := &dashMempoolPolicyBridge{enabled: 1}
	state, err := bridge.GetInstantSend(common.DASHChain)
	c.Assert(err, IsNil)
	state.Vaults[0].State.Pending = []common.PendingInbound{{Id: common.TxID(strings.ToUpper(f.inbound)), Amount: common.One / 10, Height: 1}}
	bridge.accounting = state
	client.bridge = bridge
	f.overrideRPC = func(method string, params []json.RawMessage) (any, bool) {
		if method == "getrawtransaction" {
			var id string
			c.Assert(json.Unmarshal(params[0], &id), IsNil)
			if id == f.inbound {
				return &btcjson.RPCError{Code: btcjson.ErrRPCNoTxInfo, Message: "No such mempool transaction"}, true
			}
		}
		return nil, false
	}
	c.Assert(client.instantSend.verifyPending(state), Equals, errInstantSendPaused)
	batch, err := client.FetchMemPool(151)
	c.Assert(err, IsNil)
	c.Assert(batch.TxArray, HasLen, 2)
	for _, item := range batch.TxArray {
		c.Assert(item.InstantSend, IsNil)
	}
}
