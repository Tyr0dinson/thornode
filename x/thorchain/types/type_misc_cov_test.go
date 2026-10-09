package types

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"

	se "github.com/cosmos/cosmos-sdk/types/errors"
	. "gopkg.in/check.v1"

	tssMessages "gitlab.com/thorchain/thornode/v3/bifrost/p2p/messages"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

type TypeMiscCovSuite struct{}

var _ = Suite(&TypeMiscCovSuite{})

func (s *TypeMiscCovSuite) SetUpSuite(c *C) {
	SetupConfigForTest()
}

func (s *TypeMiscCovSuite) TestLiquidityProviderRedeemValues(c *C) {
	pool := NewPool()
	pool.Asset = common.BTCAsset
	pool.BalanceRune = cosmos.NewUint(1000)
	pool.BalanceAsset = cosmos.NewUint(500)
	pool.LPUnits = cosmos.NewUint(100)

	lp := LiquidityProvider{
		Asset:             common.BTCAsset,
		Units:             cosmos.NewUint(10),
		RuneDepositValue:  cosmos.NewUint(4),
		AssetDepositValue: cosmos.NewUint(1),
	}

	err, runeVal := lp.GetRuneRedeemValue(pool, cosmos.ZeroUint())
	c.Assert(err, IsNil)
	c.Assert(runeVal.Uint64(), Equals, uint64(100)) // 10 * 1000 / 100
	err, assetVal := lp.GetAssetRedeemValue(pool, cosmos.ZeroUint())
	c.Assert(err, IsNil)
	c.Assert(assetVal.Uint64(), Equals, uint64(50)) // 10 * 500 / 100

	// synth supply increases pool units which dilutes redeem value
	err, runeVal = lp.GetRuneRedeemValue(pool, cosmos.NewUint(500))
	c.Assert(err, IsNil)
	c.Assert(runeVal.Uint64(), Equals, uint64(50)) // synth units = 100*500/(1000-500)=100

	// sqrt(4e8*1e8)/10 = 2e8/10
	err, luvi := lp.GetLuviDepositValue(pool)
	c.Assert(err, IsNil)
	c.Assert(luvi.Uint64(), Equals, uint64(20000000))

	err, luvi = lp.GetLuviRedeemValue(cosmos.NewUint(9), cosmos.NewUint(4))
	c.Assert(err, IsNil)
	c.Assert(luvi.Uint64(), Equals, uint64(60000000)) // sqrt(36e16)=6e8 /10

	c.Assert(lp.GetSaversAssetRedeemValue(pool).Uint64(), Equals, uint64(50))

	// mismatched assets
	other := pool
	other.Asset = common.ETHAsset
	err, v := lp.GetRuneRedeemValue(other, cosmos.ZeroUint())
	c.Assert(err, ErrorMatches, "LP and Pool assets do not match.*")
	c.Assert(v.IsZero(), Equals, true)
	err, v = lp.GetAssetRedeemValue(other, cosmos.ZeroUint())
	c.Assert(err, NotNil)
	c.Assert(v.IsZero(), Equals, true)
	err, v = lp.GetLuviDepositValue(other)
	c.Assert(err, NotNil)
	c.Assert(v.IsZero(), Equals, true)

	// zero denominators
	empty := NewPool()
	empty.Asset = common.BTCAsset
	err, v = lp.GetRuneRedeemValue(empty, cosmos.ZeroUint())
	c.Assert(err, IsNil)
	c.Assert(v.IsZero(), Equals, true)
	err, v = lp.GetAssetRedeemValue(empty, cosmos.ZeroUint())
	c.Assert(err, IsNil)
	c.Assert(v.IsZero(), Equals, true)
	c.Assert(lp.GetSaversAssetRedeemValue(empty).IsZero(), Equals, true)
	noUnits := lp
	noUnits.Units = cosmos.ZeroUint()
	err, v = noUnits.GetLuviDepositValue(pool)
	c.Assert(err, IsNil)
	c.Assert(v.IsZero(), Equals, true)
	err, v = noUnits.GetLuviRedeemValue(cosmos.NewUint(1), cosmos.NewUint(1))
	c.Assert(err, IsNil)
	c.Assert(v.IsZero(), Equals, true)

	// address selection
	assetAddr := GetRandomBTCAddress()
	runeAddr := GetRandomTHORAddress()
	c.Assert(LiquidityProvider{AssetAddress: assetAddr}.GetAddress(), Equals, assetAddr)
	c.Assert(LiquidityProvider{AssetAddress: assetAddr, RuneAddress: runeAddr}.GetAddress(), Equals, runeAddr)
}

