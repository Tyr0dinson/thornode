package thorchain

import (
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/common"
	"gitlab.com/thorchain/thornode/common/cosmos"
)

type HandlerUpdateLpRecordSuite struct{}

var _ = Suite(&HandlerUpdateLpRecordSuite{})

func (HandlerUpdateLpRecordSuite) TestHandlerUpdateLpRecord_Validate(c *C) {
	lpDoubleSided := LiquidityProvider{
		Asset:             common.BNBAsset,
		AssetAddress:      GetRandomBNBAddress(),
		RuneAddress:       GetRandomTHORAddress(),
		AssetDepositValue: cosmos.NewUint(100),
		RuneDepositValue:  cosmos.NewUint(100),
	}

	singleSidedAsset := LiquidityProvider{
		Asset:             common.BTCAsset,
		AssetAddress:      GetRandomBTCAddress(),
		RuneAddress:       common.NoAddress,
		AssetDepositValue: cosmos.NewUint(100),
		RuneDepositValue:  cosmos.NewUint(100),
	}

	singleSidedRune := LiquidityProvider{
		Asset:             common.BNBAsset,
		AssetAddress:      common.NoAddress,
		RuneAddress:       GetRandomTHORAddress(),
		AssetDepositValue: cosmos.NewUint(100),
		RuneDepositValue:  cosmos.NewUint(100),
	}

	bnbPool := Pool{
		Asset:        common.BNBAsset,
		Status:       PoolAvailable,
		BalanceAsset: cosmos.NewUint(100),
		BalanceRune:  cosmos.NewUint(100),
		LPUnits:      cosmos.NewUint(100),
	}

	btcPool := Pool{
		Asset:        common.BTCAsset,
		Status:       PoolAvailable,
		BalanceAsset: cosmos.NewUint(100),
		BalanceRune:  cosmos.NewUint(100),
		LPUnits:      cosmos.NewUint(100),
	}

	ctx, mgr := setupManagerForTest(c)
	ctx, k := setupKeeperForTest(c)
	mgr.K = k
	k.SetLiquidityProvider(ctx, lpDoubleSided)
	k.SetLiquidityProvider(ctx, singleSidedAsset)
	k.SetLiquidityProvider(ctx, singleSidedRune)

	c.Assert(k.SetPool(ctx, bnbPool), IsNil)
	c.Assert(k.SetPool(ctx, btcPool), IsNil)
	handler := NewUpdateLPRecordHandler(mgr)

	bnbTxIn := common.NewTx(
		GetRandomTxHash(),
		lpDoubleSided.AssetAddress,
		GetRandomBNBAddress(),
		common.Coins{
			common.NewCoin(common.BNBAsset, cosmos.NewUint(uint64(0))),
		},
		BNBGasFeeSingleton,
		"updatelp:bnb.bnb:abc",
	)
	btcTxIn := common.NewTx(
		GetRandomTxHash(),
		GetRandomBTCAddress(),
		GetRandomBTCAddress(),
		common.Coins{
			common.NewCoin(common.BTCAsset, cosmos.NewUint(uint64(0))),
		},
		common.Gas{
			common.NewCoin(common.BTCAsset, cosmos.NewUint(uint64(1))),
		},
		"updatelp:btc.btc:abc",
	)

	// Chain halted, can't update
	mgr.Keeper().SetMimir(ctx, "HaltBNBChain", 1)
	msg := NewMsgUpdateLpRecord(bnbTxIn, common.BNBAsset, GetRandomBNBAddress(), GetRandomBech32Addr())
	err := handler.validate(ctx, *msg)
	c.Assert(err.Error(), Equals, "unable to update LP record while chain is halted")
	mgr.Keeper().SetMimir(ctx, "HaltBNBChain", 0)

	// Feature not enabled, can't update
	msg = NewMsgUpdateLpRecord(bnbTxIn, common.BNBAsset, GetRandomBNBAddress(), GetRandomBech32Addr())
	err = handler.validate(ctx, *msg)
	c.Assert(err.Error(), Equals, "update memo not enabled for this chain")

	msg = NewMsgUpdateLpRecord(btcTxIn, common.BTCAsset, GetRandomBTCAddress(), GetRandomBech32Addr())
	err = handler.validate(ctx, *msg)
	c.Assert(err.Error(), Equals, "update memo not enabled for this chain")
	mgr.Keeper().SetMimir(ctx, "EnableUpdateMemoBTC", 1)
	mgr.Keeper().SetMimir(ctx, "EnableUpdateMemoBNB", 1)

	// Can't request with synthetic asset
	msg = NewMsgUpdateLpRecord(bnbTxIn, common.BNBAsset.GetSyntheticAsset(), GetRandomBNBAddress(), GetRandomBech32Addr())
	err = handler.validate(ctx, *msg)
	c.Assert(err.Error(), Equals, errUpdateLpRecordFailValidation.Error())

	// Requester has no LP units
	bnbTxIn.FromAddress = GetRandomBNBAddress()
	msg = NewMsgUpdateLpRecord(bnbTxIn, common.BNBAsset, GetRandomBNBAddress(), GetRandomBech32Addr())
	err = handler.validate(ctx, *msg)
	c.Assert(err.Error(), Equals, "LP record has no units, skipping")
}
