package thorchain

import (
	"fmt"

	"github.com/blang/semver"

	"gitlab.com/thorchain/thornode/common"
	"gitlab.com/thorchain/thornode/common/cosmos"
	"gitlab.com/thorchain/thornode/constants"
	"gitlab.com/thorchain/thornode/x/thorchain/keeper"
)

// CommonOutboundTxHandler is the place where those common logic can be shared
// between multiple different kind of outbound tx handler
// at the moment, handler_refund, and handler_outbound_tx are largely the same
// , only some small difference
type CommonOutboundTxHandler struct {
	keeper keeper.Keeper
	mgr    Manager
}

// NewCommonOutboundTxHandler create a new instance of the CommonOutboundTxHandler
func NewCommonOutboundTxHandler(k keeper.Keeper, mgr Manager) CommonOutboundTxHandler {
	return CommonOutboundTxHandler{
		keeper: k,
		mgr:    mgr,
	}
}

func (h CommonOutboundTxHandler) slash(ctx cosmos.Context, version semver.Version, tx ObservedTx) error {
	var returnErr error
	for _, c := range tx.Tx.Coins {
		if err := h.mgr.Slasher().SlashNodeAccount(ctx, tx.ObservedPubKey, c.Asset, c.Amount, h.mgr); err != nil {
			ctx.Logger().Error("fail to slash account", "error", err)
			returnErr = err
		}
	}
	return returnErr
}

func (h CommonOutboundTxHandler) handle(ctx cosmos.Context, version semver.Version, tx ObservedTx, inTxID common.TxID, constAccessor constants.ConstantValues) (*cosmos.Result, error) {
	// note: Outbound tx usually it is related to an inbound tx except migration
	// thus here try to get the ObservedTxInVoter,  and set the tx out hash accordingly
	voter, err := h.keeper.GetObservedTxInVoter(ctx, inTxID)
	if err != nil {
		return nil, ErrInternal(err, "fail to get observed tx voter")
	}
	voter.AddOutTx(tx.Tx)
	h.keeper.SetObservedTxInVoter(ctx, voter)

	// complete events
	if voter.IsDone() {
		for _, item := range voter.OutTxs {
			if err := h.mgr.EventMgr().EmitEvent(ctx, NewEventOutbound(inTxID, item)); err != nil {
				return nil, ErrInternal(err, "fail to emit outbound event")
			}
		}
	}

	if tx.Tx.Chain.Equals(common.THORChain) {
		return &cosmos.Result{}, nil
	}

	shouldSlash := true
	signingTransPeriod := constAccessor.GetInt64Value(constants.SigningTransactionPeriod)
	// every Signing Transaction Period , THORNode will check whether a
	// TxOutItem had been sent by signer or not
	// if a txout item that is older than SigningTransactionPeriod, but has not
	// been sent out by signer , then it will create a new TxOutItem
	// here THORNode will have to mark all the TxOutItem to complete one the tx
	// get processed
	for height := voter.FinalisedHeight; height <= common.BlockHeight(ctx); height += signingTransPeriod {

		// update txOut record with our TxID that sent funds out of the pool
		txOut, err := h.keeper.GetTxOut(ctx, height)
		if err != nil {
			ctx.Logger().Error("unable to get txOut record", "error", err)
			return nil, cosmos.ErrUnknownRequest(err.Error())
		}

		// Save TxOut back with the TxID only when the TxOut on the block height is
		// not empty
		for i, txOutItem := range txOut.TxArray {
			// withdraw , refund etc, one inbound tx might result two outbound
			// txes, THORNode have to correlate outbound tx back to the
			// inbound, and also txitem , thus THORNode could record both
			// outbound tx hash correctly given every tx item will only have
			// one coin in it , THORNode could use that to identify which tx it
			// is

			if txOutItem.InHash.Equals(inTxID) &&
				txOutItem.OutHash.IsEmpty() &&
				tx.Tx.Chain.Equals(txOutItem.Chain) &&
				tx.Tx.ToAddress.Equals(txOutItem.ToAddress) &&
				tx.ObservedPubKey.Equals(txOutItem.VaultPubKey) {

				matchCoin := tx.Tx.Coins.Equals(common.Coins{txOutItem.Coin})
				// when outbound is gas asset
				if !matchCoin && txOutItem.Coin.Asset.Equals(txOutItem.Chain.GetGasAsset()) {
					asset := txOutItem.Chain.GetGasAsset()
					intendToSpend := txOutItem.Coin.Amount.Add(txOutItem.MaxGas.ToCoins().GetCoin(asset).Amount)
					actualSpend := tx.Tx.Coins.GetCoin(asset).Amount.Add(tx.Tx.Gas.ToCoins().GetCoin(asset).Amount)
					if intendToSpend.Equal(actualSpend) {
						matchCoin = true
						maxGasAmt := txOutItem.MaxGas.ToCoins().GetCoin(asset).Amount
						realGasAmt := tx.Tx.Gas.ToCoins().GetCoin(asset).Amount
						ctx.Logger().Info(fmt.Sprintf("intend to spend: %s, actual spend: %s are the same , override match coin, max_gas: %s , actual gas: %s ", intendToSpend, actualSpend, maxGasAmt, realGasAmt))
						if maxGasAmt.GT(realGasAmt) {
							// the outbound spend less than MaxGas
							diffGas := maxGasAmt.Sub(realGasAmt)
							h.mgr.GasMgr().AddGasAsset(common.Gas{
								common.NewCoin(asset, diffGas),
							}, false)
						} else if maxGasAmt.LT(realGasAmt) {
							// signer spend more than the maximum gas prescribed by THORChain , slash it
							ctx.Logger().Info(fmt.Sprintf("max gas: %s, real gas spend: %s ,gap %s , slash node", maxGasAmt.String(), realGasAmt.String(), common.SafeSub(realGasAmt, maxGasAmt).String()))
							matchCoin = false
						}
					}
				}

				if !matchCoin {
					continue
				}
				txOut.TxArray[i].OutHash = tx.Tx.ID
				shouldSlash = false
				if err := h.keeper.SetTxOut(ctx, txOut); err != nil {
					ctx.Logger().Error("fail to save tx out", "error", err)
				}
				break

			}
		}
	}

	if shouldSlash {
		ctx.Logger().Info("slash node account, no matched tx out item", "inbound txid", inTxID, "outbound tx", tx.Tx)
		if err := h.slash(ctx, version, tx); err != nil {
			return nil, ErrInternal(err, "fail to slash account")
		}
	}

	if err := h.keeper.SetLastSignedHeight(ctx, voter.FinalisedHeight); err != nil {
		ctx.Logger().Info("fail to update last signed height", "error", err)
	}

	return &cosmos.Result{}, nil
}