func (s *TypeMiscCovSuite) TestCanonicalTssRound(c *C) {
	c.Assert(CanonicalTssRound("", common.SigningAlgoSecp256k1), Equals, "")

	secp := map[string]string{
		"binance.tsslib.ecdsa.keygen.KGRound1Message":     tssMessages.KEYGEN1,
		"binance.tsslib.ecdsa.keygen.KGRound2Message1":    tssMessages.KEYGEN2aUnicast,
		"binance.tsslib.ecdsa.keygen.KGRound2Message2":    tssMessages.KEYGEN2b,
		"binance.tsslib.ecdsa.keygen.KGRound3Message":     tssMessages.KEYGEN3,
		"binance.tsslib.ecdsa.signing.SignRound1Message1": tssMessages.KEYSIGN1aUnicast,
		"binance.tsslib.ecdsa.signing.SignRound1Message2": tssMessages.KEYSIGN1b,
		"binance.tsslib.ecdsa.signing.SignRound2Message":  tssMessages.KEYSIGN2Unicast,
		"binance.tsslib.ecdsa.signing.SignRound3Message":  tssMessages.KEYSIGN3,
		"binance.tsslib.ecdsa.signing.SignRound4Message":  tssMessages.KEYSIGN4,
		"binance.tsslib.ecdsa.signing.SignRound5Message":  tssMessages.KEYSIGN5,
		"binance.tsslib.ecdsa.signing.SignRound6Message":  tssMessages.KEYSIGN6,
		"binance.tsslib.ecdsa.signing.SignRound7Message":  tssMessages.KEYSIGN7,
		"binance.tsslib.ecdsa.signing.SignRound8Message":  tssMessages.KEYSIGN8,
		"binance.tsslib.ecdsa.signing.SignRound9Message":  tssMessages.KEYSIGN9,
		"binance.tsslib.ecdsa.signing.Unknown":            "binance.tsslib.ecdsa.signing.Unknown",
	}
	for raw, want := range secp {
		// algo inferred from ".ecdsa." and passed explicitly
		c.Check(CanonicalTssRound(raw, ""), Equals, want, Commentf(raw))
		c.Check(CanonicalTssRound(raw, common.SigningAlgoSecp256k1), Equals, want, Commentf(raw))
	}
	// canonical constants map to themselves
	c.Check(CanonicalTssRound(tssMessages.KEYSIGN5, common.SigningAlgoSecp256k1), Equals, tssMessages.KEYSIGN5)

	ed := map[string]string{
		"binance.tsslib.eddsa.keygen.KGRound1Message":    tssMessages.EDDSAKEYGEN1,
		"binance.tsslib.eddsa.keygen.KGRound2Message1":   tssMessages.EDDSAKEYGEN2a,
		"binance.tsslib.eddsa.keygen.KGRound2Message2":   tssMessages.EDDSAKEYGEN2b,
		"binance.tsslib.eddsa.signing.SignRound1Message": tssMessages.EDDSAKEYSIGN1,
		"binance.tsslib.eddsa.signing.SignRound2Message": tssMessages.EDDSAKEYSIGN2,
		"binance.tsslib.eddsa.signing.SignRound3Message": tssMessages.EDDSAKEYSIGN3,
		"binance.tsslib.eddsa.signing.Other":             "binance.tsslib.eddsa.signing.Other",
	}
	for raw, want := range ed {
		c.Check(CanonicalTssRound(raw, ""), Equals, want, Commentf(raw))
		c.Check(CanonicalTssRound(raw, common.SigningAlgoEd25519), Equals, want, Commentf(raw))
	}

	// explicit algo wins over the inferred one; unknown algo is passthrough
	c.Check(CanonicalTssRound("x.eddsa.KGRound1Message", common.SigningAlgoSecp256k1), Equals, tssMessages.KEYGEN1)
	c.Check(CanonicalTssRound("KGRound1Message", ""), Equals, "KGRound1Message")

	norm, err := NormalizeTssKeysignRound("")
	c.Assert(err, IsNil)
	c.Assert(norm, Equals, "")
	norm, err = NormalizeTssKeysignRound("binance.tsslib.ecdsa.signing.SignRound4Message")
	c.Assert(err, IsNil)
	c.Assert(norm, Equals, tssMessages.KEYSIGN4)
}

