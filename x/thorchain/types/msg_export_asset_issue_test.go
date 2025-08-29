package types

import (
	"fmt"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	. "gopkg.in/check.v1"
)

type MsgExportAssetIssueSuite struct{}

var _ = Suite(&MsgExportAssetIssueSuite{})

func (s *MsgExportAssetIssueSuite) TestValidate(c *C) {
	testCases := []struct {
		Comment string
		TxId    common.TxID
		Amount  cosmos.Uint
		Source  common.Asset
		Target  common.Asset
		From    common.Address
		To      common.Address
		Address common.Address
		Signer  cosmos.AccAddress
		Error   string
	}{
		{
			Comment: "valid message",
			TxId:    GetRandomTxHash(),
			Amount:  cosmos.NewUint(common.One),
			Source:  common.RUJI,
			Target:  s.mustNewAsset("ETH.RUJI"),
			From:    GetRandomRUNEAddress(),
			To:      GetRandomRUNEAddress(),
			Address: GetRandomETHAddress(),
			Signer:  GetRandomBech32Addr(),
		},
		{
			Comment: "empty tx hash",
			TxId:    "",
			Amount:  cosmos.NewUint(common.One),
			Source:  common.RUJI,
			Target:  s.mustNewAsset("ETH.RUJI"),
			From:    GetRandomRUNEAddress(),
			To:      GetRandomRUNEAddress(),
			Address: GetRandomETHAddress(),
			Signer:  GetRandomBech32Addr(),
			Error:   "Tx ID cannot be empty",
		},
		{
			Comment: "zero amount",
			TxId:    GetRandomTxHash(),
			Amount:  cosmos.ZeroUint(),
			Source:  common.RUJI,
			Target:  s.mustNewAsset("ETH.RUJI"),
			From:    GetRandomRUNEAddress(),
			To:      GetRandomRUNEAddress(),
			Address: GetRandomETHAddress(),
			Signer:  GetRandomBech32Addr(),
			Error:   "amount cannot be zero",
		},
		{
			Comment: "empty amount",
			TxId:    GetRandomTxHash(),
			Amount:  cosmos.Uint{},
			Source:  common.RUJI,
			Target:  s.mustNewAsset("ETH.RUJI"),
			From:    GetRandomRUNEAddress(),
			To:      GetRandomRUNEAddress(),
			Address: GetRandomETHAddress(),
			Signer:  GetRandomBech32Addr(),
			Error:   "amount cannot be nil",
		},
		{
			Comment: "empty source asset",
			TxId:    GetRandomTxHash(),
			Amount:  cosmos.NewUint(common.One),
			Source:  common.Asset{},
			Target:  s.mustNewAsset("ETH.RUJI"),
			From:    GetRandomRUNEAddress(),
			To:      GetRandomRUNEAddress(),
			Address: GetRandomETHAddress(),
			Signer:  GetRandomBech32Addr(),
			Error:   "chain cannot be empty",
		},
		{
			Comment: "source is no export asset",
			TxId:    GetRandomTxHash(),
			Amount:  cosmos.NewUint(common.One),
			Source:  common.RuneNative,
			Target:  s.mustNewAsset("ETH.RUJI"),
			From:    GetRandomRUNEAddress(),
			To:      GetRandomRUNEAddress(),
			Address: GetRandomETHAddress(),
			Signer:  GetRandomBech32Addr(),
			Error:   "must provide an export asset",
		},
		{
			Comment: "source is no native asset",
			TxId:    GetRandomTxHash(),
			Amount:  cosmos.NewUint(common.One),
			Source:  common.BTCAsset,
			Target:  s.mustNewAsset("ETH.RUJI"),
			From:    GetRandomRUNEAddress(),
			To:      GetRandomRUNEAddress(),
			Address: GetRandomETHAddress(),
			Signer:  GetRandomBech32Addr(),
			Error:   "must provide a native asset",
		},
		{
			Comment: "empty target asset",
			TxId:    GetRandomTxHash(),
			Amount:  cosmos.NewUint(common.One),
			Source:  common.RUJI,
			Target:  common.Asset{},
			From:    GetRandomRUNEAddress(),
			To:      GetRandomRUNEAddress(),
			Address: GetRandomETHAddress(),
			Signer:  GetRandomBech32Addr(),
			Error:   "issued asset cannot be empty",
		},
		{
			Comment: "target asset is native asset",
			TxId:    GetRandomTxHash(),
			Amount:  cosmos.NewUint(common.One),
			Source:  common.RUJI,
			Target:  common.RuneNative,
			From:    GetRandomRUNEAddress(),
			To:      GetRandomRUNEAddress(),
			Address: GetRandomETHAddress(),
			Signer:  GetRandomBech32Addr(),
			Error:   "asset is no valid export asset",
		},
		{
			Comment: "target asset no valid export asset",
			TxId:    GetRandomTxHash(),
			Amount:  cosmos.NewUint(common.One),
			Source:  common.RUJI,
			Target:  common.BTCAsset,
			From:    GetRandomRUNEAddress(),
			To:      GetRandomRUNEAddress(),
			Address: GetRandomETHAddress(),
			Signer:  GetRandomBech32Addr(),
			Error:   "asset is no valid export asset",
		},
		{
			Comment: "target asset wrong export asset",
			TxId:    GetRandomTxHash(),
			Amount:  cosmos.NewUint(common.One),
			Source:  common.RUJI,
			Target:  s.mustNewAsset("ETH.VULT"),
			From:    GetRandomRUNEAddress(),
			To:      GetRandomRUNEAddress(),
			Address: GetRandomETHAddress(),
			Signer:  GetRandomBech32Addr(),
			Error:   "asset is no valid export asset",
		},
		{
			Comment: "target asset is no L1 asset",
			TxId:    GetRandomTxHash(),
			Amount:  cosmos.NewUint(common.One),
			Source:  common.RUJI,
			Target:  s.mustNewAsset("ETH/RUJI"),
			From:    GetRandomRUNEAddress(),
			To:      GetRandomRUNEAddress(),
			Address: GetRandomETHAddress(),
			Signer:  GetRandomBech32Addr(),
			Error:   "asset is no valid export asset",
		},
		{
			Comment: "empty address",
			TxId:    GetRandomTxHash(),
			Amount:  cosmos.NewUint(common.One),
			Source:  common.RUJI,
			Target:  s.mustNewAsset("ETH.RUJI"),
			From:    GetRandomRUNEAddress(),
			To:      GetRandomRUNEAddress(),
			Address: common.NoAddress,
			Signer:  GetRandomBech32Addr(),
			Error:   "address cannot be empty",
		},
		{
			Comment: "address doesn't match chain",
			TxId:    GetRandomTxHash(),
			Amount:  cosmos.NewUint(common.One),
			Source:  common.RUJI,
			Target:  s.mustNewAsset("ETH.RUJI"),
			From:    GetRandomRUNEAddress(),
			To:      GetRandomRUNEAddress(),
			Address: GetRandomBTCAddress(),
			Signer:  GetRandomBech32Addr(),
			Error:   "address does not match chain",
		},
		{
			Comment: "empty signer",
			TxId:    GetRandomTxHash(),
			Amount:  cosmos.NewUint(common.One),
			Source:  common.RUJI,
			Target:  s.mustNewAsset("ETH.RUJI"),
			From:    GetRandomRUNEAddress(),
			To:      GetRandomRUNEAddress(),
			Address: GetRandomETHAddress(),
			Signer:  nil,
			Error:   ".* invalid address",
		},
	}

	for _, tc := range testCases {
		tx := common.NewTx(
			tc.TxId,
			tc.From,
			tc.To,
			common.Coins{common.NewCoin(tc.Source, tc.Amount)},
			common.Gas{common.NewCoin(tc.Source, cosmos.NewUint(1))},
			fmt.Sprintf("ISSUE:%s", tc.Address),
		)

		msg := NewMsgExportAssetIssue(tx, tc.Target, tc.Amount, tc.Address, tc.Signer)
		comment := Commentf(tc.Comment)
		if tc.Error == "" {
			c.Assert(msg.ValidateBasic(), IsNil, comment)
		} else {
			c.Assert(msg.ValidateBasic(), ErrorMatches, tc.Error+".*", comment)
		}
	}
}

func (s *MsgExportAssetIssueSuite) mustNewAsset(str string) common.Asset {
	asset, err := common.NewAsset(str)
	if err != nil {
		panic(err)
	}
	return asset
}
