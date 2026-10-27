package solana

import (
	"time"

	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/bifrost/pubkeymanager"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	stypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/config"
)

// TestNonSOLDropDoesNotHoldDurableSiblings: SignTx drops an outbound of an asset this
// client cannot send, without signing it. THORChain keeps such an item pending on its
// vault while the vault's durable nonce has not moved, and reschedules it there ahead
// of the vault's later outbounds. If the durable-nonce serialize gate waited on it,
// every later SOL outbound of the vault would wait behind it for good: a node running
// this release meets exactly that once SPL outbounds exist. The drop must be
// remembered as durable-rejected, as the pre-sign filter's rejects are.
func (s *SolTestSuite) TestNonSOLDropDoesNotHoldDurableSiblings(c *C) {
	pubkeyMgr, err := pubkeymanager.NewPubKeyManager(s.bridge, s.m)
	c.Assert(err, IsNil)
	e, err := NewSOLClient(s.thorKeys,
		config.BifrostChainConfiguration{
			RPCHost: "http://" + s.server.Listener.Addr().String(),
			BlockScanner: config.BifrostBlockScannerConfiguration{
				ChainID:            common.SOLChain,
				StartBlockHeight:   1, // avoids querying thorchain for block height
				HTTPRequestTimeout: time.Second,
				MaxGasLimit:        80000,
			},
		}, nil, s.bridge, s.m, pubkeyMgr, thorclient.NewPoolMgr(s.bridge))
	c.Assert(err, IsNil)
	c.Assert(pubkeyMgr.Start(), IsNil)
	defer func() { c.Assert(pubkeyMgr.Stop(), IsNil) }()
	e.solScanner.globalNetworkFeeQueue = make(chan common.NetworkFee, 1)
	vault := pubkeyMgr.GetPubKeys()[0]

	const height = int64(100)
	token := guardTokenOutbound(c, vault)
	signed, checkpoint, obs, err := e.SignTx(token, height)
	c.Assert(err, IsNil)
	c.Assert(signed, IsNil)
	c.Assert(checkpoint, IsNil)
	c.Assert(obs, IsNil) // dropped for reschedule
	c.Check(e.durableRejected(pendingOf(token, height)), Equals, true)

	// A later SOL outbound of the same vault does not wait behind the dropped item.
	next := stypes.TxOutItem{
		Chain: common.SOLChain, ToAddress: token.ToAddress, VaultPubKey: vault, VaultPubKeyEddsa: vault,
		Coins:  common.Coins{common.NewCoin(common.SOLAsset, cosmos.NewUint(11047250))},
		Memo:   "OUT:7D91ADAFA69765E7805B5FF2F3A0BA1DBE69E37A1CFCD20C48B99C528AA3EE87",
		InHash: "7D91ADAFA69765E7805B5FF2F3A0BA1DBE69E37A1CFCD20C48B99C528AA3EE87",
	}
	bridge := e.bridge
	e.bridge = &pendingBridge{items: []thorclient.PendingOutbound{pendingOf(token, height), pendingOf(next, height+1)}}
	c.Check(e.durableSerializeAllows(next, height+1), Equals, true)
	e.bridge = bridge
}
