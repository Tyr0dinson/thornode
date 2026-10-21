package solana

import (
	"time"

	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/solana/types"
	"gitlab.com/thorchain/thornode/v3/bifrost/pubkeymanager"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	stypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/config"
)

// guardTokenAsset is a Solana SPL token asset, which this client cannot send.
const guardTokenAsset = "SOL.USDC-EPJFWDD5AUFQSSQEM2QN1XZYBAPC8G4WEGGKZWYTDT1V" //nolint:gosec // G101: a THORChain asset name, not a credential

func guardTokenOutbound(c *C, vault common.PubKey) stypes.TxOutItem {
	usdc, err := common.NewAsset(guardTokenAsset)
	c.Assert(err, IsNil)
	return stypes.TxOutItem{
		Chain:            common.SOLChain,
		ToAddress:        "D9A6eE2pZ6oSiGb8BPkag4gvAeEhHvc3eEYPBoLSMshG",
		VaultPubKey:      vault,
		VaultPubKeyEddsa: vault,
		Coins:            common.Coins{common.NewCoin(usdc, cosmos.NewUint(100*common.One))},
		MaxGas:           common.Gas{common.NewCoin(common.SOLAsset, cosmos.NewUint(15000))},
		GasRate:          1,
		Memo:             "OUT:4D91ADAFA69765E7805B5FF2F3A0BA1DBE69E37A1CFCD20C48B99C528AA3EE87",
	}
}

// TestNonSOLOutboundNeverSigned: this client can only send SOL. Without a guard it
// builds any SOL-chain outbound as a native SOL transfer of the coin amount, so an
// SPL token outbound of 100 USDC would pay 100 SOL. Every node that runs a release
// with SPL support would build a different transaction, but a signing party made
// only of nodes without it would sign and pay. A non-SOL outbound must therefore never
// be built or signed: SignTx drops it (no signature, no error, so this node steps
// aside instead of retrying it forever), and both builders refuse it.
func (s *SolTestSuite) TestNonSOLOutboundNeverSigned(c *C) {
	pubkeyMgr, err := pubkeymanager.NewPubKeyManager(s.bridge, s.m)
	c.Assert(err, IsNil)
	poolMgr := thorclient.NewPoolMgr(s.bridge)
	e, err := NewSOLClient(s.thorKeys,
		config.BifrostChainConfiguration{
			RPCHost: "http://" + s.server.Listener.Addr().String(),
			BlockScanner: config.BifrostBlockScannerConfiguration{
				ChainID:            common.SOLChain,
				StartBlockHeight:   1, // avoids querying thorchain for block height
				HTTPRequestTimeout: time.Second,
				MaxGasLimit:        80000,
			},
		}, nil, s.bridge, s.m, pubkeyMgr, poolMgr)
	c.Assert(err, IsNil)
	c.Assert(pubkeyMgr.Start(), IsNil)
	defer func() { c.Assert(pubkeyMgr.Stop(), IsNil) }()
	e.solScanner.globalNetworkFeeQueue = make(chan common.NetworkFee, 1)
	vault := pubkeyMgr.GetPubKeys()[0]
	tx := guardTokenOutbound(c, vault)

	// Neither builder produces a transaction for it.
	_, err = e.CreateTx(tx, tx.Memo, mockBlockhashForSlot(950))
	c.Check(err, NotNil)
	vaultAddr, err := vault.GetAddress(common.SOLChain)
	c.Assert(err, IsNil)
	vaultKey, err := types.PublicKeyFromString(vaultAddr.String())
	c.Assert(err, IsNil)
	nonceAddr, err := common.SolNonceAccountAddress(vaultAddr.String())
	c.Assert(err, IsNil)
	nonceKey, err := types.PublicKeyFromString(nonceAddr)
	c.Assert(err, IsNil)
	_, err = e.CreateTxWithNonce(tx, tx.Memo, mockBlockhashForSlot(900), nonceKey, vaultKey)
	c.Check(err, NotNil)

	// SignTx signs nothing and returns no error, so the signer clears the item.
	signed, checkpoint, obs, err := e.SignTx(tx, 1)
	c.Check(err, IsNil)
	c.Check(signed, IsNil)
	c.Check(checkpoint, IsNil)
	c.Check(obs, IsNil)

	// SOL itself still signs.
	tx.Coins = common.Coins{common.NewCoin(common.SOLAsset, cosmos.NewUint(11047250))}
	signed, _, obs, err = e.SignTx(tx, 1)
	c.Assert(err, IsNil)
	c.Check(signed, NotNil)
	c.Check(obs, NotNil)
}
