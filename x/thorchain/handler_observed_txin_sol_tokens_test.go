package thorchain

import (
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

type SOLTokenGateSuite struct{}

var _ = Suite(&SOLTokenGateSuite{})

const solTokenGateUSDC = "SOL.USDC-EPJFWDD5AUFQSSQEM2QN1XZYBAPC8G4WEGGKZWYTDT1V" //nolint:gosec // G101: a THORChain asset name, not a credential

type solTokenGateFixture struct {
	ctx   cosmos.Context
	mgr   Manager
	na    NodeAccount
	vault Vault
	usdc  common.Asset
}

func newSOLTokenGateFixture(c *C) solTokenGateFixture {
	ctx, mgr := setupManagerForTest(c)
	ctx = ctx.WithBlockHeight(18)
	na := GetRandomValidatorNode(NodeActive)
	c.Assert(mgr.Keeper().SetNodeAccount(ctx, na), IsNil)
	vault := NewVaultV2(ctx.BlockHeight(), ActiveVault, AsgardVault, GetRandomPubKey(), []string{common.SOLChain.String()}, nil, types.GetRandomEd25519PubKey())
	c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)
	usdc, err := common.NewAsset(solTokenGateUSDC)
	c.Assert(err, IsNil)
	return solTokenGateFixture{ctx: ctx, mgr: mgr, na: na, vault: vault, usdc: usdc}
}

// observe runs a finalised single-node observation of an inbound to the vault.
func (f solTokenGateFixture) observe(c *C, from common.Address, coin common.Coin, memo string) common.TxID {
	vaultAddr, err := f.vault.PubKeyEddsa.GetAddress(common.SOLChain)
	c.Assert(err, IsNil)
	tx := common.NewTx(GetRandomTxHash(), from, vaultAddr, common.NewCoins(coin),
		common.Gas{common.NewCoin(common.SOLAsset, cosmos.NewUint(500))}, memo)
	obs := NewObservedTx(tx, 12, f.vault.PubKeyEddsa, 12)
	_, err = NewObservedTxInHandler(f.mgr).handle(f.ctx, *NewMsgObservedTxIn(ObservedTxs{obs}, f.na.NodeAddress))
	c.Assert(err, IsNil)
	return tx.ID
}

func (f solTokenGateFixture) balance(c *C, asset common.Asset) cosmos.Uint {
	v, err := f.mgr.Keeper().GetVault(f.ctx, f.vault.PubKey)
	c.Assert(err, IsNil)
	return v.GetCoin(asset).Amount
}

func randomSOLAddress(c *C) common.Address {
	addr, err := types.GetRandomEd25519PubKey().GetAddress(common.SOLChain)
	c.Assert(err, IsNil)
	return addr
}

// Until the SOLTokens mimir is set, an SPL token deposit is a no-op: the vault is not
// credited, and nothing is swapped, refunded or scheduled. A Bifrost that predates SPL
// support signs any SOL-chain outbound as a native SOL transfer of the coin amount, so
// no SPL value may enter until every signer runs SPL-aware Bifrost. The observation is
// still closed out so it does not linger as pending inbound work.
func (s *SOLTokenGateSuite) TestSOLTokenInboundNoOpUntilEnabled(c *C) {
	f := newSOLTokenGateFixture(c)
	user := randomSOLAddress(c)
	deposit := common.NewCoin(f.usdc, cosmos.NewUint(25*common.One))

	txID := f.observe(c, user, deposit, "=:BTC.BTC:"+GetRandomBTCAddress().String())
	c.Assert(f.balance(c, f.usdc).IsZero(), Equals, true)
	voter, err := f.mgr.Keeper().GetObservedTxInVoter(f.ctx, txID)
	c.Assert(err, IsNil)
	c.Assert(voter.FinalisedHeight, Equals, f.ctx.BlockHeight())
	c.Assert(voter.Tx.Status, Equals, common.Status_done)
	c.Assert(voter.UpdatedVault, Equals, false)
	swaps, err := newSwapQueue(f.mgr.Keeper()).FetchQueue(f.ctx)
	c.Assert(err, IsNil)
	c.Assert(swaps, HasLen, 0)
	items, err := f.mgr.TxOutStore().GetOutboundItems(f.ctx)
	c.Assert(err, IsNil)
	c.Assert(items, HasLen, 0) // no refund either: a refund is an SPL outbound

	// Once enabled, deposits are credited as usual.
	f.mgr.Keeper().SetMimir(f.ctx, "SOLTokens", 1)
	f.observe(c, user, deposit, "=:BTC.BTC:"+GetRandomBTCAddress().String())
	c.Assert(f.balance(c, f.usdc).Equal(deposit.Amount), Equals, true)
}

// SOL itself is never gated.
func (s *SOLTokenGateSuite) TestSOLInboundNotGated(c *C) {
	f := newSOLTokenGateFixture(c)
	deposit := common.NewCoin(common.SOLAsset, cosmos.NewUint(3*common.One))
	f.observe(c, randomSOLAddress(c), deposit, "=:BTC.BTC:"+GetRandomBTCAddress().String())
	c.Assert(f.balance(c, common.SOLAsset).Equal(deposit.Amount), Equals, true)
}

// A transfer between THORChain's own vaults (a migration) only moves tokens that
// entered while the mimir was on, so it is credited even if the mimir is later turned
// off; gating it would drop the tokens from vault accounting. A vault sender cannot
// be forged: an SPL sender is the source token account's owner, who must sign.
func (s *SOLTokenGateSuite) TestSOLTokenVaultTransferNotGated(c *C) {
	f := newSOLTokenGateFixture(c)
	retiring := NewVaultV2(f.ctx.BlockHeight(), RetiringVault, AsgardVault, GetRandomPubKey(), []string{common.SOLChain.String()}, nil, types.GetRandomEd25519PubKey())
	c.Assert(f.mgr.Keeper().SetVault(f.ctx, retiring), IsNil)
	from, err := retiring.PubKeyEddsa.GetAddress(common.SOLChain)
	c.Assert(err, IsNil)

	migrated := common.NewCoin(f.usdc, cosmos.NewUint(40*common.One))
	f.observe(c, from, migrated, NewMigrateMemo(12).String())
	c.Assert(f.balance(c, f.usdc).Equal(migrated.Amount), Equals, true)
}