func (s *TypeMiscCovSuite) TestPoolHelpers(c *C) {
	c.Assert(PoolStatus_Available.Valid(), IsNil)
	c.Assert(PoolStatus(99).Valid(), ErrorMatches, "invalid pool status")

	var ps PoolStatus
	c.Assert(json.Unmarshal([]byte(`"Staged"`), &ps), IsNil)
	c.Assert(ps, Equals, PoolStatus_Staged)
	c.Assert(json.Unmarshal([]byte(`5`), &ps), NotNil)

	p := NewPool()
	p.Asset = common.BTCAsset
	p.BalanceRune = cosmos.NewUint(1000)
	p.BalanceAsset = cosmos.NewUint(100)
	p.Decimals = 8
	c.Assert(p.IsStaged(), Equals, false)
	p.Status = PoolStatus_Staged
	c.Assert(p.IsStaged(), Equals, true)

	str := p.String()
	for _, want := range []string{"rune-balance: 1000\n", "asset-balance: 100\n", "asset: BTC.BTC\n", "status: Staged\n", "decimals:8\n"} {
		c.Check(strings.Contains(str, want), Equals, true, Commentf(want))
	}

	// rune needed to keep x*y constant when withdrawing 50 of 100 asset: 1000*50/50
	c.Assert(p.RuneReimbursementForAssetWithdrawal(cosmos.NewUint(50)).Uint64(), Equals, uint64(1000))
	c.Assert(p.RuneReimbursementForAssetWithdrawal(cosmos.NewUint(100)).IsZero(), Equals, true)
	c.Assert(p.RuneReimbursementForAssetWithdrawal(cosmos.NewUint(200)).IsZero(), Equals, true)
	empty := NewPool()
	c.Assert(empty.RuneReimbursementForAssetWithdrawal(cosmos.NewUint(1)).IsZero(), Equals, true)

	pools := Pools{p}
	got, ok := pools.Get(common.BTCAsset)
	c.Assert(ok, Equals, true)
	c.Assert(got.BalanceRune.Uint64(), Equals, uint64(1000))
	got, ok = pools.Get(common.ETHAsset)
	c.Assert(ok, Equals, false)
	c.Assert(got.Asset.IsEmpty(), Equals, true)
	c.Assert(got.Status, Equals, PoolStatus_Available)
}

func (s *TypeMiscCovSuite) TestTHORNameHelpers(c *C) {
	owner := GetRandomBech32Addr()
	thorAddr := GetRandomTHORAddress()
	btcAddr := GetRandomBTCAddress()

	n := THORName{Name: "MyName"}
	c.Assert(n.Key(), Equals, base64.StdEncoding.EncodeToString([]byte("myname")))

	// no owner
	n.SetAlias(common.THORChain, thorAddr)
	c.Assert(n.CanReceiveAffiliateFee(), Equals, false)

	// owner + rune alias, no preferred asset
	n.Owner = owner
	c.Assert(n.CanReceiveAffiliateFee(), Equals, true)
	c.Assert(n.GetAlias(common.ETHChain), Equals, common.NoAddress)

	// owner, no rune alias, no preferred asset
	n2 := THORName{Name: "x", Owner: owner}
	c.Assert(n2.CanReceiveAffiliateFee(), Equals, false)

	// preferred asset requires an alias on that chain
	n.PreferredAsset = common.BTCAsset
	c.Assert(n.CanReceiveAffiliateFee(), Equals, false)
	n.SetAlias(common.BTCChain, btcAddr)
	c.Assert(n.CanReceiveAffiliateFee(), Equals, true)
	c.Assert(n.GetAlias(common.BTCChain), Equals, btcAddr)
}

func (s *TypeMiscCovSuite) TestTssMetrics(c *C) {
	a1 := GetRandomBech32Addr()
	a2 := GetRandomBech32Addr()
	a3 := GetRandomBech32Addr()

	km := NewTssKeygenMetric(GetRandomPubKey())
	c.Assert(km.GetMedianTime(), Equals, int64(0))
	km.AddNodeTssTime(a1, 100)
	km.AddNodeTssTime(a2, 300)
	c.Assert(km.GetMedianTime(), Equals, int64(200))
	km.AddNodeTssTime(a1, 500) // update existing entry
	c.Assert(km.NodeTssTimes, HasLen, 2)
	c.Assert(km.GetMedianTime(), Equals, int64(400))
	km.AddNodeTssTime(a3, 50)
	c.Assert(km.GetMedianTime(), Equals, int64(300))

	ksm := NewTssKeysignMetric(GetRandomTxHash())
	ksm.AddNodeTssTime(a1, 10)
	ksm.AddNodeTssTime(a1, 20)
	c.Assert(ksm.NodeTssTimes, HasLen, 1)
	c.Assert(ksm.GetMedianTime(), Equals, int64(20))
}

