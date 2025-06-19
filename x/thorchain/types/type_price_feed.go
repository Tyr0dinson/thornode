package types

import "errors"

func (m *PriceFeed) Valid() error {
	if m.Node.Empty() {
		return errors.New("node is empty")
	}
	if m.Height <= 0 {
		return errors.New("height <= 0")
	}
	if len(m.Rates) == 0 {
		return errors.New("rates is empty")
	}
	return nil
}
