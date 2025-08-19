package common

import (
	"fmt"
	"strings"
)

// TXTYPE:STATE1:STATE2:STATE3:FINALMEMO

type TxType uint8

const (
	TxUnknown TxType = iota
	TxAdd
	TxWithdraw
	TxSwap
	TxLimitSwap
	TxModifyLimitSwap
	TxOutbound
	TxDonate
	TxBond
	TxUnbond
	TxLeave
	TxReserve
	TxRefund
	TxMigrate
	TxRagnarok
	TxNoOp
	TxConsolidate
	TxTHORName
	TxLoanOpen
	TxLoanRepayment
	TxTradeAccountDeposit
	TxTradeAccountWithdrawal
	TxSecuredAssetDeposit
	TxSecuredAssetWithdraw
	TxRunePoolDeposit
	TxRunePoolWithdraw
	TxExec
	TxSwitch
	TxTCYClaim
	TxTCYStake
	TxTCYUnstake
	TxMaint
	TxRebond
	TxOperatorRotate
)

var stringToTxTypeMap = map[string]TxType{
	"add":         TxAdd,
	"+":           TxAdd,
	"withdraw":    TxWithdraw,
	"wd":          TxWithdraw,
	"-":           TxWithdraw,
	"swap":        TxSwap,
	"s":           TxSwap,
	"=":           TxSwap,
	"=<":          TxLimitSwap,
	"m=<":         TxModifyLimitSwap,
	"out":         TxOutbound,
	"donate":      TxDonate,
	"d":           TxDonate,
	"bond":        TxBond,
	"unbond":      TxUnbond,
	"rebond":      TxRebond,
	"leave":       TxLeave,
	"reserve":     TxReserve,
	"refund":      TxRefund,
	"migrate":     TxMigrate,
	"ragnarok":    TxRagnarok,
	"noop":        TxNoOp,
	"consolidate": TxConsolidate,
	"name":        TxTHORName,
	"n":           TxTHORName,
	"~":           TxTHORName,
	"$+":          TxLoanOpen,
	"loan+":       TxLoanOpen,
	"$-":          TxLoanRepayment,
	"loan-":       TxLoanRepayment,
	"trade+":      TxTradeAccountDeposit,
	"trade-":      TxTradeAccountWithdrawal,
	"secure+":     TxSecuredAssetDeposit,
	"secure-":     TxSecuredAssetWithdraw,
	"pool+":       TxRunePoolDeposit,
	"pool-":       TxRunePoolWithdraw,
	"x":           TxExec,
	"exec":        TxExec,
	"switch":      TxSwitch,
	"tcy":         TxTCYClaim,
	"tcy+":        TxTCYStake,
	"tcy-":        TxTCYUnstake,
	"maint":       TxMaint,
	"operator":    TxOperatorRotate,
}

var txToStringMap = map[TxType]string{
	TxAdd:                    "add",
	TxWithdraw:               "withdraw",
	TxSwap:                   "swap",
	TxLimitSwap:              "=<",
	TxModifyLimitSwap:        "m=<",
	TxOutbound:               "out",
	TxRefund:                 "refund",
	TxDonate:                 "donate",
	TxBond:                   "bond",
	TxUnbond:                 "unbond",
	TxRebond:                 "rebond",
	TxLeave:                  "leave",
	TxReserve:                "reserve",
	TxMigrate:                "migrate",
	TxRagnarok:               "ragnarok",
	TxNoOp:                   "noop",
	TxConsolidate:            "consolidate",
	TxTHORName:               "thorname",
	TxLoanOpen:               "$+",
	TxLoanRepayment:          "$-",
	TxTradeAccountDeposit:    "trade+",
	TxTradeAccountWithdrawal: "trade-",
	TxSecuredAssetDeposit:    "secure+",
	TxSecuredAssetWithdraw:   "secure-",
	TxExec:                   "x",
	TxSwitch:                 "switch",
	TxTCYClaim:               "tcy",
	TxTCYStake:               "tcy+",
	TxTCYUnstake:             "tcy-",
	TxMaint:                  "maint",
}

// converts a string into a txType
func StringToTxType(s string) (TxType, error) {
	// THORNode can support Abbreviated MEMOs , usually it is only one character
	sl := strings.ToLower(s)
	if t, ok := stringToTxTypeMap[sl]; ok {
		return t, nil
	}
	return TxUnknown, fmt.Errorf("invalid tx type: %s", s)
}

func (tx TxType) IsInbound() bool {
	switch tx {
	case TxAdd,
		TxWithdraw,
		TxTradeAccountDeposit,
		TxTradeAccountWithdrawal,
		TxRunePoolDeposit,
		TxRunePoolWithdraw,
		TxSecuredAssetDeposit,
		TxSecuredAssetWithdraw,
		TxSwap,
		TxLimitSwap,
		TxModifyLimitSwap,
		TxDonate,
		TxBond,
		TxUnbond,
		TxRebond,
		TxLeave,
		TxMaint,
		TxReserve,
		TxNoOp,
		TxTHORName,
		TxLoanOpen,
		TxLoanRepayment,
		TxExec,
		TxSwitch,
		TxTCYClaim,
		TxTCYStake,
		TxTCYUnstake,
		TxOperatorRotate:
		return true
	}
	return false
}

func (tx TxType) IsOutbound() bool {
	switch tx {
	case TxOutbound, TxRefund, TxRagnarok:
		return true
	}
	return false
}

func (tx TxType) IsInternal() bool {
	switch tx {
	case TxMigrate, TxConsolidate:
		return true
	}
	return false
}

// HasOutbound whether the txtype might trigger outbound tx
func (tx TxType) HasOutbound() bool {
	switch tx {
	case TxAdd,
		TxBond,
		TxTradeAccountDeposit,
		TxSecuredAssetDeposit,
		TxRunePoolDeposit,
		TxDonate,
		TxReserve,
		TxMigrate,
		TxMaint,
		TxRagnarok:
		return false
	}
	return true
}

func (tx TxType) IsEmpty() bool {
	return tx == TxUnknown
}

// Check if two txTypes are the same
func (tx TxType) Equals(tx2 TxType) bool {
	return tx == tx2
}

// Converts a txType into a string
func (tx TxType) String() string {
	return txToStringMap[tx]
}
