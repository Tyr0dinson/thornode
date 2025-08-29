package types

import (
	"errors"
	"strings"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

func NewExportAsset(symbol string) ExportAsset {
	asset, err := common.NewAsset(symbol)
	if err != nil {
		asset = common.EmptyAsset
	}
	return ExportAsset{
		Asset:          asset,
		AmountBound:    cosmos.ZeroUint(),
		AmountExported: cosmos.ZeroUint(),
	}
}

func (m ExportAsset) Valid() error {
	if m.Asset.IsEmpty() {
		return errors.New("asset is empty")
	}

	return nil
}

func (m ExportAsset) String() string {
	builder := strings.Builder{}
	builder.WriteString("asset" + m.Asset.String())
	builder.WriteString("amount-bound: " + m.AmountBound.String())
	builder.WriteString("amount-exported: " + m.AmountExported.String())
	return builder.String()
}
