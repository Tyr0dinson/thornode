package thorchain

import "strings"

type ExportAssetRedeemMemo struct {
	MemoBase
}

func (m ExportAssetRedeemMemo) String() string {
	args := []string{
		m.TxType.String(),
		m.Asset.String(),
	}
	return strings.Join(args, ":")
}
