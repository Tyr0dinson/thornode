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
	default:
		return nil, false
	}
}