func (s *TypeMiscCovSuite) TestMsgBondYieldValidation(c *C) {
	nodeAddr := GetRandomBech32Addr()
	signer := GetRandomBech32Addr()
	txin := common.Tx{ID: GetRandomTxHash(), FromAddress: GetRandomTHORAddress()}
	msg := NewMsgBondYield(txin, nodeAddr, 100, "", signer)
	c.Assert(msg.ValidateBasic(), IsNil)
	c.Assert(msg.GetSigners(), DeepEquals, []cosmos.AccAddress{signer})

	noID := txin
	noID.ID = ""
	noFrom := txin
	noFrom.FromAddress = ""
	cases := []struct {
		msg *MsgBondYield
		err error
	}{
		{NewMsgBondYield(txin, cosmos.AccAddress{}, 100, "", signer), se.ErrInvalidAddress},
		{NewMsgBondYield(txin, nodeAddr, 10001, "", signer), se.ErrUnknownRequest},
		{NewMsgBondYield(noID, nodeAddr, 100, "", signer), se.ErrUnknownRequest},
		{NewMsgBondYield(noFrom, nodeAddr, 100, "", signer), se.ErrInvalidAddress},
		{NewMsgBondYield(txin, nodeAddr, 100, "", cosmos.AccAddress{}), se.ErrInvalidAddress},
	}
	for i, tc := range cases {
		c.Check(errors.Is(tc.msg.ValidateBasic(), tc.err), Equals, true, Commentf("case %d", i))
	}
}

func (s *TypeMiscCovSuite) TestMsgObservedTxQuorumValidation(c *C) {
	pk := GetRandomPubKey()
	vaultAddr, err := pk.GetAddress(common.ETHChain)
	c.Assert(err, IsNil)
	other := GetRandomETHAddress()
	signer := GetRandomBech32Addr()
	mk := func(from, to common.Address) common.ObservedTx {
		return common.NewObservedTx(common.Tx{
			ID:          GetRandomTxHash(),
			Chain:       common.ETHChain,
			FromAddress: from,
			ToAddress:   to,
			Coins:       common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(100))),
			Gas:         common.Gas{common.NewCoin(common.ETHAsset, cosmos.NewUint(1))},
			Memo:        "memo",
		}, 10, pk, 10)
	}
	att, _, attAddr := newAttestationCov()

	inbound := mk(other, vaultAddr)
	msg := NewMsgObservedTxQuorum(&common.QuorumTx{ObsTx: inbound, Inbound: true, Attestations: []*common.Attestation{att}}, signer)
	c.Assert(msg.ValidateBasic(), IsNil)
	c.Assert(msg.GetSigners(), HasLen, 1)
	c.Assert(msg.GetSigners()[0].Equals(attAddr), Equals, true)

	outbound := mk(vaultAddr, other)
	c.Assert(NewMsgObservedTxQuorum(&common.QuorumTx{ObsTx: outbound}, signer).ValidateBasic(), IsNil)

	c.Assert(errors.Is(NewMsgObservedTxQuorum(&common.QuorumTx{ObsTx: inbound, Inbound: true}, cosmos.AccAddress{}).ValidateBasic(), se.ErrInvalidAddress), Equals, true)
	c.Assert(NewMsgObservedTxQuorum(nil, signer).ValidateBasic(), ErrorMatches, ".*QuoTx cannot be nil.*")

	invalid := inbound
	invalid.Tx.ID = ""
	c.Assert(errors.Is(NewMsgObservedTxQuorum(&common.QuorumTx{ObsTx: invalid, Inbound: true}, signer).ValidateBasic(), se.ErrUnknownRequest), Equals, true)

	withRefs := inbound
	withRefs.SpentOutputRefs = []common.OutputRef{{}}
	c.Assert(NewMsgObservedTxQuorum(&common.QuorumTx{ObsTx: withRefs, Inbound: true}, signer).ValidateBasic(), ErrorMatches, ".*spent output refs are only valid for.*")

	withKey := inbound
	withKey.XmrTxKey = "abc"
	c.Assert(NewMsgObservedTxQuorum(&common.QuorumTx{ObsTx: withKey, Inbound: true}, signer).ValidateBasic(), ErrorMatches, "(?i).*xmr tx key.*")

	c.Assert(NewMsgObservedTxQuorum(&common.QuorumTx{ObsTx: outbound, Inbound: true}, signer).ValidateBasic(), ErrorMatches, ".*not an inbound observed transaction.*")
	c.Assert(NewMsgObservedTxQuorum(&common.QuorumTx{ObsTx: inbound}, signer).ValidateBasic(), ErrorMatches, ".*not an outbound observed transaction.*")

	withSigners := inbound
	withSigners.Signers = []string{"x"}
	c.Assert(NewMsgObservedTxQuorum(&common.QuorumTx{ObsTx: withSigners, Inbound: true}, signer).ValidateBasic(), ErrorMatches, ".*signers must be empty.*")

	withOut := inbound
	withOut.OutHashes = []string{"x"}
	c.Assert(NewMsgObservedTxQuorum(&common.QuorumTx{ObsTx: withOut, Inbound: true}, signer).ValidateBasic(), ErrorMatches, ".*out hashes must be empty.*")

	done := inbound
	done.Status = common.Status_done
	c.Assert(NewMsgObservedTxQuorum(&common.QuorumTx{ObsTx: done, Inbound: true}, signer).ValidateBasic(), ErrorMatches, ".*status must be incomplete.*")
}

