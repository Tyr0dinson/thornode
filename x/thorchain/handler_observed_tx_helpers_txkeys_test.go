package thorchain

import (
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

type XMRTxKeysFlowSuite struct{}

var _ = Suite(&XMRTxKeysFlowSuite{})

func (s *XMRTxKeysFlowSuite) TestAppendVaultXMRTxKeysDedupesByTxHashAndKey(c *C) {
	vault := Vault{
		TxKeysXmr: []*common.XmrTxKeyRef{
			{TxHash: "aaa", TxKeys: []string{"key-0"}},
		},
	}

	appendVaultXMRTxKeys(&vault, "aaa", []string{"key-0", "key-1", "key-1"})
	appendVaultXMRTxKeys(&vault, "bbb", []string{"key-2", "", "key-2"})

	c.Assert(vault.TxKeysXmr, HasLen, 2)
	c.Assert(vault.TxKeysXmr[0].TxHash, Equals, "aaa")
	c.Assert(vault.TxKeysXmr[0].TxKeys, DeepEquals, []string{"key-0", "key-1"})
	c.Assert(vault.TxKeysXmr[1].TxHash, Equals, "bbb")
	c.Assert(vault.TxKeysXmr[1].TxKeys, DeepEquals, []string{"key-2"})
}

func (s *XMRTxKeysFlowSuite) TestRemoveVaultXMRTxKeysByTxHash(c *C) {
	vault := Vault{
		TxKeysXmr: []*common.XmrTxKeyRef{
			{TxHash: "aaa", TxKeys: []string{"key-0"}},
			{TxHash: "bbb", TxKeys: []string{"key-1"}},
		},
	}

	removed := removeVaultXMRTxKeys(&vault, "bbb")

	c.Assert(removed, Equals, true)
	c.Assert(vault.TxKeysXmr, HasLen, 1)
	c.Assert(vault.TxKeysXmr[0].TxHash, Equals, "aaa")
}

func prepareTestXMRVault(c *C, vault *Vault) common.Address {
	vault.PubKeyEddsa = vault.PubKey
	vault.PubKeyFrost = vault.PubKey
	address, err := vault.GetAddress(common.XMRChain)
	c.Assert(err, IsNil)
	return address
}

func (s *HandlerObservedTxHelpersSuite) TestHandleObservedTxOutQuorumStoresXMRTxKeysOnVault(c *C) {
	ctx, mgr := setupManagerForTest(c)

	vaultPubKey := GetRandomEd25519PubKey()
	vault := NewVault(1, ActiveVault, AsgardVault, vaultPubKey, []string{
		common.XMRChain.String(),
	}, []ChainContract{})
	vaultAddress := prepareTestXMRVault(c, &vault)
	vault.AddFunds(common.Coins{
		common.NewCoin(common.XMRAsset, cosmos.NewUint(5*common.One)),
	})
	c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)

	txID := GetRandomTxHash()
	observedTx := common.ObservedTx{
		Tx: common.Tx{
			ID:          txID,
			Chain:       common.XMRChain,
			FromAddress: vaultAddress,
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
		TxKeys:         []string{"key-a", "key-b", "key-a"},
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
	c.Assert(vault.TxKeysXmr, HasLen, 1)
	c.Assert(vault.TxKeysXmr[0].TxHash, Equals, txID.String())
	c.Assert(vault.TxKeysXmr[0].TxKeys, DeepEquals, []string{"key-a", "key-b"})
}

func (s *HandlerObservedTxHelpersSuite) TestHandleObservedTxOutQuorumRemovesReceiverSideMigrateXMRTxKeys(c *C) {
	ctx, mgr := setupManagerForTest(c)

	senderPubKey := GetRandomEd25519PubKey()
	senderVault := NewVault(1, RetiringVault, AsgardVault, senderPubKey, []string{
		common.XMRChain.String(),
	}, []ChainContract{})
	senderAddress := prepareTestXMRVault(c, &senderVault)
	c.Assert(mgr.Keeper().SetVault(ctx, senderVault), IsNil)

	receiverPubKey := GetRandomEd25519PubKey()
	receiverVault := NewVault(1, ActiveVault, AsgardVault, receiverPubKey, []string{
		common.XMRChain.String(),
	}, []ChainContract{})
	receiverAddress := prepareTestXMRVault(c, &receiverVault)
	receiverVault.AddFunds(common.Coins{
		common.NewCoin(common.XMRAsset, cosmos.NewUint(5*common.One)),
	})

	txID := GetRandomTxHash()
	receiverVault.TxKeysXmr = []*common.XmrTxKeyRef{
		{TxHash: txID.String(), TxKeys: []string{"stale-receiver-key"}},
	}
	c.Assert(mgr.Keeper().SetVault(ctx, receiverVault), IsNil)

	observedTx := common.ObservedTx{
		Tx: common.Tx{
			ID:          txID,
			Chain:       common.XMRChain,
			FromAddress: senderAddress,
			ToAddress:   receiverAddress,
			Coins: common.Coins{
				common.NewCoin(common.XMRAsset, cosmos.NewUint(common.One)),
			},
			Gas: common.Gas{
				common.NewCoin(common.XMRAsset, cosmos.NewUint(1000)),
			},
			Memo: "MIGRATE:123",
		},
		BlockHeight:    1024,
		FinaliseHeight: 1024,
		ObservedPubKey: receiverPubKey,
		TxKeys:         []string{"wrong-vault-key"},
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

	receiverVault, err = mgr.Keeper().GetVault(ctx, receiverPubKey)
	c.Assert(err, IsNil)
	c.Assert(receiverVault.TxKeysXmr, HasLen, 0)

	senderVault, err = mgr.Keeper().GetVault(ctx, senderPubKey)
	c.Assert(err, IsNil)
	c.Assert(senderVault.TxKeysXmr, HasLen, 0)
}

func (s *HandlerObservedTxHelpersSuite) TestHandleObservedTxOutQuorumStoresXMRTxKeysFromNonFinalObservationMetadata(c *C) {
	ctx, mgr := setupManagerForTest(c)

	vaultPubKey := GetRandomEd25519PubKey()
	vault := NewVault(1, ActiveVault, AsgardVault, vaultPubKey, []string{
		common.XMRChain.String(),
	}, []ChainContract{})
	vaultAddress := prepareTestXMRVault(c, &vault)
	vault.AddFunds(common.Coins{
		common.NewCoin(common.XMRAsset, cosmos.NewUint(5*common.One)),
	})
	c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)

	txID := GetRandomTxHash()
	finalObserved := common.ObservedTx{
		Tx: common.Tx{
			ID:          txID,
			Chain:       common.XMRChain,
			FromAddress: vaultAddress,
			ToAddress:   common.Address(common.XMRSeededDummyAddress('8', []byte("recipient"))),
			Coins: common.Coins{
				common.NewCoin(common.XMRAsset, cosmos.NewUint(common.One)),
			},
			Gas: common.Gas{
				common.NewCoin(common.XMRAsset, cosmos.NewUint(1000)),
			},
			Memo: "OUT:" + GetRandomTxHash().String(),
		},
		BlockHeight:    2048,
		FinaliseHeight: 2048,
		ObservedPubKey: vaultPubKey,
	}
	nonFinalWithMetadata := finalObserved
	nonFinalWithMetadata.BlockHeight = 2047
	nonFinalWithMetadata.TxKeys = []string{"key-a", "key-b"}

	voter := ObservedTxVoter{
		TxID:            txID,
		Tx:              finalObserved,
		Txs:             common.ObservedTxs{finalObserved, nonFinalWithMetadata},
		Height:          finalObserved.BlockHeight,
		FinalisedHeight: finalObserved.BlockHeight,
	}

	err := handleObservedTxOutQuorum(
		ctx,
		mgr,
		cosmos.AccAddress{},
		NodeAccounts{},
		func(cosmos.Context, cosmos.Msg) (*cosmos.Result, error) { return nil, errKaboom },
		nonFinalWithMetadata,
		voter,
		nil,
		true,
	)
	c.Assert(err, IsNil)

	vault, err = mgr.Keeper().GetVault(ctx, vaultPubKey)
	c.Assert(err, IsNil)
	c.Assert(vault.TxKeysXmr, HasLen, 1)
	c.Assert(vault.TxKeysXmr[0].TxHash, Equals, txID.String())
	c.Assert(vault.TxKeysXmr[0].TxKeys, DeepEquals, []string{"key-a", "key-b"})
}

func (s *HandlerObservedTxHelpersSuite) TestHandleObservedTxOutQuorumBackfillsLateXMRTxKeysAfterFinalization(c *C) {
	ctx, mgr := setupManagerForTest(c)

	vaultPubKey := GetRandomEd25519PubKey()
	vault := NewVault(1, ActiveVault, AsgardVault, vaultPubKey, []string{
		common.XMRChain.String(),
	}, []ChainContract{})
	vaultAddress := prepareTestXMRVault(c, &vault)
	vault.AddFunds(common.Coins{
		common.NewCoin(common.XMRAsset, cosmos.NewUint(5*common.One)),
	})
	c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)

	txID := GetRandomTxHash()
	finalObserved := common.ObservedTx{
		Tx: common.Tx{
			ID:          txID,
			Chain:       common.XMRChain,
			FromAddress: vaultAddress,
			ToAddress:   common.Address(common.XMRSeededDummyAddress('8', []byte("recipient"))),
			Coins: common.Coins{
				common.NewCoin(common.XMRAsset, cosmos.NewUint(common.One)),
			},
			Gas: common.Gas{
				common.NewCoin(common.XMRAsset, cosmos.NewUint(1000)),
			},
			Memo: "OUT:" + GetRandomTxHash().String(),
		},
		BlockHeight:    4096,
		FinaliseHeight: 4096,
		ObservedPubKey: vaultPubKey,
	}
	lateMetadata := finalObserved
	lateMetadata.TxKeys = []string{"key-a", "key-b"}

	voter := ObservedTxVoter{
		TxID:            txID,
		Tx:              finalObserved,
		Txs:             common.ObservedTxs{finalObserved, lateMetadata},
		Height:          finalObserved.BlockHeight,
		FinalisedHeight: finalObserved.BlockHeight,
	}

	err := handleObservedTxOutQuorum(
		ctx,
		mgr,
		cosmos.AccAddress{},
		NodeAccounts{},
		func(cosmos.Context, cosmos.Msg) (*cosmos.Result, error) { return nil, errKaboom },
		lateMetadata,
		voter,
		nil,
		false,
	)
	c.Assert(err, IsNil)

	vault, err = mgr.Keeper().GetVault(ctx, vaultPubKey)
	c.Assert(err, IsNil)
	c.Assert(vault.TxKeysXmr, HasLen, 1)
	c.Assert(vault.TxKeysXmr[0].TxHash, Equals, txID.String())
	c.Assert(vault.TxKeysXmr[0].TxKeys, DeepEquals, []string{"key-a", "key-b"})
}
