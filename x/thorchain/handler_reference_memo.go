package thorchain

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
)

// ReferenceMemoHandler a handler to process reference memo messages
type ReferenceMemoHandler struct {
	mgr Manager
}

// NewReferenceMemoHandler create a new instance of network fee handler
func NewReferenceMemoHandler(mgr Manager) ReferenceMemoHandler {
	return ReferenceMemoHandler{mgr: mgr}
}

// Run is the main entry point for network fee logic
func (h ReferenceMemoHandler) Run(ctx cosmos.Context, m cosmos.Msg) (*cosmos.Result, error) {
	msg, ok := m.(*MsgReferenceMemo)
	if !ok {
		return nil, errInvalidMessage
	}
	ctx.Logger().Info("MsgReferenceMemo", "asset", msg.Asset, "memo", msg.Memo, "signer", msg.Signer)
	if err := h.validate(ctx, *msg); err != nil {
		ctx.Logger().Error("MsgReferenceMemo failed validation", "error", err)
		return nil, err
	}
	result, err := h.handle(ctx, *msg)
	if err != nil {
		ctx.Logger().Error("fail to process MsgReferenceMemo", "error", err)
	}
	return result, err
}

func (h ReferenceMemoHandler) validate(ctx cosmos.Context, msg MsgReferenceMemo) error {
	if err := msg.ValidateBasic(); err != nil {
		return err
	}

	_, err := ParseMemoWithTHORNames(ctx, h.mgr.Keeper(), msg.Memo)
	if err != nil {
		return err
	}

	// Check if memoless transactions are halted
	haltMemoless, err := h.mgr.Keeper().GetMimir(ctx, "HaltMemoless")
	if err == nil && haltMemoless > 0 {
		return errors.New("memoless transactions are currently halted")
	}

	ttl := h.mgr.Keeper().GetConfigInt64(ctx, constants.MemolessTxnTTL)
	if ttl <= 0 {
		return errors.New("memoless transactions is currently disabled")
	}

	cost := h.mgr.Keeper().GetConfigInt64(ctx, constants.MemolessTxnCost)
	if cost > 0 {
		runeAmount := cosmos.NewInt(cost)
		cosmosRuneCoin := cosmos.NewCoin(common.RuneNative.Native(), runeAmount)
		if !h.mgr.Keeper().HasCoins(ctx, msg.Signer, cosmos.NewCoins(cosmosRuneCoin)) {
			return fmt.Errorf("insufficient RUNE balance to pay memoless transaction cost of %d", cost)
		}
	}

	return nil
}

// calculateAssetBasedInterval calculates the interval based on pool decimals
// For assets with different decimal precision, we need different intervals:
// - 8 decimals (BTC, etc.): interval = 1 (common.One / 10^8 = 1)
// - 6 decimals (XRP, GAIA): interval = 100 (common.One / 10^6 = 100)
func (h ReferenceMemoHandler) calculateAssetBasedInterval(ctx cosmos.Context, asset common.Asset) (int64, error) {
	if asset.IsEmpty() {
		// Fallback to interval of 1 for empty assets
		return 0, fmt.Errorf("fail to get asset in calculating asset interval")
	}

	// Get pool-specific decimals for the asset
	pool, err := h.mgr.Keeper().GetPool(ctx, asset)
	if err != nil {
		// Fallback to gas asset decimals if pool not found
		return 0, err
	}

	decimals := pool.Decimals
	if decimals >= 8 {
		return 1, nil // No adjustment needed for 8+ decimal assets
	}

	// For assets with fewer decimals, we need larger intervals
	// 6 decimals: 10^8 / 10^6 = 100
	// 5 decimals: 10^8 / 10^5 = 1000, etc.
	interval := int64(1)
	for i := decimals; i < 8; i++ {
		interval *= 10
	}

	return interval, nil
}

