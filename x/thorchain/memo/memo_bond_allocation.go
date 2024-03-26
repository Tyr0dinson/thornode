package thorchain

import (
	"fmt"

	"github.com/blang/semver"
	"gitlab.com/thorchain/thornode/common/cosmos"
)

// example: "<thorname>:<type>:<amount>"

type ManageBondAllocationMemo struct {
	MemoBase
	THORName string
	Amount   cosmos.Uint
	TypeInt  int32
}

func (m ManageBondAllocationMemo) GetTHORName() string    { return m.THORName }
func (m ManageBondAllocationMemo) GetAmount() cosmos.Uint { return m.Amount }
func (m ManageBondAllocationMemo) GetTypeInt() int32      { return m.TypeInt }

func NewManageBondAllocationMemo(name string, amount cosmos.Uint, type_ int32) ManageBondAllocationMemo {
	return ManageBondAllocationMemo{
		MemoBase: MemoBase{TxType: TxBondAllocation},
		THORName: name,
		Amount:   amount,
		TypeInt:  type_,
	}
}

func (p *parser) ParseManageBondAllocationMemo() (ManageBondAllocationMemo, error) {
	switch {
	case p.version.GTE(semver.MustParse("1.130.0")):
		return p.ParseManageBondAllocationMemoV130()
	default:
		return ManageBondAllocationMemo{}, fmt.Errorf("version %s is not supported", p.version)
	}
}

func (p *parser) ParseManageBondAllocationMemoV130() (ManageBondAllocationMemo, error) {
	name := p.get(2)
	type_ := p.getInt32(3, true, 0)
	amount := p.getUintWithScientificNotation(3, false, 0)
	return NewManageBondAllocationMemo(name, amount, type_), p.Error()
}
