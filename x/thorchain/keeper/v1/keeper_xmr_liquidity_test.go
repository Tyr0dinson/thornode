package keeperv1

import (
	"strings"

	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	kvTypes "gitlab.com/thorchain/thornode/v3/x/thorchain/keeper/types"
)

func (s *KeeperLiquidityProviderSuite) TestXMRLiquidityReceiptPersistenceAndExactIndex(c *C) {
	ctx, k := setupKeeperForTest(c)
	owner := common.Address("4AbCdEfG")
	variant := common.Address(strings.ToUpper(owner.String()))
	txID := GetRandomTxHash()
	receipts := []kvTypes.XMRLiquidityReceipt{{
		TxID: txID, Owner: owner, AssetAddress: owner, AssetAmount: cosmos.NewUint(123),
		RuneAmount: cosmos.ZeroUint(), Units: cosmos.NewUint(17), RuneDepositValue: cosmos.ZeroUint(), AssetDepositValue: cosmos.NewUint(123),
	}}
	c.Assert(k.SetXMRLiquidityReceipts(ctx, txID, receipts), IsNil)
	stored, err := k.GetXMRLiquidityReceipts(ctx, txID)
	c.Assert(err, IsNil)
	c.Check(stored, DeepEquals, receipts)
	c.Assert(k.SetXMRLiquidityTxIDs(ctx, owner, []common.TxID{txID}), IsNil)
	ids, err := k.GetXMRLiquidityTxIDs(ctx, variant)
	c.Assert(err, IsNil)
	c.Check(ids, HasLen, 0)
	ids, err = k.GetXMRLiquidityTxIDs(ctx, owner)
	c.Assert(err, IsNil)
	c.Check(ids, DeepEquals, []common.TxID{txID})
}
