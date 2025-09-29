package types

import (
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	. "gopkg.in/check.v1"
)

type MsgMoveLiquiditySuite struct{}

var _ = Suite(&MsgMoveLiquiditySuite{})

func (MsgMoveLiquiditySuite) TestValidateBasic(c *C) {
	runeAddress := GetRandomRUNEAddress()
	btcAddress := GetRandomBTCAddress()

	testCases := []struct {
		Asset     common.Asset
		RuneAddr  common.Address
		AssetAddr common.Address
		Amount    cosmos.Uint
		Signer    cosmos.AccAddress
		Error     string
	}{
		{
			// No asset => Fail
			Asset:     common.EmptyAsset,
			RuneAddr:  common.NoAddress,
			AssetAddr: common.NoAddress,
			Amount:    cosmos.ZeroUint(),
			Signer:    GetRandomBech32Addr(),
			Error:     "asset cannot be empty.*",
		},
		{
			// No address => Fail
			Asset:  common.BTCAsset,
			Amount: cosmos.ZeroUint(),
			Signer: GetRandomBech32Addr(),
			Error:  "no address provided.*",
		},
		{
			// No signer => Fail
			Asset:     common.ETHAsset,
			RuneAddr:  common.NoAddress,
			AssetAddr: GetRandomETHAddress(),
			Amount:    cosmos.ZeroUint(),
			Error:     ".*invalid address.*",
		},
		{
			// No amount => OK
			Asset:     common.BTCAsset,
			RuneAddr:  GetRandomRUNEAddress(),
			AssetAddr: common.NoAddress,
			Signer:    GetRandomBech32Addr(),
		},
		{
			// No Asset address, valid Rune address => OK
			Asset:    common.BTCAsset,
			RuneAddr: GetRandomRUNEAddress(),
			Amount:   cosmos.ZeroUint(),
			Signer:   GetRandomBech32Addr(),
		},
		{
			// No Rune address, valid Asset address => OK
			Asset:     common.BTCAsset,
			AssetAddr: GetRandomBTCAddress(),
			Amount:    cosmos.NewUint(123),
			Signer:    GetRandomBech32Addr(),
		},
		{
			// Valid Rune & Asset address => OK
			Asset:     common.BTCAsset,
			RuneAddr:  GetRandomRUNEAddress(),
			AssetAddr: GetRandomBTCAddress(),
			Amount:    cosmos.NewUint(2726252524),
			Signer:    GetRandomBech32Addr(),
		},
		{
			// Two different Rune addresses, non Rune Asset => fail
			Asset:     common.BTCAsset,
			RuneAddr:  GetRandomRUNEAddress(),
			AssetAddr: GetRandomRUNEAddress(),
			Amount:    cosmos.NewUint(1),
			Signer:    GetRandomBech32Addr(),
			Error:     ".*does not match asset chain.*",
		},
		{
			// Two different Rune addresses, Rune Asset => fail
			Asset:     common.RUJI,
			RuneAddr:  GetRandomRUNEAddress(),
			AssetAddr: GetRandomRUNEAddress(),
			Amount:    cosmos.NewUint(1),
			Signer:    GetRandomBech32Addr(),
			Error:     ".*rune address does not match asset address.*",
		},
		{
			// Two Rune address, non Rune Asset => fail
			Asset:     common.BTCAsset,
			RuneAddr:  runeAddress,
			AssetAddr: runeAddress,
			Amount:    cosmos.NewUint(1),
			Signer:    GetRandomBech32Addr(),
			Error:     ".*does not match asset chain.*",
		},
		{
			// Two Rune addresses, Rune Asset => OK
			Asset:     common.RUJI,
			RuneAddr:  runeAddress,
			AssetAddr: runeAddress,
			Amount:    cosmos.NewUint(1),
			Signer:    GetRandomBech32Addr(),
		},
		{
			// Two Asset addresses => fail
			Asset:     common.BTCAsset,
			RuneAddr:  btcAddress,
			AssetAddr: btcAddress,
			Amount:    cosmos.NewUint(1),
			Signer:    GetRandomBech32Addr(),
			Error:     ".*does not match THORChain.*",
		},
		{
			// Wrong RUNE address => Fail
			Asset:    common.ETHAsset,
			RuneAddr: GetRandomBTCAddress(),
			Signer:   GetRandomBech32Addr(),
			Error:    ".*does not match THORChain.*",
		},
		{
			// Bogus RUNE address => Fail
			Asset:    common.ETHAsset,
			RuneAddr: "bogus",
			Signer:   GetRandomBech32Addr(),
			Error:    ".*does not match THORChain.*",
		},
		{
			// Wrong Asset address => Fail
			Asset:     common.ETHAsset,
			AssetAddr: GetRandomBTCAddress(),
			Signer:    GetRandomBech32Addr(),
			Error:     ".*does not match asset chain.*",
		},
		{
			// Bogus Asset address => Fail
			Asset:     common.ETHAsset,
			AssetAddr: "bogus",
			Signer:    GetRandomBech32Addr(),
			Error:     ".*does not match asset chain.*",
		},
	}

	for _, tc := range testCases {
		tx := GetRandomTx()
		tx.Coins = common.Coins{}

		msg := MsgMoveLiquidity{
			TxIn:            tx,
			Asset:           tc.Asset,
			NewAssetAddress: tc.AssetAddr,
			NewRuneAddress:  tc.RuneAddr,
			Amount:          tc.Amount,
			Signer:          tc.Signer,
		}

		err := msg.ValidateBasic()
		if tc.Error == "" {
			c.Assert(err, IsNil)
		} else {
			c.Assert(err, ErrorMatches, tc.Error)
		}
	}
}
