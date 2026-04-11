package substrate

type Params struct {
	SpecName               string
	MortalEraPeriod        uint32 // blocks a mortal era tx is valid for
	DefaultFee             uint64
	BalanceSize            int
	SystemPallet           byte
	BalancesPallet         byte
	UtilityPallet          byte
	TxPaymentPallet        byte
	SystemRemarkCall       byte
	BalancesAllowDeathCall byte
	BalancesKeepAliveCall  byte
	UtilityBatchAllCall    byte
}
