package types

import (
	"cosmossdk.io/math"
	"fmt"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

type MsgExportAssetBindSuite struct{}

var _ = Suite(&MsgExportAssetBindSuite{})

func (MsgExportAssetBindSuite) TestMsgExportAssetBind(c *C) {
	signer := GetRandomBech32Addr()

	fmt.Println(common.AssetExports)

	testCases := []struct {
		Note        string
		Chain       common.Chain
		Coins       []string
		Asset       string
		AssetAmount math.Uint
		FeeAmount   math.Uint
		Signer      cosmos.AccAddress
		Error       string
	}{
		{
			Note:  "bind ETH asset",
			Chain: common.ETHChain,
			Coins: []string{
				"123 ETH.RUJI",
			},
			Asset:       "ETH.RUJI",
			AssetAmount: math.NewUint(123),
			FeeAmount:   math.NewUint(0),
			Signer:      signer,
		},
		{
			Note:  "fund fees from ETH",
			Chain: common.ETHChain,
			Coins: []string{
				"123 ETH.ETH",
			},
			Asset:       "ETH.RUJI",
			AssetAmount: math.NewUint(0),
			FeeAmount:   math.NewUint(123),
			Signer:      signer,
		},
		{
			Note:  "bind and fund ETH asset",
			Chain: common.ETHChain,
			Coins: []string{
				"123 ETH.RUJI",
				"456 ETH.ETH",
			},
			Asset:       "ETH.RUJI",
			AssetAmount: math.NewUint(123),
			FeeAmount:   math.NewUint(456),
			Signer:      signer,
		},

		{
			Note:  "bind BSC asset",
			Chain: common.BSCChain,
			Coins: []string{
				"123 BSC.RUJI",
			},
			Asset:       "BSC.RUJI",
			AssetAmount: math.NewUint(123),
			FeeAmount:   math.NewUint(0),
			Signer:      signer,
		},

		{
			Note:  "bind THOR asset",
			Chain: common.THORChain,
			Coins: []string{
				"123 THOR.RUJI",
			},
			Asset:       "THOR.RUJI",
			AssetAmount: math.NewUint(123),
			FeeAmount:   math.NewUint(0),
			Signer:      signer,
		},
		{
			Note:  "fund fees from THOR",
			Chain: common.THORChain,
			Coins: []string{
				"123 THOR.RUNE",
			},
			Asset:       "THOR.RUJI",
			AssetAmount: math.NewUint(0),
			FeeAmount:   math.NewUint(123),
			Signer:      signer,
		},
		{
			Note:  "bind and fund THOR asset",
			Chain: common.THORChain,
			Coins: []string{
				"123 THOR.RUJI",
				"456 THOR.RUNE",
			},
			Asset:       "THOR.RUJI",
			AssetAmount: math.NewUint(123),
			FeeAmount:   math.NewUint(456),
			Signer:      signer,
		},

		{
			Note:  "bind unknown THOR asset",
			Chain: common.THORChain,
			Coins: []string{
				"123 THOR.BTC",
			},
			Asset:       "THOR.BTC",
			AssetAmount: math.NewUint(123),
			FeeAmount:   math.NewUint(0),
			Signer:      signer,
			Error:       "unknown asset.*",
		},
		{
			Note:  "bind from unsupported chain",
			Chain: common.BASEChain,
			Coins: []string{
				"123 BASE.RUJI",
			},
			Asset:       "BASE.RUJI",
			AssetAmount: math.NewUint(123),
			FeeAmount:   math.NewUint(0),
			Signer:      signer,
			Error:       "chain not supported.*",
		},
		{
			Note:  "bind unknown ETH asset",
			Chain: common.ETHChain,
			Coins: []string{
				"123 ETH.BTC",
			},
			Asset:       "ETH.BTC",
			AssetAmount: math.NewUint(123),
			FeeAmount:   math.NewUint(0),
			Signer:      signer,
			Error:       "unknown asset.*",
		},

		{
			Note:        "empty coins, empty amounts",
			Chain:       common.ETHChain,
			Coins:       []string{},
			Asset:       "ETH.RUJI",
			AssetAmount: math.NewUint(0),
			FeeAmount:   math.NewUint(0),
			Signer:      signer,
			Error:       "fee and asset amounts are zero.*",
		},
		{
			Note:        "empty coins, set amounts",
			Chain:       common.ETHChain,
			Coins:       []string{},
			Asset:       "ETH.RUJI",
			AssetAmount: math.NewUint(123),
			FeeAmount:   math.NewUint(456),
			Signer:      signer,
			Error:       ".*and amount dont match.*",
		},
		{
			Note:  "fee coins and amounts don't match",
			Chain: common.ETHChain,
			Coins: []string{
				"1 ETH.ETH",
			},
			Asset:       "ETH.RUJI",
			AssetAmount: math.NewUint(0),
			FeeAmount:   math.NewUint(456),
			Signer:      signer,
			Error:       "fee coin and amount dont match.*",
		},
		{
			Note:  "asset coins and amounts don't match",
			Chain: common.ETHChain,
			Coins: []string{
				"1 ETH.RUJI",
			},
			Asset:       "ETH.RUJI",
			AssetAmount: math.NewUint(123),
			FeeAmount:   math.NewUint(0),
			Signer:      signer,
			Error:       "asset coin and amount dont match.*",
		},
	}

	for _, tc := range testCases {
		coins := common.Coins{}

		for _, coin := range tc.Coins {
			coin, err := common.ParseCoin(coin)
			c.Assert(err, IsNil)
			coins = append(coins, coin)
		}

		tx := GetRandomTx()
		tx.Chain = tc.Chain
		tx.Coins = coins

		asset, err := common.NewAsset(tc.Asset)
		c.Assert(err, IsNil)

		msg := NewMsgExportAssetBind(
			tx, asset, tc.AssetAmount, tc.FeeAmount, signer,
		)

		fmt.Println(tc.Note)
		fmt.Println(coins)
		fmt.Println(msg)

		if len(tc.Error) == 0 {
			c.Assert(msg.ValidateBasic(), IsNil)
		} else {
			err = msg.ValidateBasic()
			fmt.Printf("error: %v\n", err)
			c.Assert(err, NotNil)
			c.Assert(err, ErrorMatches, tc.Error)
		}
	}
}
