package types

type Provider interface {
	Start()
	GetTickers() (map[string]Ticker, error)

	GetAvailablePairs() ([]string, error)
	ToProviderSymbol(string, string) string

	GetStats() (map[string]uint64, error)
}

type PollingProvider interface {
	Provider
	Poll()
}

type WebsocketProvider interface {
	Provider
	Poll()
	HandleWsMessage([]byte) error
	Subscribe() error
}
