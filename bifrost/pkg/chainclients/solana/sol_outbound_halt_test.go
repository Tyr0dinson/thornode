package solana

import (
	"strings"
	"sync/atomic"

	"github.com/rs/zerolog/log"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/signing"
	"gitlab.com/thorchain/thornode/v3/bifrost/pubkeymanager"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/constants"
	types2 "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

type SolOutboundHaltSuite struct{}

var _ = Suite(&SolOutboundHaltSuite{})

// outboundHaltSOLBridge serves the halt mimirs, the THORChain height and the
// Asgard set.
type outboundHaltSOLBridge struct {
	thorclient.ThorchainBridge
	height  int64
	mimirs  map[string]int64
	asgards types2.Vaults
}

func (b *outboundHaltSOLBridge) GetBlockHeight() (int64, error) { return b.height, nil }

func (b *outboundHaltSOLBridge) GetMimir(key string) (int64, error) {
	for k, v := range b.mimirs {
		if strings.EqualFold(k, key) {
			return v, nil
		}
	}
	return -1, nil
}

func (b *outboundHaltSOLBridge) GetAsgards() (types2.Vaults, error) { return b.asgards, nil }

func (b *outboundHaltSOLBridge) FetchNodeStatus() (types2.NodeStatus, error) {
	return types2.NodeStatus_Active, nil
}

// The keep-alive is a CONSOLIDATE self-send to the vault's EdDSA SOL address, so
// HaltOutbound lets it through; it keeps the scanner able to observe migrations.
// Sent anywhere but an Asgard vault, the same item is blocked.
func (s *SolOutboundHaltSuite) TestKeepAliveItemUnderHaltOutbound(c *C) {
	vault := types2.NewVaultV2(1, types2.VaultStatus_RetiringVault, types2.VaultType_AsgardVault,
		types2.GetRandomPubKey(), common.Chains{common.SOLChain}.Strings(), nil, types2.GetRandomEd25519PubKey())
	addr, err := vault.PubKeyEddsa.GetAddress(common.SOLChain)
	c.Assert(err, IsNil)
	other, err := types2.GetRandomEd25519PubKey().GetAddress(common.SOLChain)
	c.Assert(err, IsNil)

	for _, mimirs := range []map[string]int64{
		{constants.MimirKeyHaltOutbound: 1},
		{"HaltOutboundSOL": 1},
	} {
		bridge := &outboundHaltSOLBridge{height: 100, mimirs: mimirs, asgards: types2.Vaults{vault}}
		self := keepAliveTxOutItem(vault, addr, keepAliveAmount(5000), 5000)
		c.Check(signing.IsTxOutBlockedByOutboundHalt(bridge, self, log.Logger), Equals, false, Commentf("%v", mimirs))

		away := keepAliveTxOutItem(vault, other, keepAliveAmount(5000), 5000)
		c.Check(signing.IsTxOutBlockedByOutboundHalt(bridge, away, log.Logger), Equals, true, Commentf("%v", mimirs))
	}
}

// countingPubKeyMgr records whether provisioning got past its halt gates (the
// node pubkey lookup is the next step) and returns no key so it stops there.
type countingPubKeyMgr struct {
	pubkeymanager.PubKeyValidator
	nodePubKeyCalls int
}

func (m *countingPubKeyMgr) GetNodePubKey(common.SigningAlgo) common.PubKey {
	m.nodePubKeyCalls++
	return common.EmptyPubKey
}

// Durable-nonce provisioning funds a nonce account that is not an Asgard vault
// address, so HaltOutbound blocks it. Outbounds fall back to the recent-blockhash
// path without the nonce account, so this never stalls a migration.
func (s *SolOutboundHaltSuite) TestNonceProvisionBlockedUnderHaltOutbound(c *C) {
	for _, tc := range []struct {
		mimirs  map[string]int64
		reaches bool
	}{
		{map[string]int64{}, true},
		{map[string]int64{"HaltOutboundSOL": 101}, true}, // not active yet
		{map[string]int64{"HaltOutboundBTC": 1}, true},   // other chain
		{map[string]int64{"HaltOutboundSOL": 1}, false},
		{map[string]int64{constants.MimirKeyHaltOutbound: 1}, false},
		{map[string]int64{"HaltSigningSOL": 1}, false},
	} {
		healthy := &atomic.Bool{}
		healthy.Store(true)
		pkm := &countingPubKeyMgr{}
		cl := &SOLClient{
			bridge:     &outboundHaltSOLBridge{height: 100, mimirs: tc.mimirs},
			logger:     log.Logger,
			solScanner: &SOLScanner{healthy: healthy},
			pubkeyMgr:  pkm,
		}
		cl.cfg.ChainID = common.SOLChain
		cl.nonceProvisionCheck(100)
		c.Check(pkm.nodePubKeyCalls == 1, Equals, tc.reaches, Commentf("mimirs %v", tc.mimirs))
	}
}