func (h CommonOutboundTxHandler) handleV20(ctx cosmos.Context, version semver.Version, tx ObservedTx, inTxID common.TxID, constAccessor constants.ConstantValues) (*cosmos.Result, error) {
	// note: Outbound tx usually it is related to an inbound tx except migration
	// thus here try to get the ObservedTxInVoter,  and set the tx out hash accordingly
	voter, err := h.keeper.GetObservedTxInVoter(ctx, inTxID)
	if err != nil {
		return nil, ErrInternal(err, "fail to get observed tx voter")
	}
	voter.AddOutTx(tx.Tx)
	h.keeper.SetObservedTxInVoter(ctx, voter)

	// complete events
	if voter.IsDone() {
		for _, item := range voter.OutTxs {
			if err := h.mgr.EventMgr().EmitEvent(ctx, NewEventOutbound(inTxID, item)); err != nil {
				return nil, ErrInternal(err, "fail to emit outbound event")
			}
		}
	}

	if tx.Tx.Chain.Equals(common.THORChain) {
		return &cosmos.Result{}, nil
	}

	shouldSlash := true
	signingTransPeriod := constAccessor.GetInt64Value(constants.SigningTransactionPeriod)
	// every Signing Transaction Period , THORNode will check whether a
	// TxOutItem had been sent by signer or not
	// if a txout item that is older than SigningTransactionPeriod, but has not
	// been sent out by signer , then it will create a new TxOutItem
	// here THORNode will have to mark all the TxOutItem to complete one the tx
	// get processed
	for height := voter.FinalisedHeight; height <= common.BlockHeight(ctx); height += signingTransPeriod {

		// update txOut record with our TxID that sent funds out of the pool
		txOut, err := h.keeper.GetTxOut(ctx, height)
		if err != nil {
			ctx.Logger().Error("unable to get txOut record", "error", err)
			return nil, cosmos.ErrUnknownRequest(err.Error())
		}

		// Save TxOut back with the TxID only when the TxOut on the block height is
		// not empty
		for i, txOutItem := range txOut.TxArray {
			// withdraw , refund etc, one inbound tx might result two outbound
			// txes, THORNode have to correlate outbound tx back to the
			// inbound, and also txitem , thus THORNode could record both
			// outbound tx hash correctly given every tx item will only have
			// one coin in it , THORNode could use that to identify which tx it
			// is

			if txOutItem.InHash.Equals(inTxID) &&
				txOutItem.OutHash.IsEmpty() &&
				tx.Tx.Chain.Equals(txOutItem.Chain) &&
				tx.Tx.ToAddress.Equals(txOutItem.ToAddress) &&
				tx.ObservedPubKey.Equals(txOutItem.VaultPubKey) {

				matchCoin := tx.Tx.Coins.Equals(common.Coins{txOutItem.Coin})

				// when the total coin match,  continue to check this logic for ETH chain
				// the reason the network need this special logic for ETH chain is because
				// on ETH, when bifrost sign and send out a tx , it could ask the chain to do an estimate of gas ,
				// however the real amount gas that will be spend, can't be predict, so there might be some room there ,
				// sometimes it spend a bit more , sometimes it spend a bit less , if bifrost spend less sending out the tx
				// the network need to put the ETH that left in the wallet back to pool , and take RUNE , and send it to reserve
				if matchCoin && txOutItem.Chain.Equals(common.ETHChain) &&
					txOutItem.Coin.Asset.Equals(common.ETHChain.GetGasAsset()) {
					asset := txOutItem.Chain.GetGasAsset()
					maxGas := txOutItem.MaxGas.ToCoins().GetCoin(asset).Amount
					realGas := tx.Tx.Gas.ToCoins().GetCoin(asset).Amount
					if maxGas.GT(realGas) {
						gap := maxGas.Sub(realGas)
						pool, err := h.keeper.GetPool(ctx, asset)
						if err != nil {
							ctx.Logger().Error("fail to get pool", "error", err)
						} else {
							runeValue := pool.AssetValueInRune(gap)
							ctx.Logger().Info(fmt.Sprintf("intend to spend %s as max gas , however only used: %s, send %s in rune to reserve", maxGas, realGas, runeValue))
							pool.BalanceAsset = pool.BalanceAsset.Add(gap)
							pool.BalanceRune = common.SafeSub(pool.BalanceRune, runeValue)
							if err := h.keeper.SendFromModuleToModule(ctx, AsgardName, ReserveName, common.Coins{
								common.NewCoin(common.RuneNative, runeValue),
							}); err != nil {
								ctx.Logger().Error("fail to send rune to reserve", "error", err)
							}
						}
					}
				}

				// when outbound is gas asset
				if !matchCoin && txOutItem.Coin.Asset.Equals(txOutItem.Chain.GetGasAsset()) {
					asset := txOutItem.Chain.GetGasAsset()
					intendToSpend := txOutItem.Coin.Amount.Add(txOutItem.MaxGas.ToCoins().GetCoin(asset).Amount)
					actualSpend := tx.Tx.Coins.GetCoin(asset).Amount.Add(tx.Tx.Gas.ToCoins().GetCoin(asset).Amount)
					if intendToSpend.Equal(actualSpend) {
						matchCoin = true
						maxGasAmt := txOutItem.MaxGas.ToCoins().GetCoin(asset).Amount
						realGasAmt := tx.Tx.Gas.ToCoins().GetCoin(asset).Amount
						ctx.Logger().Info(fmt.Sprintf("intend to spend: %s, actual spend: %s are the same , override match coin, max_gas: %s , actual gas: %s ", intendToSpend, actualSpend, maxGasAmt, realGasAmt))
						if maxGasAmt.GT(realGasAmt) {
							// the outbound spend less than MaxGas
							diffGas := maxGasAmt.Sub(realGasAmt)
							h.mgr.GasMgr().AddGasAsset(common.Gas{
								common.NewCoin(asset, diffGas),
							}, false)
						} else if maxGasAmt.LT(realGasAmt) {
							// signer spend more than the maximum gas prescribed by THORChain , slash it
							ctx.Logger().Info(fmt.Sprintf("max gas: %s, real gas spend: %s ,gap %s , slash node", maxGasAmt.String(), realGasAmt.String(), common.SafeSub(realGasAmt, maxGasAmt).String()))
							matchCoin = false
						}
					}
				}

				if !matchCoin {
					continue
				}
				txOut.TxArray[i].OutHash = tx.Tx.ID
				shouldSlash = false
				if err := h.keeper.SetTxOut(ctx, txOut); err != nil {
					ctx.Logger().Error("fail to save tx out", "error", err)
				}
				break

			}
		}
	}

	if shouldSlash {
		ctx.Logger().Info("slash node account, no matched tx out item", "inbound txid", inTxID, "outbound tx", tx.Tx)
		if err := h.slash(ctx, version, tx); err != nil {
			return nil, ErrInternal(err, "fail to slash account")
		}
	}

	if err := h.keeper.SetLastSignedHeight(ctx, voter.FinalisedHeight); err != nil {
		ctx.Logger().Info("fail to update last signed height", "error", err)
	}

	return &cosmos.Result{}, nil
}
