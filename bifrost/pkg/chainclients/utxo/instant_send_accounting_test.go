package utxo

import (
	"encoding/json"
	"strings"

	"github.com/btcsuite/btcd/btcjson"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	ttypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
	. "gopkg.in/check.v1"
)

type dashRecoveryBridge struct{ dashMempoolPolicyBridge }

func (b *dashRecoveryBridge) GetMimir(key string) (int64, error) {
	if key == "SolvencyHaltDASHChain" {
		return 10, nil
	}
	return b.dashMempoolPolicyBridge.GetMimir(key)
}

func (s *DashSuite) TestDashSolvencyReportsOnlyInsolventOrAllClear(c *C) {
	client, f, cleanup := newDashMempoolFixture(c)
	defer cleanup()
	key, err := dashTestVaultPubKey()
	c.Assert(err, IsNil)
	other := ttypes.GetRandomPubKey()
	otherAddr, err := other.GetAddress(common.DASHChain)
	c.Assert(err, IsNil)
	coins := func(amount uint64) common.Coins {
		return common.NewCoins(common.NewCoin(common.DASHAsset, cosmos.NewUint(amount)))
	}
	bridge := &dashRecoveryBridge{dashMempoolPolicyBridge{enabled: 1, accounting: &ttypes.QueryInstantSendResponse{Vaults: []ttypes.QueryInstantSendVault{
		{State: common.InstantSendState{Chain: common.DASHChain, PubKey: key}, Coins: coins(common.One)},
		{State: common.InstantSendState{Chain: common.DASHChain, PubKey: other, Pending: []common.PendingInbound{{Id: common.TxID(strings.Repeat("A", 64)), Amount: common.One, Height: 1}}}, Coins: coins(2 * common.One)},
	}}}}
	client.bridge = bridge
	queue := make(chan types.Solvency, 10)
	client.globalSolvencyQueue = queue
	client.stopchan = make(chan struct{})
	recoverFirst, failSecond := false, false
	f.overrideRPC = func(method string, params []json.RawMessage) (any, bool) {
		if method != "listunspent" {
			return nil, false
		}
		var addresses []string
		c.Assert(json.Unmarshal(params[2], &addresses), IsNil)
		if addresses[0] == otherAddr.String() && failSecond {
			return &btcjson.RPCError{Code: btcjson.ErrRPCInternal.Code, Message: "unavailable"}, true
		}
		amount := 1.0
		if addresses[0] != otherAddr.String() && !recoverFirst {
			amount = 0
		}
		return []btcjson.ListUnspentResult{{TxID: strings.Repeat("9", 64), Confirmations: 1, Amount: amount, ScriptPubKey: newDashP2PKHScriptHex(c, f.asgard)}}, true
	}
	c.Assert(client.reportDashSolvency(), IsNil)
	c.Assert(queue, HasLen, 1)
	c.Assert((<-queue).PubKey, Equals, key)
	// Queue delivery is not an attestation acknowledgement: retry even at a stalled ChainLock.
	c.Assert(client.reportDashSolvency(), IsNil)
	c.Assert(queue, HasLen, 1)
	c.Assert((<-queue).PubKey, Equals, key)
	recoverFirst, failSecond = true, true
	c.Assert(client.reportDashSolvency(), NotNil)
	c.Assert(queue, HasLen, 0) // incomplete collection cannot clear the halt
	failSecond = false
	c.Assert(client.reportDashSolvency(), IsNil)
	c.Assert(queue, HasLen, 2)
	// A pending credit may already have been spent before its settlement
	// certificate arrives. This is valid exposure, not a response error.
	bridge.accounting.Vaults[1].Coins = coins(0)
	state, err := client.getInstantSendAccounting()
	c.Assert(err, IsNil)
	c.Assert(state.Vaults[1].State.PendingAmount().Uint64(), Equals, uint64(common.One))
}

func (s *DashSuite) TestDashSolvencyReportsOverdueDeficit(c *C) {
	client, f, cleanup := newDashMempoolFixture(c)
	defer cleanup()
	bridge := &dashMempoolPolicyBridge{enabled: 1}
	state, err := bridge.GetInstantSend(common.DASHChain)
	c.Assert(err, IsNil)
	id := common.TxID(strings.ToUpper(f.inbound))
	state.Vaults[0].State.Pending = []common.PendingInbound{{Id: id, Amount: common.One, Height: 1}}
	state.Vaults[0].Coins = common.NewCoins(common.NewCoin(common.DASHAsset, cosmos.NewUint(2*common.One)))
	bridge.accounting = state
	client.bridge = bridge
	queue := make(chan types.Solvency, 2)
	client.globalSolvencyQueue = queue
	client.stopchan = make(chan struct{})
	f.overrideRPC = func(method string, _ []json.RawMessage) (any, bool) {
		if method == "listunspent" {
			return []btcjson.ListUnspentResult{{TxID: strings.Repeat("9", 64), Confirmations: 1, Amount: 1, ScriptPubKey: newDashP2PKHScriptHex(c, f.asgard)}}, true
		}
		return nil, false
	}
	c.Assert(client.reportDashSolvency(), IsNil)
	c.Assert(queue, HasLen, 0)
	state.Status.Overdue = []common.TxID{id}
	c.Assert(client.reportDashSolvency(), IsNil)
	c.Assert(queue, HasLen, 1)
	c.Assert((<-queue).PubKey, Equals, state.Vaults[0].State.PubKey)
	c.Assert(state.Vaults[0].State.PendingAmount().Uint64(), Equals, uint64(common.One))
}
