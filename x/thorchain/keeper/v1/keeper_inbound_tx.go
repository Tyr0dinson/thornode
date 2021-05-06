// Package keeperv1
// InboundTxCount doesn't need to provide an iterator , as it is not necessary to iterate those information
// if the network hard fork , it is fine to lose record
package keeperv1

import (
	"fmt"

	"gitlab.com/thorchain/thornode/common"
	"gitlab.com/thorchain/thornode/common/cosmos"
)

// IncInboundTxCount increase the number of inbound transaction per chain
func (k KVStore) IncInboundTxCount(ctx cosmos.Context, pubKey common.PubKey, chain common.Chain) error {
	record, err := k.GetInboundTxCount(ctx, pubKey, chain)
	if err != nil {
		return fmt.Errorf("fail to get current inbound tx count: %w", err)
	}
	k.setInboundTxCount(ctx, pubKey, chain, record+1)
	return nil
}

// GetInboundTxCount return the number of inbound tx for the given vault pubkey and chain
func (k KVStore) GetInboundTxCount(ctx cosmos.Context, pubKey common.PubKey, chain common.Chain) (int64, error) {
	record := int64(0)
	_, err := k.getInt64(ctx, k.GetKey(ctx, prefixInboundTxCount, fmt.Sprintf("%s-%s", pubKey, chain.String())), &record)
	return record, err
}

func (k KVStore) setInboundTxCount(ctx cosmos.Context, pubKey common.PubKey, chain common.Chain, count int64) {
	if count < 0 {
		count = 0
	}
	k.setInt64(ctx, k.GetKey(ctx, prefixInboundTxCount, fmt.Sprintf("%s-%s", pubKey, chain.String())), count)
}
