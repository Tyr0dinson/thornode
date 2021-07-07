package thorchain

import (
	"fmt"

	"github.com/blang/semver"
	"gitlab.com/thorchain/thornode/common"
	"gitlab.com/thorchain/thornode/common/cosmos"
	"gitlab.com/thorchain/thornode/constants"
)

// SolvencyHandler is to process MsgSolvency message from bifrost
// Bifrost constantly monitor the account balance , and report to THORNode
// If it detect that wallet is short of fund , much less than vault, the network should automatically halt trading
type SolvencyHandler struct {
	mgr Manager
}

// NewSolvencyHandler create a new instance of solvency handler
func NewSolvencyHandler(mgr Manager) SolvencyHandler {
	return SolvencyHandler{
		mgr: mgr,
	}
}

// Run is the main entry point to process MsgSolvency
func (h SolvencyHandler) Run(ctx cosmos.Context, m cosmos.Msg) (*cosmos.Result, error) {
	msg, ok := m.(*MsgSolvency)
	if !ok {
		return nil, errInvalidMessage
	}
	if err := h.validate(ctx, *msg); err != nil {
		ctx.Logger().Error("msg solvency failed validation", "error", err)
		return nil, err
	}
	return h.handle(ctx, *msg)
}
func (h SolvencyHandler) validate(ctx cosmos.Context, msg MsgSolvency) error {
	version := h.mgr.GetVersion()
	if version.GTE(semver.MustParse("0.1.0")) {
		return h.validateV1(ctx, msg)
	}
	return errBadVersion
}
func (h SolvencyHandler) validateV1(ctx cosmos.Context, msg MsgSolvency) error {
	return h.validateCurrent(ctx, msg)
}
func (h SolvencyHandler) validateCurrent(ctx cosmos.Context, msg MsgSolvency) error {
	return msg.ValidateBasic()
}
func (h SolvencyHandler) handle(ctx cosmos.Context, msg MsgSolvency) (*cosmos.Result, error) {
	ctx.Logger().Info("handle Solvency request", "id", msg.Id.String(), "signer", msg.Signer.String())
	version := h.mgr.GetVersion()
	if version.GTE(semver.MustParse("0.1.0")) {
		return h.handleV1(ctx, msg)
	}
	ctx.Logger().Error(errInvalidVersion.Error())
	return nil, errBadVersion
}

func (h SolvencyHandler) handleV1(ctx cosmos.Context, msg MsgSolvency) (*cosmos.Result, error) {
	voter, err := h.mgr.Keeper().GetSolvencyVoter(ctx, msg.Id, msg.Chain)
	if err != nil {
		return &cosmos.Result{}, fmt.Errorf("fail to ")
	}
	observeSlashPoints := h.mgr.GetConstants().GetInt64Value(constants.ObserveSlashPoints)
	observeFlex := h.mgr.GetConstants().GetInt64Value(constants.ObservationDelayFlexibility)
	h.mgr.Slasher().IncSlashPoints(ctx, observeSlashPoints, msg.Signer)
	if voter.Empty() {
		voter = NewSolvencyVoter(msg.Id, msg.Chain, msg.PubKey, msg.Coins, msg.Height, msg.Signer)
	} else {
		if !voter.Sign(msg.Signer) {
			ctx.Logger().Info("signer already signed MsgSolvency", "signer", msg.Signer.String(), "id", msg.Id)
			return &cosmos.Result{}, nil
		}
	}
	h.mgr.Keeper().SetSolvencyVoter(ctx, voter)
	active, err := h.mgr.Keeper().ListActiveNodeAccounts(ctx)
	if err != nil {
		return nil, wrapError(ctx, err, "fail to get list of active node accounts")
	}
	if !voter.HasConsensus(active) {
		return &cosmos.Result{}, nil
	}

	// from this point , solvency reach consensus
	if voter.ConsensusBlockHeight > 0 {
		if (voter.ConsensusBlockHeight + observeFlex) >= common.BlockHeight(ctx) {
			h.mgr.Slasher().DecSlashPoints(ctx, observeSlashPoints, msg.Signer)
		}
		// solvency tx already processed
		return &cosmos.Result{}, nil
	}
	voter.ConsensusBlockHeight = common.BlockHeight(ctx)
	h.mgr.Keeper().SetSolvencyVoter(ctx, voter)
	// decrease the slash points
	h.mgr.Slasher().DecSlashPoints(ctx, observeSlashPoints, voter.GetSigners()...)
	vault, err := h.mgr.Keeper().GetVault(ctx, voter.PubKey)
	if err != nil {
		ctx.Logger().Error("fail to get vault", "error", err)
		return &cosmos.Result{}, fmt.Errorf("fail to get vault: %w", err)
	}
	const haltTradingKey = `HaltTrading`
	if !h.insolvencyCheck(ctx, vault, voter.Coins, voter.Chain) {
		// here doesn't override HaltTrading when the vault is solvent
		// in some case even the vault is solvent , the network might need to halt for some reasons
		// Use mimir to enable it again
		return &cosmos.Result{}, nil
	}
	haltTrading, err := h.mgr.Keeper().GetMimir(ctx, haltTradingKey)
	if err != nil {
		ctx.Logger().Error("fail to get mimir", "error", err)
	}
	if haltTrading > 0 && haltTrading < common.BlockHeight(ctx) {
		// Trading already halt
		return &cosmos.Result{}, nil
	}
	h.mgr.Keeper().SetMimir(ctx, haltTradingKey, 1)
	ctx.Logger().Info("vault is insolvent, halt trading until it is resolved")
	return &cosmos.Result{}, nil
}

