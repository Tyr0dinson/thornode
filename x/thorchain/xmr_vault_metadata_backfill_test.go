package thorchain

import (
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

type XMRVaultMetadataBackfillSuite struct{}

var _ = Suite(&XMRVaultMetadataBackfillSuite{})

func setTestXMRBackfillVault(c *C, ctx cosmos.Context, mgr *Mgrs, pubKey common.PubKey, amount cosmos.Uint) Vault {
	vault := NewVault(1, ActiveVault, AsgardVault, pubKey, []string{
		common.XMRChain.String(),
	}, nil)
	prepareTestXMRVault(c, &vault)
	if !amount.IsZero() {
		vault.AddFunds(common.Coins{
			common.NewCoin(common.XMRAsset, amount),
		})
	}
	c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)
	return vault
}

func newTestXMRBackfillObservedTx(
	txID common.TxID,
	observedPubKey common.PubKey,
	from common.Address,
	to common.Address,
	amount cosmos.Uint,
	memo string,
	height int64,
) common.ObservedTx {
	return common.ObservedTx{
		Tx: common.Tx{
			ID:          txID,
			Chain:       common.XMRChain,
			FromAddress: from,
			ToAddress:   to,
			Coins: common.Coins{
				common.NewCoin(common.XMRAsset, amount),
			},
			Gas: common.Gas{
				common.NewCoin(common.XMRAsset, cosmos.NewUint(1000)),
			},
			Memo: memo,
		},
		BlockHeight:    height,
		FinaliseHeight: height,
		ObservedPubKey: observedPubKey,
	}
}

