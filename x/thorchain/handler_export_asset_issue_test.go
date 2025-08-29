package thorchain

import (
	"fmt"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	. "gopkg.in/check.v1"
)

type HandlerExportAssetIssueSuite struct{}

var _ = Suite(&HandlerExportAssetIssueSuite{})

func (HandlerExportAssetIssueSuite) TestIssueAsset(c *C) {}

func (HandlerExportAssetIssueSuite) TestIssueAssetPools(c *C) {
	ctx, mgr := setupManagerForTest(c)
	handler := NewExportAssetIssueHandler(mgr)

	asset, err := common.NewAsset("ETH.RUJI")
	c.Assert(err, IsNil)

	amount := cosmos.NewUint(common.One)
	address := GetRandomETHAddress()

	tx := common.NewTx(
		GetRandomTxHash(),
		GetRandomRUNEAddress(),
		GetRandomRUNEAddress(),
		common.Coins{common.NewCoin(common.RUJI, amount)},
		common.Gas{common.NewCoin(common.RuneNative, cosmos.NewUint(1))},
		fmt.Sprintf("ISSUE:%s:%s", asset, address),
	)

	msg := NewMsgExportAssetIssue(
		tx,
		asset,
		cosmos.NewUint(common.One),
		address,
		GetRandomBech32Addr(),
	)

	_, err = handler.Run(ctx, msg)
	c.Assert(err, NotNil)
	fmt.Println(err)
}
