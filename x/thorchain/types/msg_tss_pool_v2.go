package types

import (
	"fmt"

	"gitlab.com/thorchain/thornode/common"
	"gitlab.com/thorchain/thornode/common/cosmos"
)

// NewMsgTssPool is a constructor function for MsgTssPool
func NewMsgTssPoolV2(pks []string, poolpk common.PubKey, keysharesBackup []byte, keygenType KeygenType, height int64, bl Blame, chains []string, signer cosmos.AccAddress, keygenTime int64, poolPubKeyEddsa common.PubKey) (*MsgTssPool, error) {
	id, err := getTssID(pks, poolpk, height, bl)
	if err != nil {
		return nil, fmt.Errorf("fail to get tss id: %w", err)
	}
	return &MsgTssPool{
		ID:              id,
		PubKeys:         pks,
		PoolPubKey:      poolpk,
		Height:          height,
		KeygenType:      keygenType,
		Blame:           bl,
		Chains:          chains,
		Signer:          signer,
		KeygenTime:      keygenTime,
		KeysharesBackup: keysharesBackup,
		PoolPubKeyEddsa: poolPubKeyEddsa,
	}, nil
}
