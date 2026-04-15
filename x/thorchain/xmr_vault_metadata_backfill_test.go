package thorchain

import (
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

type XMRVaultMetadataBackfillSuite struct{}

var _ = Suite(&XMRVaultMetadataBackfillSuite{})

func (s *XMRVaultMetadataBackfillSuite) TestBackfillXMRVaultMetadataFromHistoricalObservedVoters(c *C) {
	ctx, mgr := setupManagerForTest(c)

	outboundVaultPubKey := GetRandomPubKey()
	outboundVault := NewVault(1, ActiveVault, AsgardVault, outboundVaultPubKey, []string{
		common.XMRChain.String(),
	}, nil)
	outboundVault.AddFunds(common.Coins{
		common.NewCoin(common.XMRAsset, cosmos.NewUint(10*common.One)),
	})
	c.Assert(mgr.Keeper().SetVault(ctx, outboundVault), IsNil)

	inboundVaultPubKey := GetRandomPubKey()
	inboundVault := NewVault(1, ActiveVault, AsgardVault, inboundVaultPubKey, []string{
		common.XMRChain.String(),
	}, nil)
	c.Assert(mgr.Keeper().SetVault(ctx, inboundVault), IsNil)

	outboundTxID := GetRandomTxHash()
	outboundBase := common.ObservedTx{
		Tx: common.Tx{
			ID:          outboundTxID,
			Chain:       common.XMRChain,
			FromAddress: common.Address(common.XMRSeededDummyAddress('4', []byte("vault-out"))),
			ToAddress:   common.Address(common.XMRSeededDummyAddress('8', []byte("recipient"))),
			Coins: common.Coins{
				common.NewCoin(common.XMRAsset, cosmos.NewUint(common.One)),
			},
			Gas: common.Gas{
				common.NewCoin(common.XMRAsset, cosmos.NewUint(1000)),
			},
			Memo: "OUT:" + GetRandomTxHash().String(),
		},
		BlockHeight:    100,
		FinaliseHeight: 100,
		ObservedPubKey: outboundVaultPubKey,
	}
	outboundWithMetadata := outboundBase
	outboundWithMetadata.TxKeys = []string{"tx-key-1"}
	outboundWithMetadata.XmrKeyImages = []*common.XmrKeyImageProof{
		{TxHash: outboundTxID.String(), OutputIndex: 1, KeyImage: "ki-out", ProofC: "c1", ProofS: "s1"},
	}
	mgr.Keeper().SetObservedTxOutVoter(ctx, ObservedTxVoter{
		TxID:            outboundTxID,
		Tx:              outboundBase,
		Txs:             common.ObservedTxs{outboundBase, outboundWithMetadata},
		Height:          100,
		FinalisedHeight: 100,
	})

	inboundTxID := GetRandomTxHash()
	inboundBase := common.ObservedTx{
		Tx: common.Tx{
			ID:          inboundTxID,
			Chain:       common.XMRChain,
			FromAddress: common.Address(common.XMRSeededDummyAddress('8', []byte("sender"))),
			ToAddress:   common.Address(common.XMRSeededDummyAddress('4', []byte("vault-in"))),
			Coins: common.Coins{
				common.NewCoin(common.XMRAsset, cosmos.NewUint(2*common.One)),
			},
			Gas: common.Gas{
				common.NewCoin(common.XMRAsset, cosmos.NewUint(1000)),
			},
			Memo: "ADD:XMR.XMR:" + GetRandomBech32Addr().String(),
		},
		BlockHeight:    120,
		FinaliseHeight: 120,
		ObservedPubKey: inboundVaultPubKey,
	}
	inboundWithMetadata := inboundBase
	inboundWithMetadata.XmrKeyImages = []*common.XmrKeyImageProof{
		{TxHash: inboundTxID.String(), OutputIndex: 0, KeyImage: "ki-in", ProofC: "c2", ProofS: "s2"},
	}
	mgr.Keeper().SetObservedTxInVoter(ctx, ObservedTxVoter{
		TxID:            inboundTxID,
		Tx:              inboundBase,
		Txs:             common.ObservedTxs{inboundBase, inboundWithMetadata},
		Height:          120,
		FinalisedHeight: 120,
	})

	stats, err := backfillXMRVaultMetadata(ctx, mgr)
	c.Assert(err, IsNil)
	c.Assert(stats.vaultsTouched, Equals, 2)
	c.Assert(stats.keyImagesAdded, Equals, 2)
	c.Assert(stats.txKeyRefsAdded, Equals, 1)
	c.Assert(stats.txKeysAdded, Equals, 1)

	outboundVault, err = mgr.Keeper().GetVault(ctx, outboundVaultPubKey)
	c.Assert(err, IsNil)
	c.Assert(outboundVault.KeyImagesXmr, HasLen, 1)
	c.Assert(outboundVault.KeyImagesXmr[0].TxHash, Equals, outboundTxID.String())
	c.Assert(outboundVault.KeyImagesXmr[0].OutputIndex, Equals, uint32(1))
	c.Assert(outboundVault.KeyImagesXmr[0].KeyImage, Equals, "ki-out")
	c.Assert(outboundVault.TxKeysXmr, HasLen, 1)
	c.Assert(outboundVault.TxKeysXmr[0].TxHash, Equals, outboundTxID.String())
	c.Assert(outboundVault.TxKeysXmr[0].TxKeys, DeepEquals, []string{"tx-key-1"})

	inboundVault, err = mgr.Keeper().GetVault(ctx, inboundVaultPubKey)
	c.Assert(err, IsNil)
	c.Assert(inboundVault.KeyImagesXmr, HasLen, 1)
	c.Assert(inboundVault.KeyImagesXmr[0].TxHash, Equals, inboundTxID.String())
	c.Assert(inboundVault.KeyImagesXmr[0].OutputIndex, Equals, uint32(0))
	c.Assert(inboundVault.KeyImagesXmr[0].KeyImage, Equals, "ki-in")

	stats, err = backfillXMRVaultMetadata(ctx, mgr)
	c.Assert(err, IsNil)
	c.Assert(stats.vaultsTouched, Equals, 0)
	c.Assert(stats.keyImagesAdded, Equals, 0)
	c.Assert(stats.txKeyRefsAdded, Equals, 0)
	c.Assert(stats.txKeysAdded, Equals, 0)
}
