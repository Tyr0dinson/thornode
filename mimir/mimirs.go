package mimir

import (
	"fmt"
	"strings"
	"sync"

	"gitlab.com/thorchain/thornode/constants"
)

var (
	supportedChains map[string]bool
	once            sync.Once
)

func init() {
	once.Do(initializeSupportedChains)
}

func initializeSupportedChains() {
	supportedChains = map[string]bool{
		"avax":   true,
		"bch":    true,
		"bnb":    true,
		"btc":    true,
		"doge":   true,
		"eth":    true,
		"gaia":   true,
		"global": true,
		"ltc":    true,
	}
}

func IsSupportedChain(chain string) bool {
	return supportedChains[strings.ToLower(chain)]
}

func IsThorChain(chain string) bool {
	return strings.ToLower(chain) == "thor"
}

func getRef(refs []string) (reference string) {
	for _, ref := range refs {
		if len(ref) > 0 {
			reference = ref
		}
	}
	return
}

func NewAffiliateFeeBasisPointsMax(refs ...string) Mimir {
	id := AffiliateFeeBasisPointsMaxId
	return &mimir{
		id:           id,
		name:         mimirRefToStringMap[id],
		defaultValue: constants.NewConstantValue().GetInt64Value(constants.MaxAffiliateFeeBasisPoints),
		mimirType:    EconomicMimir,
		reference:    getRef(refs),
		tags:         []string{"economic", "affiliate fee"},
		description:  "Maximum fee to allow affiliates to set",
		legacyMimirKey: func(_ string) string {
			return "MaxAffiliateFeeBasisPoints"
		},
	}
}

func NewPauseBond(refs ...string) Mimir {
	id := PauseBondId
	ref := getRef(refs)

	newMimir := &mimir{
		id:        id,
		name:      mimirRefToStringMap[id],
		mimirType: OperationalMimir,
		tags:      []string{"operational", "bond"},
	}
	switch ref {
	case "bond":
		newMimir.defaultValue = constants.NewConstantValue().GetInt64Value(constants.PauseBond)
		newMimir.description = "Pauses bonding"
		newMimir.legacyMimirKey = func(_ string) string {
			return "PauseBond"
		}
	case "unbond":
		newMimir.defaultValue = constants.NewConstantValue().GetInt64Value(constants.PauseUnbond)
		newMimir.description = "Pauses unbonding"
		newMimir.legacyMimirKey = func(_ string) string {
			return "PauseUnbond"
		}
	default:
		return nil
	}

	newMimir.reference = ref
	return newMimir
}

func NewEnableOrderBooks(refs ...string) Mimir {
	id := EnableOrderBooksId
	return &mimir{
		id:           id,
		name:         mimirRefToStringMap[id],
		defaultValue: constants.NewConstantValue().GetInt64Value(constants.EnableOrderBooks),
		reference:    getRef(refs),
		mimirType:    EconomicMimir,
		tags:         []string{"operational", "swaps"},
		description:  "Enables order books instead of swap queue",
		legacyMimirKey: func(_ string) string {
			return "EnableOrderBooks"
		},
	}
}

func NewPauseStreamingSwap(refs ...string) Mimir {
	id := PauseStreamingSwapId
	return &mimir{
		id:           id,
		name:         mimirRefToStringMap[id],
		defaultValue: constants.NewConstantValue().GetInt64Value(constants.StreamingSwapPause),
		reference:    getRef(refs),
		mimirType:    OperationalMimir,
		tags:         []string{"operational", "swaps"},
		description:  "Pauses streaming swaps from being processed or accepted",
		legacyMimirKey: func(_ string) string {
			return "StreamingSwapPause"
		},
	}
}

func NewPauseLoans(refs ...string) Mimir {
	id := PauseLoansId
	return &mimir{
		id:           id,
		name:         mimirRefToStringMap[id],
		defaultValue: constants.NewConstantValue().GetInt64Value(constants.PauseLoans),
		reference:    getRef(refs),
		mimirType:    OperationalMimir,
		tags:         []string{"operational", "lending"},
		description:  "Pauses opening new loans and repaying loans",
		legacyMimirKey: func(_ string) string {
			return "PauseLoans"
		},
	}
}

