package thorchain

import (
	"strings"

	"gitlab.com/thorchain/thornode/v3/common"
)

type ExportAssetIssueMemo struct {
	MemoBase
	Address common.Address
}

func (m ExportAssetIssueMemo) String() string {
	args := []string{
		m.TxType.String(),
		m.Asset.String(),
		m.Address.String(),
	}
	return strings.Join(args, ":")
}

func (p *parser) ParseExportAssetIssueMemo() (ExportAssetIssueMemo, error) {
	asset := p.getAsset(1, true, common.EmptyAsset)
	address := p.getAddress(2, true, common.NoAddress)
	return ExportAssetIssueMemo{
		MemoBase: MemoBase{
			TxType: TxExportAssetIssue,
			Asset:  asset,
		},
		Address: address,
	}, p.Error()
}
