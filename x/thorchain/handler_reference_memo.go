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
	ctx.Logger().Info("MsgReferenceMemo", "chain", msg.Chain, "memo", msg.Memo, "signer", msg.Signer)
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

// handle process MsgReferenceMemo
func (h ReferenceMemoHandler) handle(ctx cosmos.Context, msg MsgReferenceMemo) (*cosmos.Result, error) {
	start := h.mgr.Keeper().GetConfigInt64(ctx, constants.MemolessTxnStartRef)
	end := h.mgr.Keeper().GetConfigInt64(ctx, constants.MemolessTxnEndRef)
	ttl := h.mgr.Keeper().GetConfigInt64(ctx, constants.MemolessTxnTTL)
	cost := h.mgr.Keeper().GetConfigInt64(ctx, constants.MemolessTxnCost)

	if cost > 0 {
		runeCoin := common.NewCoin(common.RuneNative, cosmos.NewUint(uint64(cost)))
		if err := h.mgr.Keeper().SendFromAccountToModule(ctx, msg.Signer, ReserveName, common.NewCoins(runeCoin)); err != nil {
			return nil, fmt.Errorf("fail to transfer memoless transaction cost to reserve: %w", err)
		}
	}

	posStr := h.mgr.Keeper().GetLastReferenceNumber(ctx, msg.Chain)
	next := h.nextRefence(ctx, msg.Chain, posStr, start, end, ttl)
	if next == "" {
		return nil, errors.New("unable to find an available reference")
	}

	hash := sha256.New()
	_, err := hash.Write(ctx.TxBytes())
	if err != nil {
		return nil, fmt.Errorf("fail to get txid: %w", err)
	}
	txid := hex.EncodeToString(hash.Sum(nil))
	txID, err := common.NewTxID(txid)
	if err != nil {
		return nil, fmt.Errorf("fail to get txid: %w", err)
	}

	refMemo := NewReferenceMemo(msg.Chain, msg.Memo, next, ctx.BlockHeight())
	refMemo.RegisteredBy = msg.Signer
	refMemo.RegistrationHash = txID
	refMemo.ReferenceHash = msg.TxID

	h.mgr.Keeper().SetReferenceMemo(ctx, refMemo)
	h.mgr.Keeper().SetLastReferenceNumber(ctx, msg.Chain, next)
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

func (h ReferenceMemoHandler) nextRefence(ctx cosmos.Context, chain common.Chain, posStr string, start, end, ttl int64) string {
	lastPos, _ := strconv.ParseInt(posStr, 10, 64)
	pos := lastPos + 1 // increment as pos was the last used reference
	if pos < start || pos > end {
		pos = start
		posStr = leadingZeros(5, fmt.Sprintf("%d", end))
	}

	for {
		if pos > end {
			pos = start
		}
		ref := leadingZeros(constants.MemolessTxnRefLength, fmt.Sprintf("%d", pos))
		refMemo, err := h.mgr.Keeper().GetReferenceMemo(ctx, chain, ref)
		if err != nil {
			ctx.Logger().Error("fail to get ref memo", "error", err)
			continue
		}

		// see if this memo is available, if its not being used, return it
		if refMemo.Height == 0 || refMemo.IsExpired(ctx.BlockHeight(), ttl) {
			return ref
		}
		// we've done a complete circle ending where we started. There are non
		// reference ids available right now
		if posStr == ref {
			return ""
		}
		pos += 1
	}
}
