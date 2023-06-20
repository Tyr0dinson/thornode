package thorchain

import (
	"fmt"

	"gitlab.com/thorchain/thornode/common"
	"gitlab.com/thorchain/thornode/common/cosmos"

	"github.com/blang/semver"
)

type ColdBondMemo struct {
	MemoBase
	Operator               cosmos.AccAddress
	Asset                  common.Asset
	NodeOperatorFee        int64
	CollateralizationRatio int64
	PubKey                 common.PubKey
}

func (m ColdBondMemo) GetAccAddress() cosmos.AccAddress { return m.Operator }

func NewColdBondMemo(addr cosmos.AccAddress, asset common.Asset, operatorFee, cr int64) ColdBondMemo {
	return ColdBondMemo{
		MemoBase:               MemoBase{TxType: TxColdBond, Asset: asset},
		Operator:               addr,
		NodeOperatorFee:        operatorFee,
		CollateralizationRatio: cr,
	}
}

func ParseColdBondMemo(version semver.Version, parts []string, asset common.Asset) (ColdBondMemo, error) {
	switch {
	case version.GTE(semver.MustParse("1.115.0")):
		return ParseColdBondMemoV115(parts, asset)
	default:
		return ColdBondMemo{}, fmt.Errorf("invalid version(%s)", version.String())
	}
}

func ParseColdBondMemoV115(parts []string, asset common.Asset) (ColdBondMemo, error) {
	if len(parts) < 2 {
		return ColdBondMemo{}, fmt.Errorf("not enough parameters")
	}
	operator, err := cosmos.AccAddressFromBech32(GetPart(parts, 2))
	if err != nil {
		return ColdBondMemo{}, fmt.Errorf("%s is an invalid thorchain address: %w", parts[2], err)
	}
	operatorFee, ok, err := getPartInt64(parts, 3)
	if err != nil {
		return ColdBondMemo{}, fmt.Errorf("bad operator fee (%s)", GetPart(parts, 3))
	}
	if !ok {
		operatorFee = -1
	}
	cr, _, err := getPartInt64(parts, 4)
	if err != nil {
		return ColdBondMemo{}, fmt.Errorf("bad collateralization ratio (%s)", GetPart(parts, 4))
	}
	return NewColdBondMemo(operator, asset, operatorFee, cr), nil
}

type ColdUnBondMemo struct {
	MemoBase
	Operator cosmos.AccAddress
	Amount   cosmos.Uint
}

func (m ColdUnBondMemo) GetAccAddress() cosmos.AccAddress { return m.Operator }

func NewColdUnBondMemo(addr cosmos.AccAddress, asset common.Asset, amount cosmos.Uint) ColdUnBondMemo {
	return ColdUnBondMemo{
		MemoBase: MemoBase{TxType: TxColdUnBond, Asset: asset},
		Operator: addr,
		Amount:   amount,
	}
}

func ParseColdUnBondMemo(version semver.Version, parts []string, asset common.Asset) (ColdUnBondMemo, error) {
	switch {
	case version.GTE(semver.MustParse("1.115.0")):
		return ParseColdUnBondMemoV115(parts, asset)
	default:
		return ColdUnBondMemo{}, fmt.Errorf("invalid version(%s)", version.String())
	}
}

func ParseColdUnBondMemoV115(parts []string, asset common.Asset) (ColdUnBondMemo, error) {
	if len(parts) < 2 {
		return ColdUnBondMemo{}, fmt.Errorf("not enough parameters")
	}
	operator, err := cosmos.AccAddressFromBech32(GetPart(parts, 2))
	if err != nil {
		return ColdUnBondMemo{}, fmt.Errorf("%s is an invalid thorchain address: %w", parts[1], err)
	}
	amount, err := parseTradeTarget(GetPart(parts, 3))
	if err != nil {
		return ColdUnBondMemo{}, fmt.Errorf("bad amount (%s)", GetPart(parts, 3))
	}
	return NewColdUnBondMemo(operator, asset, amount), nil
}
