//go:build mainnet
// +build mainnet

package thorchain

import (
	"fmt"
	"strings"

	sdk "github.com/cosmos/cosmos-sdk/types"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	v2 "gitlab.com/thorchain/thornode/v3/x/thorchain/migrations/v2"
)

// Migrator is a struct for handling in-place store migrations.
type Migrator struct {
	mgr *Mgrs
}

// NewMigrator returns a new Migrator.
func NewMigrator(mgr *Mgrs) Migrator {
	return Migrator{mgr: mgr}
}

// Migrate1to2 migrates from version 1 to 2.
func (m Migrator) Migrate1to2(ctx sdk.Context) error {
	// Loads the manager for this migration (we are in the x/upgrade's preblock)
	// Note, we do not require the manager loaded for this migration, but it is okay
	// to load it earlier and this is the pattern for migrations to follow.
	if err := m.mgr.LoadManagerIfNecessary(ctx); err != nil {
		return err
	}
	return v2.MigrateStore(ctx, m.mgr.storeKey)
}

// Migrate2to3 migrates from version 2 to 3.
func (m Migrator) Migrate2to3(ctx sdk.Context) error {
	// Loads the manager for this migration (we are in the x/upgrade's preblock)
	// Note, we do not require the manager loaded for this migration, but it is okay
	// to load it earlier and this is the pattern for migrations to follow.
	if err := m.mgr.LoadManagerIfNecessary(ctx); err != nil {
		return err
	}

	// refund stagenet funding wallet for user refund
	// original user tx: https://runescan.io/tx/A9AF3ED203079BB246CEE0ACD837FBA024BC846784DE488D5BE70044D8877C52
	// refund to user from stagenet funding wallet: https://bscscan.com/tx/0xba67f3a88f8c998f29e774ffa8328e5625521e37c2db282b29a04ab3d2593f48
	stagenetWallet := "0x3021C479f7F8C9f1D5c7d8523BA5e22C0Bcb5430"
	inTxId := "A9AF3ED203079BB246CEE0ACD837FBA024BC846784DE488D5BE70044D8877C52" // original user tx

	bscUsdt, err := common.NewAsset("BSC.USDT-0X55D398326F99059FF775485246999027B3197955")
	if err != nil {
		return err
	}
	usdtCoin := common.NewCoin(bscUsdt, cosmos.NewUint(4860737515919))
	blockHeight := ctx.BlockHeight()

	// schedule refund
	if err := unsafeAddRefundOutbound(ctx, m.mgr, inTxId, stagenetWallet, usdtCoin, blockHeight); err != nil {
		return err
	}

	return nil
}

// Migrate3to4 migrates from version 4 to 5.
func (m Migrator) Migrate4to5(ctx sdk.Context) error {
	// Loads the manager for this migration (we are in the x/upgrade's preblock)
	// Note, we do not require the manager loaded for this migration, but it is okay
	// to load it earlier and this is the pattern for migrations to follow.
	if err := m.mgr.LoadManagerIfNecessary(ctx); err != nil {
		return err
	}

	// ------------------------------ TCY ------------------------------

	totalTCYCoin := common.NewCoin(common.TCY, cosmos.NewUint(210_000_000_00000000))
	err := m.mgr.Keeper().MintToModule(ctx, ModuleName, totalTCYCoin)
	if err != nil {
		return err
	}

	// Claims 206_606_541_28874864
	claimingModuleCoin := common.NewCoin(common.TCY, cosmos.NewUint(206_606_541_28874864))
	err = m.mgr.Keeper().SendFromModuleToModule(ctx, ModuleName, TCYClaimingName, common.NewCoins(claimingModuleCoin))
	if err != nil {
		return err
	}

	// 210M minus claims: 206_606_541_28874864
	treasuryCoin := common.NewCoin(common.TCY, totalTCYCoin.Amount.Sub(claimingModuleCoin.Amount))
	treasuryAddress, err := common.NewAddress("thor10qh5272ktq4wes8ex343ky9rsuehcypddjh08k")
	if err != nil {
		return err
	}

	treasuryAccAddress, err := treasuryAddress.AccAddress()
	if err != nil {
		return err
	}

	err = m.mgr.Keeper().SendFromModuleToAccount(ctx, ModuleName, treasuryAccAddress, common.NewCoins(treasuryCoin))
	if err != nil {
		return err
	}

	err = setTCYClaims(ctx, m.mgr)
	if err != nil {
		return err
	}

	// ------------------------------ Bond Slash Refund ------------------------------

	for _, slashRefund := range mainnetSlashRefunds4to5 {
		recipient, err := cosmos.AccAddressFromBech32(slashRefund.address)
		if err != nil {
			ctx.Logger().Error("error parsing address in store migration", "error", err)
			continue
		}
		amount := cosmos.NewUint(slashRefund.amount)
		refundCoins := common.NewCoins(common.NewCoin(common.RuneAsset(), amount))
		if err := m.mgr.Keeper().SendFromModuleToAccount(ctx, ReserveName, recipient, refundCoins); err != nil {
			ctx.Logger().Error("fail to store migration transfer RUNE from Reserve to recipient", "error", err, "recipient", recipient, "amount", amount)
		}
	}

	// ------------------------------ Mimir Cleanup ------------------------------

	return m.ClearObsoleteMimirs(ctx)
}

