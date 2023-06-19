package types

import (
	"fmt"
	"strings"

	"gitlab.com/thorchain/thornode/common"
	"gitlab.com/thorchain/thornode/common/cosmos"
)

func NewColdAccount(owner cosmos.AccAddress, asset common.Asset, addr common.Address) ColdAccount {
	return ColdAccount{
		Owner:   owner,
		Asset:   asset,
		Address: addr,
		Status:  ColdStatus_StandbyColdStatus,
		Bond:    cosmos.ZeroUint(),
	}
}

func (a *ColdAccount) Key() string {
	return fmt.Sprintf("%s/%s", a.Owner, a.Asset.String())
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
	sb.WriteString("owner:" + a.Owner.String() + "\n")
	sb.WriteString("status:" + a.Status.String() + "\n")
	sb.WriteString("acct pubkeys:" + a.Address.String() + "\n")
	sb.WriteString("bond:" + a.Bond.String() + "\n")
	return sb.String()
}

// ColdAccounts just a list of ColdAccount
type ColdAccounts []ColdAccount
