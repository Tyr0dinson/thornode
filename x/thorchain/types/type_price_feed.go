package types

import "errors"

func (m *PriceFeed) Valid() error {
	if len(m.Node) == 0 {
		return errors.New("node is empty")
	}
	if m.Height <= 0 {
		return errors.New("height <= 0")
	}
	if m.Rates == "" {
		return errors.New("rates is empty")
	}
	return nil
}
