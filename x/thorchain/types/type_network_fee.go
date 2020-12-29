package types

import (
	"errors"

	"gitlab.com/thorchain/thornode/common"
)

// NewNetworkFee create a new instance of network fee
func NewNetworkFee(chain common.Chain, transactionSize, transactionFeeRate uint64) NetworkFee {
	return NetworkFee{
		Chain:              chain,
		TransactionSize:    transactionSize,
		TransactionFeeRate: transactionFeeRate,
	}
}

func (f NetworkFee) Valid() error {
	if f.Chain.IsEmpty() {
		return errors.New("chain can't be empty")
	}
	if f.TransactionSize <= 0 {
		return errors.New("transaction size can't be negative")
	}
	if f.TransactionFeeRate <= 0 {
		return errors.New("transaction fee rate can't be zero")
	}
	return nil
}
