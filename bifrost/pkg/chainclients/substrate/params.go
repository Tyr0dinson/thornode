package substrate

type Params struct {
	SpecName               string
	MortalEraPeriod        uint32 // blocks a mortal era tx is valid for
	DefaultFee             uint64
	BalanceSize            int
	CheckMetadataHash      bool // whether the runtime includes CheckMetadataHash (RFC-0078)
	SystemPallet           byte
	BalancesPallet         byte
	UtilityPallet          byte
	TxPaymentPallet        byte
	SystemRemarkCall       byte
	BalancesAllowDeathCall byte
	BalancesKeepAliveCall  byte
	UtilityBatchAllCall    byte
}