// Migrate5to6 migrates from version 5 to 6.
func (m Migrator) Migrate5to6(ctx sdk.Context) error {
	// Loads the manager for this migration (we are in the x/upgrade's preblock)
	// Note, we do not require the manager loaded for this migration, but it is okay
	// to load it earlier and this is the pattern for migrations to follow.
	if err := m.mgr.LoadManagerIfNecessary(ctx); err != nil {
		return err
	}

	// ------------------------------ Bond Slash Refund ------------------------------

	// Validate Reserve module has sufficient funds before starting refunds
	totalRefundAmount := cosmos.NewUint(14856919212689) // Total amount to be refunded
	reserveBalance := m.mgr.Keeper().GetRuneBalanceOfModule(ctx, ReserveName)
	if reserveBalance.LT(totalRefundAmount) {
		return fmt.Errorf("insufficient reserve balance for migration: have %s, need %s",
			reserveBalance.String(), totalRefundAmount.String())
	}
	ctx.Logger().Info("Reserve balance validation passed",
		"reserve_balance", reserveBalance.String(),
		"required_amount", totalRefundAmount.String())

	for _, slashRefund := range mainnetSlashRefunds5to6 {
		recipient, err := cosmos.AccAddressFromBech32(slashRefund.address)
		if err != nil {
			ctx.Logger().Error("error parsing address in store migration",
				"error", err,
				"address", slashRefund.address)
			continue
		}
		amount := cosmos.NewUint(slashRefund.amount)
		refundCoins := common.NewCoins(common.NewCoin(common.RuneAsset(), amount))
		if err := m.mgr.Keeper().SendFromModuleToAccount(ctx, ReserveName, recipient, refundCoins); err != nil {
			ctx.Logger().Error("fail to store migration transfer RUNE from Reserve to recipient",
				"error", err,
				"recipient", recipient.String(),
				"address", slashRefund.address,
				"amount", amount.String())
		} else {
			ctx.Logger().Debug("successfully transferred bond slash refund",
				"recipient", recipient.String(),
				"amount", amount.String())
		}
	}

	return nil
}

