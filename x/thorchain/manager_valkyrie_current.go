package thorchain

import (
	"errors"
	"fmt"

	"gitlab.com/thorchain/thornode/common"
	"gitlab.com/thorchain/thornode/common/cosmos"
	"gitlab.com/thorchain/thornode/constants"
	"gitlab.com/thorchain/thornode/x/thorchain/keeper"
	kvTypes "gitlab.com/thorchain/thornode/x/thorchain/keeper/types"
	"gitlab.com/thorchain/thornode/x/thorchain/types"
)

const (
	MimirStopFundValkyrie = `StopFundValkyrie`
)

// ValkyrieMgrV74 is an implementation of ValkyrieManager
type ValkyrieMgrV74 struct {
	keeper keeper.Keeper
}

// newValkyrieMgrV74 create a new instance of ValkyrieMgrV74 which implement ValkyrieManager interface
func newValkyrieMgrV74(keeper keeper.Keeper) *ValkyrieMgrV74 {
	return &ValkyrieMgrV74{
		keeper: keeper,
	}
}

func (vmgr ValkyrieMgrV74) Fund(ctx cosmos.Context, mgr Manager, constAccessor constants.ConstantValues) error {
	// Check if we have triggered the ragnarok protocol
	ragnarokHeight, err := vmgr.keeper.GetRagnarokBlockHeight(ctx)
	if err != nil {
		return fmt.Errorf("fail to get ragnarok height: %w", err)
	}
	if ragnarokHeight > 0 {
		return nil
	}

	stopFundValkyrie, err := mgr.Keeper().GetMimir(ctx, MimirStopFundValkyrie)
	if err == nil && stopFundValkyrie > 0 {
		ctx.Logger().Info("Valkyrie funding has been stopped by the Mimir setting")
		return nil
	}

	nodeAccs, err := vmgr.keeper.ListActiveVaultNodes(ctx)
	if err != nil {
		ctx.Logger().Info("Failed to get active Vault Nodes")
		return nil
	}

	// We don't want to check all Valkyrie Vault every time THORNode run this
	// function. So THORNode use modulus to determine which Valkyrie THORNode should process. This
	// should behave as a "round robin" approach checking one Valkyrie per block.
	// With 1000 Valkyrie vaults, THORNode should check each vault every 83.3 minutes.
	na := nodeAccs[common.BlockHeight(ctx)%int64(len(nodeAccs))]

	if na.Type != NodeTypeVault {
		return nil
	}

	valkyrie, err := vmgr.keeper.GetVault(ctx, na.PubKeySet.Secp256k1)

	// Create the Valkyrie Vault if it doesn't exist
	if err != nil {

		if !errors.Is(err, kvTypes.ErrVaultNotFound) {
			return fmt.Errorf("fail to get valkyrie vault: %w", err)
		}

		// Check if max Vault Nodes has been reached
		activeVaultNodes, err := vmgr.keeper.ListActiveVaultNodes(ctx)
		if err != nil {
			return fmt.Errorf("failed to get active vault nodes: %w", err)
		}
		maxVaultNodes, err := vmgr.keeper.GetMimir(ctx, constants.MaximumNumberOfVaultNodes.String())
		if err != nil || maxVaultNodes < 0 {
			maxVaultNodes = constAccessor.GetInt64Value(constants.MaximumNumberOfVaultNodes)
		}
		if len(activeVaultNodes) >= int(maxVaultNodes) {
			return fmt.Errorf("Can't create Valkyrie Vault, system limit has been reached")
		}

		// get what chain asgard currently support
		asgards, err := vmgr.keeper.GetAsgardVaultsByStatus(ctx, ActiveVault)
		if err != nil {
			return fmt.Errorf("fail to get active asgards: %w", err)
		}
		if len(asgards) == 0 {
			return fmt.Errorf("can't find current active asgard: %w", err)
		}
		supportChains := asgards[0].GetChains()

		valkyrie = NewVault(common.BlockHeight(ctx), ActiveVault, ValkyrieVault, na.PubKeySet.Secp256k1, supportChains.Strings(), vmgr.keeper.GetChainContracts(ctx, supportChains))
		valkyrie.Asset = &common.EmptyAsset
		valkyrie.Membership = append(valkyrie.Membership, na.PubKeySet.Secp256k1.String())

		if err := vmgr.keeper.SetVault(ctx, valkyrie); err != nil {
			return fmt.Errorf("fail to create valkyrie vault: %w", err)
		}
	}

	if !valkyrie.IsValkyrie() {
		return nil
	}

	// Valkyrie Vault hasn't been assigned an asset, or the vault is empty
	if valkyrie.Asset.IsEmpty() || valkyrie.GetCoin(*valkyrie.Asset).Amount.IsZero() {

		activePools, err := vmgr.keeper.GetPoolsByStatus(ctx, types.PoolStatus_Available)

		if err != nil {
			return fmt.Errorf("failed to get active pools: %w", err)
		}

		vaultAsset, err := vmgr.calcAssetForValkyrieVault(ctx, activePools)

		if err != nil {
			return fmt.Errorf("failed to assign valkyrie vault an asset: %w", err)
		}

		valkyrie.Asset = &vaultAsset

		if err := vmgr.keeper.SetVault(ctx, valkyrie); err != nil {
			return fmt.Errorf("fail to set valkyrie vault: %w", err)
		}
	}

	vaultPool, err := vmgr.keeper.GetPool(ctx, *valkyrie.Asset)
	if err != nil {
		return fmt.Errorf("failed to get vault pool: %w", err)
	}

	// Vault pool is no longer available, request funds back and un-assign this vault's asset
	if !vaultPool.IsAvailable() {

		// TODO: request funds back

		valkyrie.Asset = &common.EmptyAsset
		if err := vmgr.keeper.SetVault(ctx, valkyrie); err != nil {
			return fmt.Errorf("failed to set valkyrie vault: %w", err)
		}

		return nil
	}

	sendCoin, err := vmgr.calcTargetValkyrieCoin(vaultPool, valkyrie, na.Bond)
	if err != nil {
		return err
	}

	count, err := vmgr.sendCoinToValkyrie(ctx, sendCoin, valkyrie, mgr, constAccessor)
	if err != nil {
		return err
	}
	for i := 0; i < count; i++ {
		valkyrie.AppendPendingTxBlockHeights(common.BlockHeight(ctx), constAccessor)
	}
	if err := vmgr.keeper.SetVault(ctx, valkyrie); err != nil {
		return fmt.Errorf("fail to set valkyrie vault: %w", err)
	}

	return nil
}

