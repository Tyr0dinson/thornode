package types

import (
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	. "gopkg.in/check.v1"
)

type MsgSolvencyTestSuite struct{}

var _ = Suite(&MsgSolvencyTestSuite{})

func (MsgSolvencyTestSuite) TestMsgSolvency(c *C) {
	pubKey := GetRandomPubKey()
	height := int64(1024)
	signer := GetRandomBech32Addr()
	coins := common.NewCoins(
		common.NewCoin(common.BTCAsset, cosmos.NewUint(1024)),
	)
	testCases := []struct {
		name      string
		chain     common.Chain
		pubKey    common.PubKey
		coins     common.Coins
		height    int64
		signer    cosmos.AccAddress
		expectErr bool
	}{
		{
			name:      "empty chain should fail",
			chain:     common.EmptyChain,
			pubKey:    pubKey,
			coins:     coins,
			height:    height,
			signer:    signer,
			expectErr: true,
		},
		{
			name:      "empty pubkey should fail",
			chain:     common.BTCChain,
			pubKey:    common.EmptyPubKey,
			coins:     coins,
			height:    height,
			signer:    signer,
			expectErr: true,
		},
		{
			name:      "empty coins should fail",
			chain:     common.BTCChain,
			pubKey:    pubKey,
			coins:     common.NewCoins(),
			height:    height,
			signer:    signer,
			expectErr: false,
		},
		{
			name:      "invalid height should fail",
			chain:     common.BTCChain,
			pubKey:    pubKey,
			coins:     coins,
			height:    0,
			signer:    signer,
			expectErr: true,
		},
		{
			name:      "invalid signer should fail",
			chain:     common.BTCChain,
			pubKey:    pubKey,
			coins:     coins,
			height:    height,
			signer:    cosmos.AccAddress{},
			expectErr: true,
		},
		{
			chain:     common.BTCChain,
			pubKey:    pubKey,
			coins:     coins,
			height:    height,
			signer:    signer,
			expectErr: false,
		},
	}
	for _, tc := range testCases {
		msg, err := NewMsgSolvency(tc.chain, tc.pubKey, tc.coins, tc.height, tc.signer)
		c.Assert(err, IsNil)
		err = msg.ValidateBasic()
		if tc.expectErr {
			c.Assert(err, NotNil, Commentf("name:%s"))
		} else {
			EnsureMsgBasicCorrect(msg, c)
		}
	}
}

func (MsgSolvencyTestSuite) TestEmptyPendingSolvencyJSON(c *C) {
	msg, err := NewMsgSolvency(common.BTCChain, GetRandomPubKey(), nil, 1000, GetRandomBech32Addr())
	c.Assert(err, IsNil)
	bz, err := ModuleCdc.MarshalJSON(msg)
	c.Assert(err, IsNil)
	c.Assert(string(bz), Not(Matches), ".*pending.*")
	voter := NewSolvencyVoter(msg.Id, msg.Chain, msg.PubKey, msg.Coins, msg.Height)
	bz, err = ModuleCdc.MarshalJSON(voter)
	c.Assert(err, IsNil)
	c.Assert(string(bz), Not(Matches), ".*pending.*")
}
