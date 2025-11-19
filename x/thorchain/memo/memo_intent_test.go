package thorchain

import (
	"github.com/blang/semver"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

type MemoIntentSuite struct{}

var _ = Suite(&MemoIntentSuite{})

func (MemoIntentSuite) TestIntentOpenMemo_Parse(c *C) {
	// Happy path with all fields
	memo := "IO:BTC.BTC:1500000000:1000000000:bc1qtest:thor1test:100"
	ctx, mgr := setupManagerForTest(c)
	p, err := newParser(ctx, mgr.Keeper(), semver.MustParse("999.0.0"), memo)
	c.Assert(err, IsNil)
	m, err := p.ParseMemoString(memo)
	c.Assert(err, IsNil)
	c.Check(m.GetType(), Equals, TxIntentOpen)

	intentMemo, ok := m.(IntentOpenMemo)
	c.Assert(ok, Equals, true)
	c.Check(intentMemo.GetTargetAsset().Equals(common.BTCAsset), Equals, true)
	c.Check(intentMemo.GetHighTarget().Equal(cosmos.NewUint(1500000000)), Equals, true)
	c.Check(intentMemo.GetLowTarget().Equal(cosmos.NewUint(1000000000)), Equals, true)
	c.Check(intentMemo.GetTargetAddress().IsEmpty(), Equals, false)
	c.Check(intentMemo.GetAffiliateAddress().IsEmpty(), Equals, false)
	c.Check(intentMemo.GetAffiliateBasisPoints().Equal(cosmos.NewUint(100)), Equals, true)
}

func (MemoIntentSuite) TestIntentOpenMemo_Parse_NoAffiliate(c *C) {
	// Without affiliate fields
	memo := "IO:BTC.BTC:1500000000:1000000000:bc1qtest"
	ctx, mgr := setupManagerForTest(c)
	p, err := newParser(ctx, mgr.Keeper(), semver.MustParse("999.0.0"), memo)
	c.Assert(err, IsNil)
	m, err := p.ParseMemoString(memo)
	c.Assert(err, IsNil)

	intentMemo, ok := m.(IntentOpenMemo)
	c.Assert(ok, Equals, true)
	c.Check(intentMemo.GetTargetAsset().Equals(common.BTCAsset), Equals, true)
	c.Check(intentMemo.GetHighTarget().Equal(cosmos.NewUint(1500000000)), Equals, true)
	c.Check(intentMemo.GetLowTarget().Equal(cosmos.NewUint(1000000000)), Equals, true)
	c.Check(intentMemo.GetAffiliateAddress().IsEmpty(), Equals, true)
	c.Check(intentMemo.GetAffiliateBasisPoints().IsZero(), Equals, true)
}

func (MemoIntentSuite) TestIntentOpenMemo_String(c *C) {
	// Test String() serialization
	targetAsset, err := common.NewAsset("BTC.BTC")
	c.Assert(err, IsNil)

	memo := NewIntentOpenMemo(
		targetAsset,
		common.Address("bc1qtest"),
		cosmos.NewUint(1500000000),
		cosmos.NewUint(1000000000),
		common.NoAddress,
		cosmos.ZeroUint(),
	)

	str := memo.String()
	c.Check(str, Equals, "IO:BTC.BTC:1500000000:1000000000:bc1qtest")
}

func (MemoIntentSuite) TestIntentOpenMemo_String_WithAffiliate(c *C) {
	targetAsset, err := common.NewAsset("BTC.BTC")
	c.Assert(err, IsNil)

	memo := NewIntentOpenMemo(
		targetAsset,
		common.Address("bc1qtest"),
		cosmos.NewUint(1500000000),
		cosmos.NewUint(1000000000),
		common.Address("thor1affiliate"),
		cosmos.NewUint(100),
	)

	str := memo.String()
	c.Check(str, Equals, "IO:BTC.BTC:1500000000:1000000000:bc1qtest:thor1affiliate:100")
}

func (MemoIntentSuite) TestIntentCancelMemo_Parse(c *C) {
	memo := "IC:ABCD1234567890ABCD1234567890ABCD1234567890ABCD1234567890ABCD1234"
	ctx, mgr := setupManagerForTest(c)
	p, err := newParser(ctx, mgr.Keeper(), semver.MustParse("999.0.0"), memo)
	c.Assert(err, IsNil)
	m, err := p.ParseMemoString(memo)
	c.Assert(err, IsNil)
	c.Check(m.GetType(), Equals, TxIntentCancel)

	cancelMemo, ok := m.(IntentCancelMemo)
	c.Assert(ok, Equals, true)
	c.Check(cancelMemo.GetIntentTxID().IsEmpty(), Equals, false)
}

func (MemoIntentSuite) TestIntentCancelMemo_String(c *C) {
	txID := common.TxID("ABCD1234567890ABCD1234567890ABCD1234567890ABCD1234567890ABCD1234")
	memo := NewIntentCancelMemo(txID)

	str := memo.String()
	c.Check(str, Equals, "IC:ABCD1234567890ABCD1234567890ABCD1234567890ABCD1234567890ABCD1234")
}

func (MemoIntentSuite) TestIntentSolveMemo_Parse(c *C) {
	memo := "IS:ABCD1234567890ABCD1234567890ABCD1234567890ABCD1234567890ABCD1234"
	ctx, mgr := setupManagerForTest(c)
	p, err := newParser(ctx, mgr.Keeper(), semver.MustParse("999.0.0"), memo)
	c.Assert(err, IsNil)
	m, err := p.ParseMemoString(memo)
	c.Assert(err, IsNil)
	c.Check(m.GetType(), Equals, TxIntentSolve)

	solveMemo, ok := m.(IntentSolveMemo)
	c.Assert(ok, Equals, true)
	c.Check(solveMemo.GetIntentTxID().IsEmpty(), Equals, false)
}

func (MemoIntentSuite) TestIntentSolveMemo_String(c *C) {
	txID := common.TxID("ABCD1234567890ABCD1234567890ABCD1234567890ABCD1234567890ABCD1234")
	memo := NewIntentSolveMemo(txID)

	str := memo.String()
	c.Check(str, Equals, "IS:ABCD1234567890ABCD1234567890ABCD1234567890ABCD1234567890ABCD1234")
}

func (MemoIntentSuite) TestIntentOpenMemo_InvalidFormats(c *C) {
	ctx, mgr := setupManagerForTest(c)

	// Missing required fields
	p1, err := newParser(ctx, mgr.Keeper(), semver.MustParse("999.0.0"), "IO:BTC.BTC")
	c.Assert(err, IsNil)
	_, err = p1.ParseMemoString("IO:BTC.BTC")
	c.Check(err, NotNil)

	// Invalid asset
	p2, err := newParser(ctx, mgr.Keeper(), semver.MustParse("999.0.0"), "IO:INVALID:1500000000:1000000000:bc1qtest")
	c.Assert(err, IsNil)
	_, err = p2.ParseMemoString("IO:INVALID:1500000000:1000000000:bc1qtest")
	c.Check(err, NotNil)

	// Invalid uint (scientific notation should be handled)
	p3, err := newParser(ctx, mgr.Keeper(), semver.MustParse("999.0.0"), "IO:BTC.BTC:1.5e9:1000000000:bc1qtest")
	c.Assert(err, IsNil)
	_, err = p3.ParseMemoString("IO:BTC.BTC:1.5e9:1000000000:bc1qtest")
	c.Check(err, IsNil) // Should parse scientific notation
}
