package types

import (
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"strings"
)

func NewAssetExport() AssetExport {
	return AssetExport{
		Status:         AssetExport_Unknown,
		AmountExternal: cosmos.ZeroUint(),
		AmountNative:   cosmos.ZeroUint(),
		GasReserveL1:   cosmos.ZeroUint(),
		GasReserveRune: cosmos.ZeroUint(),
	}
}

func (l AssetExport) String() string {
	builder := strings.Builder{}
	builder.WriteString("status: " + l.Status.String())
	builder.WriteString("balance-external: " + l.AmountExternal.String())
	builder.WriteString("balance-native: " + l.AmountNative.String())
	builder.WriteString("gas-reserve-external: " + l.GasReserveL1.String())
	builder.WriteString("gas-reserve-rune: " + l.GasReserveRune.String())
	return builder.String()
}