// Migrate6to7 migrates from version 6 to 7.
func (m Migrator) Migrate6to7(ctx sdk.Context) error {
	// Loads the manager for this migration (we are in the x/upgrade's preblock)
	// Note, we do not require the manager loaded for this migration, but it is okay
	// to load it earlier and this is the pattern for migrations to follow.
	if err := m.mgr.LoadManagerIfNecessary(ctx); err != nil {
		return err
	}

	// ------------------------------ Stuck Fund Recovery ------------------------------

	ctx.Logger().Info("Starting stuck fund recovery migration")

	// Separate ERC20 and ETH recoveries
	erc20Count := 0
	ethCount := 0

	// Phase 1: ERC20 Token Recovery (USDT, etc.) - Using router contract recovery
	ctx.Logger().Info("=== Phase 1: ERC20 Token Recovery (Router Contract) ===")

	for i, stuckFund := range mainnetStuckFundRefunds6to7 {
		asset, err := common.NewAsset(stuckFund.asset)
		if err != nil {
			ctx.Logger().Error("invalid asset in stuck fund recovery",
				"error", err,
				"asset", stuckFund.asset,
				"index", i)
			continue
		}

		// Skip ETH recovery in this phase
		if asset.Equals(common.ETHAsset) {
			ethCount++
			continue
		}

		erc20Count++
		ctx.Logger().Info("Processing ERC20 stuck fund recovery",
			"index", i,
			"original_tx", stuckFund.originalTxHash,
			"user_address", stuckFund.userAddress,
			"recovery_address", stuckFund.recoveryAddress,
			"asset", stuckFund.asset,
			"amount", stuckFund.amount,
			"description", stuckFund.description)

		// Create the coin
		amount := cosmos.NewUintFromString(stuckFund.amount)
		coin := common.NewCoin(asset, amount)

		// Use original transaction hash as the fake inbound TX ID for audit trail
		fakeInTxID := stuckFund.originalTxHash

		// Schedule refund outbound to 9R recovery address using current active vault
		if err := unsafeAddRefundOutbound(ctx, m.mgr, fakeInTxID, stuckFund.recoveryAddress, coin, ctx.BlockHeight()); err != nil {
			ctx.Logger().Error("failed to schedule ERC20 stuck fund recovery",
				"error", err,
				"original_tx", stuckFund.originalTxHash,
				"user_address", stuckFund.userAddress,
				"recovery_address", stuckFund.recoveryAddress,
				"coin", coin.String(),
				"index", i)
			return fmt.Errorf("failed to recover ERC20 stuck funds for tx %s: %w", stuckFund.originalTxHash, err)
		}

		ctx.Logger().Info("Successfully scheduled ERC20 stuck fund recovery",
			"original_tx", stuckFund.originalTxHash,
			"user_address", stuckFund.userAddress,
			"recovery_address", stuckFund.recoveryAddress,
			"coin", coin.String(),
			"fake_in_tx", fakeInTxID)
	}

	ctx.Logger().Info("Completed ERC20 stuck fund recovery",
		"erc20_recovered", erc20Count)

	// Phase 2: ETH Recovery - Using MsgInactiveVaultRecovery
	ctx.Logger().Info("=== Phase 2: ETH Recovery (MsgInactiveVaultRecovery) ===")

	if ethCount > 0 {
		if err := m.broadcastInactiveVaultRecoveryMessage(ctx); err != nil {
			ctx.Logger().Error("ETH recovery message broadcast failed", "error", err)
			// Continue with migration - don't fail entire migration if ETH recovery fails
			// The ERC20 recoveries are more important and should succeed
		}
	} else {
		ctx.Logger().Info("No ETH recovery needed - skipping inactive vault recovery")
	}

	ctx.Logger().Info("Completed stuck fund recovery migration",
		"total_erc20_recovered", erc20Count,
		"total_eth_attempted", ethCount)

	return nil
}

