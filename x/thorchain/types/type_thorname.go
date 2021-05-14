package types

import (
	"errors"
	"fmt"
	"strings"

	"gitlab.com/thorchain/thornode/common"
)

// NewTHORName create a new instance of network fee
func NewTHORName(name string, chain common.Chain, addr common.Address, exp int64) THORName {
	return THORName{
		Name:              name,
		Chain:             chain,
		Address:           addr,
		ExpireBlockHeight: exp,
	}
}

// Valid - check whether THORName struct represent valid information
func (m *THORName) Valid() error {
	if len(m.Name) == 0 {
		return errors.New("name can't be empty")
	}
	if m.Chain.IsEmpty() {
		return errors.New("chain can't be empty")
	}
	if m.Address.IsEmpty() {
		return errors.New("address cannot be empty")
	}
	return nil
}

func (m *THORName) Key() string {
	return fmt.Sprintf("%s/%s", strings.ToLower(m.Name), strings.ToLower(m.Chain.String()))
}
