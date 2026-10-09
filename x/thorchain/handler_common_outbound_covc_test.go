package thorchain

import (
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"
)

type CovCCCommonOutboundSuite struct{}

var _ = Suite(&CovCCCommonOutboundSuite{})

type covCCOutboundFixture struct {
	inHash      common.TxID
	outHash     common.TxID
	vaultPubKey common.PubKey
	vaultAddr   common.Address
	toAddr      common.Address
	item        TxOutItem
}

// covCCSetupOutbound stores a vault, an inbound voter and a scheduled TxOutItem.
func covCCSetupOutbound(c *C, ctx cosmos.Context, k keeper.Keeper, chain common.Chain, coin common.Coin, maxGas common.Coin, vaultStatus VaultStatus) covCCOutboundFixture {
	f := covCCOutboundFixture{
		inHash:      GetRandomTxHash(),
		outHash:     GetRandomTxHash(),
		vaultPubKey: GetRandomPubKey(),
	}
	var err error
	f.vaultAddr, err = f.vaultPubKey.GetAddress(chain)
	c.Assert(err, IsNil)
	f.toAddr, err = GetRandomPubKey().GetAddress(chain)
	c.Assert(err, IsNil)
	f.item = TxOutItem{
		Chain:       chain,
		InHash:      f.inHash,
		ToAddress:   f.toAddr,
		VaultPubKey: f.vaultPubKey,
		Coin:        coin,
		Memo:        NewOutboundMemo(f.inHash).String(),
		MaxGas:      common.Gas{maxGas},
	}
	txOut := NewTxOut(ctx.BlockHeight())
	txOut.TxArray = append(txOut.TxArray, f.item)
	c.Assert(k.SetTxOut(ctx, txOut), IsNil)

	vault := NewVault(1, vaultStatus, AsgardVault, f.vaultPubKey, common.Chains{chain}.Strings(), nil)
	c.Assert(k.SetVault(ctx, vault), IsNil)
	voter := NewObservedTxVoter(f.inHash, nil)
	voter.Height = ctx.BlockHeight()
	voter.FinalisedHeight = ctx.BlockHeight()
	k.SetObservedTxInVoter(ctx, voter)
	return f
}

func (f covCCOutboundFixture) observed(ctx cosmos.Context, coins common.Coins, gas common.Gas) ObservedTx {
	return NewObservedTx(common.NewTx(f.outHash, f.vaultAddr, f.toAddr, coins, gas, f.item.Memo),
		ctx.BlockHeight(), f.vaultPubKey, ctx.BlockHeight())
}

func covCCHasVaultSlash(res *cosmos.Result) bool {
	for _, e := range res.Events {
		if e.Type == "vault-slash" {
			return true
		}
	}
	return false
}

func (s *CovCCCommonOutboundSuite) TestVaultNotFound(c *C) {
	ctx, k := setupKeeperForTest(c)
	mgr := NewDummyMgrWithKeeper(k)
	tx := GetRandomObservedTx()
	res, err := NewCommonOutboundTxHandler(mgr).handle(ctx, tx, GetRandomTxHash())
	c.Assert(err, NotNil)
	c.Assert(res, IsNil)
	c.Assert(err.Error(), Matches, "(?s).*vault not found.*")
}

func (s *CovCCCommonOutboundSuite) TestGasAssetUnderspendRefundsGas(c *C) {
	ctx, mgr := setupManagerForTest(c)
	k := mgr.Keeper()
	f := covCCSetupOutbound(c, ctx, k, common.BTCChain,
		common.NewCoin(common.BTCAsset, cosmos.NewUint(common.One)),
		common.NewCoin(common.BTCAsset, cosmos.NewUint(1000)), ActiveVault)

	// signer spent 500 gas of 1000 allowed and sent the 500 surplus to the user
	obs := f.observed(ctx,
		common.NewCoins(common.NewCoin(common.BTCAsset, cosmos.NewUint(common.One+500))),
		common.Gas{common.NewCoin(common.BTCAsset, cosmos.NewUint(500))})

	before := len(mgr.GasMgr().GetGas())
	res, err := NewCommonOutboundTxHandler(mgr).handle(ctx, obs, f.inHash)
	c.Assert(err, IsNil)
	c.Assert(covCCHasVaultSlash(res), Equals, false)

	stored, err := k.GetTxOut(ctx, ctx.BlockHeight())
	c.Assert(err, IsNil)
	c.Assert(stored.TxArray[0].OutHash.Equals(f.outHash), Equals, true)

	gas := mgr.GasMgr().GetGas()
	c.Assert(len(gas), Equals, before+1)
	c.Assert(gas[len(gas)-1].Amount.Uint64(), Equals, uint64(500))
}

