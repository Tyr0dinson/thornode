package types

import (
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"strings"
)

func NewExportAsset() ExportAsset {
	return ExportAsset{
		AmountBound: cosmos.ZeroUint(),
		Chains:      []string{},
	}
}

func (l ExportAsset) String() string {
	builder := strings.Builder{}
	builder.WriteString("asset" + l.Asset.String())
	builder.WriteString("amount-bound: " + l.AmountBound.String())
	builder.WriteString("chains: " + strings.Join(l.Chains, ","))
	return builder.String()
}
