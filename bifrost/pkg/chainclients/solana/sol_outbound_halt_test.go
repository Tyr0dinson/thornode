package solana

import (
	"errors"
	"strings"
	"sync/atomic"

	"github.com/rs/zerolog/log"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/signing"
	"gitlab.com/thorchain/thornode/v3/bifrost/pubkeymanager"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	stypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/constants"
	mem "gitlab.com/thorchain/thornode/v3/x/thorchain/memo"
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

// serializeHaltBridge serves the pending queue plus the HaltOutbound mimirs and
// the Asgard set as of a consensus height, recording the heights read. The live
// GetMimir is deliberately different so a live read would show up.
type serializeHaltBridge struct {
	thorclient.ThorchainBridge
	pending      []thorclient.PendingOutbound
	byHeight     map[int64]map[string]int64
	asgards      types2.Vaults
	mimirErr     error
	asgardErr    error
	mimirReadAt  []int64
	asgardReadAt []int64
}

func (b *serializeHaltBridge) GetPendingOutbounds(int64) ([]thorclient.PendingOutbound, error) {
	return b.pending, nil
}

func (b *serializeHaltBridge) GetMimir(string) (int64, error) { return 1, nil }

func (b *serializeHaltBridge) GetMimirAtHeight(key string, height int64) (int64, error) {
	b.mimirReadAt = append(b.mimirReadAt, height)
	if b.mimirErr != nil {
		return 0, b.mimirErr
	}
	for k, v := range b.byHeight[height] {
		if strings.EqualFold(k, key) {
			return v, nil
		}
	}
	return -1, nil
}

func (b *serializeHaltBridge) GetAsgardsAtHeight(height int64) (types2.Vaults, error) {
	b.asgardReadAt = append(b.asgardReadAt, height)
	if b.asgardErr != nil {
		return nil, b.asgardErr
	}
	return b.asgards, nil
}

// A durable-nonce vault V is Retiring with an external SOL outbound X queued at
// height 100 and its MIGRATE M to the new vault at 110. HaltOutbound refuses X,
// so X never consumes V's nonce; if the serialize gate still waited on X, M would
// stall until the halt lifts and the next churn would skip on V's funds. Read at
// M's keysign height, the halt lets M past X, and only past siblings the halt
// blocks.
func (s *SolOutboundHaltSuite) TestDurableSerializeSkipsSiblingBlockedByHaltOutbound(c *C) {
	sol := common.SOLChain.String()
	retiring := types2.NewVaultV2(1, types2.VaultStatus_RetiringVault, types2.VaultType_AsgardVault,
		types2.GetRandomPubKey(), common.Chains{common.SOLChain}.Strings(), nil, types2.GetRandomEd25519PubKey())
	active := types2.NewVaultV2(2, types2.VaultStatus_ActiveVault, types2.VaultType_AsgardVault,
		types2.GetRandomPubKey(), common.Chains{common.SOLChain}.Strings(), nil, types2.GetRandomEd25519PubKey())
	asgards := types2.Vaults{retiring, active}
	activeAddr, err := active.PubKeyEddsa.GetAddress(common.SOLChain)
	c.Assert(err, IsNil)
	userAddr, err := types2.GetRandomEd25519PubKey().GetAddress(common.SOLChain)
	c.Assert(err, IsNil)

	vault := retiring.PubKey.String()
	migrateMemo := mem.NewMigrateMemo(5).String()
	external := thorclient.PendingOutbound{
		Chain: sol, VaultPubKey: vault, ToAddress: userAddr.String(),
		Memo: "OUT:" + types2.GetRandomTxHash().String(), Height: 100,
	}
	ragnarok := thorclient.PendingOutbound{
		Chain: sol, VaultPubKey: vault, ToAddress: userAddr.String(),
		Memo: mem.NewRagnarokMemo(5).String(), Height: 100,
	}
	earlierMigrate := thorclient.PendingOutbound{
		Chain: sol, VaultPubKey: vault, ToAddress: activeAddr.String(), Memo: migrateMemo, Height: 100,
	}
	// A MIGRATE memo to an address that is no Asgard vault is external.
	fakeMigrate := thorclient.PendingOutbound{
		Chain: sol, VaultPubKey: vault, ToAddress: userAddr.String(), Memo: migrateMemo, Height: 100,
	}
	m := thorclient.PendingOutbound{
		Chain: sol, VaultPubKey: vault, ToAddress: activeAddr.String(), Memo: migrateMemo, Height: 110,
	}
	migrate := stypes.TxOutItem{
		Chain: common.SOLChain, VaultPubKey: retiring.PubKey, ToAddress: activeAddr, Memo: migrateMemo, Height: 110,
	}
	chainHalt := map[int64]map[string]int64{110: {"HaltOutboundSOL": 105}}

	for _, tc := range []struct {
		name      string
		siblings  []thorclient.PendingOutbound
		byHeight  map[int64]map[string]int64
		mimirErr  error
		asgardErr error
		allow     bool
	}{
		{"no halt: M waits behind X", []thorclient.PendingOutbound{external}, nil, nil, nil, false},
		{"HaltOutboundSOL: X skipped", []thorclient.PendingOutbound{external}, chainHalt, nil, nil, true},
		{"HALTOUTBOUND: X skipped", []thorclient.PendingOutbound{external}, map[int64]map[string]int64{110: {"HALTOUTBOUND": 1}}, nil, nil, true},
		{"ragnarok sibling skipped", []thorclient.PendingOutbound{ragnarok}, chainHalt, nil, nil, true},
		{"migrate to a non-vault skipped", []thorclient.PendingOutbound{fakeMigrate}, chainHalt, nil, nil, true},
		{"halt for another chain: waits", []thorclient.PendingOutbound{external}, map[int64]map[string]int64{110: {"HaltOutboundETH": 1}}, nil, nil, false},
		{"halt not yet active at keysign height: waits", []thorclient.PendingOutbound{external}, map[int64]map[string]int64{110: {"HaltOutboundSOL": 111}}, nil, nil, false},
		// Set at a later height only: the keysign height decides, not the live value.
		{"halt only at a later height: waits", []thorclient.PendingOutbound{external}, map[int64]map[string]int64{120: {"HaltOutboundSOL": 1}}, nil, nil, false},
		{"earlier internal sibling: still waits", []thorclient.PendingOutbound{earlierMigrate}, chainHalt, nil, nil, false},
		{"one skipped, one internal: waits", []thorclient.PendingOutbound{external, earlierMigrate}, chainHalt, nil, nil, false},
		{"mimir read error: waits", []thorclient.PendingOutbound{external}, chainHalt, errors.New("boom"), nil, false},
		{"asgard read error: waits", []thorclient.PendingOutbound{external}, chainHalt, nil, errors.New("boom"), false},
	} {
		bridge := &serializeHaltBridge{
			pending:   append(append([]thorclient.PendingOutbound{}, tc.siblings...), m),
			byHeight:  tc.byHeight,
			asgards:   asgards,
			mimirErr:  tc.mimirErr,
			asgardErr: tc.asgardErr,
		}
		cl := &SOLClient{bridge: bridge, logger: log.Logger}
		c.Check(cl.durableSerializeAllows(migrate, 110), Equals, tc.allow, Commentf(tc.name))
		for _, h := range append(append([]int64{}, bridge.mimirReadAt...), bridge.asgardReadAt...) {
			c.Check(h, Equals, int64(110), Commentf("%s: read at height %d, want the keysign height", tc.name, h))
		}
		c.Check(len(bridge.mimirReadAt) <= 2 && len(bridge.asgardReadAt) <= 1, Equals, true,
			Commentf("%s: halt state is read once per decision", tc.name))
	}

	// M at the head of its vault's queue reads no halt state at all.
	bridge := &serializeHaltBridge{pending: []thorclient.PendingOutbound{m}, byHeight: chainHalt, asgards: asgards}
	cl := &SOLClient{bridge: bridge, logger: log.Logger}
	c.Check(cl.durableSerializeAllows(migrate, 110), Equals, true)
	c.Check(bridge.mimirReadAt, HasLen, 0)
	c.Check(bridge.asgardReadAt, HasLen, 0)
}
