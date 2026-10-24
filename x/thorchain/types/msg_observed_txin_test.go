package types

import (
	"errors"
	"strings"

	se "github.com/cosmos/cosmos-sdk/types/errors"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	cosmos "gitlab.com/thorchain/thornode/v3/common/cosmos"
)

type MsgObservedTxInSuite struct{}

var _ = Suite(&MsgObservedTxInSuite{})

func (s *MsgObservedTxInSuite) TestRejectsXMRVaultAddressCaseVariant(c *C) {
	pk := getRandomXMRPubKeyForMsgTest(c)
	toAddress, err := pk.GetAddress(common.XMRChain)
	c.Assert(err, IsNil)
	fromAddress, err := common.NewAddress(common.XMRUnknownSenderAddress(GetRandomTxHash().String(), toAddress.String()))
	c.Assert(err, IsNil)
	tx := common.NewObservedTx(common.Tx{
		ID:          GetRandomTxHash(),
		Chain:       common.XMRChain,
		FromAddress: fromAddress,
		ToAddress:   common.Address(strings.ToUpper(toAddress.String())),
		Coins:       common.NewCoins(common.NewCoin(common.XMRAsset, cosmos.OneUint())),
		Gas:         common.Gas{common.NewCoin(common.XMRAsset, cosmos.OneUint())},
	}, 55, pk, 55)

	msg := NewMsgObservedTxIn(common.ObservedTxs{tx}, GetRandomBech32Addr())
	c.Assert(msg.ValidateBasic(), NotNil)
}

// solCaseVariantForMsgTest flips one lowercase letter of a SOL address to uppercase,
// keeping a valid address: base58 has no checksum, so it names another account.
func solCaseVariantForMsgTest(c *C, addr common.Address) common.Address {
	str := addr.String()
	for i := 0; i < len(str); i++ {
		ch := str[i]
		if ch < 'a' || ch > 'z' || ch == 'i' || ch == 'o' { // I and O are not base58
			continue
		}
		variant := str[:i] + string(ch-'a'+'A') + str[i+1:]
		if common.IsValidSOLAddress(variant) {
			return common.Address(variant)
		}
	}
	c.Fatalf("no case variant of %s", str)
	return common.NoAddress
}

// solObservedTxForMsgTest is a SOL transfer observed against vault pk.
func solObservedTxForMsgTest(pk common.PubKey, from, to common.Address) common.ObservedTx {
	return common.NewObservedTx(common.Tx{
		ID:          GetRandomTxHash(),
		Chain:       common.SOLChain,
		FromAddress: from,
		ToAddress:   to,
		Coins:       common.NewCoins(common.NewCoin(common.SOLAsset, cosmos.NewUint(100))),
		Gas:         common.Gas{common.NewCoin(common.SOLAsset, cosmos.OneUint())},
	}, 55, pk, 55)
}

// A SOL inbound must be to the observed vault's exact address: a case variant is
// another account, so funds sent to it never reached the vault.
func (s *MsgObservedTxInSuite) TestRejectsSOLVaultAddressCaseVariant(c *C) {
	pk := GetRandomEd25519PubKey()
	vaultAddress, err := pk.GetAddress(common.SOLChain)
	c.Assert(err, IsNil)
	sender, err := GetRandomEd25519PubKey().GetAddress(common.SOLChain)
	c.Assert(err, IsNil)

	exact := solObservedTxForMsgTest(pk, sender, vaultAddress)
	c.Assert(NewMsgObservedTxIn(common.ObservedTxs{exact}, GetRandomBech32Addr()).ValidateBasic(), IsNil)

	variant := solObservedTxForMsgTest(pk, sender, solCaseVariantForMsgTest(c, vaultAddress))
	c.Assert(NewMsgObservedTxIn(common.ObservedTxs{variant}, GetRandomBech32Addr()).ValidateBasic(), NotNil)
}

func (s *MsgObservedTxInSuite) TestRejectsInboundXMROutboundMetadata(c *C) {
	pk := getRandomXMRPubKeyForMsgTest(c)
	toAddress, err := pk.GetAddress(common.XMRChain)
	c.Assert(err, IsNil)
	fromAddress, err := common.NewAddress(common.XMRUnknownSenderAddress(GetRandomTxHash().String(), toAddress.String()))
	c.Assert(err, IsNil)
	tx := common.NewObservedTx(common.Tx{
		ID:          GetRandomTxHash(),
		Chain:       common.XMRChain,
		FromAddress: fromAddress,
		ToAddress:   toAddress,
		Coins:       common.NewCoins(common.NewCoin(common.XMRAsset, cosmos.NewUint(100))),
		Gas:         common.Gas{common.NewCoin(common.XMRAsset, cosmos.OneUint())},
		Memo:        "=:THOR.RUNE:" + GetRandomTHORAddress().String(),
	}, 55, pk, 55)
	tx.XmrMinedHeight = 55

	msg := NewMsgObservedTxIn(common.ObservedTxs{tx}, GetRandomBech32Addr())
	c.Assert(msg.ValidateBasic(), IsNil)

	withKey := tx
	withKey.XmrTxKey = strings.Repeat("a", 64)
	msg = NewMsgObservedTxIn(common.ObservedTxs{withKey}, GetRandomBech32Addr())
	err = msg.ValidateBasic()
	c.Assert(err, ErrorMatches, ".*xmr tx key is only valid for outbound observed transactions.*")
	c.Check(errors.Is(err, se.ErrUnknownRequest), Equals, true)

	withRefs := tx
	withRefs.SpentOutputRefs = []common.OutputRef{validXMRSpentRefForMsgTest(tx)}
	msg = NewMsgObservedTxIn(common.ObservedTxs{withRefs}, GetRandomBech32Addr())
	c.Assert(msg.ValidateBasic(), ErrorMatches, ".*spent output refs are only valid for outbound observed transactions.*")
}

