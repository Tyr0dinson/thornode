package mimir

import (
	"fmt"

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
		defaultValue: int64(constants.MaxBasisPts),
		mimirType:    EconomicMimir,
		reference:    getRef(refs),
		tags:         []string{"economic", "affiliate fee"},
		description:  "Maximum fee to allow affiliates to set",
		legacyMimirKey: func(_ string) string {
			return "MaxAffiliateFeeBasisPoints"
		},
	}
}

func NewBondPause(refs ...string) Mimir {
	id := BondPauseId
	ref := getRef(refs)
	var refConst constants.ConstantName

	if ref == "unbond" {
		refConst = constants.PauseUnbond
	} else {
		ref = "bond"
		refConst = constants.PauseBond
	}
	newMimir := &mimir{
		id:           id,
		name:         mimirRefToStringMap[id],
		reference:    ref,
		mimirType:    OperationalMimir,
		tags:         []string{"operational", "bond"},
		defaultValue: constants.NewConstantValue().GetInt64Value(refConst),
		description:  fmt.Sprintf("Pauses %s", ref),
		legacyMimirKey: func(_ string) string {
			return refConst.String()
		},
	}
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
	var refConst constants.ConstantName
	if ref == "burn" {
		refConst = constants.BurnSynths
	} else {
		ref = "mint"
		refConst = constants.MintSynths
	}
	newMimir := &mimir{
		id:           id,
		name:         mimirRefToStringMap[id],
		reference:    ref,
		mimirType:    OperationalMimir,
		tags:         []string{"operational", "synths"},
		defaultValue: constants.NewConstantValue().GetInt64Value(refConst),
		description:  fmt.Sprintf("Pauses the %s of synths", ref),
		legacyMimirKey: func(_ string) string {
			return refConst.String()
		},
	}
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
	return &mimir{
		id:           id,
		name:         mimirRefToStringMap[id],
		defaultValue: constants.NewConstantValue().GetInt64Value(constants.PauseLP),
		reference:    ref,
		mimirType:    OperationalMimir,
		tags:         []string{"operational", "lp"},
		description:  "Pauses the ability for LPs to add/remove liquidity",
		legacyMimirKey: func(ref string) string {
			return fmt.Sprintf("PauseLP%s", ref)
		},
	}
}

func NewPauseSigning(refs ...string) Mimir {
	id := PauseSigningId
	ref := getRef(refs)
	return &mimir{
		id:           id,
		name:         mimirRefToStringMap[id],
		defaultValue: constants.NewConstantValue().GetInt64Value(constants.HaltSigning),
		reference:    ref,
		mimirType:    OperationalMimir,
		tags:         []string{"operational", "chain"},
		description:  "Halts signing",
		legacyMimirKey: func(ref string) string {
			return fmt.Sprintf("HaltSigning%s", ref)
		},
	}
}

func NewAnchor(refs ...string) Mimir {
	id := AnchorId
	return &mimir{
		id:           id,
		name:         mimirRefToStringMap[id],
		defaultValue: constants.NewConstantValue().GetInt64Value(constants.TorAnchor),
		reference:    getRef(refs),
		mimirType:    OperationalMimir,
		tags:         []string{"operational", "tor"},
		description:  "Allow listed asset to be included in the TOR Price Anchor.",
		legacyMimirKey: func(ref string) string {
			return fmt.Sprintf("TorAnchor%s" + ref)
		},
	}
}

func NewConfBasisPointValue(refs ...string) Mimir {
	id := ConfMultiplierBasisPointsId
	return &mimir{
		id:           id,
		name:         mimirRefToStringMap[id],
		defaultValue: int64(constants.MaxBasisPts),
		reference:    getRef(refs),
		mimirType:    EconomicMimir,
		tags:         []string{"economic", "chain-client"},
		description:  "adjusts confirmation multiplier for chain client",
		legacyMimirKey: func(_ string) string {
			return ""
		},
	}
}

func NewMaxConfValue(refs ...string) Mimir {
	id := MaxConfirmationsId
	return &mimir{
		id:           id,
		name:         mimirRefToStringMap[id],
		defaultValue: constants.NewConstantValue().GetInt64Value(constants.HaltChurning),
		reference:    getRef(refs),
		mimirType:    EconomicMimir,
		tags:         []string{"economic", "chain-client"},
		description:  "max confirmations for chain client",
		legacyMimirKey: func(_ string) string {
			return ""
		},
	}
}

func NewSwapperCloutLimit(refs ...string) Mimir {
	id := CloutSwapperLimitId
	return &mimir{
		id:           id,
		name:         mimirRefToStringMap[id],
		defaultValue: 0,
		mimirType:    EconomicMimir,
		reference:    getRef(refs),
		tags:         []string{"economic", "clout"},
		description:  "Maximum clout applicable to an outbound txn",
		legacyMimirKey: func(_ string) string {
			return "CloutLimit"
		},
	}
}

func NewSwapperCloutReset(refs ...string) Mimir {
	id := CloutSwapperResetId
	return &mimir{
		id:           id,
		name:         mimirRefToStringMap[id],
		defaultValue: 720,
		mimirType:    EconomicMimir,
		reference:    getRef(refs),
		tags:         []string{"economic", "clout"},
		description:  "Amount of blocks before pending clout spent is reset",
		legacyMimirKey: func(_ string) string {
			return "CloutReset"
		},
	}
}
