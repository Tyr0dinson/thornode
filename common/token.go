package common

import (
	"fmt"
	"strings"
)

// Token is a struct to represent the token
type Token struct {
	Address  string `json:"address"`
	Symbol   string `json:"symbol"`
	Name     string `json:"name"`
	Decimals int    `json:"decimals"`
}

// Asset returns the Asset representation of the token.
func (t Token) Asset(chain Chain) Asset {
	return Asset{
		Chain:  chain,
		Ticker: Ticker(t.Symbol),
		Symbol: Symbol(strings.ToUpper(fmt.Sprintf("%s-%s", t.Symbol, t.Address))),
	}
}
