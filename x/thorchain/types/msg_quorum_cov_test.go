package types

import (
	"errors"

	"cosmossdk.io/x/tx/signing"
	coskey "github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	se "github.com/cosmos/cosmos-sdk/types/errors"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	. "gopkg.in/check.v1"

	apicommon "gitlab.com/thorchain/thornode/v3/api/common"
	apitypes "gitlab.com/thorchain/thornode/v3/api/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

type MsgQuorumCovSuite struct{}

var _ = Suite(&MsgQuorumCovSuite{})

// newAttestationCov returns a common attestation, its api twin and the
// account address derived from the attestation pubkey.
func newAttestationCov() (*common.Attestation, *apicommon.Attestation, cosmos.AccAddress) {
	pub := coskey.GenPrivKey().PubKey()
	return &common.Attestation{PubKey: pub.Bytes(), Signature: []byte("sig")},
		&apicommon.Attestation{PubKey: pub.Bytes(), Signature: []byte("sig")},
		cosmos.AccAddress(pub.Address())
}

func (s *MsgQuorumCovSuite) TestQuorumSignerHelpers(c *C) {
	a1, api1, addr1 := newAttestationCov()
	a2, api2, addr2 := newAttestationCov()

	signers := quorumSignersCommon([]*common.Attestation{a1, a2})
	c.Assert(signers, HasLen, 2)
	c.Check(signers[0].Equals(addr1), Equals, true)
	c.Check(signers[1].Equals(addr2), Equals, true)

	raw := quorumSignersApiCommon([]*apicommon.Attestation{api1, api2})
	c.Assert(raw, HasLen, 2)
	c.Check([]byte(addr1), DeepEquals, raw[0])
	c.Check([]byte(addr2), DeepEquals, raw[1])

	c.Check(quorumSignersCommon(nil), HasLen, 0)
	c.Check(quorumSignersApiCommon(nil), HasLen, 0)
}

func (s *MsgQuorumCovSuite) TestMsgErrataTxQuorum(c *C) {
	att, _, addr := newAttestationCov()
	signer := GetRandomBech32Addr()
	good := &common.QuorumErrataTx{
		ErrataTx:     &common.ErrataTx{Id: GetRandomTxHash(), Chain: common.BTCChain},
		Attestations: []*common.Attestation{att},
	}
	msg := NewMsgErrataTxQuorum(good, signer)
	c.Assert(msg.QuoErrata, Equals, good)
	c.Assert(msg.Signer.Equals(signer), Equals, true)
	c.Assert(msg.ValidateBasic(), IsNil)
	c.Assert(msg.GetSigners(), HasLen, 1)
	c.Assert(msg.GetSigners()[0].Equals(addr), Equals, true)

	err := NewMsgErrataTxQuorum(good, cosmos.AccAddress{}).ValidateBasic()
	c.Assert(errors.Is(err, se.ErrInvalidAddress), Equals, true)

	cases := []*common.QuorumErrataTx{
		nil,
		{},
		{ErrataTx: &common.ErrataTx{Chain: common.BTCChain}},
		{ErrataTx: &common.ErrataTx{Id: GetRandomTxHash()}},
	}
	for i, q := range cases {
		err = NewMsgErrataTxQuorum(q, signer).ValidateBasic()
		c.Check(errors.Is(err, se.ErrUnknownRequest), Equals, true, Commentf("case %d", i))
	}
}

func (s *MsgQuorumCovSuite) TestMsgNetworkFeeQuorum(c *C) {
	att, _, addr := newAttestationCov()
	signer := GetRandomBech32Addr()
	nf := func() *common.NetworkFee {
		return &common.NetworkFee{Height: 10, Chain: common.BTCChain, TransactionSize: 250, TransactionRate: 10}
	}
	msg := NewMsgNetworkFeeQuorum(&common.QuorumNetworkFee{NetworkFee: nf(), Attestations: []*common.Attestation{att}}, signer)
	c.Assert(msg.ValidateBasic(), IsNil)
	c.Assert(msg.GetSigners(), HasLen, 1)
	c.Assert(msg.GetSigners()[0].Equals(addr), Equals, true)

	err := NewMsgNetworkFeeQuorum(&common.QuorumNetworkFee{NetworkFee: nf()}, cosmos.AccAddress{}).ValidateBasic()
	c.Assert(errors.Is(err, se.ErrInvalidAddress), Equals, true)

	mut := []func(*common.NetworkFee){
		func(n *common.NetworkFee) { n.Height = 0 },
		func(n *common.NetworkFee) { n.Chain = common.EmptyChain },
		func(n *common.NetworkFee) { n.TransactionSize = 0 },
		func(n *common.NetworkFee) { n.TransactionRate = 0 },
	}
	for i, m := range mut {
		f := nf()
		m(f)
		err = NewMsgNetworkFeeQuorum(&common.QuorumNetworkFee{NetworkFee: f}, signer).ValidateBasic()
		c.Check(errors.Is(err, se.ErrUnknownRequest), Equals, true, Commentf("case %d", i))
	}
	c.Assert(errors.Is(NewMsgNetworkFeeQuorum(nil, signer).ValidateBasic(), se.ErrUnknownRequest), Equals, true)
	c.Assert(errors.Is(NewMsgNetworkFeeQuorum(&common.QuorumNetworkFee{}, signer).ValidateBasic(), se.ErrUnknownRequest), Equals, true)
}

func (s *MsgQuorumCovSuite) TestMsgPriceFeedQuorum(c *C) {
	att, _, addr := newAttestationCov()
	att2, _, _ := newAttestationCov()
	signer := GetRandomBech32Addr()
	pf := func() *common.PriceFeed {
		return &common.PriceFeed{Time: 100, Rates: []*common.OraclePrice{{Amount: 1, Decimals: 8}}}
	}
	msg := NewMsgPriceFeedQuorum(&common.QuorumPriceFeed{PriceFeed: pf(), Attestations: []*common.Attestation{att}}, signer)
	c.Assert(msg.ValidateBasic(), IsNil)
	c.Assert(msg.GetSigners(), HasLen, 1)
	c.Assert(msg.GetSigners()[0].Equals(addr), Equals, true)

	bad := []*common.QuorumPriceFeed{
		{PriceFeed: &common.PriceFeed{Time: 0, Rates: pf().Rates}, Attestations: []*common.Attestation{att}},
		{PriceFeed: &common.PriceFeed{Time: 1}, Attestations: []*common.Attestation{att}},
		{PriceFeed: pf()},
		{PriceFeed: pf(), Attestations: []*common.Attestation{att, att2}},
	}
	for i, q := range bad {
		err := NewMsgPriceFeedQuorum(q, signer).ValidateBasic()
		c.Check(errors.Is(err, se.ErrUnknownRequest), Equals, true, Commentf("case %d", i))
	}
	err := NewMsgPriceFeedQuorum(&common.QuorumPriceFeed{PriceFeed: pf(), Attestations: []*common.Attestation{att}}, cosmos.AccAddress{}).ValidateBasic()
	c.Assert(errors.Is(err, se.ErrInvalidAddress), Equals, true)
}

func (s *MsgQuorumCovSuite) TestMsgPriceFeedQuorumBatch(c *C) {
	att, _, addr := newAttestationCov()
	att2, _, addr2 := newAttestationCov()
	signer := GetRandomBech32Addr()
	pf := func() *common.PriceFeed {
		return &common.PriceFeed{Time: 100, Rates: []*common.OraclePrice{{Amount: 1, Decimals: 8}}}
	}
	feeds := []*common.QuorumPriceFeed{
		{PriceFeed: pf(), Attestations: []*common.Attestation{att}},
		{PriceFeed: pf(), Attestations: []*common.Attestation{att2}},
	}
	msg := NewMsgPriceFeedQuorumBatch(feeds, signer)
	c.Assert(msg.ValidateBasic(), IsNil)
	signers := msg.GetSigners()
	c.Assert(signers, HasLen, 2)
	c.Check(signers[0].Equals(addr), Equals, true)
	c.Check(signers[1].Equals(addr2), Equals, true)

	c.Assert(errors.Is(NewMsgPriceFeedQuorumBatch(nil, signer).ValidateBasic(), se.ErrUnknownRequest), Equals, true)
	c.Assert(errors.Is(NewMsgPriceFeedQuorumBatch(feeds, cosmos.AccAddress{}).ValidateBasic(), se.ErrInvalidAddress), Equals, true)

	bad := []*common.QuorumPriceFeed{
		nil,
		{},
		{PriceFeed: &common.PriceFeed{Time: 0, Rates: pf().Rates}, Attestations: []*common.Attestation{att}},
		{PriceFeed: &common.PriceFeed{Time: 1}, Attestations: []*common.Attestation{att}},
		{PriceFeed: pf()},
		{PriceFeed: pf(), Attestations: []*common.Attestation{att, att2}},
	}
	for i, q := range bad {
		err := NewMsgPriceFeedQuorumBatch([]*common.QuorumPriceFeed{feeds[0], q}, signer).ValidateBasic()
		c.Check(errors.Is(err, se.ErrUnknownRequest), Equals, true, Commentf("case %d", i))
	}
}

func (s *MsgQuorumCovSuite) TestMsgSolvencyQuorum(c *C) {
	att, _, addr := newAttestationCov()
	signer := GetRandomBech32Addr()
	newSolvency := func() *common.Solvency {
		return &common.Solvency{
			Chain:  common.BTCChain,
			PubKey: GetRandomPubKey(),
			Coins:  common.NewCoins(common.NewCoin(common.BTCAsset, cosmos.NewUint(100))),
			Height: 10,
		}
	}
	q := &common.QuorumSolvency{Solvency: newSolvency(), Attestations: []*common.Attestation{att}}
	msg, err := NewMsgSolvencyQuorum(q, signer)
	c.Assert(err, IsNil)
	c.Assert(q.Solvency.Id.IsEmpty(), Equals, false)
	expectedID, err := q.Solvency.Hash()
	c.Assert(err, IsNil)
	c.Assert(q.Solvency.Id.Equals(expectedID), Equals, true)
	c.Assert(msg.ValidateBasic(), IsNil)
	c.Assert(msg.GetSigners(), HasLen, 1)
	c.Assert(msg.GetSigners()[0].Equals(addr), Equals, true)

	// nil quorum / solvency
	c.Assert(errors.Is((&MsgSolvencyQuorum{Signer: signer}).ValidateBasic(), se.ErrUnknownRequest), Equals, true)
	c.Assert(errors.Is((&MsgSolvencyQuorum{QuoSolvency: &common.QuorumSolvency{}, Signer: signer}).ValidateBasic(), se.ErrUnknownRequest), Equals, true)

	// attestations bounds
	m := &MsgSolvencyQuorum{QuoSolvency: &common.QuorumSolvency{Solvency: q.Solvency}, Signer: signer}
	c.Assert(m.ValidateBasic(), ErrorMatches, ".*no attestations found.*")
	many := make([]*common.Attestation, MaxSolvencyQuorumAttestations+1)
	for i := range many {
		many[i] = att
	}
	m.QuoSolvency.Attestations = many
	c.Assert(m.ValidateBasic(), ErrorMatches, ".*too many attestations.*")

	// field level validation
	withID := func(mut func(*common.Solvency)) *common.Solvency {
		sol := newSolvency()
		mut(sol)
		sol.Id, err = sol.Hash()
		c.Assert(err, IsNil)
		return sol
	}
	emptyID := newSolvency()
	staleID := withID(func(*common.Solvency) {})
	staleID.Height = 99 // id no longer matches content
	cases := []struct {
		sol   *common.Solvency
		match string
	}{
		{emptyID, ".*invalid id.*"},
		{staleID, ".*invalid id.*"},
		{withID(func(s *common.Solvency) { s.Chain = common.EmptyChain }), ".*chain can't be empty.*"},
		{withID(func(s *common.Solvency) { s.PubKey = common.EmptyPubKey }), ".*pubkey is empty.*"},
		{withID(func(s *common.Solvency) { s.Height = 0 }), ".*block height is invalid.*"},
	}
	for i, tc := range cases {
		mm := &MsgSolvencyQuorum{QuoSolvency: &common.QuorumSolvency{Solvency: tc.sol, Attestations: []*common.Attestation{att}}, Signer: signer}
		c.Check(mm.ValidateBasic(), ErrorMatches, tc.match, Commentf("case %d", i))
	}

	// empty signer
	sol := newSolvency()
	sol.Id, _ = sol.Hash()
	mm := &MsgSolvencyQuorum{QuoSolvency: &common.QuorumSolvency{Solvency: sol, Attestations: []*common.Attestation{att}}}
	c.Assert(errors.Is(mm.ValidateBasic(), se.ErrUnauthorized), Equals, true)
}

func (s *MsgQuorumCovSuite) TestCustomGetSigners(c *C) {
	signer := GetRandomBech32Addr()
	_, apiAtt, attAddr := newAttestationCov()
	atts := []*apicommon.Attestation{apiAtt}

	type tc struct {
		name   string
		fn     func(proto.Message) ([][]byte, error)
		msg    proto.Message
		expect [][]byte
	}
	single := [][]byte{signer}
	quorum := [][]byte{attAddr}
	cases := []tc{
		{"ban", MsgBanCustomGetSigners, &apitypes.MsgBan{Signer: signer}, single},
		{"deposit", MsgDepositCustomGetSigners, &apitypes.MsgDeposit{Signer: signer}, single},
		{"errata", MsgErrataCustomGetSigners, &apitypes.MsgErrataTx{Signer: signer}, single},
		{"errataQuorum", MsgErrataTxQuorumCustomGetSigners, &apitypes.MsgErrataTxQuorum{QuoErrata: &apicommon.QuorumErrataTx{Attestations: atts}}, quorum},
		{"mimir", MsgMimirCustomGetSigners, &apitypes.MsgMimir{Signer: signer}, single},
		{"modifyLimitSwap", MsgModifyLimitSwapCustomGetSigners, &apitypes.MsgModifyLimitSwap{Signer: signer}, single},
		{"networkFee", MsgNetworkFeeCustomGetSigners, &apitypes.MsgNetworkFee{Signer: signer}, single},
		{"networkFeeQuorum", MsgNetworkFeeQuorumCustomGetSigners, &apitypes.MsgNetworkFeeQuorum{QuoNetFee: &apicommon.QuorumNetworkFee{Attestations: atts}}, quorum},
		{"nodePauseChain", MsgNodePauseChainCustomGetSigners, &apitypes.MsgNodePauseChain{Signer: signer}, single},
		{"observedTxIn", MsgObservedTxInCustomGetSigners, &apitypes.MsgObservedTxIn{Signer: signer}, single},
		{"observedTxOut", MsgObservedTxOutCustomGetSigners, &apitypes.MsgObservedTxOut{Signer: signer}, single},
		{"observedTxQuorum", MsgObservedTxQuorumCustomGetSigners, &apitypes.MsgObservedTxQuorum{QuoTx: &apicommon.QuorumTx{Attestations: atts}}, quorum},
		{"send", MsgSendCustomGetSigners, &apitypes.MsgSend{FromAddress: signer}, single},
		{"setIP", MsgSetIPAddressCustomGetSigners, &apitypes.MsgSetIPAddress{Signer: signer}, single},
		{"setNodeKeys", MsgSetNodeKeysCustomGetSigners, &apitypes.MsgSetNodeKeys{Signer: signer}, single},
		{"solvency", MsgSolvencyCustomGetSigners, &apitypes.MsgSolvency{Signer: signer}, single},
		{"solvencyQuorum", MsgSolvencyQuorumCustomGetSigners, &apitypes.MsgSolvencyQuorum{QuoSolvency: &apicommon.QuorumSolvency{Attestations: atts}}, quorum},
		{"solNonceAttest", MsgSolNonceAttestCustomGetSigners, &apitypes.MsgSolNonceAttest{Signer: signer}, single},
		{"tssKeysignFail", MsgTssKeysignFailCustomGetSigners, &apitypes.MsgTssKeysignFail{Signer: signer}, single},
		{"tssPool", MsgTssPoolCustomGetSigners, &apitypes.MsgTssPool{Signer: signer}, single},
		{"setVersion", MsgSetVersionCustomGetSigners, &apitypes.MsgSetVersion{Signer: signer}, single},
		{"proposeUpgrade", MsgProposeUpgradeCustomGetSigners, &apitypes.MsgProposeUpgrade{Signer: signer}, single},
		{"approveUpgrade", MsgApproveUpgradeCustomGetSigners, &apitypes.MsgApproveUpgrade{Signer: signer}, single},
		{"rejectUpgrade", MsgRejectUpgradeCustomGetSigners, &apitypes.MsgRejectUpgrade{Signer: signer}, single},
		{"priceFeedQuorum", MsgPriceFeedQuorumCustomGetSigners, &apitypes.MsgPriceFeedQuorum{QuoPriceFeed: &apicommon.QuorumPriceFeed{Attestations: atts}}, quorum},
		{"priceFeedQuorumBatch", MsgPriceFeedQuorumBatchCustomGetSigners, &apitypes.MsgPriceFeedQuorumBatch{QuoPriceFeeds: []*apicommon.QuorumPriceFeed{{Attestations: atts}, {Attestations: atts}}}, [][]byte{attAddr, attAddr}},
		{"maint", MsgMaintCustomGetSigners, &apitypes.MsgMaint{Signer: signer}, single},
	}
	// a message of the wrong type must be rejected by every getter
	wrong := &apitypes.MsgNoOp{}
	for _, t := range cases {
		got, err := t.fn(t.msg)
		c.Assert(err, IsNil, Commentf(t.name))
		c.Assert(len(got), Equals, len(t.expect), Commentf(t.name))
		for i := range got {
			c.Check(got[i], DeepEquals, t.expect[i], Commentf(t.name))
		}
		got, err = t.fn(wrong)
		c.Check(err, NotNil, Commentf(t.name))
		c.Check(got, IsNil, Commentf(t.name))
	}
}

func (s *MsgQuorumCovSuite) TestDefineCustomGetSigners(c *C) {
	opts := &signing.Options{}
	DefineCustomGetSigners(opts)
	c.Assert(opts.CustomGetSigners, HasLen, 25)
	for _, name := range []string{"types.MsgBan", "types.MsgSend", "types.MsgSolvencyQuorum", "types.MsgPriceFeedQuorumBatch", "types.MsgObservedTxQuorum"} {
		_, ok := opts.CustomGetSigners[protoreflect.FullName(name)]
		c.Check(ok, Equals, true, Commentf(name))
	}
	// MsgMaint has a custom getter but it is intentionally not registered
	_, ok := opts.CustomGetSigners[protoreflect.FullName("types.MsgMaint")]
	c.Check(ok, Equals, false)

	// the registered function must actually be the one for the message
	signer := GetRandomBech32Addr()
	got, err := opts.CustomGetSigners[protoreflect.FullName("types.MsgBan")](&apitypes.MsgBan{Signer: signer})
	c.Assert(err, IsNil)
	c.Assert(got, DeepEquals, [][]byte{signer})
}
