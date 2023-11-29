package mimir

import (
	"gitlab.com/thorchain/thornode/constants"
)

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
		defaultValue: 0,
		reference:    getRef(refs),
		mimirType:    OperationalMimir,
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
		defaultValue: 0,
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
		defaultValue: 0,
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
	return &mimir{
		id:           id,
		name:         mimirRefToStringMap[id] + getRef(refs),
		defaultValue: 0,
		reference:    getRef(refs),
		mimirType:    OperationalMimir,
		tags:         []string{"operational", "chain"},
		description:  "Halts the referenced chain",
		legacyMimirKey: func(_ string) string {
			return "Halt" + getRef(refs) + "Chain"
		},
	}
}

func NewPauseSynths(refs ...string) Mimir {
	id := PauseSynthsId
	return &mimir{
		id:           id,
		name:         mimirRefToStringMap[id],
		defaultValue: 0,
		reference:    getRef(refs),
		mimirType:    OperationalMimir,
		tags:         []string{"operational", "synths"},
		description:  "Enable/Disable minting synths",
		legacyMimirKey: func(_ string) string {
			return "MintSynths"
		},
	}
}

func NewPauseChurning(refs ...string) Mimir {
	id := PauseChurningId
	return &mimir{
		id:           id,
		name:         mimirRefToStringMap[id],
		defaultValue: 0,
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
	return &mimir{
		id:           id,
		name:         mimirRefToStringMap[id] + getRef(refs),
		defaultValue: 0,
		reference:    getRef(refs),
		mimirType:    OperationalMimir,
		tags:         []string{"operational", "swaps"},
		description:  "Pauses all trading",
		legacyMimirKey: func(_ string) string {
			return "HaltTrading"
		},
	}
}

func NewPauseLP(refs ...string) Mimir {
	id := PauseLPId
	return &mimir{
		id:           id,
		name:         mimirRefToStringMap[id] + getRef(refs),
		defaultValue: 0,
		reference:    getRef(refs),
		mimirType:    OperationalMimir,
		tags:         []string{"operational", "lp"},
		description:  "Pauses the ability for LPs to add/remove liquidity",
		legacyMimirKey: func(_ string) string {
			return "PauseLP"
		},
	}
}

func NewPauseSigning(refs ...string) Mimir {
	id := PauseSigningId
	return &mimir{
		id:           id,
		name:         mimirRefToStringMap[id] + getRef(refs),
		defaultValue: 0,
		reference:    getRef(refs),
		mimirType:    OperationalMimir,
		tags:         []string{"operational", "chain"},
		description:  "Halts signing",
		legacyMimirKey: func(_ string) string {
			return "HALTSIGNING"
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
