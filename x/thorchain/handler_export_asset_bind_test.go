package thorchain

import (
	"fmt"

	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

type HandlerExportAssetBindSuite struct{}

var _ = Suite(&HandlerExportAssetBindSuite{})

func (HandlerExportAssetBindSuite) TestBindAsset(c *C) {
	thorVULT, err := common.NewAsset("THOR.VULT")
	c.Assert(err, IsNil)
	baseVULT, err := common.NewAsset("BASE.VULT")
	c.Assert(err, IsNil)

	gaiaRUJI, err := common.NewAsset("GAIA.RUJI")
	c.Assert(err, IsNil)
	tronRUJI, err := common.NewAsset("TRON.RUJI")
	c.Assert(err, IsNil)
	ethRUJI, err := common.NewAsset("ETH.RUJI")
	c.Assert(err, IsNil)

	testCases := []struct {
		Asset  common.Asset
		Amount int64
		Fail   bool
	}{
		{
			// no valid export
			Asset:  common.BTCAsset,
			Amount: 1000,
			Fail:   true,
		},
		{
			// valid export asset
			Asset:  common.RUJI,
			Amount: 2000,
		},
		{
			// valid external asset
			Asset:  gaiaRUJI,
			Amount: 1000,
		},
		{
			// valid external asset
			Asset:  ethRUJI,
			Amount: 2000,
		},
		{
			// invalid external asset
			Asset:  tronRUJI,
			Amount: 1000,
			Fail:   true,
		},
		{
			// zero amount
			Asset:  thorVULT,
			Amount: 0,
			Fail:   true,
		},
		{
			// valid export asset
			Asset:  baseVULT,
			Amount: 3000,
		},
	}

	ctx, mgr := setupManagerForTest(c)

	handler := NewExportAssetBindHandler(mgr)

	for _, tc := range testCases {
		tx := GetRandomTx()
		tx.Chain = tc.Asset.Chain
		msg := NewMsgExportAssetBind(
			tx,
			tc.Asset,
			cosmos.NewUint(uint64(tc.Amount)),
			GetRandomBech32Addr(),
		)
		_, err = handler.Run(ctx, msg)

		fmt.Println(err, msg)

		if tc.Fail {
			c.Assert(err, NotNil)
		} else {
			c.Assert(err, IsNil)
		}
	}

	n := 0
	iter := mgr.Keeper().GetAssetExportIterator(ctx, common.RUJI)
	for ; iter.Valid(); iter.Next() {
		var export types.AssetExport
		err = mgr.cdc.Unmarshal(iter.Value(), &export)
		c.Assert(err, IsNil)
		fmt.Println(export.String())
		switch export.Asset {
		case gaiaRUJI:
			c.Assert(export.AmountExternal.String(), Equals, "1000")
		case ethRUJI:
			c.Assert(export.AmountExternal.String(), Equals, "2000")
		default:
			c.Fail()
		}
		n++
	}
	c.Assert(n, Equals, 2)
}
