package thorchain

import (
	"github.com/blang/semver"

	"gitlab.com/thorchain/thornode/common"
	cosmos "gitlab.com/thorchain/thornode/common/cosmos"
	"gitlab.com/thorchain/thornode/constants"
	keeper "gitlab.com/thorchain/thornode/x/thorchain/keeper"
)

// LedgersHandler is design to process MsgLedger
type LedgersHandler struct {
	keeper keeper.Keeper
	mgr    Manager
}

func NewLedgersHandler(keeper keeper.Keeper, mgr Manager) LedgersHandler {
	return LedgersHandler{
		keeper: keeper,
		mgr:    mgr,
	}
}

func (h LedgersHandler) Run(ctx cosmos.Context, m cosmos.Msg, version semver.Version, constAccessor constants.ConstantValues) (*cosmos.Result, error) {
	msg, ok := m.(MsgLedger)
	if !ok {
		return nil, errInvalidMessage
	}
	err := h.validate(ctx, msg, version)
	if err != nil {
		ctx.Logger().Error("MsgLedger failed validation", "error", err)
		return nil, err
	}
	result, err := h.handle(ctx, msg, version, constAccessor)
	if err != nil {
		ctx.Logger().Error("failed to process MsgLedger", "error", err)
	}
	return result, err
}

func (h LedgersHandler) validate(ctx cosmos.Context, msg MsgLedger, version semver.Version) error {
	if version.GTE(semver.MustParse("0.1.0")) {
		return h.validateV1(ctx, msg)
	}
	return errBadVersion
}

func (h LedgersHandler) validateV1(ctx cosmos.Context, msg MsgLedger) error {
	if err := msg.ValidateBasic(); err != nil {
		return err
	}

	if !isSignedByActiveNodeAccounts(ctx, h.keeper, msg.GetSigners()) {
		return cosmos.ErrUnauthorized("not authorized")
	}

	return nil
}

func (h LedgersHandler) handle(ctx cosmos.Context, msg MsgLedger, version semver.Version, constAccessor constants.ConstantValues) (*cosmos.Result, error) {
	ctx.Logger().Info("handle MsgLedger request", "ID:", msg.ID)
	if version.GTE(semver.MustParse("0.1.0")) {
		return h.handleV1(ctx, msg, version, constAccessor)
	}
	return nil, errBadVersion
}

