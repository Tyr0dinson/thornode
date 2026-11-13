package thorchain

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/cosmos/cosmos-sdk/telemetry"
	"github.com/hashicorp/go-metrics"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

const defaultInstantSendTimeout int64 = 300

// instantSendVaultSender identifies transfers handled by ordinary vault observations.
func instantSendVaultSender(ctx cosmos.Context, k keeper.Keeper, chain common.Chain, sender common.Address) (bool, error) {
	iter := k.GetVaultIterator(ctx)
	defer iter.Close()
	for ; iter.Valid(); iter.Next() {
		var vault Vault
		err := k.Cdc().Unmarshal(iter.Value(), &vault)
		if err != nil {
			return false, err
		}
		if !vault.IsAsgard() {
			continue
		}
		address, err := vault.PubKey.GetAddress(chain)
		if err != nil {
			return false, err
		}
		if address.Equals(sender) {
			return true, nil
		}
	}
	return false, nil
}

// instantSendEnabled fails closed on policy read failures. Settlement and
// ordinary ChainLock processing deliberately do not use this admission check.
func instantSendEnabled(ctx cosmos.Context, k keeper.Keeper, chain common.Chain) (bool, error) {
	status, err := k.GetInstantSendStatus(ctx, chain)
	if err != nil || status.Disabled {
		return false, err
	}
	enabled, err := k.GetMimirWithRef(ctx, constants.MimirTemplateInstantSendEnabled, chain)
	if err != nil || enabled <= 0 {
		return false, err
	}
	for _, key := range []string{
		"Halt" + chain.String() + "Chain", "SolvencyHalt" + chain.String() + "Chain",
		"HaltChainGlobal", fmt.Sprintf(constants.MimirTemplateHaltTrading, chain),
	} {
		var haltHeight int64
		haltHeight, err = k.GetMimir(ctx, key)
		if err != nil {
			return false, err
		}
		if haltHeight > 0 && haltHeight <= ctx.BlockHeight() {
			return false, nil
		}
	}
	pauseHeight, err := k.GetMimir(ctx, "NodePauseChainGlobal")
	return err == nil && !(pauseHeight > 0 && ctx.BlockHeight() <= pauseHeight), err
}

// prepareInstantSendCredit records exposure before any economic action. The caller
// commits its cache only if the deposit was credited and its swap was queued.
func prepareInstantSendCredit(ctx cosmos.Context, mgr Manager, tx common.ObservedTx, voter ObservedTxVoter, signatures, active int) (*common.InstantSendState, bool, error) {
	k := mgr.Keeper()
	if !tx.Tx.Chain.Equals(common.DASHChain) {
		return nil, false, errors.New("chain does not support InstantSend")
	}
	if !voter.Tx.IsEmpty() && !voter.Tx.Tx.Chain.Equals(tx.Tx.Chain) {
		return nil, false, errors.New("InstantSend voter chain mismatch")
	}
	if voter.FinalisedHeight > 0 {
		return nil, true, nil
	}
	if !types.HasSuperMajority(signatures, active) {
		return nil, false, nil
	}
	enabled, err := instantSendEnabled(ctx, k, tx.Tx.Chain)
	if err != nil || !enabled {
		return nil, false, err
	}
	available, err := instantSendInputsAvailable(ctx, k, tx)
	if err != nil || !available {
		return nil, false, err
	}
	height, err := k.GetLastChainHeight(ctx, tx.Tx.Chain)
	if err != nil {
		return nil, false, err
	}
	status, err := k.GetInstantSendStatus(ctx, tx.Tx.Chain)
	if err != nil {
		return nil, false, err
	}
	if tx.BlockHeight != common.InstantSendAnchor(tx.BlockHeight) ||
		tx.BlockHeight < status.MinimumHeight(height) || tx.BlockHeight > height {
		return nil, false, nil
	}
	vault, err := k.GetVault(ctx, tx.ObservedPubKey)
	if err != nil {
		return nil, false, err
	}
	if !vault.IsAsgard() || vault.Status != ActiveVault || !vault.HasChain(tx.Tx.Chain) {
		return nil, false, nil
	}
	compromised, err := isVaultCompromised(ctx, k, vault.PubKey)
	if err != nil || compromised {
		return nil, false, err
	}
	sender, err := instantSendVaultSender(ctx, k, tx.Tx.Chain, tx.Tx.FromAddress)
	if err != nil || sender {
		return nil, false, err
	}
	memo, err := ParseMemoWithTHORNames(ctx, k, tx.Tx.Memo)
	if err != nil || !memo.IsType(TxSwap) {
		return nil, false, nil
	}
	minAmount, maxAmount, err := instantSendLimits(ctx, k, tx.Tx.Chain)
	if err != nil {
		return nil, false, err
	}
	amount := tx.Tx.Coins.GetCoin(common.DASHAsset).Amount
	if amount.IsZero() || !amount.BigInt().IsUint64() || amount.Uint64() < minAmount || maxAmount == 0 {
		return nil, false, nil
	}
	states, err := k.ListInstantSend(ctx, tx.Tx.Chain)
	if err != nil {
		return nil, false, err
	}
	total := cosmos.ZeroUint()
	state := common.InstantSendState{Chain: tx.Tx.Chain, PubKey: tx.ObservedPubKey}
	for _, entry := range states {
		for _, pending := range entry.Pending {
			total = total.Add(cosmos.NewUint(pending.Amount))
			if pending.Id.Equals(tx.Tx.ID) {
				return nil, false, errors.New("duplicate pending InstantSend deposit")
			}
		}
		if entry.PubKey.Equals(tx.ObservedPubKey) {
			state = entry
		}
	}
	if len(state.Pending) >= common.InstantSendMaxPending || total.Add(amount).GT(cosmos.NewUint(maxAmount)) {
		return nil, false, nil
	}
	state.Pending = append(state.Pending, common.PendingInbound{Id: tx.Tx.ID, Amount: amount.Uint64(), Height: ctx.BlockHeight()})
	slices.SortFunc(state.Pending, func(a, b common.PendingInbound) int { return strings.Compare(a.Id.String(), b.Id.String()) })
	err = k.SetInstantSend(ctx, state)
	return &state, err == nil, err
}

