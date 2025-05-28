package types

type Provider interface {
	Start()
	GetTickers() (map[string]Ticker, error)

	Poll()
	GetAvailablePairs() ([]string, error)
	ToProviderSymbol(string, string) string
}
