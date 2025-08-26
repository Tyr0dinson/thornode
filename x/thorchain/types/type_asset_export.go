package types

import (
	"errors"
	"strconv"
	"strings"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

func NewAssetExport() AssetExport {
	return AssetExport{
		Asset:          common.EmptyAsset,
		AmountExternal: cosmos.ZeroUint(),
		AmountNative:   cosmos.ZeroUint(),
	}
}

func (m AssetExport) Valid() error {
	if m.Asset.IsEmpty() {
		return errors.New("asset is empty")
	}

	if m.Address == "" {
		return errors.New("address is empty")
	}

	return nil
}

func (m AssetExport) String() string {
	builder := strings.Builder{}
	builder.WriteString("asset: " + m.Asset.String())
	builder.WriteString(" address: " + m.Address)
	builder.WriteString(" decimals: " + strconv.FormatUint(m.Decimals, 10))
	builder.WriteString(" amount-external: " + m.AmountExternal.String())
	builder.WriteString(" amount-native: " + m.AmountNative.String())
	return builder.String()
}