func instantSendLimits(ctx cosmos.Context, k keeper.Keeper, chain common.Chain) (uint64, uint64, error) {
	minimum, err := k.GetMimirWithRef(ctx, constants.MimirTemplateInstantSendMinAmount, chain)
	if err != nil {
		return 0, 0, err
	}
	maximum, err := k.GetMimirWithRef(ctx, constants.MimirTemplateInstantSendMaxAmount, chain)
	if err != nil {
		return 0, 0, err
	}
	if minimum < 0 {
		minimum = int64(common.DefaultInstantSendMinAmount)
	}
	return uint64(minimum), uint64(max(maximum, 0)), nil
}

// settleInstantSend handles the second observation separately from economic finality.
// Early signatures cannot release exposure, including after validator churn.
func settleInstantSend(ctx cosmos.Context, mgr Manager, tx common.ObservedTx, voter ObservedTxVoter, signers []cosmos.AccAddress, nodes NodeAccounts, slashDuplicates bool) error {
	if tx.InstantSend != nil || !tx.IsFinal() {
		return nil
	}
	if !tx.Tx.Chain.Equals(common.DASHChain) || !voter.Tx.Tx.Chain.Equals(tx.Tx.Chain) ||
		!voter.Tx.ObservedPubKey.Equals(tx.ObservedPubKey) || !voter.Tx.Tx.ID.Equals(tx.Tx.ID) ||
		!voter.Tx.Tx.Coins.EqualsEx(tx.Tx.Coins) || !voter.Tx.Tx.FromAddress.Equals(tx.Tx.FromAddress) ||
		!voter.Tx.Tx.ToAddress.Equals(tx.Tx.ToAddress) || !voter.Tx.HasEquivalentSpentOutputRefs(tx) {
		return errors.New("ChainLock deposit mismatch")
	}
	k := mgr.Keeper()
	changed := false
	for _, signer := range signers {
		var added bool
		voter, _, added = applyTxInAttestation(ctx, mgr, voter, nodes, tx, signer, slashDuplicates)
		changed = changed || added
	}
	if changed {
		k.SetObservedTxInVoter(ctx, voter)
	}
	// Keep partial certificates in the ordinary voter. Only this exact final
	// variant counts; the cached economic consensus includes early signatures.
	count := 0
	for _, observed := range voter.Txs {
		if !observed.Equals(tx) {
			continue
		}
		for _, node := range nodes {
			if observed.HasSigned(node.NodeAddress) {
				count++
			}
		}
		break
	}
	if !types.HasSuperMajority(count, len(nodes)) {
		return nil
	}
	state, err := k.GetInstantSend(ctx, tx.Tx.Chain, tx.ObservedPubKey)
	if err != nil {
		return err
	}
	index := slices.IndexFunc(state.Pending, func(entry common.PendingInbound) bool { return entry.Id.Equals(tx.Tx.ID) })
	if index < 0 || voter.Reverted {
		return nil
	}
	credit, err := k.GetInstantSendCredit(ctx, tx.Tx.ID)
	if err != nil {
		return err
	}
	height, err := k.GetLastChainHeight(ctx, common.DASHChain)
	if err != nil {
		return err
	}
	credit.Height = max(height, tx.BlockHeight)
	err = k.SetInstantSendCredit(ctx, credit)
	if err != nil {
		return err
	}
	state.Pending = slices.Delete(state.Pending, index, index+1)
	err = k.SetInstantSend(ctx, state)
	if err == nil {
		err = clearInstantSendTimeout(ctx, k, tx.Tx.ID)
	}
	if err == nil {
		updateInstantSendMetrics(state)
	}
	return err
}

// validateInstantSendPending checks the ledger against its credited voters.
func validateInstantSendPending(ctx cosmos.Context, k keeper.Keeper, state common.InstantSendState) error {
	for _, entry := range state.Pending {
		voter, err := k.GetObservedTxInVoter(ctx, entry.Id)
		if err != nil {
			return err
		}
		if entry.Height > ctx.BlockHeight() || voter.FinalisedHeight > ctx.BlockHeight() {
			return errors.New("future InstantSend credit height")
		}
		err = validateInstantSendVoter(state, entry, voter)
		if err != nil {
			return err
		}
	}
	return nil
}

