package thorchain

import (
	"fmt"

	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

type MemoIntentSuite struct {
	MemoSuite
}

var _ = Suite(&MemoIntentSuite{})

func (s *MemoIntentSuite) TestIntentOpenMemo_Parse(c *C) {
	// Happy path with all fields
	// Format: IO:ASSET:HIGH:LOW:TARGET_ADDR:AFFILIATE:FEE_BPS
	btcAddr := types.GetRandomBTCAddress()
	thorAddr := types.GetRandomTHORAddress()
	memoStr := fmt.Sprintf("IO:BTC.BTC:1500000000:1000000000:%s:%s:100", btcAddr, thorAddr)
	m, err := ParseMemoWithTHORNames(s.ctx, s.k, memoStr)
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

func (s *MemoIntentSuite) TestIntentOpenMemo_Parse_NoAffiliate(c *C) {
	// Without affiliate fields
	// Format: IO:ASSET:HIGH:LOW:TARGET_ADDR
	btcAddr := types.GetRandomBTCAddress()
	memoStr := fmt.Sprintf("IO:BTC.BTC:1500000000:1000000000:%s", btcAddr)
	m, err := ParseMemoWithTHORNames(s.ctx, s.k, memoStr)
	c.Assert(err, IsNil)

	intentMemo, ok := m.(IntentOpenMemo)
	c.Assert(ok, Equals, true)
	c.Check(intentMemo.GetTargetAsset().Equals(common.BTCAsset), Equals, true)
	c.Check(intentMemo.GetHighTarget().Equal(cosmos.NewUint(1500000000)), Equals, true)
	c.Check(intentMemo.GetLowTarget().Equal(cosmos.NewUint(1000000000)), Equals, true)
	c.Check(intentMemo.GetAffiliateAddress().IsEmpty(), Equals, true)
	c.Check(intentMemo.GetAffiliateBasisPoints().IsZero(), Equals, true)
}

func (s *MemoIntentSuite) TestIntentOpenMemo_String(c *C) {
	// Test String() serialization
	targetAsset, err := common.NewAsset("BTC.BTC")
	c.Assert(err, IsNil)
	btcAddr := types.GetRandomBTCAddress()

	memo := NewIntentOpenMemo(
		targetAsset,
		btcAddr,
		cosmos.NewUint(1500000000),
		cosmos.NewUint(1000000000),
		common.NoAddress,
		cosmos.ZeroUint(),
	)

	str := memo.String()
	// TxType.String() returns lowercase, format is IO:ASSET:HIGH:LOW:ADDR
	expectedStr := fmt.Sprintf("io:BTC.BTC:1500000000:1000000000:%s", btcAddr)
	c.Check(str, Equals, expectedStr)
}

func (s *MemoIntentSuite) TestIntentOpenMemo_String_WithAffiliate(c *C) {
	targetAsset, err := common.NewAsset("BTC.BTC")
	c.Assert(err, IsNil)
	btcAddr := types.GetRandomBTCAddress()
	thorAddr := types.GetRandomTHORAddress()

	memo := NewIntentOpenMemo(
		targetAsset,
		btcAddr,
		cosmos.NewUint(1500000000),
		cosmos.NewUint(1000000000),
		thorAddr,
		cosmos.NewUint(100),
	)

	str := memo.String()
	// TxType.String() returns lowercase, format is IO:ASSET:HIGH:LOW:ADDR:AFFILIATE:FEE
	expectedStr := fmt.Sprintf("io:BTC.BTC:1500000000:1000000000:%s:%s:100", btcAddr, thorAddr)
	c.Check(str, Equals, expectedStr)
}

func (s *MemoIntentSuite) TestIntentCancelMemo_Parse(c *C) {
	memoStr := "IC:ABCD1234567890ABCD1234567890ABCD1234567890ABCD1234567890ABCD1234"
	m, err := ParseMemoWithTHORNames(s.ctx, s.k, memoStr)
	c.Assert(err, IsNil)
	c.Check(m.GetType(), Equals, TxIntentCancel)

	cancelMemo, ok := m.(IntentCancelMemo)
	c.Assert(ok, Equals, true)
	c.Check(cancelMemo.GetIntentTxID().IsEmpty(), Equals, false)
}

func (s *MemoIntentSuite) TestIntentCancelMemo_String(c *C) {
	txID := common.TxID("ABCD1234567890ABCD1234567890ABCD1234567890ABCD1234567890ABCD1234")
	memo := NewIntentCancelMemo(txID)

	str := memo.String()
	c.Check(str, Equals, "ic:ABCD1234567890ABCD1234567890ABCD1234567890ABCD1234567890ABCD1234")
}

func (s *MemoIntentSuite) TestIntentSolveMemo_Parse(c *C) {
	memoStr := "IS:ABCD1234567890ABCD1234567890ABCD1234567890ABCD1234567890ABCD1234"
	m, err := ParseMemoWithTHORNames(s.ctx, s.k, memoStr)
	c.Assert(err, IsNil)
	c.Check(m.GetType(), Equals, TxIntentSolve)

	solveMemo, ok := m.(IntentSolveMemo)
	c.Assert(ok, Equals, true)
	c.Check(solveMemo.GetIntentTxID().IsEmpty(), Equals, false)
}

func (s *MemoIntentSuite) TestIntentSolveMemo_String(c *C) {
	txID := common.TxID("ABCD1234567890ABCD1234567890ABCD1234567890ABCD1234567890ABCD1234")
	memo := NewIntentSolveMemo(txID)

	str := memo.String()
	c.Check(str, Equals, "is:ABCD1234567890ABCD1234567890ABCD1234567890ABCD1234567890ABCD1234")
}

func (s *MemoIntentSuite) TestIntentOpenMemo_InvalidFormats(c *C) {
	btcAddr := types.GetRandomBTCAddress()

	// Missing required fields
	_, err := ParseMemoWithTHORNames(s.ctx, s.k, "IO:BTC.BTC")
	c.Check(err, NotNil)

	// Scientific notation - integers only, no decimals
	sciMemo := fmt.Sprintf("IO:BTC.BTC:15e8:1000000000:%s", btcAddr)
	m, err := ParseMemoWithTHORNames(s.ctx, s.k, sciMemo)
	c.Check(err, IsNil) // Should parse scientific notation with integer coefficient
	intentMemo, ok := m.(IntentOpenMemo)
	c.Assert(ok, Equals, true)
	c.Check(intentMemo.GetHighTarget().GT(cosmos.ZeroUint()), Equals, true)
}
