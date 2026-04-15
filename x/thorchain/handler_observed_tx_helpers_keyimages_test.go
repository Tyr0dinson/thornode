package thorchain

import (
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
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

func (s *HandlerObservedTxHelpersSuite) TestHandleObservedTxOutQuorumStoresXMRKeyImagesOnVault(c *C) {
	ctx, mgr := setupManagerForTest(c)

	vaultPubKey := GetRandomPubKey()
	vault := NewVault(1, ActiveVault, AsgardVault, vaultPubKey, []string{
		common.XMRChain.String(),
	}, []ChainContract{})
	vault.AddFunds(common.Coins{
		common.NewCoin(common.XMRAsset, cosmos.NewUint(5*common.One)),
	})
	c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)

	txID := GetRandomTxHash()
	observedTx := common.ObservedTx{
		Tx: common.Tx{
			ID:          txID,
			Chain:       common.XMRChain,
			FromAddress: common.Address(common.XMRSeededDummyAddress('4', []byte("vault"))),
			ToAddress:   common.Address(common.XMRSeededDummyAddress('8', []byte("recipient"))),
			Coins: common.Coins{
				common.NewCoin(common.XMRAsset, cosmos.NewUint(common.One)),
			},
			Gas: common.Gas{
				common.NewCoin(common.XMRAsset, cosmos.NewUint(1000)),
			},
			Memo: "OUT:" + GetRandomTxHash().String(),
		},
		BlockHeight:    1024,
		FinaliseHeight: 1024,
		ObservedPubKey: vaultPubKey,
		TxKeys:         []string{"key-a"},
		XmrKeyImages: []*common.XmrKeyImageProof{
			{TxHash: txID.String(), OutputIndex: 0, KeyImage: "ki-a", ProofC: "c0", ProofS: "s0"},
		},
	}
	voter := ObservedTxVoter{
		TxID:            txID,
		Tx:              observedTx,
		Txs:             common.ObservedTxs{observedTx},
		Height:          observedTx.BlockHeight,
		FinalisedHeight: observedTx.BlockHeight,
	}

	err := handleObservedTxOutQuorum(
		ctx,
		mgr,
		cosmos.AccAddress{},
		NodeAccounts{},
		func(cosmos.Context, cosmos.Msg) (*cosmos.Result, error) { return nil, errKaboom },
		observedTx,
		voter,
		nil,
		true,
	)
	c.Assert(err, IsNil)

	vault, err = mgr.Keeper().GetVault(ctx, vaultPubKey)
	c.Assert(err, IsNil)
	c.Assert(vault.KeyImagesXmr, HasLen, 1)
	c.Assert(vault.KeyImagesXmr[0].TxHash, Equals, txID.String())
	c.Assert(vault.KeyImagesXmr[0].OutputIndex, Equals, uint32(0))
	c.Assert(vault.KeyImagesXmr[0].KeyImage, Equals, "ki-a")
}
