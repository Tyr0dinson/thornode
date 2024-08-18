package common

import (
	"errors"
	"regexp"
	"strings"
)

var isAlphaNumeric = regexp.MustCompile(`^[A-Za-z0-9-._]+$`).MatchString

// Symbol represent an asset
type Symbol string

// NewSymbol parse the input as symbol
func NewSymbol(input string) (Symbol, error) {
	if !isAlphaNumeric(input) {
		return "", errors.New("invalid symbol")
	}

	return Symbol(strings.ToUpper(input)), nil
}

func (s Symbol) Valid() error {
	if !isAlphaNumeric(s.String()) {
		return errors.New("symbol must be alphanumeric")
	}
	return nil
}

// Ticker code reference kept only for ticker-equal fuzzy matching.
func (s Symbol) Ticker() string {
	parts := strings.Split(s.String(), "-")
	return strings.ToUpper(parts[0])
}

func (s Symbol) ID() string {
	parts := strings.Split(s.String(), "-")
	if len(parts) < 2 {
		return "" // No ID.
	}
	return strings.ToUpper(parts[1])
}

// Equals check whether two symbol are the same
func (s Symbol) Equals(s2 Symbol) bool {
	return strings.EqualFold(s.String(), s2.String())
}

// IsEmpty return true when symbol is just empty string
func (s Symbol) IsEmpty() bool {
	return strings.TrimSpace(s.String()) == ""
}

// String implement fmt.Stringer
func (s Symbol) String() string {
	// convert it to upper case again just in case for example made with Symbol("rune")
	return strings.ToUpper(string(s))
}

// IsMiniToken is to determine whether it is a mini token on binance chain
func (s Symbol) IsMiniToken() bool {
	parts := strings.Split(s.String(), "-")
	if len(parts) == 1 {
		return false
	}
	return len(parts[1]) == 4 && strings.HasSuffix(s.String(), "M")
}
