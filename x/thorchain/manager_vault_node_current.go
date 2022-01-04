package thorchain

import (
	"fmt"

	"gitlab.com/thorchain/thornode/common"
	"gitlab.com/thorchain/thornode/common/cosmos"
	"gitlab.com/thorchain/thornode/constants"
	"gitlab.com/thorchain/thornode/x/thorchain/keeper"
)

// vaultNodeMgrV79 manages vault nodes, sets them active, and requests funds back from their Valkyrie Vault
type vaultNodeMgrV79 struct {
	k          keeper.Keeper
	vaultMgr   NetworkManager
	txOutStore TxOutStore
	eventMgr   EventManager
}

// vaultNodeMgrV56 create a new instance of vaultNodeMgr
func newVaultNodeMgrV79(k keeper.Keeper, vaultMgr NetworkManager, txOutStore TxOutStore, eventMgr EventManager) *vaultNodeMgrV79 {
	return &vaultNodeMgrV79{
		k:          k,
		vaultMgr:   vaultMgr,
		txOutStore: txOutStore,
		eventMgr:   eventMgr,
	}
}

// RequestValkyrieReturn requests the Vault Node to return funds from its Valkyrie Vault
func (vm *vaultNodeMgrV79) RequestValkyrieReturn(ctx cosmos.Context, node NodeAccount, mgr Manager, constAccessor constants.ConstantValues) error {
	if !vm.k.VaultExists(ctx, node.PubKeySet.Secp256k1) {
		return nil
	}
	valkyrie, err := vm.k.GetVault(ctx, node.PubKeySet.Secp256k1)
	if err != nil {
		return fmt.Errorf("fail to get valkyrie: %w", err)
	}
	if valkyrie.IsAsgard() {
		return nil
	}
	if valkyrie.IsYggdrasil() {
		return nil
	}
	if !valkyrie.HasFunds() {
		return nil
	}

	active, err := vm.k.GetAsgardVaultsByStatus(ctx, ActiveVault)
	if err != nil {
		return err
	}

	signingTransactionPeriod := constAccessor.GetInt64Value(constants.SigningTransactionPeriod)

	// select vault that is most secure
	vault := vm.k.GetMostSecure(ctx, active, signingTransactionPeriod)
	if vault.IsEmpty() {
		return fmt.Errorf("unable to determine asgard vault")
	}

	valkyrieAsset := valkyrie.Asset
	valkyrieChain := valkyrieAsset.Chain

	toAddr, err := vault.PubKey.GetAddress(valkyrieChain)
	if err != nil {
		return err
	}

	if !valkyrie.HasFundsForChain(valkyrieChain) {
		ctx.Logger().Info("Valkyrie vault doesn't have funds for it's chain, no need to return", "chain", valkyrieChain)
		return nil
	}

	if !toAddr.IsEmpty() {
		txOutItem := TxOutItem{
			Chain:       valkyrieChain,
			ToAddress:   toAddr,
			InHash:      common.BlankTxID,
			VaultPubKey: valkyrie.PubKey,
			Coin:        common.NewCoin(common.RuneAsset(), cosmos.ZeroUint()),
			Memo:        NewValkyrieReturnMemo(common.BlockHeight(ctx)).String(),
			GasRate:     int64(mgr.GasMgr().GetGasRate(ctx, valkyrieChain).Uint64()),
			// DO NOT specify MaxGas , for Valkyrie return , should allow node to spend more on gas , for example ETH, return multiple
			// ERC20 token / ETH at the same time cost a lot gas
		}

		// valkyrie- will not set coin field here, when signer see a TxOutItem that has memo "valkyrie-" it will query the chain
		// and find out all the remaining assets , and fill in the field
		if err := vm.txOutStore.UnSafeAddTxOutItem(ctx, mgr, txOutItem); err != nil {
			return err
		}
	}

	return nil
}
