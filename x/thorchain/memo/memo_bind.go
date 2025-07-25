package thorchain

import (
	"gitlab.com/thorchain/thornode/v3/common"
	"strings"
)

type ExportAssetBindMemo struct {
	MemoBase
}

func (m ExportAssetBindMemo) String() string {
	args := []string{
		m.TxType.String(),
		m.Asset.String(),
	}
	return strings.Join(args, ":")
}

func (p *parser) ParseExportAssetBindMemo() (ExportAssetBindMemo, error) {
	asset := p.getAsset(1, true, common.EmptyAsset)
	return ExportAssetBindMemo{
		MemoBase{
			TxType: TxExportAssetBind,
			Asset:  asset,
		},
	}, p.Error()
}
