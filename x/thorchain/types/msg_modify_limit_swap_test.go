package types

import (
	"errors"

	se "github.com/cosmos/cosmos-sdk/types/errors"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

type MsgModifyLimitSwapSuite struct{}

var _ = Suite(&MsgModifyLimitSwapSuite{})

func (MsgModifyLimitSwapSuite) TestMsgModifyLimitSwapConstructor(c *C) {
	// Setup for tests
	SetupConfigForTest()

	from := GetRandomBTCAddress()
	source := common.NewCoin(common.BTCAsset, cosmos.NewUint(100*common.One))
	target := common.NewCoin(common.ETHAsset, cosmos.NewUint(50*common.One))
	modifiedAmount := cosmos.NewUint(60 * common.One)
	signer := GetRandomBech32Addr()

	// Test constructor
	msg := NewMsgModifyLimitSwap(from, source, target, modifiedAmount, signer)

	// Verify all fields are set correctly
	c.Assert(msg.From.Equals(from), Equals, true)
	c.Assert(msg.Source.Equals(source), Equals, true)
	c.Assert(msg.Target.Equals(target), Equals, true)
	c.Assert(msg.ModifiedTargetAmount.Equal(modifiedAmount), Equals, true)
	c.Assert(msg.Signer.Equals(signer), Equals, true)
}

func (MsgModifyLimitSwapSuite) TestMsgModifyLimitSwapValidateBasic(c *C) {
	// Setup for tests
	SetupConfigForTest()

	// Test valid message
	from := GetRandomBTCAddress()
	source := common.NewCoin(common.BTCAsset, cosmos.NewUint(100*common.One))
	target := common.NewCoin(common.ETHAsset, cosmos.NewUint(50*common.One))
	modifiedAmount := cosmos.NewUint(60 * common.One)
	signer := GetRandomBech32Addr()

	msg := NewMsgModifyLimitSwap(from, source, target, modifiedAmount, signer)
	c.Assert(msg.ValidateBasic(), IsNil)

	// Test invalid cases
	testCases := []struct {
		name        string
		from        common.Address
		source      common.Coin
		target      common.Coin
		modAmount   cosmos.Uint
		signer      cosmos.AccAddress
		expectedErr error
	}{
		{
			name:        "empty signer",
			from:        from,
			source:      source,
			target:      target,
			modAmount:   modifiedAmount,
			signer:      cosmos.AccAddress{},
			expectedErr: se.ErrInvalidAddress,
		},
		{
			name:        "invalid source asset",
			from:        from,
			source:      common.NewCoin(common.EmptyAsset, cosmos.NewUint(100*common.One)),
			target:      target,
			modAmount:   modifiedAmount,
			signer:      signer,
			expectedErr: se.ErrUnknownRequest,
		},
		{
			name:        "invalid target asset",
			from:        from,
			source:      source,
			target:      common.NewCoin(common.EmptyAsset, cosmos.NewUint(50*common.One)),
			modAmount:   modifiedAmount,
			signer:      signer,
			expectedErr: se.ErrUnknownRequest,
		},
		{
			name:        "same source and target asset",
			from:        from,
			source:      common.NewCoin(common.BTCAsset, cosmos.NewUint(100*common.One)),
			target:      common.NewCoin(common.BTCAsset, cosmos.NewUint(50*common.One)),
			modAmount:   modifiedAmount,
			signer:      signer,
			expectedErr: se.ErrUnknownRequest,
		},
		{
			name:        "from address not matching source asset chain",
			from:        GetRandomETHAddress(),
			source:      common.NewCoin(common.BTCAsset, cosmos.NewUint(100*common.One)),
			target:      common.NewCoin(common.ETHAsset, cosmos.NewUint(50*common.One)),
			modAmount:   modifiedAmount,
			signer:      signer,
			expectedErr: se.ErrUnknownRequest,
		},
	}

	for _, tc := range testCases {
		c.Logf("Test case: %s", tc.name)
		message := NewMsgModifyLimitSwap(tc.from, tc.source, tc.target, tc.modAmount, tc.signer)
		err := message.ValidateBasic()
		c.Assert(err, NotNil)
		c.Assert(errors.Is(err, tc.expectedErr), Equals, true, Commentf("Expected: %v, got: %v", tc.expectedErr, err))
	}
}

func (MsgModifyLimitSwapSuite) TestMsgModifyLimitSwapGetSigners(c *C) {
	// Setup for tests
	SetupConfigForTest()

	from := GetRandomBTCAddress()
	source := common.NewCoin(common.BTCAsset, cosmos.NewUint(100*common.One))
	target := common.NewCoin(common.ETHAsset, cosmos.NewUint(50*common.One))
	modifiedAmount := cosmos.NewUint(60 * common.One)
	signer := GetRandomBech32Addr()

	// Test GetSigners
	msg := NewMsgModifyLimitSwap(from, source, target, modifiedAmount, signer)
	signers := msg.GetSigners()

	c.Assert(len(signers), Equals, 1)
	c.Assert(signers[0].String(), Equals, signer.String())
	c.Assert(signers[0].Equals(signer), Equals, true)
}

func (MsgModifyLimitSwapSuite) TestMsgModifyLimitSwapWithZeroAmount(c *C) {
	// Setup for tests
	SetupConfigForTest()

	from := GetRandomBTCAddress()
	source := common.NewCoin(common.BTCAsset, cosmos.NewUint(100*common.One))
	target := common.NewCoin(common.ETHAsset, cosmos.NewUint(50*common.One))
	zeroAmount := cosmos.ZeroUint()
	signer := GetRandomBech32Addr()

	// Test with zero amount (should still be valid as it might be used for cancellation)
	msg := NewMsgModifyLimitSwap(from, source, target, zeroAmount, signer)
	c.Assert(msg.ValidateBasic(), IsNil)
	c.Assert(msg.ModifiedTargetAmount.Equal(zeroAmount), Equals, true)
}

func (MsgModifyLimitSwapSuite) TestMsgModifyLimitSwapWithDifferentChains(c *C) {
	// Setup for tests
	SetupConfigForTest()

	// Test with different chain combinations
	testCases := []struct {
		name      string
		from      common.Address
		source    common.Coin
		target    common.Coin
		modAmount cosmos.Uint
		signer    cosmos.AccAddress
		valid     bool
	}{
		{
			name:      "BTC to ETH",
			from:      GetRandomBTCAddress(),
			source:    common.NewCoin(common.BTCAsset, cosmos.NewUint(100*common.One)),
			target:    common.NewCoin(common.ETHAsset, cosmos.NewUint(50*common.One)),
			modAmount: cosmos.NewUint(60 * common.One),
			signer:    GetRandomBech32Addr(),
			valid:     true,
		},
		{
			name:      "ETH to BTC",
			from:      GetRandomETHAddress(),
			source:    common.NewCoin(common.ETHAsset, cosmos.NewUint(100*common.One)),
			target:    common.NewCoin(common.BTCAsset, cosmos.NewUint(50*common.One)),
			modAmount: cosmos.NewUint(60 * common.One),
			signer:    GetRandomBech32Addr(),
			valid:     true,
		},
		{
			name:      "BTC to LTC",
			from:      GetRandomBTCAddress(),
			source:    common.NewCoin(common.BTCAsset, cosmos.NewUint(100*common.One)),
			target:    common.NewCoin(common.LTCAsset, cosmos.NewUint(50*common.One)),
			modAmount: cosmos.NewUint(60 * common.One),
			signer:    GetRandomBech32Addr(),
			valid:     true,
		},
		{
			name:      "DOGE to BCH",
			from:      GetRandomDOGEAddress(),
			source:    common.NewCoin(common.DOGEAsset, cosmos.NewUint(100*common.One)),
			target:    common.NewCoin(common.BCHAsset, cosmos.NewUint(50*common.One)),
			modAmount: cosmos.NewUint(60 * common.One),
			signer:    GetRandomBech32Addr(),
			valid:     true,
		},
	}

	for _, tc := range testCases {
		c.Logf("Test case: %s", tc.name)
		msg := NewMsgModifyLimitSwap(tc.from, tc.source, tc.target, tc.modAmount, tc.signer)
		err := msg.ValidateBasic()
		if tc.valid {
			c.Assert(err, IsNil, Commentf("Expected valid message but got error: %v", err))
		} else {
			c.Assert(err, NotNil, Commentf("Expected invalid message but got no error"))
		}
	}
}