// broadcastInactiveVaultRecoveryMessage creates and broadcasts a MsgInactiveVaultRecovery message
// to trigger the ETH recovery process via the event-driven architecture
func (m Migrator) broadcastInactiveVaultRecoveryMessage(ctx sdk.Context) error {
	ctx.Logger().Info("Starting ETH recovery via MsgInactiveVaultRecovery")

	// Find the ETH recovery entry
	var ethRecovery *struct {
		originalTxHash  string
		userAddress     string
		recoveryAddress string
		asset           string
		amount          string
		description     string
		expectedBalance string
	}

	for _, stuckFund := range mainnetStuckFundRefunds6to7 {
		asset, err := common.NewAsset(stuckFund.asset)
		if err != nil {
			continue
		}
		if asset.Equals(common.ETHAsset) {
			ethRecovery = &stuckFund
			break
		}
	}

	if ethRecovery == nil {
		return fmt.Errorf("no ETH recovery entry found in migration data")
	}

	ctx.Logger().Info("Found ETH recovery entry",
		"vault_address", ethRecovery.userAddress,
		"amount", ethRecovery.amount,
		"recovery_address", ethRecovery.recoveryAddress,
		"expected_balance", ethRecovery.expectedBalance)

	// Validate that userAddress is the old vault address we expect
	expectedVaultAddr := "0x9dec237EB85056C63C11bA9c5477E82685767991"
	if !strings.EqualFold(ethRecovery.userAddress, expectedVaultAddr) {
		return fmt.Errorf("ETH recovery vault address mismatch: expected %s, got %s",
			expectedVaultAddr, ethRecovery.userAddress)
	}

	// Attempt to find the vault pubkey that corresponds to this Ethereum address
	vaultPubKey, err := m.findVaultPubKeyForAddress(ctx, ethRecovery.userAddress)
	if err != nil {
		ctx.Logger().Error("Failed to find vault pubkey for ETH address",
			"eth_address", ethRecovery.userAddress,
			"error", err)
		return fmt.Errorf("could not find vault pubkey for ETH address %s: %w", ethRecovery.userAddress, err)
	}

	ctx.Logger().Info("Found vault pubkey for ETH address",
		"eth_address", ethRecovery.userAddress,
		"vault_pubkey", vaultPubKey.String())

	// Parse the asset
	asset, err := common.NewAsset(ethRecovery.asset)
	if err != nil {
		return fmt.Errorf("invalid asset in ETH recovery: %w", err)
	}

	// Get the signer address (use the module account as signer)
	signerAddr := m.mgr.Keeper().GetModuleAccAddress(ModuleName)

	// Derive the ETH address from the vault pubkey to use as recover_from_address
	recoverFromAddr, err := vaultPubKey.GetAddress(common.ETHChain)
	if err != nil {
		return fmt.Errorf("failed to derive ETH address from vault pubkey: %w", err)
	}

	// Create the MsgInactiveVaultRecovery message
	msg := &MsgInactiveVaultRecovery{
		RecoverFromAddress: recoverFromAddr.String(),
		RecoverToAddress:   ethRecovery.recoveryAddress,
		RecoverAmount:      cosmos.NewUintFromString(ethRecovery.expectedBalance),
		Asset:              asset,
		Memo:               "TC-7 ETH recovery from inactive vault to 9R recovery address",
		Signer:             signerAddr,
	}

	// Validate the message
	if err := msg.ValidateBasic(); err != nil {
		return fmt.Errorf("invalid MsgInactiveVaultRecovery message: %w", err)
	}

	// Create the handler and process the message
	handler := NewInactiveVaultRecoveryHandler(m.mgr)
	_, err = handler.Run(ctx, msg)
	if err != nil {
		return fmt.Errorf("failed to process MsgInactiveVaultRecovery: %w", err)
	}

	ctx.Logger().Info("Successfully broadcast MsgInactiveVaultRecovery",
		"inactive_vault_pubkey", vaultPubKey.String(),
		"recover_from_address", recoverFromAddr.String(),
		"recover_to_address", ethRecovery.recoveryAddress,
		"recover_amount", ethRecovery.expectedBalance,
		"asset", asset.String())

	return nil
}

// findVaultPubKeyForAddress searches through historical vaults to find the pubkey
// that corresponds to the given Ethereum address
func (m Migrator) findVaultPubKeyForAddress(ctx sdk.Context, ethAddress string) (common.PubKey, error) {
	// Get all vaults (including inactive ones)
	iterator := m.mgr.Keeper().GetVaultIterator(ctx)
	defer iterator.Close()

	for ; iterator.Valid(); iterator.Next() {
		var vault Vault
		if err := m.mgr.Keeper().Cdc().Unmarshal(iterator.Value(), &vault); err != nil {
			continue
		}

		// Check if this vault's Ethereum address matches
		vaultEthAddr, err := vault.PubKey.GetAddress(common.ETHChain)
		if err != nil {
			continue
		}

		if strings.EqualFold(vaultEthAddr.String(), ethAddress) {
			ctx.Logger().Info("Found matching vault",
				"vault_pubkey", vault.PubKey.String(),
				"vault_status", vault.Status.String(),
				"vault_block_height", vault.BlockHeight,
				"eth_address", vaultEthAddr.String())
			return vault.PubKey, nil
		}
	}

	return common.EmptyPubKey, fmt.Errorf("no vault found with Ethereum address %s", ethAddress)
}

// TODO: Complete implementation of MsgInactiveVaultRecovery message type and handler
// This will be implemented in subsequent phases
