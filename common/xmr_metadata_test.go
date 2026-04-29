package common

import . "gopkg.in/check.v1"

func (s CommonSuite) TestXMRMetadataRefsAndTxKeys(c *C) {
	c.Assert(NormalizeXMRTxHash(" ABCD "), Equals, "abcd")
	c.Assert(XMROutputRefKey(" ABCD ", 7), Equals, "abcd:7")

	c.Assert(NormalizeXMRTxKeys([]string{" B ", "", "a"}), DeepEquals, []string{"a", "b"})
	c.Assert(MergeXMRStrings([]string{" B ", "a"}, []string{"b", " C "}), DeepEquals, []string{"b", "a", "c"})
}

func (s CommonSuite) TestXMRMetadataKeyImageProofs(c *C) {
	proofs := []*XmrKeyImageProof{
		nil,
		{TxHash: "BBBB", OutputIndex: 1, KeyImage: " KI2 ", ProofC: " C2 ", ProofS: " S2 "},
		{TxHash: "AAAA", OutputIndex: 0, KeyImage: " KI1 ", ProofC: " C1 ", ProofS: " S1 "},
	}

	canonical := CanonicalXMRKeyImageProofs(proofs)
	c.Assert(canonical, HasLen, 2)
	c.Assert(canonical[0], DeepEquals, &XmrKeyImageProof{TxHash: "aaaa", OutputIndex: 0, KeyImage: "ki1", ProofC: "c1", ProofS: "s1"})
	c.Assert(canonical[1], DeepEquals, &XmrKeyImageProof{TxHash: "bbbb", OutputIndex: 1, KeyImage: "ki2", ProofC: "c2", ProofS: "s2"})

	merged := MergeXMRKeyImageProofs(canonical[:1], []*XmrKeyImageProof{
		{TxHash: "AAAA", OutputIndex: 0, KeyImage: "KI1", ProofC: "ignored", ProofS: "ignored"},
		{TxHash: "CCCC", OutputIndex: 2, KeyImage: "KI3", ProofC: "C3", ProofS: "S3"},
	})
	c.Assert(merged, HasLen, 2)
	c.Assert(merged[1].TxHash, Equals, "cccc")

	registered := []*XmrKeyImageRef{
		{TxHash: "AAAA", OutputIndex: 0},
		{TxHash: "BBBB", OutputIndex: 1},
	}
	c.Assert(XMRKeyImageProofsRegistered(canonical, registered), Equals, true)
	c.Assert(XMRKeyImageRefsContainTx(" bbbb ", registered), Equals, true)
	c.Assert(XMRKeyImageRefsContainTx("cccc", registered), Equals, false)
}