// Shared with genesis validation, which has no current THORChain block height.
func validateInstantSendVoter(state common.InstantSendState, entry common.PendingInbound, voter ObservedTxVoter) error {
	valid := entry.Height > 0 && voter.FinalisedHeight >= entry.Height &&
		voter.Tx.InstantSend != nil && voter.UpdatedVault && !voter.Reverted &&
		voter.Tx.IsFinal() && voter.TxID.Equals(entry.Id) && voter.Tx.Tx.ID.Equals(entry.Id) &&
		voter.Tx.ObservedPubKey.Equals(state.PubKey) && voter.Tx.Tx.Chain.Equals(state.Chain) &&
		voter.Tx.Tx.Coins.GetCoin(state.Chain.GetGasAsset()).Amount.Equal(cosmos.NewUint(entry.Amount))
	if !valid {
		return errors.New("inconsistent InstantSend credit")
	}
	return nil
}

// solvencyVault excuses pending deposits only until they are overdue.
// The independent timeout halt remains necessary because solvency also discounts outbounds.
func solvencyVault(ctx cosmos.Context, k keeper.Keeper, vault Vault, chain common.Chain) (Vault, error) {
	if !chain.Equals(common.DASHChain) {
		return vault, nil
	}
	state, err := k.GetInstantSend(ctx, chain, vault.PubKey)
	if err != nil {
		return vault, err
	}
	err = validateInstantSendPending(ctx, k, state)
	if err != nil {
		return vault, err
	}
	status, err := k.GetInstantSendStatus(ctx, chain)
	if err != nil {
		return vault, err
	}
	// A ChainLocked deposit can already have been spent before its settlement
	// certificate arrives. Pending exposure may therefore exceed vault funds.
	vault.Coins = vault.Coins.Copy().SafeSub(common.NewCoin(chain.GetGasAsset(), state.SolvencyAllowance(status.Overdue)))
	return vault, nil
}

// InstantSend returns pending exposure and vault funds from the same keeper snapshot.
func (s *queryServer) InstantSend(c context.Context, req *types.QueryInstantSendRequest) (*types.QueryInstantSendResponse, error) {
	err := checkHeightParam(req.Height)
	if err != nil {
		return nil, err
	}
	chain, err := common.NewChain(req.Chain)
	if err != nil {
		return nil, err
	}
	if !chain.Equals(common.DASHChain) {
		return nil, errors.New("chain does not support InstantSend")
	}
	ctx := s.unwrapSdkContext(c)
	k := s.mgr.Keeper()
	minimum, maximum, err := instantSendLimits(ctx, k, chain)
	if err != nil {
		return nil, err
	}
	height, err := k.GetLastChainHeight(ctx, chain)
	if err != nil {
		return nil, err
	}
	result := &types.QueryInstantSendResponse{ObservationHeight: height, MinAmount: minimum, MaxAmount: maximum}
	result.Status, err = k.GetInstantSendStatus(ctx, chain)
	if err != nil {
		return nil, err
	}
	result.Status.MinHeight = result.Status.MinimumHeight(height)
	iter := k.GetVaultIterator(ctx)
	defer iter.Close()
	for ; iter.Valid(); iter.Next() {
		var vault Vault
		if err = k.Cdc().Unmarshal(iter.Value(), &vault); err != nil {
			return nil, err
		}
		if !vault.IsAsgard() {
			continue
		}
		state, stateErr := k.GetInstantSend(ctx, chain, vault.PubKey)
		if stateErr != nil {
			return nil, stateErr
		}
		flag, flagErr := k.GetMimir(ctx, solvencyHaltVaultKey(vault.PubKey, chain))
		if flagErr != nil {
			return nil, flagErr
		}
		active := vault.Status == ActiveVault && vault.HasChain(chain)
		funded := vault.HasFundsForChain(chain)
		if !active && !funded && len(state.Pending) == 0 && flag <= 0 {
			continue
		}
		creditErr := validateInstantSendPending(ctx, k, state)
		if creditErr != nil {
			return nil, creditErr
		}
		compromised, flagErr := isVaultCompromised(ctx, k, vault.PubKey)
		if flagErr != nil {
			return nil, flagErr
		}
		result.Vaults = append(result.Vaults, types.QueryInstantSendVault{State: state, Coins: vault.Coins.Copy(), Eligible: active && !compromised})
	}
	return result, nil
}

func updateInstantSendMetrics(state common.InstantSendState) {
	labels := []metrics.Label{telemetry.NewLabel("pubkey", state.PubKey.String())}
	telemetry.SetGaugeWithLabels([]string{"thornode", strings.ToLower(state.Chain.String()), "pending_count"}, float32(len(state.Pending)), labels)
	telemetry.SetGaugeWithLabels([]string{"thornode", strings.ToLower(state.Chain.String()), "pending_amount"}, telem(state.PendingAmount()), labels)
}