func (s *XMRVaultMetadataBackfillSuite) TestBackfillXMRVaultMetadataFromHistoricalObservedVoters(c *C) {
	ctx, mgr := setupManagerForTest(c)

	outboundVaultPubKey := GetRandomEd25519PubKey()
	outboundVault := setTestXMRBackfillVault(c, ctx, mgr, outboundVaultPubKey, cosmos.NewUint(10*common.One))

	inboundVaultPubKey := GetRandomEd25519PubKey()
	inboundVault := setTestXMRBackfillVault(c, ctx, mgr, inboundVaultPubKey, cosmos.ZeroUint())

	outboundTxID := GetRandomTxHash()
	outboundVaultAddress, err := outboundVault.GetAddress(common.XMRChain)
	c.Assert(err, IsNil)
	outboundBase := newTestXMRBackfillObservedTx(
		outboundTxID,
		outboundVaultPubKey,
		outboundVaultAddress,
		common.Address(common.XMRSeededDummyAddress('8', []byte("recipient"))),
		cosmos.NewUint(common.One),
		"OUT:"+GetRandomTxHash().String(),
		100,
	)
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
	inboundVaultAddress, err := inboundVault.GetAddress(common.XMRChain)
	c.Assert(err, IsNil)
	inboundBase := newTestXMRBackfillObservedTx(
		inboundTxID,
		inboundVaultPubKey,
		common.Address(common.XMRSeededDummyAddress('8', []byte("sender"))),
		inboundVaultAddress,
		cosmos.NewUint(2*common.One),
		"ADD:XMR.XMR:"+GetRandomBech32Addr().String(),
		120,
	)
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

func (s *XMRVaultMetadataBackfillSuite) TestBackfillXMRVaultMetadataFromNonFinalObservationMetadata(c *C) {
	ctx, mgr := setupManagerForTest(c)

	vaultPubKey := GetRandomEd25519PubKey()
	vault := setTestXMRBackfillVault(c, ctx, mgr, vaultPubKey, cosmos.NewUint(10*common.One))

	txID := GetRandomTxHash()
	vaultAddress, err := vault.GetAddress(common.XMRChain)
	c.Assert(err, IsNil)
	finalObserved := newTestXMRBackfillObservedTx(
		txID,
		vaultPubKey,
		vaultAddress,
		common.Address(common.XMRSeededDummyAddress('8', []byte("recipient"))),
		cosmos.NewUint(common.One),
		"OUT:"+GetRandomTxHash().String(),
		100,
	)
	nonFinalWithMetadata := finalObserved
	nonFinalWithMetadata.BlockHeight = 99
	nonFinalWithMetadata.TxKeys = []string{"tx-key-1"}
	nonFinalWithMetadata.XmrKeyImages = []*common.XmrKeyImageProof{
		{TxHash: txID.String(), OutputIndex: 1, KeyImage: "ki-out", ProofC: "c1", ProofS: "s1"},
	}
	mgr.Keeper().SetObservedTxOutVoter(ctx, ObservedTxVoter{
		TxID:            txID,
		Tx:              finalObserved,
		Txs:             common.ObservedTxs{finalObserved, nonFinalWithMetadata},
		Height:          100,
		FinalisedHeight: 100,
	})

	stats, err := backfillXMRVaultMetadata(ctx, mgr)
	c.Assert(err, IsNil)
	c.Assert(stats.vaultsTouched, Equals, 1)
	c.Assert(stats.keyImagesAdded, Equals, 1)
	c.Assert(stats.txKeyRefsAdded, Equals, 1)
	c.Assert(stats.txKeysAdded, Equals, 1)

	vault, err = mgr.Keeper().GetVault(ctx, vaultPubKey)
	c.Assert(err, IsNil)
	c.Assert(vault.KeyImagesXmr, HasLen, 1)
	c.Assert(vault.KeyImagesXmr[0].TxHash, Equals, txID.String())
	c.Assert(vault.TxKeysXmr, HasLen, 1)
	c.Assert(vault.TxKeysXmr[0].TxHash, Equals, txID.String())
	c.Assert(vault.TxKeysXmr[0].TxKeys, DeepEquals, []string{"tx-key-1"})
}

func (s *XMRVaultMetadataBackfillSuite) TestBackfillRemovesReceiverSideMigrateXMRTxKeys(c *C) {
	ctx, mgr := setupManagerForTest(c)

	senderPubKey := GetRandomEd25519PubKey()
	senderVault := setTestXMRBackfillVault(c, ctx, mgr, senderPubKey, cosmos.NewUint(10*common.One))
	senderAddress, err := senderVault.GetAddress(common.XMRChain)
	c.Assert(err, IsNil)

	receiverPubKey := GetRandomEd25519PubKey()
	receiverVault := setTestXMRBackfillVault(c, ctx, mgr, receiverPubKey, cosmos.NewUint(10*common.One))
	receiverAddress, err := receiverVault.GetAddress(common.XMRChain)
	c.Assert(err, IsNil)

	txID := GetRandomTxHash()
	receiverVault.TxKeysXmr = []*common.XmrTxKeyRef{
		{TxHash: txID.String(), TxKeys: []string{"stale-receiver-key"}},
	}
	c.Assert(mgr.Keeper().SetVault(ctx, receiverVault), IsNil)

	receiverObserved := newTestXMRBackfillObservedTx(
		txID,
		receiverPubKey,
		senderAddress,
		receiverAddress,
		cosmos.NewUint(common.One),
		"MIGRATE:123",
		100,
	)
	receiverObservedWithMetadata := receiverObserved
	receiverObservedWithMetadata.TxKeys = []string{"wrong-vault-key"}
	mgr.Keeper().SetObservedTxOutVoter(ctx, ObservedTxVoter{
		TxID:            txID,
		Tx:              receiverObserved,
		Txs:             common.ObservedTxs{receiverObserved, receiverObservedWithMetadata},
		Height:          100,
		FinalisedHeight: 100,
	})

	stats, err := backfillXMRVaultMetadata(ctx, mgr)
	c.Assert(err, IsNil)
	c.Assert(stats.vaultsTouched, Equals, 1)
	c.Assert(stats.txKeyRefsAdded, Equals, -1)
	c.Assert(stats.txKeysAdded, Equals, -1)

	receiverVault, err = mgr.Keeper().GetVault(ctx, receiverPubKey)
	c.Assert(err, IsNil)
	c.Assert(receiverVault.TxKeysXmr, HasLen, 0)

	senderVault, err = mgr.Keeper().GetVault(ctx, senderPubKey)
	c.Assert(err, IsNil)
	c.Assert(senderVault.TxKeysXmr, HasLen, 0)
}
