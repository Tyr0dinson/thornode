package mimir

type Id int32

const (
	UnknownId Id = iota
	AffiliateFeeBasisPointsMaxId
	BondPauseId
	UnbondPauseId
	OrderBooksEnableId
	StreamingSwapPauseId
	LoansPauseId
	SynthsMintId
	SynthsBurnId
	ChurnPauseId
	TradingPauseId
	LPPauseId
)

var StringToId = map[string]Id{
	"unknown":                    UnknownId,
	"AffiliateFeeBasisPointsMax": AffiliateFeeBasisPointsMaxId,
	"BondPause":                  BondPauseId,
	"UnbondPause":                UnbondPauseId,
	"OrderBooksEnable":           OrderBooksEnableId,
	"StreamingSwapPause":         StreamingSwapPauseId,
	"LoansPause":                 LoansPauseId,
	"SynthsMint":                 SynthsMintId,
	"SynthsBurn":                 SynthsBurnId,
	"ChurnPause":                 ChurnPauseId,
	"TradingPause":               TradingPauseId,
	"LPPause":                    LPPauseId,
}

var mimirRefToStringMap = map[Id]string{
	UnknownId:                    "unknown",
	AffiliateFeeBasisPointsMaxId: "AffiliateFeeBasisPointsMax",
	BondPauseId:                  "BondPause",
	UnbondPauseId:                "UnbondPause",
	OrderBooksEnableId:           "OrderBooksEnable",
	StreamingSwapPauseId:         "StreamingSwapPause",
	LoansPauseId:                 "LoansPause",
	SynthsMintId:                 "SynthsMint",
	SynthsBurnId:                 "SynthsBurn",
	ChurnPauseId:                 "ChurnPause",
	TradingPauseId:               "TradingPause",
	LPPauseId:                    "LPPause",
}

// fetches a mimir by id number
func GetMimir(id Id, ref string) (Mimir, bool) {
	switch id {
	case AffiliateFeeBasisPointsMaxId:
		return NewAffiliateFeeBasisPointsMax(ref), true
	case BondPauseId:
		return NewBondPause(ref), true
	case UnbondPauseId:
		return NewUnbondPause(ref), true
	case OrderBooksEnableId:
		return NewOrderBooksEnable(ref), true
	case LoansPauseId:
		return NewLoansPause(ref), true
	case SynthsBurnId:
		return NewSynthsBurn(ref), true
	case ChurnPauseId:
		return NewChurnPause(ref), true
	case TradingPauseId:
		return NewTradingPause(ref), true
	case LPPauseId:
		return NewLPPause(ref), true
	default:
		return nil, false
	}
}