func (s *TypeMiscCovSuite) TestMsgSecuredAssetAndTradeAccount(c *C) {
	acc := GetRandomBech32Addr()
	signer := GetRandomBech32Addr()
	tx := GetRandomTx()
	noID := tx
	noID.ID = ""
	btcAddr := GetRandomBTCAddress()
	secured, err := common.NewAsset("BTC-BTC")
	c.Assert(err, IsNil)
	trade, err := common.NewAsset("BTC~BTC")
	c.Assert(err, IsNil)

	// deposits
	c.Assert(NewMsgSecuredAssetDeposit(common.BTCAsset, cosmos.NewUint(1), acc, signer, tx).ValidateBasic(), IsNil)
	c.Assert(NewMsgTradeAccountDeposit(common.BTCAsset, cosmos.NewUint(1), acc, signer, tx).ValidateBasic(), IsNil)
	sdCases := []*MsgSecuredAssetDeposit{
		NewMsgSecuredAssetDeposit(common.EmptyAsset, cosmos.NewUint(1), acc, signer, tx),
		NewMsgSecuredAssetDeposit(common.RuneNative, cosmos.NewUint(1), acc, signer, tx),
		NewMsgSecuredAssetDeposit(common.BTCAsset, cosmos.ZeroUint(), acc, signer, tx),
		NewMsgSecuredAssetDeposit(common.BTCAsset, cosmos.NewUint(1), cosmos.AccAddress{}, signer, tx),
		NewMsgSecuredAssetDeposit(common.BTCAsset, cosmos.NewUint(1), acc, cosmos.AccAddress{}, tx),
		NewMsgSecuredAssetDeposit(common.BTCAsset, cosmos.NewUint(1), acc, signer, noID),
	}
	for i, m := range sdCases {
		c.Check(m.ValidateBasic(), NotNil, Commentf("secured deposit case %d", i))
	}
	tdCases := []*MsgTradeAccountDeposit{
		NewMsgTradeAccountDeposit(common.EmptyAsset, cosmos.NewUint(1), acc, signer, tx),
		NewMsgTradeAccountDeposit(common.RuneNative, cosmos.NewUint(1), acc, signer, tx),
		NewMsgTradeAccountDeposit(common.BTCAsset, cosmos.ZeroUint(), acc, signer, tx),
		NewMsgTradeAccountDeposit(common.BTCAsset, cosmos.NewUint(1), cosmos.AccAddress{}, signer, tx),
		NewMsgTradeAccountDeposit(common.BTCAsset, cosmos.NewUint(1), acc, cosmos.AccAddress{}, tx),
		NewMsgTradeAccountDeposit(common.BTCAsset, cosmos.NewUint(1), acc, signer, noID),
	}
	for i, m := range tdCases {
		c.Check(m.ValidateBasic(), NotNil, Commentf("trade deposit case %d", i))
	}

	// withdrawals
	c.Assert(NewMsgSecuredAssetWithdraw(secured, cosmos.NewUint(1), btcAddr, signer, tx).ValidateBasic(), IsNil)
	c.Assert(NewMsgTradeAccountWithdrawal(trade, cosmos.NewUint(1), btcAddr, signer, tx).ValidateBasic(), IsNil)
	swCases := []struct {
		m     *MsgSecuredAssetWithdraw
		match string
	}{
		{NewMsgSecuredAssetWithdraw(common.EmptyAsset, cosmos.NewUint(1), btcAddr, signer, tx), ".*asset cannot be empty.*"},
		{NewMsgSecuredAssetWithdraw(common.BTCAsset, cosmos.NewUint(1), btcAddr, signer, tx), ".*must be a secured asset.*"},
		{NewMsgSecuredAssetWithdraw(secured, cosmos.ZeroUint(), btcAddr, signer, tx), ".*amount cannot be zero.*"},
		{NewMsgSecuredAssetWithdraw(secured, cosmos.NewUint(1), common.NoAddress, signer, tx), ".*asset address cannot be empty.*"},
		{NewMsgSecuredAssetWithdraw(secured, cosmos.NewUint(1), GetRandomETHAddress(), signer, tx), ".*does not match asset chain.*"},
		{NewMsgSecuredAssetWithdraw(secured, cosmos.NewUint(1), btcAddr, cosmos.AccAddress{}, tx), ".*"},
		{NewMsgSecuredAssetWithdraw(secured, cosmos.NewUint(1), btcAddr, signer, noID), ".*txID cannot be empty.*"},
	}
	for i, tc := range swCases {
		c.Check(tc.m.ValidateBasic(), ErrorMatches, tc.match, Commentf("secured withdraw case %d", i))
	}
	twCases := []struct {
		m     *MsgTradeAccountWithdrawal
		match string
	}{
		{NewMsgTradeAccountWithdrawal(common.EmptyAsset, cosmos.NewUint(1), btcAddr, signer, tx), ".*asset cannot be empty.*"},
		{NewMsgTradeAccountWithdrawal(common.BTCAsset, cosmos.NewUint(1), btcAddr, signer, tx), ".*must be a trade asset.*"},
		{NewMsgTradeAccountWithdrawal(trade, cosmos.ZeroUint(), btcAddr, signer, tx), ".*amount cannot be zero.*"},
		{NewMsgTradeAccountWithdrawal(trade, cosmos.NewUint(1), common.NoAddress, signer, tx), ".*asset address cannot be empty.*"},
		{NewMsgTradeAccountWithdrawal(trade, cosmos.NewUint(1), GetRandomETHAddress(), signer, tx), ".*does not match asset chain.*"},
		{NewMsgTradeAccountWithdrawal(trade, cosmos.NewUint(1), btcAddr, cosmos.AccAddress{}, tx), ".*"},
		{NewMsgTradeAccountWithdrawal(trade, cosmos.NewUint(1), btcAddr, signer, noID), ".*txID cannot be empty.*"},
	}
	for i, tc := range twCases {
		c.Check(tc.m.ValidateBasic(), ErrorMatches, tc.match, Commentf("trade withdraw case %d", i))
	}
}

