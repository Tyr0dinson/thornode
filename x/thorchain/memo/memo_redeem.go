package thorchain

import (
	"strings"

	"gitlab.com/thorchain/thornode/v3/common"
)

type ExportAssetRedeemMemo struct {
	MemoBase
	Address common.Address
}

func (m ExportAssetRedeemMemo) String() string {
	args := []string{
		m.TxType.String(),
		m.Asset.String(),
		m.Address.String(),
	}
	return strings.Join(args, ":")
}
