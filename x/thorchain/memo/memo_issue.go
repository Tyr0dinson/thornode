package thorchain

import "strings"

type ExportAssetIssueMemo struct {
	MemoBase
}

func (m ExportAssetIssueMemo) String() string {
	args := []string{
		m.TxType.String(),
		m.Asset.String(),
	}
	return strings.Join(args, ":")
}