func (s *TypeMiscCovSuite) TestNodeAccountHelpers(c *C) {
	c.Assert(NodeStatus_Active.Valid(), IsNil)
	c.Assert(NodeStatus(99).Valid(), ErrorMatches, "invalid node status")

	var ns NodeStatus
	c.Assert(json.Unmarshal([]byte(`"active"`), &ns), IsNil)
	c.Assert(ns, Equals, NodeStatus_Active)
	c.Assert(json.Unmarshal([]byte(`"Standby"`), &ns), IsNil)
	c.Assert(ns, Equals, NodeStatus_Standby)
	c.Assert(json.Unmarshal([]byte(`"bogus"`), &ns), IsNil)
	c.Assert(ns, Equals, NodeStatus_Unknown)
	c.Assert(json.Unmarshal([]byte(`1`), &ns), NotNil)

	na := GetRandomValidatorNode(NodeStatus_Active)
	str := na.String()
	c.Assert(strings.Contains(str, "node:"+na.NodeAddress.String()+"\n"), Equals, true)
	c.Assert(strings.Contains(str, "status:Active\n"), Equals, true)
	c.Assert(strings.Contains(str, "requested to leave:false\n"), Equals, true)

	na1 := GetRandomValidatorNode(NodeStatus_Active)
	na2 := GetRandomValidatorNode(NodeStatus_Active)
	na1.StatusSince = 5
	na2.StatusSince = 10
	nas := NodeAccounts{na1, na2}
	c.Assert(nas.Less(0, 1), Equals, true)
	c.Assert(nas.Less(1, 0), Equals, false)
	na2.StatusSince = 5
	nas = NodeAccounts{na1, na2}
	c.Assert(nas.Less(0, 1), Equals, na1.NodeAddress.String() < na2.NodeAddress.String())

	addrs := nas.GetNodeAddresses()
	c.Assert(addrs, HasLen, 2)
	c.Assert(addrs[0].Equals(na1.NodeAddress), Equals, true)
	c.Assert(addrs[1].Equals(na2.NodeAddress), Equals, true)
}

