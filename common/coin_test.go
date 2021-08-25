package common

import (
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/common/cosmos"
)

type CoinSuite struct{}

var _ = Suite(&CoinSuite{})

func (s CoinSuite) TestCoin(c *C) {
	coin := NewCoin(BNBAsset, cosmos.NewUint(230000000))
	c.Check(coin.Asset.Equals(BNBAsset), Equals, true)
	c.Check(coin.Amount.Uint64(), Equals, uint64(230000000))
	c.Check(coin.Valid(), IsNil)
	c.Check(coin.IsEmpty(), Equals, false)
	c.Check(NoCoin.IsEmpty(), Equals, true)

	c.Check(coin.IsNative(), Equals, false)
	_, err := coin.Native()
	c.Assert(err, NotNil)
	coin = NewCoin(RuneNative, cosmos.NewUint(230))
	c.Check(coin.IsNative(), Equals, true)
	sdkCoin, err := coin.Native()
	c.Assert(err, IsNil)
	c.Check(sdkCoin.Denom, Equals, "rune")
	c.Check(sdkCoin.Amount.Equal(cosmos.NewInt(230)), Equals, true)
}

func (s CoinSuite) TestDistinct(c *C) {
	coins := Coins{
		NewCoin(BNBAsset, cosmos.NewUint(1000)),
		NewCoin(BNBAsset, cosmos.NewUint(1000)),
		NewCoin(BTCAsset, cosmos.NewUint(1000)),
		NewCoin(BTCAsset, cosmos.NewUint(1000)),
	}
	newCoins := coins.Distinct()
	c.Assert(len(newCoins), Equals, 2)
}

func (s CoinSuite) TestAdds(c *C) {
	coins := Coins{
		NewCoin(BNBAsset, cosmos.NewUint(1000)),
	}
	newCoins := coins.Adds(NewCoins(
		NewCoin(BNBAsset, cosmos.NewUint(1000)),
		NewCoin(BTCAsset, cosmos.NewUint(1000)),
	))
	c.Assert(len(newCoins), Equals, 2)
	c.Check(newCoins.GetCoin(BNBAsset).Amount.Uint64(), Equals, uint64(2000))
	c.Check(newCoins.GetCoin(BTCAsset).Amount.Uint64(), Equals, uint64(1000))
}

func (s CoinSuite) TestNoneEmpty(c *C) {
	coins := Coins{
		NewCoin(BNBAsset, cosmos.NewUint(1000)),
		NewCoin(ETHAsset, cosmos.ZeroUint()),
	}
	newCoins := coins.NoneEmpty()
	c.Assert(newCoins, HasLen, 1)
	ethCoin := newCoins.GetCoin(ETHAsset)
	c.Assert(ethCoin.IsEmpty(), Equals, true)
}

func (s CoinSuite) TestHasSynthetic(c *C) {
	bnbSynthAsset, _ := NewAsset("BNB/BNB")
	coins := Coins{
		NewCoin(bnbSynthAsset, cosmos.NewUint(1000)),
		NewCoin(ETHAsset, cosmos.ZeroUint()),
	}
	c.Assert(coins.HasSynthetic(), Equals, true)
	coins = Coins{
		NewCoin(BNBAsset, cosmos.NewUint(1000)),
		NewCoin(ETHAsset, cosmos.ZeroUint()),
	}
	c.Assert(coins.HasSynthetic(), Equals, false)
}
func (s CoinSuite) TestCoinsEqual(c *C) {
	assetETH, _ := NewAsset("ETH.ETH")
	assetSNX, _ := NewAsset("ETH.SNX-0XC011A73EE8576FB46F5E1C5751CA3B9FE0AF2A6F")
	assetSUSHI, _ := NewAsset("ETH.SUSHI-0X6B3595068778DD592E39A122F4F5A5CF09C90FE2")
	assetUSDT, _ := NewAsset("ETH.USDT-0XDAC17F958D2EE523A2206206994597C13D831EC7")
	coins := NewCoins(
		NewCoin(assetSUSHI, cosmos.NewUint(256817616172)),
		NewCoin(assetSNX, cosmos.NewUint(45667819317)),
		NewCoin(assetUSDT, cosmos.NewUint(2453815652300)),
		NewCoin(assetETH, cosmos.NewUint(1990254385)),
	)

	coins1 := coins
	theSame := coins.EqualsEx(coins1)
	c.Assert(theSame, Equals, true)
}
