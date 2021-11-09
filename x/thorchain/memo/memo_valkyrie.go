package thorchain

import (
	"errors"
	"fmt"
	"strconv"
)

type ValkyrieFundMemo struct {
	MemoBase
	BlockHeight int64
}

func (m ValkyrieFundMemo) String() string {
	return fmt.Sprintf("VALKYRIE+:%d", m.BlockHeight)
}

func (m ValkyrieFundMemo) GetBlockHeight() int64 {
	return m.BlockHeight
}

func NewValkyrieFundMemo(blockHeight int64) ValkyrieFundMemo {
	return ValkyrieFundMemo{
		MemoBase:    MemoBase{TxType: TxYggdrasilFund},
		BlockHeight: blockHeight,
	}
}

type ValkyrieReturnMemo struct {
	MemoBase
	BlockHeight int64
}

func (m ValkyrieReturnMemo) String() string {
	return fmt.Sprintf("VALKYRIE-:%d", m.BlockHeight)
}

func (m ValkyrieReturnMemo) GetBlockHeight() int64 {
	return m.BlockHeight
}

func NewValkyrieReturnMemo(blockHeight int64) ValkyrieReturnMemo {
	return ValkyrieReturnMemo{
		MemoBase:    MemoBase{TxType: TxYggdrasilReturn},
		BlockHeight: blockHeight,
	}
}

func ParseValkyrieFundMemo(parts []string) (ValkyrieFundMemo, error) {
	if len(parts) < 2 {
		return ValkyrieFundMemo{}, errors.New("not enough parameters")
	}
	blockHeight, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return ValkyrieFundMemo{}, fmt.Errorf("fail to convert (%s) to a valid block height: %w", parts[1], err)
	}
	return NewValkyrieFundMemo(blockHeight), nil
}

func ParseValkyrieReturnMemo(parts []string) (ValkyrieReturnMemo, error) {
	if len(parts) < 2 {
		return ValkyrieReturnMemo{}, errors.New("not enough parameters")
	}
	blockHeight, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return ValkyrieReturnMemo{}, fmt.Errorf("fail to convert (%s) to a valid block height: %w", parts[1], err)
	}
	return NewValkyrieReturnMemo(blockHeight), nil
}
