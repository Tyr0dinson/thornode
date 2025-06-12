package types

import "errors"

func (m *OraclePrice) Valid() error {
	if m.Symbol == "" {
		return errors.New("empty symbol")
	}
	if m.Amount == "" {
		return errors.New("empty amount")
	}
	return nil
}
