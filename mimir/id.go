package mimir

type Id int32

const (
	UnknownId Id = iota
	AffiliateFeeBasisPointsMaxId
	PauseBondId
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
	"PauseBonding":               PauseBondId, //ref: bond, unbond
	"EnableOrderBooks":           EnableOrderBooksId,
	"PauseStreamingSwap":         PauseStreamingSwapId,
	"PauseLoans":                 PauseLoansId,
	"PauseChain":                 PauseChainId,  //ref: chains
	"PauseSynths":                PauseSynthsId, // ref: burn, mint
	"PauseChurning":              PauseChurningId,
	"PauseSwaps":                 PauseSwapsId,   //ref: chains
	"PauseLP":                    PauseLPId,      //ref: chains
	"PauseSigning":               PauseSigningId, //ref: chains
	"Anchor":                     AnchorId,       // ref: asset pair and address attached
}

var mimirRefToStringMap = map[Id]string{
	UnknownId:                    "unknown",
	AffiliateFeeBasisPointsMaxId: "AffiliateFeeBasisPointsMax",
	PauseBondId:                  "PauseBonding",
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
	case PauseBondId:
		return NewPauseBond(ref), true
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