func NewPauseChain(refs ...string) Mimir {
	id := PauseChainId
	ref := getRef(refs)
	if !IsSupportedChain(strings.ToLower(ref)) && !IsThorChain(strings.ToLower(ref)) {
		return nil
	}
	return &mimir{
		id:           id,
		name:         mimirRefToStringMap[id],
		defaultValue: constants.NewConstantValue().GetInt64Value(constants.HaltChain),
		reference:    ref,
		mimirType:    OperationalMimir,
		tags:         []string{"operational", "chain"},
		description:  fmt.Sprintf("Halts interaction with the %s chain", ref),
		legacyMimirKey: func(ref string) string {
			return fmt.Sprintf("Halt%sChain", ref)
		},
	}
}

func NewPauseSynths(refs ...string) Mimir {
	id := PauseSynthsId
	ref := getRef(refs)
	newMimir := &mimir{
		id:        id,
		name:      mimirRefToStringMap[id],
		mimirType: OperationalMimir,
		tags:      []string{"operational", "synths"},
	}
	switch ref {
	case "burn":
		newMimir.defaultValue = constants.NewConstantValue().GetInt64Value(constants.BurnSynths)
		newMimir.description = "Pauses the burning of synths"
		newMimir.legacyMimirKey = func(_ string) string {
			return "BurnSynths"
		}
	case "mint":
		newMimir.defaultValue = constants.NewConstantValue().GetInt64Value(constants.MintSynths)
		newMimir.description = "Pauses the minting of synths"
		newMimir.legacyMimirKey = func(_ string) string {
			return "MintSynths"
		}
	default:
		return nil
	}

	newMimir.reference = ref
	return newMimir
}

func NewPauseChurning(refs ...string) Mimir {
	id := PauseChurningId
	return &mimir{
		id:           id,
		name:         mimirRefToStringMap[id],
		defaultValue: constants.NewConstantValue().GetInt64Value(constants.HaltChurning),
		reference:    getRef(refs),
		mimirType:    OperationalMimir,
		tags:         []string{"operational", "churn", "migration"},
		description:  "Pauses churning",
		legacyMimirKey: func(_ string) string {
			return "HaltChurning"
		},
	}
}

func NewPauseSwaps(refs ...string) Mimir {
	id := PauseSwapsId
	ref := getRef(refs)
	if !IsSupportedChain(strings.ToLower(ref)) {
		return nil
	}
	return &mimir{
		id:           id,
		name:         mimirRefToStringMap[id],
		defaultValue: constants.NewConstantValue().GetInt64Value(constants.HaltTrading),
		reference:    ref,
		mimirType:    OperationalMimir,
		tags:         []string{"operational", "chain"},
		description:  fmt.Sprintf("Pauses swaps on %s chain", ref),
		legacyMimirKey: func(ref string) string {
			return fmt.Sprintf("Halt%sTrading", ref)
		},
	}
}

func NewPauseLP(refs ...string) Mimir {
	id := PauseLPId
	ref := getRef(refs)
	if !IsSupportedChain(strings.ToLower(ref)) {
		return nil
	}
	return &mimir{
		id:           id,
		name:         mimirRefToStringMap[id],
		defaultValue: constants.NewConstantValue().GetInt64Value(constants.PauseLP),
		reference:    ref,
		mimirType:    OperationalMimir,
		tags:         []string{"operational", "lp"},
		description:  "Pauses the ability for LPs to add/remove liquidity",
		legacyMimirKey: func(ref string) string {
			return fmt.Sprintf("PauseLP%s", strings.ToUpper(ref))
		},
	}
}

func NewPauseSigning(refs ...string) Mimir {
	id := PauseSigningId
	ref := getRef(refs)
	if !IsSupportedChain(strings.ToLower(ref)) {
		return nil
	}
	return &mimir{
		id:           id,
		name:         mimirRefToStringMap[id],
		defaultValue: constants.NewConstantValue().GetInt64Value(constants.HaltSigning),
		reference:    ref,
		mimirType:    OperationalMimir,
		tags:         []string{"operational", "chain"},
		description:  "Halts signing",
		legacyMimirKey: func(ref string) string {
			return fmt.Sprintf("HaltSigning%s", strings.ToUpper(ref))
		},
	}
}

func NewAnchor(refs ...string) Mimir {
	id := AnchorId
	return &mimir{
		id:           id,
		name:         mimirRefToStringMap[id] + getRef(refs),
		defaultValue: 0,
		reference:    getRef(refs),
		mimirType:    OperationalMimir,
		tags:         []string{"operational", "tor"},
		description:  "Enables tor anchor",
		legacyMimirKey: func(_ string) string {
			return "TORANCHOR" + getRef(refs)
		},
	}
}
