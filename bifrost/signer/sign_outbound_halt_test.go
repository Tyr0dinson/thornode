package signer

import (
	"errors"

	"github.com/rs/zerolog/log"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients"
	"gitlab.com/thorchain/thornode/v3/bifrost/pubkeymanager"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/config"
	"gitlab.com/thorchain/thornode/v3/constants"
	mem "gitlab.com/thorchain/thornode/v3/x/thorchain/memo"
	ttypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// errReachedKeysign marks an item that got past every pre-sign gate: the
// GetKeysign lookup is the first step after the HaltOutbound check.
var errReachedKeysign = errors.New("reached keysign lookup")

// outboundHaltSigner builds a signer whose ETH outbound path stops at the
// keysign lookup, so a nil result means a gate discarded the item and
// errReachedKeysign means the gates let it through.
func outboundHaltSigner(c *C, mimirs map[string]int64) (*Signer, *mockSignBridge, ttypes.Vault) {
	vaultPubKey, err := common.NewPubKey(pubkeymanager.MockPubkey)
	c.Assert(err, IsNil)
	vault := ttypes.NewVaultV2(1, ttypes.VaultStatus_ActiveVault, ttypes.VaultType_AsgardVault, vaultPubKey,
		common.Chains{common.ETHChain}.Strings(), nil, common.EmptyPubKey)
	bridge := &mockSignBridge{
		blockHeight: 100,
		constants: map[string]int64{
			constants.SigningTransactionPeriod.String(): 300,
			constants.ChurnInterval.String():            43200,
		},
		mimirs:     mimirs,
		asgards:    ttypes.Vaults{vault},
		keysignErr: errReachedKeysign,
	}
	sign := &Signer{
		logger:            log.With().Str("module", "test").Logger(),
		cfg:               config.Bifrost{Signer: config.BifrostSignerConfiguration{RescheduleBufferBlocks: 100}},
		thorchainBridge:   bridge,
		constantsProvider: NewConstantsProvider(bridge),
		chains: map[common.Chain]chainclients.ChainClient{
			common.ETHChain: &MockChainClient{},
		},
		pubkeyMgr: pubkeymanager.NewMockPoolAddressValidator(),
	}
	return sign, bridge, vault
}

func outboundHaltItem(c *C, vault ttypes.Vault, toVault bool, memo string) TxOutStoreItem {
	to := common.Address("0x90f2b1ae50e6018230e90a33f98c7844a0ab635a")
	if toVault {
		var err error
		to, err = vault.GetAddress(common.ETHChain)
		c.Assert(err, IsNil)
	}
	return TxOutStoreItem{
		TxOutItem: types.TxOutItem{
			Chain:       common.ETHChain,
			VaultPubKey: vault.PubKey,
			ToAddress:   to,
			Memo:        memo,
		},
		Height: 90,
	}
}

const outboundHaltOutMemo = "OUT:A6F5B26F2F1B1A8BB39EF6DE0E1A6E1C5A3E1AEB8A84F9E8D24C0D9C3B6C3A1E"

func (s *SignCoverageSuite) TestSignAndBroadcast_HaltOutboundBlocksExternal(c *C) {
	for _, mimirs := range []map[string]int64{
		{constants.MimirKeyHaltOutbound: 50},
		{"HaltOutboundETH": 50},
	} {
		sign, bridge, vault := outboundHaltSigner(c, mimirs)
		for _, item := range []TxOutStoreItem{
			outboundHaltItem(c, vault, false, outboundHaltOutMemo),             // swap / withdraw payout
			outboundHaltItem(c, vault, false, mem.NewMigrateMemo(10).String()), // migrate memo, user address
			outboundHaltItem(c, vault, true, outboundHaltOutMemo),              // vault address, external memo
			outboundHaltItem(c, vault, true, ""),                               // memo-less self-send
		} {
			checkpoint, obs, err := sign.signAndBroadcast(item)
			c.Check(err, IsNil, Commentf("mimirs %v memo %q to %s", mimirs, item.TxOutItem.Memo, item.TxOutItem.ToAddress))
			c.Check(checkpoint, IsNil)
			c.Check(obs, IsNil)
		}
		c.Check(bridge.asgardsCalls > 0, Equals, true)
	}
}

func (s *SignCoverageSuite) TestSignAndBroadcast_HaltOutboundAllowsInternal(c *C) {
	for _, mimirs := range []map[string]int64{
		{constants.MimirKeyHaltOutbound: 50},
		{"HaltOutboundETH": 50},
	} {
		sign, _, vault := outboundHaltSigner(c, mimirs)
		for _, memo := range []string{mem.NewMigrateMemo(10).String(), mem.NewConsolidateMemo().String()} {
			_, _, err := sign.signAndBroadcast(outboundHaltItem(c, vault, true, memo))
			c.Check(err, Equals, errReachedKeysign, Commentf("mimirs %v memo %q", mimirs, memo))
		}
	}
}

// With no outbound halt (or a halt on another chain, or one scheduled for later)
// the gate never reads the vault set and external outbounds proceed.
func (s *SignCoverageSuite) TestSignAndBroadcast_HaltOutboundInactive(c *C) {
	for _, mimirs := range []map[string]int64{
		{},
		{"HaltOutboundBTC": 50},
		{"HaltOutboundETH": 101},
	} {
		sign, bridge, vault := outboundHaltSigner(c, mimirs)
		bridge.asgardsErr = errors.New("must not be read")
		_, _, err := sign.signAndBroadcast(outboundHaltItem(c, vault, false, outboundHaltOutMemo))
		c.Check(err, Equals, errReachedKeysign, Commentf("mimirs %v", mimirs))
		c.Check(bridge.asgardsCalls, Equals, 0)
	}
}

// A vault read failure under an active halt retries the item instead of
// signing it, even for a migration.
func (s *SignCoverageSuite) TestSignAndBroadcast_HaltOutboundVaultErrorFailsClosed(c *C) {
	sign, bridge, vault := outboundHaltSigner(c, map[string]int64{"HaltOutboundETH": 50})
	bridge.asgardsErr = errors.New("asgards unavailable")
	_, _, err := sign.signAndBroadcast(outboundHaltItem(c, vault, true, mem.NewMigrateMemo(10).String()))
	c.Assert(err, NotNil)
	c.Check(err, Not(Equals), errReachedKeysign)
}

func (s *SignCoverageSuite) TestSignAndBroadcast_HaltOutboundMimirErrorFailsClosed(c *C) {
	sign, bridge, vault := outboundHaltSigner(c, map[string]int64{})
	sign.thorchainBridge = &haltOutboundMimirErrBridge{mockSignBridge: bridge}
	_, _, err := sign.signAndBroadcast(outboundHaltItem(c, vault, true, mem.NewMigrateMemo(10).String()))
	c.Assert(err, NotNil)
	c.Check(err, Not(Equals), errReachedKeysign)
}

// HaltSigning still wins over HaltOutbound: a migration is not signed under it.
func (s *SignCoverageSuite) TestSignAndBroadcast_HaltSigningBlocksMigration(c *C) {
	sign, _, vault := outboundHaltSigner(c, map[string]int64{"HaltSigningETH": 50})
	checkpoint, obs, err := sign.signAndBroadcast(outboundHaltItem(c, vault, true, mem.NewMigrateMemo(10).String()))
	c.Check(err, IsNil)
	c.Check(checkpoint, IsNil)
	c.Check(obs, IsNil)
}

// haltOutboundMimirErrBridge fails only the HaltOutbound reads.
type haltOutboundMimirErrBridge struct {
	*mockSignBridge
}

func (b *haltOutboundMimirErrBridge) GetMimir(key string) (int64, error) {
	if key == constants.MimirKeyHaltOutbound {
		return 0, errors.New("mimir unavailable")
	}
	return b.mockSignBridge.GetMimir(key)
}
