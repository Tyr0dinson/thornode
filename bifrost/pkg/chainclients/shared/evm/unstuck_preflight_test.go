package evm

import (
	"fmt"

	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	stypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	ttypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
	. "gopkg.in/check.v1"
)

type mockUnstuckKeysignBridge struct {
	thorclient.ThorchainBridge
	txOut stypes.TxOut
	err   error
}

func (m mockUnstuckKeysignBridge) GetKeysign(int64, string) (stypes.TxOut, error) {
	if m.err != nil {
		return stypes.TxOut{}, m.err
	}
	return m.txOut, nil
}

func (s *UtilsTestSuite) TestIsEVMUnstuckTxOutSignable(c *C) {
	tx := evmUnstuckPreflightTxOutItem()
	bridge := mockUnstuckKeysignBridge{
		txOut: stypes.TxOut{
			Height: tx.Height,
			TxArray: []stypes.TxArrayItem{
				evmUnstuckPreflightTxArrayItem(tx, common.TxID("")),
			},
		},
	}

	ok, reason, err := IsEVMUnstuckTxOutSignable(bridge, &tx)
	c.Assert(err, IsNil)
	c.Assert(ok, Equals, true)
	c.Assert(reason, Equals, "")
}

func (s *UtilsTestSuite) TestIsEVMUnstuckTxOutSignableRejectsMissingTxOutItem(c *C) {
	ok, reason, err := IsEVMUnstuckTxOutSignable(mockUnstuckKeysignBridge{}, nil)
	c.Assert(err, IsNil)
	c.Assert(ok, Equals, false)
	c.Assert(reason, Equals, "missing txout item")
}

func (s *UtilsTestSuite) TestIsEVMUnstuckTxOutSignableRejectsOutHash(c *C) {
	tx := evmUnstuckPreflightTxOutItem()
	bridge := mockUnstuckKeysignBridge{
		txOut: stypes.TxOut{
			Height: tx.Height,
			TxArray: []stypes.TxArrayItem{
				evmUnstuckPreflightTxArrayItem(tx, ttypes.GetRandomTxHash()),
			},
		},
	}

	ok, reason, err := IsEVMUnstuckTxOutSignable(bridge, &tx)
	c.Assert(err, IsNil)
	c.Assert(ok, Equals, false)
	c.Assert(reason, Equals, "scheduled txout already has an out hash")
}

func (s *UtilsTestSuite) TestIsEVMUnstuckTxOutSignableRejectsMissingTxOut(c *C) {
	tx := evmUnstuckPreflightTxOutItem()
	other := tx
	other.InHash = ttypes.GetRandomTxHash()
	bridge := mockUnstuckKeysignBridge{
		txOut: stypes.TxOut{
			Height: tx.Height,
			TxArray: []stypes.TxArrayItem{
				evmUnstuckPreflightTxArrayItem(other, common.TxID("")),
			},
		},
	}

	ok, reason, err := IsEVMUnstuckTxOutSignable(bridge, &tx)
	c.Assert(err, IsNil)
	c.Assert(ok, Equals, false)
	c.Assert(reason, Equals, "txout was not found in scheduled keysign")
}

func (s *UtilsTestSuite) TestIsEVMUnstuckTxOutSignableRetriesKeysignError(c *C) {
	tx := evmUnstuckPreflightTxOutItem()
	bridge := mockUnstuckKeysignBridge{err: fmt.Errorf("bridge unavailable")}

	ok, reason, err := IsEVMUnstuckTxOutSignable(bridge, &tx)
	c.Assert(err, NotNil)
	c.Assert(ok, Equals, false)
	c.Assert(reason, Equals, "")
}

func evmUnstuckPreflightTxOutItem() stypes.TxOutItem {
	pubKey := ttypes.GetRandomPubKey()
	to, _ := pubKey.GetAddress(common.ETHChain)
	inHash := ttypes.GetRandomTxHash()

	return stypes.TxOutItem{
		Chain:       common.ETHChain,
		ToAddress:   to,
		VaultPubKey: pubKey,
		Coins: common.Coins{
			common.NewCoin(common.ETHAsset, cosmos.NewUint(100000000)),
		},
		Memo:    "OUT:" + inHash.String(),
		MaxGas:  common.Gas{common.NewCoin(common.ETHAsset, cosmos.NewUint(100000))},
		GasRate: 5,
		InHash:  inHash,
		Height:  800,
	}
}

func evmUnstuckPreflightTxArrayItem(tx stypes.TxOutItem, outHash common.TxID) stypes.TxArrayItem {
	return stypes.TxArrayItem{
		Chain:                 tx.Chain,
		ToAddress:             tx.ToAddress,
		VaultPubKey:           tx.VaultPubKey,
		Coin:                  tx.Coins[0],
		Memo:                  tx.Memo,
		OriginalMemo:          tx.OriginalMemo,
		MaxGas:                tx.MaxGas,
		GasRate:               tx.GasRate,
		InHash:                tx.InHash,
		OutHash:               outHash,
		Aggregator:            tx.Aggregator,
		AggregatorTargetAsset: tx.AggregatorTargetAsset,
		AggregatorTargetLimit: tx.AggregatorTargetLimit,
		VaultPubKeyEddsa:      tx.VaultPubKeyEddsa,
		VaultPubKeyFrost:      tx.VaultPubKeyFrost,
	}
}
