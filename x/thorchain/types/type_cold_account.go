package types

import (
	"fmt"
	"strings"

	"gitlab.com/thorchain/thornode/common"
	"gitlab.com/thorchain/thornode/common/cosmos"
)

func NewColdAccount(operator cosmos.AccAddress, asset common.Asset, pk common.PubKey) ColdAccount {
	return ColdAccount{
		Operator: operator,
		Asset:    asset,
		PubKey:   pk,
		Status:   ColdStatus_StandbyColdStatus,
		Bond:     cosmos.ZeroUint(),
	}
}

func (a *ColdAccount) Key() string {
	return fmt.Sprintf("%s/%s", a.Operator, a.Asset.String())
}

func (a ColdAccount) IsPositiveInterest() bool {
	return a.Interest == ColdInterest_ColdPositiveInterest
}

func (a ColdAccount) IsNegativeInterest() bool {
	return a.Interest == ColdInterest_ColdNegativeInterest
}

func (a ColdAccount) GetBondModule() string {
	switch a.Interest {
	case ColdInterest_ColdPositiveInterest:
		return ColdBondPositiveInterestName
	case ColdInterest_ColdNegativeInterest:
		return ColdBondNegativeInterestName
	default:
		return ColdBondPositiveInterestName
	}
}

// IsEmpty decide whether ColdAccount is empty
func (a *ColdAccount) IsEmpty() bool {
	return a.Status == ColdStatus_UnknownColdStatus
}

func (a *ColdAccount) Valid() error {
	// TODO
	return fmt.Errorf("not implemented")
}

// UpdateStatus change the status of node account, in the mean time update StatusSince field
func (a *ColdAccount) UpdateStatus(status ColdStatus, height int64) {
	if a.Status == status {
		return
	}
	a.Status = status
	a.StatusSince = height
}

// String implement fmt.Stringer interface
func (a *ColdAccount) String() string {
	sb := strings.Builder{}
	sb.WriteString("operator:" + a.Operator.String() + "\n")
	sb.WriteString("status:" + a.Status.String() + "\n")
	sb.WriteString("pubkey:" + a.PubKey.String() + "\n")
	sb.WriteString("bond:" + a.Bond.String() + "\n")
	return sb.String()
}

// ColdAccounts just a list of ColdAccount
type ColdAccounts []ColdAccount