func (h LedgersHandler) handleV1(ctx cosmos.Context, msg MsgLedger, version semver.Version, constAccessor constants.ConstantValues) (*cosmos.Result, error) {
	observeSlashPoints := constAccessor.GetInt64Value(constants.ObserveSlashPoints)

	active, err := h.keeper.ListActiveNodeAccounts(ctx)
	if err != nil {
		return nil, wrapError(ctx, err, "fail to get list of active node accounts")
	}

	voter, err := h.keeper.GetLedgerVoter(ctx, msg.ID)
	if err != nil {
		return nil, err
	}
	h.mgr.Slasher().IncSlashPoints(ctx, observeSlashPoints, msg.Signer)
	if !voter.Sign(msg.Signer) {
		ctx.Logger().Info("signer already signed MsgLedger", "signer", msg.Signer.String(), "txid", msg.ID)
		return &cosmos.Result{}, nil
	}
	h.keeper.SetLedgerVoter(ctx, voter)
	// doesn't have consensus yet
	if !voter.HasConsensus(active) {
		ctx.Logger().Info("not having consensus yet, return")
		return &cosmos.Result{}, nil
	}

	if voter.BlockHeight == 0 {
		voter.BlockHeight = common.BlockHeight(ctx)
		h.keeper.SetLedgerVoter(ctx, voter)

		ledgerTotalCoins := make(common.Coins, 0) // total coins in ledgers
		vaultTotalCoins := make(common.Coins, 0)  // total coins in vaults
		vaultSurplus := make(common.Coins, 0)     // vault surplus of coins
		vaultDeficit := make(common.Coins, 0)     // vault missing coins
		poolSurplus := make(common.Coins, 0)      // pool surplus of coins
		poolDeficit := make(common.Coins, 0)      // pool deficit of coins

		for _, ledger := range msg.Ledgers {
			vault, err := h.keeper.GetVault(ctx, ledger.PubKey)
			if err != nil {
				ctx.Logger().Error("fail to get vault", "error", err)
				return &cosmos.Result{}, err
			}
			if vault.Status == ActiveVault || vault.Status == RetiringVault {
				filteredLedgerCoins := make(common.Coins, 0) // ledger coins, minus non-supported coins

				// total up coins
				for _, coin := range ledger.Coins {
					if h.keeper.PoolExist(ctx, coin.Asset) || coin.Asset.IsRune() {
						ledgerTotalCoins = ledgerTotalCoins.Add(coin)
						filteredLedgerCoins = filteredLedgerCoins.Add(coin)
					}
				}

				for _, coin := range vault.Coins {
					if h.keeper.PoolExist(ctx, coin.Asset) || coin.Asset.IsRune() {
						vaultTotalCoins = vaultTotalCoins.Add(coin)
					}
				}

				vault.Coins = filteredLedgerCoins
				if !vault.HasFunds() {
					vault.Status = InactiveVault
				}
				if err := h.keeper.SetVault(ctx, vault); err != nil {
					ctx.Logger().Error("fail to set vault", "error", err)
					return &cosmos.Result{}, err
				}
			}
		}

		// Calculate the vault surplus/deficits, if any.
		// decide which coin list we want to iterate over. The longer coin list wins.
		if len(ledgerTotalCoins) >= len(vaultTotalCoins) {
			for _, ledgerCoin := range ledgerTotalCoins {
				vaultCoin := vaultTotalCoins.GetCoin(ledgerCoin.Asset)
				if ledgerCoin.Amount.GT(vaultCoin.Amount) {
					coin := common.NewCoin(ledgerCoin.Asset, ledgerCoin.Amount.Sub(vaultCoin.Amount))
					vaultSurplus = vaultSurplus.Add(coin)
				} else if ledgerCoin.Amount.LT(vaultCoin.Amount) {
					coin := common.NewCoin(vaultCoin.Asset, vaultCoin.Amount.Sub(ledgerCoin.Amount))
					vaultDeficit = vaultDeficit.Add(coin)
				}
			}
		} else {
			for _, vaultCoin := range vaultTotalCoins {
				ledgerCoin := ledgerTotalCoins.GetCoin(vaultCoin.Asset)
				if ledgerCoin.Amount.GT(vaultCoin.Amount) {
					coin := common.NewCoin(ledgerCoin.Asset, ledgerCoin.Amount.Sub(vaultCoin.Amount))
					vaultSurplus = vaultSurplus.Add(coin)
				} else if ledgerCoin.Amount.LT(vaultCoin.Amount) {
					coin := common.NewCoin(vaultCoin.Asset, vaultCoin.Amount.Sub(ledgerCoin.Amount))
					vaultDeficit = vaultDeficit.Add(coin)
				}
			}
		}

		// Calculate the pool surplus/deficits, if any.
		iter := h.keeper.GetPoolIterator(ctx)
		defer iter.Close()
		for ; iter.Valid(); iter.Next() {
			var pool Pool
			h.keeper.Cdc().MustUnmarshalBinaryBare(iter.Value(), &pool)
			coin := ledgerTotalCoins.GetCoin(pool.Asset)
			if pool.BalanceAsset.GT(coin.Amount) {
				coin = common.NewCoin(pool.Asset, pool.BalanceAsset.Sub(coin.Amount))
				poolSurplus = poolSurplus.Add(coin)
			} else if pool.BalanceAsset.LT(coin.Amount) {
				coin = common.NewCoin(pool.Asset, coin.Amount.Sub(pool.BalanceAsset))
				poolDeficit = poolDeficit.Add(coin)
			}

			pool.BalanceAsset = coin.Amount
			if pool.BalanceAsset.IsZero() {
				pool.Status = PoolBootstrap
			}
			if err := h.keeper.SetPool(ctx, pool); err != nil {
				ctx.Logger().Error("fail to set pool", "error", err)
				return &cosmos.Result{}, err
			}
		}

		evt := NewEventSettlement(vaultSurplus, vaultDeficit, poolSurplus, poolDeficit)
		if err := h.mgr.EventMgr().EmitSettlementEvent(ctx, evt); err != nil {
			return nil, ErrInternal(err, "fail to emit settlement event")
		}

		// fail to generate a new tss key let's slash the node account
		h.mgr.Slasher().DecSlashPoints(ctx, observeSlashPoints, voter.Signers...)
		return &cosmos.Result{}, nil
	}
	if voter.BlockHeight == common.BlockHeight(ctx) {
		h.mgr.Slasher().DecSlashPoints(ctx, observeSlashPoints, msg.Signer)
	}

	return &cosmos.Result{}, nil
}
