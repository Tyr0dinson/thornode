package mimir

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
		defaultValue: 10_000,
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
	return &mimir{
		id:           id,
		name:         mimirRefToStringMap[id],
		defaultValue: 0,
		reference:    getRef(refs),
		mimirType:    OperationalMimir,
		tags:         []string{"operational", "bond"},
		description:  "Pauses bonding (unbonding is still allowed)",
		legacyMimirKey: func(_ string) string {
			return "PauseBond"
		},
	}
}

func NewUnbondPause(refs ...string) Mimir {
	id := UnbondPauseId
	return &mimir{
		id:           id,
		name:         mimirRefToStringMap[id],
		defaultValue: 0,
		reference:    getRef(refs),
		mimirType:    OperationalMimir,
		tags:         []string{"operational", "unbond"},
		description:  "Pauses unbonding",
		legacyMimirKey: func(_ string) string {
			return "PauseUnbond"
		},
	}
}

func NewOrderBooksEnable(refs ...string) Mimir {
	id := OrderBooksEnableId
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

func NewStreamingSwapPause(refs ...string) Mimir {
	id := StreamingSwapPauseId
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

func NewLoansPause(refs ...string) Mimir {
	id := LoansPauseId
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

func NewSynthsMint(refs ...string) Mimir {
	id := SynthsMintId
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