func (s *CovCCCommonOutboundSuite) TestGasAssetInactiveVaultNoRefund(c *C) {
	ctx, mgr := setupManagerForTest(c)
	k := mgr.Keeper()
	f := covCCSetupOutbound(c, ctx, k, common.BTCChain,
		common.NewCoin(common.BTCAsset, cosmos.NewUint(common.One)),
		common.NewCoin(common.BTCAsset, cosmos.NewUint(1000)), InactiveVault)
	obs := f.observed(ctx,
		common.NewCoins(common.NewCoin(common.BTCAsset, cosmos.NewUint(common.One+500))),
		common.Gas{common.NewCoin(common.BTCAsset, cosmos.NewUint(500))})

	before := len(mgr.GasMgr().GetGas())
	res, err := NewCommonOutboundTxHandler(mgr).handle(ctx, obs, f.inHash)
	c.Assert(err, IsNil)
	c.Assert(covCCHasVaultSlash(res), Equals, false)
	c.Assert(len(mgr.GasMgr().GetGas()), Equals, before)
}

func (s *CovCCCommonOutboundSuite) TestGasAssetOverspendSlashes(c *C) {
	ctx, mgr := setupManagerForTest(c)
	k := mgr.Keeper()
	// coalesce outbounds into multiples of 7 blocks: search starts at the next multiple
	k.SetMimir(ctx, constants.RescheduleCoalesceBlocks.String(), 7)
	f := covCCSetupOutbound(c, ctx, k, common.BTCChain,
		common.NewCoin(common.BTCAsset, cosmos.NewUint(common.One)),
		common.NewCoin(common.BTCAsset, cosmos.NewUint(500)), ActiveVault)

	// total spend equals intent but gas exceeded MaxGas
	obs := f.observed(ctx,
		common.NewCoins(common.NewCoin(common.BTCAsset, cosmos.NewUint(common.One-500))),
		common.Gas{common.NewCoin(common.BTCAsset, cosmos.NewUint(1000))})
	res, err := NewCommonOutboundTxHandler(mgr).handle(ctx, obs, f.inHash)
	c.Assert(err, IsNil)
	c.Assert(covCCHasVaultSlash(res), Equals, true)

	stored, err := k.GetTxOut(ctx, ctx.BlockHeight())
	c.Assert(err, IsNil)
	c.Assert(stored.TxArray[0].OutHash.IsEmpty(), Equals, true)

	// a failing slasher surfaces as an error
	ctx2, k2 := setupKeeperForTest(c)
	dmgr := NewDummyMgrWithKeeper(k2)
	f2 := covCCSetupOutbound(c, ctx2, k2, common.BTCChain,
		common.NewCoin(common.BTCAsset, cosmos.NewUint(common.One)),
		common.NewCoin(common.BTCAsset, cosmos.NewUint(500)), ActiveVault)
	obs2 := f2.observed(ctx2,
		common.NewCoins(common.NewCoin(common.BTCAsset, cosmos.NewUint(common.One-500))),
		common.Gas{common.NewCoin(common.BTCAsset, cosmos.NewUint(1000))})
	res, err = NewCommonOutboundTxHandler(dmgr).handle(ctx2, obs2, f2.inHash)
	c.Assert(err, NotNil)
	c.Assert(res, IsNil)
	c.Assert(err.Error(), Matches, "(?s)fail to slash account.*")
}

