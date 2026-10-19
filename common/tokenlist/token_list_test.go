package tokenlist

import (
	"testing"

	"gitlab.com/thorchain/thornode/v3/common"
	. "gopkg.in/check.v1"
)

func TestPackage(t *testing.T) { TestingT(t) }

type TokenListSuite struct{}

var _ = Suite(&TokenListSuite{})

func (s *TokenListSuite) TestERC20TokenAsset(c *C) {
	token := ERC20Token{
		Address:  "0x1234567890abcdef",
		Symbol:   "TKN",
		Name:     "TestToken",
		Decimals: 18,
	}
	asset := token.Asset(common.ETHChain)
	c.Check(asset.Chain, Equals, common.ETHChain)
	c.Check(asset.Ticker, Equals, common.Ticker("TKN"))
	c.Check(asset.Symbol, Equals, common.Symbol("TKN-0X1234567890ABCDEF"))
}

func (s *TokenListSuite) TestGetEVMTokenListETH(c *C) {
	list := GetEVMTokenList(common.ETHChain)
	c.Check(list.Name, Not(Equals), "")
	c.Check(len(list.Tokens) > 0, Equals, true)
}

func (s *TokenListSuite) TestGetEVMTokenListAVAX(c *C) {
	list := GetEVMTokenList(common.AVAXChain)
	c.Check(list.Name, Not(Equals), "")
	c.Check(len(list.Tokens) > 0, Equals, true)
}

func (s *TokenListSuite) TestGetEVMTokenListBSC(c *C) {
	list := GetEVMTokenList(common.BSCChain)
	// BSC mocknet has empty name but tokens exist
	c.Check(len(list.Tokens) > 0, Equals, true)
}

func (s *TokenListSuite) TestGetEVMTokenListBASE(c *C) {
	list := GetEVMTokenList(common.BASEChain)
	c.Check(list.Name, Not(Equals), "")
	c.Check(len(list.Tokens) > 0, Equals, true)
}

func (s *TokenListSuite) TestGetEVMTokenListTRON(c *C) {
	list := GetEVMTokenList(common.TRONChain)
	c.Check(list.Name, Not(Equals), "")
	c.Check(len(list.Tokens) > 0, Equals, true)
}

func (s *TokenListSuite) TestGetEVMTokenListPOL(c *C) {
	list := GetEVMTokenList(common.POLChain)
	c.Check(list.Name, Not(Equals), "")
}

func (s *TokenListSuite) TestGetEVMTokenListSOL(c *C) {
	list := GetEVMTokenList(common.SOLChain)
	c.Check(list.Name, Not(Equals), "")
	c.Assert(len(list.Tokens) > 0, Equals, true)
	for _, token := range list.Tokens {
		// Solana mints are case-sensitive base58, never hex.
		c.Check(token.Address, Not(Matches), "0x.*")
		// Bifrost refuses 0-decimal mints (THORChain reads 0 as "unset").
		c.Check(token.Decimals >= 1 && token.Decimals <= 18, Equals, true)
	}

	usdc := ERC20Token{Address: "EPjFWdd5AufqSSqeM2qN1xzybapC8G4wEGGkZwyTDt1v", Symbol: "USDC", Decimals: 6}
	asset := usdc.Asset(common.SOLChain)
	c.Check(asset.String(), Equals, "SOL.USDC-EPJFWDD5AUFQSSQEM2QN1XZYBAPC8G4WEGGKZWYTDT1V")
}

func (s *TokenListSuite) TestGetEVMTokenListDefault(c *C) {
	list := GetEVMTokenList(common.BTCChain)
	c.Check(list.Name, Equals, "")
	c.Check(len(list.Tokens), Equals, 0)
}

func (s *TokenListSuite) TestGetAVAXTokenList(c *C) {
	list := GetAVAXTokenList()
	c.Check(list.Name, Not(Equals), "")
	c.Check(len(list.Tokens) > 0, Equals, true)
}

func (s *TokenListSuite) TestGetBASETokenList(c *C) {
	list := GetBASETokenList()
	c.Check(list.Name, Not(Equals), "")
	c.Check(len(list.Tokens) > 0, Equals, true)
}

func (s *TokenListSuite) TestGetBSCTokenList(c *C) {
	list := GetBSCTokenList()
	// BSC mocknet has empty name but tokens exist
	c.Check(len(list.Tokens) > 0, Equals, true)
}

func (s *TokenListSuite) TestGetPOLTokenList(c *C) {
	list := GetPOLTokenList()
	c.Check(list.Name, Not(Equals), "")
}

func (s *TokenListSuite) TestGetTRONTokenList(c *C) {
	list := GetTRONTokenList()
	c.Check(list.Name, Not(Equals), "")
	c.Check(len(list.Tokens) > 0, Equals, true)
}
