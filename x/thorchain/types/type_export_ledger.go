package types

import (
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"strings"
)

func NewExportLedger() ExportLedger {
	return ExportLedger{
		BalanceExternal: cosmos.ZeroUint(),
		BalanceNative:   cosmos.ZeroUint(),
		GasReserveL1:    cosmos.ZeroUint(),
		GasReserveRune:  cosmos.ZeroUint(),
	}
}

func (l ExportLedger) String() string {
	builder := strings.Builder{}
	builder.WriteString("balance-external: " + l.BalanceExternal.String())
	builder.WriteString("balance-native: " + l.BalanceNative.String())
	builder.WriteString("gas-reserve-external: " + l.GasReserveL1.String())
	builder.WriteString("gas-reserve-rune: " + l.GasReserveRune.String())
	return builder.String()
}
