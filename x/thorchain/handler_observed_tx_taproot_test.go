package thorchain

import (
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// Message validation accepts both address forms of a secp256k1 key on chains
// with Taproot vaults; the handler must then insist on the vault's real address.
func (s *HandlerObservedTxInSuite) TestInboundToWrongAddressFormIsDropped(c *C) {
	if !common.LTCChain.SupportsTaprootVaults() {
		c.Skip("taproot vaults are not enabled on this build")
	}
	frostKey := types.GetTaprootTestPubKey()
	taprootAddr, err := frostKey.GetAddressForAlgo(common.LTCChain, common.SigningAlgoSecp256k1Taproot)
	c.Assert(err, IsNil)
	unusedECDSAForm, err := frostKey.GetAddress(common.LTCChain)
	c.Assert(err, IsNil)

	run := func(vault Vault, observedKey common.PubKey, to common.Address) int {
		ctx, mgr := setupManagerForTest(c)
		tx := common.NewTx(
			GetRandomTxHash(),
			GetRandomLTCAddress(),
			to,
			common.Coins{common.NewCoin(common.LTCAsset, cosmos.NewUint(100_000_000))},
			common.Gas{common.NewCoin(common.LTCAsset, cosmos.NewUint(1_000))},
			"SWAP:THOR.RUNE:"+GetRandomRUNEAddress().String(),
		)
		obTx := NewObservedTx(tx, 12, observedKey, 12)
		keeper := &TestObservedTxInHandleKeeper{
			nas: NodeAccounts{
				GetRandomValidatorNode(NodeActive),
				GetRandomValidatorNode(NodeActive),
				GetRandomValidatorNode(NodeActive),
				GetRandomValidatorNode(NodeActive),
			},
			vault:       vault,
			vaultExists: true,
		}
		mgr.K = keeper
		handler := NewObservedTxInHandler(mgr)
		_, handleErr := handler.handle(ctx, *NewMsgObservedTxIn(ObservedTxs{obTx}, keeper.nas[0].NodeAddress))
		c.Assert(handleErr, IsNil)
		voter, voterErr := keeper.GetObservedTxInVoter(ctx, tx.ID)
		c.Assert(voterErr, IsNil)
		return len(voter.Txs)
	}

	// A Taproot vault: only its P2TR address is accepted.
	frostVault := GetRandomVault()
	frostVault.Chains = append(frostVault.Chains, common.LTCChain.String())
	frostVault.PubKeyFrost = frostKey
	c.Check(run(frostVault, frostKey, taprootAddr), Equals, 1)
	c.Check(run(frostVault, frostKey, unusedECDSAForm), Equals, 0)

	// An ECDSA vault: only its P2WPKH address is accepted, never a P2TR form.
	ecdsaVault := GetRandomVault()
	ecdsaVault.Chains = append(ecdsaVault.Chains, common.LTCChain.String())
	legacyAddr, err := ecdsaVault.PubKey.GetAddress(common.LTCChain)
	c.Assert(err, IsNil)
	taprootFormOfECDSAKey, err := ecdsaVault.PubKey.GetAddressForAlgo(common.LTCChain, common.SigningAlgoSecp256k1Taproot)
	c.Assert(err, IsNil)
	c.Check(run(ecdsaVault, ecdsaVault.PubKey, legacyAddr), Equals, 1)
	c.Check(run(ecdsaVault, ecdsaVault.PubKey, taprootFormOfECDSAKey), Equals, 0)
}

// The same exact-address rule applies to outbounds: the observed FromAddress
// must be the vault's real address on the chain.
func (s *HandlerObservedTxOutSuite) TestOutboundFromWrongAddressFormIsDropped(c *C) {
	if !common.LTCChain.SupportsTaprootVaults() {
		c.Skip("taproot vaults are not enabled on this build")
	}
	frostKey := types.GetTaprootTestPubKey()
	taprootAddr, err := frostKey.GetAddressForAlgo(common.LTCChain, common.SigningAlgoSecp256k1Taproot)
	c.Assert(err, IsNil)
	unusedECDSAForm, err := frostKey.GetAddress(common.LTCChain)
	c.Assert(err, IsNil)

	run := func(from common.Address) int {
		ctx, mgr := setupManagerForTest(c)
		txInHash := GetRandomTxHash()
		tx := common.NewTx(
			GetRandomTxHash(),
			from,
			GetRandomLTCAddress(),
			common.Coins{common.NewCoin(common.LTCAsset, cosmos.NewUint(50_000_000))},
			common.Gas{common.NewCoin(common.LTCAsset, cosmos.NewUint(1_000))},
			"OUT:"+txInHash.String(),
		)
		obTx := NewObservedTx(tx, 12, frostKey, 12)
		vault := GetRandomVault()
		vault.Chains = append(vault.Chains, common.LTCChain.String())
		vault.PubKeyFrost = frostKey
		keeper := &TestObservedTxOutHandleKeeper{
			nas: NodeAccounts{
				GetRandomValidatorNode(NodeActive),
				GetRandomValidatorNode(NodeActive),
				GetRandomValidatorNode(NodeActive),
				GetRandomValidatorNode(NodeActive),
			},
			voter:       NewObservedTxVoter(tx.ID, make(ObservedTxs, 0)),
			txInVoter:   NewObservedTxVoter(txInHash, make(ObservedTxs, 0)),
			vaultExists: true,
			vault:       vault,
			hashes:      make([]common.TxID, 0),
		}
		mgr.K = keeper
		mgr.txOutStore = NewTxStoreDummy()
		handler := NewObservedTxOutHandler(mgr)
		_, handleErr := handler.handle(ctx, *NewMsgObservedTxOut(ObservedTxs{obTx}, keeper.nas[0].NodeAddress))
		c.Assert(handleErr, IsNil)
		return len(keeper.voter.Txs)
	}

	c.Check(run(taprootAddr), Equals, 1)
	c.Check(run(unusedECDSAForm), Equals, 0)
}