// insolvencyCheck compare the coins in vault against the coins report by solvency message
// insolvent usually means vault has more coins than wallet
// return true means the vault is insolvent , the network should halt , otherwise false
func (h SolvencyHandler) insolvencyCheck(ctx cosmos.Context, vault Vault, coins common.Coins, chain common.Chain) bool {
	adjustVault, err := h.excludePendingOutboundFromVault(ctx, vault)
	if err != nil {
		return false
	}
	// Use the coin in vault as baseline , wallet can have more coins than vault
	for _, c := range adjustVault.Coins {
		if !c.Asset.Chain.Equals(chain) {
			continue
		}

		// ETH.RUNE will be burned on the way in , so the wallet will not have any, thus exclude it from solvency check
		if c.Asset.IsRune() {
			continue
		}
		walletCoin := coins.GetCoin(c.Asset)
		if walletCoin.IsEmpty() {
			ctx.Logger().Info("asset exist in vault , but not in wallet, insolvent", "asset", c.Asset.String(), "amount", c.Amount.String())
			return true
		}
		if c.Amount.GT(walletCoin.Amount) {
			gap := c.Amount.Sub(walletCoin.Amount)
			if gap.MulUint64(100).GT(walletCoin.Amount) {
				ctx.Logger().Info("vault has more asset than wallet, insolvent", "asset", c.Asset.String(), "vault amount", c.Amount.String(), "wallet amount", walletCoin.Amount.String(), "gap", gap.String())
				return true
			}
		}
	}
	return false
}
func (h SolvencyHandler) excludePendingOutboundFromVault(ctx cosmos.Context, vault Vault) (Vault, error) {
	// go back SigningTransactionPeriod blocks to see whether there are outstanding tx, the vault need to send out
	// if there is , deduct it from their balance
	signingPeriod := h.mgr.GetConstants().GetInt64Value(constants.SigningTransactionPeriod)
	startHeight := common.BlockHeight(ctx) - signingPeriod
	if startHeight < 1 {
		startHeight = 1
	}
	for i := startHeight; i < common.BlockHeight(ctx); i++ {
		blockOut, err := h.mgr.Keeper().GetTxOut(ctx, i)
		if err != nil {
			ctx.Logger().Error("fail to get block tx out", "error", err)
			return vault, fmt.Errorf("fail to get block tx out, err: %w", err)
		}
		vault = h.deductVaultBlockPendingOutbound(vault, blockOut)
	}
	return vault, nil
}
func (h SolvencyHandler) deductVaultBlockPendingOutbound(vault Vault, block *TxOut) Vault {
	for _, txOutItem := range block.TxArray {
		if !txOutItem.VaultPubKey.Equals(vault.PubKey) {
			continue
		}
		// only still outstanding txout will be considered
		if !txOutItem.OutHash.IsEmpty() {
			continue
		}
		// deduct the gas asset from the vault as well
		var gasCoin common.Coin
		if !txOutItem.MaxGas.IsEmpty() {
			gasCoin = txOutItem.MaxGas.ToCoins().GetCoin(txOutItem.Chain.GetGasAsset())
		}
		for i, yggCoin := range vault.Coins {
			if yggCoin.Asset.Equals(txOutItem.Coin.Asset) {
				vault.Coins[i].Amount = common.SafeSub(vault.Coins[i].Amount, txOutItem.Coin.Amount)
			}
			if yggCoin.Asset.Equals(gasCoin.Asset) {
				vault.Coins[i].Amount = common.SafeSub(vault.Coins[i].Amount, gasCoin.Amount)
			}
		}
	}
	return vault
}
