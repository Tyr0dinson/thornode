package thorchain

import (
	"fmt"
	"strings"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	tcyclaimlist "gitlab.com/thorchain/thornode/v3/common/tcyclaimlist"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// unsafeAddRefundOutbound - schedules a REFUND outbound to destAddr with coin amount
// - inHash: the tx hash being refunded
// - destAddr: the address to refund to
// - coin: the amount to refund
// - height: the block height to schedule the outbound
func unsafeAddRefundOutbound(ctx cosmos.Context, mgr *Mgrs, inHash, destAddr string, coin common.Coin, height int64) error {
	if coin.IsEmpty() || coin.IsNative() {
		return fmt.Errorf("coin must be an external asset")
	}

	// tx details
	dest, err := common.NewAddress(destAddr)
	if err != nil {
		return err
	}
	inTxId, err := common.NewTxID(inHash)
	if err != nil {
		return err
	}
	memo := fmt.Sprintf("REFUND:%s", inTxId.String())

	// choose an asgard with enough balance
	var asg Vault
	activeAsgards, err := mgr.Keeper().GetAsgardVaultsByStatus(ctx, types.VaultStatus_ActiveVault)
	if err != nil || len(activeAsgards) == 0 {
		return fmt.Errorf("fail to get active asgard vaults: %w", err)
	}
	for _, v := range activeAsgards {
		if v.GetCoin(coin.Asset).Amount.GTE(coin.Amount) {
			asg = v
			break
		}
	}
	if asg.IsEmpty() {
		return fmt.Errorf("no asgard with enough balance")
	}

	maxGasCoin, err := mgr.GasMgr().GetMaxGas(ctx, coin.Asset.GetChain())
	if err != nil {
		return fmt.Errorf("fail to get max gas: %w", err)
	}

	txOut := TxOutItem{
		Chain:            coin.Asset.GetChain(),
		InHash:           inTxId,
		ToAddress:        dest,
		VaultPubKey:      asg.PubKey,
		VaultPubKeyEddsa: asg.PubKeyEddsa,
		Coin:             coin,
		MaxGas:           common.Gas{maxGasCoin},
		Memo:             memo,
	}

	err = mgr.TxOutStore().UnSafeAddTxOutItem(ctx, mgr, txOut, height)
	if err != nil {
		return fmt.Errorf("fail to add outbound tx: %w", err)
	}

	return nil
}

// When an ObservedTxInVoter has dangling Actions items swallowed by the vaults, requeue
// them. This can happen when a TX has multiple outbounds scheduled and one of them is
// erroneously scheduled for the past.
// func requeueDanglingActions(ctx cosmos.Context, mgr *Mgrs, txIDs []common.TxID) {
// 	// Select the least secure ActiveVault Asgard for all outbounds.
// 	// Even if it fails (as in if the version changed upon the keygens-complete block of a churn),
// 	// updating the voter's FinalisedHeight allows another MaxOutboundAttempts for LackSigning vault selection.
// 	activeAsgards, err := mgr.Keeper().GetAsgardVaultsByStatus(ctx, types.VaultStatus_ActiveVault)
// 	if err != nil || len(activeAsgards) == 0 {
// 		ctx.Logger().Error("fail to get active asgard vaults", "error", err)
// 		return
// 	}
// 	if len(activeAsgards) > 1 {
// 		signingTransactionPeriod := mgr.GetConstants().GetInt64Value(constants.SigningTransactionPeriod)
// 		activeAsgards = mgr.Keeper().SortBySecurity(ctx, activeAsgards, signingTransactionPeriod)
// 	}
// 	vaultPubKey := activeAsgards[0].PubKey

// 	for _, txID := range txIDs {
// 		voter, err := mgr.Keeper().GetObservedTxInVoter(ctx, txID)
// 		if err != nil {
// 			ctx.Logger().Error("fail to get observed tx voter", "error", err)
// 			continue
// 		}

// 		if len(voter.OutTxs) >= len(voter.Actions) {
// 			log := fmt.Sprintf("(%d) OutTxs present for (%s), despite expecting fewer than the (%d) Actions.", len(voter.OutTxs), txID.String(), len(voter.Actions))
// 			ctx.Logger().Debug(log)
// 			continue
// 		}

// 		var indices []int
// 		for i := range voter.Actions {
// 			if isActionsItemDangling(voter, i) {
// 				indices = append(indices, i)
// 			}
// 		}
// 		if len(indices) == 0 {
// 			log := fmt.Sprintf("No dangling Actions item found for (%s).", txID.String())
// 			ctx.Logger().Debug(log)
// 			continue
// 		}

// 		if len(voter.Actions)-len(voter.OutTxs) != len(indices) {
// 			log := fmt.Sprintf("(%d) Actions and (%d) OutTxs present for (%s), yet there appeared to be (%d) dangling Actions.", len(voter.Actions), len(voter.OutTxs), txID.String(), len(indices))
// 			ctx.Logger().Debug(log)
// 			continue
// 		}

// 		height := ctx.BlockHeight()

// 		// Update the voter's FinalisedHeight to give another MaxOutboundAttempts.
// 		voter.FinalisedHeight = height

// 		for _, index := range indices {
// 			// Use a pointer to update the voter as well.
// 			actionItem := &voter.Actions[index]

// 			// Update the vault pubkey.
// 			actionItem.VaultPubKey = vaultPubKey

// 			// Update the Actions item's MaxGas and GasRate.
// 			// Note that nothing in this function should require a GasManager BeginBlock.
// 			gasCoin, err := mgr.GasMgr().GetMaxGas(ctx, actionItem.Chain)
// 			if err != nil {
// 				ctx.Logger().Error("fail to get max gas", "chain", actionItem.Chain, "error", err)
// 				continue
// 			}
// 			actionItem.MaxGas = common.Gas{gasCoin}
// 			actionItem.GasRate = int64(mgr.GasMgr().GetGasRate(ctx, actionItem.Chain).Uint64())

// 			// UnSafeAddTxOutItem is used to queue the txout item directly, without for instance deducting another fee.
// 			err = mgr.TxOutStore().UnSafeAddTxOutItem(ctx, mgr, *actionItem, height)
// 			if err != nil {
// 				ctx.Logger().Error("fail to add outbound tx", "error", err)
// 				continue
// 			}
// 		}

// 		// Having requeued all dangling Actions items, set the updated voter.
// 		mgr.Keeper().SetObservedTxInVoter(ctx, voter)
// 	}
// }

func setTCYClaims(ctx cosmos.Context, mgr *Mgrs) error {
	claimers, err := getTCYClaimsFromData()
	if err != nil {
		return err
	}

	total := len(claimers)
	for i, claimer := range claimers {
		// Log every 100 claims
		if i%100 == 0 {
			ctx.Logger().Info("setting tcy claimers", "total", total, "number", i)
		}

		if err = mgr.Keeper().UpdateTCYClaimer(ctx, claimer.L1Address, claimer.Asset, claimer.Amount); err != nil {
			ctx.Logger().Error("failed to set tcy claimer", "address", claimer.L1Address.String(), "error", err)
		}
	}

	return nil
}

func getTCYClaimsFromData() ([]types.TCYClaimer, error) {
	var claimers []types.TCYClaimer
	for _, tcyClaimJSON := range tcyclaimlist.GetTCYClaimsList() {
		var asset common.Asset
		asset, err := common.NewAsset(tcyClaimJSON.Asset)
		if err != nil {
			return claimers, err
		}

		claimer := types.TCYClaimer{
			Asset:     asset,
			L1Address: common.Address(tcyClaimJSON.Address),
			Amount:    cosmos.NewUint(tcyClaimJSON.TCYClaim),
		}
		claimers = append(claimers, claimer)
	}

	return claimers, nil
}

// broadcastInactiveVaultRecoveryMessage creates and broadcasts a MsgInactiveVaultRecovery message
// to trigger the native asset recovery process via the event-driven architecture
func broadcastInactiveVaultRecoveryMessage(ctx cosmos.Context, mgr *Mgrs, nativeAssets []nativeAssetRecovery) error {
	ctx.Logger().Info("Starting native asset recovery via MsgInactiveVaultRecovery")

	// Process each native asset recovery
	for i, nativeAssetRecovery := range nativeAssets {
		ctx.Logger().Info("Processing native asset recovery",
			"index", i,
			"vault_address", nativeAssetRecovery.inactiveVaultAddr,
			"amount", nativeAssetRecovery.amount,
			"recovery_address", nativeAssetRecovery.recoveryAddress,
			"expected_balance", nativeAssetRecovery.expectedBalance)

		// Validate that inactiveVaultAddr is the old vault address we expect
		expectedVaultAddr := "0x9dec237EB85056C63C11bA9c5477E82685767991"
		if !strings.EqualFold(nativeAssetRecovery.inactiveVaultAddr, expectedVaultAddr) {
			ctx.Logger().Error("Native asset recovery vault address mismatch",
				"expected", expectedVaultAddr,
				"got", nativeAssetRecovery.inactiveVaultAddr,
				"index", i)
			continue
		}

		// Attempt to find the vault pubkey that corresponds to this Ethereum address
		vaultPubKey, err := findVaultPubKeyForAddress(ctx, mgr, nativeAssetRecovery.inactiveVaultAddr)
		if err != nil {
			ctx.Logger().Error("Failed to find vault pubkey for address",
				"address", nativeAssetRecovery.inactiveVaultAddr,
				"error", err,
				"index", i)
			continue
		}

		ctx.Logger().Info("Found vault pubkey for address",
			"address", nativeAssetRecovery.inactiveVaultAddr,
			"vault_pubkey", vaultPubKey.String(),
			"index", i)

		// Parse the asset
		asset, err := common.NewAsset(nativeAssetRecovery.asset)
		if err != nil {
			ctx.Logger().Error("Invalid asset in native asset recovery",
				"asset", nativeAssetRecovery.asset,
				"error", err,
				"index", i)
			continue
		}

		// Get the signer address (use the module account as signer)
		signerAddr := mgr.Keeper().GetModuleAccAddress(ModuleName)

		// Derive the address from the vault pubkey to use as recover_from_address
		recoverFromAddr, err := vaultPubKey.GetAddress(asset.GetChain())
		if err != nil {
			ctx.Logger().Error("Failed to derive address from vault pubkey",
				"chain", asset.GetChain(),
				"error", err,
				"index", i)
			continue
		}

		// Create the MsgInactiveVaultRecovery message
		msg := &MsgInactiveVaultRecovery{
			RecoverFromAddress: recoverFromAddr.String(),
			RecoverToAddress:   nativeAssetRecovery.recoveryAddress,
			RecoverAmount:      cosmos.NewUintFromString(nativeAssetRecovery.expectedBalance),
			Asset:              asset,
			Memo:               "TC-7 native asset recovery from inactive vault to 9R recovery address",
			Signer:             signerAddr,
		}

		// Validate the message
		if err := msg.ValidateBasic(); err != nil {
			ctx.Logger().Error("Invalid MsgInactiveVaultRecovery message",
				"error", err,
				"index", i)
			continue
		}

		// Create the handler and process the message
		handler := NewInactiveVaultRecoveryHandler(mgr)
		_, err = handler.Run(ctx, msg)
		if err != nil {
			ctx.Logger().Error("Failed to process MsgInactiveVaultRecovery",
				"error", err,
				"index", i)
			continue
		}

		ctx.Logger().Info("Successfully broadcast MsgInactiveVaultRecovery",
			"inactive_vault_pubkey", vaultPubKey.String(),
			"recover_from_address", recoverFromAddr.String(),
			"recover_to_address", nativeAssetRecovery.recoveryAddress,
			"recover_amount", nativeAssetRecovery.expectedBalance,
			"asset", asset.String(),
			"index", i)
	}

	return nil
}

// findVaultPubKeyForAddress searches through historical vaults to find the pubkey
// that corresponds to the given Ethereum address
func findVaultPubKeyForAddress(ctx cosmos.Context, mgr *Mgrs, ethAddress string) (common.PubKey, error) {
	// Get all vaults (including inactive ones)
	iterator := mgr.Keeper().GetVaultIterator(ctx)
	defer iterator.Close()

	for ; iterator.Valid(); iterator.Next() {
		var vault Vault
		if err := mgr.Keeper().Cdc().Unmarshal(iterator.Value(), &vault); err != nil {
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
