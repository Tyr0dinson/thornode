package mimir

type Id int32

const (
	UnknownId Id = iota
	AffiliateFeeBasisPointsMaxId
	BondPauseId
	UnbondPauseId
)

var StringToId = map[string]Id{
	"unknown":                    UnknownId,
	"AffiliateFeeBasisPointsMax": AffiliateFeeBasisPointsMaxId,
	"BondPause":                  BondPauseId,
	"UnbondPause":                UnbondPauseId,
}

var mimirRefToStringMap = map[Id]string{
	UnknownId:                    "unknown",
	AffiliateFeeBasisPointsMaxId: "AffiliateFeeBasisPointsMax",
	BondPauseId:                  "BondPause",
	UnbondPauseId:                "UnbondPause",
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
	default:
		return nil, false
	}
}