func (s *CovCCCommonOutboundSuite) TestEVMAggregatorAndCloutReclaim(c *C) {
	ctx, k := setupKeeperForTest(c)
	mgr := NewDummyMgrWithKeeper(k)
	f := covCCSetupOutbound(c, ctx, k, common.ETHChain,
		common.NewCoin(common.ETHAsset, cosmos.NewUint(common.One)),
		common.NewCoin(common.ETHAsset, cosmos.NewUint(1000)), ActiveVault)

	// rewrite the scheduled item with an aggregator and clout spent
	spent := cosmos.NewUint(40)
	txOut, err := k.GetTxOut(ctx, ctx.BlockHeight())
	c.Assert(err, IsNil)
	txOut.TxArray[0].Aggregator = "0xAGG"
	txOut.TxArray[0].AggregatorTargetAsset = "0xTARGET"
	txOut.TxArray[0].CloutSpent = &spent
	c.Assert(k.SetTxOut(ctx, txOut), IsNil)

	// inbound voter with a from address holding spent clout
	fromAddr := GetRandomETHAddress()
	voter, err := k.GetObservedTxInVoter(ctx, f.inHash)
	c.Assert(err, IsNil)
	inTx := GetRandomTx()
	inTx.FromAddress = fromAddr
	voter.Tx = common.ObservedTx{Tx: inTx}
	k.SetObservedTxInVoter(ctx, voter)

	cloutIn := NewSwapperClout(fromAddr)
	cloutIn.Score = cosmos.NewUint(100)
	cloutIn.Spent = cosmos.NewUint(30)
	c.Assert(k.SetSwapperClout(ctx, cloutIn), IsNil)
	cloutOut := NewSwapperClout(f.toAddr)
	cloutOut.Score = cosmos.NewUint(100)
	cloutOut.Spent = cosmos.NewUint(30)
	c.Assert(k.SetSwapperClout(ctx, cloutOut), IsNil)

	obs := f.observed(ctx,
		common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(common.One))),
		common.Gas{common.NewCoin(common.ETHAsset, cosmos.NewUint(1000))})
	obs.Aggregator = "0xagg"
	obs.AggregatorTarget = "0xtarget"

	res, err := NewCommonOutboundTxHandler(mgr).handle(ctx, obs, f.inHash)
	c.Assert(err, IsNil)
	c.Assert(covCCHasVaultSlash(res), Equals, false)

	stored, err := k.GetTxOut(ctx, ctx.BlockHeight())
	c.Assert(err, IsNil)
	c.Assert(stored.TxArray[0].OutHash.Equals(f.outHash), Equals, true)

	gotIn, err := k.GetSwapperClout(ctx, fromAddr)
	c.Assert(err, IsNil)
	gotOut, err := k.GetSwapperClout(ctx, f.toAddr)
	c.Assert(err, IsNil)
	// 40 spent clout reclaimed evenly (20/20)
	c.Assert(gotIn.Reclaimed.Uint64(), Equals, uint64(20))
	c.Assert(gotOut.Reclaimed.Uint64(), Equals, uint64(20))
	c.Assert(gotIn.LastReclaimHeight, Equals, ctx.BlockHeight())
}

func (s *CovCCCommonOutboundSuite) TestCloutReclaimSameAddress(c *C) {
	ctx, k := setupKeeperForTest(c)
	mgr := NewDummyMgrWithKeeper(k)
	f := covCCSetupOutbound(c, ctx, k, common.BTCChain,
		common.NewCoin(common.BTCAsset, cosmos.NewUint(common.One)),
		common.NewCoin(common.BTCAsset, cosmos.NewUint(1000)), ActiveVault)

	spent := cosmos.NewUint(40)
	txOut, err := k.GetTxOut(ctx, ctx.BlockHeight())
	c.Assert(err, IsNil)
	txOut.TxArray[0].CloutSpent = &spent
	c.Assert(k.SetTxOut(ctx, txOut), IsNil)

	// swapper sent from the same address that receives the outbound
	voter, err := k.GetObservedTxInVoter(ctx, f.inHash)
	c.Assert(err, IsNil)
	inTx := GetRandomTx()
	inTx.FromAddress = f.toAddr
	voter.Tx = common.ObservedTx{Tx: inTx}
	k.SetObservedTxInVoter(ctx, voter)

	clout := NewSwapperClout(f.toAddr)
	clout.Score = cosmos.NewUint(100)
	clout.Spent = cosmos.NewUint(60)
	c.Assert(k.SetSwapperClout(ctx, clout), IsNil)

	obs := f.observed(ctx,
		common.NewCoins(common.NewCoin(common.BTCAsset, cosmos.NewUint(common.One))),
		common.Gas{common.NewCoin(common.BTCAsset, cosmos.NewUint(1000))})
	_, err = NewCommonOutboundTxHandler(mgr).handle(ctx, obs, f.inHash)
	c.Assert(err, IsNil)

	got, err := k.GetSwapperClout(ctx, f.toAddr)
	c.Assert(err, IsNil)
	c.Assert(got.Reclaimed.Uint64(), Equals, uint64(40))
}
