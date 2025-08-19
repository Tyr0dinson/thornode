package types

import (
	"gitlab.com/thorchain/thornode/v3/common"
	cosmos "gitlab.com/thorchain/thornode/v3/common/cosmos"
	. "gopkg.in/check.v1"
)

type TxOutTestSuite struct{}

var _ = Suite(&TxOutTestSuite{})

func (TxOutTestSuite) TestTxOutItemHash(c *C) {
	item := TxOutItem{
		Chain:       "ETH",
		ToAddress:   "0x90f2b1ae50e6018230e90a33f98c7844a0ab635a",
		VaultPubKey: "",
		Coins: common.Coins{
			common.NewCoin(common.ETHAsset, cosmos.NewUint(194765912)),
		},
		Memo:   "REFUND:9999A5A08D8FCF942E1AAAA01AB1E521B699BA3A009FA0591C011DC1FFDC5E68",
		InHash: "9999A5A08D8FCF942E1AAAA01AB1E521B699BA3A009FA0591C011DC1FFDC5E68",
		TxType: common.TxRefund,
	}
	c.Check(item.Hash(), Equals, "844C911F7FB93ACE9E3D69FC6F362519653A68D9B5606F3AC2E9B3FBCC29A6E2")

	item = TxOutItem{
		Chain:       "ETH",
		ToAddress:   "0x90f2b1ae50e6018230e90a33f98c7844a0ab635a",
		VaultPubKey: "",
		Memo:        "REFUND:9999A5A08D8FCF942E1AAAA01AB1E521B699BA3A009FA0591C011DC1FFDC5E68",
		InHash:      "9999A5A08D8FCF942E1AAAA01AB1E521B699BA3A009FA0591C011DC1FFDC5E68",
		TxType:      common.TxRefund,
	}
	c.Check(item.Hash(), Equals, "15D1C5EFF2FC5784BE0B03CFB93E9FB5F8077AF70721DEC2479478E5603B4CB9")

	item = TxOutItem{
		Chain:       "ETH",
		ToAddress:   "0x90f2b1ae50e6018230e90a33f98c7844a0ab635a",
		VaultPubKey: "thorpub1addwnpepqv7kdf473gc4jyls7hlx4rg",
		Memo:        "REFUND:9999A5A08D8FCF942E1AAAA01AB1E521B699BA3A009FA0591C011DC1FFDC5E68",
		InHash:      "9999A5A08D8FCF942E1AAAA01AB1E521B699BA3A009FA0591C011DC1FFDC5E68",
		TxType:      common.TxRefund,
	}
	c.Check(item.Hash(), Equals, "23B12B02AD3E21E347D1CF847F890E5FBA7C51C75C33D09D380E33285F868E0D")
}

func (TxOutTestSuite) TestTxOutItemEqualsShouldIgnoreHeight(c *C) {
	item1 := TxOutItem{
		Chain:       "ETH",
		ToAddress:   "0x90f2b1ae50e6018230e90a33f98c7844a0ab635a",
		VaultPubKey: "",
		Coins: common.Coins{
			common.NewCoin(common.ETHAsset, cosmos.NewUint(194765912)),
		},
		Memo:   "REFUND:9999A5A08D8FCF942E1AAAA01AB1E521B699BA3A009FA0591C011DC1FFDC5E68",
		InHash: "9999A5A08D8FCF942E1AAAA01AB1E521B699BA3A009FA0591C011DC1FFDC5E68",
		TxType: common.TxRefund,
	}
	item2 := TxOutItem{
		Chain:       "ETH",
		ToAddress:   "0x90f2b1ae50e6018230e90a33f98c7844a0ab635a",
		VaultPubKey: "",
		Coins: common.Coins{
			common.NewCoin(common.ETHAsset, cosmos.NewUint(194765912)),
		},
		Memo:   "REFUND:9999A5A08D8FCF942E1AAAA01AB1E521B699BA3A009FA0591C011DC1FFDC5E68",
		InHash: "9999A5A08D8FCF942E1AAAA01AB1E521B699BA3A009FA0591C011DC1FFDC5E68",
		TxType: common.TxRefund,
	}
	c.Check(item1.Equals(item2), Equals, true)

	item1.Height = 1
	item2.Height = 2
	c.Check(item1.Equals(item2), Equals, true)
}