func (vmgr ValkyrieMgrV74) sendCoinToValkyrie(ctx cosmos.Context, coinToSend common.Coin, valkyrie Vault, mgr Manager, constAccessor constants.ConstantValues) (int, error) {

	var count int
	var coins common.Coins

	active, err := vmgr.keeper.GetAsgardVaultsByStatus(ctx, ActiveVault)
	if err != nil {
		return count, err
	}

	if !vmgr.shouldFundValkyrie(ctx, active[0], valkyrie, coinToSend.Asset.GetChain()) {
		addr, _ := valkyrie.PubKey.GetThorAddress()
		ctx.Logger().Error("valkyrie didn't upgrade contract, should not be funded", "address", addr)
		return 0, nil
	}
	if isChainHalted(ctx, mgr, coinToSend.Asset.GetChain()) {
		ctx.Logger().Info("chain is halt , don't fund valkyrie", "chain", coinToSend.Asset.GetChain().String())
		return 0, nil
	}

	gasCoin, err := mgr.GasMgr().GetMaxGas(ctx, coinToSend.Asset.GetChain())
	if err != nil {
		ctx.Logger().Error("fail to get max gas coin", "error", err)
		return 0, nil
	}

	// If the coin to send is not the gas asset, the gas asset must also be sent along
	if !gasCoin.Asset.Equals(coinToSend.Asset) {
		coins = append(coins, gasCoin)
		coins = append(coins, coinToSend)
		count = 2
	} else {
		coinToSend.Amount = coinToSend.Amount.Add(gasCoin.Amount)
		coins = append(coins, coinToSend)
		count = 1
	}

	// Find Asgard vaults with enough of the coins to send
	filterVaults := make(Vaults, 0)
	for _, v := range active {

		var shouldSkip = false

		for _, coin := range coins {
			if coin.Amount.GT(v.GetCoin(coin.Asset).Amount) {
				shouldSkip = true
			}
		}

		if !shouldSkip {
			filterVaults = append(filterVaults, v)
		}
	}
	signingTransactionPeriod := constAccessor.GetInt64Value(constants.SigningTransactionPeriod)
	vault := vmgr.keeper.GetLeastSecure(ctx, filterVaults, signingTransactionPeriod)
	if vault.IsEmpty() {
		// TODO: return error?
		return 0, nil
	}

	for _, coin := range coins {
		to, err := valkyrie.PubKey.GetAddress(coin.Asset.GetChain())
		if err != nil {
			ctx.Logger().Error("fail to get address from pub key", "pub key", valkyrie.PubKey, "chain", coin.Asset.GetChain(), "error", err)
			continue
		}

		toi := TxOutItem{
			Chain:       coin.Asset.GetChain(),
			ToAddress:   to,
			InHash:      common.BlankTxID,
			Memo:        NewValkyrieFundMemo(common.BlockHeight(ctx)).String(),
			Coin:        coin,
			VaultPubKey: vault.PubKey,
			MaxGas: common.Gas{
				gasCoin,
			},
			GasRate: int64(mgr.GasMgr().GetGasRate(ctx, coin.Asset.GetChain()).Uint64()),
		}
		if err := mgr.TxOutStore().UnSafeAddTxOutItem(ctx, mgr, toi); err != nil {
			return count, err
		}
	}

	return count, nil
}

