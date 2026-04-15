package thorchain

import (
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
)

type XMRKeyImagesFlowSuite struct{}

var _ = Suite(&XMRKeyImagesFlowSuite{})

func (s *XMRKeyImagesFlowSuite) TestAppendVaultXMRKeyImagesDedupesByOutputRef(c *C) {
	vault := Vault{
		KeyImagesXmr: []*common.XmrKeyImageRef{
			{TxHash: "aaa", OutputIndex: 0, KeyImage: "ki-aaa"},
		},
	}

	appendVaultXMRKeyImages(&vault, []*common.XmrKeyImageProof{
		{TxHash: "aaa", OutputIndex: 0, KeyImage: "ki-aaa-new", ProofC: "c0", ProofS: "s0"},
		{TxHash: "bbb", OutputIndex: 1, KeyImage: "ki-bbb", ProofC: "c1", ProofS: "s1"},
	})

	c.Assert(vault.KeyImagesXmr, HasLen, 2)
	c.Assert(vault.KeyImagesXmr[0].TxHash, Equals, "aaa")
	c.Assert(vault.KeyImagesXmr[1].TxHash, Equals, "bbb")
	c.Assert(vault.KeyImagesXmr[1].KeyImage, Equals, "ki-bbb")
}

func (s *XMRKeyImagesFlowSuite) TestRemoveVaultXMRKeyImagesByOutputRef(c *C) {
	vault := Vault{
		KeyImagesXmr: []*common.XmrKeyImageRef{
			{TxHash: "aaa", OutputIndex: 0, KeyImage: "ki-aaa"},
			{TxHash: "bbb", OutputIndex: 1, KeyImage: "ki-bbb"},
		},
	}

	removed := removeVaultXMRKeyImages(&vault, []*common.XmrKeyImageProof{
		{TxHash: "bbb", OutputIndex: 1, KeyImage: "different-ki", ProofC: "c1", ProofS: "s1"},
	})

	c.Assert(removed, Equals, true)
	c.Assert(vault.KeyImagesXmr, HasLen, 1)
	c.Assert(vault.KeyImagesXmr[0].TxHash, Equals, "aaa")
}
