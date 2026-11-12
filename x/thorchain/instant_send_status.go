package thorchain

import (
	"slices"
	"strconv"
	"strings"

	"github.com/cosmos/cosmos-sdk/telemetry"
	"github.com/hashicorp/go-metrics"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"
)

func instantSendTimeout(ctx cosmos.Context, k keeper.Keeper) (int64, error) {
	timeout, err := k.GetMimirWithRef(ctx, constants.MimirTemplateInstantSendTimeout, common.DASHChain)
	if timeout <= 0 {
		timeout = defaultInstantSendTimeout
	}
	return timeout, err
}

// Timeout halts require manual clearance, even after late ChainLock settlement.
func haltInstantSend(ctx cosmos.Context, mgr Manager) error {
	k := mgr.Keeper()
	for _, key := range []string{"HaltDASHTrading", "HaltSigningDASH"} {
		height, err := k.GetMimir(ctx, key)
		if err != nil {
			return err
		}
		if height > 0 && height <= ctx.BlockHeight() {
			continue
		}
		k.SetMimir(ctx, key, ctx.BlockHeight())
		err = mgr.EventMgr().EmitEvent(ctx, NewEventSetMimir(strings.ToUpper(key), strconv.FormatInt(ctx.BlockHeight(), 10)))
		if err != nil {
			return err
		}
	}
	return nil
}

func clearInstantSendTimeout(ctx cosmos.Context, k keeper.Keeper, id common.TxID) error {
	status, err := k.GetInstantSendStatus(ctx, common.DASHChain)
	if err != nil {
		return err
	}
	status.Overdue = slices.DeleteFunc(status.Overdue, func(other common.TxID) bool { return other.Equals(id) })
	return k.SetInstantSendStatus(ctx, status)
}

// Timeout causes are retained until an actual ChainLock settlement. Changing
// the timeout cannot clear an existing cause. No clock or daemon timestamp is used.
func checkInstantSend(ctx cosmos.Context, mgr Manager) {
	cache, commit := ctx.CacheContext()
	err := updateInstantSendTimeouts(cache, mgr)
	if err == nil {
		commit()
		pruneInstantSendCredits(ctx, mgr.Keeper())
		return
	}
	ctx.Logger().Error("fail to check InstantSend settlement", "error", err)
	// Keep the halt even if malformed accounting prevents settlement checks.
	err = haltInstantSend(ctx, mgr)
	if err != nil {
		ctx.Logger().Error("fail to halt InstantSend", "error", err)
	}
}

func pruneInstantSendCredits(ctx cosmos.Context, k keeper.Keeper) {
	cache, commit := ctx.CacheContext()
	height, err := k.GetLastChainHeight(cache, common.DASHChain)
	defer func() {
		if err != nil {
			telemetry.IncrCounter(float32(1), "thornode", "instantsend", "prune_error")
		}
	}()
	if err != nil {
		ctx.Logger().Error("fail to read DASH height for input pruning", "error", err)
		return
	}
	if height <= 0 {
		return
	}
	status, err := k.GetInstantSendStatus(cache, common.DASHChain)
	if err == nil {
		status.MinHeight = status.MinimumHeight(height)
		err = k.SetInstantSendStatus(cache, status)
	}
	if err == nil {
		err = k.PruneInstantSendCredits(cache, status.MinHeight, 5000)
	}
	if err != nil {
		ctx.Logger().Error("fail to prune DASH input credits", "error", err)
		return
	}
	commit()
}

func updateInstantSendTimeouts(ctx cosmos.Context, mgr Manager) error {
	k := mgr.Keeper()
	status, err := k.GetInstantSendStatus(ctx, common.DASHChain)
	if err != nil {
		return err
	}
	timeout, err := instantSendTimeout(ctx, k)
	if err != nil {
		return err
	}
	states, err := k.ListInstantSend(ctx, common.DASHChain)
	if err != nil {
		return err
	}
	var age int64
	for _, state := range states {
		err = validateInstantSendPending(ctx, k, state)
		if err != nil {
			return err
		}
		for _, entry := range state.Pending {
			age = max(age, ctx.BlockHeight()-entry.Height)
			if ctx.BlockHeight()-entry.Height >= timeout && !slices.Contains(status.Overdue, entry.Id) {
				status.Overdue = append(status.Overdue, entry.Id)
			}
		}
	}
	labels := []metrics.Label{telemetry.NewLabel("chain", common.DASHChain.String())}
	telemetry.SetGaugeWithLabels([]string{"thornode", "instantsend", "oldest_age_blocks"}, float32(age), labels)
	slices.Sort(status.Overdue)
	err = k.SetInstantSendStatus(ctx, status)
	if err != nil {
		return err
	}
	if len(status.Overdue) > 0 {
		return haltInstantSend(ctx, mgr)
	}
	return nil
}

func instantSendInputsAvailable(ctx cosmos.Context, k keeper.Keeper, tx common.ObservedTx) (bool, error) {
	err := common.ValidateDashInputs(tx.SpentOutputRefs)
	if err != nil {
		return false, err
	}
	for _, input := range tx.SpentOutputRefs {
		credit, readErr := k.GetInstantSendInput(ctx, input)
		if readErr != nil {
			return false, readErr
		}
		if !credit.Id.IsEmpty() && !credit.Id.Equals(tx.Tx.ID) {
			return false, nil
		}
	}
	return true, nil
}

// Called immediately before customer vault credit. Internal transfers retain
// their usual accounting. A conflicting customer's memo must never execute.
func recordInstantSendInputs(ctx cosmos.Context, k keeper.Keeper, tx common.ObservedTx) (bool, error) {
	if !tx.Tx.Chain.Equals(common.DASHChain) {
		return false, nil
	}
	available, err := instantSendInputsAvailable(ctx, k, tx)
	if err != nil || !available {
		return !available, err
	}
	sender, err := instantSendVaultSender(ctx, k, tx.Tx.Chain, tx.Tx.FromAddress)
	if err != nil || sender {
		return false, err
	}
	credit := common.InstantSendCredit{Id: tx.Tx.ID, Inputs: tx.SpentOutputRefs, Early: tx.InstantSend != nil}
	if !credit.Early {
		height, readErr := k.GetLastChainHeight(ctx, common.DASHChain)
		if readErr != nil {
			return false, readErr
		}
		credit.Height = max(height, tx.BlockHeight)
	}
	err = k.SetInstantSendCredit(ctx, credit)
	return false, err
}
