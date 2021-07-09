package thorchain

import (
	"fmt"
	"strings"

	"gitlab.com/thorchain/thornode/common"
	"gitlab.com/thorchain/thornode/common/cosmos"
	"gitlab.com/thorchain/thornode/constants"
)

const (
	MimirRecallFund      = `MimirRecallFund`
	MimirWithdrawUSDT    = `MimirWithdrawUSDT`
	MimirUpgradeContract = `MimirUpgradeContract`
)

type WithdrawLiquidityCallback func(ctx cosmos.Context, pool Pool, na NodeAccount, nth int64, mgr Manager, constAccessor constants.ConstantValues) error
type RouterUpgradeController struct {
	mgr                       Manager
	withdrawLiquidityCallback WithdrawLiquidityCallback
}

// NewRouterUpgradeController create a new instance of RouterUpgradeController
func NewRouterUpgradeController(mgr Manager, withdrawLiquidityCallback WithdrawLiquidityCallback) *RouterUpgradeController {
	return &RouterUpgradeController{
		mgr:                       mgr,
		withdrawLiquidityCallback: withdrawLiquidityCallback,
	}
}

func (r *RouterUpgradeController) recallYggdrasilFund(ctx cosmos.Context) error {
	recallFund, err := r.mgr.Keeper().GetMimir(ctx, MimirRecallFund)
	if err != nil {
		return fmt.Errorf("fail to get mimir: %w", err)
	}

	if recallFund <= 0 {
		// mimir has not been set , return
		return nil
	}
	vaultMgr := r.mgr.VaultMgr()
	if err := vaultMgr.RecallChainFunds(ctx, common.ETHChain, r.mgr, common.PubKeys{}); err != nil {
		return fmt.Errorf("fail to recall chain funds, err:%w", err)
	}
	return r.mgr.Keeper().DeleteMimir(ctx, MimirRecallFund)
}
func (r *RouterUpgradeController) refundUSDTLiquidityProviders(ctx cosmos.Context) error {
	if r.withdrawLiquidityCallback == nil {
		ctx.Logger().Error("withdraw liquidity callback func is nil")
		return nil
	}
	withdrawUSDT, err := r.mgr.Keeper().GetMimir(ctx, MimirWithdrawUSDT)
	if err != nil {
		return fmt.Errorf("fail to get mimir: %w", err)
	}

	if withdrawUSDT <= 0 {
		// mimir has not been set , return
		return nil
	}
	usdtAsset, err := common.NewAsset(ethUSDTAsset)
	if err != nil {
		return fmt.Errorf("fail to parse USDT asset,err:%w", err)
	}
	pool, err := r.mgr.Keeper().GetPool(ctx, usdtAsset)
	if err != nil {
		return fmt.Errorf("fail to get pool, err: %w", err)
	}
	if pool.IsEmpty() {
		ctx.Logger().Info("pool is empty, no need to withdraw")
		return nil
	}
	nas, err := r.mgr.Keeper().ListActiveNodeAccounts(ctx)
	if err != nil {
		ctx.Logger().Error("can't get active nodes", "error", err)
		return err
	}
	if len(nas) == 0 {
		return fmt.Errorf("can't find any active nodes")
	}
	na := nas[0]
	if err := r.withdrawLiquidityCallback(ctx, pool, na, 10, r.mgr, r.mgr.GetConstants()); err != nil {
		return fmt.Errorf("fail to withdraw liquidities,err:%w", err)
	}
	return r.mgr.Keeper().DeleteMimir(ctx, MimirWithdrawUSDT)
}
func (r *RouterUpgradeController) upgradeContract(ctx cosmos.Context) error {
	upgrade, err := r.mgr.Keeper().GetMimir(ctx, MimirUpgradeContract)
	if err != nil {
		return fmt.Errorf("fail to get mimir: %w", err)
	}

	if upgrade <= 0 {
		// mimir has not been set , return
		return nil
	}

	newRouterAddr, err := common.NewAddress(ethNewRouter)
	if err != nil {
		return fmt.Errorf("fail to parse router address, err: %w")
	}
	cc, err := r.mgr.Keeper().GetChainContract(ctx, common.ETHChain)
	if err != nil {
		return fmt.Errorf("fail to get existing chain contract,err:%w", err)
	}
	// ensure it is upgrading the current router contract used on multichain chaosnet
	if !strings.EqualFold(cc.Router.String(), ethOldRouter) {
		return fmt.Errorf("old router is not %s, no need to upgrade", ethOldRouter)
	}
	chainContract := ChainContract{
		Chain:  common.ETHChain,
		Router: newRouterAddr,
	}
	// Update the contract address
	r.mgr.Keeper().SetChainContract(ctx, chainContract)

	// first of all need to write off all the USDT asset in all vaults
	usdtAsset, err := common.NewAsset(ethUSDTAsset)
	if err != nil {
		return fmt.Errorf("fail to parse asset, err: %w", err)
	}
	vaultIter := r.mgr.Keeper().GetVaultIterator(ctx)
	defer vaultIter.Close()
	for ; vaultIter.Valid(); vaultIter.Next() {
		var vault Vault
		if err := r.mgr.Keeper().Cdc().UnmarshalBinaryBare(vaultIter.Value(), &vault); err != nil {
			ctx.Logger().Error("fail to unmarshal vault", "error", err)
			continue
		}
		// vault is empty , ignore
		if vault.IsEmpty() {
			continue
		}
		for idx, c := range vault.Coins {
			if c.Asset.Equals(usdtAsset) {
				vault.Coins[idx].Amount = cosmos.ZeroUint()
			}
		}
		if vault.IsType(YggdrasilVault) {
			vault.UpdateContract(chainContract)
		}
		if err := r.mgr.Keeper().SetVault(ctx, vault); err != nil {
			ctx.Logger().Error("fail to save vault", "error", err)
		}

	}

	return r.mgr.Keeper().DeleteMimir(ctx, MimirUpgradeContract)
}

// Process is the main entry of router upgrade controller , it will recall yggdrasil fund , and refund all USDT liquidity , and then upgrade contract
//  all these steps are controled by mimir
func (r *RouterUpgradeController) Process(ctx cosmos.Context) {
	if err := r.recallYggdrasilFund(ctx); err != nil {
		ctx.Logger().Error("fail to recall yggdrasil funds", "error", err)
	}
	if err := r.refundUSDTLiquidityProviders(ctx); err != nil {
		ctx.Logger().Error("fail to refund all USDT liquidity providers", "error", err)
	}
	if err := r.upgradeContract(ctx); err != nil {
		ctx.Logger().Error("fail to upgrade contract", "error", err)
	}
}