// shouldFundValkyrie  make sure asgard and the valkyrie is using the same contract for the given chain.
// in a scenario that when contract get updated , all vaults will have to return their fund from old contract to
// new contract , once that happen and detected by THORChain, Valkyrie vault's smart contract will be updated to the new address
// if there are different , means Valkyrie didn't transfer their fund from old control to new one
// thus asgard should not send Valkyrie fund for the chain
func (vmgr ValkyrieMgrV74) shouldFundValkyrie(ctx cosmos.Context, asgard, valkyrie Vault, chain common.Chain) bool {
	asgardContract := asgard.GetContract(chain)
	if asgardContract.IsEmpty() {
		// the request chain doesn't support contract
		return true
	}
	valkyrieContract := valkyrie.GetContract(chain)
	if asgardContract.Router.Equals(valkyrieContract.Router) {
		return true
	}
	return false
}

// Iterate through all Asgards and find which asset is most abundant priced in RUNE
// The ValkyrieVault will be assigned this asset and be sent 25% of its bond value in this asset
func (vmgr ValkyrieMgrV74) calcAssetForValkyrieVault(ctx cosmos.Context, activePools []Pool) (common.Asset, error) {

	activeAsgardVaults, err := vmgr.keeper.GetAsgardVaultsByStatus(ctx, ActiveVault)
	if err != nil {
		return common.Asset{}, fmt.Errorf("failed to get active Asgard vaults: %w", err)
	}

	vaultAsset := common.EmptyAsset
	highestAssetValueInRune := cosmos.ZeroUint()

	for _, pool := range activePools {

		assetAmount := cosmos.ZeroUint()
		for _, vault := range activeAsgardVaults {
			assetAmount = assetAmount.Add(vault.GetCoin(pool.Asset).Amount)
		}

		runeValue := pool.AssetValueInRune(assetAmount)
		if runeValue.GT(highestAssetValueInRune) {
			vaultAsset = pool.Asset
			highestAssetValueInRune = runeValue
		}
	}

	return vaultAsset, nil
}

// Valkyrie Vaults should be funded with 25% the value of their bonded RUNE for their assigned pool
// If they have less than 12.5%, top them up
func (vmgr ValkyrieMgrV74) calcTargetValkyrieCoin(pool Pool, vault Vault, vaultBond cosmos.Uint) (coin common.Coin, err error) {

	// Don't send Valkyrie Vault
	if !pool.IsAvailable() {
		return coin, nil
	}

	// Target RUNE is the target value of the vault's asset denominated in RUNE (should be 25% the value of the Vault's bond)
	targetRune := vaultBond.QuoUint64(4)
	vaultAsset := vault.GetCoin(pool.Asset)

	vaultAssetInRune := pool.AssetValueInRune(vaultAsset.Amount)

	// Vault has less than 12.5% its bond value in asset, needs to be topped up
	if vaultAssetInRune.LT(targetRune.QuoUint64(2)) {
		topUpAmountInRune := common.SafeSub(targetRune, vaultAssetInRune)
		topUpAmountInAsset := pool.RuneValueInAsset(topUpAmountInRune)

		coin = common.NewCoin(pool.Asset, cosmos.RoundToDecimal(topUpAmountInAsset, pool.Decimals))
	}

	return coin, nil
}