// handle process MsgReferenceMemo
func (h ReferenceMemoHandler) handle(ctx cosmos.Context, msg MsgReferenceMemo) (*cosmos.Result, error) {
	// Use source asset (where the transaction will occur) for interval calculation
	// This ensures reference assignment and extraction use the same asset's decimals
	sourceAsset := msg.Asset

	ttl := h.mgr.Keeper().GetConfigInt64(ctx, constants.MemolessTxnTTL)
	cost := h.mgr.Keeper().GetConfigInt64(ctx, constants.MemolessTxnCost)

	if cost > 0 {
		runeCoin := common.NewCoin(common.RuneNative, cosmos.NewUint(uint64(cost)))
		if err := h.mgr.Keeper().SendFromAccountToModule(ctx, msg.Signer, ReserveName, common.NewCoins(runeCoin)); err != nil {
			return nil, fmt.Errorf("fail to transfer memoless transaction cost to reserve: %w", err)
		}
	}

	// Use original message asset for reference tracking (where the transaction occurred)
	posStr := h.mgr.Keeper().GetLastReferenceNumber(ctx, msg.Asset)
	next, err := h.nextReference(ctx, msg.Asset, sourceAsset, posStr, ttl)
	if next == "" || err != nil {
		return nil, fmt.Errorf("unable to find an available reference: %w", err)
	}

	hash := sha256.New()
	_, err = hash.Write(ctx.TxBytes())
	if err != nil {
		return nil, fmt.Errorf("fail to get txid: %w", err)
	}
	txid := hex.EncodeToString(hash.Sum(nil))
	txID, err := common.NewTxID(txid)
	if err != nil {
		return nil, fmt.Errorf("fail to get txid: %w", err)
	}

	refMemo := NewReferenceMemo(msg.Asset, msg.Memo, next, ctx.BlockHeight())
	refMemo.RegisteredBy = msg.Signer
	refMemo.RegistrationHash = txID

	h.mgr.Keeper().SetReferenceMemo(ctx, refMemo)
	h.mgr.Keeper().SetLastReferenceNumber(ctx, msg.Asset, next)
	ctx.Logger().Info("successfully registered memo", "asset", refMemo.Asset, "reference", refMemo.Reference, "memo", refMemo.Memo, "height", refMemo.Height, "registration_hash", refMemo.RegistrationHash)
	return &cosmos.Result{}, nil
}

func leadingZeros(length int, str string) string {
	switch {
	case len(str) < length:
		var b strings.Builder
		for i := 1; i <= length-len(str); i += 1 {
			b.WriteString("0")
		}
		b.WriteString(str)
		return b.String()
	case len(str) > length:
		return str[:length]
	}
	return str
}

// nextReferenceWithInterval finds the next available reference with proper interval support
func (h ReferenceMemoHandler) nextReferenceWithInterval(ctx cosmos.Context, asset common.Asset, posStr string, start, end, interval, ttl int64) string {
	lastPos, _ := strconv.ParseInt(posStr, 10, 64)

	// Ensure lastPos is aligned to the interval
	lastPos = (lastPos / interval) * interval

	txnRefLength := len(fmt.Sprintf("%d", end))

	pos := lastPos + interval // increment by interval, not just 1
	if pos < start || pos > end {
		pos = start
		posStr = leadingZeros(txnRefLength, fmt.Sprintf("%d", end))
	}

	for {
		if pos > end {
			pos = start
		}
		ref := leadingZeros(txnRefLength, fmt.Sprintf("%d", pos))
		refMemo, err := h.mgr.Keeper().GetReferenceMemo(ctx, asset, ref)
		if err != nil {
			ctx.Logger().Error("fail to get ref memo", "error", err)
			pos += interval
			continue
		}

		// see if this memo is available, if its not being used, return it
		if refMemo.Height == 0 || refMemo.IsExpired(ctx.BlockHeight(), ttl) {
			return ref
		}
		// we've done a complete circle ending where we started. There are no
		// reference ids available right now
		if posStr == ref {
			return ""
		}
		pos += interval // increment by interval to ensure proper spacing
	}
}

// nextReference finds the next available reference by calculating interval and range from target asset
func (h ReferenceMemoHandler) nextReference(ctx cosmos.Context, asset, targetAsset common.Asset, posStr string, ttl int64) (string, error) {
	// Calculate dynamic start/end based on target asset decimals
	interval, err := h.calculateAssetBasedInterval(ctx, targetAsset)
	if interval == 0 || err != nil {
		return "", err
	}
	baseStart := h.mgr.Keeper().GetConfigInt64(ctx, constants.MemolessTxnStartRef)
	baseEnd := h.mgr.Keeper().GetConfigInt64(ctx, constants.MemolessTxnEndRef)

	// Adjust start and end to be multiples of the interval
	start := (baseStart / interval) * interval
	if start < baseStart {
		start += interval
	}
	end := (baseEnd / interval) * interval

	return h.nextReferenceWithInterval(ctx, asset, posStr, start, end, interval, ttl), nil
}
