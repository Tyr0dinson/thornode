package solana

import (
	"bytes"
	"time"

	soltypes "gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/solana/types"
	"gitlab.com/thorchain/thornode/v3/bifrost/pubkeymanager"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	stypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/config"

	. "gopkg.in/check.v1"
)

// TestSignTxDurableNonceOffLegacyShape proves that with the SOLDurableNonce mimir
// unset (the default / disabled state), SignTx produces a recent-blockhash
// transaction byte-shape-identical to the pre-#661 (3.20.3) path — never a
// durable-nonce tx. The proof: the signed bytes contain no RecentBlockhashes sysvar
// account, which every durable-nonce tx (AdvanceNonceAccount) must list. Provisioning
// is likewise gated on the same mimir (>0), so with it off no nonce account is ever
// created and the durable branch is never taken. This is the "off is inert" guard
// the 3.20.4 release requires; the safe-when-ON serialize-with-finality path (N2) and
// its burst tests land before any enable.
func (s *SolTestSuite) TestSignTxDurableNonceOffLegacyShape(c *C) {
	pubkeyMgr, err := pubkeymanager.NewPubKeyManager(s.bridge, s.m)
	c.Assert(err, IsNil)
	poolMgr := thorclient.NewPoolMgr(s.bridge)
	e, err := NewSOLClient(s.thorKeys,
		config.BifrostChainConfiguration{
			RPCHost: "http://" + s.server.Listener.Addr().String(),
			BlockScanner: config.BifrostBlockScannerConfiguration{
				ChainID:            common.SOLChain,
				StartBlockHeight:   1,
				HTTPRequestTimeout: time.Second,
				MaxGasLimit:        80000,
			},
		}, nil, s.bridge, s.m, pubkeyMgr, poolMgr)
	c.Assert(err, IsNil)
	c.Assert(e, NotNil)
	c.Assert(pubkeyMgr.Start(), IsNil)
	defer func() { c.Assert(pubkeyMgr.Stop(), IsNil) }()
	e.solScanner.globalNetworkFeeQueue = make(chan common.NetworkFee, 1)
	pubkeys := pubkeyMgr.GetPubKeys()

	result, _, obs, err := e.SignTx(stypes.TxOutItem{
		Chain:            common.SOLChain,
		ToAddress:        "D9A6eE2pZ6oSiGb8BPkag4gvAeEhHvc3eEYPBoLSMshG",
		VaultPubKey:      pubkeys[0],
		VaultPubKeyEddsa: pubkeys[0],
		Coins: common.Coins{
			common.NewCoin(common.SOLAsset, cosmos.NewUint(11047250)),
		},
		MaxGas: common.Gas{
			common.NewCoin(common.SOLAsset, cosmos.NewUint(15000)),
		},
		GasRate: 1,
		Memo:    "OUT:4D91ADAFA69765E7805B5FF2F3A0BA1DBE69E37A1CFCD20C48B99C528AA3EE87",
	}, 1)
	c.Assert(err, IsNil)
	c.Assert(result, NotNil)
	c.Assert(obs, NotNil)

	// No RecentBlockhashes sysvar account => this is the legacy recent-blockhash tx,
	// not a durable-nonce (AdvanceNonceAccount) tx.
	sysvar := soltypes.SysVarRecentBlockhashesID
	c.Assert(bytes.Contains(result, sysvar[:]), Equals, false)
}
