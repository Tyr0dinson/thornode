package mimir

type Id int32

const (
	UnknownId Id = iota
	AffiliateFeeBasisPointsMaxId
	BondPauseId
	EnableOrderBooksId
	PauseStreamingSwapId
	PauseLoansId
	PauseChainId
	PauseSynthsId
	PauseChurningId
	PauseSwapsId
	PauseLPId
	PauseSigningId
	AnchorId
)

var StringToId = map[string]Id{
	"unknown":                    UnknownId,
	"AffiliateFeeBasisPointsMax": AffiliateFeeBasisPointsMaxId,
	"PauseBonding":               BondPauseId,
	"EnableOrderBooks":           EnableOrderBooksId,
	"PauseStreamingSwap":         PauseStreamingSwapId,
	"PauseLoans":                 PauseLoansId,
	"PauseChain":                 PauseChainId,
	"PauseSynths":                PauseSynthsId,
	"PauseChurning":              PauseChurningId,
	"PauseSwaps":                 PauseSwapsId,
	"PauseLP":                    PauseLPId,
	"PauseSigning":               PauseSigningId,
	"Anchor":                     AnchorId,
}

var mimirRefToStringMap = map[Id]string{
	UnknownId:                    "unknown",
	AffiliateFeeBasisPointsMaxId: "AffiliateFeeBasisPointsMax",
	BondPauseId:                  "PauseBonding",
	EnableOrderBooksId:           "EnableOrderBooks",
	PauseStreamingSwapId:         "PauseStreamingSwap",
	PauseLoansId:                 "PauseLoans",
	PauseChainId:                 "PauseChain",
	PauseSynthsId:                "PauseSynths",
	PauseChurningId:              "PauseChurning",
	PauseSwapsId:                 "PauseSwaps",
	PauseLPId:                    "PauseLP",
	PauseSigningId:               "PauseSigning",
	AnchorId:                     "Anchor",
}

// fetches a mimir by id number
func GetMimir(id Id, ref string) (Mimir, bool) {
	switch id {
	case AffiliateFeeBasisPointsMaxId:
		return NewAffiliateFeeBasisPointsMax(ref), true
	case BondPauseId:
		return NewBondPause(ref), true
	case EnableOrderBooksId:
		return NewEnableOrderBooks(ref), true
	case PauseStreamingSwapId:
		return NewPauseStreamingSwap(ref), true
	case PauseLoansId:
		return NewPauseLoans(ref), true
	case PauseChainId:
		return NewPauseChain(ref), true
	case PauseSynthsId:
		return NewPauseSynths(ref), true
	case PauseChurningId:
		return NewPauseChurning(ref), true
	case PauseSwapsId:
		return NewPauseSwaps(ref), true
	case PauseLPId:
		return NewPauseLP(ref), true
	case PauseSigningId:
		return NewPauseSigning(ref), true
	case AnchorId:
		return NewAnchor(ref), true
	default:
		return nil, false
	}
}
