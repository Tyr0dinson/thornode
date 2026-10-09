package thorchain

import (
	"strings"

	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

type MigrationDataCovCSuite struct{}

var _ = Suite(&MigrationDataCovCSuite{})

// covCGasMgr fails GetMaxGas on the n-th call (1-based) for the given chain.
type covCGasMgr struct {
	GasManager
	failChain common.Chain
	failCall  int
	calls     map[common.Chain]int
}

func (g *covCGasMgr) GetMaxGas(ctx cosmos.Context, chain common.Chain) (common.Coin, error) {
	if g.calls == nil {
		g.calls = map[common.Chain]int{}
	}
	g.calls[chain]++
	if chain.Equals(g.failChain) && g.calls[chain] == g.failCall {
		return common.NoCoin, errKaboom
	}
	return g.GasManager.GetMaxGas(ctx, chain)
}

func covCSetupMigrationDataMgr(c *C) (cosmos.Context, *Mgrs) {
	ctx, mgr := setupManagerForTest(c)
	for _, chain := range []common.Chain{common.BSCChain, common.AVAXChain, common.BTCChain} {
		c.Assert(mgr.Keeper().SaveNetworkFee(ctx, chain, NetworkFee{
			Chain:              chain,
			TransactionSize:    1,
			TransactionFeeRate: 1000,
		}), IsNil)
	}
	return ctx, mgr
}

func covCCheckManualOutbounds(c *C, ctx cosmos.Context, mgr *Mgrs, items []TxOutItem) {
	for _, item := range items {
		c.Check(item.InHash.IsEmpty(), Equals, false)
		c.Check(item.ToAddress.IsEmpty(), Equals, false)
		c.Check(item.ToAddress.IsChain(item.Chain), Equals, true, Commentf("%s", item.ToAddress))
		c.Check(item.VaultPubKey.IsEmpty(), Equals, false)
		c.Check(item.Coin.Amount.IsZero(), Equals, false)
		c.Check(item.Coin.Asset.GetChain().Equals(item.Chain), Equals, true)
		c.Check(strings.HasSuffix(item.Memo, item.InHash.String()), Equals, true)
		c.Check(strings.HasPrefix(item.Memo, "OUT:") || strings.HasPrefix(item.Memo, "REFUND:"), Equals, true)
		c.Assert(item.MaxGas, HasLen, 1)
		maxGas, err := mgr.GasMgr().GetMaxGas(ctx, item.Chain)
		c.Assert(err, IsNil)
		c.Check(item.MaxGas[0].Equals(maxGas), Equals, true)
	}
}

func (s *MigrationDataCovCSuite) TestManualOutbounds6to7CovC(c *C) {
	ctx, mgr := covCSetupMigrationDataMgr(c)
	items, err := mainnetManualOutbounds6to7(ctx, mgr)
	c.Assert(err, IsNil)
	c.Assert(items, HasLen, 3)
	covCCheckManualOutbounds(c, ctx, mgr, items)

	c.Check(items[0].Chain.Equals(common.ETHChain), Equals, true)
	c.Check(items[0].Coin.Amount.Uint64(), Equals, uint64(2999900000000))
	c.Check(items[0].Memo, Equals, "OUT:000B1FF9E82D516788174D2FAE6EE535AAAA9ECC0B5071D16B5B8592CEA22FBD")
	c.Check(items[1].Coin.Amount.Uint64(), Equals, uint64(20000000000000))
	c.Check(items[2].Chain.Equals(common.BSCChain), Equals, true)
	c.Check(items[2].Coin.Asset.String(), Equals, "BSC.USDT-0X55D398326F99059FF775485246999027B3197955")

	// gas lookup failures are surfaced
	for _, chain := range []common.Chain{common.ETHChain, common.BSCChain} {
		gm := &covCGasMgr{GasManager: mgr.gasMgr, failChain: chain, failCall: 1}
		mgr.gasMgr = gm
		items, err = mainnetManualOutbounds6to7(ctx, mgr)
		c.Check(err, ErrorMatches, "fail to get max gas: kaboom")
		c.Check(items, IsNil)
		mgr.gasMgr = gm.GasManager
	}
}

func (s *MigrationDataCovCSuite) TestManualInbounds6to7CovC(c *C) {
	txs, err := mainnetManualInbounds6to7()
	c.Assert(err, IsNil)
	c.Assert(txs, HasLen, 3)

	expected := []struct {
		chain  common.Chain
		height int64
		amount uint64
	}{
		{common.BSCChain, 53453670, 350349963},
		{common.AVAXChain, 64039968, 105120813500},
		{common.ETHChain, 22756683, 1926543989},
	}
	for i, tx := range txs {
		c.Check(tx.Tx.Chain.Equals(expected[i].chain), Equals, true)
		c.Check(tx.BlockHeight, Equals, expected[i].height)
		c.Check(tx.FinaliseHeight, Equals, expected[i].height)
		c.Check(tx.Tx.Memo, Equals, "RECOVERY")
		c.Assert(tx.Tx.Coins, HasLen, 1)
		c.Check(tx.Tx.Coins[0].Amount.Uint64(), Equals, expected[i].amount)
		c.Check(tx.Tx.FromAddress.String(), Equals, "0x3c4a7c01811e14bb3d723d4961b4f2c28afc5e6e")
		c.Check(tx.ObservedPubKey.IsEmpty(), Equals, false)
		c.Check(tx.Tx.Gas, HasLen, 1)
		c.Check(tx.Tx.Gas[0].Asset.Equals(tx.Tx.Chain.GetGasAsset()), Equals, true)
	}
}

func (s *MigrationDataCovCSuite) TestManualOutbounds8to9CovC(c *C) {
	ctx, mgr := covCSetupMigrationDataMgr(c)
	base, err := mainnetManualOutbounds6to7(ctx, mgr)
	c.Assert(err, IsNil)

	items, err := mainnetManualOutbounds8to9(ctx, mgr)
	c.Assert(err, IsNil)
	c.Assert(items, HasLen, len(base)+3)
	c.Check(items[:len(base)], DeepEquals, base)
	covCCheckManualOutbounds(c, ctx, mgr, items)

	extra := items[len(base):]
	bscGas, err := mgr.GasMgr().GetMaxGas(ctx, common.BSCChain)
	c.Assert(err, IsNil)
	ethGas, err := mgr.GasMgr().GetMaxGas(ctx, common.ETHChain)
	c.Assert(err, IsNil)
	// gas is deducted from the refunded gas asset amounts, but not from tokens
	c.Check(extra[0].Coin.Amount.Uint64(), Equals, 350349963-bscGas.Amount.Uint64())
	c.Check(extra[1].Coin.Amount.Uint64(), Equals, uint64(105120813500))
	c.Check(extra[2].Coin.Amount.Uint64(), Equals, 1926543989-ethGas.Amount.Uint64())
	for _, item := range extra {
		c.Check(strings.HasPrefix(item.Memo, "REFUND:"), Equals, true)
	}

	type failCase struct {
		chain common.Chain
		call  int
		msg   string
	}
	for _, fc := range []failCase{
		{common.ETHChain, 1, "failed to get manual outbounds for 6to7: fail to get max gas: kaboom"},
		{common.ETHChain, 2, "fail to get max gas: kaboom"},
		{common.BSCChain, 2, "fail to get max gas: kaboom"},
		{common.AVAXChain, 1, "fail to get max gas: kaboom"},
	} {
		gm := &covCGasMgr{GasManager: mgr.gasMgr, failChain: fc.chain, failCall: fc.call}
		mgr.gasMgr = gm
		items, err = mainnetManualOutbounds8to9(ctx, mgr)
		c.Check(err, ErrorMatches, fc.msg, Commentf("chain %s call %d", fc.chain, fc.call))
		c.Check(items, IsNil)
		mgr.gasMgr = gm.GasManager
	}
}

func (s *MigrationDataCovCSuite) TestManualOutbounds9to10CovC(c *C) {
	ctx, mgr := covCSetupMigrationDataMgr(c)
	base, err := mainnetManualOutbounds8to9(ctx, mgr)
	c.Assert(err, IsNil)

	items, err := mainnetManualOutbounds9to10(ctx, mgr)
	c.Assert(err, IsNil)
	c.Assert(len(items) > len(base), Equals, true)
	c.Check(items[:len(base)], DeepEquals, base)
	covCCheckManualOutbounds(c, ctx, mgr, items)

	// every inbound hash is refunded at most once
	seen := map[string]bool{}
	for _, item := range items {
		c.Check(seen[item.InHash.String()], Equals, false, Commentf("duplicate %s", item.InHash))
		seen[item.InHash.String()] = true
	}

	// BTC refunds go to the BTC recovery address
	btcCount := 0
	for _, item := range items[len(base):] {
		if item.Chain.Equals(common.BTCChain) {
			btcCount++
			c.Check(item.ToAddress.String(), Equals, "bc1q257dt7c94z6rq54pv0jmtahe23uyxwyjksgyp5")
		}
	}
	c.Check(btcCount > 0, Equals, true)

	type failCase struct {
		chain common.Chain
		call  int
		msg   string
	}
	for _, fc := range []failCase{
		{common.AVAXChain, 1, "failed to get manual outbounds for 8to9: fail to get max gas: kaboom"},
		{common.ETHChain, 3, "fail to get max gas: kaboom"},
		{common.BSCChain, 3, "fail to get max gas: kaboom"},
		{common.AVAXChain, 2, "fail to get max gas: kaboom"},
		{common.BTCChain, 1, "fail to get max gas: kaboom"},
	} {
		gm := &covCGasMgr{GasManager: mgr.gasMgr, failChain: fc.chain, failCall: fc.call}
		mgr.gasMgr = gm
		items, err = mainnetManualOutbounds9to10(ctx, mgr)
		c.Check(err, ErrorMatches, fc.msg, Commentf("chain %s call %d", fc.chain, fc.call))
		c.Check(items, IsNil)
		mgr.gasMgr = gm.GasManager
	}
}