func (s *TypeMiscCovSuite) TestVaultHelpers(c *C) {
	pk := GetRandomPubKey()
	v := NewVault(10, VaultStatus_ActiveVault, VaultType_AsgardVault, pk, []string{"BTC", "ETH", "not-a-chain!"}, nil)
	c.Assert(v.HasChain(common.BTCChain), Equals, true)
	c.Assert(v.HasChain(common.DOGEChain), Equals, false)
	c.Assert(v.IsActive(), Equals, true)
	c.Assert(v.IsRetiring(), Equals, false)
	v.Status = VaultStatus_RetiringVault
	c.Assert(v.IsRetiring(), Equals, true)
	c.Assert(v.IsActive(), Equals, false)

	// XMR monovault shape
	c.Assert(v.IsXMRMonovault(), Equals, false)
	xmr := NewVault(1, VaultStatus_ActiveVault, VaultType_AsgardVault, pk, []string{"THOR", "XMR"}, nil)
	c.Assert(xmr.IsXMRMonovault(), Equals, true)
	notAsgard := xmr
	notAsgard.Type = VaultType_UnknownVault
	c.Assert(notAsgard.IsXMRMonovault(), Equals, false)
	mixed := NewVault(1, VaultStatus_ActiveVault, VaultType_AsgardVault, pk, []string{"XMR", "BTC"}, nil)
	c.Assert(mixed.IsXMRMonovault(), Equals, false)

	// membership
	m1 := GetRandomPubKey()
	m2 := GetRandomPubKey()
	v.Membership = []string{m1.String(), m2.String()}
	c.Assert(v.MembershipEquals(common.PubKeys{m2, m1}), Equals, true)
	c.Assert(v.MembershipEquals(common.PubKeys{m1}), Equals, false)
	c.Assert(v.MembershipEquals(common.PubKeys{m1, GetRandomPubKey()}), Equals, false)

	// coin counts
	v.Coins = common.Coins{
		common.NewCoin(common.BTCAsset, cosmos.NewUint(5)),
		common.NewCoin(common.ETHAsset, cosmos.ZeroUint()),
		common.NewCoin(common.ETHAsset.GetSyntheticAsset(), cosmos.NewUint(1)),
	}
	c.Assert(v.CoinLength(), Equals, 2)
	c.Assert(v.CoinLengthByChain(common.BTCChain), Equals, 1)
	c.Assert(v.CoinLengthByChain(common.ETHChain), Equals, 0)

	// routers
	empty := v.GetContract(common.ETHChain)
	c.Assert(empty.IsEmpty(), Equals, true)
	router := ChainContract{Chain: common.ETHChain, Router: GetRandomETHAddress()}
	v.UpdateContract(router)
	c.Assert(v.Routers, HasLen, 1)
	c.Assert(v.GetContract(common.ETHChain).Router, Equals, router.Router)
	router2 := ChainContract{Chain: common.ETHChain, Router: GetRandomETHAddress()}
	v.UpdateContract(router2)
	c.Assert(v.Routers, HasLen, 1)
	c.Assert(v.GetContract(common.ETHChain).Router, Equals, router2.Router)
}

func (s *TypeMiscCovSuite) TestVaultDeductPendingOutbounds(c *C) {
	pk := GetRandomPubKey()
	v := NewVault(10, VaultStatus_ActiveVault, VaultType_AsgardVault, pk, []string{"ETH"}, nil)
	v.Coins = common.Coins{
		common.NewCoin(common.ETHAsset, cosmos.NewUint(1000)),
		common.NewCoin(common.BTCAsset, cosmos.NewUint(10)),
	}
	items := []TxOutItem{
		{ // matching, outstanding: coin + gas both ETH
			Chain: common.ETHChain, VaultPubKey: pk,
			Coin:   common.NewCoin(common.ETHAsset, cosmos.NewUint(100)),
			MaxGas: common.Gas{common.NewCoin(common.ETHAsset, cosmos.NewUint(5))},
		},
		{ // other vault
			Chain: common.ETHChain, VaultPubKey: GetRandomPubKey(),
			Coin: common.NewCoin(common.ETHAsset, cosmos.NewUint(100)),
		},
		{ // already sent
			Chain: common.ETHChain, VaultPubKey: pk, OutHash: GetRandomTxHash(),
			Coin: common.NewCoin(common.ETHAsset, cosmos.NewUint(100)),
		},
		{ // no gas, over-deduct clamps at zero
			Chain: common.BTCChain, VaultPubKey: pk,
			Coin: common.NewCoin(common.BTCAsset, cosmos.NewUint(50)),
		},
	}
	v.DeductVaultPendingOutbounds(items)
	c.Assert(v.GetCoin(common.ETHAsset).Amount.Uint64(), Equals, uint64(895))
	c.Assert(v.GetCoin(common.BTCAsset).Amount.IsZero(), Equals, true)
}