func (s *MsgObservedTxInSuite) TestMsgObservedTxIn(c *C) {
	var err error
	pk := GetRandomPubKey()
	tx := common.NewObservedTx(GetRandomTx(), 55, pk, 55)
	acc := GetRandomBech32Addr()
	tx.Tx.ToAddress, err = pk.GetAddress(tx.Tx.Coins[0].Asset.Chain)
	c.Assert(err, IsNil)

	m := NewMsgObservedTxIn(common.ObservedTxs{tx}, acc)
	EnsureMsgBasicCorrect(m, c)

	m1 := NewMsgObservedTxIn(nil, acc)
	c.Assert(m1.ValidateBasic(), NotNil)
	m2 := NewMsgObservedTxIn(common.ObservedTxs{tx}, cosmos.AccAddress{})
	c.Assert(m2.ValidateBasic(), NotNil)

	// will not accept observations with pre-determined signers. This is
	// important to ensure an observer can fake signers from other node accounts
	// *IMPORTANT* DON'T REMOVE THIS CHECK
	tx.Signers = append(tx.Signers, GetRandomBech32Addr().String())
	m3 := NewMsgObservedTxIn(common.ObservedTxs{tx}, acc)
	c.Assert(m3.ValidateBasic(), NotNil)

	tx4 := common.NewObservedTx(GetRandomTx(), 1, pk, 1)
	m4 := NewMsgObservedTxIn(common.ObservedTxs{tx4}, acc)
	err4 := m4.ValidateBasic()
	c.Assert(err4, NotNil)
	c.Assert(errors.Is(err4, se.ErrUnknownRequest), Equals, true)

	tx5 := common.NewObservedTx(GetRandomTx(), 1, pk, 1)
	tx5.Tx.ToAddress, err = pk.GetAddress(tx.Tx.Coins[0].Asset.Chain)
	c.Assert(err, IsNil)
	tx5.OutHashes = []string{
		GetRandomTxHash().String(),
	}
	m5 := NewMsgObservedTxIn(common.ObservedTxs{tx5}, acc)
	err5 := m5.ValidateBasic()
	c.Assert(err5, NotNil)
	c.Assert(errors.Is(err4, se.ErrUnknownRequest), Equals, true)

	tx6 := common.NewObservedTx(GetRandomTx(), 1, pk, 1)
	tx6.Tx.FromAddress = common.Address("")
	m6 := NewMsgObservedTxIn(common.ObservedTxs{tx6}, acc)
	err6 := m6.ValidateBasic()
	c.Assert(err6, NotNil)
	c.Assert(errors.Is(err4, se.ErrUnknownRequest), Equals, true)

	tx7 := common.NewObservedTx(GetRandomTx(), 1, "whatever", 1)
	m7 := NewMsgObservedTxIn(common.ObservedTxs{tx7}, acc)
	err7 := m7.ValidateBasic()
	c.Assert(err7, NotNil)
	c.Assert(errors.Is(err4, se.ErrUnknownRequest), Equals, true)

	xmrVault := GetRandomEd25519PubKey()
	xmrVaultAddr, err := xmrVault.GetAddress(common.XMRChain)
	c.Assert(err, IsNil)
	xmrTxID := GetRandomTxHash()
	xmrSender, err := common.NewAddress(common.XMRUnknownSenderAddress(xmrTxID.String(), xmrVaultAddr.String()))
	c.Assert(err, IsNil)
	xmrTx := common.NewTx(
		xmrTxID,
		xmrSender,
		xmrVaultAddr,
		common.Coins{common.NewCoin(common.XMRAsset, cosmos.OneUint())},
		common.Gas{common.NewCoin(common.XMRAsset, cosmos.OneUint())},
		"",
	)
	xmrObservedTx := common.NewObservedTx(xmrTx, 1, xmrVault, 1)
	xmrObservedTx.SpentOutputRefs = []common.OutputRef{{
		TxHash:      GetRandomTxHash().String(),
		OutputIndex: 0,
		KeyImage:    strings.Repeat("a", 64),
		SpendTxHash: xmrTxID.String(),
	}}
	xmrMsg := NewMsgObservedTxIn(common.ObservedTxs{xmrObservedTx}, acc)
	xmrErr := xmrMsg.ValidateBasic()
	c.Assert(xmrErr, NotNil)
	c.Assert(errors.Is(xmrErr, se.ErrUnknownRequest), Equals, true)

	xmrObservedTx.SpentOutputRefs = nil
	xmrObservedTx.XmrTxKey = strings.Repeat("a", 64)
	xmrMsg = NewMsgObservedTxIn(common.ObservedTxs{xmrObservedTx}, acc)
	xmrErr = xmrMsg.ValidateBasic()
	c.Assert(xmrErr, NotNil)
	c.Assert(errors.Is(xmrErr, se.ErrUnknownRequest), Equals, true)
}