func (s *TypeMiscCovSuite) TestVaultAddressesAndSelection(c *C) {
	pk := GetRandomPubKey()
	edPk := GetRandomEd25519PubKey()
	v := NewVault(10, VaultStatus_ActiveVault, VaultType_AsgardVault, pk, []string{"BTC"}, nil)

	got, err := v.AlgoPubKey(common.BTCChain)
	c.Assert(err, IsNil)
	c.Assert(got, Equals, pk)
	_, err = v.AlgoPubKey(common.SOLChain)
	c.Assert(err, ErrorMatches, "vault has no eddsa public key")
	_, err = v.AlgoPubKey(common.XMRChain)
	c.Assert(err, ErrorMatches, "xmr vault has no frost public key")
	v.PubKeyEddsa = edPk
	v.PubKeyFrost = edPk
	got, err = v.AlgoPubKey(common.SOLChain)
	c.Assert(err, IsNil)
	c.Assert(got, Equals, edPk)
	got, err = v.AlgoPubKey(common.XMRChain)
	c.Assert(err, IsNil)
	c.Assert(got, Equals, edPk)

	v2 := NewVault(10, VaultStatus_ActiveVault, VaultType_AsgardVault, pk, []string{"SOL"}, nil)
	_, err = v2.GetAddress(common.SOLChain)
	c.Assert(err, ErrorMatches, "fail to get vault address.*")
	addr, err := v.GetAddress(common.BTCChain)
	c.Assert(err, IsNil)
	expected, _ := pk.GetAddress(common.BTCChain)
	c.Assert(addr, Equals, expected)

	// Has / SelectByMin / SelectByMax
	a := GetRandomVault()
	a.Coins = common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(10))}
	b := GetRandomVault()
	b.Coins = common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(30))}
	cc := GetRandomVault()
	cc.Coins = common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(20))}
	vs := Vaults{a, b, cc}

	has := vs.Has(common.NewCoin(common.BTCAsset, cosmos.NewUint(20)))
	c.Assert(has, HasLen, 2)
	c.Assert(has[0].PubKey, Equals, b.PubKey)
	c.Assert(has[1].PubKey, Equals, cc.PubKey)

	c.Assert(vs.SelectByMinCoin(common.BTCAsset).PubKey, Equals, a.PubKey)
	c.Assert(vs.SelectByMaxCoin(common.BTCAsset).PubKey, Equals, b.PubKey)
	c.Assert(Vaults{b}.SelectByMinCoin(common.BTCAsset).PubKey, Equals, b.PubKey)
	c.Assert(Vaults{a}.SelectByMaxCoin(common.BTCAsset).PubKey, Equals, a.PubKey)
	c.Assert(Vaults{}.SelectByMaxCoin(common.BTCAsset).IsEmpty(), Equals, true)

	// HasAddress surfaces address errors
	ok, err := Vaults{v2}.HasAddress(common.SOLChain, "x")
	c.Assert(err, NotNil)
	c.Assert(ok, Equals, false)
}

func (s *TypeMiscCovSuite) TestVaultEnumJSON(c *C) {
	bz, err := json.Marshal(VaultStatus_RetiringVault)
	c.Assert(err, IsNil)
	c.Assert(string(bz), Equals, `"RetiringVault"`)
	var vs VaultStatus
	c.Assert(json.Unmarshal(bz, &vs), IsNil)
	c.Assert(vs, Equals, VaultStatus_RetiringVault)
	c.Assert(json.Unmarshal([]byte(`"Nope"`), &vs), ErrorMatches, "invalid vault status value: Nope")
	c.Assert(json.Unmarshal([]byte(`3`), &vs), NotNil)

	bz, err = json.Marshal(VaultType_AsgardVault)
	c.Assert(err, IsNil)
	c.Assert(string(bz), Equals, `"AsgardVault"`)
	var vt VaultType
	c.Assert(json.Unmarshal(bz, &vt), IsNil)
	c.Assert(vt, Equals, VaultType_AsgardVault)
	c.Assert(json.Unmarshal([]byte(`"Nope"`), &vt), ErrorMatches, "invalid vault type value: Nope")
	c.Assert(json.Unmarshal([]byte(`3`), &vt), NotNil)
}

func (s *TypeMiscCovSuite) TestRandomHelpers(c *C) {
	vn := GetRandomVaultNode(NodeStatus_Active)
	c.Assert(vn.Type, Equals, NodeType_TypeVault)
	c.Assert(vn.ActiveBlockHeight, Equals, int64(10))
	c.Assert(vn.Bond.Uint64(), Equals, uint64(1000*common.One))
	sn := GetRandomVaultNode(NodeStatus_Standby)
	c.Assert(sn.Bond.Uint64(), Equals, uint64(100*common.One))

	item := GetRandomTxOutItem()
	c.Assert(item.Chain, Equals, common.ETHChain)
	c.Assert(item.Coin.Asset.Equals(common.ETHAsset), Equals, true)

	voter := GetRandomObservedTxVoter()
	c.Assert(voter.Txs, HasLen, 1)
	c.Assert(voter.Actions, HasLen, 1)
	c.Assert(voter.Height, Equals, int64(10))

	c.Assert(GetRandomGAIAAddress().IsChain(common.GAIAChain), Equals, true)
	c.Assert(GetRandomLTCAddress().IsChain(common.LTCChain), Equals, true)
	c.Assert(GetRandomBCHAddress().IsChain(common.BCHChain), Equals, true)

	vault := GetRandomVault()
	c.Assert(vault.IsAsgard(), Equals, true)
	c.Assert(vault.HasChain(common.DOGEChain), Equals, true)
	c.Assert(vault.PubKeyEddsa.IsEmpty(), Equals, false)

	secp := GetRandomPubkeyForChain(common.BTCChain)
	c.Assert(secp.IsEmpty(), Equals, false)
	ed := GetRandomPubkeyForChain(common.SOLChain)
	_, err := ed.GetAddress(common.SOLChain)
	c.Assert(err, IsNil)
}
